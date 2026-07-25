package handler

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"

	_ "github.com/lib/pq"
)

type SyntheticsHandler struct {
	ClickHouseURL string
	DB            *sql.DB
}

type SyntheticResult struct {
	Timestamp  time.Time `json:"timestamp"`
	TenantID   string    `json:"tenant_id"`
	URL        string    `json:"url"`
	StatusCode int       `json:"status_code"`
	LatencyMs  float64   `json:"latency_ms"`
	Success    bool      `json:"success"`
}

func NewSyntheticsHandler(clickhouseURL string, db *sql.DB) *SyntheticsHandler {
	handler := &SyntheticsHandler{
		ClickHouseURL: clickhouseURL,
		DB:            db,
	}
	handler.initClickHouseTable()
	handler.initPostgresTable()
	return handler
}

func (h *SyntheticsHandler) initPostgresTable() {
	if h.DB == nil {
		log.Println("[SyntheticsHandler] WARNING: Postgres DB is nil, targets will not be persistent.")
		return
	}
	query := `
		CREATE TABLE IF NOT EXISTS synthetic_targets (
			id SERIAL PRIMARY KEY,
			tenant_id VARCHAR(50) NOT NULL DEFAULT 'default',
			url VARCHAR(255) NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(tenant_id, url)
		);
	`
	_, err := h.DB.Exec(query)
	if err != nil {
		log.Printf("[SyntheticsHandler] WARNING: Failed to create Postgres synthetic_targets table: %v", err)
	} else {
		log.Println("[SyntheticsHandler] Postgres synthetic_targets table initialized.")
	}
	// Backfill tenant_id on a pre-existing single-tenant table (best-effort).
	_, _ = h.DB.Exec("ALTER TABLE synthetic_targets ADD COLUMN IF NOT EXISTS tenant_id VARCHAR(50) NOT NULL DEFAULT 'default'")
}

func (h *SyntheticsHandler) initClickHouseTable() {
	query := `
		CREATE TABLE IF NOT EXISTS pulsetrace.synthetic_results (
			Timestamp DateTime64(3) DEFAULT now(),
			TenantID String DEFAULT 'default',
			URL String,
			StatusCode Int32,
			LatencyMs Float64,
			Success UInt8
		) ENGINE = MergeTree()
		PARTITION BY TenantID
		ORDER BY (Timestamp, URL)
		TTL toDateTime(Timestamp) + INTERVAL 7 DAY;
	`

	req, _ := http.NewRequest("POST", h.ClickHouseURL, bytes.NewBufferString(query))
	req.SetBasicAuth(clickhouseUser, clickhousePassword)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[SyntheticsHandler] WARNING: Failed to create ClickHouse table: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[SyntheticsHandler] WARNING: ClickHouse table creation returned %d: %s", resp.StatusCode, string(body))
	} else {
		log.Println("[SyntheticsHandler] ClickHouse synthetic_results table initialized.")
	}

	// Add TenantID to a pre-existing table (best-effort), backfilling old rows to 'default'.
	alter, _ := http.NewRequest("POST", h.ClickHouseURL, bytes.NewBufferString(
		"ALTER TABLE pulsetrace.synthetic_results ADD COLUMN IF NOT EXISTS TenantID String DEFAULT 'default'"))
	alter.SetBasicAuth(clickhouseUser, clickhousePassword)
	if aresp, aerr := client.Do(alter); aerr == nil {
		aresp.Body.Close()
	}
}

// StartWorker begins polling the endpoints in the background
func (h *SyntheticsHandler) StartWorker() {
	go func() {
		client := &http.Client{Timeout: 3 * time.Second}
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			if h.DB == nil {
				continue // Need DB for targets
			}

			// Fetch targets from postgres, carrying each target's owning tenant so
			// its probe results are attributed back to that tenant.
			rows, err := h.DB.Query("SELECT tenant_id, url FROM synthetic_targets")
			if err != nil {
				log.Printf("[SyntheticsWorker] Failed to query targets: %v", err)
				continue
			}

			type target struct{ tenantID, url string }
			var endpoints []target
			for rows.Next() {
				var t target
				if err := rows.Scan(&t.tenantID, &t.url); err == nil {
					endpoints = append(endpoints, t)
				}
			}
			rows.Close()

			if len(endpoints) == 0 {
				continue
			}

			var results []SyntheticResult
			now := time.Now()

			for _, tgt := range endpoints {
				start := time.Now()
				resp, err := client.Get(tgt.url)
				latency := float64(time.Since(start).Milliseconds())

				res := SyntheticResult{
					Timestamp: now,
					TenantID:  tgt.tenantID,
					URL:       tgt.url,
					LatencyMs: latency,
				}

				if err != nil {
					res.Success = false
					res.StatusCode = 0
				} else {
					res.StatusCode = resp.StatusCode
					res.Success = (resp.StatusCode >= 200 && resp.StatusCode < 300)
					resp.Body.Close()
				}
				results = append(results, res)
			}
			h.flushResults(results)
		}
	}()
}

func (h *SyntheticsHandler) flushResults(results []SyntheticResult) {
	if len(results) == 0 {
		return
	}

	var insertQuery bytes.Buffer
	insertQuery.WriteString("INSERT INTO pulsetrace.synthetic_results (Timestamp, TenantID, URL, StatusCode, LatencyMs, Success) FORMAT JSONEachRow\n")

	for _, res := range results {
		// Convert time to string for CH JSONEachRow
		type chResult struct {
			Timestamp  string  `json:"Timestamp"`
			TenantID   string  `json:"TenantID"`
			URL        string  `json:"URL"`
			StatusCode int     `json:"StatusCode"`
			LatencyMs  float64 `json:"LatencyMs"`
			Success    uint8   `json:"Success"`
		}

		succ := uint8(0)
		if res.Success {
			succ = 1
		}

		tenantID := res.TenantID
		if tenantID == "" {
			tenantID = "default"
		}

		ch := chResult{
			Timestamp:  res.Timestamp.Format("2006-01-02 15:04:05.000"),
			TenantID:   tenantID,
			URL:        res.URL,
			StatusCode: res.StatusCode,
			LatencyMs:  res.LatencyMs,
			Success:    succ,
		}

		b, _ := json.Marshal(ch)
		insertQuery.Write(b)
		insertQuery.WriteString("\n")
	}

	req, _ := http.NewRequest("POST", h.ClickHouseURL, &insertQuery)
	req.SetBasicAuth(clickhouseUser, clickhousePassword)
	
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[SyntheticsWorker] Insert failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[SyntheticsWorker] Insert returned %d: %s", resp.StatusCode, string(body))
	}
}

// GetResults serves analytics to the frontend
func (h *SyntheticsHandler) GetResults(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	tenantID := r.Header.Get("X-Tenant-ID")
	if tenantID == "" {
		tenantID = "default"
	}

	// Get the last 60 minutes of data for sparklines, plus aggregated stats,
	// scoped to the caller's tenant.
	query := `
		SELECT
			URL,
			avg(LatencyMs) as avg_latency_ms,
			avg(Success) * 100 as uptime_percent,
			groupArray(LatencyMs) as latency_history
		FROM pulsetrace.synthetic_results
		WHERE TenantID = {tenant:String} AND Timestamp >= now() - INTERVAL 1 HOUR
		GROUP BY URL
		FORMAT JSON
	`

	reqURL := h.ClickHouseURL + "?param_tenant=" + url.QueryEscape(tenantID)
	req, _ := http.NewRequest("POST", reqURL, bytes.NewBufferString(query))
	req.SetBasicAuth(clickhouseUser, clickhousePassword)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "failed to query synthetics", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	
	if resp.StatusCode == http.StatusNotFound {
		io.WriteString(w, `{"data": []}`)
		return
	}
	io.Copy(w, resp.Body)
}

// CreateTarget registers a new synthetic endpoint
func (h *SyntheticsHandler) CreateTarget(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.URL == "" {
		http.Error(w, "invalid JSON or missing url", http.StatusBadRequest)
		return
	}

	if h.DB == nil {
		http.Error(w, "database unavailable", http.StatusInternalServerError)
		return
	}

	tenantID := r.Header.Get("X-Tenant-ID")
	if tenantID == "" {
		tenantID = "default"
	}

	_, err := h.DB.Exec("INSERT INTO synthetic_targets (tenant_id, url) VALUES ($1, $2) ON CONFLICT (tenant_id, url) DO NOTHING", tenantID, payload.URL)
	if err != nil {
		log.Printf("[SyntheticsHandler] Failed to insert target: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	io.WriteString(w, `{"status":"ok"}`)
}
