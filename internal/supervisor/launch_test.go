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
	"errors"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestRegistryCatalog_ValidatesHarnessModelAndEffort(t *testing.T) {
	reg := llm.NewRegistry()
	reg.Register(testutil.FakeClaudeProvider{Script: "unused"})
	eligible := reg.EligibleModelsForPhase(llm.PhaseChat)["claude"]
	if len(eligible) == 0 {
		t.Fatal("fake Claude has no chat-eligible model")
	}
	catalog := RegistryCatalog{Registry: reg}
	if err := catalog.ValidateSettings(Settings{Harness: "claude", Model: eligible[0]}); err != nil {
		t.Fatalf("eligible model with default effort: %v", err)
	}
	var invalid *SettingsInvalidError
	for name, s := range map[string]Settings{
		"unknown harness":       {Harness: "nope", Model: eligible[0]},
		"model outside catalog": {Harness: "claude", Model: "opus-imaginary"},
		"unsupported effort":    {Harness: "claude", Model: eligible[0], Effort: "max"},
	} {
		if err := catalog.ValidateSettings(s); !errors.As(err, &invalid) {
			t.Fatalf("%s: err = %v, want SettingsInvalidError", name, err)
		}
	}
	if err := (RegistryCatalog{}).ValidateSettings(Settings{Harness: "claude", Model: eligible[0]}); !errors.As(err, &invalid) {
		t.Fatalf("nil registry err = %v", err)
	}
}
