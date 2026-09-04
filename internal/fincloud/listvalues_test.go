package fincloud

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAuthListValuesIsStrictPreAuthRequest(t *testing.T) {
	var calls []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls = append(calls, request.URL.Path)
		if request.Method != http.MethodGet || request.URL.Path != "/fincloud/admin/access/listvalues" || request.Header.Get("sessionid") != "" {
			t.Errorf("request method=%s path=%s session=%q", request.Method, request.URL.Path, request.Header.Get("sessionid"))
		}
		_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"roleid":[{"id":"R-0089","descr":"Operations Role"}],"locationid":[{"id":"000","descr":"Head Office"}]}}}`)
	}))
	defer server.Close()
	client, err := newPreAuthClient(Config{BaseURL: server.URL + "/fincloud", HTTPTimeout: time.Second}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	values, err := client.FetchAuthListValues(context.Background())
	want := AuthListValues{Roles: []ListValue{{ID: "R-0089", Description: "Operations Role"}}, Locations: []ListValue{{ID: "000", Description: "Head Office"}}}
	if err != nil || !reflect.DeepEqual(values, want) || !reflect.DeepEqual(calls, []string{"/fincloud/admin/access/listvalues"}) {
		t.Fatalf("values=%+v calls=%v error=%v", values, calls, err)
	}
}

func TestAuthListValuesRejectsInvalidContracts(t *testing.T) {
	tests := []struct {
		name, body string
		status     int
		kind       ErrorKind
	}{
		{name: "non-ok status", body: `{"status":"failed","data":{"result":{"roleid":[],"locationid":[]}}}`, status: http.StatusOK, kind: ErrorUpstream},
		{name: "malformed JSON", body: `{"status":"ok"`, status: http.StatusOK, kind: ErrorMalformed},
		{name: "missing status", body: `{"data":{"result":{"roleid":[],"locationid":[]}}}`, status: http.StatusOK, kind: ErrorMalformed},
		{name: "missing data", body: `{"status":"ok"}`, status: http.StatusOK, kind: ErrorMalformed},
		{name: "malformed result", body: `{"status":"ok","data":{"result":[]}}`, status: http.StatusOK, kind: ErrorMalformed},
		{name: "missing roles", body: `{"status":"ok","data":{"result":{"locationid":[]}}}`, status: http.StatusOK, kind: ErrorMalformed},
		{name: "null locations", body: `{"status":"ok","data":{"result":{"roleid":[],"locationid":null}}}`, status: http.StatusOK, kind: ErrorMalformed},
		{name: "role wrong shape", body: `{"status":"ok","data":{"result":{"roleid":[{"id":9,"descr":"Role"}],"locationid":[]}}}`, status: http.StatusOK, kind: ErrorMalformed},
		{name: "missing description", body: `{"status":"ok","data":{"result":{"roleid":[{"id":"R","descr":""}],"locationid":[]}}}`, status: http.StatusOK, kind: ErrorMalformed},
		{name: "whitespace ID", body: `{"status":"ok","data":{"result":{"roleid":[],"locationid":[{"id":" 000","descr":"HQ"}]}}}`, status: http.StatusOK, kind: ErrorMalformed},
		{name: "HTTP failure", body: `{"password":"must-not-leak"}`, status: http.StatusBadGateway, kind: ErrorUpstream},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path == "/admin/access/login" {
					t.Fatal("pre-auth list-values request attempted login")
				}
				response := jsonResponse(test.body)
				response.StatusCode = test.status
				response.Status = http.StatusText(test.status)
				response.Request = request
				return response, nil
			})
			client, err := newPreAuthClient(Config{BaseURL: "https://fincloud.test", HTTPTimeout: time.Second}, &http.Client{Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.FetchAuthListValues(context.Background())
			var sourceError *Error
			if !errors.As(err, &sourceError) || sourceError.Kind != test.kind || strings.Contains(err.Error(), "must-not-leak") {
				t.Fatalf("error=%v typed=%+v", err, sourceError)
			}
			if diagnostic, ok := TechnicalDiagnostic(err); ok {
				encoded, _ := json.Marshal(diagnostic)
				if strings.Contains(string(encoded), "must-not-leak") {
					t.Fatalf("diagnostic leaked sensitive body: %s", encoded)
				}
			}
		})
	}
}
