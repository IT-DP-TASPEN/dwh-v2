package fincloud

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFetchSavingAccountStatementContract(t *testing.T) {
	body := atomic.Value{}
	body.Store(`{"status":"ok","data":{"result":{"mutasi":[]}}}`)
	var statements, logins atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/fincloud/admin/access/login":
			logins.Add(1)
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"sessionid":"session"}}}`)
		case "/fincloud/tabungan/inquiry/rekening/historyMutasi":
			if request.Method != http.MethodGet || request.URL.Query().Get("id") != "S-1" || request.Header.Get("sessionid") != "session" {
				t.Errorf("statement request method=%s query=%v session=%q", request.Method, request.URL.Query(), request.Header.Get("sessionid"))
			}
			if statements.Add(1) == 1 {
				response.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(response, body.Load().(string))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	client, err := newClient(testConfig(server.URL+"/fincloud"), server.Client())
	if err != nil {
		t.Fatal(err)
	}

	empty, err := client.FetchSavingAccountStatement(context.Background(), "S-1")
	if err != nil || empty == nil || empty.Mutations == nil || len(empty.Mutations) != 0 || logins.Load() != 2 || statements.Load() != 2 {
		t.Fatalf("empty=%+v logins=%d statements=%d error=%v", empty, logins.Load(), statements.Load(), err)
	}
	body.Store(`{"status":"ok","data":{"result":{"mutasi":[{"tgltransaksi":"2026-08-31","jam":"01:34:35","saldoawal":"9,057,279.49","debit":"3,000.00","kredit":null,"saldoakhir":"9,054,279.49","saldoakhir_equivalent":"9,054,279.49","jenistransaksi":"fee","keterangan":"  exact text  ","referensi":"ref","lokasi":"HQ","nojurnal":"journal","rec_dibuat_oleh":"system","trx_rate":1,"mid_rate_dc":"1.00","future":"kept"}]}}}`)
	statement, err := client.FetchSavingAccountStatement(context.Background(), "S-1")
	if err != nil || len(statement.Mutations) != 1 {
		t.Fatalf("statement=%+v error=%v", statement, err)
	}
	item := statement.Mutations[0]
	if item.Credit != nil || item.Description == nil || *item.Description != "  exact text  " || item.TransactionRate == nil || item.TransactionRate.String() != "1" || !strings.Contains(string(item.RawPayload), `"future":"kept"`) {
		t.Fatalf("item=%+v raw=%s", item, item.RawPayload)
	}
}

func TestFetchSavingAccountStatementRejectsMalformedContractsWithoutLeakingBody(t *testing.T) {
	body := `{"status":"ok"}`
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/admin/access/login" {
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"sessionid":"session"}}}`)
			return
		}
		_, _ = io.WriteString(response, body)
	}))
	defer server.Close()
	client, err := newClient(testConfig(server.URL), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	invalid := []string{
		`{"status":"failed","data":{"result":{"mutasi":[]}}}`,
		`{"status":"ok"}`,
		`{"status":"ok","data":null}`,
		`{"status":"ok","data":[]}`,
		`{"status":"ok","data":{}}`,
		`{"status":"ok","data":{"result":null}}`,
		`{"status":"ok","data":{"result":[]}}`,
		`{"status":"ok","data":{"result":{}}}`,
		`{"status":"ok","data":{"result":{"mutasi":null}}}`,
		`{"status":"ok","data":{"result":{"mutasi":{}}}}`,
		`{"status":"ok","data":{"result":{"mutasi":[null]}}}`,
		`{"status":"ok","data":{"result":{"mutasi":[{"trx_rate":"1"}]}}}`,
		`{"status":"ok","data":{"result":{"mutasi":[{"mid_rate_dc":1}]}}}`,
		`{"status":"ok","data":{"result":{"mutasi":[{"keterangan":1}]}}}`,
		`{"status":"ok","data":{"result":{"mutasi":[{"tgltransaksi":"31-08-2026"}]}}}`,
		`{"status":"ok","data":{"result":{"mutasi":[{"jam":"1:02:03"}]}}}`,
		`{"status":"ok","data":{"result":{"mutasi":[{"saldoawal":"1,23.00"}]}}}`,
		`{"status":"ok","data":{"result":{"mutasi":[]}}} {}`,
	}
	for _, candidate := range invalid {
		body = candidate
		_, err := client.FetchSavingAccountStatement(context.Background(), "ACCOUNT-SECRET-123456")
		if err == nil {
			t.Fatalf("invalid statement accepted: %s", candidate)
		}
		diagnostic, ok := TechnicalDiagnostic(err)
		if !ok || diagnostic.Response == nil || !diagnostic.Response.Body.Redacted || strings.Contains(diagnostic.Response.Body.Body, "ACCOUNT-SECRET") || strings.Contains(diagnostic.Response.Body.Body, "mutasi") {
			t.Fatalf("unsafe diagnostic=%+v error=%v", diagnostic, err)
		}
		if values := diagnostic.Request.Query["id"]; len(values) > 0 && values[0] != "REDACTED" {
			t.Fatalf("statement account leaked in request diagnostic: %+v", diagnostic.Request)
		}
	}
}
