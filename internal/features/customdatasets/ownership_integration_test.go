//go:build integration

package customdatasets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/auth"
	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/customdataset"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/navigation"
	"github.com/ibldzn/go-admin/internal/render"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
	"github.com/ibldzn/go-admin/internal/user"
	webfiles "github.com/ibldzn/go-admin/web"
)

func TestPendingUploadOwnershipAndRetainedRetry(t *testing.T) {
	db := integrationdb.Open(t)
	integrationdb.Reset(t, db, PermissionDefinitions())
	role := integrationdb.CustomRole(t, db, "Dataset managers", "dataset-managers")
	if _, err := db.Exec(`INSERT INTO role_permissions(role_id,permission_id) SELECT ?,p.id FROM permissions p WHERE p.key IN (?,?)`, role.ID, PermissionManage, PermissionView); err != nil {
		t.Fatal(err)
	}
	a := integrationdb.User(t, db, "upload-owner", role.ID, true)
	b := integrationdb.User(t, db, "other-manager", role.ID, true)
	now := time.Now().UTC()
	sessions := auth.NewSessionRepository(db)
	service, err := browserauth.NewService(user.NewRepository(db), access.NewRepository(db), sessions, 24*time.Hour, 30*24*time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	errorsHTTP := render.NewErrorResponder(renderer, "Test", logger)
	registry, err := navigation.NewRegistry(nil, PermissionDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	shell := adminshell.New(renderer, registry, "Test", errorsHTTP)
	repository, _ := customdataset.NewRepository(db)
	storage, err := customdataset.NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(shell, repository, storage)
	loginHTTP := browserauth.NewHTTP(service, renderer, browserauth.NewCookieManager("test_session", false, 30*24*time.Hour), "Test", false, logger, nil, errorsHTTP)
	router := chi.NewRouter()
	router.Use(loginHTTP.LoadPrincipal)
	handler.RegisterRoutes(router)
	tokens := make(map[uint64]string)
	for _, owner := range []user.User{a, b} {
		token, err := auth.GenerateToken()
		if err != nil {
			t.Fatal(err)
		}
		tokens[owner.ID] = token
		integrationdb.Session(t, sessions, owner.ID, false, token, now)
	}
	request := func(owner user.User, method, path string, body io.Reader, contentType string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, body)
		r.AddCookie(&http.Cookie{Name: "test_session", Value: tokens[owner.ID]})
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	var csv bytes.Buffer
	writer := multipart.NewWriter(&csv)
	part, _ := writer.CreateFormFile("file", "ledger.csv")
	_, _ = io.WriteString(part, "Title\nprivate-value\n")
	_ = writer.Close()
	response := request(a, http.MethodPost, "/custom-datasets/uploads", &csv, writer.FormDataContentType())
	if response.Code != http.StatusSeeOther {
		t.Fatalf("upload=%d %s", response.Code, response.Body.String())
	}
	location := response.Header().Get("Location")
	var uploadID uint64
	if _, err := fmt.Sscanf(location, "/custom-datasets/uploads/%d/configure", &uploadID); err != nil || uploadID == 0 {
		t.Fatal(location)
	}
	for _, query := range []string{"", "?dataset_id=999&mode=append", "?mode=replace"} {
		if got := request(b, http.MethodGet, location+query, nil, ""); got.Code != http.StatusNotFound || strings.Contains(got.Body.String(), "private-value") {
			t.Fatalf("unauthorized preview=%d", got.Code)
		}
	}
	if got := request(a, http.MethodGet, location+"?delimiter=comma", nil, ""); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "private-value") {
		t.Fatalf("owner preview=%d %s", got.Code, got.Body.String())
	}
	form := url.Values{"upload_id": {fmt.Sprint(uploadID)}, "name": {"Ledger"}, "delimiter": {"comma"}, "header_record": {"1"}, "mode": {"replace"}, "type_0": {"text"}}
	if got := request(b, http.MethodPost, "/custom-datasets/imports", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded"); got.Code != http.StatusNotFound {
		t.Fatalf("unauthorized submit=%d", got.Code)
	}
	columns := []customdataset.Column{{Ordinal: 1, DisplayName: "Title", QueryName: "title", PhysicalName: "c001", LogicalType: customdataset.TypeText}}
	input := customdataset.Submission{Name: "Ledger", UploadID: uploadID, Delimiter: customdataset.DelimiterComma, HeaderRecordNumber: 1, Mode: customdataset.ModeReplace, Columns: columns}
	if _, _, err := repository.Submit(context.Background(), integrationdb.Requester(b, role), input, now); !errors.Is(err, customdataset.ErrNotFound) {
		t.Fatalf("transactional owner guard=%v", err)
	}
	if got := request(a, http.MethodPost, "/custom-datasets/imports", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded"); got.Code != http.StatusSeeOther {
		t.Fatalf("owner submit=%d %s", got.Code, got.Body.String())
	}
	var datasetID uint64
	if err := db.Get(&datasetID, `SELECT dataset_id FROM custom_dataset_imports WHERE upload_id=?`, uploadID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE custom_dataset_imports SET status='failed' WHERE upload_id=?`, uploadID); err != nil {
		t.Fatal(err)
	}
	retainedLocation := fmt.Sprintf("%s?dataset_id=%d&mode=replace", location, datasetID)
	if got := request(b, http.MethodGet, retainedLocation, nil, ""); got.Code != http.StatusOK {
		t.Fatalf("legitimate retained preview=%d", got.Code)
	}
	if got := request(b, http.MethodGet, location, nil, ""); got.Code != http.StatusNotFound {
		t.Fatalf("unassociated retained preview=%d", got.Code)
	}
	// An unrelated provisioning dataset does not authorize reuse of this upload.
	result, err := db.Exec(`INSERT INTO custom_datasets(name,description,status,revision,schema_revision,row_count,created_by_user_id,updated_by_user_id,created_at,updated_at) VALUES ('Other','','provisioning',1,1,0,?,?,?,?)`, b.ID, b.ID, now, now)
	if err != nil {
		t.Fatal(err)
	}
	otherID, _ := result.LastInsertId()
	if got := request(b, http.MethodGet, fmt.Sprintf("%s?dataset_id=%d&mode=append", location, otherID), nil, ""); got.Code != http.StatusNotFound {
		t.Fatalf("unrelated retained preview=%d", got.Code)
	}
	input.DatasetID, input.DatasetRevision, input.Name = uint64(otherID), 1, ""
	if _, _, err := repository.Submit(context.Background(), integrationdb.Requester(b, role), input, now); !errors.Is(err, customdataset.ErrNotFound) {
		t.Fatalf("unrelated retained submit=%v", err)
	}
	dataset, err := repository.Find(context.Background(), datasetID)
	if err != nil {
		t.Fatal(err)
	}
	input.DatasetID, input.DatasetRevision = dataset.ID, dataset.Revision
	if _, _, err := repository.Submit(context.Background(), integrationdb.Requester(b, role), input, now); err != nil {
		t.Fatalf("legitimate provisioning retry=%v", err)
	}
}
