package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// auditGenesisHash is the prev_hash of the very first audit row — a fixed,
// well-known value so the chain has a deterministic anchor. 64 hex zeros.
const auditGenesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

// auditChainLockKey serializes concurrent audit inserts via a Postgres
// transaction-level advisory lock, so prev_hash always references the true tail
// of the chain even under concurrent writers. The value is arbitrary but fixed.
const auditChainLockKey int64 = 918273645

// auditRecord is the immutable projection of an audit row that is fed into the
// hash. ID is deliberately excluded: it is assigned by the database on insert
// (so it isn't known when the hash is computed) and chain integrity comes from
// the prev_hash linkage, not the serial id. CreatedAt is truncated to
// microseconds — Postgres timestamptz precision — so the value hashed at write
// time is byte-identical to the value read back at verify time.
type auditRecord struct {
	Actor       string
	Action      string
	TargetType  string
	TargetID    string
	BeforeState string // canonical JSON, or "null"
	AfterState  string // canonical JSON, or "null"
	CreatedAt   time.Time
}

// computeAuditHash returns the entry hash for a record chained onto prevHash.
//
// Fields are length-prefixed before hashing so that no combination of field
// values can be rearranged to produce the same digest (e.g. actor="ab",
// action="c" must not collide with actor="a", action="bc"). This is the single
// source of truth for the algorithm — write, back-fill, and verify all call it.
func computeAuditHash(prevHash string, r auditRecord) string {
	h := sha256.New()
	writeHashField(h, prevHash)
	writeHashField(h, r.Actor)
	writeHashField(h, r.Action)
	writeHashField(h, r.TargetType)
	writeHashField(h, r.TargetID)
	writeHashField(h, r.BeforeState)
	writeHashField(h, r.AfterState)
	writeHashField(h, canonicalTime(r.CreatedAt))
	return hex.EncodeToString(h.Sum(nil))
}

func writeHashField(h interface{ Write([]byte) (int, error) }, field string) {
	fmt.Fprintf(h, "%d:", len(field))
	_, _ = h.Write([]byte(field))
}

// canonicalTime renders a timestamp deterministically at microsecond precision.
// Both writer and verifier normalize to UTC + microseconds so a round-trip
// through Postgres (which stores microseconds) hashes identically.
func canonicalTime(t time.Time) string {
	return t.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
}

// canonicalJSON normalizes a JSON value to a stable byte form: object keys are
// sorted (Go's encoding/json sorts map keys) and insignificant whitespace is
// removed. This makes the hash independent of how before/after state was
// serialized (Go struct field order at write time vs. Postgres jsonb key order
// at read time). Invalid or empty input canonicalizes to "null".
func canonicalJSON(raw []byte) string {
	if len(raw) == 0 {
		return "null"
	}
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return "null"
	}
	out, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(out)
}

// canonicalJSONOf marshals an arbitrary Go value and canonicalizes it, for the
// write path where before/after arrive as structs/maps rather than raw bytes.
func canonicalJSONOf(v interface{}) string {
	if v == nil {
		return "null"
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return canonicalJSON(raw)
}

// ChainRow is one row as read back for verification: its stored hashes plus the
// fields needed to recompute them.
type ChainRow struct {
	ID          int64
	Actor       string
	Action      string
	TargetType  string
	TargetID    string
	BeforeState []byte
	AfterState  []byte
	CreatedAt   time.Time
	PrevHash    string
	EntryHash   string
}

func (row ChainRow) record(prevHash string) auditRecord {
	return auditRecord{
		Actor:       row.Actor,
		Action:      row.Action,
		TargetType:  row.TargetType,
		TargetID:    row.TargetID,
		BeforeState: canonicalJSON(row.BeforeState),
		AfterState:  canonicalJSON(row.AfterState),
		CreatedAt:   row.CreatedAt,
	}
}

// AuditVerification is the result of replaying the chain.
type AuditVerification struct {
	Valid         bool   `json:"valid"`
	Count         int    `json:"count"`
	FirstBrokenID int64  `json:"first_broken_id,omitempty"` // 0 when valid
	Message       string `json:"message"`
}

// VerifyAuditChain replays rows in ascending id order and confirms that (a)
// each row's prev_hash links to the previous row's entry_hash and (b) each
// row's entry_hash equals the recomputed hash of its content. It reports the
// first row where either check fails.
//
// Rows must be provided oldest-first. A row with an empty entry_hash is treated
// as un-hashed (a back-fill gap) and fails verification, since an auditor
// cannot vouch for a row outside the chain.
func VerifyAuditChain(rows []ChainRow) AuditVerification {
	prev := auditGenesisHash
	for _, row := range rows {
		if strings.TrimSpace(row.EntryHash) == "" {
			return AuditVerification{
				Valid: false, Count: len(rows), FirstBrokenID: row.ID,
				Message: fmt.Sprintf("row %d is not part of the hash chain (no entry hash)", row.ID),
			}
		}
		if row.PrevHash != prev {
			return AuditVerification{
				Valid: false, Count: len(rows), FirstBrokenID: row.ID,
				Message: fmt.Sprintf("row %d breaks the chain: its prev_hash does not match the preceding row (a row was deleted, reordered, or inserted)", row.ID),
			}
		}
		want := computeAuditHash(prev, row.record(prev))
		if row.EntryHash != want {
			return AuditVerification{
				Valid: false, Count: len(rows), FirstBrokenID: row.ID,
				Message: fmt.Sprintf("row %d has been altered since it was written (content does not match its hash)", row.ID),
			}
		}
		prev = row.EntryHash
	}
	return AuditVerification{
		Valid: true, Count: len(rows),
		Message: fmt.Sprintf("verified %d audit entries — the trail is intact", len(rows)),
	}
}
