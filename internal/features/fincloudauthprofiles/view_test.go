package fincloudauthprofiles

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/ibldzn/go-admin/internal/fincloud"
	"github.com/ibldzn/go-admin/internal/fincloudauth"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/render"
	webfiles "github.com/ibldzn/go-admin/web"
)

func TestAuthProfileFormRendersAuthoritativeSelectsAndCanonicalBorders(t *testing.T) {
	data := FormData{Roles: []fincloud.ListValue{{ID: "R-0089", Description: "Operations Role"}}, Locations: []fincloud.ListValue{{ID: "000", Description: "Head Office"}}, Errors: map[string]string{}}
	body := renderForm(t, data)
	for _, want := range []string{
		`<select class="mt-2 w-full rounded-lg border border-slate-300`, `dark:border-slate-700`, `name="role_id"`, `value="R-0089"`, `R-0089 — Operations Role`,
		`name="location_id"`, `value="000"`, `000 — Head Office`, `rounded-xl border border-slate-200`, `dark:border-slate-800`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("form missing %q: %s", want, body)
		}
	}
	if regexp.MustCompile(`<input[^>]+name="(?:role_id|location_id)"`).MatchString(body) {
		t.Fatalf("role/location text input rendered: %s", body)
	}
}

func TestAuthProfileEditSelectsCurrentAndPreservesUnavailableValues(t *testing.T) {
	valid := FormData{ID: 7, RoleID: "R-2", LocationID: "002", Roles: []fincloud.ListValue{{ID: "R-1", Description: "One"}, {ID: "R-2", Description: "Two"}}, Locations: []fincloud.ListValue{{ID: "002", Description: "Branch"}}, Errors: map[string]string{}}
	body := renderForm(t, valid)
	if !strings.Contains(body, `value="R-2" selected`) || !strings.Contains(body, `value="002" selected`) {
		t.Fatalf("current values not selected: %s", body)
	}
	stale := valid
	stale.RoleID, stale.LocationID = "Gone-Role", "Gone-Location"
	stale.RoleUnavailable, stale.LocationUnavailable = true, true
	body = renderForm(t, stale)
	for _, want := range []string{`Gone-Role — Currently configured (unavailable)`, `Gone-Location — Currently configured (unavailable)`, `id="password" name="password" type="password" autocomplete="new-password"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("stale edit form missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "BrowserSecret") || strings.Contains(body, `name="password" type="password" value=`) {
		t.Fatalf("plaintext password disclosed: %s", body)
	}
}

func TestAuthProfileSelectionValidationAllowsOnlyCurrentStaleEditValue(t *testing.T) {
	handler := &Handler{}
	existing := fincloudauth.Profile{RoleID: "Stale-Role", LocationID: "Stale-Location"}
	form := FormData{RoleID: existing.RoleID, LocationID: existing.LocationID, Roles: []fincloud.ListValue{{ID: "R-1"}}, Locations: []fincloud.ListValue{{ID: "001"}}, Errors: map[string]string{}}
	handler.validateSelections(&form, &existing)
	if len(form.Errors) != 0 {
		t.Fatalf("unchanged stale values rejected: %v", form.Errors)
	}
	form = FormData{RoleID: "Forged", LocationID: "001", Roles: []fincloud.ListValue{{ID: "R-1"}}, Locations: []fincloud.ListValue{{ID: "001"}}, Errors: map[string]string{}}
	handler.validateSelections(&form, &existing)
	if form.Errors["role_id"] == "" {
		t.Fatal("new unavailable role accepted")
	}
	form.Errors = map[string]string{}
	handler.validateSelections(&form, nil)
	if form.Errors["role_id"] == "" {
		t.Fatal("unavailable role accepted for create")
	}
}

func TestAuthProfileFormPreservesExactSubmittedIdentifiers(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/fincloud-auth-profiles", strings.NewReader(url.Values{
		"name": {"Ops"}, "username": {"CaseSensitive"}, "role_id": {"R-AbC"}, "location_id": {"000"}, "password": {"secret"},
	}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	form, input, ok := (&Handler{}).form(httptest.NewRecorder(), request, true)
	if !ok || form.Username != "CaseSensitive" || input.RoleID != "R-AbC" || input.LocationID != "000" {
		t.Fatalf("form=%+v input=%+v ok=%v", form, input, ok)
	}
}

func TestAuthProfileListValueFailureKeepsStoredValuesAndRemovesMutationForm(t *testing.T) {
	data := FormData{ID: 7, RoleID: "Stored-Role", LocationID: "Stored-Location", OptionsError: "Role and location options could not be loaded from Fincloud. Retry to continue.", Errors: map[string]string{}}
	body := renderForm(t, data)
	for _, want := range []string{"Role and location options unavailable", "Stored-Role", "Stored-Location", `/fincloud-auth-profiles/7/edit`, "Retry"} {
		if !strings.Contains(body, want) {
			t.Fatalf("failure form missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, `<form method="post" action="/fincloud-auth-profiles/7"`) || strings.Contains(body, `name="role_id"`) {
		t.Fatalf("load failure exposed mutation form: %s", body)
	}
}

func TestWithListValuesMarksOnlyUnchangedMissingValuesUnavailable(t *testing.T) {
	reader := listValuesStub{values: fincloud.AuthListValues{Roles: []fincloud.ListValue{{ID: "R-1", Description: "Role"}}, Locations: []fincloud.ListValue{{ID: "001", Description: "Branch"}}}}
	handler := &Handler{listValues: reader}
	existing := fincloudauth.Profile{RoleID: "Old-Role", LocationID: "Old-Location"}
	form := handler.withListValues(context.Background(), FormData{RoleID: existing.RoleID, LocationID: "001"}, &existing)
	if !form.RoleUnavailable || form.LocationUnavailable {
		t.Fatalf("unavailable flags=%v/%v", form.RoleUnavailable, form.LocationUnavailable)
	}
}

type listValuesStub struct {
	values fincloud.AuthListValues
	err    error
}

func (stub listValuesStub) FetchAuthListValues(context.Context) (fincloud.AuthListValues, error) {
	return stub.values, stub.err
}

func renderForm(t *testing.T, data FormData) string {
	t.Helper()
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	if err := renderer.RenderPage(response, http.StatusOK, "features/fincloudauthprofiles/form", adminshell.PageData{Title: "Auth Profile", AppName: "Test", Data: data}); err != nil {
		t.Fatal(err)
	}
	return response.Body.String()
}
