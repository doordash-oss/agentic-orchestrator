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

func TestParseDecision(t *testing.T) {
	cases := []struct {
		in      string
		want    Decision
		wantOK  bool
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
