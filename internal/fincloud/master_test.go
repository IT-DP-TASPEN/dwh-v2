package fincloud

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestMasterReadersUseExactPathsAndStrictEnvelopes(t *testing.T) {
	responseBody := `{"status":"ok","data":{"result":{}}}`
	var requests []string
	client, err := newClient(testConfig("https://fincloud.test/fincloud"), &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"status":"ok","data":{"result":{"sessionid":"session"}}}`
		if request.URL.Path != "/fincloud/admin/access/login" {
			if request.Method != http.MethodGet {
				t.Errorf("method=%s", request.Method)
			}
			requests = append(requests, request.URL.RequestURI())
			body = responseBody
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	referencePaths := []string{
		"/cif/inquiry/cif//listvalues",
		"/tabungan/inquiry/rekening//listvalues",
		"/deposito/inquiry/rekening//listvalues",
		"/pinjaman/inquiry/rekening//listvalues",
	}
	for _, path := range referencePaths {
		if result, err := client.FetchReferenceMaster(context.Background(), path); err != nil || len(result) != 0 {
			t.Fatalf("empty reference %s=%v error=%v", path, result, err)
		}
	}
	responseBody = `{"status":"ok","data":{"result":[]}}`
	if result, err := client.FetchMarketingMaster(context.Background()); err != nil || len(result) != 0 {
		t.Fatalf("empty Marketing=%v error=%v", result, err)
	}
	wantRequests := []string{
		"/fincloud/cif/inquiry/cif//listvalues",
		"/fincloud/tabungan/inquiry/rekening//listvalues",
		"/fincloud/deposito/inquiry/rekening//listvalues",
		"/fincloud/pinjaman/inquiry/rekening//listvalues",
		"/fincloud/system/marketing/pembuatan/cari?nama=",
	}
	if strings.Join(requests, "\n") != strings.Join(wantRequests, "\n") {
		t.Fatalf("Master requests=%v", requests)
	}
	if _, err := client.FetchReferenceMaster(context.Background(), "/cif/inquiry/cif/cif"); err == nil {
		t.Fatal("unsupported reference path accepted")
	}
	for _, invalid := range []string{`{"status":"failed","data":{"result":{}}}`, `{"status":"ok"}`, `{"status":"ok","data":null}`, `{"status":"ok","data":{}}`, `{"status":"ok","data":{"result":null}}`, `{"status":"ok","data":{"result":[]}}`} {
		responseBody = invalid
		if _, err := client.FetchReferenceMaster(context.Background(), "/cif/inquiry/cif//listvalues"); err == nil {
			t.Fatalf("invalid reference accepted: %s", invalid)
		}
	}
	for _, invalid := range []string{`{"status":"failed","data":{"result":[]}}}`, `{"status":"ok"}`, `{"status":"ok","data":null}`, `{"status":"ok","data":{}}`, `{"status":"ok","data":{"result":null}}`, `{"status":"ok","data":{"result":{}}}`} {
		responseBody = invalid
		if _, err := client.FetchMarketingMaster(context.Background()); err == nil {
			t.Fatalf("invalid Marketing accepted: %s", invalid)
		}
	}
}
