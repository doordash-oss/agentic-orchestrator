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
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func TestMaybeWrapHelperSandbox_DisabledUnchanged(t *testing.T) {
	cmd := []string{"helper", "run"}
	got, sandboxed, cleanup := maybeWrapHelperSandbox(cmd, false, "/Users/x/.agentic-workflow/features")
	cleanup()
	if sandboxed || len(got) != 2 || got[0] != "helper" || got[1] != "run" {
		t.Errorf("disabled sandbox: got=%v sandboxed=%v, want unchanged [helper run]", got, sandboxed)
	}
}

func TestFinishOrViolateNudgeForModel(t *testing.T) {
	capable := &captureProvider{name: "nudge", model: "nudge-model", finishOrViolate: true}
	plain := &captureProvider{name: "plain", model: "plain-model"}

	reg := llm.NewRegistry()
	reg.Register(capable)
	reg.Register(plain)

	cases := []struct {
		name     string
		registry *llm.Registry
		model    string
		want     bool
	}{
		{name: "capability positive provider", registry: reg, model: "nudge-model", want: true},
		{name: "provider without capability", registry: reg, model: "plain-model", want: false},
		{name: "unresolved model", registry: reg, model: "unknown-model", want: false},
		{name: "nil registry", registry: nil, model: "nudge-model", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pr := &PhaseRunner{Registry: tc.registry}
			if got := pr.finishOrViolateNudgeForModel(tc.model); got != tc.want {
				t.Errorf("finishOrViolateNudgeForModel(%q) = %v, want %v", tc.model, got, tc.want)
			}
			// The exported wrapper (used by the orchestrator package) must
			// delegate to the unexported resolver, not diverge.
			if got := pr.FinishOrViolateNudgeForModel(tc.model); got != tc.want {
				t.Errorf("FinishOrViolateNudgeForModel(%q) = %v, want %v", tc.model, got, tc.want)
			}
		})
	}

	if got := (*PhaseRunner)(nil).finishOrViolateNudgeForModel("nudge-model"); got {
		t.Errorf("nil PhaseRunner: got %v, want false", got)
	}
}
