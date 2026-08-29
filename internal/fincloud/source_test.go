package fincloud

import (
	"context"
	"encoding/json"
	"errors"
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
		case "/fincloud/bukuBesar/laporan/jurnal//listvalues":
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"jenistransaksi":[{"id":"001","descr":"Cash deposit"},{"id":"TRX-2","descr":"Transfer"}]}}}`)
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
			want := []string{"", "2026-08-12", "2026-08-12"}
			content := "\uFEFFA|B\n1|2\n"
			if request.URL.Query().Get("nm") == "CIF Opening Report" {
				want = []string{"", "1900-01-01", "2026-08-12"}
				content = "\uFEFFCIF No|Name\n C2 |x\nC1|y\nC1|z\n"
			}
			if !reflect.DeepEqual(parameters, want) {
				t.Errorf("parameters = %v", parameters)
			}
			_, _ = io.WriteString(response, content)
		case "/fincloud/tabungan/inquiry/rekening/cari":
			if request.URL.Query().Get("cabang") != "ALL" || request.URL.Query().Get("datamutasi") != "false" {
				t.Errorf("saving query=%v", request.URL.Query())
			}
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":[{"id":"S2"},{"id":"S1"}]}}`)
		case "/fincloud/deposito/inquiry/rekening/cari":
			if request.URL.Query().Get("cabang") != "ALL" {
				t.Errorf("time-deposit query=%v", request.URL.Query())
			}
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":[{"id":"T1"}]}}`)
		case "/fincloud/pinjaman/inquiry/rekening/cari":
			if request.URL.Query().Get("cabang") != "ALL" {
				t.Errorf("loan query=%v", request.URL.Query())
			}
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":[{"id":"L2"},{"id":"L1"}]}}`)
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
	transactionTypes, err := client.FetchJournalTransactionTypes(context.Background())
	if err != nil || !reflect.DeepEqual(transactionTypes, []JournalTransactionType{{ID: "001", Description: "Cash deposit"}, {ID: "TRX-2", Description: "Transfer"}}) {
		t.Fatalf("journal transaction types=%v error=%v", transactionTypes, err)
	}
	cifs, err := client.FetchCIFNumbers(context.Background(), "2026-08-12")
	if err != nil || !reflect.DeepEqual(cifs, []string{"C1", "C2"}) {
		t.Fatalf("CIFs=%v error=%v", cifs, err)
	}
	savings, err := client.FetchSavingAccounts(context.Background())
	if err != nil || !reflect.DeepEqual(savings, []string{"S1", "S2"}) {
		t.Fatalf("savings=%v error=%v", savings, err)
	}
	deposits, err := client.FetchTimeDepositAccounts(context.Background())
	if err != nil || !reflect.DeepEqual(deposits, []string{"T1"}) {
		t.Fatalf("deposits=%v error=%v", deposits, err)
	}
	loans, err := client.FetchLoanAccounts(context.Background())
	if err != nil || !reflect.DeepEqual(loans, []string{"L1", "L2"}) {
		t.Fatalf("loans=%v error=%v", loans, err)
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

func TestJournalTransactionTypeListingRejectsInvalidContracts(t *testing.T) {
	responseBody := `{"status":"ok","data":{"result":{"jenistransaksi":[]}}}`
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/fincloud/admin/access/login" {
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"sessionid":"session"}}}`)
			return
		}
		_, _ = io.WriteString(response, responseBody)
	}))
	defer server.Close()
	client, err := newClient(testConfig(server.URL+"/fincloud"), server.Client())
	if err != nil {
		t.Fatal(err)
	}

	transactionTypes, err := client.FetchJournalTransactionTypes(context.Background())
	if err != nil || len(transactionTypes) != 0 {
		t.Fatalf("valid empty listing=%v error=%v", transactionTypes, err)
	}
	for _, invalid := range []string{
		`{"status":"failed","data":{"result":{"jenistransaksi":[]}}}`,
		`{"status":"ok","data":{"result":{}}}`,
		`{"status":"ok","data":{"result":{"jenistransaksi":null}}}`,
		`{"status":"ok","data":{"result":{"jenistransaksi":{}}}}`,
		`{"status":"ok"`,
	} {
		responseBody = invalid
		if _, err := client.FetchJournalTransactionTypes(context.Background()); err == nil {
			t.Fatalf("invalid journal transaction-type listing accepted: %s", invalid)
		}
	}
}

func TestDetailListingsAcceptOnlyContractValidEmptyResults(t *testing.T) {
	var accountResult atomic.Value
	accountResult.Store(`[]`)
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/fincloud/admin/access/login":
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"sessionid":"session"}}}`)
		case "/fincloud/system/laporanUmum/data/lap":
			_, _ = io.WriteString(response, "CIF No|Name\n")
		default:
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":`+accountResult.Load().(string)+`}}`)
		}
	}))
	defer server.Close()
	client, err := newClient(testConfig(server.URL+"/fincloud"), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	for name, fetch := range map[string]func(context.Context) ([]string, error){
		"CIF":          func(ctx context.Context) ([]string, error) { return client.FetchCIFNumbers(ctx, "2026-08-12") },
		"saving":       client.FetchSavingAccounts,
		"time deposit": client.FetchTimeDepositAccounts,
		"loan":         client.FetchLoanAccounts,
	} {
		values, err := fetch(context.Background())
		if err != nil || len(values) != 0 {
			t.Fatalf("%s empty listing=%v error=%v", name, values, err)
		}
	}
	for name, fetch := range map[string]func(context.Context) ([]string, error){
		"saving":       client.FetchSavingAccounts,
		"time deposit": client.FetchTimeDepositAccounts,
	} {
		for _, invalid := range []string{"null", `{}`} {
			accountResult.Store(invalid)
			if _, err := fetch(context.Background()); err == nil {
				t.Fatalf("%s invalid result %s accepted", name, invalid)
			}
		}
	}
	accountResult.Store(`[]`)
	server.Config.Handler = http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/fincloud/admin/access/login" {
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"sessionid":"session"}}}`)
			return
		}
		_, _ = io.WriteString(response, `{"status":"ok","data":{}}`)
	})
	if _, err := client.FetchSavingAccounts(context.Background()); err == nil {
		t.Fatal("missing result accepted")
	}
}

func TestLoanAccountListingNullAndMalformedContracts(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		bodies    map[string]string
		want      []string
		wantKind  ErrorKind
		wantCalls []string
	}{
		{
			name:      "explicit null is empty",
			body:      `{"data":{"result":null},"status":"ok"}`,
			want:      []string{},
			wantCalls: []string{"Aktif", "Closed", "WO", "HT"},
		},
		{
			name: "null status among populated statuses",
			bodies: map[string]string{
				"Aktif":  `{"status":"ok","data":{"result":[{"id":"L2"},{"id":"L1"}]}}`,
				"Closed": `{"status":"ok","data":{"result":null}}`,
				"WO":     `{"status":"ok","data":{"result":[{"id":"L3"}]}}`,
				"HT":     `{"status":"ok","data":{"result":null}}`,
			},
			want:      []string{"L1", "L2", "L3"},
			wantCalls: []string{"Aktif", "Closed", "WO", "HT"},
		},
		{name: "missing data", body: `{"status":"ok"}`, wantKind: ErrorMalformed, wantCalls: []string{"Aktif"}},
		{name: "null data", body: `{"status":"ok","data":null}`, wantKind: ErrorMalformed, wantCalls: []string{"Aktif"}},
		{name: "wrong data type", body: `{"status":"ok","data":[]}`, wantKind: ErrorMalformed, wantCalls: []string{"Aktif"}},
		{name: "missing result", body: `{"status":"ok","data":{}}`, wantKind: ErrorMalformed, wantCalls: []string{"Aktif"}},
		{name: "application failure", body: `{"status":"failed","data":{"result":null}}`, wantKind: ErrorUpstream, wantCalls: []string{"Aktif"}},
		{name: "wrong non-null result type", body: `{"status":"ok","data":{"result":{}}}`, wantKind: ErrorMalformed, wantCalls: []string{"Aktif"}},
		{name: "malformed JSON", body: `{"status":"ok"`, wantKind: ErrorMalformed, wantCalls: []string{"Aktif"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := []string{}
			server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/fincloud/admin/access/login":
					_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"sessionid":"session"}}}`)
				case "/fincloud/pinjaman/inquiry/rekening/cari":
					status := request.URL.Query().Get("status")
					calls = append(calls, status)
					body := test.body
					if test.bodies != nil {
						body = test.bodies[status]
					}
					_, _ = io.WriteString(response, body)
				default:
					http.NotFound(response, request)
				}
			}))
			defer server.Close()
			client, err := newClient(testConfig(server.URL+"/fincloud"), server.Client())
			if err != nil {
				t.Fatal(err)
			}

			values, err := client.FetchLoanAccounts(context.Background())
			if test.wantKind == "" {
				if err != nil || !reflect.DeepEqual(values, test.want) {
					t.Fatalf("values=%v error=%v want=%v", values, err, test.want)
				}
			} else {
				var sourceError *Error
				if !errors.As(err, &sourceError) || sourceError.Kind != test.wantKind {
					t.Fatalf("error=%v kind=%v want=%v", err, sourceError, test.wantKind)
				}
			}
			if !reflect.DeepEqual(calls, test.wantCalls) {
				t.Fatalf("calls=%v want=%v", calls, test.wantCalls)
			}
		})
	}
}

func TestScalarDecimalAcceptsOnlyStrictCommaGrouping(t *testing.T) {
	valid := map[string]string{
		"0": "0", "1234.56": "1234.56", "-0.01": "-0.01",
		"1,234.56": "1234.56", "+12,345.67": "12345.67",
		"-1,234,567.89": "-1234567.89", "1,234,567": "1234567",
	}
	for input, want := range valid {
		value, err := Scalar(input).Decimal()
		if err != nil || value.String() != want {
			t.Fatalf("Decimal(%q)=%s error=%v want=%s", input, value, err, want)
		}
	}
	for _, input := range []string{
		"", " ", "1,234", "12,34.56", "1,,234.56", "1,234.",
		"1.234,56", "1234,56", "1 234.56", "Rp1,234.56", "garbage",
	} {
		if _, err := Scalar(input).Decimal(); err == nil {
			t.Fatalf("Decimal(%q) unexpectedly succeeded", input)
		}
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
