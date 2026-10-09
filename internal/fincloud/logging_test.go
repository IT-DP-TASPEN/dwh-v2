package fincloud

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRequestLoggingIsDebugOnlyAndNeverCarriesSecrets(t *testing.T) {
	secrets := []string{"PASSWORD_SECRET", "SESSION_SECRET", "QUERY_SECRET", "RESPONSE_SECRET", "PAYLOAD_SECRET"}
	var logins atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/admin/access/login" {
			generation := logins.Add(1)
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"sessionid":"SESSION_SECRET_`+string(rune('0'+generation))+`"}}}`)
			return
		}
		if request.Header.Get("sessionid") == "SESSION_SECRET_1" {
			response.WriteHeader(http.StatusUnauthorized) // stale session: forces one reauthentication
			return
		}
		if strings.Contains(request.URL.RawQuery, "failing") {
			response.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(response, "RESPONSE_SECRET failure")
			return
		}
		_, _ = io.WriteString(response, "RESPONSE_SECRET,PAYLOAD_SECRET\n")
	}))
	defer server.Close()
	var logs bytes.Buffer
	config := testConfig(server.URL)
	config.Password = "PASSWORD_SECRET"
	config.Logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client, err := newClient(config, server.Client())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := client.DownloadReport(context.Background(), "report", "QUERY_SECRET", "PAYLOAD_SECRET"); err != nil {
		t.Fatal(err)
	}
	records := decodeRecords(t, logs.Bytes())
	var completed, reauthenticated int
	for _, record := range records {
		switch record["event"] {
		case "fincloud.request.completed":
			completed++
			if record["level"] != "DEBUG" || record["operation"] != "download report" || record["method"] != "GET" ||
				record["http_status"] != float64(http.StatusOK) || record["duration_ms"] == nil {
				t.Fatalf("completion=%v", record)
			}
		case "fincloud.session.reauthenticated":
			reauthenticated++
			if record["level"] != "INFO" || record["reason"] != "unauthorized" {
				t.Fatalf("reauthentication=%v", record)
			}
		}
	}
	if completed != 1 || reauthenticated != 1 {
		t.Fatalf("completed=%d reauthenticated=%d records=%v", completed, reauthenticated, records)
	}

	logs.Reset()
	_, err = client.DownloadReport(context.Background(), "failing", "QUERY_SECRET")
	if diagnostic, ok := TechnicalDiagnostic(err); !ok || diagnostic.Response == nil || diagnostic.Response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("failure diagnostic lost: %v", err)
	}
	if logs.Len() != 0 {
		t.Fatalf("failed request logged as completion: %s", logs.String())
	}

	client.logger.Error("source", "error", err) // the shape callers use for failures
	for _, secret := range secrets {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("log leaked %s: %s", secret, logs.String())
		}
	}
}

func TestProductionLevelDropsRequestCompletions(t *testing.T) {
	var logs bytes.Buffer
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/admin/access/login" {
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"sessionid":"s"}}}`)
			return
		}
		_, _ = io.WriteString(response, "ok")
	}))
	defer server.Close()
	config := testConfig(server.URL)
	config.Logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	client, err := newClient(config, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	for range 50 {
		if _, err := client.DownloadReport(context.Background(), "report"); err != nil {
			t.Fatal(err)
		}
	}
	if logs.Len() != 0 {
		t.Fatalf("INFO logger received per-request records: %s", logs.String())
	}
}

func decodeRecords(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("bad log line %q", line)
		}
		records = append(records, record)
	}
	return records
}
