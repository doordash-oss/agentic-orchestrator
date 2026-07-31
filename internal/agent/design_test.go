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
	"os"
	"path/filepath"
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

	prompt := BuildDesignPrompt(f, "", "", "/tmp/research/doc.md", "/tmp/design/decision-ledger.md", nil)

	checks := []string{
		"# Feature Context",
		"## Feature Request",
		"**Name**: Test Feature",
		"> Implement user authentication",
		"## Research Findings",
		"/tmp/research/doc.md",
		"## Authoritative Decision Ledger",
		"/tmp/design/decision-ledger.md",
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
	prompt := BuildDesignPrompt(f, "", "", "", "", nil)
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
	prompt := BuildDesignPrompt(f, "", "", "/tmp/research.md", "", nil)
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
	prompt := BuildDesignPrompt(f, "", "", "/tmp/research.md", "/tmp/features/abc/design/decision-ledger.md", qaFiles)

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
	if !strings.Contains(prompt, "/tmp/features/abc/design/decision-ledger.md") {
		t.Error("expected authoritative decision ledger path")
	}
}

func TestBuildDesignPromptNoQAPaths(t *testing.T) {
	f := &feature.Feature{
		Name:        "No QA Feature",
		Description: "A feature without Q&A",
		Inquireness: feature.InquirenessMedium,
	}
	prompt := BuildDesignPrompt(f, "", "", "/tmp/research.md", "", nil)
	if strings.Contains(prompt, "## User Decisions") {
		t.Error("expected no User Decisions section when qaFilePaths is nil")
	}

	prompt2 := BuildDesignPrompt(f, "", "", "/tmp/research.md", "", []string{})
	if strings.Contains(prompt2, "## User Decisions") {
		t.Error("expected no User Decisions section when qaFilePaths is empty")
	}
}

func TestWriteDesignDecisionLedgerAssignsStableRequirementAndDecisionIDs(t *testing.T) {
	artifactDir := t.TempDir()
	firstQA := filepath.Join(t.TempDir(), "qa-answers.md")
	secondQA := filepath.Join(t.TempDir(), "qa-answers.md")
	if err := os.WriteFile(firstQA, []byte(`# User Q&A

## Q: Which storage?

**A:** Reuse the customer table.

## Q: Which delivery semantics?

**A:** Publish the freshest state.

**Notes:** Intermediate mutations do not matter, but dropping the newest committed state is fatal.
`), 0o644); err != nil {
		t.Fatalf("write first Q&A: %v", err)
	}
	if err := os.WriteFile(secondQA, []byte(`# User Q&A

## Q: Which performance gate?

**A:** Correctness-only acceptance.
`), 0o644); err != nil {
		t.Fatalf("write second Q&A: %v", err)
	}
	f := &feature.Feature{
		Name:         "Linearizable CDC",
		Description:  "Support CDC without dropping committed state.",
		ExitCriteria: "Preserve state-convergent delivery.",
	}

	path, err := WriteDesignDecisionLedger(artifactDir, f, []string{firstQA, secondQA})
	if err != nil {
		t.Fatalf("WriteDesignDecisionLedger() error = %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read decision ledger: %v", err)
	}
	for _, want := range []string{
		"### REQ-001 — Feature request",
		"### REQ-002 — Exit criteria",
		"### DEC-001 — Which storage?",
		"### DEC-002 — Which delivery semantics?",
		"### DEC-003 — Which performance gate?",
		"**Decision:** Reuse the customer table.",
		"**Notes:** Intermediate mutations do not matter, but dropping the newest committed state is fatal.",
		"**Decision:** Correctness-only acceptance.",
	} {
		if !strings.Contains(string(first), want) {
			t.Errorf("decision ledger missing %q:\n%s", want, first)
		}
	}

	secondPath, err := WriteDesignDecisionLedger(artifactDir, f, []string{firstQA, secondQA})
	if err != nil {
		t.Fatalf("second WriteDesignDecisionLedger() error = %v", err)
	}
	second, err := os.ReadFile(secondPath)
	if err != nil {
		t.Fatalf("read second decision ledger: %v", err)
	}
	if string(second) != string(first) {
		t.Fatalf("decision ledger IDs/content changed across identical regeneration:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

func TestDesignDecisionSourcePathsIncludesCurrentDesignAnswersLast(t *testing.T) {
	artifactDir := t.TempDir()
	current := filepath.Join(artifactDir, "qa-answers.md")
	if err := os.WriteFile(current, []byte("# current design answers\n"), 0o644); err != nil {
		t.Fatalf("write current design answers: %v", err)
	}
	firstAttemptDir := filepath.Join(artifactDir, "attempt-01")
	if err := os.MkdirAll(firstAttemptDir, 0o755); err != nil {
		t.Fatalf("create first attempt: %v", err)
	}
	humanDirection, err := RecordDesignHumanDirection(firstAttemptDir, "Keep the selected delivery semantics.")
	if err != nil {
		t.Fatalf("RecordDesignHumanDirection(): %v", err)
	}
	revisionDir := filepath.Join(artifactDir, "attempt-02")
	if err := os.MkdirAll(revisionDir, 0o755); err != nil {
		t.Fatalf("create revision attempt: %v", err)
	}
	revision := filepath.Join(revisionDir, "qa-answers.md")
	if err := os.WriteFile(revision, []byte("# revision answers\n"), 0o644); err != nil {
		t.Fatalf("write revision answers: %v", err)
	}

	got := designDecisionSourcePaths([]string{"/upstream/inquire.md", "/upstream/research.md"}, artifactDir)
	want := []string{"/upstream/inquire.md", "/upstream/research.md", current, humanDirection, revision}
	if len(got) != len(want) {
		t.Fatalf("designDecisionSourcePaths() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("designDecisionSourcePaths()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRecordDesignHumanDirectionBecomesBindingLedgerDecision(t *testing.T) {
	artifactDir := t.TempDir()
	attemptDir := filepath.Join(artifactDir, "attempt-01")
	if err := os.MkdirAll(attemptDir, 0o755); err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	if _, err := RecordDesignHumanDirection(attemptDir, "Preserve watermark-first resolution."); err != nil {
		t.Fatalf("RecordDesignHumanDirection(): %v", err)
	}
	f := &feature.Feature{
		Name:        "Human-reviewed Design",
		Description: "Preserve reviewed decisions.",
	}
	ledgerPath, err := WriteDesignDecisionLedger(
		artifactDir,
		f,
		designDecisionSourcePaths(nil, artifactDir),
	)
	if err != nil {
		t.Fatalf("WriteDesignDecisionLedger(): %v", err)
	}
	ledger, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("read decision ledger: %v", err)
	}
	for _, want := range []string{
		"### DEC-001 — Which direction did the human give after reviewing this Design attempt?",
		"**Decision:** Preserve watermark-first resolution.",
	} {
		if !strings.Contains(string(ledger), want) {
			t.Fatalf("decision ledger missing %q:\n%s", want, ledger)
		}
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
	prompt := BuildDesignPrompt(f, "", "", "some research output", "", nil)
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
		prompt := BuildDesignPrompt(f, "", "", "/tmp/research.md", "", nil)
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
		prompt := BuildDesignPrompt(f, "", "", "/tmp/research.md", "", nil)
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
		prompt := BuildDesignPrompt(f, "", "", "/tmp/research.md", "", nil)
		if !strings.Contains(prompt, "/wt/a") {
			t.Error("expected worktree path /wt/a in prompt")
		}
		if strings.Contains(prompt, "/path/a") {
			t.Error("should use worktree path instead of repo path")
		}
	})
}
