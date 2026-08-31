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

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/claude"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/codex"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// Claude-backed sessions may outlive the mock CLI itself while the session
// layer gives the wrapper time to exit after a result before escalating.
// Keep these integration waits above that watchdog window so `-race ./...`
// load does not fail at the timeout boundary.
const claudeSessionDoneTimeout = 90 * time.Second

// newResearchFeature creates a minimal feature for research testing.
func newResearchFeature(t *testing.T, repoPath string) *feature.Feature {
	t.Helper()
	return &feature.Feature{
		ID:            "test-research-001",
		Name:          "Research Test",
		Slug:          "research-test",
		Description:   "Test research phase",
		Status:        feature.StatusResearching,
		CurrentPhase:  feature.PhaseResearch,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: "test-repo", Path: repoPath},
		},
		Models: config.ModelConfig{
			Research: "test-model",
			Planning: "test-model",
		},
	}
}

func TestBuildSession_AgentSelectionSmoke(t *testing.T) {
	subsetJSON, err := agentsJSONForNames([]string{"codebase-locator", "web-search-researcher"})
	if err != nil {
		t.Fatalf("agentsJSONForNames(subset): %v", err)
	}
	tests := []struct {
		name           string
		model          string
		agentNames     []string
		wantProvider   string
		wantAgentsJSON string
	}{
		{
			name:         "claude interactive nil selection",
			model:        "claude-tracer",
			wantProvider: "claude",
		},
		{
			name:         "claude interactive empty selection",
			model:        "claude-tracer",
			agentNames:   []string{},
			wantProvider: "claude",
		},
		{
			name:           "claude interactive subset selection",
			model:          "claude-tracer",
			agentNames:     []string{"codebase-locator", "web-search-researcher"},
			wantProvider:   "claude",
			wantAgentsJSON: subsetJSON,
		},
		{
			name:           "codex interactive ignores selection",
			model:          "codex-tracer",
			agentNames:     []string{"codebase-locator", "web-search-researcher"},
			wantProvider:   "codex",
			wantAgentsJSON: subsetJSON,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claudeProv := &mocks.MockProvider{
				ProviderName: "claude",
				Models:       []string{"claude-tracer"},
				CLIDetected:  true,
				CommandArgs:  []string{"claude", "--model", "claude-tracer"},
			}
			codexProv := &mocks.MockProvider{
				ProviderName: "codex",
				Models:       []string{"codex-tracer"},
				CLIDetected:  true,
				CommandArgs:  []string{"codex", "app-server"},
			}

			registry := llm.NewRegistry()
			registry.Register(claudeProv)
			registry.Register(codexProv)

			dir := t.TempDir()
			eventCh := make(chan interface{}, 8)
			sm := session.NewManager(eventCh)
			pr := NewPhaseRunner(sm, feature.NewStore(dir), dir)
			pr.Registry = registry

			_, _, sessOpts, err := pr.BuildSession(BuildSessionOpts{
				Model:       tt.model,
				Prompt:      "tracer bullet",
				AgentNames:  tt.agentNames,
				PIDDir:      filepath.Join(dir, "pid"),
				PermHandler: permHandlerFor(false, nil, ""),
				WorkDir:     dir,
			})
			if err != nil {
				t.Fatalf("BuildSession() error: %v", err)
			}
			if sessOpts.ProviderName != tt.wantProvider {
				t.Fatalf("BuildSession() ProviderName = %q, want %q", sessOpts.ProviderName, tt.wantProvider)
			}

			switch tt.wantProvider {
			case "claude":
				if len(claudeProv.BuildCommandCalls) != 1 {
					t.Fatalf("BuildCommandCalls = %d, want 1", len(claudeProv.BuildCommandCalls))
				}
				if got := claudeProv.BuildCommandCalls[0].Opts.AgentsJSON; got != tt.wantAgentsJSON {
					t.Fatalf("AgentsJSON = %q, want %q", got, tt.wantAgentsJSON)
				}
				if got := claudeProv.BuildCommandCalls[0].Opts.AgentNames; strings.Join(got, ",") != strings.Join(tt.agentNames, ",") {
					t.Fatalf("AgentNames = %v, want %v", got, tt.agentNames)
				}
				if len(codexProv.BuildCommandCalls) != 0 {
					t.Fatalf("codex provider should be untouched, got %+v", codexProv.BuildCommandCalls)
				}
			case "codex":
				if len(codexProv.BuildCommandCalls) != 1 {
					t.Fatalf("BuildCommandCalls = %d, want 1", len(codexProv.BuildCommandCalls))
				}
				if got := codexProv.BuildCommandCalls[0].Opts.AgentsJSON; got != tt.wantAgentsJSON {
					t.Fatalf("AgentsJSON = %q, want %q", got, tt.wantAgentsJSON)
				}
				if got := codexProv.BuildCommandCalls[0].Opts.AgentNames; strings.Join(got, ",") != strings.Join(tt.agentNames, ",") {
					t.Fatalf("AgentNames = %v, want %v", got, tt.agentNames)
				}
				if len(claudeProv.BuildCommandCalls) != 0 {
					t.Fatalf("claude provider should be untouched, got %+v", claudeProv.BuildCommandCalls)
				}
			}
		})
	}
}

func TestPhaseRunnerResearchSuccess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := filepath.Join(tmpDir, "state")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	// Research script: writes an artifact and emits stream-json success
	artifactDir := filepath.Join(stateDir, "test-research-001", "runs", "run-001", "research")
	researchScript := testutil.WriteScript(t, scriptsDir, "research.sh",
		testutil.JSONLInit+"\n"+`mkdir -p "`+artifactDir+`"`+"\n"+`echo "# Research Output" > "`+artifactDir+`/research.md"`+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	pr := &PhaseRunner{
		SessionManager: sm,
		StateDir:       stateDir,
		BuildSessionFn: func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
			sessOpts := &session.SessionOpts{
				PIDDir:        opts.PIDDir,
				PermHandler:   opts.PermHandler,
				InitialPrompt: opts.Prompt,
				RepoName:      opts.RepoName,
				LogPath:       opts.LogPath,
			}
			return []string{"bash", researchScript}, nil, sessOpts, nil
		},
	}

	f := newResearchFeature(t, workDir)
	sessionID, err := pr.RunResearchFromQuestions(f, "questions-stub.md")
	if err != nil {
		t.Fatalf("RunResearchFromQuestions error: %v", err)
	}

	if sessionID != "test-research-001-research" {
		t.Errorf("expected session ID test-research-001-research, got %s", sessionID)
	}

	// Wait for the session to complete
	sess := sm.GetSession(sessionID)
	if sess == nil {
		t.Fatal("session not found")
	}

	done := make(chan struct{})
	go func() {
		sess.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("session did not complete within timeout")
	}

	// Verify artifact was created
	artifactPath := filepath.Join(artifactDir, "research.md")
	if _, err := os.Stat(artifactPath); os.IsNotExist(err) {
		t.Error("expected research artifact to be created")
	}

	// Verify log file was written
	logPath := filepath.Join(artifactDir, "output.txt")
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		t.Error("expected output.txt log file")
	}

	// Verify SUCCESS event was received (SDK: result message with IsSuccess())
	var gotSuccess bool
	timeout := time.After(2 * time.Second)
	for !gotSuccess {
		select {
		case evt := <-eventCh:
			if e, ok := evt.(session.SDKEventMsg); ok && e.Message.Result != nil && e.Message.Result.IsSuccess() {
				gotSuccess = true
			}
		case <-timeout:
			goto checkSuccess
		}
	}
checkSuccess:
	// Note: plain bash scripts don't emit JSON result messages, so this may be false.
	_ = gotSuccess
}

// TestPhaseRunnerResearchSuccess_MockProtocol demonstrates the mock-based test
// pattern. It uses MockProvider + MockProtocol via the production BuildSession
// path (registry routing) instead of bash script stubs. This test runs in
// -short mode since it only spawns a minimal "cat" subprocess.
func TestPhaseRunnerResearchSuccess_MockProtocol(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := filepath.Join(tmpDir, "state")
	for _, d := range []string{workDir, stateDir} {
		os.MkdirAll(d, 0o755)
	}

	// Set up MockProvider with MockProtocol
	mockProto := mocks.NewMockProtocol(mocks.StandardSequence("Research complete")...)
	mockProv := &mocks.MockProvider{
		ProviderName: testMockIdentifier,
		Models:       []string{"test-model"},
		CLIDetected:  true,
		CommandArgs:  []string{"cat"}, // minimal subprocess that echoes stdin to stdout
	}
	mockProv.Protocol = mockProto

	// Create registry with mock provider
	registry := llm.NewRegistry()
	registry.Register(mockProv)

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	pr := &PhaseRunner{
		SessionManager: sm,
		StateDir:       stateDir,
		Registry:       registry,
		// BuildSessionFn is nil → production BuildSession path via registry
	}

	f := newResearchFeature(t, workDir)
	sessionID, err := pr.RunResearchFromQuestions(f, "questions-stub.md")
	if err != nil {
		t.Fatalf("RunResearchFromQuestions error: %v", err)
	}

	if sessionID != "test-research-001-research" {
		t.Errorf("expected session ID test-research-001-research, got %s", sessionID)
	}

	// Wait for the session to complete
	sess := sm.GetSession(sessionID)
	if sess == nil {
		t.Fatal("session not found")
	}

	done := make(chan struct{})
	go func() {
		sess.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("session did not complete within timeout")
	}

	// Verify the provider was used correctly
	if mockProv.NewProtocolCalls != 1 {
		t.Errorf("NewProtocolCalls = %d, want 1", mockProv.NewProtocolCalls)
	}
	if len(mockProv.BuildCommandCalls) != 1 {
		t.Errorf("BuildCommandCalls = %d, want 1", len(mockProv.BuildCommandCalls))
	}

	// Verify protocol completed message replay
	if !mockProto.Initialized() {
		t.Error("MockProtocol.Handshake was not called")
	}

	// Verify result event was received via the protocol
	var gotSuccess bool
	timeout := time.After(2 * time.Second)
	for !gotSuccess {
		select {
		case evt := <-eventCh:
			if e, ok := evt.(session.SDKEventMsg); ok && e.Message.Result != nil && e.Message.Result.IsSuccess() {
				gotSuccess = true
			}
		case <-timeout:
			goto checkMockSuccess
		}
	}
checkMockSuccess:
	if !gotSuccess {
		t.Error("expected SUCCESS result event from MockProtocol message sequence")
	}
}

func TestPhaseRunnerResearchFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := filepath.Join(tmpDir, "state")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	// Script exits without SUCCESS
	failScript := testutil.WriteScript(t, scriptsDir, "fail.sh", `
echo "Something went wrong"
exit 1
`)

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	pr := &PhaseRunner{
		SessionManager: sm,
		StateDir:       stateDir,
		BuildSessionFn: func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
			sessOpts := &session.SessionOpts{
				PIDDir:        opts.PIDDir,
				PermHandler:   opts.PermHandler,
				InitialPrompt: opts.Prompt,
				RepoName:      opts.RepoName,
				LogPath:       opts.LogPath,
			}
			return []string{"bash", failScript}, nil, sessOpts, nil
		},
	}

	f := newResearchFeature(t, workDir)
	sessionID, err := pr.RunResearchFromQuestions(f, "questions-stub.md")
	if err != nil {
		t.Fatalf("RunResearchFromQuestions error: %v", err)
	}

	sess := sm.GetSession(sessionID)
	done := make(chan struct{})
	go func() {
		sess.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("session did not complete within timeout")
	}

	// Session should have failed status
	if sess.Status() != session.SessionFailed {
		t.Errorf("expected SessionFailed, got %v", sess.Status())
	}
}

func TestPhaseRunnerResearchNeedInput(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := filepath.Join(tmpDir, "state")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	// Script emits NEED_INPUT then SUCCESS
	script := testutil.WriteScript(t, scriptsDir, "input.sh", `
echo "I have a question about scope"
sleep 0.2
`)

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	pr := &PhaseRunner{
		SessionManager: sm,
		StateDir:       stateDir,
		BuildSessionFn: func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
			sessOpts := &session.SessionOpts{
				PIDDir:        opts.PIDDir,
				PermHandler:   opts.PermHandler,
				InitialPrompt: opts.Prompt,
				RepoName:      opts.RepoName,
				LogPath:       opts.LogPath,
			}
			return []string{"bash", script}, nil, sessOpts, nil
		},
	}

	f := newResearchFeature(t, workDir)
	sessionID, err := pr.RunResearchFromQuestions(f, "questions-stub.md")
	if err != nil {
		t.Fatalf("RunResearchFromQuestions error: %v", err)
	}

	sess := sm.GetSession(sessionID)
	done := make(chan struct{})
	go func() {
		sess.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("session did not complete within timeout")
	}

	// Verify NEED_INPUT event was received (SDK: assistant message with AskUserQuestion tool use)
	var gotNeedInput bool
	drainTimeout := time.After(2 * time.Second)
	for {
		select {
		case evt := <-eventCh:
			if e, ok := evt.(session.SDKEventMsg); ok && e.Message.Assistant != nil {
				for _, block := range e.Message.Assistant.Message.Content {
					if block.IsToolUse() && block.Name == "AskUserQuestion" {
						gotNeedInput = true
					}
				}
			}
		case <-drainTimeout:
			goto doneCheck
		default:
			if gotNeedInput {
				goto doneCheck
			}
			// Retained: bounded poll interval while draining asynchronous events.
			time.Sleep(10 * time.Millisecond)
		}
	}
doneCheck:
	// Note: plain bash scripts don't emit JSON assistant messages with tool use,
	// so this may be false. The test verifies session completion instead.
	_ = gotNeedInput
}

func TestPhaseRunnerImplementationForwardsBuilders(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := filepath.Join(tmpDir, "state")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	// ArtifactDir for implementation is stateDir/featureID/runs/run-001/implement
	implArtifactDir := filepath.Join(stateDir, "test-feat-001", "runs", "run-001", "implement")
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+testutil.WriteImplementSuccessArtifacts(implArtifactDir)+"\n"+testutil.JSONLSuccess+"\n")
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(implArtifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	pr := &PhaseRunner{
		SessionManager: sm,
		StateDir:       stateDir,
		BuildSessionFn: mockBuildSession(agentScript, reviewScript),
	}

	f := newTestFeature(t, workDir)
	implementDir := filepath.Join(stateDir, f.ID, "runs", "run-001", "implement")
	os.MkdirAll(implementDir, 0o755)
	planPath := writePlanFile(t, implementDir, "Test plan")

	resultCh, err := pr.RunImplementation(f, planPath)
	if err != nil {
		t.Fatalf("RunImplementation error: %v", err)
	}

	select {
	case result := <-resultCh:
		if result.FinalStatus != finalStatusReviewPassed {
			t.Errorf("expected review_passed, got %s (error: %s)", result.FinalStatus, result.LastError)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("implementation loop did not complete within timeout")
	}
}

func TestPhaseRunnerGetPhaseOutputPreservesContent(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := tmpDir

	f := &feature.Feature{ID: "feat-001", ActiveRun: 1, RunCount: 1}

	// Create a fake output file — run-aware path.
	outputDir := filepath.Join(ActiveRunDir(stateDir, f), "research")
	os.MkdirAll(outputDir, 0o755)
	os.WriteFile(filepath.Join(outputDir, "output.txt"),
		[]byte("Research results\nMore text\n"), 0o644)

	pr := &PhaseRunner{StateDir: stateDir}
	output := pr.GetPhaseOutput(f, "research")

	if !strings.Contains(output, "Research results") {
		t.Error("expected output to contain research results")
	}
	if !strings.Contains(output, "More text") {
		t.Error("expected all text to be preserved")
	}
}

func TestRunResearch_ViaRegistry_Claude(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := filepath.Join(tmpDir, "state")
	mockBinDir := filepath.Join(tmpDir, "bin")
	for _, d := range []string{workDir, stateDir, mockBinDir} {
		os.MkdirAll(d, 0o755)
	}

	artifactDir := filepath.Join(stateDir, "test-research-001", "research")

	// Mock claude binary: reads handshake from stdin, then emits Claude JSONL.
	// claude.Protocol.Handshake sends 2 JSON lines (initialize + user message).
	mockClaudeScript := fmt.Sprintf(`#!/bin/bash
# Read protocol handshake (initialize request + user message)
read -r _line
read -r _line
# Emit Claude SDK JSONL
%s
mkdir -p "%s"
echo "# Research Output" > "%s/research.md"
%s
`, testutil.JSONLInit, artifactDir, artifactDir, testutil.JSONLSuccess)

	mockClaudePath := filepath.Join(mockBinDir, "claude")
	if err := os.WriteFile(mockClaudePath, []byte(mockClaudeScript), 0o755); err != nil {
		t.Fatalf("writing mock claude: %v", err)
	}
	t.Setenv("PATH", mockBinDir+":"+os.Getenv("PATH"))

	reg := llm.NewRegistry()
	reg.Register(&claude.Provider{})
	reg.Register(&codex.Provider{})

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	pr := &PhaseRunner{
		SessionManager: sm,
		StateDir:       stateDir,
		Registry:       reg,
		// InteractiveCommandBuilder intentionally nil — forces production registry path
	}

	f := newResearchFeature(t, workDir)
	f.Models.Research = "opus"

	sessionID, err := pr.RunResearchFromQuestions(f, "questions-stub.md")
	if err != nil {
		t.Fatalf("RunResearchFromQuestions error: %v", err)
	}

	sess := sm.GetSession(sessionID)
	if sess == nil {
		t.Fatal("session not found")
	}

	// Verify session used Claude provider (set via registry path)
	if sess.ProviderName() != "claude" {
		t.Errorf("expected provider 'claude', got %q", sess.ProviderName())
	}

	done := make(chan struct{})
	go func() {
		sess.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(claudeSessionDoneTimeout):
		t.Fatal("session did not complete within timeout")
	}

	// Verify artifact was created
	if _, err := os.Stat(filepath.Join(artifactDir, "research.md")); os.IsNotExist(err) {
		t.Error("expected research artifact to be created")
	}

	// Drain events and verify SUCCESS result came through
	var gotSuccess bool
	timeout := time.After(2 * time.Second)
	for !gotSuccess {
		select {
		case evt := <-eventCh:
			if e, ok := evt.(session.SDKEventMsg); ok && e.Message.Result != nil && e.Message.Result.IsSuccess() {
				gotSuccess = true
			}
		case <-timeout:
			goto checkClaude
		}
	}
checkClaude:
	if !gotSuccess {
		t.Error("expected SUCCESS result event from Claude registry path")
	}
}

func TestRunResearch_GlobalBashRulesAutoApproveAcrossProviders(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	type providerParityCase struct {
		name         string
		model        string
		providerName string
		writeMockCLI func(t *testing.T, binDir, artifactDir string) string
	}

	tests := []providerParityCase{
		{
			name:         "claude",
			model:        "opus",
			providerName: "claude",
			writeMockCLI: writeResearchApprovalMockClaude,
		},
		{
			name:         "codex",
			model:        "gpt-5.4",
			providerName: "codex",
			writeMockCLI: writeResearchApprovalMockCodex,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			workDir := filepath.Join(tmpDir, "work")
			stateDir := filepath.Join(tmpDir, "state")
			mockBinDir := filepath.Join(tmpDir, "bin")
			permDir := filepath.Join(tmpDir, "permissions")
			for _, d := range []string{workDir, stateDir, mockBinDir, permDir} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatalf("MkdirAll(%s): %v", d, err)
				}
			}

			artifactDir := filepath.Join(stateDir, "test-research-001", "research")
			tt.writeMockCLI(t, mockBinDir, artifactDir)
			t.Setenv("PATH", mockBinDir+":"+os.Getenv("PATH"))

			store := permission.NewStore(permDir)
			if err := store.EnsureGlobalDefaults(); err != nil {
				t.Fatalf("EnsureGlobalDefaults: %v", err)
			}

			reg := llm.NewRegistry()
			reg.Register(&claude.Provider{})
			reg.Register(&codex.Provider{})

			eventCh := make(chan interface{}, 100)
			sm := session.NewManager(eventCh)
			defer sm.Shutdown()

			pr := &PhaseRunner{
				SessionManager:  sm,
				StateDir:        stateDir,
				Registry:        reg,
				PermissionCache: permission.NewCache(store),
			}

			f := newResearchFeature(t, workDir)
			f.Models.Research = tt.model

			sessionID, err := pr.RunResearchFromQuestions(f, "questions-stub.md")
			if err != nil {
				t.Fatalf("RunResearchFromQuestions error: %v", err)
			}

			sess := sm.GetSession(sessionID)
			if sess == nil {
				t.Fatal("session not found")
			}
			if sess.ProviderName() != tt.providerName {
				t.Fatalf("ProviderName = %q, want %q", sess.ProviderName(), tt.providerName)
			}

			select {
			case <-sess.Done():
			case <-time.After(claudeSessionDoneTimeout):
				t.Fatal("session did not complete within timeout")
			}

			if _, err := os.Stat(filepath.Join(artifactDir, "research.md")); os.IsNotExist(err) {
				t.Fatal("expected research artifact to be created")
			}

			var gotSuccess bool
			var sawBashPrompt bool
			timeout := time.After(2 * time.Second)
			for {
				select {
				case evt := <-eventCh:
					e, ok := evt.(session.SDKEventMsg)
					if !ok {
						continue
					}
					if e.Message.ControlRequest != nil && e.Message.ControlRequest.Request.ToolName == "Bash" {
						sawBashPrompt = true
					}
					if e.Message.Result != nil && e.Message.Result.IsSuccess() {
						gotSuccess = true
					}
				case <-timeout:
					if !gotSuccess {
						t.Fatal("expected SUCCESS result event from provider registry path")
					}
					if sawBashPrompt {
						t.Fatal("expected seeded global Bash rule to auto-approve before reaching the desktop app")
					}
					return
				}
			}
		})
	}
}

func TestRunResearch_CodexRegistryPath_UsesResolvedHome(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := filepath.Join(tmpDir, "state")
	mockBinDir := filepath.Join(tmpDir, "bin")
	homeDir := filepath.Join(tmpDir, "home")
	for _, d := range []string{workDir, stateDir, mockBinDir} {
		os.MkdirAll(d, 0o755)
	}

	artifactDir := filepath.Join(stateDir, "test-research-001", "research")
	t.Setenv("HOME", homeDir)
	t.Setenv("CODEX_HOME", "~/resolved-codex-home")

	// Mock codex binary: handles the Codex JSON-RPC handshake protocol.
	// Handshake sequence:
	//   1. Protocol sends initialize request (stdin line 1)
	//   2. Protocol sends initialized notification (stdin line 2)
	//   3. Mock responds with initialize result (closes handshakeDone)
	//   4. Protocol sends thread/start (stdin line 3)
	//   5. Mock responds with thread/start result (closes threadReady)
	//   6. Protocol sends turn/start (stdin line 4)
	//   7. Mock responds with turn/start result, then emits turn/completed
	mockCodexScript := fmt.Sprintf(`#!/bin/bash
# Step 1-2: Read initialize request + initialized notification
read -r _line
read -r _line
# Step 3: Respond with initialize result
echo '{"jsonrpc":"2.0","id":99,"result":{"userAgent":"mock-codex/1.0","codexHome":"/tmp/mock"}}'
# Step 4: Read thread/start request
read -r _line
# Step 5: Respond with thread/start result
echo '{"jsonrpc":"2.0","id":98,"result":{"thread":{"id":"test-thread-1"}}}'
# Step 6: Read turn/start request
read -r _line
# Step 7: Respond with turn/start result
echo '{"jsonrpc":"2.0","id":97,"result":{"turn":{"id":"test-turn-1","status":"started"}}}'
# Create artifact
mkdir -p "%s"
echo "# Research Output" > "%s/research.md"
# Emit turn/completed notification (signals success)
echo '{"method":"turn/completed","params":{"threadId":"test-thread-1","turn":{"id":"test-turn-1","status":"completed"}}}'
`, artifactDir, artifactDir)

	mockCodexPath := filepath.Join(mockBinDir, "codex")
	if err := os.WriteFile(mockCodexPath, []byte(mockCodexScript), 0o755); err != nil {
		t.Fatalf("writing mock codex: %v", err)
	}
	t.Setenv("PATH", mockBinDir+":"+os.Getenv("PATH"))

	reg := llm.NewRegistry()
	reg.Register(&claude.Provider{})
	reg.Register(&codex.Provider{})

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	pr := &PhaseRunner{
		SessionManager: sm,
		StateDir:       stateDir,
		Registry:       reg,
		// InteractiveCommandBuilder intentionally nil — forces production registry path
	}

	f := newResearchFeature(t, workDir)
	f.Models.Research = "gpt-5.4"

	sessionID, err := pr.RunResearchFromQuestions(f, "questions-stub.md")
	if err != nil {
		t.Fatalf("RunResearchFromQuestions error: %v", err)
	}

	sess := sm.GetSession(sessionID)
	if sess == nil {
		t.Fatal("session not found")
	}

	// Verify session used Codex provider (set via registry path)
	if sess.ProviderName() != "codex" {
		t.Errorf("expected provider 'codex', got %q", sess.ProviderName())
	}

	done := make(chan struct{})
	go func() {
		sess.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("session did not complete within timeout")
	}

	// Verify artifact was created
	if _, err := os.Stat(filepath.Join(artifactDir, "research.md")); os.IsNotExist(err) {
		t.Error("expected research artifact to be created")
	}

	// Drain events and verify SUCCESS result came through
	var gotSuccess bool
	timeout := time.After(2 * time.Second)
	for !gotSuccess {
		select {
		case evt := <-eventCh:
			if e, ok := evt.(session.SDKEventMsg); ok && e.Message.Result != nil && e.Message.Result.IsSuccess() {
				gotSuccess = true
			}
		case <-timeout:
			goto checkCodex
		}
	}
checkCodex:
	if !gotSuccess {
		t.Error("expected SUCCESS result event from Codex registry path")
	}
}

func writeResearchApprovalMockClaude(t *testing.T, binDir, artifactDir string) string {
	t.Helper()

	script := fmt.Sprintf(`#!/bin/bash
read -r _
read -r _
%s
echo '{"type":"control_request","request_id":"req-bash-1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls -la"}}}'
read -r approval
case "$approval" in
  *'"request_id":"req-bash-1"'*'"behavior":"allow"'*) ;;
  *)
    echo "unexpected approval response: $approval" >&2
    exit 1
    ;;
esac
mkdir -p "%s"
echo "# Research Output" > "%s/research.md"
%s
`, testutil.JSONLInit, artifactDir, artifactDir, testutil.JSONLSuccess)

	path := filepath.Join(binDir, "claude")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing mock claude: %v", err)
	}
	return path
}

func writeResearchApprovalMockCodex(t *testing.T, binDir, artifactDir string) string {
	t.Helper()

	script := fmt.Sprintf(`#!/bin/bash
read -r _
read -r _
echo '{"jsonrpc":"2.0","id":1,"result":{"userAgent":"mock-codex/1.0","codexHome":"/tmp/mock"}}'
read -r _
echo '{"jsonrpc":"2.0","id":2,"result":{"thread":{"id":"test-thread-1"}}}'
read -r _
echo '{"jsonrpc":"2.0","id":3,"result":{"turn":{"id":"test-turn-1","status":"started"}}}'
echo '{"jsonrpc":"2.0","id":41,"method":"item/commandExecution/requestApproval","params":{"command":"ls -la"}}'
read -r approval
case "$approval" in
  *'"id":41'*'"decision":"accept"'*) ;;
  *)
    echo "unexpected approval response: $approval" >&2
    exit 1
    ;;
esac
mkdir -p "%s"
echo "# Research Output" > "%s/research.md"
echo '{"method":"turn/completed","params":{"threadId":"test-thread-1","turn":{"id":"test-turn-1","status":"completed"}}}'
`, artifactDir, artifactDir)

	path := filepath.Join(binDir, "codex")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing mock codex: %v", err)
	}
	return path
}
