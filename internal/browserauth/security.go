package browserauth

import (
	"context"
	"errors"
	"sync"
	"time"
)

type SecurityConfig struct {
	IdleTimeout                 time.Duration
	MaxConcurrentPasswordHashes int
	LoginFailureWindow          time.Duration
	LoginMaxFailures            int
	LoginLockout                time.Duration
}

const maxLoginFailureEntries = 10000

// errLoginThrottled is internal: HTTP renders the same invalid-credentials
// response, but skipped verification must not count or create an audit event.
var errLoginThrottled = errors.New("login throttled")

type loginFailure struct {
	count       int
	first       time.Time
	lockedUntil time.Time
}

type loginFailures struct {
	mu              sync.Mutex
	entries         map[string]loginFailure
	window, lockout time.Duration
	max             int
	lastCleanup     time.Time
}

func (f *loginFailures) cleanup(now time.Time) {
	if now.Sub(f.lastCleanup) < min(time.Minute, f.window, f.lockout) {
		return
	}
	for key, entry := range f.entries {
		if !entry.lockedUntil.IsZero() {
			if !entry.lockedUntil.After(now) {
				delete(f.entries, key)
			}
		} else if !entry.first.Add(f.window).After(now) {
			delete(f.entries, key)
		}
	}
	f.lastCleanup = now
}

func (f *loginFailures) blocked(username string, now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleanup(now)
	entry := f.entries[username]
	if !entry.lockedUntil.IsZero() && !entry.lockedUntil.After(now) {
		delete(f.entries, username)
	}
	return entry.lockedUntil.After(now)
}

func (f *loginFailures) fail(username string, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleanup(now)
	entry := f.entries[username]
	if entry.lockedUntil.After(now) {
		return
	}
	if entry.first.IsZero() || !entry.first.Add(f.window).After(now) || !entry.lockedUntil.IsZero() {
		entry = loginFailure{first: now}
	}
	if _, exists := f.entries[username]; !exists && len(f.entries) >= maxLoginFailureEntries {
		return
	}
	entry.count++
	if entry.count >= f.max {
		entry.lockedUntil = now.Add(f.lockout)
	}
	f.entries[username] = entry
}

func (f *loginFailures) clear(username string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.entries, username)
}

func (s *Service) verifyBounded(ctx context.Context, username, password, hash string, now time.Time) (bool, error) {
	select {
	case s.passwordSlots <- struct{}{}:
	case <-ctx.Done():
		return false, context.Cause(ctx)
	}
	defer func() { <-s.passwordSlots }()
	if err := ctx.Err(); err != nil {
		return false, context.Cause(ctx)
	}
	if s.failures.blocked(username, now) {
		return false, errLoginThrottled
	}
	// Argon2 is synchronous and cannot be interrupted. Hold the slot until it
	// returns, including on cancellation; never spawn detached hashing work.
	valid, err := s.verifyPassword(password, hash)
	if ctx.Err() != nil {
		return false, context.Cause(ctx)
	}
	return valid, err
}
