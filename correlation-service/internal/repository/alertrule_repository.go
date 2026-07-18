package repository

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AlertRule mirrors a row of gateway-service's alert_rules table (see
// gateway-service/migrations/008_create_alert_rules.sql). Rules are managed
// via gateway-service's CRUD API but evaluated here, in correlation-service,
// which already polls ClickHouse for RED metrics on the same cadence.
type AlertRule struct {
	ID              string
	TenantID        string
	Name            string
	ServiceName     string // "*" = every service
	Condition       string // expr-lang boolean expression
	Severity        string
	CooldownSeconds int
}

// AlertRuleRepository reads alert_rules from the shared Postgres database.
// Read-only from correlation-service's side — mutations happen through
// gateway-service's AlertRuleHandler.
type AlertRuleRepository struct {
	db *pgxpool.Pool
}

func NewAlertRuleRepository(db *pgxpool.Pool) *AlertRuleRepository {
	return &AlertRuleRepository{db: db}
}

// ListEnabled returns every enabled alert rule across all tenants.
func (r *AlertRuleRepository) ListEnabled(ctx context.Context) ([]AlertRule, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, tenant_id, name, service_name, condition, severity, cooldown_seconds
		FROM alert_rules
		WHERE enabled = true`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AlertRule
	for rows.Next() {
		var ar AlertRule
		if err := rows.Scan(&ar.ID, &ar.TenantID, &ar.Name, &ar.ServiceName, &ar.Condition, &ar.Severity, &ar.CooldownSeconds); err != nil {
			continue
		}
		out = append(out, ar)
	}
	return out, rows.Err()
}
