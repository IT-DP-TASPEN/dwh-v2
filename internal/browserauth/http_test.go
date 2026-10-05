package browserauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/auth"
	"github.com/ibldzn/go-admin/internal/mfa"
	"github.com/ibldzn/go-admin/internal/render"
	"github.com/ibldzn/go-admin/internal/user"
	webfiles "github.com/ibldzn/go-admin/web"
)

type fakeHTTPService struct {
	loginInput       LoginInput
	loginResult      LoginResult
	loginErr         error
	registerInput    RegisterInput
	registered       user.User
	registerErr      error
	resolveHash      [32]byte
	resolvePrincipal Principal
	resolveErr       error
	logoutHash       [32]byte
	logoutErr        error
}

func (service *fakeHTTPService) Login(_ context.Context, input LoginInput, _ time.Time) (LoginResult, error) {
	service.loginInput = input
	return service.loginResult, service.loginErr
}

func (service *fakeHTTPService) Register(_ context.Context, input RegisterInput, _ time.Time) (user.User, error) {
	service.registerInput = input
	return service.registered, service.registerErr
}

func (service *fakeHTTPService) ResolveSession(_ context.Context, hash [32]byte, _ time.Time) (Principal, error) {
	service.resolveHash = hash
	return service.resolvePrincipal, service.resolveErr
}

func (service *fakeHTTPService) Logout(_ context.Context, hash [32]byte) error {
	service.logoutHash = hash
	return service.logoutErr
}

func TestCookieManager(t *testing.T) {
	now := time.Date(2026, 8, 9, 1, 2, 3, 0, time.UTC)
	manager := NewCookieManager("admin_session", true, 30*24*time.Hour)

	t.Run("normal", func(t *testing.T) {
		response := httptest.NewRecorder()
		manager.Set(response, "token", false, now)
		cookie := response.Result().Cookies()[0]
		if cookie.Name != "admin_session" || cookie.Value != "token" || !cookie.HttpOnly || !cookie.Secure || cookie.Path != "/" || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge != 0 || !cookie.Expires.IsZero() || cookie.Domain != "" {
			t.Fatalf("unexpected normal cookie: %+v", cookie)
		}
	})

	t.Run("remember", func(t *testing.T) {
		response := httptest.NewRecorder()
		manager.Set(response, "token", true, now)
		cookie := response.Result().Cookies()[0]
		if cookie.MaxAge != int((30*24*time.Hour)/time.Second) || !cookie.Expires.Equal(now.Add(30*24*time.Hour)) {
			t.Fatalf("unexpected remember cookie: %+v", cookie)
		}
	})

	t.Run("rotated remember uses original absolute expiry", func(t *testing.T) {
		response := httptest.NewRecorder()
		expiresAt := now.Add(20 * 24 * time.Hour)
		manager.SetForSession(response, "rotated", auth.Session{RememberMe: true, ExpiresAt: expiresAt}, now)
		cookie := response.Result().Cookies()[0]
		if cookie.MaxAge != int((20*24*time.Hour)/time.Second) || !cookie.Expires.Equal(expiresAt) {
			t.Fatalf("rotated cookie extended expiry: %+v", cookie)
		}
	})

	t.Run("rotated normal remains browser session cookie", func(t *testing.T) {
		response := httptest.NewRecorder()
		manager.SetForSession(response, "rotated", auth.Session{ExpiresAt: now.Add(24 * time.Hour)}, now)
		cookie := response.Result().Cookies()[0]
		if cookie.MaxAge != 0 || !cookie.Expires.IsZero() {
			t.Fatalf("rotated normal cookie became persistent: %+v", cookie)
		}
	})

	t.Run("clear", func(t *testing.T) {
		response := httptest.NewRecorder()
		manager.Clear(response)
		cookie := response.Result().Cookies()[0]
		if cookie.Value != "" || cookie.MaxAge != -1 || !cookie.Expires.Before(time.Now()) || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
			t.Fatalf("unexpected cleared cookie: %+v", cookie)
		}
	})
}

func TestSafeRedirect(t *testing.T) {
	tests := map[string]string{
		"/":                         "/",
		"/users?page=2":             "/users?page=2",
		"":                          "/",
		"users":                     "/",
		"//evil.example":            "/",
		"https://evil.example/path": "/",
		"javascript:alert(1)":       "/",
		`/\evil.example`:            "/",
		"/%2f%2fevil.example":       "/",
		"/users#section":            "/",
	}
	for input, expected := range tests {
		if actual := SafeRedirect(input); actual != expected {
			t.Errorf("SafeRedirect(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestLoadPrincipal(t *testing.T) {
	t.Run("valid session attaches principal", func(t *testing.T) {
		rawToken := mustToken(t)
		service := &fakeHTTPService{resolvePrincipal: Principal{UserID: 9, Username: "admin"}}
		handler := newTestHTTP(t, service, false)
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.AddCookie(&http.Cookie{Name: "test_session", Value: rawToken})
		response := httptest.NewRecorder()
		called := false
		handler.LoadPrincipal(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			principal, ok := CurrentPrincipal(request.Context())
			called = ok && principal.UserID == 9
			writer.WriteHeader(http.StatusNoContent)
		})).ServeHTTP(response, request)
		if response.Code != http.StatusNoContent || !called || service.resolveHash != auth.HashToken(rawToken) {
			t.Fatalf("principal not attached: status=%d called=%v", response.Code, called)
		}
	})

	for _, test := range []struct {
		name       string
		cookie     string
		resolveErr error
	}{
		{name: "malformed token", cookie: "bad"},
		{name: "unknown session", cookie: mustToken(t), resolveErr: ErrUnauthenticated},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeHTTPService{resolveErr: test.resolveErr}
			handler := newTestHTTP(t, service, false)
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.AddCookie(&http.Cookie{Name: "test_session", Value: test.cookie})
			response := httptest.NewRecorder()
			handler.LoadPrincipal(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusNoContent)
			})).ServeHTTP(response, request)
			cookies := response.Result().Cookies()
			if response.Code != http.StatusNoContent || len(cookies) != 1 || cookies[0].MaxAge != -1 {
				t.Fatalf("expected guest and cleared cookie: status=%d cookies=%+v", response.Code, cookies)
			}
		})
	}

	t.Run("database error stops request", func(t *testing.T) {
		service := &fakeHTTPService{resolveErr: errors.New("database unavailable")}
		handler := newTestHTTP(t, service, false)
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.AddCookie(&http.Cookie{Name: "test_session", Value: mustToken(t)})
		response := httptest.NewRecorder()
		handler.LoadPrincipal(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("next handler must not run")
		})).ServeHTTP(response, request)
		if response.Code != http.StatusInternalServerError || response.Header().Get("Set-Cookie") != "" {
			t.Fatalf("unexpected error response: status=%d cookie=%q", response.Code, response.Header().Get("Set-Cookie"))
		}
	})
}

func TestAuthGuards(t *testing.T) {
	handler := newTestHTTP(t, &fakeHTTPService{}, false)

	t.Run("guest redirect", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/reports?month=8", nil)
		response := httptest.NewRecorder()
		handler.RequireAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(response, request)
		if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login?next=%2Freports%3Fmonth%3D8" {
			t.Fatalf("unexpected redirect: status=%d location=%q", response.Code, response.Header().Get("Location"))
		}
	})

	t.Run("authenticated guest redirected", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/login", nil)
		request = request.WithContext(context.WithValue(request.Context(), principalContextKey{}, Principal{UserID: 1}))
		response := httptest.NewRecorder()
		handler.RequireGuest(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(response, request)
		if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/" {
			t.Fatalf("unexpected redirect: status=%d location=%q", response.Code, response.Header().Get("Location"))
		}
	})
}

func TestLoginHandler(t *testing.T) {
	t.Run("validation preserves safe fields only", func(t *testing.T) {
		service := &fakeHTTPService{}
		handler := newTestHTTP(t, service, true)
		form := url.Values{"username": {"  ADMIN "}, "password": {""}, "remember_me": {"on"}, "next": {"/users"}}
		request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		handler.Login(response, request)
		body := response.Body.String()
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(body, `value="admin"`) || !strings.Contains(body, "checked") || strings.Contains(body, `value="" name="password"`) {
			t.Fatalf("unexpected validation response: status=%d body=%s", response.Code, body)
		}
	})

	t.Run("invalid credentials are generic", func(t *testing.T) {
		service := &fakeHTTPService{loginErr: ErrInvalidCredentials}
		handler := newTestHTTP(t, service, false)
		form := url.Values{"username": {"missing"}, "password": {"not the password"}}
		request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		handler.Login(response, request)
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "Invalid username or password.") {
			t.Fatalf("unexpected credential response: status=%d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("success sets cookie and safe redirect", func(t *testing.T) {
		now := time.Date(2026, 8, 9, 1, 2, 3, 0, time.UTC)
		rawToken := mustToken(t)
		service := &fakeHTTPService{loginResult: LoginResult{Challenge: mfa.Issued{Token: rawToken, Challenge: mfa.Challenge{ExpiresAt: now.Add(5 * time.Minute)}}}}
		handler := newTestHTTP(t, service, false)
		form := url.Values{"username": {"admin"}, "password": {"correct horse battery staple"}, "remember_me": {"on"}, "next": {"https://evil.example"}}
		request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		handler.Login(response, request)
		cookies := response.Result().Cookies()
		if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/mfa" || len(cookies) != 1 || cookies[0].Name != "test_session_mfa" || cookies[0].Path != "/mfa" || cookies[0].Value != rawToken || cookies[0].MaxAge <= 0 || !service.loginInput.RememberMe {
			t.Fatalf("unexpected login success: status=%d location=%q cookies=%+v input=%+v", response.Code, response.Header().Get("Location"), cookies, service.loginInput)
		}
	})

	t.Run("oversize body", func(t *testing.T) {
		handler := newTestHTTP(t, &fakeHTTPService{}, false)
		request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username="+strings.Repeat("a", maxFormBody)))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		handler.Login(response, request)
		if response.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("expected 413, got %d", response.Code)
		}
	})
}

// Keep production handler/service wiring while controlling lockout expiry.
type timedLoginService struct {
	*Service
	now time.Time
}

func (s *timedLoginService) Login(ctx context.Context, input LoginInput, _ time.Time) (LoginResult, error) {
	return s.Service.Login(ctx, input, s.now)
}

func TestLoginFailureAuditsExcludeThrottledRequests(t *testing.T) {
	var publicResponse *httptest.ResponseRecorder
	for _, unknown := range []bool{false, true} {
		name := "known"
		if unknown {
			name = "unknown"
		}
		t.Run(name, func(t *testing.T) {
			users := &fakeUsers{byUsername: user.User{ID: 1, IsActive: true, PasswordHash: "known-hash"}}
			if unknown {
				users.findUsernameErr = user.ErrNotFound
			}
			service := newTestService(t, users, &fakeRoles{}, &fakeSessions{})
			verified := 0
			service.verifyPassword = func(_, hash string) (bool, error) {
				verified++
				expected := "known-hash"
				if unknown {
					expected = service.dummyHash
				}
				if hash != expected {
					t.Errorf("verification hash=%q expected=%q", hash, expected)
				}
				return false, nil
			}
			clock := &timedLoginService{Service: service, now: time.Now().UTC()}
			var events []audit.Event
			handler := newTestHTTPWithAudit(t, clock, false, func(_ context.Context, event audit.Event) error {
				events = append(events, event)
				if event.Action != audit.ActionAuthLoginFailed || event.Attribution.Actor != nil || event.Attribution.Effective != nil || event.Resource != "" || event.ResourceID != 0 || event.Metadata != (audit.LoginFailureMetadata{Username: "user"}) {
					t.Errorf("unsafe failure event: %+v", event)
				}
				return nil
			})
			attempt := func() {
				form := url.Values{"username": {" USER "}, "password": {"wrong"}}
				request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
				request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				response := httptest.NewRecorder()
				handler.Login(response, request)
				if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "Invalid username or password.") {
					t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
				}
				if publicResponse == nil {
					publicResponse = response
				} else if response.Code != publicResponse.Code || response.Body.String() != publicResponse.Body.String() || !reflect.DeepEqual(response.Header(), publicResponse.Header()) {
					t.Fatal("known/unknown, throttled/verified responses differ")
				}
			}
			for i := 1; i <= 5; i++ {
				attempt()
				if verified != i || len(events) != i || service.failures.entries["user"].count != i {
					t.Fatalf("attempt=%d verified=%d audits=%d failure=%+v", i, verified, len(events), service.failures.entries["user"])
				}
			}
			if !service.failures.blocked("user", clock.now) {
				t.Fatal("threshold did not start lockout")
			}
			failure := service.failures.entries["user"]
			attempt()
			if verified != 5 || len(events) != 5 || service.failures.entries["user"] != failure {
				t.Fatal("locked request verified, audited or changed failure state")
			}
			clock.now = failure.lockedUntil
			attempt()
			if verified != 6 || len(events) != 6 || service.failures.entries["user"].count != 1 {
				t.Fatal("expired lockout did not restore verified, audited failures")
			}
		})
	}
}

type hashSlotWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *hashSlotWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestLoginHashSlotWaiterDoesNotCountOrAuditNewLockout(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		name := "known"
		if unknown {
			name = "unknown"
		}
		t.Run(name, func(t *testing.T) {
			users := &fakeUsers{byUsername: user.User{ID: 1, IsActive: true, PasswordHash: "hash"}}
			if unknown {
				users.findUsernameErr = user.ErrNotFound
			}
			service := newTestService(t, users, &fakeRoles{}, &fakeSessions{})
			service.passwordSlots = make(chan struct{}, 1)
			service.passwordSlots <- struct{}{}
			verified := 0
			service.verifyPassword = func(string, string) (bool, error) { verified++; return false, nil }
			now := time.Now().UTC()
			for i := 0; i < 4; i++ {
				service.failures.fail("user", now)
			}
			audits := 0
			handler := newTestHTTPWithAudit(t, &timedLoginService{Service: service, now: now}, false, func(context.Context, audit.Event) error { audits++; return nil })
			form := url.Values{"username": {" USER "}, "password": {"wrong"}}
			base, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx := &hashSlotWaitContext{Context: base, waiting: make(chan struct{})}
			request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode())).WithContext(ctx)
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); handler.Login(response, request) }()
			select {
			case <-ctx.waiting: // initial throttle check passed; password slot is full
			case <-base.Done():
				t.Fatal("request never waited for password slot")
			}
			service.failures.fail("user", now) // another completed attempt reaches threshold
			failure := service.failures.entries["user"]
			<-service.passwordSlots
			select {
			case <-done:
			case <-base.Done():
				t.Fatal("waiting request did not finish")
			}
			if verified != 0 || audits != 0 || service.failures.entries["user"] != failure || len(service.passwordSlots) != 0 {
				t.Fatalf("verified=%d audits=%d failure=%+v slots=%d", verified, audits, service.failures.entries["user"], len(service.passwordSlots))
			}
			if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "Invalid username or password.") {
				t.Fatalf("unexpected public response: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestRegistrationHandler(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		service := &fakeHTTPService{registered: user.User{ID: 3}}
		handler := newTestHTTP(t, service, true)
		form := url.Values{"name": {"User"}, "username": {"USER"}, "password": {"correct horse battery staple"}, "password_confirmation": {"correct horse battery staple"}}
		request := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		handler.Register(response, request)
		if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login?notice=registered" || service.registerInput.Username != "user" {
			t.Fatalf("unexpected registration: status=%d location=%q input=%+v", response.Code, response.Header().Get("Location"), service.registerInput)
		}
	})

	t.Run("duplicate username", func(t *testing.T) {
		service := &fakeHTTPService{registerErr: user.ErrUsernameTaken}
		handler := newTestHTTP(t, service, true)
		form := url.Values{"name": {"User"}, "username": {"user"}, "password": {"correct horse battery staple"}, "password_confirmation": {"correct horse battery staple"}}
		request := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		handler.Register(response, request)
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "Username is already taken.") {
			t.Fatalf("unexpected duplicate response: status=%d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("passwords are never rendered", func(t *testing.T) {
		handler := newTestHTTP(t, &fakeHTTPService{}, true)
		form := url.Values{"name": {"User"}, "username": {"user"}, "password": {"secret-value"}, "password_confirmation": {"different-secret"}}
		request := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		handler.Register(response, request)
		body := response.Body.String()
		if response.Code != http.StatusUnprocessableEntity || strings.Contains(body, "secret-value") || strings.Contains(body, "different-secret") {
			t.Fatalf("password leaked in response: %s", body)
		}
	})
}

func TestAuthenticationAuditAttributionAndBestEffortFailure(t *testing.T) {
	var events []audit.Event
	appendAudit := func(_ context.Context, event audit.Event) error {
		events = append(events, event)
		return errors.New("audit unavailable")
	}
	t.Run("failed login has no account identity or secrets", func(t *testing.T) {
		events = nil
		handler := newTestHTTPWithAudit(t, &fakeHTTPService{loginErr: ErrInvalidCredentials}, false, appendAudit)
		form := url.Values{"username": {" UNKNOWN "}, "password": {"never-record-this-password"}}
		request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		handler.Login(response, request)
		if response.Code != http.StatusUnprocessableEntity || len(events) != 1 {
			t.Fatalf("status=%d events=%v", response.Code, events)
		}
		event := events[0]
		if event.Action != audit.ActionAuthLoginFailed || event.Attribution.Actor != nil || event.Attribution.Effective != nil || event.ResourceID != 0 || event.Resource != "" {
			t.Fatalf("unsafe failure attribution: %+v", event)
		}
		encoded, _ := json.Marshal(event.Metadata)
		if string(encoded) != `{"username":"unknown"}` {
			t.Fatalf("unexpected failure metadata: %s", encoded)
		}
	})

	t.Run("password success has no login audit", func(t *testing.T) {
		events = nil
		token := mustToken(t)
		service := &fakeHTTPService{loginResult: LoginResult{Challenge: mfa.Issued{Token: token, Challenge: mfa.Challenge{ExpiresAt: time.Now().Add(5 * time.Minute)}}}}
		handler := newTestHTTPWithAudit(t, service, false, appendAudit)
		request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(url.Values{"username": {"user"}, "password": {"password"}}.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		handler.Login(response, request)
		if response.Code != 303 || len(events) != 0 {
			t.Fatal("password-only login audited as authenticated")
		}
	})

	t.Run("registration is unauthenticated", func(t *testing.T) {
		events = nil
		service := &fakeHTTPService{registered: user.User{ID: 8, Username: "new-user"}}
		handler := newTestHTTPWithAudit(t, service, true, appendAudit)
		form := url.Values{"name": {"New User"}, "username": {"new-user"}, "password": {"long-enough-password"}, "password_confirmation": {"long-enough-password"}}
		request := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		handler.Register(response, request)
		if response.Code != http.StatusSeeOther || len(events) != 1 || events[0].Action != audit.ActionAuthRegistration || events[0].Attribution.Actor != nil || events[0].Attribution.Effective != nil || events[0].ResourceID != 8 {
			t.Fatalf("unexpected registration audit: status=%d events=%+v", response.Code, events)
		}
	})

	t.Run("logout snapshots principal before revocation", func(t *testing.T) {
		events = nil
		service := &fakeHTTPService{}
		handler := newTestHTTPWithAudit(t, service, false, appendAudit)
		principal := Principal{UserID: 9, Username: "effective", Actor: Identity{UserID: 1, Username: "admin"}, IsImpersonating: true}
		request := httptest.NewRequest(http.MethodPost, "/logout", nil)
		request = request.WithContext(context.WithValue(request.Context(), principalContextKey{}, principal))
		request.AddCookie(&http.Cookie{Name: "test_session", Value: mustToken(t)})
		response := httptest.NewRecorder()
		handler.Logout(response, request)
		if response.Code != http.StatusSeeOther || len(events) != 1 {
			t.Fatalf("audit failure changed logout: status=%d events=%d", response.Code, len(events))
		}
		event := events[0]
		if event.Action != audit.ActionAuthLogout || event.Attribution.Actor == nil || event.Attribution.Effective == nil || event.Attribution.Actor.UserID != 1 || event.Attribution.Actor.Username != "admin" || event.Attribution.Effective.UserID != 9 || event.Attribution.Effective.Username != "effective" {
			t.Fatalf("unexpected logout attribution: %+v", event)
		}
	})
}

func TestLogoutHandler(t *testing.T) {
	rawToken := mustToken(t)

	t.Run("success revokes and clears", func(t *testing.T) {
		service := &fakeHTTPService{}
		handler := newTestHTTP(t, service, false)
		request := httptest.NewRequest(http.MethodPost, "/logout", nil)
		request.AddCookie(&http.Cookie{Name: "test_session", Value: rawToken})
		response := httptest.NewRecorder()
		handler.Logout(response, request)
		cookies := response.Result().Cookies()
		if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login" || service.logoutHash != auth.HashToken(rawToken) || len(cookies) != 2 || cookies[0].MaxAge != -1 {
			t.Fatalf("unexpected logout: status=%d hash=%x cookies=%+v", response.Code, service.logoutHash, cookies)
		}
	})

	t.Run("repository failure is not success", func(t *testing.T) {
		service := &fakeHTTPService{logoutErr: errors.New("database unavailable")}
		handler := newTestHTTP(t, service, false)
		request := httptest.NewRequest(http.MethodPost, "/logout", nil)
		request.AddCookie(&http.Cookie{Name: "test_session", Value: rawToken})
		response := httptest.NewRecorder()
		handler.Logout(response, request)
		if response.Code != http.StatusInternalServerError || response.Header().Get("Set-Cookie") != "" {
			t.Fatalf("unexpected logout failure: status=%d cookie=%q", response.Code, response.Header().Get("Set-Cookie"))
		}
	})

	t.Run("missing session is safe", func(t *testing.T) {
		handler := newTestHTTP(t, &fakeHTTPService{}, false)
		request := httptest.NewRequest(http.MethodPost, "/logout", nil)
		response := httptest.NewRecorder()
		handler.Logout(response, request)
		cookies := response.Result().Cookies()
		if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login" || len(cookies) != 2 || cookies[0].MaxAge != -1 {
			t.Fatalf("unexpected missing-session logout: status=%d cookies=%+v", response.Code, cookies)
		}
	})
}

func newTestHTTP(t *testing.T, service authenticationService, allowRegistration bool) *HTTP {
	return newTestHTTPWithAudit(t, service, allowRegistration, nil)
}

func newTestHTTPWithAudit(t *testing.T, service authenticationService, allowRegistration bool, appendAudit func(context.Context, audit.Event) error) *HTTP {
	t.Helper()
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewHTTP(service, renderer, NewCookieManager("test_session", false, 30*24*time.Hour), "Go Admin", allowRegistration, logger, appendAudit, render.NewErrorResponder(renderer, "Go Admin", logger))
}

func mustToken(t *testing.T) string {
	t.Helper()
	token, err := auth.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	return token
}
