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

func intakeTestFeature() *feature.Feature {
	return &feature.Feature{
		Name:         "sample-feature",
		Description:  "sample description",
		ExitCriteria: "the sample outcome is observable end to end",
		Artifacts:    map[string]string{},
	}
}

func TestInquirePromptCarriesExitCriteria(t *testing.T) {
	prompt := BuildInquirePrompt(intakeTestFeature(), "")
	if !strings.Contains(prompt, "the sample outcome is observable end to end") {
		t.Fatalf("inquire prompt must carry exit criteria:\n%s", prompt)
	}
}

func TestDesignPromptCarriesExitCriteria(t *testing.T) {
	prompt := BuildDesignPrompt(intakeTestFeature(), "", "", "", nil)
	if !strings.Contains(prompt, "the sample outcome is observable end to end") {
		t.Fatalf("design prompt must carry exit criteria:\n%s", prompt)
	}
	if !strings.Contains(prompt, "## Acceptance Criteria") {
		t.Fatalf("design prompt must direct distillation into the acceptance section:\n%s", prompt)
	}
}

func TestRoadmapPromptExitCriteriaOnlyWithoutDesign(t *testing.T) {
	f := intakeTestFeature()
	noDesign := BuildRoadmapPromptWithResearch(f, "", "", "", "", nil)
	if !strings.Contains(noDesign, "the sample outcome is observable end to end") {
		t.Fatalf("design-less roadmap prompt must carry exit criteria:\n%s", noDesign)
	}
	withDesign := BuildRoadmapPromptWithResearch(f, "", "", "/state/design.md", "", nil)
	if strings.Contains(withDesign, "the sample outcome is observable end to end") {
		t.Fatalf("design-backed roadmap prompt must not inline raw exit criteria:\n%s", withDesign)
	}
}
