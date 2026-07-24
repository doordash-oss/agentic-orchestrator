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

package main

import (
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
)

func TestMergeRuntimeDefaultsAutomaticReviewEnabled(t *testing.T) {
	dst := config.DefaultsConfig{}
	enabled := true
	changed := mergeRuntimeDefaultsMutation(&dst, serverruntime.RuntimeDefaultsMutation{
		AutomaticReviewEnabled: &enabled,
	})
	if !changed || !dst.AutomaticReviewEnabled {
		t.Fatalf("merge did not set AutomaticReviewEnabled=true (changed=%v)", changed)
	}
}

func TestMergeRuntimeDefaultsAutomaticReviewDisabledDistinguishableFromOmitted(t *testing.T) {
	dst := config.DefaultsConfig{AutomaticReviewEnabled: true}
	disabled := false
	changed := mergeRuntimeDefaultsMutation(&dst, serverruntime.RuntimeDefaultsMutation{
		AutomaticReviewEnabled: &disabled,
	})
	if !changed || dst.AutomaticReviewEnabled {
		t.Fatalf("merge did not set AutomaticReviewEnabled=false (changed=%v)", changed)
	}
	// Omitted (nil) must preserve the existing value.
	dst.AutomaticReviewEnabled = true
	changed = mergeRuntimeDefaultsMutation(&dst, serverruntime.RuntimeDefaultsMutation{})
	if changed || !dst.AutomaticReviewEnabled {
		t.Fatalf("omitted AutomaticReviewEnabled should preserve true (changed=%v)", changed)
	}
}

func TestMergeRuntimeDefaultsAutomaticReviewModelPreservedWhenOmitted(t *testing.T) {
	// When a Models patch is present but AutomaticReview is nil (omitted),
	// the existing AutomaticReview value must be preserved. This is the key
	// presence-sensitivity fix: a caller that sends models only to change a
	// phase model must not silently reset an explicit automatic-review model.
	dst := config.DefaultsConfig{Models: config.ModelConfig{AutomaticReview: "claude:haiku[200K]", Inquiry: "sonnet"}}
	changed := mergeRuntimeDefaultsMutation(&dst, serverruntime.RuntimeDefaultsMutation{
		Models: &serverruntime.ModelConfigPatch{Inquiry: "opus"},
	})
	if !changed {
		t.Fatalf("expected changed=true (Inquiry updated)")
	}
	if dst.Models.AutomaticReview != "claude:haiku[200K]" {
		t.Errorf("AutomaticReview = %q, want claude:haiku[200K] (preserved when omitted)", dst.Models.AutomaticReview)
	}
	if dst.Models.Inquiry != "opus" {
		t.Errorf("Inquiry = %q, want opus", dst.Models.Inquiry)
	}
}

func TestMergeRuntimeDefaultsAutomaticReviewModelSet(t *testing.T) {
	model := "claude:haiku[200K]"
	dst := config.DefaultsConfig{}
	changed := mergeRuntimeDefaultsMutation(&dst, serverruntime.RuntimeDefaultsMutation{
		Models: &serverruntime.ModelConfigPatch{AutomaticReview: &model},
	})
	if !changed || dst.Models.AutomaticReview != "claude:haiku[200K]" {
		t.Fatalf("merge did not set AutomaticReview model (changed=%v, got %q)", changed, dst.Models.AutomaticReview)
	}
}

func TestMergeRuntimeDefaultsAutomaticReviewModelClearToEmpty(t *testing.T) {
	// A patch whose only intended change is clearing automatic_review back to
	// the meaningful empty "Automatic" value must trigger the merge. The
	// *string pointer being non-nil distinguishes "set to empty" from
	// "omitted".
	empty := ""
	dst := config.DefaultsConfig{Models: config.ModelConfig{AutomaticReview: "claude:haiku[200K]"}}
	changed := mergeRuntimeDefaultsMutation(&dst, serverruntime.RuntimeDefaultsMutation{
		Models: &serverruntime.ModelConfigPatch{AutomaticReview: &empty},
	})
	if !changed {
		t.Fatalf("clearing AutomaticReview to empty should trigger merge (changed=false)")
	}
	if dst.Models.AutomaticReview != "" {
		t.Errorf("AutomaticReview = %q, want empty (Automatic)", dst.Models.AutomaticReview)
	}
}

func TestMergeRuntimeDefaultsModelsOmittedDoesNotMerge(t *testing.T) {
	// When Models is nil (omitted from the request), no merge should happen.
	dst := config.DefaultsConfig{Models: config.ModelConfig{AutomaticReview: "claude:haiku[200K]", Inquiry: "sonnet"}}
	changed := mergeRuntimeDefaultsMutation(&dst, serverruntime.RuntimeDefaultsMutation{})
	if changed {
		t.Fatalf("omitted Models should not trigger merge (changed=true)")
	}
	if dst.Models.AutomaticReview != "claude:haiku[200K]" {
		t.Errorf("AutomaticReview = %q, want claude:haiku[200K] (preserved)", dst.Models.AutomaticReview)
	}
}

func TestMergeRuntimeDefaultsAutomaticReviewModelUnsetWhenAllEmpty(t *testing.T) {
	// A Models patch with all fields empty/nil and AutomaticReview explicitly
	// set to empty should clear the model without touching phase-role models.
	empty := ""
	dst := config.DefaultsConfig{Models: config.ModelConfig{AutomaticReview: "claude:haiku[200K]", Inquiry: "sonnet"}}
	changed := mergeRuntimeDefaultsMutation(&dst, serverruntime.RuntimeDefaultsMutation{
		Models: &serverruntime.ModelConfigPatch{AutomaticReview: &empty},
	})
	if !changed {
		t.Fatalf("clearing AutomaticReview should trigger merge (changed=false)")
	}
	if dst.Models.AutomaticReview != "" {
		t.Errorf("AutomaticReview = %q, want empty", dst.Models.AutomaticReview)
	}
	if dst.Models.Inquiry != "sonnet" {
		t.Errorf("Inquiry = %q, want sonnet (preserved)", dst.Models.Inquiry)
	}
}
