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

// refactorChildFixture is a refactor pass whose description uses relative
// language ("as they are"). Planning and design own resolving that language
// against the fork point: reviewers judge the assembled result against the
// spec those phases produce, so the spec must state absolute end states.
func refactorChildFixture() *feature.Feature {
	return &feature.Feature{
		Name:        "Refactor Translate README",
		Description: "Translate the headers to French, leave the bodies as they are",
		Inquireness: feature.InquirenessMedium,
		Repos:       []feature.FeatureRepo{{Name: "repo-a", BaseBranch: "main"}},
		Parent: &feature.ChildRelationship{
			ParentID: "parent1234ef5678",
			Kind:     feature.ChildKindRefactor,
			Bases:    []feature.ChildRepoBase{{Repo: "repo-a", SHA: "abc123def456", ParentBranch: "feature/parent"}},
		},
	}
}

func TestRefactorPassForkPoint_NamesForkPointPerRepo(t *testing.T) {
	if got := refactorPassForkPoint(refactorChildFixture()); got != "repo-a @ abc123def456" {
		t.Fatalf("refactorPassForkPoint(child) = %q", got)
	}
	if got := refactorPassForkPoint(&feature.Feature{}); got != "" {
		t.Fatalf("refactorPassForkPoint(top-level) = %q, want empty", got)
	}
}

// Planning and design are where user intent becomes the authoritative spec,
// so they — not reviewers — receive the fork-point context and the duty to
// resolve relative language into absolute end-state statements.
func TestSpecPhasePromptsCarryRefactorPassContext(t *testing.T) {
	child := refactorChildFixture()
	roadmap := BuildRoadmapPrompt(child, "", "", "/path/to/design.md", nil)
	design := BuildDesignPrompt(child, "", "", "/path/to/research.md", nil)

	for name, prompt := range map[string]string{"roadmap": roadmap, "design": design} {
		if !strings.Contains(prompt, "## Refactor Pass Context") {
			t.Fatalf("%s prompt missing the refactor pass context:\n%s", name, prompt)
		}
		if !strings.Contains(prompt, "abc123def456") {
			t.Fatalf("%s prompt missing the fork-point SHA:\n%s", name, prompt)
		}
		if !strings.Contains(prompt, `"as they are"`) {
			t.Fatalf("%s prompt missing the relative-language pin:\n%s", name, prompt)
		}
		if !strings.Contains(prompt, "absolute statements of the intended end state") {
			t.Fatalf("%s prompt missing the end-state resolution duty:\n%s", name, prompt)
		}
		// The description stays the scope authority: undoing fork-point
		// content on request is legitimate.
		if !strings.Contains(prompt, "it may change or undo anything the fork point contains") {
			t.Fatalf("%s prompt lost the description-is-scope-authority clause:\n%s", name, prompt)
		}
	}

	// Top-level features never see the block.
	top := &feature.Feature{
		Name:        "Plain feature",
		Description: "Do the thing",
		Inquireness: feature.InquirenessMedium,
		Repos:       []feature.FeatureRepo{{Name: "repo-a", BaseBranch: "main"}},
	}
	if strings.Contains(BuildRoadmapPrompt(top, "", "", "", nil), "Refactor Pass Context") {
		t.Fatal("top-level roadmap prompt unexpectedly carries the refactor pass context")
	}
	if strings.Contains(BuildDesignPrompt(top, "", "", "", nil), "Refactor Pass Context") {
		t.Fatal("top-level design prompt unexpectedly carries the refactor pass context")
	}
}
