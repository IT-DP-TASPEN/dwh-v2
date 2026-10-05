package main

import (
	"image/png"
	"net/http"
	"strings"
	"time"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/qr"
	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/browserauth"
	usersfeature "github.com/ibldzn/go-admin/internal/features/users"
	"github.com/ibldzn/go-admin/internal/mfa"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/render"
)

// Presentation fixtures contain only synthetic setup keys and recovery codes.
func (f *fixture) mfaPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "strict-origin")
	form := browserauth.MFAForm{Next: "/", Secret: "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"}
	page, title := "mfa", "Verify your identity"
	switch strings.TrimPrefix(r.URL.Path, "/fixture/mfa/") {
	case "challenge":
	case "error":
		form.Error = "Invalid verification code."
	case "enrollment":
		form.Enrollment, title = true, "Set up authenticator"
	case "recovery", "logout-recovery":
		page, title = "mfa_recovery", "Save your recovery codes"
		form.Logout = strings.HasSuffix(r.URL.Path, "logout-recovery")
		for i := 0; i < 10; i++ {
			form.RecoveryCodes = append(form.RecoveryCodes, "ABCDE-FGHIJ-KLMNO-PQRST-UVWXYZ")
		}
	case "step-up", "impersonated-step-up":
		form.Authenticated, title = true, "Confirm your identity"
	case "rotation-authorize", "regenerate":
		form.Authenticated, form.Management, title = true, true, "Confirm your identity"
		form.Regenerate = strings.HasSuffix(r.URL.Path, "regenerate")
	case "rotation-enrollment":
		form.Authenticated, form.Enrollment, form.Rotation, title = true, true, true, "Set up new authenticator"
	case "security":
		form.Authenticated, page, title = true, "mfa_security", "Security / MFA"
		form.Status = mfa.Status{Enabled: true, Remaining: 8, EnrolledAt: time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)}
	case "admin-reset":
		form.Authenticated, page, title = true, "mfa_admin_reset", "Reset MFA"
		form.TargetID, form.TargetName, form.TargetUsername = 9, "Test User", "test-user"
	case "user-detail":
		data := usersfeature.UserDetailData{User: usersfeature.UserRecord{ID: 9, Name: "Test User", Username: "test-user", IsActive: true}, CanResetMFA: true, CanEdit: true}
		f.renderAdmin(w, r, "features/users/show", "User Details", "/users/9", data)
		return
	default:
		http.NotFound(w, r)
		return
	}
	if !form.Authenticated {
		if err := f.renderer.RenderPageWithLayout(w, 200, page, "auth", render.PageData{Title: title, AppName: "DWH", Data: form}); err != nil {
			http.Error(w, err.Error(), 500)
		}
		return
	}
	principal := browserauth.Principal{Name: "Test Admin", Username: "admin", RoleSlug: access.AdminRoleSlug, Actor: browserauth.Identity{Name: "Test Admin", Username: "admin"}}
	if strings.HasSuffix(r.URL.Path, "impersonated-step-up") {
		principal.IsImpersonating, principal.Name, principal.Username = true, "Test User", "test-user"
	}
	data := adminshell.PageData{Title: title, AppName: "DWH", Principal: principal, Navigation: fixtureNavigation(true, r.URL.Path), CurrentPath: r.URL.Path, Data: form}
	if err := f.renderer.RenderPage(w, 200, page, data); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

func (f *fixture) mfaQR(w http.ResponseWriter, r *http.Request) {
	code, err := qr.Encode(mfa.ProvisioningURI("DWH", "test-user", "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"), qr.M, qr.Auto)
	if err != nil {
		http.Error(w, "QR fixture unavailable", 500)
		return
	}
	scaled, err := barcode.Scale(code, 256, 256)
	if err != nil {
		http.Error(w, "QR fixture unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_ = png.Encode(w, scaled)
}
