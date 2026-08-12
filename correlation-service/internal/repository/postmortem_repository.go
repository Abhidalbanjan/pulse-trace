package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pulsetrace/shared/models"
)

// PostmortemRepository persists the one editable postmortem per incident
// (Incidents · E1). Every operation is tenant-scoped so a postmortem is only
// ever readable or writable by the incident's own tenant.
type PostmortemRepository struct {
	db *pgxpool.Pool
}

func NewPostmortemRepository(db *pgxpool.Pool) *PostmortemRepository {
	return &PostmortemRepository{db: db}
}

// ErrPostmortemNotFound is returned when no postmortem exists for an incident.
var ErrPostmortemNotFound = errors.New("postmortem not found")

// Get returns the stored postmortem for an incident, or ErrPostmortemNotFound.
func (r *PostmortemRepository) Get(ctx context.Context, tenantID, incidentID string) (*models.IncidentPostmortem, error) {
	var pm models.IncidentPostmortem
	var edited *time.Time
	err := r.db.QueryRow(ctx,
		`SELECT incident_id, content, model, generated_at, edited_at
		 FROM incident_postmortems WHERE tenant_id = $1 AND incident_id = $2`,
		tenantID, incidentID,
	).Scan(&pm.IncidentID, &pm.Content, &pm.Model, &pm.GeneratedAt, &edited)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPostmortemNotFound
	}
	if err != nil {
		return nil, err
	}
	pm.EditedAt = edited
	return &pm, nil
}

// Upsert stores a freshly-generated postmortem, replacing any prior draft for
// the incident (regeneration is intentional). Resets edited_at.
func (r *PostmortemRepository) Upsert(ctx context.Context, tenantID, incidentID, content, model string) (*models.IncidentPostmortem, error) {
	now := time.Now().UTC()
	_, err := r.db.Exec(ctx,
		`INSERT INTO incident_postmortems (incident_id, tenant_id, content, model, generated_at, edited_at)
		 VALUES ($1, $2, $3, $4, $5, NULL)
		 ON CONFLICT (incident_id) DO UPDATE
		   SET content = EXCLUDED.content, model = EXCLUDED.model,
		       generated_at = EXCLUDED.generated_at, edited_at = NULL,
		       tenant_id = EXCLUDED.tenant_id`,
		incidentID, tenantID, content, model, now,
	)
	if err != nil {
		return nil, err
	}
	return &models.IncidentPostmortem{IncidentID: incidentID, Content: content, Model: model, GeneratedAt: now}, nil
}

// SaveEdit persists a human edit to an existing postmortem, stamping edited_at.
// Tenant-scoped: only affects the caller tenant's row. Returns
// ErrPostmortemNotFound if there is nothing to edit.
func (r *PostmortemRepository) SaveEdit(ctx context.Context, tenantID, incidentID, content string) (*models.IncidentPostmortem, error) {
	now := time.Now().UTC()
	tag, err := r.db.Exec(ctx,
		`UPDATE incident_postmortems SET content = $1, edited_at = $2
		 WHERE tenant_id = $3 AND incident_id = $4`,
		content, now, tenantID, incidentID,
	)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrPostmortemNotFound
	}
	return r.Get(ctx, tenantID, incidentID)
}
