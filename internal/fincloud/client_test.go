package fincloud

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientConstructorDoesNoNetworkIO(t *testing.T) {
	transport := roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("constructor performed network I/O")
		return nil, nil
	})
	client, err := newClient(testConfig("https://fincloud.test"), &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	client.CloseIdleConnections()
}

func TestConcurrentInitialLoginAndGenerationAwareReauthentication(t *testing.T) {
	var logins atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/fincloud/admin/access/login":
			generation := logins.Add(1)
			response.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"sessionid":"session-`+string(rune('0'+generation))+`"}}}`)
		case "/fincloud/admin/access/listvalues":
			if request.Header.Get("sessionid") == "session-1" {
				response.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"locationid":[]}}}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	client, err := newClient(testConfig(server.URL+"/fincloud"), server.Client())
	if err != nil {
		t.Fatal(err)
	}

	const callers = 20
	start := make(chan struct{})
	errorsFound := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, callErr := client.FetchAccessibleLocations(context.Background())
			errorsFound <- callErr
		}()
	}
	close(start)
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := logins.Load(); got != 2 {
		t.Fatalf("login count = %d, want one initial login and one generation refresh", got)
	}
}

func TestUnauthorizedRetryRebuildsBodyAndStopsAfterOneRetry(t *testing.T) {
	var logins atomic.Int32
	var bodiesMu sync.Mutex
	var bodies []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/admin/access/login" {
			generation := logins.Add(1)
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"sessionid":"s`+string(rune('0'+generation))+`"}}}`)
			return
		}
		body, _ := io.ReadAll(request.Body)
		bodiesMu.Lock()
		bodies = append(bodies, string(body))
		bodiesMu.Unlock()
		response.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	client, err := newClient(testConfig(server.URL), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.DownloadReport(context.Background(), "Journal Transaction csv", "", "2026-08-11", "2026-08-11", "", "")
	var sourceError *Error
	if !errors.As(err, &sourceError) || sourceError.Kind != ErrorUnauthorized {
		t.Fatalf("error = %v, want unauthorized source error", err)
	}
	if logins.Load() != 2 || len(bodies) != 2 || !strings.Contains(bodies[0], "sessionId=s1") || !strings.Contains(bodies[1], "sessionId=s2") {
		t.Fatalf("logins=%d bodies=%q, want rebuilt bodies for two generations", logins.Load(), bodies)
	}
}

func TestLoginWaiterHonorsContextCancellation(t *testing.T) {
	loginStarted := make(chan struct{})
	releaseLogin := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/admin/access/login" {
			select {
			case <-loginStarted:
			default:
				close(loginStarted)
			}
			<-releaseLogin
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"sessionid":"session"}}}`)
			return
		}
		_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"locationid":[]}}}`)
	}))
	defer server.Close()
	client, err := newClient(testConfig(server.URL), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	leaderDone := make(chan error, 1)
	go func() {
		_, callErr := client.FetchAccessibleLocations(context.Background())
		leaderDone <- callErr
	}()
	<-loginStarted
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err = client.FetchAccessibleLocations(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	close(releaseLogin)
	if err := <-leaderDone; err != nil {
		t.Fatal(err)
	}
}

func TestTransportErrorsRedactSessionValues(t *testing.T) {
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/admin/access/login" {
			return jsonResponse(`{"status":"ok","data":{"result":{"sessionid":"top-secret-session"}}}`), nil
		}
		return nil, &url.Error{Op: "Get", URL: request.URL.String() + "?sessionId=top-secret-session", Err: errors.New("dial failed")}
	})
	client, err := newClient(testConfig("https://fincloud.test"), &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.DownloadMaintenanceReport(context.Background(), "file.csv", "/reports")
	if err == nil || strings.Contains(err.Error(), "top-secret-session") {
		t.Fatalf("error leaked session: %v", err)
	}
	if !strings.Contains(err.Error(), "Fincloud request failed") {
		t.Fatalf("unexpected sanitized error: %v", err)
	}
}

func TestResponseErrorPreservesSafeHTTPStatus(t *testing.T) {
	var sourceError *Error
	err := responseError("fetch detail", http.StatusTooManyRequests)
	if !errors.As(err, &sourceError) || sourceError.HTTPStatus != http.StatusTooManyRequests || strings.Contains(err.Error(), "body") {
		t.Fatalf("typed status error=%+v", sourceError)
	}
}

func TestDownloadReportPreservesSafeBodyReadCause(t *testing.T) {
	for _, test := range []struct {
		name, class string
		err         error
	}{
		{"deadline", "deadline_exceeded", context.DeadlineExceeded},
		{"unexpected EOF", "unexpected_eof", io.ErrUnexpectedEOF},
		{"generic", "response_body_read_error", errors.New("read failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := &failingBody{err: test.err}
			transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path == "/admin/access/login" {
					return jsonResponse(`{"status":"ok","data":{"result":{"sessionid":"session"}}}`), nil
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
			})
			client, err := newClient(testConfig("https://fincloud.test"), &http.Client{Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.DownloadReport(context.Background(), "report")
			if err == nil || SafeCauseClass(err) != test.class || body.closed != 1 {
				t.Fatalf("class=%q closed=%d error=%v", SafeCauseClass(err), body.closed, err)
			}
		})
	}
}

func TestSafeCauseClassRecognizesWrappedNetworkTimeout(t *testing.T) {
	err := &url.Error{Op: "Get", URL: "https://fincloud.test", Err: timeoutError{}}
	if got := SafeCauseClass(err); got != "network_timeout" {
		t.Fatalf("class=%q", got)
	}
}

func testConfig(baseURL string) Config {
	return Config{BaseURL: baseURL, Username: "user", Password: "password", LocationID: "001", RoleID: "role", HTTPTimeout: time.Second}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func jsonResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

type failingBody struct {
	err    error
	closed int
}

func (body *failingBody) Read([]byte) (int, error) { return 0, body.err }
func (body *failingBody) Close() error             { body.closed++; return nil }

type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
