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

package autoreview

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

type reviewerCapableProvider struct {
	testutil.FakeClaudeProvider
	name    string
	catalog []llm.ModelInfo
}

func (p reviewerCapableProvider) Name() string { return p.name }

func (p reviewerCapableProvider) ModelCatalog() []llm.ModelInfo {
	return append([]llm.ModelInfo(nil), p.catalog...)
}

func (p reviewerCapableProvider) AvailableModels() []string {
	models := make([]string, 0, len(p.catalog))
	for _, model := range p.catalog {
		models = append(models, model.ID)
	}
	return models
}

func (p reviewerCapableProvider) MatchesModel(selector string) bool {
	for _, model := range p.catalog {
		if strings.EqualFold(model.ID, selector) {
			return true
		}
		for _, alias := range model.Aliases {
			if strings.EqualFold(alias, selector) {
				return true
			}
		}
	}
	return false
}

func (reviewerCapableProvider) SupportsNativeToollessReview() bool { return true }

type reviewerIncapableProvider struct {
	reviewerCapableProvider
}

func (reviewerIncapableProvider) SupportsNativeToollessReview() bool { return false }

func newReviewerRegistry(t *testing.T, providers ...llm.LLMProvider) *llm.Registry {
	t.Helper()
	registry := llm.NewRegistry()
	for _, provider := range providers {
		registry.Register(provider)
	}
	return registry
}

func reviewerProvider(t *testing.T, name string, catalog ...llm.ModelInfo) reviewerCapableProvider {
	t.Helper()
	return reviewerCapableProvider{
		FakeClaudeProvider: testutil.FakeClaudeProvider{Script: testutil.WriteFakeClaudeScript(t, testutil.FakeClaudeAllowScriptBody())},
		name:               name,
		catalog:            catalog,
	}
}

func TestParseDecision(t *testing.T) {
	cases := []struct {
		in     string
		want   Decision
		wantOK bool
	}{
		{"ALLOW", Allow, true},
		{"DEFER", Defer, true},
		{"  ALLOW  ", Allow, true},
		{"\n\tDEFER\n", Defer, true},
		{"allow", "", false},
		{"Allow", "", false},
		{"DEFER\nALLOW", "", false},
		{"ALLOW this command", "", false},
		{"```ALLOW```", "", false},
		{"", "", false},
		{"I will allow this", "", false},
		{"REFUSAL", "", false},
	}
	for _, c := range cases {
		got, ok := parseDecision(c.in)
		if got != c.want || ok != c.wantOK {
			t.Errorf("parseDecision(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.wantOK)
		}
	}
}

func TestReviewMessageRejectsUnexpectedNativeActivity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msg  llm.SDKMessage
	}{
		{name: "assistant tool use", msg: llm.SDKMessage{Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "tool_use", Name: "Bash"}}}}}},
		{name: "tool progress", msg: llm.SDKMessage{ToolProgress: &llm.ToolProgressMessage{}}},
		{name: "child task start", msg: llm.SDKMessage{TaskStarted: &llm.TaskStartedMessage{}}},
		{name: "file read", msg: llm.SDKMessage{FileReads: []llm.FileReadEvent{{}}}},
		{name: "file change", msg: llm.SDKMessage{FileChanges: []llm.FileChangeEvent{{}}}},
		{name: "extra user interaction", msg: llm.SDKMessage{User: &llm.UserMessage{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !unexpectedReviewActivity(tt.msg) {
				t.Errorf("unexpectedReviewActivity(%s) = false, want true", tt.name)
			}
		})
	}

	textOnly := llm.SDKMessage{Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "text", Text: "ALLOW"}}}}}
	if unexpectedReviewActivity(textOnly) {
		t.Error("unexpectedReviewActivity(text-only assistant) = true, want false")
	}
}

func TestReviewPromptContainsOnlyMinimalContext(t *testing.T) {
	prompt := reviewPrompt(ClassifyRequest{
		ToolName:      "Bash",
		Command:       "go test ./...",
		WorkDir:       "/tmp/work",
		WritableRoots: []string{"/tmp/work", "/tmp/out"},
	})
	for _, want := range []string{"Bash", "go test ./...", "/tmp/work", "/tmp/out", "ALLOW", "DEFER"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("reviewPrompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestBuildIsolatedEnvProviderValuesReplaceInheritedValues(t *testing.T) {
	t.Setenv("CODEX_HOME", "/user/codex")
	t.Setenv("OPENCODE_CONFIG", "/user/opencode.json")
	env := buildIsolatedEnv(testutil.FakeClaudeProvider{}, "/tmp/review", []string{
		"CODEX_HOME=/tmp/review/codex",
		"OPENCODE_CONFIG=/tmp/review/opencode.json",
	})
	for key, want := range map[string]string{
		"CLAUDE_CONFIG_DIR": "/tmp/review",
		"CODEX_HOME":        "/tmp/review/codex",
		"OPENCODE_CONFIG":   "/tmp/review/opencode.json",
	} {
		var values []string
		for _, kv := range env {
			if strings.HasPrefix(kv, key+"=") {
				values = append(values, strings.TrimPrefix(kv, key+"="))
			}
		}
		if len(values) != 1 || values[0] != want {
			t.Errorf("buildIsolatedEnv %s values = %v, want [%q]", key, values, want)
		}
	}
}

// fakeClaudeProvider, writeScript, and newFakeRegistry are consolidated in
// test/testutil/fakeclaude.go as FakeClaudeProvider, WriteFakeClaudeScript,
// and NewFakeClaudeRegistry. Script body helpers (AllowScriptBody, etc.) are
// also there.

func TestResolveReviewerAutomaticHaiku(t *testing.T) {
	script := testutil.WriteFakeClaudeScript(t, testutil.FakeClaudeAllowScriptBody())
	reg := testutil.NewFakeClaudeRegistry(t, script)
	r, ok := ResolveReviewer(reg, "")
	if !ok {
		t.Fatalf("ResolveReviewer(empty) = false, want true")
	}
	if r.Provider == nil || r.Provider.Name() != "claude" {
		t.Fatalf("ResolveReviewer provider = %v, want claude", r.Provider)
	}
	if r.Model != "haiku[200K]" {
		t.Fatalf("ResolveReviewer model = %q, want haiku[200K]", r.Model)
	}
}

func TestResolveReviewerAutomaticProviderAndModelOrder(t *testing.T) {
	claude := reviewerProvider(t, "claude",
		llm.ModelInfo{ID: "claude-haiku-large", Aliases: []string{"haiku"}, Category: "cheap", ContextWindow: 200_000},
		llm.ModelInfo{ID: "claude-haiku-small", Category: "cheap", ContextWindow: 100_000},
	)
	opencode := reviewerProvider(t, "opencode",
		llm.ModelInfo{ID: "google/gemini-flash", Category: "cheap", ContextWindow: 64_000},
	)
	codex := reviewerProvider(t, "codex",
		llm.ModelInfo{ID: "gpt-mini", Category: "cheap", ContextWindow: 32_000},
	)

	for _, providers := range [][]llm.LLMProvider{
		{codex, opencode, claude},
		{opencode, claude, codex},
		{claude, codex, opencode},
	} {
		registry := newReviewerRegistry(t, providers...)
		reviewer, ok := ResolveReviewer(registry, "")
		if !ok {
			t.Fatal("ResolveReviewer(empty) = false, want true")
		}
		if got, want := reviewer.Provider.Name(), "claude"; got != want {
			t.Errorf("ResolveReviewer(empty) provider = %q, want %q", got, want)
		}
		if got, want := reviewer.Model, "claude-haiku-small"; got != want {
			t.Errorf("ResolveReviewer(empty) model = %q, want %q", got, want)
		}
	}
}

func TestResolveReviewerAutomaticSkipsUnavailableAndIncapableProviders(t *testing.T) {
	claude := reviewerProvider(t, "claude",
		llm.ModelInfo{ID: "sonnet", Category: "balanced", ContextWindow: 200_000},
	)
	opencode := reviewerIncapableProvider{reviewerProvider(t, "opencode",
		llm.ModelInfo{ID: "anthropic/claude-haiku", Category: "cheap", ContextWindow: 200_000},
	)}
	codex := reviewerProvider(t, "codex",
		llm.ModelInfo{ID: "gpt-cheap-unknown", Category: "cheap"},
		llm.ModelInfo{ID: "gpt-cheap-known", Category: "cheap", ContextWindow: 64_000},
	)
	registry := newReviewerRegistry(t, claude, opencode, codex)

	reviewer, ok := ResolveReviewer(registry, "")
	if !ok {
		t.Fatal("ResolveReviewer(empty) = false, want Codex fallback")
	}
	if got, want := reviewer.Provider.Name(), "codex"; got != want {
		t.Errorf("ResolveReviewer(empty) provider = %q, want %q", got, want)
	}
	if got, want := reviewer.Model, "gpt-cheap-known"; got != want {
		t.Errorf("ResolveReviewer(empty) model = %q, want %q", got, want)
	}

	registry.RestrictToProviders([]llm.LLMProvider{claude, opencode})
	if _, ok := ResolveReviewer(registry, ""); ok {
		t.Error("ResolveReviewer(empty) = true, want false when every active provider misses")
	}
}

func TestResolveReviewerAutomaticProviderLocalPreferenceBands(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		catalog  []llm.ModelInfo
		want     string
	}{
		{
			name:     "claude haiku before other cheap",
			provider: "claude",
			catalog: []llm.ModelInfo{
				{ID: "cheap-small", Category: "cheap", ContextWindow: 16_000},
				{ID: "claude-haiku", Category: "cheap", ContextWindow: 200_000},
			},
			want: "claude-haiku",
		},
		{
			name:     "opencode haiku before flash before other cheap",
			provider: "opencode",
			catalog: []llm.ModelInfo{
				{ID: "vendor/cheap", Category: "cheap", ContextWindow: 8_000},
				{ID: "google/gemini-flash", Category: "cheap", ContextWindow: 16_000},
				{ID: "anthropic/claude-haiku", Category: "cheap", ContextWindow: 200_000},
			},
			want: "anthropic/claude-haiku",
		},
		{
			name:     "codex cheap before mini",
			provider: "codex",
			catalog: []llm.ModelInfo{
				{ID: "gpt-shrink", ContextWindow: 8_000},
				{ID: "gpt-cheap", Category: "cheap", ContextWindow: 200_000},
			},
			want: "gpt-cheap",
		},
		{
			name:     "canonical id breaks equal ties",
			provider: "codex",
			catalog: []llm.ModelInfo{
				{ID: "z-cheap", Category: "cheap", ContextWindow: 32_000},
				{ID: "a-cheap", Category: "cheap", ContextWindow: 32_000},
			},
			want: "a-cheap",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := reviewerProvider(t, tt.provider, tt.catalog...)
			reviewer, ok := ResolveReviewer(newReviewerRegistry(t, provider), "")
			if !ok {
				t.Fatal("ResolveReviewer(empty) = false, want true")
			}
			if got := reviewer.Model; got != tt.want {
				t.Errorf("ResolveReviewer(empty) model = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveReviewerExplicitExactProviderAndUniqueBareSelector(t *testing.T) {
	claude := reviewerProvider(t, "claude",
		llm.ModelInfo{ID: "claude-sonnet", Aliases: []string{"shared"}, Category: "balanced"},
	)
	opencode := reviewerProvider(t, "opencode",
		llm.ModelInfo{ID: "anthropic/claude-sonnet", Aliases: []string{"shared", "unique-open"}, Category: "balanced"},
	)
	registry := newReviewerRegistry(t, opencode, claude)

	reviewer, ok := ResolveReviewer(registry, "opencode:anthropic/claude-sonnet")
	if !ok || reviewer.Provider.Name() != "opencode" || reviewer.Model != "anthropic/claude-sonnet" {
		t.Errorf("ResolveReviewer(explicit OpenCode) = (%v, %v), want exact OpenCode model", reviewer, ok)
	}
	reviewer, ok = ResolveReviewer(registry, "unique-open")
	if !ok || reviewer.Provider.Name() != "opencode" || reviewer.Model != "anthropic/claude-sonnet" {
		t.Errorf("ResolveReviewer(unique bare) = (%v, %v), want canonical OpenCode model", reviewer, ok)
	}
	if _, ok := ResolveReviewer(registry, "shared"); ok {
		t.Error("ResolveReviewer(ambiguous bare) = true, want false")
	}
	if _, ok := ResolveReviewer(registry, "claude:removed"); ok {
		t.Error("ResolveReviewer(stale explicit) = true, want false")
	}
}

func TestResolveReviewerExplicitClaudeModel(t *testing.T) {
	script := testutil.WriteFakeClaudeScript(t, testutil.FakeClaudeAllowScriptBody())
	reg := testutil.NewFakeClaudeRegistry(t, script)
	r, ok := ResolveReviewer(reg, "haiku[200K]")
	if !ok || r.Model != "haiku[200K]" {
		t.Fatalf("ResolveReviewer(haiku[200K]) = (%v,%v), want (haiku[200K],true)", r, ok)
	}
}

func TestResolveReviewerNoClaude(t *testing.T) {
	reg := llm.NewRegistry()
	if _, ok := ResolveReviewer(reg, ""); ok {
		t.Fatalf("ResolveReviewer with no claude = true, want false")
	}
}

func TestResolveReviewerNonClaudeModelRejected(t *testing.T) {
	script := testutil.WriteFakeClaudeScript(t, testutil.FakeClaudeAllowScriptBody())
	reg := testutil.NewFakeClaudeRegistry(t, script)
	// A model the (claude-only) registry cannot resolve must not substitute.
	if _, ok := ResolveReviewer(reg, "sonnet-99"); ok {
		t.Fatalf("ResolveReviewer(unresolvable) = true, want false (no substitution)")
	}
}

func TestResolveReviewerStaleProviderQualifiedModelRejected(t *testing.T) {
	script := testutil.WriteFakeClaudeScript(t, testutil.FakeClaudeAllowScriptBody())
	reg := testutil.NewFakeClaudeRegistry(t, script)
	// A provider-qualified model that is not in the active catalog must not
	// create a reviewer. ResolveModel's explicit branch returns the bare
	// model even when it is not catalog-resolvable, so ResolveReviewer must
	// reject it to preserve normal human review.
	if _, ok := ResolveReviewer(reg, "claude:removed-model"); ok {
		t.Fatalf("ResolveReviewer(claude:removed-model) = true, want false (stale model not in catalog)")
	}
}

// oauthFakeClaudeProvider wraps FakeClaudeProvider but reports bare auth as
// unusable, simulating an OAuth-only Claude installation whose general
// readiness passes but whose auth cannot survive a --bare launch.
type oauthFakeClaudeProvider struct {
	testutil.FakeClaudeProvider
}

func (oauthFakeClaudeProvider) CheckBareAuth() bool { return false }

func TestResolveReviewerRejectsOAuthOnlyAuth(t *testing.T) {
	script := testutil.WriteFakeClaudeScript(t, testutil.FakeClaudeAllowScriptBody())
	reg := llm.NewRegistry()
	reg.Register(oauthFakeClaudeProvider{FakeClaudeProvider: testutil.FakeClaudeProvider{Script: script}})
	// An OAuth-only Claude installation is generally ready but --bare cannot
	// authenticate, so ResolveReviewer must reject it to avoid selecting a
	// reviewer that provider-fails on every eligible request.
	if _, ok := ResolveReviewer(reg, ""); ok {
		t.Fatalf("ResolveReviewer with OAuth-only auth = true, want false (bare auth unusable)")
	}
}

func TestClassifyAllow(t *testing.T) {
	script := testutil.WriteFakeClaudeScript(t, testutil.FakeClaudeAllowScriptBody())
	reg := testutil.NewFakeClaudeRegistry(t, script)
	reviewer, _ := ResolveReviewer(reg, "")
	got, ok := Classify(context.Background(), reviewer, ClassifyRequest{ToolName: "Bash", Command: "go test ./...", WorkDir: t.TempDir()})
	if !ok || got != Allow {
		t.Fatalf("Classify = (%q,%v), want (ALLOW,true)", got, ok)
	}
}

func TestClassifyDefer(t *testing.T) {
	script := testutil.WriteFakeClaudeScript(t, testutil.FakeClaudeDeferScriptBody())
	reg := testutil.NewFakeClaudeRegistry(t, script)
	reviewer, _ := ResolveReviewer(reg, "")
	got, ok := Classify(context.Background(), reviewer, ClassifyRequest{ToolName: "Bash", Command: "git status --short", WorkDir: t.TempDir()})
	if !ok || got != Defer {
		t.Fatalf("Classify = (%q,%v), want (DEFER,true)", got, ok)
	}
}

func TestClassifyMalformedFails(t *testing.T) {
	script := testutil.WriteFakeClaudeScript(t, testutil.FakeClaudeMalformedScriptBody())
	reg := testutil.NewFakeClaudeRegistry(t, script)
	reviewer, _ := ResolveReviewer(reg, "")
	if _, ok := Classify(context.Background(), reviewer, ClassifyRequest{ToolName: "Bash", Command: "go test ./...", WorkDir: t.TempDir()}); ok {
		t.Fatalf("Classify(prose) = true, want false")
	}
}

func TestClassifyToolUseDeniedFails(t *testing.T) {
	// The reviewer tries to use a tool; the deny-all boundary denies it and
	// fails the review immediately. Even though a result message follows,
	// the outcome is failure because the control request was terminal.
	script := testutil.WriteFakeClaudeScript(t, testutil.FakeClaudeToolUseScriptBody())
	reg := testutil.NewFakeClaudeRegistry(t, script)
	reviewer, _ := ResolveReviewer(reg, "")
	if _, ok := Classify(context.Background(), reviewer, ClassifyRequest{ToolName: "Bash", Command: "go test ./...", WorkDir: t.TempDir()}); ok {
		t.Fatalf("Classify(tool use) = true, want false (deny-all boundary)")
	}
}

func TestClassifyControlRequestThenAllowFails(t *testing.T) {
	// A reviewer that issues a control request and then returns ALLOW must
	// fail: the control request is terminal and the read loop stops, so the
	// subsequent ALLOW can never be accepted.
	script := testutil.WriteFakeClaudeScript(t, testutil.FakeClaudeControlThenAllowScriptBody())
	reg := testutil.NewFakeClaudeRegistry(t, script)
	reviewer, _ := ResolveReviewer(reg, "")
	if _, ok := Classify(context.Background(), reviewer, ClassifyRequest{ToolName: "Bash", Command: "go test ./...", WorkDir: t.TempDir()}); ok {
		t.Fatalf("Classify(control+ALLOW) = true, want false (control request is terminal)")
	}
}

func TestClassifyErrorResultFails(t *testing.T) {
	// A result with subtype "error" or is_error=true must fail even if
	// assistant text contains ALLOW.
	script := testutil.WriteFakeClaudeScript(t, testutil.FakeClaudeErrorResultScriptBody())
	reg := testutil.NewFakeClaudeRegistry(t, script)
	reviewer, _ := ResolveReviewer(reg, "")
	if _, ok := Classify(context.Background(), reviewer, ClassifyRequest{ToolName: "Bash", Command: "go test ./...", WorkDir: t.TempDir()}); ok {
		t.Fatalf("Classify(error result) = true, want false (error result is terminal)")
	}
}

func TestClassifyRefusalResultFails(t *testing.T) {
	// A result with stop_reason "refusal" must fail even if assistant text
	// contains ALLOW.
	script := testutil.WriteFakeClaudeScript(t, testutil.FakeClaudeRefusalScriptBody())
	reg := testutil.NewFakeClaudeRegistry(t, script)
	reviewer, _ := ResolveReviewer(reg, "")
	if _, ok := Classify(context.Background(), reviewer, ClassifyRequest{ToolName: "Bash", Command: "go test ./...", WorkDir: t.TempDir()}); ok {
		t.Fatalf("Classify(refusal) = true, want false (refusal is terminal)")
	}
}

func TestClassifyTimeoutFails(t *testing.T) {
	script := testutil.WriteFakeClaudeScript(t, testutil.FakeClaudeSleepScriptBody())
	reg := testutil.NewFakeClaudeRegistry(t, script)
	reviewer, _ := ResolveReviewer(reg, "")
	if _, ok := Classify(context.Background(), reviewer, ClassifyRequest{ToolName: "Bash", Command: "go test ./...", WorkDir: t.TempDir(), Timeout: 200 * time.Millisecond}); ok {
		t.Fatalf("Classify(timeout) = true, want false")
	}
}

func TestClassifyCancelledContextFails(t *testing.T) {
	script := testutil.WriteFakeClaudeScript(t, testutil.FakeClaudeSleepScriptBody())
	reg := testutil.NewFakeClaudeRegistry(t, script)
	reviewer, _ := ResolveReviewer(reg, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := Classify(ctx, reviewer, ClassifyRequest{ToolName: "Bash", Command: "go test ./...", WorkDir: t.TempDir()}); ok {
		t.Fatalf("Classify(cancelled ctx) = true, want false")
	}
}

func TestClassifyDetailedOutcomeTaxonomy(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		timeout time.Duration
		want    Outcome
	}{
		{name: "allow", body: testutil.FakeClaudeAllowScriptBody(), want: OutcomeAllow},
		{name: "defer", body: testutil.FakeClaudeDeferScriptBody(), want: OutcomeDefer},
		{name: "malformed response", body: testutil.FakeClaudeMalformedScriptBody(), want: OutcomeMalformedResponse},
		{name: "unexpected interaction", body: testutil.FakeClaudeToolUseScriptBody(), want: OutcomeUnexpectedInteraction},
		{name: "provider error", body: testutil.FakeClaudeErrorResultScriptBody(), want: OutcomeProviderError},
		{name: "timeout", body: testutil.FakeClaudeSleepScriptBody(), timeout: 100 * time.Millisecond, want: OutcomeTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			script := testutil.WriteFakeClaudeScript(t, tt.body)
			reg := testutil.NewFakeClaudeRegistry(t, script)
			reviewer, _ := ResolveReviewer(reg, "")
			got := ClassifyDetailed(context.Background(), reviewer, ClassifyRequest{
				ToolName: "Bash", Command: "go test ./...", WorkDir: t.TempDir(), Timeout: tt.timeout,
			})
			if got.Outcome != tt.want {
				t.Errorf("ClassifyDetailed().Outcome = %q, want %q; result=%+v", got.Outcome, tt.want, got)
			}
			if strings.Contains(got.FailureReason, "ALLOW") || strings.Contains(got.FailureReason, "DEFER") {
				t.Errorf("ClassifyDetailed().FailureReason leaked reviewer output: %q", got.FailureReason)
			}
		})
	}

	script := testutil.WriteFakeClaudeScript(t, testutil.FakeClaudeSleepScriptBody())
	reg := testutil.NewFakeClaudeRegistry(t, script)
	reviewer, _ := ResolveReviewer(reg, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := ClassifyDetailed(ctx, reviewer, ClassifyRequest{ToolName: "Bash", Command: "go test ./...", WorkDir: t.TempDir()}); got.Outcome != OutcomeCanceled {
		t.Errorf("ClassifyDetailed(canceled).Outcome = %q, want %q", got.Outcome, OutcomeCanceled)
	}
}
