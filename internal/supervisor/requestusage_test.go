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

package supervisor

import (
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func TestRequestUsage(t *testing.T) {
	tests := []struct {
		name                string
		harness             string
		latest              *llm.Usage
		accumulated         llm.Usage
		baseline            llm.Usage
		input, read, create int
	}{
		{
			name:    "claude reads the request's own usage",
			harness: "claude",
			latest:  &llm.Usage{InputTokens: 1, CacheReadInputTokens: 900, CacheCreationInputTokens: 50},
			input:   1, read: 900, create: 50,
		},
		{
			name:        "codex ignores the context meter and uses the counter growth",
			harness:     "codex",
			latest:      &llm.Usage{InputTokens: 5000, ContextTotalTokens: 5000},
			accumulated: llm.Usage{InputTokens: 15_000, CacheReadInputTokens: 11_900, CacheCreationInputTokens: 30},
			baseline:    llm.Usage{InputTokens: 10_000, CacheReadInputTokens: 11_000},
			input:       5000, read: 900, create: 30,
		},
		{
			name:        "codex before any request reports nothing",
			harness:     "codex",
			accumulated: llm.Usage{InputTokens: 10_000, CacheReadInputTokens: 11_000},
			baseline:    llm.Usage{InputTokens: 10_000, CacheReadInputTokens: 11_000},
		},
		{
			name:        "codex counters that went backwards report nothing",
			harness:     "codex",
			accumulated: llm.Usage{InputTokens: 100, CacheReadInputTokens: 50},
			baseline:    llm.Usage{InputTokens: 10_000},
		},
		{name: "claude without usage reports nothing", harness: "claude"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input, read, create := requestUsage(tt.harness, tt.latest, tt.accumulated, tt.baseline)
			if input != tt.input || read != tt.read || create != tt.create {
				t.Fatalf("usage = %d/%d/%d, want %d/%d/%d", input, read, create, tt.input, tt.read, tt.create)
			}
		})
	}
}
