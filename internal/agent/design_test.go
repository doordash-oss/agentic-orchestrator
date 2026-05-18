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

func TestBuildDesignPrompt(t *testing.T) {
	f := &feature.Feature{
		Name:        "Test Feature",
		Description: "Implement user authentication",
		Repos: []feature.FeatureRepo{
			{Name: "myrepo", Path: "/tmp/myrepo"},
		},
		Inquireness: feature.InquirenessHigh,
	}

	prompt := BuildDesignPrompt(f, "", "", "/tmp/research/doc.md", nil)

	checks := []string{
		"# Feature Context",
		"## Feature Request",
		"**Name**: Test Feature",
		"> Implement user authentication",
		"## Research Findings",
		"/tmp/research/doc.md",
		"Ambiguity Resolution",
	}

	for _, c := range checks {
		if !strings.Contains(prompt, c) {
			t.Errorf("expected prompt to contain %q", c)
		}
	}
}

func TestBuildDesignPromptNoResearch(t *testing.T) {
	f := &feature.Feature{
		Name:        "No Research",
		Description: "A feature",
		Inquireness: feature.InquirenessMedium,
	}
	prompt := BuildDesignPrompt(f, "", "", "", nil)
	if strings.Contains(prompt, "## Research Findings") {
		t.Error("expected no research section when path is empty")
	}
}

func TestBuildDesignPromptWithImages(t *testing.T) {
	f := &feature.Feature{
		Name:        "Image Feature",
		Description: "A feature with images",
		Images:      []string{"/tmp/images/image-1.png"},
		Inquireness: feature.InquirenessMedium,
	}
	prompt := BuildDesignPrompt(f, "", "", "/tmp/research.md", nil)
	if !strings.Contains(prompt, "Attached Images:") {
		t.Error("expected 'Attached Images' section")
	}
}

func TestBuildDesignPromptWithQAPaths(t *testing.T) {
	f := &feature.Feature{
		Name:        "QA Feature",
		Description: "A feature with user decisions",
		Inquireness: feature.InquirenessMedium,
	}
	qaFiles := []string{"/tmp/features/abc/inquire/qa-answers.md", "/tmp/features/abc/research/qa-answers.md"}
	prompt := BuildDesignPrompt(f, "", "", "/tmp/research.md", qaFiles)

	if !strings.Contains(prompt, "## User Decisions") {
		t.Error("expected '## User Decisions' section")
	}
	for _, qf := range qaFiles {
		if !strings.Contains(prompt, qf) {
			t.Errorf("expected prompt to contain Q&A path %q", qf)
		}
	}
	if !strings.Contains(prompt, "intent and preferences") {
		t.Error("expected guidance language about user decisions")
	}
}

func TestBuildDesignPromptNoQAPaths(t *testing.T) {
	f := &feature.Feature{
		Name:        "No QA Feature",
		Description: "A feature without Q&A",
		Inquireness: feature.InquirenessMedium,
	}
	prompt := BuildDesignPrompt(f, "", "", "/tmp/research.md", nil)
	if strings.Contains(prompt, "## User Decisions") {
		t.Error("expected no User Decisions section when qaFilePaths is nil")
	}

	prompt2 := BuildDesignPrompt(f, "", "", "/tmp/research.md", []string{})
	if strings.Contains(prompt2, "## User Decisions") {
		t.Error("expected no User Decisions section when qaFilePaths is empty")
	}
}

func TestBuildResearchFromQuestionsPrompt(t *testing.T) {
	f := &feature.Feature{
		Name:        "Test Feature",
		Description: "Implement user authentication",
		Repos: []feature.FeatureRepo{
			{Name: "myrepo", Path: "/tmp/myrepo"},
		},
		Inquireness: feature.InquirenessMedium,
	}

	prompt := BuildResearchFromQuestionsPrompt(f, "", "/tmp/inquire/questions.md")

	// Should contain questions path
	if !strings.Contains(prompt, "/tmp/inquire/questions.md") {
		t.Error("expected prompt to contain questions path")
	}
	if !strings.Contains(prompt, "Questions to Answer") {
		t.Error("expected 'Questions to Answer' section")
	}

	// Should NOT contain the feature description (to prevent intent leaking)
	if strings.Contains(prompt, "Implement user authentication") {
		t.Error("expected prompt to NOT contain the feature description")
	}
	if strings.Contains(prompt, "Feature Under Investigation") {
		t.Error("expected prompt to NOT contain 'Feature Under Investigation' section")
	}
}

func TestBuildResearchFromQuestionsPromptHasRepoInfo(t *testing.T) {
	f := &feature.Feature{
		Name:        "Test",
		Description: "Desc",
		Repos: []feature.FeatureRepo{
			{Name: "myrepo", Path: "/tmp/myrepo"},
		},
		Inquireness: feature.InquirenessMedium,
	}

	prompt := BuildResearchFromQuestionsPrompt(f, "", "/tmp/questions.md")
	if !strings.Contains(prompt, "myrepo") {
		t.Error("expected prompt to contain repo info")
	}
}

func TestBuildDesignPromptUsesEffectiveDescription(t *testing.T) {
	f := &feature.Feature{
		Name:           "Design Refactor",
		Description:    "original desc",
		RefactorPrompt: "improve performance",
		Repos: []feature.FeatureRepo{
			{Name: "repo", Path: "/tmp/test"},
		},
	}
	prompt := BuildDesignPrompt(f, "", "", "some research output", nil)
	if !strings.Contains(prompt, "improve performance") {
		t.Error("expected refactor prompt in design output")
	}
	if !strings.Contains(prompt, "original desc") {
		t.Error("expected original description in design output")
	}
}

func TestBuildDesignPrompt_MultiRepo(t *testing.T) {
	t.Run("multi_repo_includes_target_repos", func(t *testing.T) {
		f := &feature.Feature{
			Name:        "test-feature",
			Description: "A test feature",
			Repos: []feature.FeatureRepo{
				{Name: "repo-a", Path: "/path/a"},
				{Name: "repo-b", Path: "/path/b"},
			},
		}
		prompt := BuildDesignPrompt(f, "", "", "/tmp/research.md", nil)
		if !strings.Contains(prompt, "Target Repositories") {
			t.Error("expected 'Target Repositories' section for multi-repo feature")
		}
		if !strings.Contains(prompt, "repo-a") || !strings.Contains(prompt, "repo-b") {
			t.Error("expected both repo names in prompt")
		}
	})
	t.Run("single_repo_no_target_repos_section", func(t *testing.T) {
		f := &feature.Feature{
			Name:  "test-feature",
			Repos: []feature.FeatureRepo{{Name: "repo-a", Path: "/path/a"}},
		}
		prompt := BuildDesignPrompt(f, "", "", "/tmp/research.md", nil)
		if strings.Contains(prompt, "Target Repositories") {
			t.Error("single-repo feature should not have 'Target Repositories' section")
		}
	})
	t.Run("multi_repo_prefers_worktree_path", func(t *testing.T) {
		f := &feature.Feature{
			Name: "test-feature",
			Repos: []feature.FeatureRepo{
				{Name: "repo-a", Path: "/path/a", WorktreePath: "/wt/a"},
				{Name: "repo-b", Path: "/path/b", WorktreePath: "/wt/b"},
			},
		}
		prompt := BuildDesignPrompt(f, "", "", "/tmp/research.md", nil)
		if !strings.Contains(prompt, "/wt/a") {
			t.Error("expected worktree path /wt/a in prompt")
		}
		if strings.Contains(prompt, "/path/a") {
			t.Error("should use worktree path instead of repo path")
		}
	})
}
