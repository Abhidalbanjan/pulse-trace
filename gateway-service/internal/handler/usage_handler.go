package handler

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
)

// UsageHandler exposes a tenant's metered ingestion volume for the current
// billing period, read from usage_daily (mirrored from the Redis counters by the
// metering flusher). Backs the Settings/billing "usage this month" view.
type UsageHandler struct {
	db *sql.DB
}

func NewUsageHandler(db *sql.DB) *UsageHandler {
	return &UsageHandler{db: db}
}

// GetUsage handles GET /api/v1/usage — current calendar-month totals per signal
// for the caller's tenant.
func (h *UsageHandler) GetUsage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	usage := map[string]int64{"traces": 0, "metrics": 0, "logs": 0, "rum": 0}

	if h.db != nil {
		rows, err := h.db.Query(`
			SELECT signal, COALESCE(SUM(count), 0)
			FROM usage_daily
			WHERE tenant_id = $1 AND day >= date_trunc('month', CURRENT_DATE)
			GROUP BY signal`, tenantFromRequest(r))
		if err != nil {
			log.Printf("usage: query failed: %v", err)
			http.Error(w, "failed to load usage", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var signal string
			var count int64
			if err := rows.Scan(&signal, &count); err != nil {
				continue
			}
			usage[signal] = count
		}
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"tenant_id": tenantFromRequest(r),
		"period":    "month",
		"usage":     usage,
	})
}
