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

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// TestGrillMeInquirenessPartial_AllLevels pins the universal "[grill-me]"
// prompt body. Harness policy is intentionally absent from agent-facing text,
// so the rendered directive is byte-identical across inquireness levels.
func TestGrillMeInquirenessPartial_AllLevels(t *testing.T) {
	levels := []feature.Inquireness{
		feature.InquirenessNone,
		feature.InquirenessMedium,
		feature.InquirenessHigh,
	}

	var first string
	for _, level := range levels {
		t.Run(string(level), func(t *testing.T) {
			got := prompts.GrillMeInquireness(string(level))
			if first == "" {
				first = got
			} else if got != first {
				t.Errorf("directive for level %q differs from first level\n--- first ---\n%s\n--- got ---\n%s", level, first, got)
			}
			assertPolicyFreeGrillMePrompt(t, got)
		})
	}
}

// TestAutonomousInquirenessPartial pins the [none] autonomous prose emitted
// by the autonomous_inquireness partial (used by Roadmap-revision and
// Phase-Plan-revision builders). Header is the bracketed-tag form (no stray
// colon); body resolves all ambiguity autonomously without grilling the user.
func TestAutonomousInquirenessPartial(t *testing.T) {
	got := prompts.AutonomousInquireness()

	wantSnippets := []string{
		"## Ambiguity Resolution [none]",
		"Resolve all ambiguity autonomously",
	}
	for _, snippet := range wantSnippets {
		if !strings.Contains(got, snippet) {
			t.Errorf("autonomous_inquireness missing %q\n--- directive ---\n%s", snippet, got)
		}
	}
	// The bracketed-tag header must NOT have the stray colon present in the
	// legacy [none] prose.
	if strings.Contains(got, "[none]:") {
		t.Errorf("autonomous_inquireness header still has stray colon `[none]:`\n--- directive ---\n%s", got)
	}
}

func TestGrillMeInquirenessPartial_InvariantAndPolicyFree(t *testing.T) {
	none := prompts.GrillMeInquireness(string(feature.InquirenessNone))
	medium := prompts.GrillMeInquireness(string(feature.InquirenessMedium))
	high := prompts.GrillMeInquireness(string(feature.InquirenessHigh))

	if none != medium || medium != high {
		t.Fatalf("grill-me directive differs by inquireness level:\n--- none ---\n%s\n--- medium ---\n%s\n--- high ---\n%s", none, medium, high)
	}

	for _, forbidden := range []string{
		"strictly greater than",
		"auto-pick",
		"auto-resolve",
		"silent",
		"qa-answers.md",
		"threshold",
	} {
		if strings.Contains(strings.ToLower(none), forbidden) {
			t.Errorf("grill-me directive contains policy/authorship term %q:\n%s", forbidden, none)
		}
	}
}

// TestBuildInquirePrompt_UsesGrillMeDirective pins the Phase 1 builder flip:
// BuildInquirePrompt emits the policy-free [grill-me] block for all levels.
func TestBuildInquirePrompt_UsesGrillMeDirective(t *testing.T) {
	levels := []feature.Inquireness{
		feature.InquirenessNone,
		feature.InquirenessMedium,
		feature.InquirenessHigh,
	}

	var first string
	for _, level := range levels {
		t.Run(string(level), func(t *testing.T) {
			f := &feature.Feature{
				Name:        "Test Feature",
				Description: "exercise grill-me directive",
				Repos: []feature.FeatureRepo{
					{Name: "myrepo", Path: "/tmp/myrepo"},
				},
				Inquireness: level,
			}
			prompt := BuildInquirePrompt(f, "")

			if first == "" {
				first = prompt
			} else if prompt != first {
				t.Errorf("inquire prompt for level %q differs from first level", level)
			}
			assertPolicyFreeGrillMePrompt(t, prompt)
			// Must NOT carry the legacy three-branch prose.
			for _, legacy := range []string{
				"Interview me about this proposal",
				"Try to resolve ambiguity on your own first",
			} {
				if strings.Contains(prompt, legacy) {
					t.Errorf("inquire prompt still contains legacy prose %q for level %q", legacy, level)
				}
			}
		})
	}
}

func assertPolicyFreeGrillMePrompt(t *testing.T, prompt string) {
	t.Helper()
	if !strings.Contains(prompt, "## Ambiguity Resolution [grill-me]") {
		t.Fatalf("prompt missing [grill-me] header:\n%s", prompt)
	}
	if strings.Contains(prompt, "[grill-me]:") {
		t.Errorf("prompt contains bracketed-tag with stray colon `[grill-me]:`")
	}
	lower := strings.ToLower(prompt)
	for _, forbidden := range []string{
		"strictly greater than",
		"auto-pick",
		"auto-resolve",
		"silent",
		"qa-answers.md",
		"threshold",
	} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("prompt contains policy/authorship term %q:\n%s", forbidden, prompt)
		}
	}
}

// TestBuildRoadmapRevisionPrompt_UsesAutonomousDirective pins the Roadmap-
// revision builder flip: revision prompts emit the renamed autonomous block,
// not the legacy three-branch directive.
func TestBuildRoadmapRevisionPrompt_UsesAutonomousDirective(t *testing.T) {
	f := &feature.Feature{
		Name:        "Roadmap Revision",
		Description: "exercise revision directive",
		Inquireness: feature.InquirenessHigh,
	}
	prompt := BuildRoadmapRevisionPrompt(f, "", "/tmp/roadmap.md", "/tmp/prev.md", "feedback", "", 1, nil)

	wantSnippets := []string{
		"## Ambiguity Resolution [none]",
		"Resolve all ambiguity autonomously",
	}
	for _, snippet := range wantSnippets {
		if !strings.Contains(prompt, snippet) {
			t.Errorf("roadmap revision prompt missing %q", snippet)
		}
	}
	// Must NOT promote the user-facing grill-me prose into a revision loop.
	if strings.Contains(prompt, "## Ambiguity Resolution [grill-me]") {
		t.Errorf("roadmap revision prompt unexpectedly contains [grill-me] header — revision loops must stay autonomous")
	}
}

// TestBuildPhasePlanRevisionPrompt_UsesAutonomousDirective pins the Phase-Plan
// revision builder flip with the same guarantee.
func TestBuildPhasePlanRevisionPrompt_UsesAutonomousDirective(t *testing.T) {
	f := &feature.Feature{
		Name:        "Phase Plan Revision",
		Description: "exercise phase-plan revision directive",
		Inquireness: feature.InquirenessHigh,
	}
	phase := RoadmapPhase{Number: 1, Name: "Bootstrap", Type: "Tracer"}
	prompt := BuildPhasePlanRevisionPrompt(f, "", "/tmp/plan.md", "feedback", "", phase, 1, nil)

	wantSnippets := []string{
		"## Ambiguity Resolution [none]",
		"Resolve all ambiguity autonomously",
	}
	for _, snippet := range wantSnippets {
		if !strings.Contains(prompt, snippet) {
			t.Errorf("phase-plan revision prompt missing %q", snippet)
		}
	}
	if strings.Contains(prompt, "## Ambiguity Resolution [grill-me]") {
		t.Errorf("phase-plan revision prompt unexpectedly contains [grill-me] header — revision loops must stay autonomous")
	}
}
