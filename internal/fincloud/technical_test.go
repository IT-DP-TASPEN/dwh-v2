package fincloud

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func TestTechnicalDiagnosticsPreserveHTTPApplicationAndDecodeEvidence(t *testing.T) {
	tests := []struct {
		name, body, failure, application string
		status                           int
		call                             func(*Client) error
	}{
		{"http", `{"status":"error","message":"internal error"}`, "http", "error", 500, func(client *Client) error {
			_, err := client.DownloadReport(context.Background(), "report")
			return err
		}},
		{"application", `{"status":"77","message":"Data Not Found","data":{"result":{}}}`, "application", "77", 200, func(client *Client) error {
			_, err := client.FetchAccessibleLocations(context.Background())
			return err
		}},
		{"malformed", `{"status":`, "malformed_json", "", 200, func(client *Client) error {
			_, err := client.FetchAccessibleLocations(context.Background())
			return err
		}},
		{"dto", `{"status":"ok","data":{"result":{"roleid":{},"locationid":[]}}}`, "dto_decode", "ok", 200, func(client *Client) error {
			_, err := client.FetchAccessibleLocations(context.Background())
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := diagnosticTestClient(t, func(request *http.Request) *http.Response {
				return diagnosticResponse(test.status, []byte(test.body), nil)
			})
			err := test.call(client)
			if err == nil {
				t.Fatal("expected source failure")
			}
			diagnostic, ok := TechnicalDiagnostic(err)
			if !ok || diagnostic.FailureKind != test.failure || diagnostic.Response == nil || diagnostic.Response.StatusCode != test.status {
				t.Fatalf("diagnostic=%+v error=%v", diagnostic, err)
			}
			if diagnostic.Application.Status != test.application || !strings.Contains(diagnostic.Response.Body.Body, strings.Trim(test.body, "{}")[:min(8, len(strings.Trim(test.body, "{}")))]) {
				t.Fatalf("application/body evidence=%+v", diagnostic)
			}
			if test.name == "application" && diagnostic.Application.Message != "Data Not Found" {
				t.Fatalf("application message=%q", diagnostic.Application.Message)
			}
			if diagnostic.Request.Method == "" || diagnostic.Request.Path == "" {
				t.Fatalf("request evidence=%+v", diagnostic.Request)
			}
		})
	}
}

func TestTechnicalDiagnosticsPreservePartialReadAndTimeout(t *testing.T) {
	for _, cause := range []error{io.ErrUnexpectedEOF, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			client := diagnosticTestClient(t, func(request *http.Request) *http.Response {
				return diagnosticResponse(http.StatusOK, []byte("partial-body"), cause)
			})
			_, err := client.DownloadReport(context.Background(), "report")
			diagnostic, ok := TechnicalDiagnostic(err)
			if !ok || diagnostic.Response == nil {
				t.Fatalf("missing diagnostic: %v", err)
			}
			body := diagnostic.Response.Body
			if body.DeclaredContentLength == nil || *body.DeclaredContentLength != 99 || body.BytesRead != 12 || body.BytesCaptured != 12 || body.Truncated || body.ReadError != cause.Error() || body.Body != "partial-body" {
				t.Fatalf("body=%+v", body)
			}
			if !errors.Is(err, cause) || SafeCauseClass(err) == "context_canceled" {
				t.Fatalf("cause/class=%v %q", err, SafeCauseClass(err))
			}
		})
	}
}

func TestTechnicalBodyIsByteSafeBoundedRedactedAndCompact(t *testing.T) {
	marker := "technical-evidence-marker"
	raw := append([]byte{0xff, 0xfe}, []byte(` Authorization=secret-token password=password "api_key":"binary-secret" `+marker)...)
	client := diagnosticTestClient(t, func(request *http.Request) *http.Response {
		return diagnosticResponse(http.StatusInternalServerError, raw, nil)
	})
	_, err := client.DownloadReport(context.Background(), "report")
	diagnostic, ok := TechnicalDiagnostic(err)
	if !ok || diagnostic.Response.Body.Encoding != "base64" || !diagnostic.Response.Body.Redacted {
		t.Fatalf("diagnostic=%+v", diagnostic)
	}
	decoded, decodeErr := base64.StdEncoding.DecodeString(diagnostic.Response.Body.Body)
	if decodeErr != nil || bytes.Contains(decoded, []byte("secret-token")) || bytes.Contains(decoded, []byte("password")) || bytes.Contains(decoded, []byte("binary-secret")) || !bytes.Contains(decoded, []byte(marker)) {
		t.Fatalf("decoded body=%q decode_error=%v", decoded, decodeErr)
	}
	encodedDiagnostic, _ := json.Marshal(diagnostic)
	if bytes.Contains(encodedDiagnostic, []byte("cookie-secret")) || bytes.Contains(encodedDiagnostic, []byte("Set-Cookie")) {
		t.Fatalf("response headers leaked cookie: %s", encodedDiagnostic)
	}
	var logged bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logged, nil))
	logger.Error("source", "error", err)
	jsonError, _ := json.Marshal(err)
	for _, printable := range []string{fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err), logged.String(), string(jsonError)} {
		if strings.Contains(printable, marker) || strings.Contains(printable, "secret-token") {
			t.Fatalf("compact error leaked body: %q", printable)
		}
	}
	request, _ := http.NewRequest(http.MethodGet, "https://fincloud.test/report?token=secret-token&selector=user-password", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	request.Header.Set("Cookie", "session=session-secret")
	request.Header.Set("Accept", "application/json")
	sanitizedRequest, _ := json.Marshal(client.sanitizeRequest(request))
	if bytes.Contains(sanitizedRequest, []byte("secret-token")) || bytes.Contains(sanitizedRequest, []byte("user-password")) || bytes.Contains(sanitizedRequest, []byte("Authorization")) || bytes.Contains(sanitizedRequest, []byte("Cookie")) {
		t.Fatalf("request diagnostic leaked secret: %s", sanitizedRequest)
	}
}

func TestTechnicalBodyCaptureIsBoundedWithoutLosingLengthEvidence(t *testing.T) {
	raw := bytes.Repeat([]byte("x"), MaxErrorBodyCapture+123)
	client := diagnosticTestClient(t, func(request *http.Request) *http.Response {
		return diagnosticResponse(http.StatusBadGateway, raw, nil)
	})
	_, err := client.DownloadReport(context.Background(), "report")
	diagnostic, ok := TechnicalDiagnostic(err)
	if !ok || diagnostic.Response == nil {
		t.Fatalf("missing diagnostic: %v", err)
	}
	body := diagnostic.Response.Body
	if body.DeclaredContentLength == nil || *body.DeclaredContentLength != int64(len(raw)) || body.BytesRead != int64(len(raw)) || body.BytesCaptured != MaxErrorBodyCapture || !body.Truncated || len(body.Body) != MaxErrorBodyCapture {
		t.Fatalf("body=%+v payload_length=%d", body, len(body.Body))
	}
}

func diagnosticTestClient(t *testing.T, response func(*http.Request) *http.Response) *Client {
	t.Helper()
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/admin/access/login" {
			return jsonResponse(`{"status":"ok","data":{"result":{"sessionid":"session-secret"}}}`), nil
		}
		found := response(request)
		found.Request = request
		return found, nil
	})
	client, err := newClient(testConfig("https://fincloud.test"), &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func diagnosticResponse(status int, data []byte, readErr error) *http.Response {
	var body io.ReadCloser = io.NopCloser(bytes.NewReader(data))
	contentLength := int64(len(data))
	if readErr != nil {
		body = &partialFailureBody{data: data, err: readErr}
		contentLength = 99
	}
	return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header: http.Header{"Content-Type": {"application/json"}, "Set-Cookie": {"session=cookie-secret"}}, ContentLength: contentLength, Body: body}
}

type partialFailureBody struct {
	data []byte
	err  error
}

func (body *partialFailureBody) Read(target []byte) (int, error) {
	if len(body.data) > 0 {
		count := copy(target, body.data)
		body.data = body.data[count:]
		return count, nil
	}
	return 0, body.err
}

func (*partialFailureBody) Close() error { return nil }
