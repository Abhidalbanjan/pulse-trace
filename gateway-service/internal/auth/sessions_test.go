package auth

import "testing"

func TestSessionStore_NilSafe(t *testing.T) {
	var s *SessionStore
	if s.IsRevoked("anything") {
		t.Fatal("nil store must never report a session as revoked")
	}
}

func TestSessionStore_RevokedCache(t *testing.T) {
	s := NewSessionStore(nil) // nil db → no refresh, empty cache
	if s.IsRevoked("jti-1") {
		t.Fatal("unknown jti should not be revoked")
	}
	s.markRevokedLocal("jti-1", "jti-2")
	if !s.IsRevoked("jti-1") || !s.IsRevoked("jti-2") {
		t.Fatal("locally-revoked jti should read as revoked immediately")
	}
	if s.IsRevoked("jti-3") {
		t.Fatal("a different jti must remain valid")
	}
}

func TestSessionStore_EmptyJTINeverRevoked(t *testing.T) {
	s := NewSessionStore(nil)
	s.markRevokedLocal("") // no-op guard
	if s.IsRevoked("") {
		t.Fatal("empty jti (legacy token) must be treated as not revoked")
	}
}
