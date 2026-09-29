package datasources

import (
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ibldzn/go-admin/internal/reporting"
)

func TestDatasourceFormNetworks(t *testing.T) {
	for _, network := range []string{"", "tcp", "unix"} {
		values := url.Values{"network": {network}, "name": {"Test"}, "database_name": {"dwh"}, "username": {"dwhadmin"}, "socket_path": {"/tmp/mysql.sock"}}
		request := httptest.NewRequest("POST", "/datasources", strings.NewReader(values.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		form, input, ok := (&Handler{}).form(httptest.NewRecorder(), request, true)
		if !ok {
			t.Fatal("form failed to parse")
		}
		if network == "unix" {
			if len(form.Errors) != 0 || input.Password != "" || input.Port != 0 || input.TLSPolicy != reporting.TLSDisabled || input.SocketPath != "/tmp/mysql.sock" {
				t.Fatalf("Unix required TCP fields: %+v", form)
			}
		} else if form.Errors["password"] == "" || form.Errors["port"] == "" || input.Network != "tcp" {
			t.Fatal("TCP required fields or default mode changed")
		}
	}
	if got := publicError(errors.New("password=super-secret DSN")); strings.Contains(got, "super-secret") {
		t.Fatal("public error exposed connection credentials")
	}
}
