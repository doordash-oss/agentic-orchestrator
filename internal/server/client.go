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
}

type Client struct {
	baseURL string
	client  *http.Client
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
	Cursor int
	Limit  int
}

type TextQuery struct {
	Offset int64
	Limit  int64
}

type OperationQuery struct {
	State     OperationStatus
	FeatureID string
	Kind      string
	Cursor    string
	Limit     int
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
	return &Client{baseURL: baseURL, client: httpClient}, nil
}

func clientTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return DefaultClientTimeout
	}
	return timeout
}

func (c *Client) Health(ctx context.Context) (HealthResponse, error) {
	var out HealthResponse
	err := c.getJSON(ctx, "/api/v1/health", nil, &out)
	return out, err
}

func (c *Client) Features(ctx context.Context) (FeatureListResponse, error) {
	var out FeatureListResponse
	err := c.getJSON(ctx, "/api/v1/features", nil, &out)
	return out, err
}

func (c *Client) FeatureDetail(ctx context.Context, featureID string) (FeatureDetailResponse, error) {
	var out FeatureDetailResponse
	err := c.getJSON(ctx, "/api/v1/features/"+pathSegment(featureID), nil, &out)
	return out, err
}

func (c *Client) RuntimeConfig(ctx context.Context) (RuntimeConfigResponse, error) {
	var out RuntimeConfigResponse
	err := c.getJSON(ctx, "/api/v1/config/runtime", nil, &out)
	return out, err
}

func (c *Client) FeatureConfig(ctx context.Context, featureID string) (FeatureConfigResponse, error) {
	var out FeatureConfigResponse
	err := c.getJSON(ctx, "/api/v1/features/"+pathSegment(featureID)+"/config", nil, &out)
	return out, err
}

func (c *Client) ModelCatalog(ctx context.Context) (ModelCatalogResponse, error) {
	var out ModelCatalogResponse
	err := c.getJSON(ctx, "/api/v1/catalog/models", nil, &out)
	return out, err
}

func (c *Client) Prompts(ctx context.Context) (PromptSnapshotResponse, error) {
	var out PromptSnapshotResponse
	err := c.getJSON(ctx, "/api/v1/prompts", nil, &out)
	return out, err
}

func (c *Client) Permissions(ctx context.Context) (PermissionSnapshotResponse, error) {
	var out PermissionSnapshotResponse
	err := c.getJSON(ctx, "/api/v1/permissions", nil, &out)
	return out, err
}

func (c *Client) Sessions(ctx context.Context) (SessionListResponse, error) {
	var out SessionListResponse
	err := c.getJSON(ctx, "/api/v1/sessions", nil, &out)
	return out, err
}

func (c *Client) SessionDetail(ctx context.Context, sessionID string) (SessionDetailResponse, error) {
	var out SessionDetailResponse
	err := c.getJSON(ctx, "/api/v1/sessions/"+pathSegment(sessionID), nil, &out)
	return out, err
}

func (c *Client) Transcript(ctx context.Context, sessionID string, query CursorQuery) (TranscriptResponse, error) {
	var out TranscriptResponse
	err := c.getJSON(ctx, "/api/v1/sessions/"+pathSegment(sessionID)+"/transcript", transcriptValues(query), &out)
	return out, err
}

func (c *Client) ArtifactList(ctx context.Context, featureID string, runNumber int) (ArtifactListResponse, error) {
	var out ArtifactListResponse
	err := c.getJSON(ctx, runContentPath(featureID, runNumber, "artifacts"), nil, &out)
	return out, err
}

func (c *Client) ArtifactContent(ctx context.Context, featureID string, runNumber int, artifactID string, query TextQuery) (TextContentResponse, error) {
	var out TextContentResponse
	err := c.getJSON(ctx, runContentPath(featureID, runNumber, "artifacts")+"/"+pathSegment(artifactID), textValues(query), &out)
	return out, err
}

func (c *Client) LogContent(ctx context.Context, featureID string, runNumber int, logID string, query TextQuery) (TextContentResponse, error) {
	var out TextContentResponse
	err := c.getJSON(ctx, runContentPath(featureID, runNumber, "logs")+"/"+pathSegment(logID), textValues(query), &out)
	return out, err
}

func (c *Client) LivePreview(ctx context.Context, featureID string) (LivePreviewResponse, error) {
	var out LivePreviewResponse
	err := c.getJSON(ctx, "/api/v1/features/"+pathSegment(featureID)+"/live-preview", nil, &out)
	return out, err
}

func (c *Client) Operations(ctx context.Context, query OperationQuery) (OperationSnapshotResponse, error) {
	var out OperationSnapshotResponse
	err := c.getJSON(ctx, "/api/v1/operations", operationValues(query), &out)
	return out, err
}

func (c *Client) Recovery(ctx context.Context) (RecoverySnapshotResponse, error) {
	var out RecoverySnapshotResponse
	err := c.getJSON(ctx, "/api/v1/recovery", nil, &out)
	return out, err
}

func (c *Client) CreateFeature(ctx context.Context, req CreateFeatureRequest) (OperationAcceptedResponse, error) {
	return c.postMutation(ctx, "/api/v1/features", req)
}

func (c *Client) StartFeature(ctx context.Context, featureID string) (OperationAcceptedResponse, error) {
	return c.postMutation(ctx, "/api/v1/features/"+pathSegment(featureID)+"/start", map[string]any{})
}

func (c *Client) ResumeFeature(ctx context.Context, featureID string) (OperationAcceptedResponse, error) {
	return c.postFeatureAction(ctx, featureID, "resume", map[string]any{})
}

func (c *Client) StopFeature(ctx context.Context, featureID string) (OperationAcceptedResponse, error) {
	return c.postMutation(ctx, "/api/v1/features/"+pathSegment(featureID)+"/stop", map[string]any{})
}

func (c *Client) InterruptFeature(ctx context.Context, featureID string) (OperationAcceptedResponse, error) {
	return c.postMutation(ctx, "/api/v1/features/"+pathSegment(featureID)+"/interrupt", map[string]any{})
}

func (c *Client) RestartFeature(ctx context.Context, featureID string, req RestartFeatureRequest) (OperationAcceptedResponse, error) {
	return c.postMutation(ctx, "/api/v1/features/"+pathSegment(featureID)+"/restart", req)
}

func (c *Client) ReviewDecision(ctx context.Context, featureID string, req ReviewDecisionRequest) (OperationAcceptedResponse, error) {
	return c.postMutation(ctx, "/api/v1/features/"+pathSegment(featureID)+"/review-decision", req)
}

func (c *Client) UpdateFeatureConfig(ctx context.Context, featureID string, req FeatureConfigMutationRequest) (OperationAcceptedResponse, error) {
	return c.postMutation(ctx, "/api/v1/features/"+pathSegment(featureID)+"/config", req)
}

func (c *Client) NeedUserInputDecision(ctx context.Context, featureID string, req NeedUserInputDecisionRequest) (OperationAcceptedResponse, error) {
	return c.postMutation(ctx, "/api/v1/features/"+pathSegment(featureID)+"/need-user-input", req)
}

func (c *Client) DraftNeedUserInputAnswers(ctx context.Context, featureID string, req NeedUserInputDraftRequest) (OperationAcceptedResponse, error) {
	return c.postMutation(ctx, "/api/v1/features/"+pathSegment(featureID)+"/need-user-input-draft", req)
}

func (c *Client) AnswerPermission(ctx context.Context, req PermissionAnswerRequest) (OperationAcceptedResponse, error) {
	return c.postMutation(ctx, "/api/v1/permissions/answer", req)
}

func (c *Client) AnswerAskUser(ctx context.Context, req AskUserAnswerRequest) (OperationAcceptedResponse, error) {
	return c.postMutation(ctx, "/api/v1/prompts/ask-user/answer", req)
}

func (c *Client) SendHelp(ctx context.Context, req HelpAnswerRequest) (OperationAcceptedResponse, error) {
	return c.postMutation(ctx, "/api/v1/prompts/help/send", req)
}

func (c *Client) UpdateRuntimeConfig(ctx context.Context, req RuntimeConfigMutationRequest) (OperationAcceptedResponse, error) {
	var out OperationAcceptedResponse
	err := c.doJSON(ctx, http.MethodPatch, "/api/v1/config/runtime", nil, req, &out, true)
	return out, err
}

func (c *Client) PublishFeature(ctx context.Context, featureID string, req PublishFeatureRequest) (OperationAcceptedResponse, error) {
	return c.postFeatureAction(ctx, featureID, "publish", req)
}

func (c *Client) MergeFeature(ctx context.Context, featureID string) (OperationAcceptedResponse, error) {
	return c.postFeatureAction(ctx, featureID, "merge", map[string]any{})
}

func (c *Client) RewindFeature(ctx context.Context, featureID string, req RewindFeatureRequest) (OperationAcceptedResponse, error) {
	return c.postFeatureAction(ctx, featureID, "rewind", req)
}

func (c *Client) RetryFeature(ctx context.Context, featureID string) (OperationAcceptedResponse, error) {
	return c.postFeatureAction(ctx, featureID, "retry", map[string]any{})
}

func (c *Client) StartRebase(ctx context.Context, featureID string, req RebaseActionRequest) (OperationAcceptedResponse, error) {
	return c.postFeatureAction(ctx, featureID, "rebase", req)
}

func (c *Client) FetchReviewComments(ctx context.Context, featureID string, req ReviewCommentsFetchRequest) (ReviewCommentsFetchResponse, error) {
	var out ReviewCommentsFetchResponse
	err := c.doJSON(ctx, http.MethodPost, featureActionPath(featureID, "review-comments")+"/fetch", nil, req, &out, true)
	return out, err
}

func (c *Client) StartReviewComments(ctx context.Context, featureID string, req ReviewCommentsActionRequest) (OperationAcceptedResponse, error) {
	return c.postFeatureAction(ctx, featureID, "review-comments", req)
}

func (c *Client) StartTweak(ctx context.Context, featureID string, req TweakActionRequest) (OperationAcceptedResponse, error) {
	return c.postFeatureAction(ctx, featureID, "tweak", req)
}

func (c *Client) FinishTweak(ctx context.Context, featureID string, req TweakFinishRequest) (OperationAcceptedResponse, error) {
	var out OperationAcceptedResponse
	err := c.doJSON(ctx, http.MethodPost, featureActionPath(featureID, "tweak")+"/finish", nil, req, &out, true)
	return out, err
}

func (c *Client) StartRefactor(ctx context.Context, featureID string, req RefactorActionRequest) (OperationAcceptedResponse, error) {
	return c.postFeatureAction(ctx, featureID, "refactor", req)
}

func (c *Client) RestartRefactor(ctx context.Context, featureID string, req RefactorActionRequest) (OperationAcceptedResponse, error) {
	var out OperationAcceptedResponse
	err := c.doJSON(ctx, http.MethodPost, featureActionPath(featureID, "refactor")+"/restart", nil, req, &out, true)
	return out, err
}

func (c *Client) MarkDone(ctx context.Context, featureID string) (OperationAcceptedResponse, error) {
	return c.postFeatureAction(ctx, featureID, "mark-done", map[string]any{})
}

func (c *Client) CleanupFeature(ctx context.Context, featureID string, req CleanupActionRequest) (OperationAcceptedResponse, error) {
	return c.postFeatureAction(ctx, featureID, "cleanup", req)
}

func (c *Client) DeleteFeature(ctx context.Context, featureID string) (OperationAcceptedResponse, error) {
	return c.postFeatureAction(ctx, featureID, "delete", map[string]any{})
}

func (c *Client) ExecuteRecovery(ctx context.Context, req RecoveryActionRequest) (OperationAcceptedResponse, error) {
	return c.postMutation(ctx, "/api/v1/recovery/actions", req)
}

func (c *Client) postFeatureAction(ctx context.Context, featureID, action string, in any) (OperationAcceptedResponse, error) {
	return c.postMutation(ctx, featureActionPath(featureID, action), in)
}

func (c *Client) postMutation(ctx context.Context, path string, in any) (OperationAcceptedResponse, error) {
	var out OperationAcceptedResponse
	err := c.doJSON(ctx, http.MethodPost, path, nil, in, &out, true)
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
	if trusted {
		req.Header.Set("X-Agentico-Client", "local")
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

func textValues(query TextQuery) url.Values {
	values := url.Values{}
	if query.Offset > 0 {
		values.Set("offset", strconv.FormatInt(query.Offset, 10))
	}
	if query.Limit > 0 {
		values.Set("limit", strconv.FormatInt(query.Limit, 10))
	}
	return values
}

func operationValues(query OperationQuery) url.Values {
	values := url.Values{}
	if query.State != "" {
		values.Set("state", string(query.State))
	}
	if query.FeatureID != "" {
		values.Set("feature_id", query.FeatureID)
	}
	if query.Kind != "" {
		values.Set("kind", query.Kind)
	}
	if query.Cursor != "" {
		values.Set("cursor", query.Cursor)
	}
	if query.Limit > 0 {
		values.Set("limit", strconv.Itoa(query.Limit))
	}
	return values
}

func runContentPath(featureID string, runNumber int, kind string) string {
	return "/api/v1/features/" + pathSegment(featureID) + "/runs/" + strconv.Itoa(runNumber) + "/" + kind
}

func featureActionPath(featureID, action string) string {
	return "/api/v1/features/" + pathSegment(featureID) + "/actions/" + pathSegment(action)
}

func pathSegment(s string) string {
	return url.PathEscape(s)
}
