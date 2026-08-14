package fincloud

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:142.0) Gecko/20100101 Firefox/142.0"

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
}

func (e *Error) Error() string {
	if e.Operation == "" {
		return e.Message
	}
	return e.Operation + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.Cause }

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
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("unsupported default HTTP transport")
	}
	transport = transport.Clone()
	if config.InsecureSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit operator opt-in
	}
	return newClient(config, &http.Client{Timeout: config.HTTPTimeout, Transport: transport})
}

func newClient(config Config, httpClient *http.Client) (*Client, error) {
	if strings.TrimSpace(config.BaseURL) == "" || strings.TrimSpace(config.Username) == "" || strings.TrimSpace(config.Password) == "" || strings.TrimSpace(config.LocationID) == "" || strings.TrimSpace(config.RoleID) == "" {
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

func (c *Client) CloseIdleConnections() { c.httpClient.CloseIdleConnections() }

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
		resp.Body.Close()
		return nil, &Error{Kind: ErrorUnauthorized, Operation: operation, Message: "Fincloud rejected the request after reauthentication", HTTPStatus: http.StatusUnauthorized}
	}
	return resp, nil
}

func (c *Client) send(ctx context.Context, operation, sessionID string, build func(string) (*http.Request, error)) (*http.Response, error) {
	req, err := build(sessionID)
	if err != nil {
		return nil, &Error{Kind: ErrorUpstream, Operation: operation, Message: "could not construct Fincloud request", Cause: err}
	}
	resp, err := c.httpClient.Do(req.WithContext(ctx))
	if err != nil {
		return nil, &Error{Kind: ErrorUpstream, Operation: operation, Message: "Fincloud request failed", Cause: sanitizeTransportError(err)}
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
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", &Error{Kind: ErrorAuthentication, Operation: "authenticate", Message: "Fincloud login failed", Cause: sanitizeTransportError(err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &Error{Kind: ErrorAuthentication, Operation: "authenticate", Message: "Fincloud rejected the configured login", HTTPStatus: resp.StatusCode}
	}
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Result struct {
				SessionID string `json:"sessionid"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", &Error{Kind: ErrorMalformed, Operation: "authenticate", Message: "Fincloud login response was malformed", Cause: err}
	}
	if payload.Status != "ok" || strings.TrimSpace(payload.Data.Result.SessionID) == "" {
		return "", &Error{Kind: ErrorAuthentication, Operation: "authenticate", Message: "Fincloud rejected the configured login"}
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

func sanitizeTransportError(err error) error {
	var urlError *url.Error
	if !errors.As(err, &urlError) {
		return err
	}
	redacted := *urlError
	if parsed, parseErr := url.Parse(redacted.URL); parseErr == nil {
		query := parsed.Query()
		for key := range query {
			normalized := strings.ToLower(key)
			if strings.Contains(normalized, "session") || strings.Contains(normalized, "token") || strings.Contains(normalized, "auth") || strings.Contains(normalized, "password") {
				query.Set(key, "REDACTED")
			}
		}
		parsed.RawQuery = query.Encode()
		parsed.User = nil
		redacted.URL = parsed.String()
	}
	return &redacted
}

func responseError(operation string, status int) error {
	return &Error{Kind: ErrorUpstream, Operation: operation, Message: fmt.Sprintf("Fincloud returned HTTP %d", status), HTTPStatus: status}
}
