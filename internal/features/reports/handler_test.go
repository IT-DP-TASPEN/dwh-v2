package reports

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/auth"
	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/navigation"
	"github.com/ibldzn/go-admin/internal/render"
	"github.com/ibldzn/go-admin/internal/reportexport"
	"github.com/ibldzn/go-admin/internal/reporting"
	"github.com/ibldzn/go-admin/internal/securityctx"
	"github.com/ibldzn/go-admin/internal/user"
	webfiles "github.com/ibldzn/go-admin/web"
)

type fakeExportStore struct {
	visible       map[uint64]reportexport.VisibleJob
	jobs          map[uint64]reportexport.Job
	ownerAllowed  map[uint64]bool
	countScope    reportexport.Scope
	listScope     reportexport.Scope
	findScope     reportexport.Scope
	listUserID    uint64
	countCalls    int
	listCalls     int
	recordedPath  reportexport.DownloadAccess
	recordedJobID uint64
}

func (*fakeExportStore) Submit(context.Context, securityctx.Requester, reporting.Template, map[string]reporting.NormalizedValue, time.Time) (reportexport.Job, error) {
	return reportexport.Job{}, nil
}

func (store *fakeExportStore) CountVisible(_ context.Context, scope reportexport.Scope, userID uint64) (int64, error) {
	store.countScope = scope
	store.countCalls++
	var total int64
	for _, job := range store.visible {
		if scope == reportexport.ScopeAll || job.SubmittedByUserID == userID {
			total++
		}
	}
	return total, nil
}

func (store *fakeExportStore) ListVisible(_ context.Context, scope reportexport.Scope, userID uint64, _, _ int) ([]reportexport.VisibleJob, error) {
	store.listScope, store.listUserID = scope, userID
	store.listCalls++
	rows := make([]reportexport.VisibleJob, 0, len(store.visible))
	for _, job := range store.visible {
		if scope == reportexport.ScopeAll || job.SubmittedByUserID == userID {
			rows = append(rows, job)
		}
	}
	return rows, nil
}

func (store *fakeExportStore) FindVisible(_ context.Context, id uint64, scope reportexport.Scope, userID uint64) (reportexport.VisibleJob, error) {
	store.findScope = scope
	job, ok := store.visible[id]
	if !ok || scope == reportexport.ScopeMine && job.SubmittedByUserID != userID {
		return reportexport.VisibleJob{}, reporting.ErrNotFound
	}
	return job, nil
}

func (store *fakeExportStore) AuthorizeDownload(_ context.Context, id, userID uint64, viewAll bool) (reportexport.Job, reportexport.DownloadAccess, error) {
	job, ok := store.jobs[id]
	if !ok {
		return reportexport.Job{}, "", reporting.ErrNotFound
	}
	if job.SubmittedByUserID == userID && store.ownerAllowed[id] {
		return job, reportexport.DownloadAccessOwner, nil
	}
	if viewAll {
		return job, reportexport.DownloadAccessViewAll, nil
	}
	return reportexport.Job{}, "", nil
}

func (store *fakeExportStore) RecordDownload(_ context.Context, _ securityctx.Requester, job reportexport.Job, path reportexport.DownloadAccess, _ time.Time) error {
	store.recordedJobID, store.recordedPath = job.ID, path
	return nil
}

type exportRouteAuthentication struct{ principal browserauth.Principal }

func (*exportRouteAuthentication) Login(context.Context, browserauth.LoginInput, time.Time) (browserauth.LoginResult, error) {
	return browserauth.LoginResult{}, browserauth.ErrInvalidCredentials
}
func (*exportRouteAuthentication) Register(context.Context, browserauth.RegisterInput, time.Time) (user.User, error) {
	return user.User{}, nil
}
func (authentication *exportRouteAuthentication) ResolveSession(context.Context, [32]byte, time.Time) (browserauth.Principal, error) {
	return authentication.principal, nil
}
func (*exportRouteAuthentication) Logout(context.Context, [32]byte) error { return nil }

func TestRequestedExportScope(t *testing.T) {
	for _, test := range []struct {
		value      string
		viewAll    bool
		want       reportexport.Scope
		wantStatus int
	}{
		{want: reportexport.ScopeMine},
		{value: "mine", want: reportexport.ScopeMine},
		{value: "all", wantStatus: http.StatusForbidden},
		{viewAll: true, want: reportexport.ScopeAll},
		{value: "all", viewAll: true, want: reportexport.ScopeAll},
		{value: "mine", viewAll: true, want: reportexport.ScopeMine},
		{value: "invalid", viewAll: true, wantStatus: http.StatusBadRequest},
	} {
		got, status := requestedExportScope(test.value, test.viewAll)
		if got != test.want || status != test.wantStatus {
			t.Fatalf("scope(%q,%t)=%q/%d want %q/%d", test.value, test.viewAll, got, status, test.want, test.wantStatus)
		}
	}
	if got := exportPageURL(reportexport.ScopeMine, 2); got != "/exports?page=2&scope=mine" {
		t.Fatalf("page URL=%q", got)
	}
}

func TestExportRoutesUseEffectiveScopeAndHideUnauthorizedObjects(t *testing.T) {
	requester := "Requester B"
	username := "user-b"
	now := time.Date(2026, 8, 30, 8, 0, 0, 0, time.UTC)
	store := &fakeExportStore{
		visible: map[uint64]reportexport.VisibleJob{
			100: {ID: 100, ReportName: "Owned", SubmittedByUserID: 1, Status: reportexport.StatusSucceeded, CreatedAt: now, UpdatedAt: now},
			200: {ID: 200, ReportName: "Other succeeded", SubmittedByUserID: 2, RequesterName: &requester, RequesterUsername: &username, Status: reportexport.StatusSucceeded, CreatedAt: now, UpdatedAt: now},
			201: {ID: 201, ReportName: "Other failed", SubmittedByUserID: 2, Status: reportexport.StatusFailed, CreatedAt: now, UpdatedAt: now},
			202: {ID: 202, ReportName: "Other expired", SubmittedByUserID: 2, Status: reportexport.StatusSucceeded, ArtifactDeletedAt: &now, CreatedAt: now, UpdatedAt: now},
		},
		jobs: map[uint64]reportexport.Job{
			200: {ID: 200, SubmittedByUserID: 2, Status: reportexport.StatusSucceeded},
			201: {ID: 201, SubmittedByUserID: 2, Status: reportexport.StatusFailed},
			202: {ID: 202, SubmittedByUserID: 2, Status: reportexport.StatusSucceeded, ArtifactDeletedAt: &now},
		}, ownerAllowed: map[uint64]bool{},
	}
	principal := exportPrincipal(1, []string{PermissionExport})
	principal.Actor = browserauth.Identity{UserID: 99, Username: "admin", RoleSlug: access.AdminRoleSlug}
	principal.IsImpersonating = true
	router, token := exportRouter(t, principal, store, nil)

	response := exportRequest(router, token, "/exports")
	if response.Code != http.StatusOK || store.countScope != reportexport.ScopeMine || store.listScope != reportexport.ScopeMine || store.listUserID != 1 || store.countCalls != 1 || store.listCalls != 1 || strings.Contains(response.Body.String(), "Other succeeded") || strings.Contains(response.Body.String(), "All exports</a>") {
		t.Fatalf("mine status=%d scopes=%s/%s body=%q", response.Code, store.countScope, store.listScope, response.Body.String())
	}
	if response = exportRequest(router, token, "/exports?scope=all"); response.Code != http.StatusForbidden {
		t.Fatalf("all status=%d", response.Code)
	}

	var forbiddenBody string
	for _, path := range []string{"/exports/200", "/exports/201", "/exports/202", "/exports/999", "/exports/200/download", "/exports/201/download", "/exports/202/download", "/exports/999/download"} {
		response = exportRequest(router, token, path)
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s status=%d body=%q", path, response.Code, response.Body.String())
		}
		if forbiddenBody == "" {
			forbiddenBody = response.Body.String()
		} else if response.Body.String() != forbiddenBody {
			t.Fatalf("%s leaked object distinction", path)
		}
	}
}

func TestViewAllSupportsCrossUserDetailAndTrueOrDownload(t *testing.T) {
	storage, err := reportexport.NewStorage(filepath.Join(t.TempDir(), "exports"))
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := storage.Workspace(200, 1)
	if err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(workspace, "other.xlsx")
	if err := os.WriteFile(artifact, []byte("artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	relative, size, err := storage.Publish(200, 1, artifact)
	if err != nil {
		t.Fatal(err)
	}
	name, missingName, missingPath, kind := "other.xlsx", "missing.xlsx", "final/203/missing.xlsx", "xlsx"
	now := time.Date(2026, 8, 30, 8, 0, 0, 0, time.UTC)
	store := &fakeExportStore{
		visible: map[uint64]reportexport.VisibleJob{
			200: {ID: 200, ReportID: 9, ReportName: "Other", SubmittedByUserID: 2, Status: reportexport.StatusSucceeded, ArtifactName: &name, ArtifactType: &kind, ArtifactSize: &size, CreatedAt: now, UpdatedAt: now},
			203: {ID: 203, ReportID: 9, ReportName: "Missing", SubmittedByUserID: 2, Status: reportexport.StatusSucceeded, ArtifactName: &missingName, ArtifactType: &kind, ArtifactSize: &size, CreatedAt: now, UpdatedAt: now},
		},
		jobs: map[uint64]reportexport.Job{
			200: {ID: 200, ReportID: 9, ReportName: "Other", SubmittedByUserID: 2, Status: reportexport.StatusSucceeded, ArtifactPath: &relative, ArtifactName: &name, ArtifactType: &kind, ArtifactSize: &size, FinishedAt: &now},
			203: {ID: 203, ReportID: 9, ReportName: "Missing", SubmittedByUserID: 2, Status: reportexport.StatusSucceeded, ArtifactPath: &missingPath, ArtifactName: &missingName, ArtifactType: &kind, ArtifactSize: &size, FinishedAt: &now},
		},
		ownerAllowed: map[uint64]bool{200: false},
	}
	router, token := exportRouter(t, exportPrincipal(2, []string{PermissionViewAllExports}), store, storage)
	response := exportRequest(router, token, "/exports")
	if response.Code != http.StatusOK || store.listScope != reportexport.ScopeAll || !strings.Contains(response.Body.String(), "All exports") {
		t.Fatalf("all status=%d scope=%s body=%q", response.Code, store.listScope, response.Body.String())
	}
	response = exportRequest(router, token, "/exports?scope=mine")
	if response.Code != http.StatusOK || store.listScope != reportexport.ScopeMine {
		t.Fatalf("mine status=%d scope=%s", response.Code, store.listScope)
	}
	if response = exportRequest(router, token, "/exports/200"); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Requester") {
		t.Fatalf("detail status=%d body=%q", response.Code, response.Body.String())
	}
	response = exportRequest(router, token, "/exports/200/download")
	if response.Code != http.StatusOK || response.Body.String() != "artifact" || store.recordedJobID != 200 || store.recordedPath != reportexport.DownloadAccessViewAll {
		t.Fatalf("download status=%d body=%q audit=%d/%q", response.Code, response.Body.String(), store.recordedJobID, store.recordedPath)
	}
	if response = exportRequest(router, token, "/exports/203"); response.Code != http.StatusOK {
		t.Fatalf("missing artifact detail status=%d", response.Code)
	}
	if response = exportRequest(router, token, "/exports/203/download"); response.Code != http.StatusNotFound {
		t.Fatalf("missing artifact download status=%d", response.Code)
	}
}

func TestViewAllDoesNotGrantReportRuntimeRoutes(t *testing.T) {
	principal := exportPrincipal(3, []string{PermissionViewAllExports})
	for _, permission := range []string{PermissionView, PermissionExecute, PermissionExport, "report_templates.update", "report_templates.manage_access", "report_datasources.update"} {
		if principal.Can(permission) {
			t.Fatalf("view_all granted %s", permission)
		}
	}
	router, token := exportRouter(t, principal, &fakeExportStore{}, nil)
	for _, request := range []struct{ method, path string }{{http.MethodGet, "/reports/9"}, {http.MethodPost, "/reports/9/run"}, {http.MethodPost, "/reports/9/export"}} {
		response := exportRequestMethod(router, token, request.method, request.path)
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s %s status=%d", request.method, request.path, response.Code)
		}
	}
}

func exportPrincipal(userID uint64, permissions []string) browserauth.Principal {
	return browserauth.Principal{UserID: userID, Username: "viewer", Name: "Viewer", RoleSlug: access.UserRoleSlug, Permissions: access.NewPermissionSet(permissions), Actor: browserauth.Identity{UserID: userID, Username: "viewer", RoleSlug: access.UserRoleSlug}}
}

func exportRouter(t *testing.T, principal browserauth.Principal, store exportStore, storage *reportexport.Storage) (http.Handler, string) {
	t.Helper()
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	errors := render.NewErrorResponder(renderer, "Test", logger)
	registry, err := navigation.NewRegistry([]navigation.Group{{Key: "reporting", Label: "Reporting", Items: []navigation.Item{ExportsNavigation()}}}, PermissionDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	shell := adminshell.New(renderer, registry, "Test", errors)
	cookies := browserauth.NewCookieManager("session", false, time.Hour)
	authentication := browserauth.NewHTTP(&exportRouteAuthentication{principal}, renderer, cookies, "Test", false, logger, func(context.Context, audit.Event) error { return nil }, errors)
	router := chi.NewRouter()
	router.Use(authentication.LoadPrincipal)
	NewHandler(shell, nil, nil, store, storage, time.Minute).RegisterRoutes(router)
	token, err := auth.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	return router, token
}

func exportRequest(handler http.Handler, token, path string) *httptest.ResponseRecorder {
	return exportRequestMethod(handler, token, http.MethodGet, path)
}

func exportRequestMethod(handler http.Handler, token, method, path string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	request.AddCookie(&http.Cookie{Name: "session", Value: token})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
