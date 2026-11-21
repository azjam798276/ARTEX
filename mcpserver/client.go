package mcpserver

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxResponseBytes bounds one API response. Findings and asset pages can be
// large, but a bound keeps a misbehaving proxy from streaming forever.
const maxResponseBytes = 16 << 20

// defaultTimeout is the per-request ceiling. Exploration endpoints answer from
// PostgreSQL and are normally fast; a planner round is not on this path.
const defaultTimeout = 60 * time.Second

// APIError is an upstream failure carrying the status ARTEX returned, so a tool
// can tell "task not found" apart from "the token expired".
type APIError struct {
	Status int
	Method string
	Path   string
	Body   string
}

func (e *APIError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("%s %s: HTTP %d", e.Method, e.Path, e.Status)
	}
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Body)
}

// IsNotFound reports whether the upstream answered 404.
func (e *APIError) IsNotFound() bool { return e.Status == http.StatusNotFound }

// IsUnauthorized reports whether the token was rejected. The tool layer turns
// this into an actionable message instead of a bare 401, because the usual
// cause is an expired 7-day token rather than a wrong one.
func (e *APIError) IsUnauthorized() bool { return e.Status == http.StatusUnauthorized }

// Client talks to one ARTEX instance's HTTP API as a bearer-token caller.
//
// A bearer token (rather than the console cookie) is deliberate: requireAuth
// refuses state-changing cookie requests from a foreign origin, and this server
// has no browser origin to speak of.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewClient builds a client for baseURL. An explicit token is required: the
// facade never borrows the server's JWT signing key, so a fork operator can
// point it at a remote instance with a token minted by that instance.
func NewClient(baseURL, token string, insecureSkipVerify bool) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, errors.New("artex API URL is required")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse API URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("API URL must be http(s), got %q", u.Scheme)
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("an API token is required (create one with `artex mcp-token`, or copy it from the console)")
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	if insecureSkipVerify {
		// Loopback self-signed deployments only; never defaulted on.
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &Client{
		BaseURL: baseURL,
		Token:   strings.TrimSpace(token),
		HTTP:    &http.Client{Transport: tr, Timeout: defaultTimeout},
	}, nil
}

// Get performs a GET and decodes the JSON body into out.
func (c *Client) Get(ctx context.Context, path string, query url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, query, nil, out)
}

// Post performs a POST with a JSON body and decodes the response into out.
func (c *Client) Post(ctx context.Context, path string, query url.Values, body, out any) error {
	return c.do(ctx, http.MethodPost, path, query, body, out)
}

// Do runs a request and returns the raw body, for endpoints whose shape this
// package deliberately does not model (export, coverage graph).
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body any) ([]byte, error) {
	var raw []byte
	err := c.do(ctx, method, path, query, body, &rawBody{into: &raw})
	return raw, err
}

// rawBody lets do() hand back undecoded bytes without a second code path.
type rawBody struct{ into *[]byte }

func (r *rawBody) UnmarshalJSON(b []byte) error {
	*r.into = append([]byte(nil), b...)
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	u := c.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request body: %w", err)
		}
		payload = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, payload)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, maxResponseBytes)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("%s %s: read response: %w", method, path, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &APIError{Status: resp.StatusCode, Method: method, Path: path, Body: errorBody(raw)}
	}
	if out == nil {
		return nil
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: decode response: %w", method, path, err)
	}
	return nil
}

// errorBody renders the upstream error body for a message. ARTEX answers JSON
// such as {"error":"task not found"}; a non-JSON body (a proxy page) is passed
// through bounded so the operator still sees something actionable.
func errorBody(raw []byte) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error != "" {
		return e.Error
	}
	const max = 512
	s := string(raw)
	if len(s) > max {
		s = s[:max] + "…"
	}
	return strings.TrimSpace(s)
}
