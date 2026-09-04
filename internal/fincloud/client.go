package fincloud

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:142.0) Gecko/20100101 Firefox/142.0"

const MaxErrorBodyCapture = 64 << 10

type Config struct {
	BaseURL            string
	Username           string
	Password           string
	LocationID         string
	RoleID             string
	HTTPTimeout        time.Duration
	InsecureSkipVerify bool
}

type ErrorKind string

const (
	ErrorAuthentication ErrorKind = "authentication"
	ErrorUnauthorized   ErrorKind = "unauthorized"
	ErrorUpstream       ErrorKind = "upstream"
	ErrorMalformed      ErrorKind = "malformed_response"
)

type Error struct {
	Kind       ErrorKind
	Operation  string
	Message    string
	HTTPStatus int
	Cause      error
	diagnostic *DiagnosticPayload
}

func (e *Error) Error() string {
	if e.Operation == "" {
		return e.Message
	}
	return e.Operation + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.Cause }

func (e *Error) Format(state fmt.State, verb rune) {
	if verb == 'q' {
		_, _ = fmt.Fprintf(state, "%q", e.Error())
		return
	}
	_, _ = io.WriteString(state, e.Error())
}

func (e *Error) LogValue() slog.Value {
	return slog.GroupValue(slog.String("kind", string(e.Kind)), slog.String("operation", e.Operation),
		slog.String("message", e.Message), slog.Int("http_status", e.HTTPStatus), slog.String("cause_type", fmt.Sprintf("%T", e.Cause)))
}

func (e *Error) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Kind, Operation, Message, CauseType string
		HTTPStatus                          int
	}{string(e.Kind), e.Operation, e.Message, fmt.Sprintf("%T", e.Cause), e.HTTPStatus})
}

type RequestDiagnostic struct {
	Method  string              `json:"method,omitempty"`
	Path    string              `json:"path,omitempty"`
	Query   map[string][]string `json:"query,omitempty"`
	Headers map[string][]string `json:"headers,omitempty"`
}

type BodyDiagnostic struct {
	DeclaredContentLength *int64 `json:"declared_content_length,omitempty"`
	BytesRead             int64  `json:"bytes_read"`
	BytesCaptured         int    `json:"bytes_captured"`
	CaptureLimit          int    `json:"capture_limit"`
	Truncated             bool   `json:"truncated"`
	Encoding              string `json:"body_encoding,omitempty"`
	Body                  string `json:"body,omitempty"`
	Redacted              bool   `json:"body_redacted,omitempty"`
	ReadError             string `json:"body_read_error,omitempty"`
}

type ResponseDiagnostic struct {
	StatusCode      int                 `json:"status_code,omitempty"`
	Status          string              `json:"status,omitempty"`
	ContentType     string              `json:"content_type,omitempty"`
	ContentEncoding string              `json:"content_encoding,omitempty"`
	DurationMS      int64               `json:"duration_ms"`
	Headers         map[string][]string `json:"headers,omitempty"`
	Body            BodyDiagnostic      `json:"body"`
}

type ApplicationDiagnostic struct {
	Status  string `json:"status,omitempty"`
	Message string `json:"message,omitempty"`
}

type DiagnosticPayload struct {
	FailureKind string                `json:"failure_kind,omitempty"`
	DurationMS  int64                 `json:"duration_ms"`
	Request     RequestDiagnostic     `json:"request"`
	Response    *ResponseDiagnostic   `json:"response,omitempty"`
	Application ApplicationDiagnostic `json:"application,omitempty"`
	DecodeStage string                `json:"decode_stage,omitempty"`
}

func TechnicalDiagnostic(err error) (DiagnosticPayload, bool) {
	var sourceError *Error
	if !errors.As(err, &sourceError) || sourceError.diagnostic == nil {
		return DiagnosticPayload{}, false
	}
	data, _ := json.Marshal(sourceError.diagnostic)
	var copy DiagnosticPayload
	_ = json.Unmarshal(data, &copy)
	return copy, true
}

type requestStartedKey struct{}

var secretTextPattern = regexp.MustCompile(`(?i)(authorization|cookie|session(?:id)?|token|password|pwd|secret|api[_-]?key)(["']?\s*[=:]\s*["']?)[^\s,;\"'}]+`)

type session struct {
	id         string
	generation uint64
}

type loginCall struct {
	done chan struct{}
	err  error
}

type Client struct {
	config     Config
	baseURL    string
	httpClient *http.Client

	mu      sync.Mutex
	session session
	login   *loginCall
}

func NewClient(config Config) (*Client, error) {
	httpClient, err := configuredHTTPClient(config)
	if err != nil {
		return nil, err
	}
	return newClient(config, httpClient)
}

// NewPreAuthClient creates a client for Fincloud endpoints that explicitly do
// not require credentials or a session.
func NewPreAuthClient(config Config) (*Client, error) {
	httpClient, err := configuredHTTPClient(config)
	if err != nil {
		return nil, err
	}
	return newPreAuthClient(config, httpClient)
}

func configuredHTTPClient(config Config) (*http.Client, error) {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("unsupported default HTTP transport")
	}
	transport = transport.Clone()
	if config.InsecureSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit operator opt-in
	}
	return &http.Client{Timeout: config.HTTPTimeout, Transport: transport}, nil
}

func newClient(config Config, httpClient *http.Client) (*Client, error) {
	if config.Password == "" || invalidAuthIdentifier(config.Username) || invalidAuthIdentifier(config.LocationID) || invalidAuthIdentifier(config.RoleID) {
		return nil, errors.New("incomplete Fincloud configuration")
	}
	return newPreAuthClient(config, httpClient)
}

func newPreAuthClient(config Config, httpClient *http.Client) (*Client, error) {
	if strings.TrimSpace(config.BaseURL) == "" {
		return nil, errors.New("incomplete Fincloud configuration")
	}
	if config.HTTPTimeout <= 0 {
		return nil, errors.New("Fincloud HTTP timeout must be positive")
	}
	parsed, err := url.Parse(config.BaseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("Fincloud base URL must be an absolute HTTPS URL without credentials, query, or fragment")
	}
	if httpClient == nil {
		return nil, errors.New("Fincloud HTTP client is required")
	}
	return &Client{config: config, baseURL: strings.TrimRight(config.BaseURL, "/"), httpClient: httpClient}, nil
}

func invalidAuthIdentifier(value string) bool {
	return value == "" || value != strings.TrimSpace(value)
}

func (c *Client) CloseIdleConnections() { c.httpClient.CloseIdleConnections() }

// Authenticate validates the configured username, password, role, and location
// using Fincloud's login contract without calling a source endpoint.
func (c *Client) Authenticate(ctx context.Context) error {
	_, err := c.ensureSession(ctx, nil)
	return err
}

func (c *Client) do(ctx context.Context, operation string, build func(sessionID string) (*http.Request, error)) (*http.Response, error) {
	current, err := c.ensureSession(ctx, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.send(ctx, operation, current.id, build)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	resp.Body.Close()

	current, err = c.ensureSession(ctx, &current.generation)
	if err != nil {
		return nil, err
	}
	resp, err = c.send(ctx, operation, current.id, build)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		defer resp.Body.Close()
		return nil, c.responseFailure(ErrorUnauthorized, operation, "Fincloud rejected the request after reauthentication", resp)
	}
	return resp, nil
}

func (c *Client) send(ctx context.Context, operation, sessionID string, build func(string) (*http.Request, error)) (*http.Response, error) {
	req, err := build(sessionID)
	if err != nil {
		return nil, &Error{Kind: ErrorUpstream, Operation: operation, Message: "could not construct Fincloud request", Cause: err,
			diagnostic: &DiagnosticPayload{FailureKind: "request_build"}}
	}
	started := time.Now()
	req = req.WithContext(context.WithValue(ctx, requestStartedKey{}, started))
	resp, err := c.httpClient.Do(req)
	if err != nil {
		cause := c.sanitizeTransportError(err)
		return nil, &Error{Kind: ErrorUpstream, Operation: operation, Message: "Fincloud request failed", Cause: cause,
			diagnostic: &DiagnosticPayload{FailureKind: "network", DurationMS: time.Since(started).Milliseconds(), Request: c.sanitizeRequest(req)}}
	}
	return resp, nil
}

func (c *Client) ensureSession(ctx context.Context, staleGeneration *uint64) (session, error) {
	for {
		c.mu.Lock()
		if c.session.id != "" && (staleGeneration == nil || c.session.generation != *staleGeneration) {
			current := c.session
			c.mu.Unlock()
			return current, nil
		}
		if inFlight := c.login; inFlight != nil {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return session{}, ctx.Err()
			case <-inFlight.done:
				if inFlight.err != nil {
					return session{}, inFlight.err
				}
				continue
			}
		}

		call := &loginCall{done: make(chan struct{})}
		c.login = call
		c.mu.Unlock()

		sessionID, err := c.loginRequest(ctx)
		c.mu.Lock()
		if err == nil {
			c.session.id = sessionID
			c.session.generation++
		}
		call.err = err
		c.login = nil
		close(call.done)
		current := c.session
		c.mu.Unlock()
		if err != nil {
			return session{}, err
		}
		return current, nil
	}
}

func (c *Client) loginRequest(ctx context.Context) (string, error) {
	form := url.Values{
		"locationid": {c.config.LocationID},
		"roleid":     {c.config.RoleID},
		"username":   {c.config.Username},
		"pwd":        {c.config.Password},
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/admin/access/login", strings.NewReader(form.Encode()), "")
	if err != nil {
		return "", &Error{Kind: ErrorAuthentication, Operation: "authenticate", Message: "could not construct login request", Cause: err}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	started := time.Now()
	req = req.WithContext(context.WithValue(req.Context(), requestStartedKey{}, started))
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", &Error{Kind: ErrorAuthentication, Operation: "authenticate", Message: "Fincloud login failed", Cause: c.sanitizeTransportError(err),
			diagnostic: &DiagnosticPayload{FailureKind: "network", DurationMS: time.Since(started).Milliseconds(), Request: c.sanitizeRequest(req)}}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", c.responseFailure(ErrorAuthentication, "authenticate", "Fincloud rejected the configured login", resp)
	}
	data, body, readErr := c.readResponseBody(resp, true, "authenticate")
	diagnostic := c.responseDiagnostic(resp, body)
	if readErr != nil {
		diagnostic.FailureKind = "body_read"
		return "", &Error{Kind: ErrorAuthentication, Operation: "authenticate", Message: "could not read Fincloud login response", Cause: readErr, diagnostic: diagnostic}
	}
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Result struct {
				SessionID string `json:"sessionid"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&payload); err != nil {
		diagnostic.FailureKind, diagnostic.DecodeStage = decodeFailureKind(err), "authentication_envelope"
		return "", &Error{Kind: ErrorMalformed, Operation: "authenticate", Message: "Fincloud login response was malformed", Cause: err, diagnostic: diagnostic}
	}
	if payload.Status != "ok" || strings.TrimSpace(payload.Data.Result.SessionID) == "" {
		diagnostic.FailureKind = "application"
		diagnostic.Application.Status = payload.Status
		return "", &Error{Kind: ErrorAuthentication, Operation: "authenticate", Message: "Fincloud rejected the configured login", diagnostic: diagnostic}
	}
	return payload.Data.Result.SessionID, nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader, sessionID string) (*http.Request, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	if sessionID != "" {
		req.Header.Set("sessionid", sessionID)
	}
	return req, nil
}

func (c *Client) sanitizeTransportError(err error) error {
	var urlError *url.Error
	if !errors.As(err, &urlError) {
		return err
	}
	redacted := *urlError
	if parsed, parseErr := url.Parse(redacted.URL); parseErr == nil {
		query := parsed.Query()
		for key := range query {
			normalized := strings.ToLower(key)
			if (strings.HasSuffix(parsed.Path, "/tabungan/inquiry/rekening/historyMutasi") && normalized == "id") || strings.Contains(normalized, "session") || strings.Contains(normalized, "token") || strings.Contains(normalized, "auth") || strings.Contains(normalized, "password") {
				query.Set(key, "REDACTED")
			} else {
				for index, value := range query[key] {
					query[key][index] = c.redactSecretLiterals(value)
				}
			}
		}
		parsed.RawQuery = query.Encode()
		parsed.User = nil
		redacted.URL = parsed.String()
	}
	return &redacted
}

func (c *Client) responseFailure(kind ErrorKind, operation, message string, response *http.Response) error {
	_, body, readErr := c.readResponseBody(response, false, operation)
	diagnostic := c.responseDiagnostic(response, body)
	diagnostic.Application = applicationFromBody(body)
	diagnostic.FailureKind = "http"
	if readErr != nil {
		diagnostic.FailureKind = "body_read"
	}
	return &Error{Kind: kind, Operation: operation, Message: message, HTTPStatus: response.StatusCode, Cause: readErr, diagnostic: diagnostic}
}

func (c *Client) upstreamResponseFailure(operation string, response *http.Response) error {
	return c.responseFailure(ErrorUpstream, operation, fmt.Sprintf("Fincloud returned HTTP %d", response.StatusCode), response)
}

// responseError remains the compact status-only constructor for callers/tests
// without an HTTP response body. Runtime paths use upstreamResponseFailure.
func responseError(operation string, status int) error {
	return &Error{Kind: ErrorUpstream, Operation: operation, Message: fmt.Sprintf("Fincloud returned HTTP %d", status), HTTPStatus: status}
}

func (c *Client) sanitizeRequest(request *http.Request) RequestDiagnostic {
	result := RequestDiagnostic{Method: request.Method, Path: request.URL.Path, Query: map[string][]string{}, Headers: map[string][]string{}}
	for key, values := range request.URL.Query() {
		copy := append([]string(nil), values...)
		if (strings.HasSuffix(request.URL.Path, "/tabungan/inquiry/rekening/historyMutasi") && strings.EqualFold(key, "id")) || secretKey(key) {
			for index := range copy {
				copy[index] = "REDACTED"
			}
		} else {
			for index := range copy {
				copy[index] = c.redactSecretLiterals(copy[index])
			}
		}
		result.Query[key] = copy
	}
	for _, key := range []string{"Content-Type", "Accept", "User-Agent"} {
		if values := request.Header.Values(key); len(values) > 0 {
			result.Headers[key] = append([]string(nil), values...)
		}
	}
	return result
}

func (c *Client) redactSecretLiterals(value string) string {
	c.mu.Lock()
	sessionID := c.session.id
	c.mu.Unlock()
	for _, secret := range []string{c.config.Username, c.config.Password, sessionID} {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "REDACTED")
		}
	}
	return value
}

func secretKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", ""), "_", ""))
	for _, candidate := range []string{"authorization", "cookie", "session", "token", "password", "pwd", "secret", "apikey"} {
		if strings.Contains(key, candidate) {
			return true
		}
	}
	return false
}

func (c *Client) responseDiagnostic(response *http.Response, body BodyDiagnostic) *DiagnosticPayload {
	duration := int64(0)
	request := RequestDiagnostic{}
	if response.Request != nil {
		request = c.sanitizeRequest(response.Request)
		if started, ok := response.Request.Context().Value(requestStartedKey{}).(time.Time); ok {
			duration = time.Since(started).Milliseconds()
		}
	}
	headers := map[string][]string{}
	for _, key := range []string{"Content-Type", "Content-Length", "Content-Encoding", "Retry-After", "X-Request-ID", "X-Correlation-ID", "Request-ID", "Correlation-ID"} {
		if values := response.Header.Values(key); len(values) > 0 {
			headers[key] = append([]string(nil), values...)
		}
	}
	return &DiagnosticPayload{Request: request, Response: &ResponseDiagnostic{StatusCode: response.StatusCode,
		Status: response.Status, ContentType: response.Header.Get("Content-Type"), ContentEncoding: response.Header.Get("Content-Encoding"),
		DurationMS: duration, Headers: headers, Body: body}}
}

func (c *Client) readResponseBody(response *http.Response, keepAll bool, operation string) ([]byte, BodyDiagnostic, error) {
	body := BodyDiagnostic{CaptureLimit: MaxErrorBodyCapture}
	if response.ContentLength >= 0 {
		declared := response.ContentLength
		body.DeclaredContentLength = &declared
	}
	var all, captured bytes.Buffer
	buffer := make([]byte, 32<<10)
	var readErr error
	for {
		count, err := response.Body.Read(buffer)
		if count > 0 {
			body.BytesRead += int64(count)
			if keepAll {
				_, _ = all.Write(buffer[:count])
			}
			remaining := MaxErrorBodyCapture - captured.Len()
			if remaining > 0 {
				_, _ = captured.Write(buffer[:min(count, remaining)])
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				readErr = err
			}
			break
		}
	}
	body.BytesCaptured = captured.Len()
	body.Truncated = body.BytesRead > int64(MaxErrorBodyCapture)
	if readErr != nil {
		safe, _ := c.sanitizeBody(operation, []byte(readErr.Error()))
		body.ReadError = string(safe)
	}
	sanitized, redacted := c.sanitizeBody(operation, captured.Bytes())
	body.Redacted = redacted
	if utf8.Valid(sanitized) {
		body.Encoding, body.Body = "utf8", string(sanitized)
	} else {
		body.Encoding, body.Body = "base64", base64.StdEncoding.EncodeToString(sanitized)
	}
	if keepAll {
		return all.Bytes(), body, readErr
	}
	return nil, body, readErr
}

func (c *Client) sanitizeBody(operation string, value []byte) ([]byte, bool) {
	if operation == "authenticate" {
		return []byte("[REDACTED authentication response]"), true
	}
	if operation == "fetch saving account statement" {
		return []byte("[REDACTED saving account statement response]"), true
	}
	result := append([]byte(nil), value...)
	redacted := false
	c.mu.Lock()
	sessionID := c.session.id
	c.mu.Unlock()
	for _, secret := range []string{c.config.Username, c.config.Password, sessionID} {
		if secret != "" && bytes.Contains(result, []byte(secret)) {
			result = bytes.ReplaceAll(result, []byte(secret), []byte("REDACTED"))
			redacted = true
		}
	}
	replacedBytes := secretTextPattern.ReplaceAll(result, []byte("$1$2REDACTED"))
	redacted = redacted || !bytes.Equal(replacedBytes, result)
	result = replacedBytes
	if utf8.Valid(result) {
		var decoded any
		if json.Unmarshal(result, &decoded) == nil {
			if redactJSON(decoded) {
				redacted = true
				result, _ = json.Marshal(decoded)
			}
		}
	}
	return result, redacted
}

func redactJSON(value any) bool {
	redacted := false
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			if secretKey(key) {
				typed[key], redacted = "REDACTED", true
			} else if redactJSON(nested) {
				redacted = true
			}
		}
	case []any:
		for _, nested := range typed {
			if redactJSON(nested) {
				redacted = true
			}
		}
	}
	return redacted
}

func decodeFailureKind(err error) string {
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &typeError) {
		return "dto_decode"
	}
	return "malformed_json"
}

func SafeCauseClass(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "context_canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return "unexpected_eof"
	}
	if errors.Is(err, io.EOF) {
		return "eof"
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return "network_timeout"
	}
	var urlError *url.Error
	if errors.As(err, &urlError) {
		return "url_error"
	}
	if networkError != nil {
		return "network_error"
	}
	var sourceError *Error
	if errors.As(err, &sourceError) && sourceError.Cause != nil {
		if sourceError.Operation == "download report" || sourceError.Operation == "download maintenance report" {
			return "response_body_read_error"
		}
		return "fincloud_error"
	}
	return ""
}
