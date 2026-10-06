package auth

import (
	"strings"
	"sync"
	"time"
)

// Throttle limits failed sign-ins. Each key (an account name or an address)
// may fail Limit times within Window; after that it's blocked until the oldest
// failure leaves the window.
type Throttle struct {
	Limit  int
	Window time.Duration
	Now    func() time.Time

	mu    sync.Mutex
	fails map[string][]time.Time
}

// NewThrottle returns a throttle allowing limit failures per window.
func NewThrottle(limit int, window time.Duration) *Throttle {
	return &Throttle{Limit: limit, Window: window, Now: time.Now, fails: map[string][]time.Time{}}
}

func (t *Throttle) prune(key string, now time.Time) []time.Time {
	list := t.fails[key]
	i := 0
	for i < len(list) && now.Sub(list[i]) >= t.Window {
		i++
	}
	list = list[i:]
	if len(list) == 0 {
		delete(t.fails, key)
	} else {
		t.fails[key] = list
	}
	return list
}

// Blocked returns how long key must wait, or 0 if it may try now.
func (t *Throttle) Blocked(key string) time.Duration {
	key = strings.ToLower(key)
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.Now()
	list := t.prune(key, now)
	if len(list) < t.Limit {
		return 0
	}
	return t.Window - now.Sub(list[len(list)-t.Limit])
}

// Fail records a failed attempt for key.
func (t *Throttle) Fail(key string) {
	key = strings.ToLower(key)
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.Now()
	t.fails[key] = append(t.prune(key, now), now)
	// Keep memory bounded if someone sprays many names.
	if len(t.fails) > 100_000 {
		for k := range t.fails {
			t.prune(k, now)
		}
	}
}

// Reset forgets key's failures (after a successful sign-in).
func (t *Throttle) Reset(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.fails, strings.ToLower(key))
}
