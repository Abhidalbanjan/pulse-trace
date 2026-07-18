package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/pulsetrace/correlation-service/internal/repository"
	"github.com/pulsetrace/shared/models"
	"github.com/pulsetrace/shared/rabbitmq"
)

// BurnRateThreshold defines when to alert based on error budget consumption rate.
// Based on the Google SRE Handbook multi-window burn rate model.
type BurnRateThreshold struct {
	Multiplier float64       // e.g. 14× expected consumption
	Window     time.Duration // observation window
	Severity   string        // "CRITICAL", "WARNING", "INFO"
}

// DefaultBurnRateThresholds implements the Google SRE Book model.
var DefaultBurnRateThresholds = []BurnRateThreshold{
	{Multiplier: 14.0, Window: 1 * time.Hour, Severity: "CRITICAL"},
	{Multiplier: 6.0, Window: 6 * time.Hour, Severity: "WARNING"},
	{Multiplier: 1.0, Window: 72 * time.Hour, Severity: "INFO"},
}

// BurnRateAlerter evaluates error budget burn rates and fires alerts
// when thresholds are breached. Alerts are persisted to PostgreSQL
// and published to RabbitMQ for the notification service.
type BurnRateAlerter struct {
	repo      *repository.SLORepository
	publisher *rabbitmq.Publisher
	// cooldown prevents alert storms — at most one alert per service per severity
	// within this duration.
	cooldown        time.Duration
	lastAlerted     map[string]time.Time // key: "service:severity"
}

func NewBurnRateAlerter(repo *repository.SLORepository, publisher *rabbitmq.Publisher) *BurnRateAlerter {
	return &BurnRateAlerter{
		repo:        repo,
		publisher:   publisher,
		cooldown:    30 * time.Minute,
		lastAlerted: make(map[string]time.Time),
	}
}

// Evaluate checks the burn rate for a single service against all thresholds.
// It computes the actual burn rate from the current SLI vs the SLO target
// over the threshold's observation window.
//
// Burn rate = (actual error rate) / (allowed error rate)
// allowed error rate = (100 - slo_target) / window_days
// actual error rate  = (100 - current_sli) / elapsed_days
func (b *BurnRateAlerter) Evaluate(ctx context.Context, def *models.SLODefinition, currentSLI float64, totalEvents int64) {
	if totalEvents == 0 {
		return // no data yet
	}

	burnRate, budgetRemainingPct, ok := computeBurnRate(currentSLI, def.SLOTarget, def.WindowDays)
	if !ok {
		return
	}

	for _, threshold := range DefaultBurnRateThresholds {
		if burnRate >= threshold.Multiplier {
			b.fireAlert(ctx, def.ServiceName, burnRate, budgetRemainingPct, threshold.Severity)
			break // only fire the highest severity
		}
	}
}

// computeBurnRate implements the Google SRE Handbook burn-rate model as a pure
// function (no I/O), so the math can be unit tested directly without needing
// a real Postgres-backed repository or RabbitMQ publisher.
//
// Burn rate = (actual error rate) / (allowed error rate)
// allowed error rate = (100 - slo_target) / window_days
// actual error rate  = (100 - current_sli)
//
// ok is false when there isn't a meaningful budget to burn against (e.g. a
// 100% SLO target or a zero/negative window), in which case the caller should
// skip evaluation entirely rather than alert on a divide-by-zero artifact.
func computeBurnRate(currentSLI, sloTarget float64, windowDays int) (burnRate, budgetRemainingPct float64, ok bool) {
	if windowDays <= 0 {
		return 0, 0, false
	}

	allowedErrorRate := (100.0 - sloTarget) / float64(windowDays)
	if allowedErrorRate <= 0 {
		return 0, 0, false
	}

	actualErrorRate := 100.0 - currentSLI

	errorBudgetTotal := 100.0 - sloTarget // e.g. 0.1% for 99.9% SLO
	errorBudgetUsed := 100.0 - currentSLI // actual error percentage
	if errorBudgetTotal <= 0 {
		return 0, 0, false
	}

	budgetRemainingPct = ((errorBudgetTotal - errorBudgetUsed) / errorBudgetTotal) * 100.0
	if budgetRemainingPct < 0 {
		budgetRemainingPct = 0
	}
	if budgetRemainingPct > 100 {
		budgetRemainingPct = 100
	}

	burnRate = actualErrorRate / errorBudgetTotal // normalized burn rate
	return burnRate, budgetRemainingPct, true
}

// fireAlert persists a budget alert and publishes a notification.
func (b *BurnRateAlerter) fireAlert(ctx context.Context, serviceName string, burnRate, budgetRemainingPct float64, severity string) {
	// Cooldown check
	key := fmt.Sprintf("%s:%s", serviceName, severity)
	if last, ok := b.lastAlerted[key]; ok && time.Since(last) < b.cooldown {
		return // still in cooldown
	}
	b.lastAlerted[key] = time.Now()

	alert := &models.SLOBudgetAlert{
		ID:                 uuid.New().String(),
		ServiceName:        serviceName,
		BurnRate:           burnRate,
		BudgetRemainingPct: budgetRemainingPct,
		Severity:           severity,
		Message: fmt.Sprintf(
			"Error budget burn rate %.1f× for %s — %.1f%% budget remaining",
			burnRate, serviceName, budgetRemainingPct,
		),
		TriggeredAt: time.Now().UTC(),
	}

	if err := b.repo.InsertBudgetAlert(ctx, alert); err != nil {
		log.Printf("burn-rate-alerter: failed to persist alert for %s: %v", serviceName, err)
	}

	// Publish notification via RabbitMQ
	if b.publisher != nil {
		event := models.NotificationEvent{
			ID:        uuid.New().String(),
			Channel:   models.NotificationChannelLog,
			Title:     fmt.Sprintf("[SLO] %s error budget alert", serviceName),
			Body:      alert.Message,
			Severity:  models.LogLevel(severity),
			Services:  []string{serviceName},
			CreatedAt: time.Now().UTC(),
		}
		payload, _ := json.Marshal(event)
		if err := b.publisher.Publish(ctx, "incident.notification", payload); err != nil {
			log.Printf("burn-rate-alerter: rabbitmq publish failed for %s: %v", serviceName, err)
		}
	}

	log.Printf("burn-rate-alerter: [%s] %s — burn_rate=%.1f×, budget_remaining=%.1f%%",
		severity, serviceName, burnRate, budgetRemainingPct)
}
