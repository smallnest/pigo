package remotecontrol

import (
	"testing"
	"time"
)

func TestNewPairingTokenIsHex256Bit(t *testing.T) {
	s := NewTokenStore()
	value, err := s.NewPairing(time.Minute)
	if err != nil {
		t.Fatalf("NewPairing: %v", err)
	}
	// 32 bytes -> 64 hex chars.
	if len(value) != tokenBytes*2 {
		t.Fatalf("token length = %d, want %d", len(value), tokenBytes*2)
	}
	for _, c := range value {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("token has non-hex char %q", c)
		}
	}
}

func TestPairingIsSingleUse(t *testing.T) {
	s := NewTokenStore()
	value, err := s.NewPairing(time.Minute)
	if err != nil {
		t.Fatalf("NewPairing: %v", err)
	}
	if !s.ConsumePairing(value) {
		t.Fatal("first ConsumePairing = false, want true")
	}
	if s.ConsumePairing(value) {
		t.Fatal("second ConsumePairing = true, want false (single-use)")
	}
}

func TestConsumePairingUnknownToken(t *testing.T) {
	s := NewTokenStore()
	if s.ConsumePairing("deadbeef") {
		t.Fatal("ConsumePairing on unknown token = true, want false")
	}
}

func TestPairingExpires(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := NewTokenStore()
	s.now = func() time.Time { return now }
	value, err := s.NewPairing(10 * time.Minute)
	if err != nil {
		t.Fatalf("NewPairing: %v", err)
	}
	// Advance clock just past the TTL.
	now = now.Add(10*time.Minute + time.Second)
	if s.ConsumePairing(value) {
		t.Fatal("ConsumePairing after expiry = true, want false")
	}
}

func TestPairingValidJustBeforeExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := NewTokenStore()
	s.now = func() time.Time { return now }
	value, err := s.NewPairing(10 * time.Minute)
	if err != nil {
		t.Fatalf("NewPairing: %v", err)
	}
	now = now.Add(10*time.Minute - time.Second)
	if !s.ConsumePairing(value) {
		t.Fatal("ConsumePairing just before expiry = false, want true")
	}
}

func TestIssueAndValidateSession(t *testing.T) {
	s := NewTokenStore()
	cred, err := s.IssueSession()
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}
	if len(cred) != tokenBytes*2 {
		t.Fatalf("cred length = %d, want %d", len(cred), tokenBytes*2)
	}
	if !s.ValidateSession(cred) {
		t.Fatal("ValidateSession(issued) = false, want true")
	}
	if s.ValidateSession("") {
		t.Fatal("ValidateSession(\"\") = true, want false")
	}
	if s.ValidateSession("not-a-real-cred") {
		t.Fatal("ValidateSession(bogus) = true, want false")
	}
}

func TestClearWipesState(t *testing.T) {
	s := NewTokenStore()
	value, _ := s.NewPairing(time.Minute)
	cred, _ := s.IssueSession()
	s.Clear()
	if s.ConsumePairing(value) {
		t.Fatal("pairing token survived Clear")
	}
	if s.ValidateSession(cred) {
		t.Fatal("session credential survived Clear")
	}
}

func TestTokensAreUnique(t *testing.T) {
	s := NewTokenStore()
	seen := make(map[string]struct{})
	for range 100 {
		v, err := s.NewPairing(time.Minute)
		if err != nil {
			t.Fatalf("NewPairing: %v", err)
		}
		if _, dup := seen[v]; dup {
			t.Fatalf("duplicate token generated: %s", v)
		}
		seen[v] = struct{}{}
	}
}
