package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pulsetrace/shared/models"
)

// SilenceRepository persists alert silences / maintenance windows (Alerts · E2).
type SilenceRepository struct {
	db *pgxpool.Pool
}

func NewSilenceRepository(db *pgxpool.Pool) *SilenceRepository {
	return &SilenceRepository{db: db}
}

// Create inserts a silence and returns it with its generated id/created_at.
func (r *SilenceRepository) Create(ctx context.Context, s *models.AlertSilence) (*models.AlertSilence, error) {
	matcherJSON, err := json.Marshal(s.Matcher)
	if err != nil {
		return nil, fmt.Errorf("marshal matcher: %w", err)
	}
	const q = `
		INSERT INTO alert_silences (tenant_id, matcher, starts_at, ends_at, created_by)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''))
		RETURNING id, tenant_id, matcher, starts_at, ends_at, COALESCE(created_by, ''), created_at`
	row := r.db.QueryRow(ctx, q, s.TenantID, matcherJSON, s.StartsAt, s.EndsAt, s.CreatedBy)
	return scanSilence(row)
}

// ListForTenant returns a tenant's silences, most recent first.
func (r *SilenceRepository) ListForTenant(ctx context.Context, tenant string) ([]*models.AlertSilence, error) {
	const q = `
		SELECT id, tenant_id, matcher, starts_at, ends_at, COALESCE(created_by, ''), created_at
		FROM alert_silences WHERE tenant_id = $1 ORDER BY created_at DESC`
	rows, err := r.db.Query(ctx, q, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.AlertSilence
	for rows.Next() {
		s, err := scanSilence(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// ActiveForTenant returns only the silences active at `now` — the set used to
// annotate alerts as silenced.
func (r *SilenceRepository) ActiveForTenant(ctx context.Context, tenant string, now time.Time) ([]*models.AlertSilence, error) {
	const q = `
		SELECT id, tenant_id, matcher, starts_at, ends_at, COALESCE(created_by, ''), created_at
		FROM alert_silences WHERE tenant_id = $1 AND starts_at <= $2 AND ends_at > $2`
	rows, err := r.db.Query(ctx, q, tenant, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.AlertSilence
	for rows.Next() {
		s, err := scanSilence(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// Delete removes a tenant's silence by id. Tenant-scoped so one tenant can't
// delete another's silence.
func (r *SilenceRepository) Delete(ctx context.Context, tenant, id string) error {
	ct, err := r.db.Exec(ctx, `DELETE FROM alert_silences WHERE id = $1 AND tenant_id = $2`, id, tenant)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("silence not found")
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSilence(row rowScanner) (*models.AlertSilence, error) {
	var s models.AlertSilence
	var matcherJSON []byte
	if err := row.Scan(&s.ID, &s.TenantID, &matcherJSON, &s.StartsAt, &s.EndsAt, &s.CreatedBy, &s.CreatedAt); err != nil {
		return nil, err
	}
	if len(matcherJSON) > 0 {
		_ = json.Unmarshal(matcherJSON, &s.Matcher)
	}
	return &s, nil
}
