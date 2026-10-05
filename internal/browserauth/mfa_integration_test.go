//go:build integration

package browserauth_test

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/auth"
	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/mfa"
	"github.com/ibldzn/go-admin/internal/render"
	"github.com/ibldzn/go-admin/internal/secretcrypto"
	"github.com/ibldzn/go-admin/internal/server"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
	"github.com/ibldzn/go-admin/internal/user"
	webfiles "github.com/ibldzn/go-admin/web"
)

func TestMandatoryMFAHTTPFlowCacheCSPAndNoPostReplay(t *testing.T) {
	db := integrationdb.Open(t)
	integrationdb.Reset(t, db, nil)
	ctx := context.Background()
	now := time.Now().UTC()
	role := integrationdb.Role(t, db, access.UserRoleSlug)
	u := integrationdb.User(t, db, "http-mfa", role.ID, true)
	password := "correct horse battery staple"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE users SET password_hash=? WHERE id=?`, hash, u.ID); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service, err := browserauth.NewService(user.NewRepository(db), access.NewRepository(db), auth.NewSessionRepository(db), time.Hour, 30*24*time.Hour, logger)
	if err != nil {
		t.Fatal(err)
	}
	store := &mfa.Store{DB: db, Cipher: secretcrypto.New([32]byte{8}), Lifetime: time.Hour, RememberLifetime: 30 * 24 * time.Hour, IdleTimeout: 2 * time.Hour}
	service.EnableMFA(store)
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	cookies := browserauth.NewCookieManager("http_session", false, 30*24*time.Hour)
	errors := render.NewErrorResponder(renderer, "DWH", logger)
	h := browserauth.NewHTTP(service, renderer, cookies, "DWH", false, logger, nil, errors)
	h.EnableMFA(store)
	static, _ := fs.Sub(webfiles.Files, "static")
	router := server.NewRouter(server.RouterDependencies{Authentication: h, Errors: errors, StaticFiles: static, RegisterAuthenticated: func(r chi.Router) {
		r.Get("/", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("application")) })
		r.Get("/sensitive", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("safe form")) })
		r.Post("/sensitive", func(w http.ResponseWriter, r *http.Request) {
			if browserauth.RequireRecentMFA(w, r, "/sensitive") {
				w.Write([]byte("mutation"))
			}
		})
	}})
	request := func(method, path string, values url.Values, cookie []*http.Cookie) *httptest.ResponseRecorder {
		body := strings.NewReader(values.Encode())
		r := httptest.NewRequest(method, path, body)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, c := range cookie {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	var sessions int
	wrong := request("POST", "/login", url.Values{"username": {u.Username}, "password": {"wrong"}}, nil)
	db.Get(&sessions, `SELECT COUNT(*) FROM sessions`)
	var challenges int
	db.Get(&challenges, `SELECT COUNT(*) FROM mfa_challenges`)
	if wrong.Code != 422 || sessions != 0 || challenges != 0 {
		t.Fatal("wrong password challenge/session")
	}
	passwordSuccess := request("POST", "/login", url.Values{"username": {u.Username}, "password": {password}, "remember_me": {"1"}, "next": {"https://evil.example"}}, nil)
	pre := passwordSuccess.Result().Cookies()
	if passwordSuccess.Code != 303 || passwordSuccess.Header().Get("Location") != "/mfa" || len(pre) != 1 || pre[0].Name != "http_session_mfa" || pre[0].Path != "/mfa" || !pre[0].HttpOnly {
		t.Fatal("pre-auth cookie")
	}
	if response := request("GET", "/", nil, pre); response.Code != 303 {
		t.Fatal("challenge accepted by normal middleware")
	}
	setup := request("GET", "/mfa", nil, pre)
	secretMatch := regexp.MustCompile(`id="manual-secret"[^>]*value="([A-Z2-7]+)"`).FindStringSubmatch(setup.Body.String())
	if len(secretMatch) != 2 || setup.Header().Get("Cache-Control") != "no-store" || setup.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("setup/cache")
	}
	qr := request("GET", "/mfa/qr", nil, pre)
	if qr.Code != 200 || qr.Header().Get("Content-Type") != "image/png" || qr.Header().Get("Cache-Control") != "no-store" || !strings.Contains(qr.Header().Get("Content-Security-Policy"), "img-src 'self'") {
		t.Fatal("QR/cache/CSP")
	}
	if q := request("GET", "/mfa/qr", nil, nil); q.Code != 401 {
		t.Fatal("QR accessible without challenge")
	}
	code, _ := mfa.Code(secretMatch[1], time.Now().Unix()/30)
	completed := request("POST", "/mfa", url.Values{"code": {code}}, pre)
	codes := regexp.MustCompile(`[A-Z2-7]{5}-[A-Z2-7]{5}-[A-Z2-7]{5}-[A-Z2-7]{5}-[A-Z2-7]{6}`).FindAllString(completed.Body.String(), -1)
	if completed.Code != 200 || len(codes) != 10 || completed.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("one-time recovery response", completed.Code)
	}
	var sessionCookie []*http.Cookie
	for _, c := range completed.Result().Cookies() {
		if c.Name == "http_session" {
			sessionCookie = append(sessionCookie, c)
		}
	}
	if len(sessionCookie) != 1 {
		t.Fatal("session missing")
	}
	p, err := service.ResolveSession(ctx, auth.HashToken(sessionCookie[0].Value), time.Now().UTC())
	if err != nil || p.MFAVerifiedAt.Before(now) {
		t.Fatal("session MFA assurance", err)
	}
	if result := request("GET", "/mfa", nil, pre); result.Code != 303 || strings.Contains(result.Body.String(), codes[0]) {
		t.Fatal("recovery redisplay")
	}
	if _, err = db.Exec(`UPDATE sessions SET mfa_verified_at=? WHERE id=?`, time.Now().Add(-11*time.Minute), p.SessionID); err != nil {
		t.Fatal(err)
	}
	stale := request("POST", "/sensitive", url.Values{"password": {"DO_NOT_STORE"}, "sql": {"SECRET_SQL"}}, sessionCookie)
	if stale.Code != 303 || !strings.HasPrefix(stale.Header().Get("Location"), "/mfa/step-up?") {
		t.Fatal("stale POST bypass")
	}
	begin := request("GET", stale.Header().Get("Location"), nil, sessionCookie)
	stepCookies := append(sessionCookie, begin.Result().Cookies()...)
	// Only a known safe GET destination is persisted. No POST payload survives.
	var next string
	if err = db.Get(&next, `SELECT next_path FROM mfa_challenges WHERE purpose='step_up' AND consumed_at IS NULL`); err != nil || next != "/sensitive" {
		t.Fatal("POST replay state", err)
	}
	verified := request("POST", "/mfa", url.Values{"code": {codes[0]}}, stepCookies)
	if verified.Code != 303 || verified.Header().Get("Location") != "/sensitive" {
		t.Fatal("step-up recovery")
	}
	if result := request("POST", "/sensitive", nil, sessionCookie); result.Code != 200 || result.Body.String() != "mutation" {
		t.Fatal("fresh mutation")
	}
	logout := request("POST", "/logout", nil, sessionCookie)
	if logout.Code != 303 {
		t.Fatal("logout")
	}
	// Remember Me still requires a fresh factor challenge.
	second := request("POST", "/login", url.Values{"username": {u.Username}, "password": {password}, "remember_me": {"1"}}, nil)
	if second.Code != 303 || second.Header().Get("Location") != "/mfa" {
		t.Fatal("second login MFA")
	}
	recovered := request("POST", "/mfa", url.Values{"code": {codes[1]}}, second.Result().Cookies())
	if recovered.Code != 303 || recovered.Header().Get("Location") != "/" {
		t.Fatal("recovery login")
	}
	var auditJSON []string
	db.Select(&auditJSON, `SELECT COALESCE(CAST(metadata AS CHAR),'') FROM audit_logs`)
	for _, entry := range auditJSON {
		if strings.Contains(entry, secretMatch[1]) || strings.Contains(entry, codes[0]) || strings.Contains(entry, pre[0].Value) {
			t.Fatal("secret audit")
		}
	}
}
