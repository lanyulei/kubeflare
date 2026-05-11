package middleware

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// WSTicketStore issues short-lived, single-use tickets used to authenticate
// WebSocket / Upgrade requests that cannot carry the normal Bearer / Cookie
// credentials (e.g. when the browser connects to a different origin in the
// development environment, where SameSite=Lax cookies are not forwarded).
//
// A ticket is opaque, random, and tied to a Principal at issue time. It is
// consumed exactly once during the upgrade and then removed from the store.
type WSTicketStore struct {
	ttl     time.Duration
	mu      sync.Mutex
	entries map[string]wsTicketEntry
	now     func() time.Time
}

type wsTicketEntry struct {
	principal Principal
	expiresAt time.Time
}

const (
	// wsTicketDefaultTTL is intentionally short. A ticket is meant to be
	// redeemed by the browser immediately after the issuing HTTP round-trip
	// completes; anything longer than this widens the attack window without
	// any UX benefit.
	wsTicketDefaultTTL = 30 * time.Second
	// wsTicketGCInterval controls how aggressively expired entries are
	// purged. The store also lazily drops expired entries on every read.
	wsTicketGCInterval = 60 * time.Second
)

// NewWSTicketStore returns a fresh in-memory store. A long-running goroutine
// periodically prunes expired entries; callers that want to stop the GC must
// hold their own reference to the cancel signal (the default singleton uses
// the process lifetime, which is acceptable for this use case).
func NewWSTicketStore(ttl time.Duration) *WSTicketStore {
	if ttl <= 0 {
		ttl = wsTicketDefaultTTL
	}
	store := &WSTicketStore{
		ttl:     ttl,
		entries: make(map[string]wsTicketEntry),
		now:     time.Now,
	}
	go store.runGC()
	return store
}

var (
	defaultWSTicketStoreOnce sync.Once
	defaultWSTicketStore     *WSTicketStore
)

// DefaultWSTicketStore returns the process-wide singleton store. It is
// initialised lazily so importing the package does not start a goroutine
// unless the feature is actually used.
func DefaultWSTicketStore() *WSTicketStore {
	defaultWSTicketStoreOnce.Do(func() {
		defaultWSTicketStore = NewWSTicketStore(wsTicketDefaultTTL)
	})
	return defaultWSTicketStore
}

// Issue creates a new ticket for the given principal and returns the opaque
// ticket value plus its TTL in seconds.
func (s *WSTicketStore) Issue(principal Principal) (string, int64, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", 0, err
	}
	ticket := base64.RawURLEncoding.EncodeToString(raw)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[ticket] = wsTicketEntry{
		principal: principal,
		expiresAt: s.now().Add(s.ttl),
	}
	return ticket, int64(s.ttl / time.Second), nil
}

// Consume looks up and removes the ticket from the store. The returned bool
// is true only when the ticket existed and had not expired; the caller MUST
// treat absence as an authentication failure.
func (s *WSTicketStore) Consume(ticket string) (Principal, bool) {
	if ticket == "" {
		return Principal{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[ticket]
	if !ok {
		return Principal{}, false
	}
	delete(s.entries, ticket)
	if !s.now().Before(entry.expiresAt) {
		return Principal{}, false
	}
	return entry.principal, true
}

func (s *WSTicketStore) runGC() {
	ticker := time.NewTicker(wsTicketGCInterval)
	defer ticker.Stop()
	for range ticker.C {
		s.gc()
	}
}

func (s *WSTicketStore) gc() {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.entries {
		if !now.Before(v.expiresAt) {
			delete(s.entries, k)
		}
	}
}
