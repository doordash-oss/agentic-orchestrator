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
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	testutil_mocks "github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

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
