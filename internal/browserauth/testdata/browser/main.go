// This executable is an isolated browser test fixture, never a production server.
package main

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/auth"
	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/config"
	"github.com/ibldzn/go-admin/internal/database"
	"github.com/ibldzn/go-admin/internal/mfa"
	"github.com/ibldzn/go-admin/internal/render"
	"github.com/ibldzn/go-admin/internal/secretcrypto"
	"github.com/ibldzn/go-admin/internal/server"
	"github.com/ibldzn/go-admin/internal/user"
	webfiles "github.com/ibldzn/go-admin/web"
	"github.com/pressly/goose/v3"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	// Fail closed unless the operator explicitly selects this disposable schema.
	if os.Getenv("TEST_DB_NAME") != "mfa_browser_test" {
		return fmt.Errorf("browser fixture requires disposable TEST_DB_NAME=mfa_browser_test")
	}
	port, err := strconv.Atoi(os.Getenv("TEST_DB_PORT"))
	if err != nil {
		return err
	}
	ctx := context.Background()
	db, err := database.OpenMigrations(ctx, config.DatabaseConfig{Host: os.Getenv("TEST_DB_HOST"), Port: port, Name: os.Getenv("TEST_DB_NAME"), User: os.Getenv("TEST_DB_USER"), Password: os.Getenv("TEST_DB_PASSWORD")})
	if err != nil {
		return err
	}
	defer db.Close()
	if err = goose.SetDialect("mysql"); err != nil {
		return err
	}
	if err = goose.UpContext(ctx, db.DB, filepath.Join("migrations")); err != nil {
		return err
	}
	if err = access.Bootstrap(ctx, db, []access.PermissionDefinition{{Key: "datasources.create", Name: "Create datasource", Group: "Reporting"}}, time.Now().UTC()); err != nil {
		return err
	}
	roles := access.NewRepository(db)
	role, err := roles.FindRoleBySlug(ctx, access.AdminRoleSlug)
	if err != nil {
		return err
	}
	users := user.NewRepository(db)
	u, err := users.FindByUsername(ctx, "browser-mfa")
	if err == user.ErrNotFound {
		hash, err := auth.HashPassword("browser-password-only")
		if err != nil {
			return err
		}
		u, err = users.Create(ctx, user.CreateParams{Username: "browser-mfa", Name: "Browser MFA", PasswordHash: hash, RoleID: role.ID, IsActive: true}, time.Now().UTC())
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	store := &mfa.Store{DB: db, Cipher: secretcrypto.New([32]byte{9}), Lifetime: time.Hour, RememberLifetime: 30 * 24 * time.Hour, IdleTimeout: 2 * time.Hour}
	if err = store.Reset(ctx, u.ID, 0, 0, audit.Attribution{}, time.Now().UTC()); err != nil {
		return err
	}
	logger := slog.Default()
	service, err := browserauth.NewService(users, roles, auth.NewSessionRepository(db), time.Hour, 30*24*time.Hour, logger)
	if err != nil {
		return err
	}
	service.EnableMFA(store)
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		return err
	}
	responder := render.NewErrorResponder(renderer, "DWH", logger)
	cookie := browserauth.NewCookieManager("browser_session", false, 30*24*time.Hour)
	h := browserauth.NewHTTP(service, renderer, cookie, "DWH", false, logger, nil, responder)
	h.EnableMFA(store)
	static, _ := fs.Sub(webfiles.Files, "static")
	router := server.NewRouter(server.RouterDependencies{Authentication: h, Errors: responder, StaticFiles: static, RegisterAuthenticated: func(r chi.Router) {
		r.Get("/", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<h1>Application</h1><a href="/sensitive">Sensitive form</a><a href="/mfa/security">Security / MFA</a><form method="post" action="/logout"><button>Sign out</button></form>`)
		})
		r.Get("/sensitive", func(w http.ResponseWriter, r *http.Request) {
			if !browserauth.RequireRecentMFA(w, r, "/sensitive") {
				return
			}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<h1>Sensitive form</h1><form method="post" action="/sensitive"><input name="password" value="do-not-replay"><button>Submit sensitive action</button></form>`)
		})
		r.Post("/sensitive", func(w http.ResponseWriter, r *http.Request) {
			p, _ := browserauth.CurrentPrincipal(r.Context())
			if !p.Can("datasources.create") {
				http.Error(w, "Forbidden", 403)
				return
			}
			if !browserauth.RequireRecentMFA(w, r, "/sensitive") {
				return
			}
			fmt.Fprint(w, "Sensitive action completed")
		})
		// Test-only clock control, scoped to the fixture's current browser session.
		r.Post("/fixture/stale", func(w http.ResponseWriter, r *http.Request) {
			p, _ := browserauth.CurrentPrincipal(r.Context())
			if _, err := db.Exec(`UPDATE sessions SET mfa_verified_at=? WHERE id=?`, time.Now().Add(-11*time.Minute), p.SessionID); err != nil {
				http.Error(w, "Fixture failure", 500)
				return
			}
			w.WriteHeader(204)
		})
	}})
	return http.ListenAndServe("127.0.0.1:4174", router)
}
