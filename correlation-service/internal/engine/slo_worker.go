package engine

import (
	"context"
	"log"
	"time"

	"github.com/pulsetrace/correlation-service/internal/repository"
	"github.com/pulsetrace/shared/models"
	"github.com/pulsetrace/shared/rabbitmq"
)

const (
	// sloWorkerInterval is how often the worker computes SLI snapshots.
	sloWorkerInterval = 60 * time.Second

	// snapshotRetentionDays is how long we keep historical snapshots.
	snapshotRetentionDays = 90
)

// SLOWorker is a background goroutine that periodically computes SLI values
// from the log_entries table and stores snapshots. It also evaluates burn
// rate thresholds and fires alerts when budgets are at risk.
//
// Pattern matches the existing AnomalyDetector.Start() convention.
type SLOWorker struct {
	repo    *repository.SLORepository
	alerter *BurnRateAlerter
}

func NewSLOWorker(repo *repository.SLORepository, publisher *rabbitmq.Publisher) *SLOWorker {
	return &SLOWorker{
		repo:    repo,
		alerter: NewBurnRateAlerter(repo, publisher),
	}
}

// Start runs the SLO computation loop until the context is cancelled.
func (w *SLOWorker) Start(ctx context.Context) {
	log.Println("slo-worker: started — computing SLI snapshots every 60s")

	// Run once immediately on startup
	w.tick(ctx)

	ticker := time.NewTicker(sloWorkerInterval)
	defer ticker.Stop()

	// Cleanup old snapshots once an hour
	cleanupTicker := time.NewTicker(1 * time.Hour)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("slo-worker: stopped")
			return
		case <-ticker.C:
			w.tick(ctx)
		case <-cleanupTicker.C:
			w.cleanup(ctx)
		}
	}
}

// tick performs one cycle: fetch all SLO definitions, compute SLI for each,
// store a snapshot, and evaluate burn rates.
func (w *SLOWorker) tick(ctx context.Context) {
	defs, err := w.repo.ListDefinitions(ctx)
	if err != nil {
		log.Printf("slo-worker: failed to list definitions: %v", err)
		return
	}

	if len(defs) == 0 {
		return // no SLOs configured yet
	}

	now := time.Now().UTC()

	for _, def := range defs {
		windowStart := now.AddDate(0, 0, -def.WindowDays)

		total, errors, sli, err := w.repo.ComputeSLI(ctx, def.ServiceName, windowStart, now)
		if err != nil {
			log.Printf("slo-worker: failed to compute SLI for %s: %v", def.ServiceName, err)
			continue
		}

		// Store snapshot
		snap := &models.SLOSnapshot{
			ServiceName: def.ServiceName,
			SLIValue:    sli,
			TotalEvents: total,
			ErrorEvents: errors,
			WindowStart: windowStart,
			WindowEnd:   now,
			SnapshotAt:  now,
		}
		if err := w.repo.InsertSnapshot(ctx, snap); err != nil {
			log.Printf("slo-worker: failed to store snapshot for %s: %v", def.ServiceName, err)
			continue
		}

		// Evaluate burn rate thresholds
		w.alerter.Evaluate(ctx, def, sli, total)

		log.Printf("slo-worker: %s — SLI=%.4f%% (target=%.3f%%) events=%d errors=%d",
			def.ServiceName, sli, def.SLOTarget, total, errors)
	}
}

// cleanup removes old snapshots beyond the retention window.
func (w *SLOWorker) cleanup(ctx context.Context) {
	deleted, err := w.repo.CleanupOldSnapshots(ctx, snapshotRetentionDays)
	if err != nil {
		log.Printf("slo-worker: cleanup failed: %v", err)
		return
	}
	if deleted > 0 {
		log.Printf("slo-worker: cleaned up %d old snapshots", deleted)
	}
}
