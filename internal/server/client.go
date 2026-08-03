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

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const DefaultClientTimeout = 10 * time.Second

type ClientOptions struct {
	BaseURL    string
	HTTPClient *http.Client
	Timeout    time.Duration
	Token      string
}

type Client struct {
	baseURL string
	client  *http.Client
	token   string
}

type APIError struct {
	Status  int
	Code    string
	Message string
	Target  map[string]any
	Method  string
	Path    string
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	code := e.Code
	if code == "" {
		code = strings.ToLower(strings.ReplaceAll(http.StatusText(e.Status), " ", "_"))
	}
	message := e.Message
	if message == "" {
		message = http.StatusText(e.Status)
	}
	return fmt.Sprintf("api %s %s: %s (%d): %s", e.Method, e.Path, code, e.Status, message)
}

type CursorQuery struct {
	Cursor int `json:"cursor,omitempty"`
	Limit  int `json:"limit,omitempty"`
}

func NewClient(opts ClientOptions) (*Client, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if baseURL == "" {
		return nil, errors.New("base URL is required")
	}
	if !isLoopbackBaseURL(baseURL) {
		return nil, fmt.Errorf("base URL must be loopback HTTP: %s", baseURL)
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: clientTimeout(opts.Timeout)}
	} else if httpClient.Timeout == 0 {
		copyClient := *httpClient
		copyClient.Timeout = clientTimeout(opts.Timeout)
		httpClient = &copyClient
	}
	return &Client{
		baseURL: baseURL,
		client:  httpClient,
		token:   strings.TrimSpace(opts.Token),
	}, nil
}

func clientTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return DefaultClientTimeout
	}
	return timeout
}

func (c *Client) Transcript(ctx context.Context, sessionID string, query CursorQuery) (TranscriptResponse, error) {
	var out TranscriptResponse
	err := c.getJSON(ctx, "/api/v1/sessions/"+pathSegment(sessionID)+"/transcript", transcriptValues(query), &out)
	return out, err
}

func (c *Client) LivePreview(ctx context.Context, featureID string) (LivePreviewResponse, error) {
	var out LivePreviewResponse
	err := c.getJSON(ctx, "/api/v1/features/"+pathSegment(featureID)+"/live-preview", nil, &out)
	return out, err
}

func (c *Client) UpdateFeatureConfig(ctx context.Context, featureID string, req FeatureConfigMutationRequest) (FeatureConfigUpdateResponse, error) {
	var out FeatureConfigUpdateResponse
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/features/"+pathSegment(featureID)+"/config", nil, req, &out, true)
	return out, err
}

func (c *Client) RefactorFeature(ctx context.Context, featureID string, req RefactorFeatureRequest) (RefactorFeatureResponse, error) {
	var out RefactorFeatureResponse
	err := c.doJSON(ctx, http.MethodPost, featureActionPath(featureID, actionRefactor), nil, req, &out, true)
	return out, err
}

func (c *Client) ReviewFeedbackFeature(ctx context.Context, featureID string, req ReviewFeedbackFeatureRequest) (ReviewFeedbackFeatureResponse, error) {
	var out ReviewFeedbackFeatureResponse
	err := c.doJSON(ctx, http.MethodPost, featureActionPath(featureID, actionReviewFeedback), nil, req, &out, true)
	return out, err
}

func (c *Client) FetchReviewFeedback(ctx context.Context, featureID string) (ReviewFeedbackFetchResponse, error) {
	var out ReviewFeedbackFetchResponse
	err := c.doJSON(ctx, http.MethodPost, reviewFeedbackFetchPath(featureID), nil, ReviewFeedbackFetchRequest{}, &out, true)
	return out, err
}

func (c *Client) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	return c.doJSON(ctx, http.MethodGet, path, query, nil, out, false)
}

func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, in, out any, trusted bool) error {
	var body *bytes.Reader
	if in == nil {
		body = bytes.NewReader(nil)
	} else {
		data, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint(path, query), body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if trusted {
		req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeAPIError(resp, method, path)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func decodeAPIError(resp *http.Response, method, path string) error {
	apiErr := &APIError{Status: resp.StatusCode, Method: method, Path: path}
	var errResp ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err == nil {
		apiErr.Code = errResp.Error.Code
		apiErr.Message = errResp.Error.Message
		apiErr.Target = errResp.Error.Target
	}
	if apiErr.Message == "" {
		apiErr.Message = http.StatusText(resp.StatusCode)
	}
	return apiErr
}

func (c *Client) endpoint(path string, query url.Values) string {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return u
}

func transcriptValues(query CursorQuery) url.Values {
	values := url.Values{}
	if query.Cursor > 0 {
		values.Set("offset", strconv.Itoa(query.Cursor))
	}
	if query.Limit > 0 {
		values.Set("limit", strconv.Itoa(query.Limit))
	}
	return values
}

func featureActionPath(featureID, action string) string {
	return "/api/v1/features/" + pathSegment(featureID) + "/actions/" + pathSegment(action)
}

func pathSegment(s string) string {
	return url.PathEscape(s)
}
