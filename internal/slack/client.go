// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAPIBase = "https://slack.com/api/"
	defaultTimeout = 10 * time.Second
	userAgent      = "Agentico/1 Slack"

	// EnvSlackAPIBase overrides the Slack Web API base URL.
	EnvSlackAPIBase = "AGENTICO_SLACK_API_BASE"
)

// APIError is a successful HTTP response whose Slack envelope was not ok.
type APIError struct {
	SlackError string
}

func (e *APIError) Error() string {
	return "Slack API error: " + e.SlackError
}

// TransportError reports an HTTP, decoding, connection, or timeout failure.
type TransportError struct {
	StatusCode int
	RetryAfter time.Duration
	Detail     string
}

func (e *TransportError) Error() string {
	switch {
	case e.StatusCode != 0 && e.RetryAfter > 0:
		return fmt.Sprintf("Slack transport error: HTTP %d, retry after %s", e.StatusCode, e.RetryAfter)
	case e.StatusCode != 0 && e.Detail != "":
		return fmt.Sprintf("Slack transport error: HTTP %d: %s", e.StatusCode, e.Detail)
	case e.StatusCode != 0:
		return fmt.Sprintf("Slack transport error: HTTP %d", e.StatusCode)
	case e.Detail != "":
		return "Slack transport error: " + e.Detail
	default:
		return "Slack transport error"
	}
}

// AuthTestResponse is the typed result of Slack's auth.test method.
type AuthTestResponse struct {
	TeamID        string
	TeamName      string
	UserName      string
	UserID        string
	WorkspaceURL  string
	BotID         string
	GrantedScopes []string
}

// ClientOption customizes a Slack client.
type ClientOption func(*clientConfig)

type clientConfig struct {
	baseURL    string
	timeout    time.Duration
	httpClient *http.Client
}

// WithBaseURL overrides the Slack Web API base URL.
func WithBaseURL(baseURL string) ClientOption {
	return func(cfg *clientConfig) {
		cfg.baseURL = baseURL
	}
}

// WithTimeout overrides the per-request context timeout.
func WithTimeout(timeout time.Duration) ClientOption {
	return func(cfg *clientConfig) {
		cfg.timeout = timeout
	}
}

// WithHTTPClient overrides the HTTP client while retaining request timeouts.
func WithHTTPClient(client *http.Client) ClientOption {
	return func(cfg *clientConfig) {
		cfg.httpClient = client
	}
}

// Client is a token-bound Slack Web API client.
type Client struct {
	token      string
	baseURL    *url.URL
	timeout    time.Duration
	httpClient *http.Client
}

// NewClient constructs a client without making a network request.
func NewClient(token string, opts ...ClientOption) (*Client, error) {
	baseURL := os.Getenv(EnvSlackAPIBase)
	if baseURL == "" {
		baseURL = defaultAPIBase
	}
	cfg := clientConfig{
		baseURL:    baseURL,
		timeout:    defaultTimeout,
		httpClient: &http.Client{},
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.timeout <= 0 {
		return nil, errors.New("Slack request timeout must be positive")
	}
	if cfg.httpClient == nil {
		return nil, errors.New("Slack HTTP client must not be nil")
	}
	base, err := url.Parse(cfg.baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("invalid Slack API base URL %q", scrub(token, cfg.baseURL))
	}
	if !strings.HasSuffix(base.Path, "/") {
		base.Path += "/"
	}
	return &Client{
		token:      token,
		baseURL:    base,
		timeout:    cfg.timeout,
		httpClient: cfg.httpClient,
	}, nil
}

// AuthTest validates the client's token and returns its workspace identity.
func (c *Client) AuthTest(ctx context.Context) (AuthTestResponse, error) {
	var envelope struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		Team   string `json:"team"`
		TeamID string `json:"team_id"`
		User   string `json:"user"`
		UserID string `json:"user_id"`
		URL    string `json:"url"`
		BotID  string `json:"bot_id"`
	}
	scopes, err := c.call(ctx, "auth.test", &envelope)
	if err != nil {
		return AuthTestResponse{}, err
	}
	if !envelope.OK {
		return AuthTestResponse{}, &APIError{SlackError: scrub(c.token, envelope.Error)}
	}
	return AuthTestResponse{
		TeamID:        envelope.TeamID,
		TeamName:      envelope.Team,
		UserName:      envelope.User,
		UserID:        envelope.UserID,
		WorkspaceURL:  envelope.URL,
		BotID:         envelope.BotID,
		GrantedScopes: scopes,
	}, nil
}

func (c *Client) call(ctx context.Context, method string, target any) ([]string, error) {
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	endpoint := c.baseURL.ResolveReference(&url.URL{Path: method})
	req, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodPost,
		endpoint.String(),
		strings.NewReader(""),
	)
	if err != nil {
		return nil, c.transportError(0, 0, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, c.transportError(0, 0, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32<<10))
		return nil, c.transportError(resp.StatusCode, retryAfter(resp.Header), nil)
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	if err := decoder.Decode(target); err != nil {
		return nil, c.transportError(resp.StatusCode, 0, fmt.Errorf("decoding JSON response: %v", err))
	}
	return parseScopes(resp.Header.Get("X-OAuth-Scopes")), nil
}

func (c *Client) transportError(status int, retry time.Duration, err error) *TransportError {
	detail := ""
	if err != nil {
		detail = scrub(c.token, err.Error())
	}
	return &TransportError{StatusCode: status, RetryAfter: retry, Detail: detail}
}

func retryAfter(header http.Header) time.Duration {
	raw := strings.TrimSpace(header.Get("Retry-After"))
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func parseScopes(header string) []string {
	set := make(map[string]struct{})
	for _, raw := range strings.Split(header, ",") {
		if scope := strings.TrimSpace(raw); scope != "" {
			set[scope] = struct{}{}
		}
	}
	scopes := make([]string, 0, len(set))
	for scope := range set {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	return scopes
}

func scrub(token, text string) string {
	if token == "" {
		return text
	}
	return strings.ReplaceAll(text, token, "[REDACTED]")
}
