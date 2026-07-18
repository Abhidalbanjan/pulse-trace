package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"
)

type RUMHandler struct {
	ClickHouseURL string
}

type RUMEvent struct {
	SessionID   string  `json:"session_id"`
	Type        string  `json:"type"` // "web_vitals" or "error" or "page_view"
	Path        string  `json:"path"`
	UserAgent   string  `json:"user_agent"`
	MetricName  string  `json:"metric_name,omitempty"` // LCP, FID, CLS
	MetricValue float64 `json:"metric_value,omitempty"`
	ErrorMsg    string  `json:"error_msg,omitempty"`
	ErrorStack  string  `json:"error_stack,omitempty"`
	TraceID     string  `json:"trace_id,omitempty"` // W3C trace id shared with backend API calls made during this page view
	SpanID      string  `json:"span_id,omitempty"`
}

func NewRUMHandler(clickhouseURL string) *RUMHandler {
	handler := &RUMHandler{ClickHouseURL: clickhouseURL}
	handler.initTable()
	return handler
}

func (h *RUMHandler) initTable() {
	query := `
		CREATE TABLE IF NOT EXISTS pulsetrace.rum_events (
			Timestamp DateTime64(3) DEFAULT now(),
			SessionID String,
			Type String,
			Path String,
			UserAgent String,
			MetricName String,
			MetricValue Float64,
			ErrorMsg String,
			ErrorStack String,
			TraceID String DEFAULT '',
			SpanID String DEFAULT ''
		) ENGINE = MergeTree()
		ORDER BY (Timestamp, Type)
		TTL toDateTime(Timestamp) + INTERVAL 7 DAY;
	`

	req, _ := http.NewRequest("POST", h.ClickHouseURL, bytes.NewBufferString(query))
	req.SetBasicAuth(clickhouseUser, clickhousePassword)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[RUMHandler] WARNING: Failed to create ClickHouse table: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[RUMHandler] WARNING: ClickHouse table creation returned %d: %s", resp.StatusCode, string(body))
	} else {
		log.Println("[RUMHandler] ClickHouse rum_events table initialized.")
	}

	// Table may already exist from before TraceID/SpanID were added - add them if missing.
	alterQuery := `
		ALTER TABLE pulsetrace.rum_events
			ADD COLUMN IF NOT EXISTS TraceID String DEFAULT '',
			ADD COLUMN IF NOT EXISTS SpanID String DEFAULT ''
	`
	alterReq, _ := http.NewRequest("POST", h.ClickHouseURL, bytes.NewBufferString(alterQuery))
	alterReq.SetBasicAuth(clickhouseUser, clickhousePassword)
	alterResp, err := client.Do(alterReq)
	if err != nil {
		log.Printf("[RUMHandler] WARNING: Failed to alter rum_events table: %v", err)
		return
	}
	defer alterResp.Body.Close()
	if alterResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(alterResp.Body)
		log.Printf("[RUMHandler] WARNING: ClickHouse table alter returned %d: %s", alterResp.StatusCode, string(body))
	}
}

// Ingest handles POST requests from the browser
func (h *RUMHandler) Ingest(w http.ResponseWriter, r *http.Request) {
	var events []RUMEvent
	if err := json.NewDecoder(r.Body).Decode(&events); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if len(events) == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Batch insert into ClickHouse
	var insertQuery bytes.Buffer
	insertQuery.WriteString("INSERT INTO pulsetrace.rum_events (SessionID, Type, Path, UserAgent, MetricName, MetricValue, ErrorMsg, ErrorStack, TraceID, SpanID) FORMAT JSONEachRow\n")
	
	for _, ev := range events {
		b, _ := json.Marshal(ev)
		insertQuery.Write(b)
		insertQuery.WriteString("\n")
	}

	req, _ := http.NewRequest("POST", h.ClickHouseURL, &insertQuery)
	req.SetBasicAuth(clickhouseUser, clickhousePassword)
	
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[RUMHandler] Failed to insert RUM events: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[RUMHandler] Insert returned %d: %s", resp.StatusCode, string(body))
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"status": "ok"}`)
}

// GetAnalytics queries RUM data for the frontend dashboard
func (h *RUMHandler) GetAnalytics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	query := `
		SELECT 
			Type,
			MetricName,
			avg(MetricValue) as avg_value,
			count() as count
		FROM pulsetrace.rum_events
		WHERE Timestamp >= now() - INTERVAL 24 HOUR
		GROUP BY Type, MetricName
		FORMAT JSON
	`

	req, _ := http.NewRequest("POST", h.ClickHouseURL, bytes.NewBufferString(query))
	req.SetBasicAuth(clickhouseUser, clickhousePassword)
	
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "failed to query analytics", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	
	if resp.StatusCode == http.StatusNotFound {
		io.WriteString(w, `{"data": []}`)
		return
	}
	io.Copy(w, resp.Body)
}

// GetErrors queries recent frontend errors
func (h *RUMHandler) GetErrors(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	query := `
		SELECT
			Timestamp as timestamp,
			Path as path,
			ErrorMsg as error_msg,
			ErrorStack as error_stack,
			UserAgent as user_agent,
			TraceID as trace_id
		FROM pulsetrace.rum_events
		WHERE Type = 'error'
		ORDER BY Timestamp DESC
		LIMIT 50
		FORMAT JSON
	`

	req, _ := http.NewRequest("POST", h.ClickHouseURL, bytes.NewBufferString(query))
	req.SetBasicAuth(clickhouseUser, clickhousePassword)
	
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "failed to query errors", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	
	if resp.StatusCode == http.StatusNotFound {
		io.WriteString(w, `{"data": []}`)
		return
	}
	io.Copy(w, resp.Body)
}
