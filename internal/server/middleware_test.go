package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeaders(t *testing.T) {
	wantHeaders := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "same-origin",
		"Permissions-Policy":     "camera=(), microphone=(), geolocation=()",
	}
	wantDirectives := map[string]string{
		"default-src":     "'self'",
		"script-src":      "'self'",
		"style-src":       "'self'",
		"style-src-attr":  "'unsafe-inline'",
		"img-src":         "'self' data:",
		"font-src":        "'self'",
		"connect-src":     "'self'",
		"object-src":      "'none'",
		"base-uri":        "'self'",
		"form-action":     "'self'",
		"frame-ancestors": "'none'",
	}
	for _, status := range []int{http.StatusOK, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			SecurityHeaders(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(status)
				_, _ = writer.Write([]byte("response"))
			})).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
			if recorder.Code != status || recorder.Body.String() != "response" {
				t.Fatalf("middleware changed response: %d %q", recorder.Code, recorder.Body.String())
			}
			for name, want := range wantHeaders {
				if got := recorder.Header().Get(name); got != want {
					t.Errorf("%s = %q; want %q", name, got, want)
				}
			}
			directives := strings.Split(recorder.Header().Get("Content-Security-Policy"), ";")
			if len(directives) != len(wantDirectives) {
				t.Fatalf("CSP has %d directives; want %d", len(directives), len(wantDirectives))
			}
			seen := make(map[string]bool)
			for _, directive := range directives {
				fields := strings.Fields(directive)
				if len(fields) < 2 {
					t.Fatalf("invalid CSP directive: %q", directive)
				}
				name := fields[0]
				want, exists := wantDirectives[name]
				if !exists || seen[name] || strings.Join(fields[1:], " ") != want {
					t.Errorf("unexpected CSP directive: %q", directive)
				}
				seen[name] = true
			}
		})
	}
}
