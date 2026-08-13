package fincloud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSourceEnumerationReportProtocolAndTypedDetail(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/fincloud/admin/access/login":
			_ = request.ParseForm()
			if request.Form.Get("locationid") != "001" || request.Form.Get("roleid") != "role" {
				t.Errorf("login form = %v", request.Form)
			}
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"sessionid":"session-secret"}}}`)
		case "/fincloud/admin/access/listvalues":
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"locationid":[{"id":"000","descr":"HQ"},{"id":"008","descr":"Branch"}]}}}`)
		case "/fincloud/bukuBesar/laporan/mutasiAkun//listvalues":
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"noakun":[{"id":"1.2","descr":"Cash"},{"id":"","descr":"Blank preserved for domain normalization"}]}}}`)
		case "/fincloud/system/laporanUmum/data/lap":
			body, _ := io.ReadAll(request.Body)
			form, _ := url.ParseQuery(string(body))
			if request.Method != http.MethodGet || form.Get("sessionId") != "session-secret" || request.Header.Get("sessionid") != "session-secret" {
				t.Errorf("report protocol method=%s form=%v session=%q", request.Method, form, request.Header.Get("sessionid"))
			}
			var parameters []string
			if err := json.Unmarshal([]byte(request.URL.Query().Get("p")), &parameters); err != nil {
				t.Error(err)
			}
			if !reflect.DeepEqual(parameters, []string{"", "2026-08-12", "2026-08-12"}) {
				t.Errorf("parameters = %v", parameters)
			}
			_, _ = io.WriteString(response, "\uFEFFA|B\n1|2\n")
		case "/fincloud/pinjaman/inquiry/rekening/pinjaman":
			if request.URL.Query().Get("id") != "LN-1" {
				t.Errorf("loan id = %q", request.URL.Query().Get("id"))
			}
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"id":"LN-1","nocif":"C-1","outstandingpinjaman":1234567890.123456,"jadwalangsuran":[{"angsuranke":"1","angsuran":"10.25"}]}}}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	client, err := newClient(testConfig(server.URL+"/fincloud"), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	locations, err := client.FetchAccessibleLocations(context.Background())
	if err != nil || len(locations) != 2 || locations[1].ID != "008" {
		t.Fatalf("locations=%v error=%v", locations, err)
	}
	codes, err := client.FetchAccountCodes(context.Background())
	if err != nil || len(codes) != 2 || codes[1].ID != "" {
		t.Fatalf("codes=%v error=%v", codes, err)
	}
	report, err := client.DownloadReport(context.Background(), "Vault Mutation Report csv", "", "2026-08-12", "2026-08-12")
	if err != nil || strings.HasPrefix(report, "\uFEFF") {
		t.Fatalf("report=%q error=%v", report, err)
	}
	detail, err := client.FetchLoanDetail(context.Background(), "LN-1")
	if err != nil {
		t.Fatal(err)
	}
	decimalValue, err := detail.Outstanding.Decimal()
	if err != nil || decimalValue.String() != "1234567890.123456" || len(detail.Repayment) != 1 || string(detail.RawPayload) == "" {
		t.Fatalf("detail=%+v decimal=%s error=%v", detail, decimalValue, err)
	}
}

func TestSecureTLSDefaultAndExplicitOptIn(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/admin/access/login" {
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"sessionid":"session"}}}`)
			return
		}
		_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"locationid":[]}}}`)
	}))
	defer server.Close()
	secure, err := NewClient(testConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secure.FetchAccessibleLocations(context.Background()); err == nil {
		t.Fatal("secure default trusted a self-signed certificate")
	}
	insecureConfig := testConfig(server.URL)
	insecureConfig.InsecureSkipVerify = true
	insecure, err := NewClient(insecureConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := insecure.FetchAccessibleLocations(context.Background()); err != nil {
		t.Fatalf("explicit insecure TLS opt-in failed: %v", err)
	}
}

func TestUnauthorizedBodiesAreClosed(t *testing.T) {
	var loginCount atomic.Int32
	firstUnauthorized, secondUnauthorized := &trackedBody{Reader: strings.NewReader("")}, &trackedBody{Reader: strings.NewReader("")}
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/admin/access/login" {
			loginCount.Add(1)
			return jsonResponse(`{"status":"ok","data":{"result":{"sessionid":"session"}}}`), nil
		}
		body := firstUnauthorized
		if loginCount.Load() == 2 {
			body = secondUnauthorized
		}
		return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: body}, nil
	})
	client, err := newClient(testConfig("https://fincloud.test"), &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = client.FetchAccessibleLocations(context.Background())
	if !firstUnauthorized.closed || !secondUnauthorized.closed {
		t.Fatalf("unauthorized bodies closed first=%t second=%t", firstUnauthorized.closed, secondUnauthorized.closed)
	}
}

type trackedBody struct {
	io.Reader
	closed bool
}

func (body *trackedBody) Close() error { body.closed = true; return nil }
