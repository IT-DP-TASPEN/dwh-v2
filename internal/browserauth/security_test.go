package browserauth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/auth"
	"github.com/ibldzn/go-admin/internal/user"
)

func TestLoginThrottleCounterLockoutExpiryAndSuccessReset(t *testing.T) {
	now := time.Now().UTC()
	users := &fakeUsers{byUsername: user.User{ID: 1, IsActive: true, PasswordHash: "hash"}}
	service := newTestService(t, users, &fakeRoles{}, &fakeSessions{})
	verified := 0
	valid := false
	service.verifyPassword = func(string, string) (bool, error) { verified++; return valid, nil }
	input := LoginInput{Username: "  USER ", Password: "wrong"}
	for i := 0; i < 5; i++ {
		if _, err := service.Login(context.Background(), input, now); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatal(err)
		}
	}
	if service.failures.entries["user"].count != 5 {
		t.Fatal("failure count")
	}
	valid = true
	if _, err := service.Login(context.Background(), input, now.Add(time.Minute)); !errors.Is(err, errLoginThrottled) || errors.Is(err, ErrInvalidCredentials) || verified != 5 {
		t.Fatalf("lockout err=%v verified=%d", err, verified)
	}
	if _, err := service.Login(context.Background(), input, now.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(service.failures.entries) != 0 {
		t.Fatal("success did not reset")
	}
	valid = false
	if _, err := service.Login(context.Background(), input, now.Add(16*time.Minute)); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal(err)
	}
	valid = true
	if _, err := service.Login(context.Background(), input, now.Add(17*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(service.failures.entries) != 0 {
		t.Fatal("success below threshold did not reset")
	}
	service.failures.fail("user", now)
	service.failures.fail("user", now.Add(15*time.Minute))
	if service.failures.entries["user"].count != 1 {
		t.Fatal("failure window did not expire")
	}
}

func TestLoginThrottleBoundedMemory(t *testing.T) {
	now := time.Now()
	f := loginFailures{entries: make(map[string]loginFailure), window: time.Minute, lockout: 2 * time.Minute, max: 1}
	for i := 0; i < maxLoginFailureEntries+10; i++ {
		f.fail(fmt.Sprint(i), now)
	}
	if len(f.entries) != maxLoginFailureEntries || f.blocked("new", now) {
		t.Fatalf("entries=%d", len(f.entries))
	}
	f.fail("new", now)
	if len(f.entries) != maxLoginFailureEntries {
		t.Fatal("overflow failure grew the map")
	}
	if !f.blocked("0", now.Add(time.Minute)) {
		t.Fatal("window cleanup erased live lockout")
	}
	if f.blocked("new", now.Add(2*time.Minute)) || len(f.entries) != 0 {
		t.Fatal("stale entries not reclaimed")
	}
	f.fail("new", now.Add(2*time.Minute))
	if len(f.entries) != 1 || f.entries["new"].count != 1 || !f.blocked("new", now.Add(2*time.Minute)) {
		t.Fatal("new failures not tracked after stale cleanup")
	}
}

func TestSaturatedLoginThrottleStillVerifiesUntrackedUsername(t *testing.T) {
	service := newTestService(t, &fakeUsers{byUsername: user.User{ID: 1, IsActive: true, PasswordHash: "hash"}}, &fakeRoles{}, &fakeSessions{})
	now := time.Now()
	for i := 0; i < maxLoginFailureEntries; i++ {
		service.failures.entries[fmt.Sprint(i)] = loginFailure{count: 5, first: now, lockedUntil: now.Add(time.Minute)}
	}
	verified := 0
	service.verifyPassword = func(string, string) (bool, error) {
		verified++
		if len(service.passwordSlots) != 1 {
			t.Fatal("verification bypassed password slots")
		}
		return false, nil
	}
	_, err := service.Login(context.Background(), LoginInput{Username: "new", Password: "wrong"}, now)
	if !errors.Is(err, ErrInvalidCredentials) || verified != 1 {
		t.Fatalf("verification count=%d error=%v", verified, err)
	}
	if len(service.failures.entries) != maxLoginFailureEntries || service.failures.entries["new"].count != 0 || !service.failures.blocked("0", now) {
		t.Fatal("overflow changed the bound or a live lockout")
	}
}

func TestPasswordSlotsBoundConcurrencyAndReleaseOnErrorCancellation(t *testing.T) {
	service := newTestService(t, &fakeUsers{}, &fakeRoles{}, &fakeSessions{})
	service.passwordSlots = make(chan struct{}, 2)
	var active, peak atomic.Int32
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	service.verifyPassword = func(string, string) (bool, error) {
		n := active.Add(1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		started <- struct{}{}
		<-release
		active.Add(-1)
		return false, errors.New("hash failure")
	}
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Go(func() { _, _ = service.verifyBounded(context.Background(), "u", "password", "hash", time.Now()) })
	}
	<-started
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.verifyBounded(ctx, "u", "password", "hash", time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	group.Wait()
	if peak.Load() > 2 || len(service.passwordSlots) != 0 {
		t.Fatalf("peak=%d slots=%d", peak.Load(), len(service.passwordSlots))
	}
	ctx, cancel = context.WithCancel(context.Background())
	service.verifyPassword = func(string, string) (bool, error) { cancel(); return true, nil }
	if _, err := service.verifyBounded(ctx, "u", "password", "hash", time.Now()); !errors.Is(err, context.Canceled) || len(service.passwordSlots) != 0 {
		t.Fatalf("error=%v slots=%d", err, len(service.passwordSlots))
	}
}

func TestUnknownUsernameDummyHashStopsOnlyDuringLockout(t *testing.T) {
	service := newTestService(t, &fakeUsers{findUsernameErr: user.ErrNotFound}, &fakeRoles{}, &fakeSessions{})
	count := 0
	service.verifyPassword = func(_, hash string) (bool, error) {
		if hash != service.dummyHash {
			t.Fatal("dummy hash missing")
		}
		count++
		return false, nil
	}
	now := time.Now()
	for i := 0; i < 6; i++ {
		_, err := service.Login(context.Background(), LoginInput{Username: "missing", Password: "wrong"}, now)
		expected := ErrInvalidCredentials
		if i == 5 {
			expected = errLoginThrottled
		}
		if !errors.Is(err, expected) {
			t.Fatal(err)
		}
	}
	if count != 5 {
		t.Fatalf("dummy verifications=%d", count)
	}
	_, _ = service.Login(context.Background(), LoginInput{Username: "missing", Password: "wrong"}, now.Add(15*time.Minute))
	if count != 6 {
		t.Fatal("expired lockout did not restore dummy verification")
	}
}

func TestSessionIdleAndAbsoluteBounds(t *testing.T) {
	now := time.Now().UTC()
	for _, test := range []struct {
		name                   string
		lastSeen, expires      time.Time
		remember, valid, touch bool
	}{
		{"active", now.Add(-time.Minute), now.Add(time.Hour), false, true, false},
		{"absolute", now, now, false, false, false},
		{"idle", now.Add(-2 * time.Hour), now.Add(time.Hour), false, false, false},
		{"refresh", now.Add(-10 * time.Minute), now.Add(time.Hour), false, true, true},
		{"remember idle", now.Add(-2 * time.Hour), now.Add(30 * 24 * time.Hour), true, false, false},
		{"remember active", now.Add(-time.Minute), now.Add(30 * 24 * time.Hour), true, true, false},
		{"remember absolute", now, now, true, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			sessions := &fakeSessions{found: auth.Session{ID: 1, UserID: 1, LastSeenAt: test.lastSeen, ExpiresAt: test.expires, RememberMe: test.remember}}
			service := newTestService(t, &fakeUsers{byID: user.User{ID: 1, RoleID: 1, IsActive: true}}, &fakeRoles{byID: access.Role{ID: 1, Slug: access.AdminRoleSlug}}, sessions)
			hash := auth.HashToken("token")
			_, err := service.ResolveSession(context.Background(), hash, now)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v error=%v", test.valid, err)
			}
			if !test.valid && sessions.revokedHash != hash {
				t.Fatal("unusable session not revoked")
			}
			if test.touch != !sessions.touchedAt.IsZero() {
				t.Fatalf("touch=%v", sessions.touchedAt)
			}
		})
	}
}
