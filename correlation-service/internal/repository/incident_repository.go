package repository

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pulsetrace/shared/models"
)

// IncidentRepository handles all PostgreSQL operations for incidents.
type IncidentRepository struct {
	db *pgxpool.Pool
}

func NewIncidentRepository(db *pgxpool.Pool) *IncidentRepository {
	return &IncidentRepository{db: db}
}

// Upsert creates a new incident or updates an existing open one by adding the
// alert to it. The correlation key is (service_name, time_window_bucket).
func (r *IncidentRepository) Upsert(ctx context.Context, incident *models.Incident, alert *models.Alert) (*models.Incident, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Persist the incident row (INSERT … ON CONFLICT DO UPDATE).
	const upsertQ = `
		INSERT INTO incidents (id, title, root_cause, status, severity, alert_count, started_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (id) DO UPDATE
		  SET alert_count = incidents.alert_count + 1,
		      severity    = CASE
		                      WHEN ARRAY_POSITION(ARRAY['DEBUG','INFO','WARNING','ERROR','FATAL'], EXCLUDED.severity)
		                         > ARRAY_POSITION(ARRAY['DEBUG','INFO','WARNING','ERROR','FATAL'], incidents.severity)
		                      THEN EXCLUDED.severity
		                      ELSE incidents.severity
		                    END,
		      updated_at  = NOW()
		RETURNING id, title, root_cause, status, severity, alert_count, started_at, resolved_at, created_at, updated_at
	`
	row := tx.QueryRow(ctx, upsertQ,
		incident.ID, incident.Title, incident.RootCause,
		incident.Status, incident.Severity, incident.AlertCount,
		incident.StartedAt, incident.CreatedAt, incident.UpdatedAt,
	)

	result := &models.Incident{}
	if err := row.Scan(
		&result.ID, &result.Title, &result.RootCause, &result.Status,
		&result.Severity, &result.AlertCount, &result.StartedAt,
		&result.ResolvedAt, &result.CreatedAt, &result.UpdatedAt,
	); err != nil {
		return nil, fmt.Errorf("upsert incident: %w", err)
	}

	// Link the alert to the incident.
	const linkQ = `
		INSERT INTO incident_alerts (incident_id, alert_id, service_name, level, message, triggered_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT DO NOTHING
	`
	if _, err := tx.Exec(ctx, linkQ,
		result.ID, alert.ID, alert.ServiceName, alert.Level, alert.Message, alert.TriggeredAt,
	); err != nil {
		return nil, fmt.Errorf("link alert to incident: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}

	return result, nil
}

// QueryResult holds a page of incidents plus total count.
type QueryResult struct {
	Incidents []*models.Incident
	Total     int64
	Page      int
	PageSize  int
}

// Query fetches incidents with optional filters and pagination.
func (r *IncidentRepository) Query(ctx context.Context, params *models.IncidentQueryParams) (*QueryResult, error) {
	page := params.Page
	if page < 1 {
		page = 1
	}
	pageSize := params.PageSize
	if pageSize < 1 || pageSize > 200 {
		pageSize = 50
	}
	offset := (page - 1) * pageSize

	args := []interface{}{}
	where := "WHERE 1=1"
	idx := 1

	if params.Status != "" {
		where += fmt.Sprintf(" AND status = $%d", idx)
		args = append(args, params.Status)
		idx++
	}
	if params.Severity != "" {
		where += fmt.Sprintf(" AND severity = $%d", idx)
		args = append(args, params.Severity)
		idx++
	}
	if params.From != "" {
		if t, err := time.Parse(time.RFC3339, params.From); err == nil {
			where += fmt.Sprintf(" AND started_at >= $%d", idx)
			args = append(args, t)
			idx++
		}
	}
	if params.To != "" {
		if t, err := time.Parse(time.RFC3339, params.To); err == nil {
			where += fmt.Sprintf(" AND started_at <= $%d", idx)
			args = append(args, t)
			idx++
		}
	}

	var total int64
	if err := r.db.QueryRow(ctx, fmt.Sprintf("SELECT COUNT(*) FROM incidents %s", where), args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count incidents: %w", err)
	}

	dataQ := fmt.Sprintf(`
		SELECT id, title, root_cause, status, severity, alert_count, started_at, resolved_at, created_at, updated_at
		FROM incidents %s
		ORDER BY started_at DESC
		LIMIT $%d OFFSET $%d
	`, where, idx, idx+1)
	args = append(args, pageSize, offset)

	rows, err := r.db.Query(ctx, dataQ, args...)
	if err != nil {
		return nil, fmt.Errorf("query incidents: %w", err)
	}
	defer rows.Close()

	var incidents []*models.Incident
	for rows.Next() {
		inc := &models.Incident{}
		if err := rows.Scan(
			&inc.ID, &inc.Title, &inc.RootCause, &inc.Status,
			&inc.Severity, &inc.AlertCount, &inc.StartedAt,
			&inc.ResolvedAt, &inc.CreatedAt, &inc.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan incident: %w", err)
		}
		incidents = append(incidents, inc)
	}

	// Populate service names for each incident.
	for _, inc := range incidents {
		inc.ServiceNames, _ = r.serviceNames(ctx, inc.ID)
	}

	return &QueryResult{Incidents: incidents, Total: total, Page: page, PageSize: pageSize}, nil
}

// GetByID fetches a single incident with its linked alerts.
func (r *IncidentRepository) GetByID(ctx context.Context, id string) (*models.Incident, error) {
	const q = `
		SELECT id, title, root_cause, status, severity, alert_count, started_at, resolved_at, created_at, updated_at
		FROM incidents WHERE id = $1
	`
	inc := &models.Incident{}
	err := r.db.QueryRow(ctx, q, id).Scan(
		&inc.ID, &inc.Title, &inc.RootCause, &inc.Status,
		&inc.Severity, &inc.AlertCount, &inc.StartedAt,
		&inc.ResolvedAt, &inc.CreatedAt, &inc.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("incident not found: %w", err)
	}
	inc.ServiceNames, _ = r.serviceNames(ctx, id)
	return inc, nil
}

// Timeline returns the ordered sequence of events for an incident.
func (r *IncidentRepository) Timeline(ctx context.Context, id string) ([]models.IncidentTimelineEvent, error) {
	inc, err := r.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	var events []models.IncidentTimelineEvent

	// Incident opened event.
	events = append(events, models.IncidentTimelineEvent{
		At:          inc.StartedAt,
		EventType:   "incident_opened",
		Description: fmt.Sprintf("Incident opened: %s", inc.Title),
	})

	// Individual alert events.
	const alertQ = `
		SELECT service_name, level, message, triggered_at
		FROM incident_alerts
		WHERE incident_id = $1
		ORDER BY triggered_at ASC
	`
	rows, err := r.db.Query(ctx, alertQ, id)
	if err != nil {
		return nil, fmt.Errorf("query timeline alerts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var svc, level, msg string
		var at time.Time
		if err := rows.Scan(&svc, &level, &msg, &at); err != nil {
			continue
		}
		events = append(events, models.IncidentTimelineEvent{
			At:          at,
			EventType:   "alert_triggered",
			ServiceName: svc,
			Level:       level,
			Description: fmt.Sprintf("[%s] %s: %s", level, svc, msg),
		})
	}

	// Resolved event (if applicable).
	if inc.ResolvedAt != nil {
		events = append(events, models.IncidentTimelineEvent{
			At:          *inc.ResolvedAt,
			EventType:   "incident_resolved",
			Description: "Incident resolved",
		})
	}

	return events, nil
}

// GetOpenByWindow finds an open incident that started within the correlation
// window for the given service. Returns nil if none exists.
func (r *IncidentRepository) GetOpenByWindow(ctx context.Context, serviceName string, windowStart time.Time) (*models.Incident, error) {
	const q = `
		SELECT i.id, i.title, i.root_cause, i.status, i.severity, i.alert_count,
		       i.started_at, i.resolved_at, i.created_at, i.updated_at
		FROM incidents i
		JOIN incident_alerts ia ON ia.incident_id = i.id
		WHERE i.status = 'OPEN'
		  AND ia.service_name = $1
		  AND i.started_at >= $2
		ORDER BY i.started_at DESC
		LIMIT 1
	`
	inc := &models.Incident{}
	err := r.db.QueryRow(ctx, q, serviceName, windowStart).Scan(
		&inc.ID, &inc.Title, &inc.RootCause, &inc.Status,
		&inc.Severity, &inc.AlertCount, &inc.StartedAt,
		&inc.ResolvedAt, &inc.CreatedAt, &inc.UpdatedAt,
	)
	if err != nil {
		return nil, nil // no open incident in window — caller creates a new one
	}
	return inc, nil
}

func (r *IncidentRepository) serviceNames(ctx context.Context, incidentID string) ([]string, error) {
	rows, err := r.db.Query(ctx,
		"SELECT DISTINCT service_name FROM incident_alerts WHERE incident_id = $1 ORDER BY service_name",
		incidentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err == nil {
			names = append(names, n)
		}
	}
	return names, nil
}

// TotalPages computes the number of pages.
func TotalPages(total int64, pageSize int) int {
	return int(math.Ceil(float64(total) / float64(pageSize)))
}
