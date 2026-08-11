package auth

import (
	"testing"
	"time"
)

// buildChain produces a valid, correctly-linked chain from a set of records —
// exactly what WriteAudit persists, but in-memory so the pure verifier can be
// tested without a database.
func buildChain(recs []auditRecord) []ChainRow {
	prev := auditGenesisHash
	rows := make([]ChainRow, 0, len(recs))
	for i, rec := range recs {
		entry := computeAuditHash(prev, rec)
		rows = append(rows, ChainRow{
			ID:          int64(i + 1),
			Actor:       rec.Actor,
			Action:      rec.Action,
			TargetType:  rec.TargetType,
			TargetID:    rec.TargetID,
			BeforeState: []byte(rec.BeforeState),
			AfterState:  []byte(rec.AfterState),
			CreatedAt:   rec.CreatedAt,
			PrevHash:    prev,
			EntryHash:   entry,
		})
		prev = entry
	}
	return rows
}

func sampleRecords() []auditRecord {
	base := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	return []auditRecord{
		{Actor: "alice", Action: "create", TargetType: "role", TargetID: "admin", BeforeState: "null", AfterState: `{"name":"admin"}`, CreatedAt: base},
		{Actor: "bob", Action: "update", TargetType: "policy", TargetID: "p1", BeforeState: `{"effect":"allow"}`, AfterState: `{"effect":"deny"}`, CreatedAt: base.Add(time.Minute)},
		{Actor: "carol", Action: "delete", TargetType: "user", TargetID: "u9", BeforeState: `{"username":"dave"}`, AfterState: "null", CreatedAt: base.Add(2 * time.Minute)},
	}
}

func TestVerifyAuditChain_IntactChainVerifies(t *testing.T) {
	rows := buildChain(sampleRecords())
	v := VerifyAuditChain(rows)
	if !v.Valid {
		t.Fatalf("expected intact chain to verify, got: %+v", v)
	}
	if v.Count != 3 {
		t.Fatalf("expected count 3, got %d", v.Count)
	}
}

func TestVerifyAuditChain_EmptyIsValid(t *testing.T) {
	if v := VerifyAuditChain(nil); !v.Valid || v.Count != 0 {
		t.Fatalf("empty chain should be trivially valid, got %+v", v)
	}
}

func TestVerifyAuditChain_TamperedContentDetected(t *testing.T) {
	rows := buildChain(sampleRecords())
	// Attacker edits a persisted field without recomputing the hash.
	rows[1].Actor = "mallory"
	v := VerifyAuditChain(rows)
	if v.Valid {
		t.Fatal("expected tampered content to fail verification")
	}
	if v.FirstBrokenID != 2 {
		t.Fatalf("expected break at row 2, got %d", v.FirstBrokenID)
	}
}

func TestVerifyAuditChain_DeletedRowDetected(t *testing.T) {
	rows := buildChain(sampleRecords())
	// Attacker deletes the middle row to hide an action. The row that followed
	// it now has a prev_hash that links to nothing present.
	tampered := []ChainRow{rows[0], rows[2]}
	v := VerifyAuditChain(tampered)
	if v.Valid {
		t.Fatal("expected a deleted row to break the chain")
	}
	if v.FirstBrokenID != rows[2].ID {
		t.Fatalf("expected break at the row after the deletion (%d), got %d", rows[2].ID, v.FirstBrokenID)
	}
}

func TestVerifyAuditChain_ReorderDetected(t *testing.T) {
	rows := buildChain(sampleRecords())
	rows[1], rows[2] = rows[2], rows[1]
	if VerifyAuditChain(rows).Valid {
		t.Fatal("expected reordering to break the chain")
	}
}

func TestVerifyAuditChain_MissingHashIsRejected(t *testing.T) {
	rows := buildChain(sampleRecords())
	rows[2].EntryHash = "" // a row that never joined the chain
	v := VerifyAuditChain(rows)
	if v.Valid || v.FirstBrokenID != 3 {
		t.Fatalf("expected un-hashed row to fail at id 3, got %+v", v)
	}
}

func TestComputeAuditHash_LengthPrefixPreventsCollision(t *testing.T) {
	// Without length-prefixing, ("ab","c") and ("a","bc") would hash the same.
	t1 := time.Now().UTC().Truncate(time.Microsecond)
	a := computeAuditHash(auditGenesisHash, auditRecord{Actor: "ab", Action: "c", BeforeState: "null", AfterState: "null", CreatedAt: t1})
	b := computeAuditHash(auditGenesisHash, auditRecord{Actor: "a", Action: "bc", BeforeState: "null", AfterState: "null", CreatedAt: t1})
	if a == b {
		t.Fatal("field boundaries are ambiguous — hash must length-prefix fields")
	}
}

func TestComputeAuditHash_Deterministic(t *testing.T) {
	rec := sampleRecords()[0]
	if computeAuditHash("prev", rec) != computeAuditHash("prev", rec) {
		t.Fatal("hash must be deterministic for identical input")
	}
}

func TestCanonicalJSON_KeyOrderIndependent(t *testing.T) {
	// The same object serialized with different key order (Go struct order at
	// write time vs. jsonb order at read time) must canonicalize identically,
	// or every verify would spuriously fail.
	a := canonicalJSON([]byte(`{"b":2,"a":1}`))
	b := canonicalJSON([]byte(`{"a":1,"b":2}`))
	if a != b {
		t.Fatalf("canonical JSON must be key-order independent: %q vs %q", a, b)
	}
}

func TestCanonicalJSON_EmptyAndInvalidBecomeNull(t *testing.T) {
	if canonicalJSON(nil) != "null" {
		t.Error("nil should canonicalize to null")
	}
	if canonicalJSON([]byte("not json")) != "null" {
		t.Error("invalid JSON should canonicalize to null")
	}
}

func TestCanonicalTime_MicrosecondStable(t *testing.T) {
	// A nanosecond-precision time and its microsecond truncation must render
	// identically, matching what round-trips through Postgres.
	base := time.Date(2026, 8, 11, 10, 0, 0, 123456789, time.UTC)
	if canonicalTime(base) != canonicalTime(base.Truncate(time.Microsecond)) {
		t.Fatal("canonicalTime must be microsecond-stable")
	}
}
