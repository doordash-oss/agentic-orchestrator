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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
)

func TestRunArgsRoutesExplicitJSONCommandsWithoutLaunchingTUI(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var launchedTUI bool
	var launchedServer bool
	code := runArgs(
		[]string{"feature", "detail", "feat-1", "--json", "--server", "http://127.0.0.1:7788"},
		&stdout,
		&stderr,
		func(string, string, bool, []string, bool) int {
			launchedTUI = true
			return 0
		},
		func(string, string, bool, []string, bool) int {
			launchedServer = true
			return 0
		},
		failingUpdater(t),
	)
	if code != 1 {
		t.Fatalf("runArgs(feature detail --json) code = %d, want fake transport failure 1", code)
	}
	if launchedTUI {
		t.Fatal("TUI launcher was invoked for explicit JSON command")
	}
	if launchedServer {
		t.Fatal("foreground server launcher was invoked for explicit JSON command")
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want JSON command errors on stdout", stderr.String())
	}
	if !strings.Contains(stdout.String(), `"code":"rest_transport_failure"`) {
		t.Fatalf("stdout = %q, want structured JSON transport error", stdout.String())
	}
}

func TestJSONCLICommandsUseVersionedEnvelope(t *testing.T) {
	ctx := context.Background()
	client := &fakeJSONCLIClient{
		detail: serverruntime.FeatureDetailResponse{
			APIVersion: serverruntime.APIVersion,
			Feature: serverruntime.FeatureDetailDTO{
				FeatureSummary: serverruntime.FeatureSummary{
					ID:           "feat-1",
					Name:         "JSON contract",
					Status:       "Created",
					CurrentPhase: "inquire",
				},
			},
		},
		action: serverruntime.FeatureStartResponse{
			FeatureID: "feat-1",
			Result:    "started",
		},
	}
	var out bytes.Buffer

	code := runJSONCommand(ctx, jsonCommandOptions{
		Args:   []string{"feature", "detail", "feat-1", "--json"},
		Stdout: &out,
		Stderr: ioDiscard{},
		Deps: jsonCommandDeps{
			Connect: func(context.Context, jsonCommandOptions) (jsonCLIClient, error) {
				return client, nil
			},
		},
	})
	if code != 0 {
		t.Fatalf("runJSONCommand(feature detail) code = %d, want 0", code)
	}
	got := decodeJSONEnvelope(t, out.Bytes())
	if got.SchemaVersion != 1 || got.APIVersion != serverruntime.APIVersion || !got.OK || got.Error != nil {
		t.Fatalf("envelope = %+v, want ok schema v1 api %s", got, serverruntime.APIVersion)
	}
	if got.Result["feature"].(map[string]any)["id"] != "feat-1" {
		t.Fatalf("result = %#v, want feature detail payload", got.Result)
	}

	out.Reset()
	code = runJSONCommand(ctx, jsonCommandOptions{
		Args:   []string{"feature", "action", "feat-1", "start", "--json"},
		Stdout: &out,
		Stderr: ioDiscard{},
		Deps: jsonCommandDeps{
			Connect: func(context.Context, jsonCommandOptions) (jsonCLIClient, error) {
				return client, nil
			},
		},
	})
	if code != 0 {
		t.Fatalf("runJSONCommand(feature action start) code = %d, want 0", code)
	}
	got = decodeJSONEnvelope(t, out.Bytes())
	if got.Result["feature_id"] != "feat-1" || got.Result["result"] != "started" {
		t.Fatalf("action result = %#v, want start response", got.Result)
	}
}

func TestJSONCLIServerEnsureReusesDefaultLaunchPath(t *testing.T) {
	runtimeDir := t.TempDir()
	stateDir := filepath.Join(runtimeDir, "features")
	configPath := filepath.Join(runtimeDir, "config.yaml")
	policy := serverruntime.NewLaunchPolicy([]string{"codex"}, true)
	var out bytes.Buffer
	var captured defaultLaunchRequest

	code := runJSONCommand(context.Background(), jsonCommandOptions{
		Args:       []string{"server", "ensure", "--json"},
		ConfigPath: configPath,
		StateDir:   stateDir,
		Stdout:     &out,
		Stderr:     ioDiscard{},
		Deps: jsonCommandDeps{
			EnsureServer: func(ctx context.Context, req defaultLaunchRequest) (jsonServerEnsureResult, error) {
				captured = req
				return jsonServerEnsureResult{
					BaseURL:      "http://127.0.0.1:7799",
					Runtime:      serverruntime.RuntimeIdentity{RuntimeDir: runtimeDir, StateDir: stateDir, Config: configPath},
					LaunchPolicy: policy,
					OwnedServer:  false,
					Status:       "attached",
				}, nil
			},
		},
	})
	if code != 0 {
		t.Fatalf("runJSONCommand(server ensure) code = %d, want 0", code)
	}
	if captured.ConfigPath != configPath || captured.StateDir != stateDir || captured.Stdin != nil {
		t.Fatalf("default launch request = %+v, want config/state propagated without TUI stdin dependency", captured)
	}
	got := decodeJSONEnvelope(t, out.Bytes())
	if got.Result["base_url"] != "http://127.0.0.1:7799" || got.Result["status"] != "attached" {
		t.Fatalf("server ensure result = %#v", got.Result)
	}
}

func TestJSONCLIErrorsAreStructured(t *testing.T) {
	var out bytes.Buffer
	client := &fakeJSONCLIClient{detailErr: &serverruntime.APIError{
		Status:  http.StatusNotFound,
		Code:    "not_found",
		Message: "feature not found",
		Target:  map[string]any{"feature_id": "missing"},
	}}
	code := runJSONCommand(context.Background(), jsonCommandOptions{
		Args:   []string{"feature", "detail", "missing", "--json"},
		Stdout: &out,
		Stderr: ioDiscard{},
		Deps: jsonCommandDeps{
			Connect: func(context.Context, jsonCommandOptions) (jsonCLIClient, error) {
				return client, nil
			},
		},
	})
	if code != 1 {
		t.Fatalf("runJSONCommand(missing detail) code = %d, want 1", code)
	}
	got := decodeJSONEnvelope(t, out.Bytes())
	if got.OK || got.Error == nil {
		t.Fatalf("envelope = %+v, want structured error", got)
	}
	if got.Error.Code != "not_found" || got.Error.Target["feature_id"] != "missing" {
		t.Fatalf("error = %+v, want API error metadata preserved", got.Error)
	}
}

func TestRunArgsReturnsStructuredJSONErrorForMalformedAutomationCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runArgs(
		[]string{"feature", "detail", "feat-1", "--json", "--bad-flag"},
		&stdout,
		&stderr,
		func(string, string, bool, []string, bool) int { return 0 },
		func(string, string, bool, []string, bool) int { return 0 },
		failingUpdater(t),
	)
	if code != 1 {
		t.Fatalf("runArgs(malformed JSON command) code = %d, want 1", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want JSON command errors on stdout", stderr.String())
	}
	got := decodeJSONEnvelope(t, stdout.Bytes())
	if got.OK || got.Error == nil || got.Error.Code != "invalid_input" || !strings.Contains(got.Error.Message, "--bad-flag") {
		t.Fatalf("envelope = %+v, want structured invalid_input for bad flag", got)
	}
}

func TestJSONCLICreateAnswerAndReviewForwardRequests(t *testing.T) {
	client := &fakeJSONCLIClient{
		create: serverruntime.CreateFeatureResponse{
			FeatureID: "feat-created",
			Result:    "created",
		},
		review: serverruntime.ReviewDecisionResponse{
			FeatureID: "feat-created",
			Decision:  "approve",
			Result:    "accepted",
		},
		permission: serverruntime.PermissionAnswerResponse{
			SessionID: "sess-1",
			RequestID: "perm-1",
			Decision:  "allow",
			Result:    "answered",
		},
	}
	connect := func(context.Context, jsonCommandOptions) (jsonCLIClient, error) {
		return client, nil
	}
	var out bytes.Buffer

	code := runJSONCommand(context.Background(), jsonCommandOptions{
		Args: []string{
			"feature", "create", "--json",
			"--input-json", `{"name":"CLI feature","description":"from skill","repos":["agentic-orchestrator"],"pipeline":"medium","risk_level":"high","use_current_branch":true}`,
		},
		Stdout: &out,
		Stderr: ioDiscard{},
		Deps:   jsonCommandDeps{Connect: connect},
	})
	if code != 0 {
		t.Fatalf("runJSONCommand(feature create) code = %d, want 0", code)
	}
	if client.createReq.Name != "CLI feature" || client.createReq.Pipeline != "medium" || client.createReq.RiskLevel != "high" || !client.createReq.UseCurrentBranch {
		t.Fatalf("create request = %+v, want JSON input forwarded", client.createReq)
	}
	if got := decodeJSONEnvelope(t, out.Bytes()); got.Result["feature_id"] != "feat-created" {
		t.Fatalf("create result = %#v, want feature_id", got.Result)
	}

	out.Reset()
	code = runJSONCommand(context.Background(), jsonCommandOptions{
		Args: []string{
			"feature", "review", "feat-created", "--json",
			"--input-json", `{"decision":"approve","comment":"ship it"}`,
		},
		Stdout: &out,
		Stderr: ioDiscard{},
		Deps:   jsonCommandDeps{Connect: connect},
	})
	if code != 0 {
		t.Fatalf("runJSONCommand(feature review) code = %d, want 0", code)
	}
	if client.reviewFeatureID != "feat-created" || client.reviewReq.Decision != "approve" || client.reviewReq.Comment != "ship it" {
		t.Fatalf("review request feature=%q req=%+v, want JSON input forwarded", client.reviewFeatureID, client.reviewReq)
	}

	out.Reset()
	code = runJSONCommand(context.Background(), jsonCommandOptions{
		Args: []string{
			"feature", "answer", "feat-created", "--json",
			"--input-json", `{"kind":"permission","request_id":"perm-1","session_id":"sess-1","decision":"allow"}`,
		},
		Stdout: &out,
		Stderr: ioDiscard{},
		Deps:   jsonCommandDeps{Connect: connect},
	})
	if code != 0 {
		t.Fatalf("runJSONCommand(feature answer permission) code = %d, want 0", code)
	}
	if client.permissionReq.RequestID != "perm-1" || client.permissionReq.SessionID != "sess-1" || client.permissionReq.Decision != "allow" {
		t.Fatalf("permission request = %+v, want JSON input forwarded", client.permissionReq)
	}
}

func TestFeatureManageWatchStopsAtParkedStatuses(t *testing.T) {
	for _, status := range []string{"CodeReady", "Interrupted", "Stopped", "Published", "Done", "Failed"} {
		t.Run(status, func(t *testing.T) {
			if !shouldStopJSONWatch(jsonFeatureDetail("feat-1", status).Feature) {
				t.Fatalf("shouldStopJSONWatch(%q) = false, want true", status)
			}
		})
	}
}

func TestFeatureManageWatchStreamsSSEUpdatesAndTerminalEvent(t *testing.T) {
	signals := make(chan serverruntime.RefreshSignal, 1)
	errs := make(chan error, 1)
	signals <- serverruntime.RefreshSignal{
		Event: serverruntime.SSEEventDTO{Kind: "lifecycle.updated"},
		Resource: serverruntime.ResourceDTO{
			Type:      "feature",
			FeatureID: "feat-1",
		},
		SnapshotRequired: true,
	}
	close(signals)
	close(errs)
	client := &fakeJSONCLIClient{
		details: []serverruntime.FeatureDetailResponse{
			jsonFeatureDetail("feat-1", "Implementing"),
			jsonFeatureDetail("feat-1", "Done"),
		},
		watchSignals: signals,
		watchErrs:    errs,
	}
	var out bytes.Buffer

	code := runJSONCommand(context.Background(), jsonCommandOptions{
		Args:   []string{"feature", "manage", "feat-1", "--watch", "--json"},
		Stdout: &out,
		Stderr: ioDiscard{},
		Deps: jsonCommandDeps{
			Connect: func(context.Context, jsonCommandOptions) (jsonCLIClient, error) {
				return client, nil
			},
		},
	})
	if code != 0 {
		t.Fatalf("runJSONCommand(feature manage --watch) code = %d, want 0", code)
	}
	events := decodeWatchEvents(t, out.Bytes())
	if len(events) != 3 {
		t.Fatalf("watch event count = %d, want snapshot, state_changed, terminal; events=%+v", len(events), events)
	}
	if events[0].Type != "snapshot" || events[0].FeatureID != "feat-1" || events[0].Status != "Implementing" {
		t.Fatalf("first event = %+v, want initial snapshot", events[0])
	}
	if events[1].Type != "state_changed" || events[1].From != "Implementing" || events[1].To != "Done" {
		t.Fatalf("second event = %+v, want state_changed Implementing->Done", events[1])
	}
	if events[2].Type != "terminal" || events[2].Status != "Done" {
		t.Fatalf("third event = %+v, want terminal Done", events[2])
	}
}

func TestFeatureManageWatchEmitsPromptAndPermissionAttentionFromRefreshSnapshot(t *testing.T) {
	signals := make(chan serverruntime.RefreshSignal, 1)
	errs := make(chan error, 1)
	signals <- serverruntime.RefreshSignal{
		Event: serverruntime.SSEEventDTO{Kind: "prompt.updated"},
		Resource: serverruntime.ResourceDTO{
			Type:      "prompt",
			FeatureID: "feat-1",
		},
		SnapshotRequired: true,
	}
	close(signals)
	close(errs)
	client := &snapshotJSONCLIClient{
		fakeJSONCLIClient: &fakeJSONCLIClient{
			detail:       jsonFeatureDetail("feat-1", "Implementing"),
			watchSignals: signals,
			watchErrs:    errs,
		},
		snapshots: []serverruntime.RefreshSnapshot{{
			Prompts: &serverruntime.PromptSnapshotResponse{
				AskUserQuestions: []serverruntime.ControlRequestDTO{{
					RequestID: "ask-1",
					SessionID: "sess-ask",
					FeatureID: "feat-1",
					ToolName:  "AskUserQuestion",
					Status:    "pending",
					Summary:   "Choose an approach",
				}},
			},
			Permissions: &serverruntime.PermissionSnapshotResponse{
				Requests: []serverruntime.ControlRequestDTO{{
					RequestID: "perm-1",
					SessionID: "sess-perm",
					FeatureID: "feat-1",
					ToolName:  "Bash",
					Status:    "pending",
					Summary:   "Run tests",
				}},
			},
		}},
	}
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	code := runJSONCommand(ctx, jsonCommandOptions{
		Args:   []string{"feature", "manage", "feat-1", "--watch", "--json"},
		Stdout: &out,
		Stderr: ioDiscard{},
		Deps: jsonCommandDeps{
			Connect: func(context.Context, jsonCommandOptions) (jsonCLIClient, error) {
				return client, nil
			},
		},
	})
	if code != 0 {
		t.Fatalf("runJSONCommand(feature manage --watch prompt/permission) code = %d, want 0", code)
	}
	events := decodeWatchEvents(t, out.Bytes())
	if !hasWatchAttention(events, "ask_user", "ask-1") {
		t.Fatalf("events = %+v, want ask_user attention with request_id ask-1", events)
	}
	if !hasWatchAttention(events, "permission", "perm-1") {
		t.Fatalf("events = %+v, want permission attention with request_id perm-1", events)
	}
}

func TestFeatureManageWatchFallsBackToPollingWhenSSEFails(t *testing.T) {
	signals := make(chan serverruntime.RefreshSignal)
	errs := make(chan error, 1)
	close(signals)
	errs <- errors.New("event stream reconnect limit reached")
	close(errs)
	client := &fakeJSONCLIClient{
		details: []serverruntime.FeatureDetailResponse{
			jsonFeatureDetail("feat-1", "Implementing"),
			jsonFeatureDetail("feat-1", "Failed"),
		},
		watchSignals: signals,
		watchErrs:    errs,
	}
	var out bytes.Buffer

	code := runJSONCommand(context.Background(), jsonCommandOptions{
		Args:   []string{"feature", "manage", "feat-1", "--watch", "--json"},
		Stdout: &out,
		Stderr: ioDiscard{},
		Deps: jsonCommandDeps{
			Connect: func(context.Context, jsonCommandOptions) (jsonCLIClient, error) {
				return client, nil
			},
		},
	})
	if code != 0 {
		t.Fatalf("runJSONCommand(feature manage --watch fallback) code = %d, want 0", code)
	}
	events := decodeWatchEvents(t, out.Bytes())
	if len(events) != 3 {
		t.Fatalf("watch event count = %d, want snapshot, state_changed, terminal; events=%+v", len(events), events)
	}
	if events[1].Type != "state_changed" || events[1].From != "Implementing" || events[1].To != "Failed" {
		t.Fatalf("fallback event = %+v, want state_changed Implementing->Failed", events[1])
	}
	if events[2].Type != "terminal" || events[2].Status != "Failed" {
		t.Fatalf("terminal event = %+v, want Failed", events[2])
	}
}

type fakeJSONCLIClient struct {
	detail          serverruntime.FeatureDetailResponse
	detailErr       error
	details         []serverruntime.FeatureDetailResponse
	detailCalls     int
	action          serverruntime.FeatureStartResponse
	actionErr       error
	create          serverruntime.CreateFeatureResponse
	createErr       error
	createReq       serverruntime.CreateFeatureRequest
	review          serverruntime.ReviewDecisionResponse
	reviewErr       error
	reviewReq       serverruntime.ReviewDecisionRequest
	reviewFeatureID string
	permission      serverruntime.PermissionAnswerResponse
	permissionErr   error
	permissionReq   serverruntime.PermissionAnswerRequest
	watchSignals    <-chan serverruntime.RefreshSignal
	watchErrs       <-chan error
}

type snapshotJSONCLIClient struct {
	*fakeJSONCLIClient
	snapshots     []serverruntime.RefreshSnapshot
	snapshotErr   error
	snapshotCalls int
}

func (f *snapshotJSONCLIClient) FetchRefreshSnapshot(context.Context, serverruntime.RefreshSignal) (serverruntime.RefreshSnapshot, error) {
	if len(f.snapshots) == 0 {
		return serverruntime.RefreshSnapshot{}, f.snapshotErr
	}
	if f.snapshotCalls >= len(f.snapshots) {
		return f.snapshots[len(f.snapshots)-1], f.snapshotErr
	}
	resp := f.snapshots[f.snapshotCalls]
	f.snapshotCalls++
	return resp, f.snapshotErr
}

func (f *fakeJSONCLIClient) FeatureDetail(context.Context, string) (serverruntime.FeatureDetailResponse, error) {
	if len(f.details) > 0 {
		if f.detailCalls >= len(f.details) {
			return f.details[len(f.details)-1], f.detailErr
		}
		resp := f.details[f.detailCalls]
		f.detailCalls++
		return resp, f.detailErr
	}
	return f.detail, f.detailErr
}

func (f *fakeJSONCLIClient) StartFeature(context.Context, string) (serverruntime.FeatureStartResponse, error) {
	return f.action, f.actionErr
}

func (f *fakeJSONCLIClient) CreateFeature(_ context.Context, req serverruntime.CreateFeatureRequest) (serverruntime.CreateFeatureResponse, error) {
	f.createReq = req
	return f.create, f.createErr
}

func (f *fakeJSONCLIClient) ReviewDecision(_ context.Context, featureID string, req serverruntime.ReviewDecisionRequest) (serverruntime.ReviewDecisionResponse, error) {
	f.reviewFeatureID = featureID
	f.reviewReq = req
	return f.review, f.reviewErr
}

func (f *fakeJSONCLIClient) AnswerPermission(_ context.Context, req serverruntime.PermissionAnswerRequest) (serverruntime.PermissionAnswerResponse, error) {
	f.permissionReq = req
	return f.permission, f.permissionErr
}

func (f *fakeJSONCLIClient) Features(context.Context) (serverruntime.FeatureListResponse, error) {
	return serverruntime.FeatureListResponse{}, errors.New("not implemented")
}

func (f *fakeJSONCLIClient) RuntimeConfig(context.Context) (serverruntime.RuntimeConfigResponse, error) {
	return serverruntime.RuntimeConfigResponse{}, errors.New("not implemented")
}

func (f *fakeJSONCLIClient) SubscribeEvents(context.Context, serverruntime.EventSubscriptionOptions) (<-chan serverruntime.RefreshSignal, <-chan error) {
	if f.watchSignals == nil {
		signals := make(chan serverruntime.RefreshSignal)
		close(signals)
		errs := make(chan error)
		close(errs)
		return signals, errs
	}
	return f.watchSignals, f.watchErrs
}

type decodedEnvelope struct {
	SchemaVersion int                   `json:"schema_version"`
	APIVersion    string                `json:"api_version"`
	OK            bool                  `json:"ok"`
	Result        map[string]any        `json:"result"`
	Error         *decodedEnvelopeError `json:"error"`
}

type decodedEnvelopeError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Target  map[string]any `json:"target"`
}

func decodeJSONEnvelope(t testing.TB, data []byte) decodedEnvelope {
	t.Helper()
	var got decodedEnvelope
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal(%q): %v", string(data), err)
	}
	return got
}

func jsonFeatureDetail(featureID, status string) serverruntime.FeatureDetailResponse {
	return serverruntime.FeatureDetailResponse{
		APIVersion: serverruntime.APIVersion,
		Feature: serverruntime.FeatureDetailDTO{
			FeatureSummary: serverruntime.FeatureSummary{
				ID:     featureID,
				Name:   "Watched feature",
				Status: status,
			},
		},
	}
}

func decodeWatchEvents(t testing.TB, data []byte) []normalizedFeatureEvent {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	events := make([]normalizedFeatureEvent, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var evt normalizedFeatureEvent
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			t.Fatalf("json.Unmarshal(%q): %v", line, err)
		}
		events = append(events, evt)
	}
	return events
}

func hasWatchAttention(events []normalizedFeatureEvent, kind, requestID string) bool {
	for _, evt := range events {
		if evt.Type != "attention_required" || evt.Kind != kind {
			continue
		}
		if requestID == "" {
			return true
		}
		if evt.Detail != nil && evt.Detail["request_id"] == requestID {
			return true
		}
	}
	return false
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
