package browserauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ibldzn/go-admin/internal/render"
	webfiles "github.com/ibldzn/go-admin/web"
)

type mfaPageRenderer struct {
	request     *http.Request
	page, title string
	status      int
	form        MFAForm
}

func (r *mfaPageRenderer) RenderPage(w http.ResponseWriter, req *http.Request, status int, page, title string, data any) {
	r.request, r.page, r.title, r.status, r.form = req, page, title, status, data.(MFAForm)
	w.WriteHeader(status)
}

func TestMFARenderingContextAndHeaders(t *testing.T) {
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, page, title string
		form              MFAForm
		authenticated     bool
	}{
		{name: "login challenge", page: "mfa", title: "Verify your identity"},
		{name: "first enrollment", page: "mfa", title: "Set up authenticator", form: MFAForm{Enrollment: true, Secret: "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"}},
		{name: "initial recovery", page: "mfa_recovery", title: "Save your recovery codes", form: MFAForm{RecoveryCodes: []string{"ONE-TIME-CODE"}, Next: "/"}},
		{name: "revoked-session recovery", page: "mfa_recovery", title: "Save your recovery codes", form: MFAForm{RecoveryCodes: []string{"ONE-TIME-CODE"}, Logout: true, Next: "/login"}},
		{name: "step-up", page: "mfa", title: "Confirm your identity", authenticated: true},
		{name: "rotation authorization", page: "mfa", title: "Confirm your identity", form: MFAForm{Management: true}, authenticated: true},
		{name: "rotation enrollment", page: "mfa", title: "Set up new authenticator", form: MFAForm{Enrollment: true, Rotation: true}, authenticated: true},
		{name: "regeneration authorization", page: "mfa", title: "Confirm your identity", form: MFAForm{Management: true, Regenerate: true}, authenticated: true},
		{name: "security", page: "mfa_security", title: "Security / MFA", authenticated: true},
		{name: "admin reset", page: "mfa_admin_reset", title: "Reset MFA", authenticated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			admin := &mfaPageRenderer{}
			h := &HTTP{renderer: renderer, appName: "DWH"}
			h.SetAuthenticatedPageRenderer(admin)
			tc.form.Authenticated = tc.authenticated
			// Even a newly issued session or revoked session must not change
			// the recovery response into an authenticated shell.
			request := httptest.NewRequest("GET", "/mfa", nil)
			request = request.WithContext(context.WithValue(request.Context(), principalContextKey{}, Principal{UserID: 7}))
			response := httptest.NewRecorder()
			h.renderMFA(response, request, 422, tc.page, tc.form)
			if response.Code != 422 || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Referrer-Policy") != "strict-origin" {
				t.Fatalf("status/headers: %d %v", response.Code, response.Header())
			}
			if tc.authenticated {
				if admin.request != request || admin.status != 422 || admin.page != tc.page || admin.title != tc.title || !admin.form.Authenticated {
					t.Fatalf("incorrect authenticated renderer dispatch: %+v", admin)
				}
				p, ok := CurrentPrincipal(admin.request.Context())
				if !ok || p.UserID != 7 {
					t.Fatal("principal not forwarded")
				}
				return
			}
			body := response.Body.String()
			if admin.request != nil || strings.Contains(body, "data-admin-shell") || strings.Contains(body, "admin-sidebar") {
				t.Fatal("auth flow rendered authenticated navigation")
			}
			for _, want := range []string{tc.title, "place-items-center", "bg-emerald-500", "dark:bg-slate-900", "dark:border-slate-800"} {
				if !strings.Contains(body, want) {
					t.Errorf("missing %q", want)
				}
			}
			if tc.page == "mfa" {
				for _, want := range []string{"dark:border-slate-700", "dark:bg-slate-950", "dark:text-white", "focus:ring-emerald-500/20"} {
					if !strings.Contains(body, want) {
						t.Errorf("missing control style %q", want)
					}
				}
			}
		})
	}
}
