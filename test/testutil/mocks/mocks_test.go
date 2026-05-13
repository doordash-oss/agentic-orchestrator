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

package mocks_test

import (
	"errors"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// Compile-time interface checks.
var _ llm.Protocol = (*mocks.MockProtocol)(nil)
var _ llm.LLMProvider = (*mocks.MockProvider)(nil)
var _ llm.PromptAdapter = (*mocks.MockProvider)(nil)
var _ llm.CostCalculator = (*mocks.MockProvider)(nil)
var _ llm.CatalogProvider = (*mocks.MockProvider)(nil)
var _ session.SessionView = (*mocks.MockSessionView)(nil)

// Feature ports
var _ ports.FeatureStore = (*mocks.MockFeatureStore)(nil)
var _ ports.FeatureLifecycle = (*mocks.MockFeatureLifecycle)(nil)

// Session ports
var _ ports.SessionManager = (*mocks.MockSessionManager)(nil)

// Git ports
var _ ports.Publisher = (*mocks.MockPublisher)(nil)
var _ ports.DiffOperator = (*mocks.MockDiffOperator)(nil)
var _ ports.RebaseOperator = (*mocks.MockRebaseOperator)(nil)
var _ ports.CrossRefOperator = (*mocks.MockCrossRefOperator)(nil)
var _ ports.ReviewCommentOperator = (*mocks.MockReviewCommentOperator)(nil)
var _ ports.WorktreeOperator = (*mocks.MockWorktreeOperator)(nil)
var _ ports.BranchOperator = (*mocks.MockBranchOperator)(nil)

// Agent ports
var _ ports.CommandRunner = (*mocks.MockCommandRunner)(nil)

// Config ports
var _ ports.ConfigPersistence = (*mocks.MockConfigPersistence)(nil)

// --- MockProtocol tests ---

func TestMockProtocolReplaysMessages(t *testing.T) {
	msgs := mocks.StandardSequence("hello world")
	proto := mocks.NewMockProtocol(msgs...)

	for i, want := range msgs {
		got, err := proto.ParseLine([]byte("line"))
		if err != nil {
			t.Fatalf("ParseLine(%d): unexpected error: %v", i, err)
		}
		if len(got) != 1 {
			t.Fatalf("ParseLine(%d): expected 1 message, got %d", i, len(got))
		}
		if got[0].Type != want.Type {
			t.Errorf("ParseLine(%d): type = %q, want %q", i, got[0].Type, want.Type)
		}
	}

	// Past the end returns nil.
	got, err := proto.ParseLine([]byte("extra"))
	if err != nil {
		t.Fatalf("ParseLine past end: unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("ParseLine past end: expected nil, got %v", got)
	}
}

func TestMockProtocolStandardSequence(t *testing.T) {
	seq := mocks.StandardSequence("test output")
	if len(seq) != 3 {
		t.Fatalf("StandardSequence: expected 3 messages, got %d", len(seq))
	}
	if seq[0].Type != "system" || seq[0].Init == nil {
		t.Error("StandardSequence[0]: expected system init message")
	}
	if seq[1].Type != "assistant" || seq[1].Assistant == nil {
		t.Error("StandardSequence[1]: expected assistant message")
	}
	if seq[2].Type != "result" || seq[2].Result == nil {
		t.Error("StandardSequence[2]: expected result message")
	}
	if !seq[2].Result.IsSuccess() {
		t.Error("StandardSequence[2]: expected success result")
	}
}

func TestMockProtocolEmptySequence(t *testing.T) {
	proto := mocks.NewMockProtocol()
	got, err := proto.ParseLine([]byte("line"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for empty sequence, got %v", got)
	}
}

// --- MockProvider tests ---

func TestMockProviderMatchesModel(t *testing.T) {
	p := &mocks.MockProvider{
		Models: []string{"gpt-4", "opus"},
	}

	if !p.MatchesModel("gpt-4") {
		t.Error("MatchesModel(gpt-4) = false, want true")
	}
	if !p.MatchesModel("opus") {
		t.Error("MatchesModel(opus) = false, want true")
	}
	if p.MatchesModel("unknown") {
		t.Error("MatchesModel(unknown) = true, want false")
	}
}

func TestMockProviderBuildCommandRecordsCalls(t *testing.T) {
	p := &mocks.MockProvider{
		CommandArgs: []string{"mock-cli", "--flag"},
		CommandEnv:  []string{"KEY=VAL"},
	}

	opts := llm.CommandBuildOpts{Model: "test", Prompt: "hello"}
	args, env, err := p.BuildCommand(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(args) != 2 || args[0] != "mock-cli" {
		t.Errorf("args = %v, want [mock-cli --flag]", args)
	}
	if len(env) != 1 || env[0] != "KEY=VAL" {
		t.Errorf("env = %v, want [KEY=VAL]", env)
	}
	if len(p.BuildCommandCalls) != 1 {
		t.Fatalf("BuildCommandCalls = %d, want 1", len(p.BuildCommandCalls))
	}
	if p.BuildCommandCalls[0].Opts.Model != "test" {
		t.Errorf("recorded model = %q, want %q", p.BuildCommandCalls[0].Opts.Model, "test")
	}
}

func TestMockProviderNewProtocolReturnsConfigured(t *testing.T) {
	mockProto := mocks.NewMockProtocol(mocks.InitMessage())
	p := &mocks.MockProvider{Protocol: mockProto}

	got := p.NewProtocol(llm.ProtocolOpts{})
	if got != mockProto {
		t.Error("NewProtocol did not return the configured MockProtocol")
	}
	if p.NewProtocolCalls != 1 {
		t.Errorf("NewProtocolCalls = %d, want 1", p.NewProtocolCalls)
	}
}

func TestMockProviderNewProtocolDefaultFallback(t *testing.T) {
	p := &mocks.MockProvider{}
	got := p.NewProtocol(llm.ProtocolOpts{})
	if got == nil {
		t.Fatal("NewProtocol returned nil when Protocol is nil (should return default)")
	}
}

func TestMockProviderRegistration(t *testing.T) {
	p := &mocks.MockProvider{
		ProviderName: "mock",
		Models:       []string{"test-model"},
		CLIDetected:  true,
	}

	registry := llm.NewRegistry()
	registry.Register(p)

	got, err := registry.ForModel("test-model")
	if err != nil {
		t.Fatalf("ForModel: %v", err)
	}
	if got.Name() != "mock" {
		t.Errorf("ForModel returned provider %q, want %q", got.Name(), "mock")
	}
}

func TestMockProviderCostCalculator(t *testing.T) {
	p := &mocks.MockProvider{CostPerCall: 0.05, ContextWindow: 128000}
	if p.ComputeCost("model", 100, 200) != 0.05 {
		t.Error("ComputeCost did not return configured CostPerCall")
	}
	if p.ContextWindowForModel("model") != 128000 {
		t.Error("ContextWindowForModel did not return configured ContextWindow")
	}
}

func TestMockProviderPromptAdapter(t *testing.T) {
	p := &mocks.MockProvider{QuestionsClause: "IMPORTANT: Ask questions"}
	if p.AskingQuestionsClause() != "IMPORTANT: Ask questions" {
		t.Error("AskingQuestionsClause did not return configured value")
	}
}

func TestMockProvider_EnvVarsToExclude(t *testing.T) {
	p := &mocks.MockProvider{EnvVarsExclude: []string{"FOO"}}
	if got := p.EnvVarsToExclude(); len(got) != 1 || got[0] != "FOO" {
		t.Errorf("EnvVarsToExclude() = %v, want [FOO]", got)
	}
	// Default returns nil
	p2 := &mocks.MockProvider{}
	if got := p2.EnvVarsToExclude(); got != nil {
		t.Errorf("EnvVarsToExclude() default = %v, want nil", got)
	}
}

// --- MockSessionView tests ---

func TestMockSessionViewReturnsConfiguredValues(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-1", "feat-1")
	mv.ModelVal = "opus"
	mv.IterationVal = 3
	mv.ProviderNameVal = "claude"
	mv.RepoNameVal = "test-repo"

	if mv.ID() != "sess-1" {
		t.Errorf("ID() = %q, want %q", mv.ID(), "sess-1")
	}
	if mv.FeatureID() != "feat-1" {
		t.Errorf("FeatureID() = %q, want %q", mv.FeatureID(), "feat-1")
	}
	if mv.Model() != "opus" {
		t.Errorf("Model() = %q, want %q", mv.Model(), "opus")
	}
	if mv.Iteration() != 3 {
		t.Errorf("Iteration() = %d, want 3", mv.Iteration())
	}
	if mv.ProviderName() != "claude" {
		t.Errorf("ProviderName() = %q, want %q", mv.ProviderName(), "claude")
	}
	if mv.RepoName() != "test-repo" {
		t.Errorf("RepoName() = %q, want %q", mv.RepoName(), "test-repo")
	}
	if mv.Status() != session.SessionRunning {
		t.Errorf("Status() = %v, want SessionRunning", mv.Status())
	}
	if !mv.IsActive() {
		t.Error("IsActive() = false, want true (default)")
	}
}

func TestMockSessionViewRecordsInteractions(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-1", "feat-1")

	if err := mv.SendUserMessage("hello"); err != nil {
		t.Fatalf("SendUserMessage: %v", err)
	}
	if len(mv.SentMessages) != 1 || mv.SentMessages[0] != "hello" {
		t.Errorf("SentMessages = %v, want [hello]", mv.SentMessages)
	}

	if err := mv.RespondToControl("req-1", true, "approved"); err != nil {
		t.Fatalf("RespondToControl: %v", err)
	}
	if len(mv.ControlResponses) != 1 {
		t.Fatalf("ControlResponses = %d, want 1", len(mv.ControlResponses))
	}
	if !mv.ControlResponses[0].Allow {
		t.Error("ControlResponses[0].Allow = false, want true")
	}

	mv.ClearPendingQuestion("req-2")
	if len(mv.ClearedQuestions) != 1 || mv.ClearedQuestions[0] != "req-2" {
		t.Errorf("ClearedQuestions = %v, want [req-2]", mv.ClearedQuestions)
	}

	mv.ResetWaitingStatus()
	if mv.ResetWaitingCalled != 1 {
		t.Errorf("ResetWaitingCalled = %d, want 1", mv.ResetWaitingCalled)
	}

	if err := mv.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if mv.StopCalled != 1 {
		t.Errorf("StopCalled = %d, want 1", mv.StopCalled)
	}

	mv.Wait()
	if mv.WaitCalled != 1 {
		t.Errorf("WaitCalled = %d, want 1", mv.WaitCalled)
	}
}

func TestMockSessionViewDefaultChannels(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-1", "feat-1")

	// Channels should be initialized and non-nil.
	if mv.StatusCh() == nil {
		t.Error("StatusCh() is nil")
	}
	if mv.AttachCh() == nil {
		t.Error("AttachCh() is nil")
	}
	if mv.Done() == nil {
		t.Error("Done() is nil")
	}

	// Should be able to send to buffered channels without blocking.
	mv.StatusChVal <- "test-status"
	select {
	case got := <-mv.StatusCh():
		if got != "test-status" {
			t.Errorf("StatusCh received %q, want %q", got, "test-status")
		}
	default:
		t.Error("StatusCh was empty after send")
	}
}

func TestMockSessionViewStopReturnsError(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-1", "feat-1")
	wantErr := errors.New("session not running")
	mv.StopError = wantErr

	err := mv.Stop()
	if err != wantErr {
		t.Errorf("Stop() = %v, want %v", err, wantErr)
	}
}

func TestMockSessionViewAccumulatedUsage(t *testing.T) {
	t.Run("returns configured usage values", func(t *testing.T) {
		mv := mocks.NewMockSessionView("sess-1", "feat-1")
		mv.AccumulatedUsageVal = llm.Usage{
			InputTokens:          500,
			OutputTokens:         200,
			CacheReadInputTokens: 100,
		}

		got := mv.AccumulatedUsage()
		if got.InputTokens != 500 {
			t.Errorf("InputTokens = %d, want 500", got.InputTokens)
		}
		if got.OutputTokens != 200 {
			t.Errorf("OutputTokens = %d, want 200", got.OutputTokens)
		}
		if got.CacheReadInputTokens != 100 {
			t.Errorf("CacheReadInputTokens = %d, want 100", got.CacheReadInputTokens)
		}
	})

	t.Run("returns zero value by default", func(t *testing.T) {
		mv := mocks.NewMockSessionView("sess-2", "feat-2")

		got := mv.AccumulatedUsage()
		if got != (llm.Usage{}) {
			t.Errorf("AccumulatedUsage() = %+v, want zero llm.Usage", got)
		}
	})
}
