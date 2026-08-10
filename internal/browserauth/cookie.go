package browserauth

import (
	"net/http"
	"time"

	"github.com/ibldzn/go-admin/internal/auth"
)

type CookieManager struct {
	name             string
	secure           bool
	rememberLifetime time.Duration
}

func NewCookieManager(name string, secure bool, rememberLifetime time.Duration) CookieManager {
	return CookieManager{name: name, secure: secure, rememberLifetime: rememberLifetime}
}

func (c CookieManager) Set(writer http.ResponseWriter, rawToken string, remember bool, now time.Time) {
	cookie := c.cookie(rawToken)
	if remember {
		cookie.Expires = now.UTC().Add(c.rememberLifetime)
		cookie.MaxAge = int(c.rememberLifetime / time.Second)
	}
	http.SetCookie(writer, cookie)
}

func (c CookieManager) SetForSession(writer http.ResponseWriter, rawToken string, session auth.Session, now time.Time) {
	cookie := c.cookie(rawToken)
	if session.RememberMe {
		cookie.Expires = session.ExpiresAt.UTC()
		cookie.MaxAge = max(1, int(session.ExpiresAt.Sub(now.UTC())/time.Second))
	}
	http.SetCookie(writer, cookie)
}

func (c CookieManager) Clear(writer http.ResponseWriter) {
	cookie := c.cookie("")
	cookie.Expires = time.Unix(1, 0).UTC()
	cookie.MaxAge = -1
	http.SetCookie(writer, cookie)
}

func (c CookieManager) Read(request *http.Request) (string, error) {
	cookie, err := request.Cookie(c.name)
	if err != nil {
		return "", err
	}
	return cookie.Value, nil
}

func (c CookieManager) cookie(value string) *http.Cookie {
	return &http.Cookie{
		Name:     c.name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   c.secure,
		SameSite: http.SameSiteLaxMode,
	}
}
