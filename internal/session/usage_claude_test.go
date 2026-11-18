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

package session

import (
	"os"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/claude"
)

func TestClaudeAccumulatedUsageFromRecordedStream(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/claude-usage-two-turns.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")

	t.Run("result modelUsage is the session total", func(t *testing.T) {
		t.Parallel()
		s := NewSession("claude-usage", "feat-1", feature.PhaseResearch)
		s.protocol = claude.NewProtocol(llm.ProtocolOpts{})
		runSessionWithStdoutLines(t, s, lines, nil)

		want := llm.Usage{InputTokens: 307, OutputTokens: 1200, CacheReadInputTokens: 50748, CacheCreationInputTokens: 5920}
		if got := s.AccumulatedUsage(); got != want {
			t.Errorf("AccumulatedUsage() = %+v, want %+v", got, want)
		}
		if got := s.Cost().TotalCostUSD; got != 0.1 {
			t.Errorf("Cost().TotalCostUSD = %v, want 0.1", got)
		}
	})

	t.Run("streamed usage counts each message once", func(t *testing.T) {
		t.Parallel()
		s := NewSession("claude-usage-live", "feat-1", feature.PhaseResearch)
		s.protocol = claude.NewProtocol(llm.ProtocolOpts{})
		runSessionWithStdoutLines(t, s, lines[:6], nil)

		want := llm.Usage{InputTokens: 4, OutputTokens: 23, CacheReadInputTokens: 31632, CacheCreationInputTokens: 5800}
		if got := s.AccumulatedUsage(); got != want {
			t.Errorf("AccumulatedUsage() = %+v, want %+v", got, want)
		}
	})
}
