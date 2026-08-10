package browserauth

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ibldzn/go-admin/internal/auth"
)

type principalContextKey struct{}

func CurrentPrincipal(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}

func (h *HTTP) LoadPrincipal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rawToken, err := h.cookies.Read(request)
		if errors.Is(err, http.ErrNoCookie) {
			next.ServeHTTP(writer, request)
			return
		}
		if err != nil || !validToken(rawToken) {
			h.cookies.Clear(writer)
			next.ServeHTTP(writer, request)
			return
		}

		principal, err := h.service.ResolveSession(request.Context(), auth.HashToken(rawToken), time.Now().UTC())
		if errors.Is(err, ErrUnauthenticated) {
			h.cookies.Clear(writer)
			next.ServeHTTP(writer, request)
			return
		}
		if err != nil {
			h.internalError(writer, request, "resolve browser session", err)
			return
		}

		ctx := context.WithValue(request.Context(), principalContextKey{}, principal)
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

func (h *HTTP) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if _, ok := CurrentPrincipal(request.Context()); ok {
			next.ServeHTTP(writer, request)
			return
		}
		location := "/login?" + url.Values{"next": {SafeRedirect(request.URL.RequestURI())}}.Encode()
		if request.Header.Get("HX-Request") == "true" {
			writer.Header().Set("HX-Redirect", location)
			http.Error(writer, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		http.Redirect(writer, request, location, http.StatusSeeOther)
	})
}

func (h *HTTP) RequireGuest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if _, ok := CurrentPrincipal(request.Context()); ok {
			http.Redirect(writer, request, "/", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func SafeRedirect(target string) string {
	parsed, err := url.Parse(target)
	if err != nil || target == "" || !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") || strings.Contains(target, `\`) || strings.HasPrefix(parsed.Path, "//") || strings.Contains(parsed.Path, `\`) || parsed.IsAbs() || parsed.Host != "" || parsed.Opaque != "" || parsed.Fragment != "" {
		return "/"
	}
	return target
}

func validToken(token string) bool {
	if len(token) != base64.RawURLEncoding.EncodedLen(auth.TokenBytes) {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) == auth.TokenBytes
}
