package fincloud

import (
	"context"
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

func TestBlockedSavingAccountListingContract(t *testing.T) {
	var result atomic.Value
	result.Store(`[{"id":" B-2 ","nocif":"CIF","namanasabah":"NAME","nilaiblokir":"679,360.00"},{"id":"A-1"},{"id":"B-2"},{"id":"  "}]`)
	var listings, logins atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/fincloud/admin/access/login":
			logins.Add(1)
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"sessionid":"session"}}}`)
		case "/fincloud/cs/inquiry/block_savings/cari":
			want := url.Values{"cabang": {"ALL"}, "datamutasi": {"false"}, "pagenumber": {"0"}, "pagesize": {"999999999999"}}
			if request.Method != http.MethodGet || !reflect.DeepEqual(request.URL.Query(), want) || request.Header.Get("sessionid") != "session" {
				t.Errorf("listing method=%s query=%v", request.Method, request.URL.Query())
			}
			if listings.Add(1) == 1 {
				response.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":`+result.Load().(string)+`,"pageSize":999999999999,"pageNumber":0,"rowCount":"17378"}}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	client, err := newClient(testConfig(server.URL+"/fincloud"), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := client.FetchBlockedSavingAccounts(context.Background())
	if err != nil || !reflect.DeepEqual(accounts, []string{"A-1", "B-2"}) || logins.Load() != 2 {
		t.Fatalf("accounts=%v logins=%d error=%v", accounts, logins.Load(), err)
	}
	result.Store(`[]`)
	if accounts, err := client.FetchBlockedSavingAccounts(context.Background()); err != nil || len(accounts) != 0 {
		t.Fatalf("empty accounts=%v error=%v", accounts, err)
	}
	for _, invalid := range []string{"null", `{}`, `[{"id":1}]`} {
		result.Store(invalid)
		var fincloudErr *Error
		if _, err := client.FetchBlockedSavingAccounts(context.Background()); !errors.As(err, &fincloudErr) || fincloudErr.Kind != ErrorMalformed {
			t.Fatalf("invalid listing %s error=%v", invalid, err)
		}
	}
}

func TestFetchSavingBlockDetailContract(t *testing.T) {
	var body atomic.Value
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/fincloud/admin/access/login":
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"sessionid":"session"}}}`)
		case "/fincloud/cs/inquiry/block_savings/tabungan":
			if request.Method != http.MethodGet || !reflect.DeepEqual(request.URL.Query(), url.Values{"id": {"1130080000000037"}}) {
				t.Errorf("detail method=%s query=%v", request.Method, request.URL.Query())
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
	body.Store(`{"status":"ok","data":{"result":{"total_bo_open":2,"norekening":"1130080000000037", "datablockopen":[{"id_transaction":"A"},{"id_transaction":"B"}]}}}`)
	detail, err := client.FetchSavingBlockDetail(context.Background(), "1130080000000037")
	if err != nil || !strings.Contains(string(detail.RawPayload), `"datablockopen":[{"id_transaction":"A"},{"id_transaction":"B"}]`) || strings.Contains(string(detail.RawPayload), " ") {
		t.Fatalf("detail=%s error=%v", detail.RawPayload, err)
	}
	for _, invalid := range []struct {
		body string
		kind ErrorKind
	}{
		{`{"status":"error","data":{"result":{"norekening":"SECRET-ACCOUNT"}}}`, ErrorUpstream},
		{`{"status":"ok","data":{"result":[]}}`, ErrorMalformed},
		{`{"status":"ok","data":{"result":"text"}}`, ErrorMalformed},
		{`{"status":"ok","data":{}}`, ErrorUpstream},
		{`not json`, ErrorMalformed},
	} {
		body.Store(invalid.body)
		var fincloudErr *Error
		_, err := client.FetchSavingBlockDetail(context.Background(), "1130080000000037")
		if !errors.As(err, &fincloudErr) || fincloudErr.Kind != invalid.kind || strings.Contains(err.Error(), "SECRET-ACCOUNT") {
			t.Fatalf("body %s error=%v", invalid.body, err)
		}
	}
}
