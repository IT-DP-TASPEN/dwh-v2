package webutil

import (
	contextpkg "context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestRequestHelpers(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/items/7?page=3", nil)
	context := chi.NewRouteContext()
	context.URLParams.Add("id", "7")
	request = request.WithContext(contextpkg.WithValue(request.Context(), chi.RouteCtxKey, context))
	if id, ok := RouteID(request); !ok || id != 7 || Page(request) != 3 {
		t.Fatalf("unexpected route values: id=%d ok=%v page=%d", id, ok, Page(request))
	}

	request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader("field=too-long"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	if ParseForm(response, request, 4) || response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected bounded form failure, got %d", response.Code)
	}
}
