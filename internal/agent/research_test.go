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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	testutil_mocks "github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

func TestRunScoutExec_UsesBoundedHelper(t *testing.T) {
	called := false
	var capturedPrompt string
	runHelper := func(ctx context.Context, prompt string) (*BoundedHelperResult, error) {
		called = true
		capturedPrompt = prompt
		return &BoundedHelperResult{
			Output: "Scout findings here",
			Status: BoundedHelperStatusCompleted,
		}, nil
	}

	result, err := runScoutExec(context.Background(), "test prompt", runHelper)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected runHelper to be called")
	}
	if capturedPrompt != "test prompt" {
		t.Errorf("capturedPrompt = %q, want %q", capturedPrompt, "test prompt")
	}
	if result != "Scout findings here" {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestRunScoutExec_PropagatesHelperFailure(t *testing.T) {
	runHelper := func(ctx context.Context, prompt string) (*BoundedHelperResult, error) {
		return nil, fmt.Errorf("helper failed")
	}

	_, err := runScoutExec(context.Background(), "test prompt", runHelper)
	if err == nil {
		t.Fatal("runScoutExec() error = nil, want helper failure")
	}
	if !strings.Contains(err.Error(), "helper failed") {
		t.Errorf("error = %q, want helper failure", err)
	}
}

func TestRunParallelScouts_UsesBoundedHelper(t *testing.T) {
	repoPath := t.TempDir()
	files := []string{
		filepath.Join(repoPath, "payments", "handler.go"),
		filepath.Join(repoPath, "payments", "service.go"),
		filepath.Join(repoPath, "billing", "gateway.go"),
	}
	for _, path := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", path, err)
		}
		if err := os.WriteFile(path, []byte("package test\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(%q): %v", path, err)
		}
	}

	index := &CodebaseIndex{
		Summaries: []FileSummary{
			{Path: "payments/handler.go", Purpose: "payment request handling", SymbolCount: 3},
			{Path: "payments/service.go", Purpose: "payment service", SymbolCount: 2},
			{Path: "billing/gateway.go", Purpose: "payment gateway", SymbolCount: 1},
		},
	}

	var (
		mu      sync.Mutex
		prompts []string
	)
	runHelper := func(ctx context.Context, prompt string) (*BoundedHelperResult, error) {
		mu.Lock()
		prompts = append(prompts, prompt)
		count := len(prompts)
		mu.Unlock()
		return &BoundedHelperResult{
			Output: fmt.Sprintf("findings %d", count),
			Status: BoundedHelperStatusCompleted,
		}, nil
	}

	result := RunParallelScouts(context.Background(), "payment", index, repoPath, 2, runHelper)
	if len(prompts) != 2 {
		t.Fatalf("RunParallelScouts() helper calls = %d, want 2", len(prompts))
	}
	if !strings.Contains(result, "## Scout 1 Findings") || !strings.Contains(result, "## Scout 2 Findings") {
		t.Fatalf("RunParallelScouts() = %q, want scout findings sections", result)
	}
}

func TestFilterEnvMulti_SinglePrefix(t *testing.T) {
	env := []string{"CLAUDECODE=abc", "HOME=/home/user", "PATH=/usr/bin"}
	got := filterEnvMulti(env, []string{"CLAUDECODE"})
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", len(got), got)
	}
	for _, e := range got {
		if strings.HasPrefix(e, "CLAUDECODE=") {
			t.Errorf("CLAUDECODE should have been excluded: %v", got)
		}
	}
}

func TestFilterEnvMulti_MultipleExcludes(t *testing.T) {
	env := []string{"CLAUDECODE=abc", "CODEX_SESSION=xyz", "HOME=/home/user"}
	got := filterEnvMulti(env, []string{"CLAUDECODE", "CODEX_SESSION"})
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d: %v", len(got), got)
	}
	if got[0] != "HOME=/home/user" {
		t.Errorf("expected HOME=/home/user, got %s", got[0])
	}
}

func TestFilterEnvMulti_NoExcludes(t *testing.T) {
	env := []string{"FOO=1", "BAR=2"}
	got := filterEnvMulti(env, nil)
	if len(got) != len(env) {
		t.Errorf("expected %d entries, got %d", len(env), len(got))
	}
}

func TestFilterEnvMulti_NoMatches(t *testing.T) {
	env := []string{"FOO=1", "BAR=2"}
	got := filterEnvMulti(env, []string{"NONEXISTENT"})
	if len(got) != 2 {
		t.Errorf("expected 2 entries, got %d", len(got))
	}
}

func TestCollectEnvExcludes_MultipleProviders(t *testing.T) {
	p1 := &testutil_mocks.MockProvider{EnvVarsExclude: []string{"CLAUDECODE"}}
	p2 := &testutil_mocks.MockProvider{}
	got := collectEnvExcludes([]llm.LLMProvider{p1, p2})
	if len(got) != 1 || got[0] != "CLAUDECODE" {
		t.Errorf("expected [CLAUDECODE], got %v", got)
	}
}

func TestCollectEnvExcludes_Deduplication(t *testing.T) {
	p1 := &testutil_mocks.MockProvider{EnvVarsExclude: []string{"SAME_PREFIX"}}
	p2 := &testutil_mocks.MockProvider{EnvVarsExclude: []string{"SAME_PREFIX"}}
	got := collectEnvExcludes([]llm.LLMProvider{p1, p2})
	if len(got) != 1 || got[0] != "SAME_PREFIX" {
		t.Errorf("expected [SAME_PREFIX], got %v", got)
	}
}

func TestCollectEnvExcludes_Empty(t *testing.T) {
	got := collectEnvExcludes(nil)
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}
