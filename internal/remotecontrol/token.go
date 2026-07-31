// Package remotecontrol implements the /remote-control feature: an in-process
// web server that lets a phone on the same LAN mirror the CLI session, inject
// prompts, and approve risky tool calls (see tasks/spec-remote-control.md).
//
// This file (node #438) provides the auth substrate: a one-time, TTL-bound
// pairing token that a paired browser exchanges for an opaque session
// credential. All state is in-memory and process-scoped; nothing is persisted
// or logged, and Clear() wipes it on shutdown.
package remotecontrol

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"sync"
	"time"
)

// tokenBytes is the entropy of both pairing tokens and session credentials.
// 32 bytes = 256 bits, hex-encoded to 64 characters in the URL/cookie.
const tokenBytes = 32

// pairingToken is a single-use link credential handed to the user (embedded in
// the printed /pair?t=... URL). It is consumed on the first successful pairing
// and rejected thereafter or once expired.
type pairingToken struct {
	expiresAt time.Time
	used      bool
}

// TokenStore holds the outstanding pairing tokens and issued session
// credentials for one remote-control session. It is safe for concurrent use by
// the HTTP handlers. All values are cryptographically random secrets; the store
// keeps only their hex string form and never logs them.
type TokenStore struct {
	mu       sync.Mutex
	pairing  map[string]*pairingToken
	sessions map[string]struct{}
	// now is the clock, injectable so tests can force expiry without sleeping.
	now func() time.Time
}

// NewTokenStore returns an empty store using the wall clock.
func NewTokenStore() *TokenStore {
	return &TokenStore{
		pairing:  make(map[string]*pairingToken),
		sessions: make(map[string]struct{}),
		now:      time.Now,
	}
}

// randHex returns n cryptographically random bytes, hex-encoded. crypto/rand
// never returns a short read without an error, so a nil error guarantees a full
// buffer.
func randHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// NewPairing mints a fresh one-time pairing token that expires after ttl and
// returns its hex value for embedding in the pairing URL.
func (s *TokenStore) NewPairing(ttl time.Duration) (string, error) {
	value, err := randHex(tokenBytes)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pairing[value] = &pairingToken{expiresAt: s.now().Add(ttl)}
	return value, nil
}

// ConsumePairing validates a pairing token and, on success, marks it used so it
// can never be redeemed again. It returns false if the token is unknown,
// already used, or expired. The single-use marking happens under the lock, so
// two concurrent /pair requests with the same token cannot both succeed.
func (s *TokenStore) ConsumePairing(value string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok, ok := s.pairing[value]
	if !ok || tok.used || s.now().After(tok.expiresAt) {
		return false
	}
	tok.used = true
	return true
}

// IssueSession creates and stores a new opaque session credential (set as the
// pigo_rc cookie after pairing) and returns its hex value.
func (s *TokenStore) IssueSession() (string, error) {
	cred, err := randHex(tokenBytes)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[cred] = struct{}{}
	return cred, nil
}

// ValidateSession reports whether cred matches a currently-issued session
// credential. Comparison is constant-time to avoid leaking the credential
// through response timing; an empty cred is always rejected.
func (s *TokenStore) ValidateSession(cred string) bool {
	if cred == "" {
		return false
	}
	want := []byte(cred)
	s.mu.Lock()
	defer s.mu.Unlock()
	var matched bool
	for issued := range s.sessions {
		if subtle.ConstantTimeCompare([]byte(issued), want) == 1 {
			matched = true
		}
	}
	return matched
}

// Clear wipes all pairing tokens and session credentials. It is called on
// server shutdown so no secret outlives the remote-control session.
func (s *TokenStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pairing = make(map[string]*pairingToken)
	s.sessions = make(map[string]struct{})
}
