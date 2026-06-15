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
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/instancelock"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPStreamableHTTPInitializeListAndReadTool(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	handler := NewHandler(HandlerOptions{
		Runtime:  RuntimeIdentity{RuntimeDir: filepath.Dir(store.BaseDir), StateDir: store.BaseDir, Config: filepath.Join(filepath.Dir(store.BaseDir), "config.yaml")},
		Features: store,
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	session := connectMCPClient(t, srv)

	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	names := mcpToolNamesFromResult(tools)
	t.Logf("mcp initialize ok; tools/list count=%d names=%v", len(names), names)
	for _, want := range []string{
		"feature_list",
		"feature_get",
		"config_runtime_get",
		"prompt_snapshot_get",
		"permission_snapshot_get",
		"session_list",
		"operation_list",
		"recovery_snapshot_get",
		"feature_create",
		"review_comments_fetch",
		"recovery_execute",
	} {
		if !stringSliceContains(names, want) {
			t.Fatalf("tools/list names = %v; want %s", names, want)
		}
	}
	if stringSliceContains(names, "event_stream") || stringSliceContains(names, "events_get") {
		t.Fatalf("tools/list includes REST SSE stream substitute: %v", names)
	}
	featureListTool := mcpToolByName(t, tools.Tools, "feature_list")
	if featureListTool.InputSchema == nil || featureListTool.OutputSchema == nil {
		t.Fatalf("feature_list schemas = input:%T output:%T; want typed schemas", featureListTool.InputSchema, featureListTool.OutputSchema)
	}

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "feature_list"})
	if err != nil {
		t.Fatalf("CallTool(feature_list) error = %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool(feature_list) IsError = true; content = %s", mcpToolText(t, res))
	}
	got := structuredContentMap(t, res)
	rest := getJSONMap(t, handler, "/api/v1/features")
	if got["api_version"] != rest["api_version"] {
		t.Fatalf("feature_list api_version = %v; want REST %v", got["api_version"], rest["api_version"])
	}
	features := got["features"].([]any)
	t.Logf("mcp read feature_list api_version=%v feature_count=%d", got["api_version"], len(features))
	if len(features) != 1 || features[0].(map[string]any)["id"] != f.ID {
		t.Fatalf("feature_list features = %+v; want feature %s", features, f.ID)
	}
}

func TestMCPToolCatalogMatchesRESTParityContract(t *testing.T) {
	t.Parallel()
	handler := NewHandler(HandlerOptions{})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	session := connectMCPClient(t, srv)

	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	got := mcpToolNamesFromResult(tools)
	want := expectedMCPToolNames()
	if !slices.Equal(got, want) {
		t.Fatalf("tools/list names = %v; want %v", got, want)
	}
	for _, forbidden := range []string{"event_stream", "events_get", "event_list", "resource_list"} {
		if stringSliceContains(got, forbidden) {
			t.Fatalf("tools/list includes non-REST-parity stream/resource substitute %q in %v", forbidden, got)
		}
	}
}

func TestMCPRuntimeHealthOmitsOwnerPrivatePaths(t *testing.T) {
	t.Parallel()
	runtimeDir := t.TempDir()
	handler := NewHandler(HandlerOptions{
		Runtime: RuntimeIdentity{
			RuntimeDir: runtimeDir,
			StateDir:   filepath.Join(runtimeDir, "features"),
			Config:     filepath.Join(runtimeDir, "config.yaml"),
		},
		Owner: instancelock.Owner{
			PID:       1234,
			StartedAt: time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC),
			StateDir:  filepath.Join(runtimeDir, "features"),
			Config:    filepath.Join(runtimeDir, "config.yaml"),
		},
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	session := connectMCPClient(t, srv)

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "runtime_health_get"})
	if err != nil {
		t.Fatalf("CallTool(runtime_health_get) error = %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool(runtime_health_get) IsError = true; content = %s", mcpToolText(t, res))
	}
	got := structuredContentMap(t, res)
	owner, ok := got["owner"].(map[string]any)
	if !ok {
		t.Fatalf("runtime_health_get owner = %v; want object", got["owner"])
	}
	assertOwnerMapOmitsPrivatePaths(t, owner, "runtime_health_get owner")
}

func TestMCPSchemasCoverRESTDTOFields(t *testing.T) {
	t.Parallel()
	handler := NewHandler(HandlerOptions{})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	session := connectMCPClient(t, srv)

	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	for _, tool := range tools.Tools {
		if tool.InputSchema == nil || tool.OutputSchema == nil {
			t.Fatalf("%s schemas = input:%T output:%T; want typed REST DTO schemas", tool.Name, tool.InputSchema, tool.OutputSchema)
		}
		assertSchemaHasProperties(t, tool.OutputSchema, tool.Name+" output", "api_version")
	}
	for name, fields := range map[string][]string{
		"feature_get":            {"feature_id"},
		"feature_config_get":     {"feature_id"},
		"session_get":            {"session_id"},
		"session_transcript_get": {"session_id", "cursor", "limit"},
		"artifact_list":          {"feature_id", "run_number"},
		"artifact_content_get":   {"feature_id", "run_number", "artifact_id", "offset", "limit"},
		"log_content_get":        {"feature_id", "run_number", "log_id", "offset", "limit"},
		"operation_list":         {"state", "feature_id", "kind", "cursor", "limit"},
		"feature_create":         {"name", "description", "repos", "models", "exit_criteria", "risk_level", "pipeline"},
		"feature_restart":        {"feature_id", "max_iterations_delta", "max_plan_iterations_delta"},
		"review_decision_submit": {"feature_id", "decision", "phase", "phase_plan", "roadmap", "is_rewind", "comment"},
		"feature_config_update":  {"feature_id", "models", "inquireness", "checkpoints", "pipeline"},
		"need_user_input_decide": {"feature_id", "decision", "repo_name", "cycle_type"},
		"need_user_input_draft":  {"feature_id", "repo_name", "cycle_type", "answers"},
		"permission_answer":      {"request_id", "session_id", "decision"},
		"ask_user_answer":        {"request_id", "session_id", "answers"},
		"help_send":              {"feature_id", "session_id", "message"},
		"config_runtime_update":  {"defaults"},
		"feature_publish":        {"feature_id", "repos"},
		"feature_rewind":         {"feature_id", "target_phase", "roadmap_phase", "upgrade_pipeline"},
		"rebase_start":           {"feature_id", "repo", "rebase_target", "conflict_files"},
		"review_comments_fetch":  {"feature_id", "repo"},
		"review_comments_start":  {"feature_id", "repo", "mode"},
		"tweak_finish":           {"feature_id", "decision", "had_changes"},
		"refactor_start":         {"feature_id", "repo", "prompt", "pipeline"},
		"refactor_restart":       {"feature_id", "repo", "prompt", "pipeline"},
		"feature_cleanup":        {"feature_id", "target", "repo"},
	} {
		tool := mcpToolByName(t, tools.Tools, name)
		assertSchemaHasProperties(t, tool.InputSchema, name+" input", fields...)
	}
	for _, name := range operationAcceptedMCPTools() {
		tool := mcpToolByName(t, tools.Tools, name)
		assertSchemaHasProperties(t, tool.OutputSchema, name+" output", "operation_id", "status")
	}
}

func TestMCPMutationToolUsesRESTSemanticsAndOperationFollowUp(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	ops := mustOperationRegistry(t, filepath.Join(filepath.Dir(store.BaseDir), "operations"))
	mutations := &fakeMutationTarget{}
	handler := NewHandler(HandlerOptions{
		Runtime:    RuntimeIdentity{RuntimeDir: filepath.Dir(store.BaseDir), StateDir: store.BaseDir, Config: filepath.Join(filepath.Dir(store.BaseDir), "config.yaml")},
		Features:   store,
		Operations: ops,
		Mutations:  mutations,
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	session := connectMCPClient(t, srv)
	sseResp, sseReader := openOperationSSE(t, srv.Client(), srv.URL)
	t.Cleanup(func() { sseResp.Body.Close() })

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "feature_start",
		Arguments: map[string]any{"feature_id": f.ID},
	})
	if err != nil {
		t.Fatalf("CallTool(feature_start) error = %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool(feature_start) IsError = true; content = %s", mcpToolText(t, res))
	}
	accepted := structuredContentMap(t, res)
	opID, ok := accepted["operation_id"].(string)
	if !ok || opID == "" {
		t.Fatalf("feature_start operation_id = %v; want non-empty", accepted["operation_id"])
	}
	if accepted["status"] != string(OperationStatusQueued) {
		t.Fatalf("feature_start status = %v; want %s", accepted["status"], OperationStatusQueued)
	}
	t.Logf("mcp mutation feature_start accepted operation_id=%s status=%s", opID, accepted["status"])
	if block := readSSEBlockContaining(t, sseReader, "operation.updated", opID); !strings.Contains(block, opID) {
		t.Fatalf("operation SSE block = %s; want operation id %s", block, opID)
	}
	t.Logf("rest sse observed operation.updated for operation_id=%s", opID)

	op := waitForMCPOperationStatus(t, session, opID, OperationStatusSucceeded)
	t.Logf("mcp follow-up operation_list operation_id=%s status=%s", op["id"], op["status"])
	if op["id"] != opID || op["status"] != string(OperationStatusSucceeded) {
		t.Fatalf("operation_list op = %+v; want %s succeeded", op, opID)
	}
	mutations.mu.Lock()
	startCalls := append([]string(nil), mutations.startCalls...)
	mutations.mu.Unlock()
	if len(startCalls) != 1 || startCalls[0] != f.ID {
		t.Fatalf("StartFeature calls = %v; want [%s]", startCalls, f.ID)
	}
}

func TestMCPShutdownToolUsesRESTMutationSemantics(t *testing.T) {
	t.Parallel()
	store, _ := seedReadFeature(t)
	shutdownRequested := make(chan struct{}, 1)
	handler := NewHandler(HandlerOptions{
		Runtime:    RuntimeIdentity{RuntimeDir: filepath.Dir(store.BaseDir), StateDir: store.BaseDir, Config: filepath.Join(filepath.Dir(store.BaseDir), "config.yaml")},
		Features:   store,
		Operations: mustOperationRegistry(t, filepath.Join(filepath.Dir(store.BaseDir), "operations")),
		Mutations:  &fakeMutationTarget{},
		RequestShutdown: func() {
			shutdownRequested <- struct{}{}
		},
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	session := connectMCPClient(t, srv)

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "runtime_shutdown"})
	if err != nil {
		t.Fatalf("CallTool(runtime_shutdown) error = %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool(runtime_shutdown) IsError = true; content = %s", mcpToolText(t, res))
	}
	accepted := structuredContentMap(t, res)
	opID, ok := accepted["operation_id"].(string)
	if !ok || opID == "" {
		t.Fatalf("runtime_shutdown operation_id = %v; want non-empty", accepted["operation_id"])
	}
	if accepted["status"] != string(OperationStatusQueued) {
		t.Fatalf("runtime_shutdown status = %v; want %s", accepted["status"], OperationStatusQueued)
	}
	select {
	case <-shutdownRequested:
	case <-time.After(time.Second):
		t.Fatal("shutdown hook was not called after accepted MCP response")
	}
	op := waitForMCPOperationStatus(t, session, opID, OperationStatusSucceeded)
	if op["kind"] != "runtime.shutdown" || op["target"].(map[string]any)["type"] != "runtime" {
		t.Fatalf("operation_list shutdown op = %+v; want runtime.shutdown runtime target", op)
	}
}

func TestMCPConflictReturnsRejectedOperationStatusMatchingREST(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	started := make(chan struct{})
	release := make(chan struct{})
	mutations := &fakeMutationTarget{startHook: func() {
		close(started)
		<-release
	}}
	handler := NewHandler(HandlerOptions{
		Runtime:    RuntimeIdentity{RuntimeDir: filepath.Dir(store.BaseDir), StateDir: store.BaseDir, Config: filepath.Join(filepath.Dir(store.BaseDir), "config.yaml")},
		Features:   store,
		Operations: mustOperationRegistry(t, filepath.Join(filepath.Dir(store.BaseDir), "operations")),
		Mutations:  mutations,
		MutationLimits: MutationLimits{
			FeatureQueue:   0,
			RuntimeQueue:   0,
			AggregateQueue: 10,
		},
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	session := connectMCPClient(t, srv)
	restClient := &http.Client{Timeout: time.Second}

	first := postMutationHTTP(t, restClient, srv.URL+"/api/v1/features/"+f.ID+"/start", `{}`)
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("REST feature start status = %d; want 202", first.StatusCode)
	}
	firstID := decodeHTTPJSONMap(t, first)["operation_id"].(string)
	<-started

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "feature_start",
		Arguments: map[string]any{"feature_id": f.ID},
	})
	if err != nil {
		t.Fatalf("CallTool(feature_start conflict) error = %v; want tool error result", err)
	}
	if !res.IsError {
		t.Fatalf("CallTool(feature_start conflict) IsError = false; want tool error carrying rejected operation")
	}
	conflictBody := decodeJSONStringMap(t, mcpToolText(t, res))
	conflictID, _ := conflictBody["operation_id"].(string)
	if conflictID == "" || conflictBody["status"] != string(OperationStatusRejected) {
		t.Fatalf("MCP conflict body = %+v; want rejected operation status with operation_id", conflictBody)
	}
	op := waitForMCPOperationStatus(t, session, conflictID, OperationStatusRejected)
	if op["status"] != string(OperationStatusRejected) {
		t.Fatalf("MCP operation_list op = %+v; want rejected", op)
	}

	close(release)
	waitForMCPOperationStatus(t, session, firstID, OperationStatusSucceeded)
}

func TestMCPReadToolsCoverSnapshotsTypedContentSessionsOperationsAndRecovery(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	runDir := store.RunDir(f.ID, 1)
	writeFile(t, filepath.Join(runDir, "plan", "phase-plan.md"), "hello artifact content")
	writeFile(t, filepath.Join(runDir, "logs", "session.log"), "first\nsecond\nthird\n")
	msgs := []llm.SDKMessage{
		{Type: "assistant", Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{Role: "assistant", Content: []llm.ContentBlock{{Type: "text", Text: "safe text"}, {Type: "tool_use", Name: "Bash", Input: json.RawMessage(`{"command":"echo private-token"}`)}}}}},
		{Type: "user", User: &llm.UserMessage{Message: llm.ConversationMsg{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "raw prompt private-token"}}}}},
	}
	sessions := fakeSessionManager{views: []ports.SessionView{
		&fakeSessionView{
			id: "sess-ask", featureID: f.ID, phase: feature.PhaseImplement, repoName: "agentic-orchestrator",
			kind: ports.KindPhase, status: ports.SessionWaitingHelp, provider: "codex", model: "gpt-5.4",
			logPath: filepath.Join(runDir, "logs", "session.log"), messages: msgs,
			initialPrompt: "private-token initial prompt",
			pending: []*llm.ControlRequestMessage{{
				Type:      "control_request",
				RequestID: "ask-1",
				Request: llm.ControlRequest{
					Subtype: "can_use_tool", ToolName: "AskUserQuestion",
					Input: json.RawMessage(`{"questions":[{"question":"Choose?","options":[{"label":"A"}]}],"secret":"private-token"}`),
				},
			}},
		},
		&fakeSessionView{
			id: "sess-perm", featureID: f.ID, phase: feature.PhasePlan,
			kind: ports.KindPhase, status: ports.SessionWaitingPermission,
			pending: []*llm.ControlRequestMessage{{
				Type:      "control_request",
				RequestID: "perm-1",
				Request:   llm.ControlRequest{Subtype: "can_use_tool", ToolName: "Bash", Input: json.RawMessage(`{"command":"echo private-token"}`)},
			}},
		},
	}}
	recoveryFeature := &feature.Feature{
		ID:     "feat-recover",
		Name:   "Recover me",
		Slug:   "recover-me",
		Status: feature.StatusImplementing,
		Repos:  []feature.FeatureRepo{{Name: "api"}},
	}
	mutations := &fakeMutationTarget{
		recoveryItems: []ports.RecoveryItem{{
			PIDFile: ports.PIDFile{
				PID:         12345,
				FeatureID:   "feat-recover",
				Phase:       "implement",
				Iteration:   7,
				RepoName:    "api",
				Dir:         "/private/runtime/features/feat-recover",
				WorktreeDir: "/private/worktree",
			},
			ProcessAlive: true,
			Feature:      recoveryFeature,
			RepoName:     "api",
		}},
	}
	handler := NewHandler(HandlerOptions{
		Runtime:    RuntimeIdentity{RuntimeDir: filepath.Dir(store.BaseDir), StateDir: store.BaseDir, Config: filepath.Join(filepath.Dir(store.BaseDir), "config.yaml")},
		Features:   store,
		Sessions:   sessions,
		Operations: mustOperationRegistry(t, filepath.Join(filepath.Dir(store.BaseDir), "operations")),
		Mutations:  mutations,
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	session := connectMCPClient(t, srv)

	calls := []struct {
		name string
		args map[string]any
	}{
		{name: "config_runtime_get"},
		{name: "model_catalog_get"},
		{name: "prompt_snapshot_get"},
		{name: "permission_snapshot_get"},
		{name: "session_list"},
		{name: "session_get", args: map[string]any{"session_id": "sess-ask"}},
		{name: "session_transcript_get", args: map[string]any{"session_id": "sess-ask", "limit": 10}},
		{name: "artifact_list", args: map[string]any{"feature_id": f.ID, "run_number": 1}},
		{name: "artifact_content_get", args: map[string]any{"feature_id": f.ID, "run_number": 1, "artifact_id": "plan", "offset": 6, "limit": 8}},
		{name: "log_content_get", args: map[string]any{"feature_id": f.ID, "run_number": 1, "log_id": "session", "offset": 6, "limit": 6}},
		{name: "live_preview_get", args: map[string]any{"feature_id": f.ID}},
		{name: "operation_list", args: map[string]any{"limit": 20}},
		{name: "recovery_snapshot_get"},
	}
	for _, call := range calls {
		res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: call.name, Arguments: call.args})
		if err != nil {
			t.Fatalf("CallTool(%s) error = %v", call.name, err)
		}
		if res.IsError {
			t.Fatalf("CallTool(%s) IsError = true; content = %s", call.name, mcpToolText(t, res))
		}
		body := structuredContentMap(t, res)
		if body["api_version"] != APIVersion {
			t.Fatalf("CallTool(%s) api_version = %v; want %s", call.name, body["api_version"], APIVersion)
		}
		raw := mustMarshalJSON(t, body)
		for _, leaked := range []string{"private-token", "raw prompt", "/private/runtime", "/private/worktree"} {
			if strings.Contains(raw, leaked) {
				t.Fatalf("CallTool(%s) leaked %q in %s", call.name, leaked, raw)
			}
		}
	}
}

func TestMCPMutationRejectsNonLoopbackBrowserOriginBeforeOperation(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	ops := mustOperationRegistry(t, filepath.Join(filepath.Dir(store.BaseDir), "operations"))
	mutations := &fakeMutationTarget{}
	handler := NewHandler(HandlerOptions{
		Runtime:    RuntimeIdentity{RuntimeDir: filepath.Dir(store.BaseDir), StateDir: store.BaseDir, Config: filepath.Join(filepath.Dir(store.BaseDir), "config.yaml")},
		Features:   store,
		Operations: ops,
		Mutations:  mutations,
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	session := connectMCPClientWithHTTPClient(t, srv, httpClientWithOrigin(srv.Client(), "https://evil.example"))

	read, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "feature_list"})
	if err != nil {
		t.Fatalf("CallTool(feature_list) error = %v", err)
	}
	if read.IsError {
		t.Fatalf("CallTool(feature_list) IsError = true; content = %s", mcpToolText(t, read))
	}

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "feature_start",
		Arguments: map[string]any{"feature_id": f.ID},
	})
	if err != nil {
		t.Fatalf("CallTool(feature_start) error = %v; want REST tool error result", err)
	}
	if !res.IsError {
		t.Fatalf("CallTool(feature_start) IsError = false; want browser-origin rejection")
	}
	text := mcpToolText(t, res)
	for _, want := range []string{`"api_version":"` + APIVersion + `"`, `"forbidden"`, "browser origin is not trusted"} {
		if !strings.Contains(text, want) {
			t.Fatalf("tool error text = %s; want substring %s", text, want)
		}
	}
	assertOperationCount(t, handler, 0)
	mutations.mu.Lock()
	startCalls := append([]string(nil), mutations.startCalls...)
	mutations.mu.Unlock()
	if len(startCalls) != 0 {
		t.Fatalf("StartFeature calls = %v; want none before trusted-origin gate", startCalls)
	}
}

func TestMCPShutdownRejectsNonLoopbackBrowserOriginBeforeOperation(t *testing.T) {
	t.Parallel()
	store, _ := seedReadFeature(t)
	ops := mustOperationRegistry(t, filepath.Join(filepath.Dir(store.BaseDir), "operations"))
	shutdownRequested := make(chan struct{}, 1)
	handler := NewHandler(HandlerOptions{
		Runtime:    RuntimeIdentity{RuntimeDir: filepath.Dir(store.BaseDir), StateDir: store.BaseDir, Config: filepath.Join(filepath.Dir(store.BaseDir), "config.yaml")},
		Features:   store,
		Operations: ops,
		Mutations:  &fakeMutationTarget{},
		RequestShutdown: func() {
			shutdownRequested <- struct{}{}
		},
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	session := connectMCPClientWithHTTPClient(t, srv, httpClientWithOrigin(srv.Client(), "https://evil.example"))

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "runtime_shutdown"})
	if err != nil {
		t.Fatalf("CallTool(runtime_shutdown) error = %v; want REST tool error result", err)
	}
	if !res.IsError {
		t.Fatalf("CallTool(runtime_shutdown) IsError = false; want browser-origin rejection")
	}
	text := mcpToolText(t, res)
	for _, want := range []string{`"api_version":"` + APIVersion + `"`, `"forbidden"`, "browser origin is not trusted"} {
		if !strings.Contains(text, want) {
			t.Fatalf("tool error text = %s; want substring %s", text, want)
		}
	}
	assertOperationCount(t, handler, 0)
	select {
	case <-shutdownRequested:
		t.Fatal("shutdown hook was called before trusted-origin gate passed")
	default:
	}
}

func TestMCPMutationAllowsTrustedLoopbackBrowserOrigin(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	ops := mustOperationRegistry(t, filepath.Join(filepath.Dir(store.BaseDir), "operations"))
	mutations := &fakeMutationTarget{}
	handler := NewHandler(HandlerOptions{
		Runtime:    RuntimeIdentity{RuntimeDir: filepath.Dir(store.BaseDir), StateDir: store.BaseDir, Config: filepath.Join(filepath.Dir(store.BaseDir), "config.yaml")},
		Features:   store,
		Operations: ops,
		Mutations:  mutations,
	})

	origin := "http://127.0.0.1:5173"
	preflight := httptest.NewRequest(http.MethodOptions, "/mcp", nil)
	preflight.Header.Set("Origin", origin)
	preflight.Header.Set("Access-Control-Request-Method", http.MethodPost)
	preflight.Header.Set("Access-Control-Request-Headers", "content-type, mcp-protocol-version, mcp-session-id, mcp-method, mcp-name")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, preflight)
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("trusted /mcp preflight status = %d; want 204", resp.StatusCode)
	}
	assertAccessControlHeaders(t, resp.Header, map[string]string{
		"Access-Control-Allow-Origin":  origin,
		"Access-Control-Allow-Methods": http.MethodPost,
		"Access-Control-Allow-Headers": "Content-Type, Mcp-Protocol-Version, Mcp-Session-Id, Mcp-Method, Mcp-Name, Last-Event-ID",
	})
	t.Logf("trusted /mcp browser preflight origin=%s status=%d allow_headers=%q", origin, resp.StatusCode, resp.Header.Get("Access-Control-Allow-Headers"))
	assertOperationCount(t, handler, 0)

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	httpClient, recorder := httpClientWithOriginRecorder(srv.Client(), origin)
	session := connectMCPClientWithHTTPClient(t, srv, httpClient)

	corsResponsesBeforeCall := recorder.countHeader("Access-Control-Allow-Origin", origin)
	exposedSessionHeadersBeforeCall := recorder.countHeader("Access-Control-Expose-Headers", "Mcp-Session-Id")
	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "feature_start",
		Arguments: map[string]any{"feature_id": f.ID},
	})
	if err != nil {
		t.Fatalf("CallTool(feature_start) error = %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool(feature_start) IsError = true; content = %s", mcpToolText(t, res))
	}
	if got := recorder.countHeader("Access-Control-Allow-Origin", origin); got <= corsResponsesBeforeCall {
		t.Fatalf("actual /mcp tool-call CORS responses = %d before call, %d after call; want tool call response to allow origin %s", corsResponsesBeforeCall, got, origin)
	}
	if got := recorder.countHeader("Access-Control-Expose-Headers", "Mcp-Session-Id"); got <= exposedSessionHeadersBeforeCall {
		t.Fatalf("actual /mcp exposed session headers = %d before call, %d after call; want tool call response to expose Mcp-Session-Id", exposedSessionHeadersBeforeCall, got)
	}
	accepted := structuredContentMap(t, res)
	opID, ok := accepted["operation_id"].(string)
	if !ok || opID == "" {
		t.Fatalf("feature_start operation_id = %v; want non-empty", accepted["operation_id"])
	}
	t.Logf("trusted browser-origin MCP mutation accepted operation_id=%s status=%s", opID, accepted["status"])
	waitForMCPOperationStatus(t, session, opID, OperationStatusSucceeded)
	mutations.mu.Lock()
	startCalls := append([]string(nil), mutations.startCalls...)
	mutations.mu.Unlock()
	if len(startCalls) != 1 || startCalls[0] != f.ID {
		t.Fatalf("StartFeature calls = %v; want [%s]", startCalls, f.ID)
	}
}

func TestMCPPreflightRejectsUntrustedBrowserRequests(t *testing.T) {
	t.Parallel()
	handler := NewHandler(HandlerOptions{})
	loopbackLocalAddr := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 4321}

	tests := []struct {
		name    string
		origin  string
		method  string
		headers string
		host    string
		local   net.Addr
		want    int
	}{
		{
			name:    "non_loopback_origin",
			origin:  "https://evil.example",
			method:  http.MethodPost,
			headers: "content-type",
			want:    http.StatusForbidden,
		},
		{
			name:    "unknown_header",
			origin:  "http://127.0.0.1:5173",
			method:  http.MethodPost,
			headers: "content-type, authorization",
			want:    http.StatusForbidden,
		},
		{
			name:    "unsupported_method",
			origin:  "http://127.0.0.1:5173",
			method:  http.MethodPatch,
			headers: "content-type",
			want:    http.StatusMethodNotAllowed,
		},
		{
			name:    "host_rebinding",
			origin:  "http://127.0.0.1:5173",
			method:  http.MethodPost,
			headers: "content-type",
			host:    "evil.example",
			local:   loopbackLocalAddr,
			want:    http.StatusForbidden,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preflight := httptest.NewRequest(http.MethodOptions, "/mcp", nil)
			preflight.Header.Set("Origin", tt.origin)
			preflight.Header.Set("Access-Control-Request-Method", tt.method)
			preflight.Header.Set("Access-Control-Request-Headers", tt.headers)
			if tt.host != "" {
				preflight.Host = tt.host
			}
			if tt.local != nil {
				preflight = preflight.WithContext(context.WithValue(preflight.Context(), http.LocalAddrContextKey, tt.local))
			}
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, preflight)

			resp := w.Result()
			defer resp.Body.Close()
			if resp.StatusCode != tt.want {
				t.Fatalf("OPTIONS /mcp status = %d; want %d", resp.StatusCode, tt.want)
			}
			assertNoAccessControlHeaders(t, resp.Header)
		})
	}
}

func TestMCPActualRequestRejectsLocalhostRebindingHost(t *testing.T) {
	t.Parallel()
	handler := NewHandler(HandlerOptions{})
	loopbackLocalAddr := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 4321}
	req := httptest.NewRequest(http.MethodPost, MCPEndpointPath, strings.NewReader(`{}`))
	req.Host = "evil.example"
	req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, loopbackLocalAddr))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("actual /mcp status = %d; want 403 for localhost rebinding Host", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "invalid Host header") {
		t.Fatalf("actual /mcp body = %q; want invalid Host header", string(body))
	}
}

func TestMCPRESTValidationFailuresReturnToolErrors(t *testing.T) {
	t.Parallel()
	store, _ := seedReadFeature(t)
	handler := NewHandler(HandlerOptions{
		Runtime:    RuntimeIdentity{RuntimeDir: filepath.Dir(store.BaseDir), StateDir: store.BaseDir, Config: filepath.Join(filepath.Dir(store.BaseDir), "config.yaml")},
		Features:   store,
		Operations: mustOperationRegistry(t, filepath.Join(filepath.Dir(store.BaseDir), "operations")),
		Mutations:  &fakeMutationTarget{},
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	session := connectMCPClient(t, srv)

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "feature_create",
		Arguments: map[string]any{"name": ""},
	})
	if err != nil {
		t.Fatalf("CallTool(feature_create invalid) error = %v; want tool error result", err)
	}
	if !res.IsError {
		t.Fatalf("CallTool(feature_create invalid) IsError = false; want true")
	}
	text := mcpToolText(t, res)
	for _, want := range []string{`"api_version":"` + APIVersion + `"`, `"error"`, `"bad_request"`, `"name is required"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("tool error text = %s; want substring %s", text, want)
		}
	}
}

func TestMCPMalformedProtocolRequestIsProtocolError(t *testing.T) {
	t.Parallel()
	handler := NewHandler(HandlerOptions{})
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Result().StatusCode == http.StatusOK {
		t.Fatalf("malformed MCP status = %d; want protocol/transport error status", w.Result().StatusCode)
	}
	if strings.Contains(w.Body.String(), `"isError"`) {
		t.Fatalf("malformed MCP response = %s; want protocol error, not tool error result", w.Body.String())
	}
}

func TestPublishDiscoveryIncludesMCPMetadata(t *testing.T) {
	t.Parallel()
	runtimeDir := t.TempDir()
	rec := DiscoveryRecord{
		SchemaVersion: 1,
		APIVersion:    APIVersion,
		BaseURL:       "http://127.0.0.1:4567",
		Runtime: RuntimeIdentity{
			RuntimeDir: runtimeDir,
			StateDir:   filepath.Join(runtimeDir, "features"),
			Config:     filepath.Join(runtimeDir, "config.yaml"),
		},
		StartMode:   "server",
		StartedAt:   time.Now().UTC(),
		PublishedAt: time.Now().UTC(),
	}

	if err := PublishDiscovery(runtimeDir, rec); err != nil {
		t.Fatalf("PublishDiscovery() error = %v", err)
	}
	got, err := ReadDiscovery(runtimeDir)
	if err != nil {
		t.Fatalf("ReadDiscovery() error = %v", err)
	}
	if got.MCP.Endpoint != "http://127.0.0.1:4567/mcp" {
		t.Fatalf("MCP endpoint = %q; want base URL /mcp", got.MCP.Endpoint)
	}
	if got.MCP.Path != "/mcp" || got.MCP.Transport != "streamable_http" || got.MCP.RESTAPIVersion != APIVersion {
		t.Fatalf("MCP metadata = %+v; want streamable HTTP /mcp adapting REST %s", got.MCP, APIVersion)
	}
}

func connectMCPClient(t *testing.T, srv *httptest.Server) *mcp.ClientSession {
	t.Helper()
	return connectMCPClientWithHTTPClient(t, srv, srv.Client())
}

func connectMCPClientWithHTTPClient(t *testing.T, srv *httptest.Server, httpClient *http.Client) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	t.Cleanup(cancel)
	client := mcp.NewClient(&mcp.Implementation{Name: "agentico-test", Version: "v1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             srv.URL + "/mcp",
		HTTPClient:           httpClient,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("Connect(/mcp) error = %v", err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Fatalf("MCP session Close() error = %v", err)
		}
	})
	return session
}

func httpClientWithOrigin(base *http.Client, origin string) *http.Client {
	client, _ := httpClientWithOriginRecorder(base, origin)
	return client
}

func httpClientWithOriginRecorder(base *http.Client, origin string) (*http.Client, *responseHeaderRecorder) {
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	recorder := &responseHeaderRecorder{}
	return &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			cloned := req.Clone(req.Context())
			cloned.Header.Set("Origin", origin)
			resp, err := transport.RoundTrip(cloned)
			if resp != nil {
				recorder.record(resp.Header)
			}
			return resp, err
		}),
		CheckRedirect: base.CheckRedirect,
		Jar:           base.Jar,
		Timeout:       base.Timeout,
	}, recorder
}

type responseHeaderRecorder struct {
	mu      sync.Mutex
	headers []http.Header
}

func (r *responseHeaderRecorder) record(header http.Header) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.headers = append(r.headers, header.Clone())
}

func (r *responseHeaderRecorder) countHeader(name, value string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	var count int
	for _, header := range r.headers {
		if header.Get(name) == value {
			count++
		}
	}
	return count
}

func expectedMCPToolNames() []string {
	names := []string{
		"artifact_content_get",
		"artifact_list",
		"ask_user_answer",
		"config_runtime_get",
		"config_runtime_update",
		"feature_cleanup",
		"feature_config_get",
		"feature_config_update",
		"feature_create",
		"feature_delete",
		"feature_get",
		"feature_interrupt",
		"feature_list",
		"feature_mark_done",
		"feature_merge",
		"feature_publish",
		"feature_restart",
		"feature_resume",
		"feature_rewind",
		"feature_retry",
		"feature_start",
		"feature_stop",
		"help_send",
		"live_preview_get",
		"log_content_get",
		"model_catalog_get",
		"need_user_input_decide",
		"need_user_input_draft",
		"operation_list",
		"permission_answer",
		"permission_snapshot_get",
		"prompt_snapshot_get",
		"rebase_start",
		"recovery_execute",
		"recovery_snapshot_get",
		"refactor_restart",
		"refactor_start",
		"review_comments_fetch",
		"review_comments_start",
		"review_decision_submit",
		"runtime_health_get",
		"runtime_shutdown",
		"session_get",
		"session_list",
		"session_transcript_get",
		"tweak_finish",
		"tweak_start",
	}
	sort.Strings(names)
	return names
}

func operationAcceptedMCPTools() []string {
	return []string{
		"ask_user_answer",
		"config_runtime_update",
		"feature_cleanup",
		"feature_config_update",
		"feature_create",
		"feature_delete",
		"feature_interrupt",
		"feature_mark_done",
		"feature_merge",
		"feature_publish",
		"feature_restart",
		"feature_resume",
		"feature_rewind",
		"feature_retry",
		"feature_start",
		"feature_stop",
		"help_send",
		"need_user_input_decide",
		"need_user_input_draft",
		"permission_answer",
		"rebase_start",
		"recovery_execute",
		"refactor_restart",
		"refactor_start",
		"review_comments_start",
		"review_decision_submit",
		"runtime_shutdown",
		"tweak_finish",
		"tweak_start",
	}
}

func mcpToolNamesFromResult(result *mcp.ListToolsResult) []string {
	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return names
}

func mcpToolByName(t *testing.T, tools []*mcp.Tool, name string) *mcp.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %s not found in %v", name, mcpToolNamesFromResult(&mcp.ListToolsResult{Tools: tools}))
	return nil
}

func assertSchemaHasProperties(t *testing.T, schema any, label string, names ...string) {
	t.Helper()
	props := schemaProperties(t, schema, label)
	for _, name := range names {
		if _, ok := props[name]; !ok {
			t.Fatalf("%s schema properties = %v; want %s", label, sortedMapKeys(props), name)
		}
	}
}

func schemaProperties(t *testing.T, schema any, label string) map[string]any {
	t.Helper()
	root := schemaMap(t, schema, label)
	raw, ok := root["properties"]
	if !ok {
		t.Fatalf("%s schema = %+v; missing properties", label, root)
	}
	props, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("%s schema properties = %T; want object", label, raw)
	}
	return props
}

func schemaMap(t *testing.T, schema any, label string) map[string]any {
	t.Helper()
	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal %s schema: %v", label, err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal %s schema %s: %v", label, data, err)
	}
	return out
}

func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func structuredContentMap(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal StructuredContent: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal StructuredContent %s: %v", data, err)
	}
	return out
}

func decodeJSONStringMap(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("unmarshal JSON object %s: %v", raw, err)
	}
	return out
}

func mcpToolText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if len(result.Content) == 0 {
		return ""
	}
	if text, ok := result.Content[0].(*mcp.TextContent); ok {
		return text.Text
	}
	data, err := json.Marshal(result.Content)
	if err != nil {
		t.Fatalf("marshal content: %v", err)
	}
	return string(data)
}

func waitForMCPOperationStatus(t *testing.T, session *mcp.ClientSession, opID string, want OperationStatus) map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
			Name:      "operation_list",
			Arguments: map[string]any{"limit": 200},
		})
		if err != nil {
			t.Fatalf("CallTool(operation_list) error = %v", err)
		}
		if res.IsError {
			t.Fatalf("CallTool(operation_list) IsError = true; content = %s", mcpToolText(t, res))
		}
		body := structuredContentMap(t, res)
		for _, raw := range body["operations"].([]any) {
			op := raw.(map[string]any)
			if op["id"] == opID && op["status"] == string(want) {
				return op
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("operation %s did not reach status %s", opID, want)
	return nil
}
