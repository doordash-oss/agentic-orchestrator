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
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func TestContextWindowLabel(t *testing.T) {
	tests := []struct {
		tokens int
		want   string
	}{
		{0, ""},
		{272_000, "272K"},
		{400_000, "400K"},
		{1_000_000, "1M"},
		{1_050_000, "1.05M"},
		{2_000_000, "2M"},
	}
	for _, tt := range tests {
		if got := llm.ContextWindowLabel(tt.tokens); got != tt.want {
			t.Errorf("ContextWindowLabel(%d) = %q, want %q", tt.tokens, got, tt.want)
		}
	}
}

func TestModelContextWindowHelpers(t *testing.T) {
	if got := llm.ModelWithContextWindow("gpt-5.4", 1_000_000); got != "gpt-5.4[1M]" {
		t.Fatalf("ModelWithContextWindow() = %q, want gpt-5.4[1M]", got)
	}
	if got := llm.StripModelContextWindow("gpt-5.4[272K]"); got != "gpt-5.4" {
		t.Fatalf("StripModelContextWindow() = %q, want gpt-5.4", got)
	}
	parseTests := []struct {
		model string
		want  int
	}{
		{"gpt-5.4[272K]", 272_000},
		{"portkey/@fireworks/accounts/fireworks/models/glm-5p2[1.04M]", 1_040_000},
		{"gpt-5.4", 0},
		{"gpt-5.4[fast]", 0},
	}
	for _, tt := range parseTests {
		if got := llm.ParseModelContextWindow(tt.model); got != tt.want {
			t.Errorf("ParseModelContextWindow(%q) = %d, want %d", tt.model, got, tt.want)
		}
	}
}
