package browserauth

import (
	"database/sql"
	"errors"
	"image/png"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/qr"
	"github.com/go-chi/chi/v5"
	"github.com/ibldzn/go-admin/internal/mfa"
	"github.com/ibldzn/go-admin/internal/render"
)

const mfaCookiePath = "/mfa"

// RequireRecentMFA runs after route RBAC. The destination is a known GET form;
// request bodies and their query strings are never copied into challenge state.
func RequireRecentMFA(writer http.ResponseWriter, request *http.Request, next string) bool {
	principal, ok := CurrentPrincipal(request.Context())
	if !ok {
		http.Error(writer, "Unauthorized", http.StatusUnauthorized)
		return false
	}
	if mfa.Recent(principal.MFAVerifiedAt, time.Now().UTC()) {
		return true
	}
	location := "/mfa/step-up?" + url.Values{"next": {SafeRedirect(next)}, "resubmit": {"1"}}.Encode()
	if request.Header.Get("HX-Request") == "true" {
		writer.Header().Set("HX-Redirect", location)
		http.Error(writer, "MFA verification expired. Submit the action again after verification.", http.StatusUnauthorized)
	} else {
		http.Redirect(writer, request, location, http.StatusSeeOther)
	}
	return false
}

func (h *HTTP) EnableMFA(store *mfa.Store) { h.mfa = store }

// AuthenticatedPageRenderer preserves the application's shell without making
// browserauth depend on the adminshell package.
type AuthenticatedPageRenderer interface {
	RenderPage(http.ResponseWriter, *http.Request, int, string, string, any)
}

func (h *HTTP) SetAuthenticatedPageRenderer(renderer AuthenticatedPageRenderer) {
	h.authenticatedPages = renderer
}

func mfaHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Cache-Control", "no-store")
	// no-referrer makes form POST Origin null on HTTP LAN browsers, which
	// lack Sec-Fetch-Site. Keep paths/query strings private and Origin usable.
	writer.Header().Set("Referrer-Policy", "strict-origin")
}
func (h *HTTP) challengeCookie(value string) *http.Cookie {
	return &http.Cookie{Name: h.cookies.name + "_mfa", Value: value, Path: mfaCookiePath, HttpOnly: true, Secure: h.cookies.secure, SameSite: http.SameSiteLaxMode}
}
func (h *HTTP) setChallenge(writer http.ResponseWriter, issued mfa.Issued) {
	cookie := h.challengeCookie(issued.Token)
	cookie.Expires = issued.Challenge.ExpiresAt
	cookie.MaxAge = max(1, int(time.Until(cookie.Expires).Seconds()))
	http.SetCookie(writer, cookie)
}
func (h *HTTP) clearChallenge(writer http.ResponseWriter) {
	cookie := h.challengeCookie("")
	cookie.MaxAge = -1
	cookie.Expires = time.Unix(1, 0)
	http.SetCookie(writer, cookie)
}
func (h *HTTP) readChallenge(request *http.Request) string {
	cookie, err := request.Cookie(h.cookies.name + "_mfa")
	if err != nil || !validToken(cookie.Value) {
		return ""
	}
	return cookie.Value
}

type MFAForm struct {
	Authenticated  bool
	Secret         string
	Enrollment     bool
	Rotation       bool
	Management     bool
	Regenerate     bool
	Error          string
	Next           string
	RecoveryCodes  []string
	Logout         bool
	Status         mfa.Status
	TargetID       uint64
	TargetName     string
	TargetUsername string
}

func (h *HTTP) renderMFA(writer http.ResponseWriter, request *http.Request, status int, page string, form MFAForm) {
	mfaHeaders(writer)
	title := "Verify your identity"
	switch {
	case page == "mfa_security":
		title = "Security / MFA"
	case page == "mfa_admin_reset":
		title = "Reset MFA"
	case page == "mfa_recovery":
		title = "Save your recovery codes"
	case form.Enrollment && form.Rotation:
		title = "Set up new authenticator"
	case form.Enrollment:
		title = "Set up authenticator"
	case form.Authenticated:
		title = "Confirm your identity"
	}
	if form.Authenticated {
		if h.authenticatedPages == nil {
			h.internalError(writer, request, "render authenticated MFA", errors.New("authenticated page renderer unavailable"))
			return
		}
		h.authenticatedPages.RenderPage(writer, request, status, page, title, form)
		return
	}
	if err := h.renderer.RenderPageWithLayout(writer, status, page, "auth", render.PageData{Title: title, AppName: h.appName, Data: form}); err != nil {
		h.internalError(writer, request, "render MFA", err)
	}
}
func (h *HTTP) challenge(request *http.Request) (mfa.Challenge, error) {
	if h.mfa == nil {
		return mfa.Challenge{}, mfa.ErrInvalid
	}
	c, err := h.mfa.Get(request.Context(), h.readChallenge(request), time.Now().UTC())
	if err != nil {
		return c, err
	}
	p, ok := CurrentPrincipal(request.Context())
	if c.SessionID.Valid {
		if !ok || p.SessionID != uint64(c.SessionID.Int64) || p.Actor.UserID != c.UserID {
			return c, mfa.ErrInvalid
		}
		if c.Purpose != mfa.StepUp && p.IsImpersonating {
			return c, mfa.ErrInvalid
		}
	} else if ok {
		return c, mfa.ErrInvalid
	}
	return c, nil
}
func (h *HTTP) MFA(writer http.ResponseWriter, request *http.Request) {
	mfaHeaders(writer)
	c, err := h.challenge(request)
	if err != nil {
		h.clearChallenge(writer)
		http.Redirect(writer, request, "/login", 303)
		return
	}
	h.showChallenge(writer, request, c, "")
}
func (h *HTTP) showChallenge(writer http.ResponseWriter, request *http.Request, c mfa.Challenge, message string) {
	form := MFAForm{Authenticated: c.SessionID.Valid, Enrollment: c.Purpose == mfa.Enrollment || c.Purpose == mfa.Rotation, Rotation: c.Purpose == mfa.Rotation, Management: c.Purpose == mfa.RotateAuthorize || c.Purpose == mfa.Regenerate, Regenerate: c.Purpose == mfa.Regenerate, Error: message, Next: SafeRedirect(c.Next)}
	if form.Enrollment {
		secret, err := h.mfa.PendingSecret(c)
		if err != nil {
			h.internalError(writer, request, "decrypt pending MFA", err)
			return
		}
		form.Secret = secret
	}
	status := http.StatusOK
	if message != "" {
		status = http.StatusUnprocessableEntity
	}
	h.renderMFA(writer, request, status, "mfa", form)
}
func (h *HTTP) QR(writer http.ResponseWriter, request *http.Request) {
	mfaHeaders(writer)
	c, err := h.challenge(request)
	if err != nil {
		http.Error(writer, "Unauthorized", 401)
		return
	}
	secret, err := h.mfa.PendingSecret(c)
	if err != nil {
		http.Error(writer, "Unauthorized", 401)
		return
	}
	var username string
	if err := h.mfa.DB.GetContext(request.Context(), &username, `SELECT username FROM users WHERE id=?`, c.UserID); err != nil {
		h.internalError(writer, request, "QR identity", err)
		return
	}
	code, err := qr.Encode(mfa.ProvisioningURI(h.appName, username, secret), qr.M, qr.Auto)
	if err != nil {
		http.Error(writer, "QR unavailable. Use manual setup.", 500)
		return
	}
	scaled, err := barcode.Scale(code, 256, 256)
	if err != nil {
		http.Error(writer, "QR unavailable. Use manual setup.", 500)
		return
	}
	writer.Header().Set("Content-Type", "image/png")
	// Never report errors that could include provisioning material.
	_ = png.Encode(writer, scaled)
}
func (h *HTTP) VerifyMFA(writer http.ResponseWriter, request *http.Request) {
	mfaHeaders(writer)
	if !parseForm(writer, request) {
		return
	}
	c, err := h.challenge(request)
	if err != nil {
		h.clearChallenge(writer)
		http.Redirect(writer, request, "/login", 303)
		return
	}
	var sessionID uint64
	if c.SessionID.Valid {
		sessionID = uint64(c.SessionID.Int64)
	}
	passwordHash := ""
	if c.Purpose == mfa.RotateAuthorize || c.Purpose == mfa.Regenerate {
		service, ok := h.service.(*Service)
		if !ok {
			h.internalError(writer, request, "MFA password confirmation", errors.New("password verifier unavailable"))
			return
		}
		passwordHash, err = service.ConfirmPassword(request.Context(), c.UserID, request.PostFormValue("password"), time.Now().UTC())
		if err != nil && !errors.Is(err, ErrInvalidCredentials) && !errors.Is(err, errLoginThrottled) {
			h.internalError(writer, request, "MFA password confirmation", err)
			return
		}
	}
	result, err := h.mfa.Verify(request.Context(), h.readChallenge(request), strings.TrimSpace(request.PostFormValue("code")), sessionID, passwordHash, time.Now().UTC())
	if errors.Is(err, mfa.ErrExhausted) {
		h.clearChallenge(writer)
		if c.SessionID.Valid {
			h.cookies.Clear(writer)
		}
		http.Redirect(writer, request, "/login?notice=mfa-exhausted", 303)
		return
	}
	if errors.Is(err, mfa.ErrInvalid) {
		current, challengeErr := h.challenge(request)
		if challengeErr != nil {
			h.clearChallenge(writer)
			http.Redirect(writer, request, "/login", http.StatusSeeOther)
			return
		}
		h.showChallenge(writer, request, current, "Invalid verification code.")
		return
	}
	if err != nil {
		h.internalError(writer, request, "verify MFA", err)
		return
	}
	h.clearChallenge(writer)
	if result.Pending != nil {
		h.setChallenge(writer, *result.Pending)
		http.Redirect(writer, request, "/mfa", 303)
		return
	}
	if result.RawToken != "" {
		h.cookies.SetForSession(writer, result.RawToken, result.Session, time.Now().UTC())
	}
	if result.Logout {
		h.cookies.Clear(writer)
	}
	if len(result.RecoveryCodes) > 0 {
		h.renderMFA(writer, request, 200, "mfa_recovery", MFAForm{RecoveryCodes: result.RecoveryCodes, Next: SafeRedirect(result.Next), Logout: result.Logout})
		return
	}
	http.Redirect(writer, request, SafeRedirect(result.Next), 303)
}
func (h *HTTP) StepUp(writer http.ResponseWriter, request *http.Request) {
	mfaHeaders(writer)
	p, _ := CurrentPrincipal(request.Context())
	if mfa.Recent(p.MFAVerifiedAt, time.Now().UTC()) {
		http.Redirect(writer, request, SafeRedirect(request.URL.Query().Get("next")), 303)
		return
	}
	h.beginAuthenticated(writer, request, mfa.StepUp, SafeRedirect(request.URL.Query().Get("next")))
}
func (h *HTTP) beginAuthenticated(writer http.ResponseWriter, request *http.Request, purpose, next string) {
	p, _ := CurrentPrincipal(request.Context())
	issued, err := h.mfa.BeginAuthenticated(request.Context(), p.Actor.UserID, p.SessionID, purpose, next, h.readChallenge(request), time.Now().UTC())
	if errors.Is(err, mfa.ErrPending) {
		http.Error(writer, "Verification already pending. Complete it in the original browser, or sign in again.", 409)
		return
	}
	if errors.Is(err, mfa.ErrInvalid) {
		http.Redirect(writer, request, "/login", 303)
		return
	}
	if err != nil {
		h.internalError(writer, request, "begin MFA", err)
		return
	}
	h.setChallenge(writer, issued)
	http.Redirect(writer, request, "/mfa", 303)
}
func (h *HTTP) Security(writer http.ResponseWriter, request *http.Request) {
	mfaHeaders(writer)
	p, _ := CurrentPrincipal(request.Context())
	if p.IsImpersonating {
		http.Error(writer, "Return to your own account before managing MFA.", 403)
		return
	}
	status, err := h.mfa.Status(request.Context(), p.Actor.UserID)
	if err != nil {
		h.internalError(writer, request, "MFA status", err)
		return
	}
	h.renderMFA(writer, request, 200, "mfa_security", MFAForm{Authenticated: true, Status: status})
}
func (h *HTTP) Manage(writer http.ResponseWriter, request *http.Request) {
	mfaHeaders(writer)
	p, _ := CurrentPrincipal(request.Context())
	if p.IsImpersonating {
		http.Error(writer, "Return to your own account before managing MFA.", 403)
		return
	}
	if !parseForm(writer, request) {
		return
	}
	purpose := request.PostFormValue("action")
	if purpose != mfa.RotateAuthorize && purpose != mfa.Regenerate {
		http.Error(writer, "Invalid action", 400)
		return
	}
	h.beginAuthenticated(writer, request, purpose, "/mfa/security")
}
func (h *HTTP) AdminResetPage(writer http.ResponseWriter, request *http.Request) {
	mfaHeaders(writer)
	p, _ := CurrentPrincipal(request.Context())
	if !p.Can("users.mfa.reset") || p.IsImpersonating {
		http.Error(writer, "Forbidden", 403)
		return
	}
	id, err := strconv.ParseUint(chi.URLParam(request, "id"), 10, 64)
	if err != nil || id == 0 || id == p.Actor.UserID {
		http.Error(writer, "Use self-service MFA management for your own account.", 403)
		return
	}
	if !RequireRecentMFA(writer, request, "/mfa/users/"+strconv.FormatUint(id, 10)+"/reset") {
		return
	}
	var target struct {
		Name     string `db:"name"`
		Username string `db:"username"`
	}
	err = h.mfa.DB.GetContext(request.Context(), &target, `SELECT name, username FROM users WHERE id=?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		h.errors.NotFound(writer, request)
		return
	}
	if err != nil {
		h.internalError(writer, request, "MFA reset target", err)
		return
	}
	h.renderMFA(writer, request, 200, "mfa_admin_reset", MFAForm{Authenticated: true, TargetID: id, TargetName: target.Name, TargetUsername: target.Username})
}
func (h *HTTP) AdminReset(writer http.ResponseWriter, request *http.Request) {
	mfaHeaders(writer)
	p, _ := CurrentPrincipal(request.Context())
	if !p.Can("users.mfa.reset") || p.IsImpersonating {
		http.Error(writer, "Forbidden", 403)
		return
	}
	id, err := strconv.ParseUint(chi.URLParam(request, "id"), 10, 64)
	if err != nil || id == 0 || id == p.Actor.UserID {
		http.Error(writer, "Use self-service MFA management for your own account.", 403)
		return
	}
	if !RequireRecentMFA(writer, request, "/mfa/users/"+strconv.FormatUint(id, 10)+"/reset") {
		return
	}
	if !parseForm(writer, request) {
		return
	}
	if request.PostFormValue("confirm") != "reset" {
		http.Error(writer, "Confirm MFA reset.", 422)
		return
	}
	if err = h.mfa.Reset(request.Context(), id, p.Actor.UserID, p.SessionID, auditAttributionFromPrincipal(p), time.Now().UTC()); err != nil {
		h.internalError(writer, request, "reset MFA", err)
		return
	}
	http.Redirect(writer, request, "/users/"+strconv.FormatUint(id, 10), 303)
}
