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

package llm_test

import (
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/claude"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/codex"
)

func TestClaudeProvider_Name(t *testing.T) {
	p := &claude.Provider{}
	if p.Name() != "claude" {
		t.Errorf("got %q, want %q", p.Name(), "claude")
	}
}

func TestCodexProvider_Name(t *testing.T) {
	p := &codex.Provider{}
	if p.Name() != "codex" {
		t.Errorf("got %q, want %q", p.Name(), "codex")
	}
}

func TestRecommendationConfidenceClause_RequiresRecommendedHighestConfidence(t *testing.T) {
	if !strings.Contains(llm.RecommendationConfidenceClause, "single highest-confidence option") {
		t.Fatalf("RecommendationConfidenceClause must require the recommended option to be the highest-confidence option:\n%s", llm.RecommendationConfidenceClause)
	}
}

func TestProviderAskingQuestionsClausesIncludeRecommendationConfidenceContract(t *testing.T) {
	providers := map[string]string{
		"claude": (&claude.Provider{}).AskingQuestionsClause(),
		"codex":  (&codex.Provider{}).AskingQuestionsClause(),
	}
	for name, clause := range providers {
		if !strings.Contains(clause, "single highest-confidence option") {
			t.Fatalf("%s AskingQuestionsClause missing highest-confidence recommendation contract:\n%s", name, clause)
		}
	}
}
