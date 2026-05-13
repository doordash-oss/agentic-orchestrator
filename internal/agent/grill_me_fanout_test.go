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

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// TestBuildBrainstormPrompt_UsesGrillMeDirective pins the Brainstorm-primary
// builder: emits the policy-free [grill-me] header for every Inquireness level.
func TestBuildBrainstormPrompt_UsesGrillMeDirective(t *testing.T) {
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
				Description: "exercise grill-me directive in brainstorm",
				Repos: []feature.FeatureRepo{
					{Name: "myrepo", Path: "/tmp/myrepo"},
				},
				Inquireness: level,
			}
			prompt := BuildBrainstormPrompt(f, "", "", "/tmp/research.md", nil)

			if first == "" {
				first = prompt
			} else if prompt != first {
				t.Errorf("brainstorm prompt for level %q differs from first level", level)
			}
			assertPolicyFreeGrillMePrompt(t, prompt)
			for _, legacy := range []string{
				"Interview me about this proposal",
				"Try to resolve ambiguity on your own first",
			} {
				if strings.Contains(prompt, legacy) {
					t.Errorf("brainstorm prompt still contains legacy prose %q for level %q", legacy, level)
				}
			}
		})
	}
}

// TestBuildRoadmapPrompt_UsesGrillMeDirective pins the Roadmap-primary builder
// flip — same shape as the Brainstorm test above.
func TestBuildRoadmapPrompt_UsesGrillMeDirective(t *testing.T) {
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
				Description: "exercise grill-me directive in roadmap",
				Inquireness: level,
			}
			prompt := BuildRoadmapPrompt(f, "", "", "/tmp/brainstorm.md", nil)

			if first == "" {
				first = prompt
			} else if prompt != first {
				t.Errorf("roadmap prompt for level %q differs from first level", level)
			}
			assertPolicyFreeGrillMePrompt(t, prompt)
			for _, legacy := range []string{
				"Interview me about this proposal",
				"Try to resolve ambiguity on your own first",
				"Resolve all ambiguity autonomously",
			} {
				if strings.Contains(prompt, legacy) {
					t.Errorf("roadmap prompt still contains legacy prose %q for level %q", legacy, level)
				}
			}
		})
	}
}

// TestBuildPhasePlanPrompt_UsesInvariantGrillMe pins that Phase-Plan uses the
// same policy-free [grill-me] prompt text as other planning phases.
func TestBuildPhasePlanPrompt_UsesInvariantGrillMe(t *testing.T) {
	levels := []feature.Inquireness{
		feature.InquirenessNone,
		feature.InquirenessMedium,
		feature.InquirenessHigh,
	}

	phase := RoadmapPhase{
		Number: 1,
		Name:   "Test",
		Type:   "tdd-fill-in",
	}

	var first string
	for _, level := range levels {
		t.Run(string(level), func(t *testing.T) {
			f := &feature.Feature{
				Name:        "Test Feature",
				Description: "exercise phase-plan grill-me prompt",
				Inquireness: level,
			}
			prompt := BuildPhasePlanPrompt(f, "", "", "/roadmap.md", phase, nil)

			if first == "" {
				first = prompt
			} else if prompt != first {
				t.Errorf("phase-plan prompt for level %q differs from first level", level)
			}
			assertPolicyFreeGrillMePrompt(t, prompt)
			for _, unwanted := range []string{
				"Do not auto-pick any answer",
				"## Ambiguity Resolution: [autonomous]",
				"## Ambiguity Resolution [autonomous]",
			} {
				if strings.Contains(prompt, unwanted) {
					t.Errorf("phase-plan prompt unexpectedly contains %q for level %q", unwanted, level)
				}
			}
		})
	}
}
