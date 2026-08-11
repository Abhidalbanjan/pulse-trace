package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// WriteAudit appends one row to the audit_log for a role/policy/user/rate-limit-rule
// mutation. Best-effort: a logging failure never blocks the mutation itself, but it
// is logged loudly since a silently-failing audit trail is worse than none.
// Exported so other handler packages (e.g. rate limit rule CRUD) can reuse the same
// audit table without duplicating the insert logic.
//
// Each row is hash-chained to its predecessor (F20): under a transaction-level
// advisory lock (so concurrent writers can't fork the chain) it reads the
// current tail hash, computes this row's entry_hash from it plus the row's
// canonical content, and stores both. Tampering with any persisted row is then
// detectable by replaying the chain — see VerifyAuditChain.
func WriteAudit(db *sql.DB, actor, action, targetType, targetID string, before, after interface{}) {
	if db == nil {
		return
	}
	if actor == "" {
		actor = "unknown"
	}

	// created_at is chosen here (not left to the DB clock) and truncated to
	// microseconds so the exact value hashed is the exact value stored.
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	beforeJSON := canonicalJSONOf(before)
	afterJSON := canonicalJSONOf(after)

	rec := auditRecord{
		Actor: actor, Action: action, TargetType: targetType, TargetID: targetID,
		BeforeState: beforeJSON, AfterState: afterJSON, CreatedAt: createdAt,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := appendChained(ctx, db, rec); err != nil {
		log.Printf("audit: FAILED to record %s %s %s (actor=%s): %v", action, targetType, targetID, actor, err)
	}
}

// appendChained inserts one hash-chained row inside a transaction that holds the
// audit advisory lock, so the tail it reads is authoritative.
func appendChained(ctx context.Context, db *sql.DB, rec auditRecord) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", auditChainLockKey); err != nil {
		return err
	}

	prev := auditGenesisHash
	var tail sql.NullString
	// Order by id then entry_hash presence: the true tail is the newest row that
	// actually carries a hash (a back-fill gap would otherwise poison the chain).
	err = tx.QueryRowContext(ctx,
		"SELECT entry_hash FROM audit_log WHERE entry_hash IS NOT NULL ORDER BY id DESC LIMIT 1").Scan(&tail)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if tail.Valid && tail.String != "" {
		prev = tail.String
	}

	entryHash := computeAuditHash(prev, rec)

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO audit_log (actor, action, target_type, target_id, before_state, after_state, created_at, prev_hash, entry_hash)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		rec.Actor, rec.Action, rec.TargetType, rec.TargetID,
		rec.BeforeState, rec.AfterState, rec.CreatedAt, prev, entryHash,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// BackfillAuditChain hashes any legacy audit rows that predate tamper-evidence
// (entry_hash IS NULL), chaining them in id order onto whatever tail already
// exists. Idempotent and cheap to skip: it returns immediately when there is
// nothing to back-fill, so it is safe to call on every startup. Runs under the
// same advisory lock as WriteAudit so it can't race a live write.
func BackfillAuditChain(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return nil
	}

	var pending bool
	if err := db.QueryRowContext(ctx,
		"SELECT EXISTS (SELECT 1 FROM audit_log WHERE entry_hash IS NULL)").Scan(&pending); err != nil {
		return fmt.Errorf("audit backfill: probe: %w", err)
	}
	if !pending {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", auditChainLockKey); err != nil {
		return err
	}

	// Anchor onto the newest already-hashed row (if any), then hash the rest in
	// id order. Legacy rows keep their original created_at.
	prev := auditGenesisHash
	var tail sql.NullString
	if err := tx.QueryRowContext(ctx,
		"SELECT entry_hash FROM audit_log WHERE entry_hash IS NOT NULL ORDER BY id DESC LIMIT 1").Scan(&tail); err != nil && err != sql.ErrNoRows {
		return err
	}
	if tail.Valid && tail.String != "" {
		prev = tail.String
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT id, actor, action, target_type, target_id,
		        COALESCE(before_state, 'null'::jsonb), COALESCE(after_state, 'null'::jsonb), created_at
		 FROM audit_log WHERE entry_hash IS NULL ORDER BY id ASC`)
	if err != nil {
		return err
	}

	type pendingRow struct {
		id        int64
		rec       auditRecord
		prev      string
		entryHash string
	}
	var updates []pendingRow
	for rows.Next() {
		var r ChainRow
		if err := rows.Scan(&r.ID, &r.Actor, &r.Action, &r.TargetType, &r.TargetID,
			&r.BeforeState, &r.AfterState, &r.CreatedAt); err != nil {
			rows.Close()
			return err
		}
		rec := r.record(prev)
		entryHash := computeAuditHash(prev, rec)
		updates = append(updates, pendingRow{id: r.ID, rec: rec, prev: prev, entryHash: entryHash})
		prev = entryHash
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, u := range updates {
		if _, err := tx.ExecContext(ctx,
			"UPDATE audit_log SET prev_hash = $1, entry_hash = $2 WHERE id = $3",
			u.prev, u.entryHash, u.id); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	log.Printf("audit: back-filled hash chain for %d legacy row(s)", len(updates))
	return nil
}

func actorFromRequest(r *http.Request) string {
	return r.Header.Get("X-User-Subject")
}

type AuditEntry struct {
	ID          int64           `json:"id"`
	Actor       string          `json:"actor"`
	Action      string          `json:"action"`
	TargetType  string          `json:"target_type"`
	TargetID    string          `json:"target_id"`
	BeforeState json.RawMessage `json:"before_state,omitempty"`
	AfterState  json.RawMessage `json:"after_state,omitempty"`
	CreatedAt   string          `json:"created_at"`
	PrevHash    string          `json:"prev_hash,omitempty"`
	EntryHash   string          `json:"entry_hash,omitempty"`
}

// AuditLogHandler exposes the audit trail for the Settings UI.
type AuditLogHandler struct {
	db *sql.DB
}

func NewAuditLogHandler(db *sql.DB) *AuditLogHandler {
	return &AuditLogHandler{db: db}
}

// ListAuditLog handles GET /api/v1/admin/audit-log
func (h *AuditLogHandler) ListAuditLog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if h.db == nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": []AuditEntry{}})
		return
	}

	rows, err := h.db.Query(`
		SELECT id, actor, action, target_type, target_id,
		       COALESCE(before_state, 'null'::jsonb), COALESCE(after_state, 'null'::jsonb),
		       created_at::text, COALESCE(prev_hash, ''), COALESCE(entry_hash, '')
		FROM audit_log
		ORDER BY id DESC
		LIMIT 200
	`)
	if err != nil {
		log.Printf("audit: failed to list audit log: %v", err)
		http.Error(w, "failed to list audit log", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.TargetType, &e.TargetID, &e.BeforeState, &e.AfterState, &e.CreatedAt, &e.PrevHash, &e.EntryHash); err != nil {
			continue
		}
		out = append(out, e)
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": out})
}

// chainRows reads the full audit trail oldest-first, with the fields needed to
// recompute the hash chain. Used by both verify and export so they see the
// exact same ordering the chain was built in.
func (h *AuditLogHandler) chainRows(ctx context.Context) ([]ChainRow, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT id, actor, action, target_type, target_id,
		       COALESCE(before_state, 'null'::jsonb), COALESCE(after_state, 'null'::jsonb),
		       created_at, COALESCE(prev_hash, ''), COALESCE(entry_hash, '')
		FROM audit_log
		ORDER BY id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ChainRow
	for rows.Next() {
		var r ChainRow
		if err := rows.Scan(&r.ID, &r.Actor, &r.Action, &r.TargetType, &r.TargetID,
			&r.BeforeState, &r.AfterState, &r.CreatedAt, &r.PrevHash, &r.EntryHash); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// VerifyAuditLog handles GET /api/v1/admin/audit-log/verify — replays the hash
// chain server-side and reports whether the trail is intact, and if not, the
// first row where tamper is detected. This is the "prove it to an auditor"
// action behind the Settings → Audit "Verify integrity" button.
func (h *AuditLogHandler) VerifyAuditLog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if h.db == nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": AuditVerification{Valid: true, Count: 0, Message: "no audit database configured"},
		})
		return
	}
	rows, err := h.chainRows(r.Context())
	if err != nil {
		log.Printf("audit: verify failed to read chain: %v", err)
		http.Error(w, "failed to read audit log", http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": VerifyAuditChain(rows)})
}

// ExportAuditLog handles GET /api/v1/admin/audit-log/export — streams the entire
// audit trail (not the 200-row UI window) as newline-delimited JSON including
// the per-row hashes, so a compliance archive can be re-verified independently
// long after export.
func (h *AuditLogHandler) ExportAuditLog(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		http.Error(w, "no audit database configured", http.StatusServiceUnavailable)
		return
	}
	rows, err := h.chainRows(r.Context())
	if err != nil {
		log.Printf("audit: export failed to read chain: %v", err)
		http.Error(w, "failed to read audit log", http.StatusInternalServerError)
		return
	}

	filename := fmt.Sprintf("audit-log-%s.ndjson", time.Now().UTC().Format("20060102T150405Z"))
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))

	enc := json.NewEncoder(w)
	for _, row := range rows {
		e := AuditEntry{
			ID: row.ID, Actor: row.Actor, Action: row.Action,
			TargetType: row.TargetType, TargetID: row.TargetID,
			BeforeState: json.RawMessage(row.BeforeState), AfterState: json.RawMessage(row.AfterState),
			CreatedAt: canonicalTime(row.CreatedAt), PrevHash: row.PrevHash, EntryHash: row.EntryHash,
		}
		if err := enc.Encode(e); err != nil {
			log.Printf("audit: export write interrupted: %v", err)
			return
		}
	}
}
