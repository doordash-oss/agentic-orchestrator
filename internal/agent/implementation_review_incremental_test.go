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
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// newImplementTestFeatureWithGitRepo creates a feature backed by a real git
// repo so the anchor step can commit and capture SHAs. Mirrors
// newFRTestFeatureWithGitRepos but sets Status/Phase for the implement loop.
func newImplementTestFeatureWithGitRepo(t *testing.T, stateDir, featureID, repoName string) (*feature.Feature, string) {
	t.Helper()
	repoDir := filepath.Join(t.TempDir(), repoName)
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir repo %q: %v", repoName, err)
	}
	if err := git.InitRepository(repoDir); err != nil {
		t.Fatalf("git init repo %q: %v", repoName, err)
	}
	f := &feature.Feature{
		ID:            featureID,
		Name:          "Implement Anchor Test",
		Slug:          "implement-anchor-test",
		Description:   "Anchor step integration test",
		ExitCriteria:  "Tests pass",
		Status:        feature.StatusImplementing,
		CurrentPhase:  feature.PhaseImplement,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: repoName, Path: repoDir, BaseBranch: defaultTestBranch},
		},
		Models: config.ModelConfig{
			Implementation: "agent",
			Review:         "reviewer",
		},
	}
	return f, repoDir
}

// TestImplementIncrementalContext_TwoRoundLoop verifies that a two-reviewed-
// round implement loop produces incremental context on round 2 (prior axis
// report, prior aggregate, per-repo delta) but not on round 1, and that
// anchors are recorded in both iterations' meta.yaml.
func TestImplementIncrementalContext_TwoRoundLoop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-impl")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	const repoName = "api"
	f, repoPath := newImplementTestFeatureWithGitRepo(t, stateDir, "test-feat-impl", repoName)

	planPath := writePlanFile(t, artifactDir, "Implement with error handling")

	// Agent script: write a file to the repo worktree so the anchor step
	// has something to commit, then emit success artifacts.
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh", `
`+testutil.JSONLInit+`
echo 'package main' > '`+filepath.Join(repoPath, "fix.go")+`'
`+testutil.WriteImplementSuccessArtifacts(artifactDir)+`
`+testutil.JSONLSuccess+`
`)

	// Review script: all axes reject iteration 1, approve iteration 2.
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh", `
for _d in "`+artifactDir+`"/iteration-*; do :; done
`+testutil.JSONLInit+`
if [ "$(basename "$_d")" = "iteration-01" ]; then
    `+testutil.WriteReviewChangesRequested(artifactDir, "- **High**: Please add error handling")+`
else
    `+testutil.WriteReviewApproved(artifactDir)+`
fi
`+testutil.JSONLSuccess+`
`)

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	capturingBuild, captured := capturingBuildSession(agentScript, reviewScript)

	cfg := ImplementConfig{
		Feature:             f,
		WorkDir:             workDir,
		PlanPath:            planPath,
		MaxIterations:       5,
		MaxConsecFails:      3,
		MaxConsecNoProgress: 3,
		ExitCriteria:        "Tests pass with error handling",
		Model:               "agent",
		ReviewModel:         "reviewer",
		ArtifactDir:         artifactDir,
		StateDir:            stateDir,
		BuildSession:        capturingBuild,
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("expected FinalStatus=review_passed, got %s (LastError: %s)", result.FinalStatus, result.LastError)
	}
	if result.Iterations != 2 {
		t.Fatalf("expected Iterations=2, got %d", result.Iterations)
	}

	// Verify anchors in meta.yaml for both iterations.
	am := NewArtifactManager(artifactDir)
	for _, iter := range []int{1, 2} {
		iterDir := filepath.Join(artifactDir, "iteration-02")
		if iter == 1 {
			iterDir = filepath.Join(artifactDir, "iteration-01")
		}
		meta, metaErr := am.ReadMeta(iterDir)
		if metaErr != nil {
			t.Fatalf("reading iteration-%02d meta: %v", iter, metaErr)
		}
		anchor, ok := meta.Anchors[repoName]
		if !ok {
			t.Errorf("iteration-%02d: expected anchors for repo %s", iter, repoName)
			continue
		}
		if anchor.Base == "" || anchor.Head == "" {
			t.Errorf("iteration-%02d: anchor base/head must be non-empty (base=%s head=%s)", iter, anchor.Base, anchor.Head)
		}
	}

	// Verify deterministic commit message on iteration 1.
	logOutput := gitLog(t, repoPath, "--format=%s", "-5")
	if !strings.Contains(logOutput, "Phase 0 implement iteration 1: changes requested") {
		t.Errorf("expected deterministic commit message in git log, got:\n%s", logOutput)
	}

	// Classify captured prompts by iteration.
	var round1Prompts, round2Prompts []string
	for _, opts := range *captured {
		if !isReviewHelper(opts.PermHandler) {
			continue
		}
		if strings.Contains(opts.LogPath, "iteration-01") {
			round1Prompts = append(round1Prompts, opts.Prompt)
		} else if strings.Contains(opts.LogPath, "iteration-02") {
			round2Prompts = append(round2Prompts, opts.Prompt)
		}
	}

	if len(round1Prompts) == 0 {
		t.Fatal("expected at least one round-1 review prompt")
	}
	if len(round2Prompts) == 0 {
		t.Fatal("expected at least one round-2 review prompt")
	}

	// Round-1 prompts must NOT contain incremental sections.
	for i, p := range round1Prompts {
		if strings.Contains(p, "## Prior Axis Report (Round N-1)") {
			t.Errorf("round-1 prompt %d: should not contain Prior Axis Report", i)
		}
		if strings.Contains(p, "## Delta Since Your Last Reviewed Round") {
			t.Errorf("round-1 prompt %d: should not contain Delta section", i)
		}
		if strings.Contains(p, "## Prior Aggregate Feedback") {
			t.Errorf("round-1 prompt %d: should not contain Prior Aggregate Feedback", i)
		}
	}

	// Round-2 prompts MUST contain incremental sections.
	foundPriorAxisReport := false
	foundDelta := false
	foundPriorAggregate := false
	for _, p := range round2Prompts {
		if strings.Contains(p, "## Prior Axis Report (Round N-1)") {
			foundPriorAxisReport = true
		}
		if strings.Contains(p, "## Delta Since Your Last Reviewed Round") {
			foundDelta = true
		}
		if strings.Contains(p, "## Prior Aggregate Feedback") {
			foundPriorAggregate = true
		}
	}
	if !foundPriorAxisReport {
		t.Error("round-2 prompts: expected at least one to contain Prior Axis Report")
	}
	if !foundDelta {
		t.Error("round-2 prompts: expected at least one to contain Delta section")
	}
	if !foundPriorAggregate {
		t.Error("round-2 prompts: expected at least one to contain Prior Aggregate Feedback")
	}

	// Verify persisted review-prompt.md on disk for round 2 contains sections.
	round2PromptPath := filepath.Join(artifactDir, "iteration-02", "review", "craft", "review-prompt.md")
	if data, err := os.ReadFile(round2PromptPath); err == nil {
		prompt := string(data)
		if !strings.Contains(prompt, "## Prior Axis Report (Round N-1)") {
			t.Error("persisted round-2 craft review-prompt.md: expected Prior Axis Report")
		}
		if !strings.Contains(prompt, "## Delta Since Your Last Reviewed Round") {
			t.Error("persisted round-2 craft review-prompt.md: expected Delta section")
		}
	} else {
		t.Errorf("reading persisted round-2 review-prompt.md: %v", err)
	}
}

// TestImplementAnchorStep_RetryDoesNotAnchor verifies that a RETRY iteration
// writes no anchors and produces no commit, and the next reviewed iteration's
// anchor commit contains the RETRY iteration's left-over edits.
func TestImplementAnchorStep_RetryDoesNotAnchor(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-retry")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	const repoName = "api"
	f, repoPath := newImplementTestFeatureWithGitRepo(t, stateDir, "test-feat-retry", repoName)

	planPath := writePlanFile(t, artifactDir, "Implement with error handling")

	// Agent script: iteration 1 emits RETRY, iterations 2+ emit success.
	progressFile := filepath.Join(workDir, "progress.md")
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh", `
PROGRESS_FILE="`+progressFile+`"
`+testutil.JSONLInit+`
if [ ! -f "$PROGRESS_FILE" ]; then
    echo "retry" > "$PROGRESS_FILE"
    `+testutil.WriteImplementRetryArtifacts(artifactDir)+`
    `+testutil.JSONLRetry+`
else
    echo 'package main' > '`+filepath.Join(repoPath, "fix.go")+`'
    `+testutil.WriteImplementSuccessArtifacts(artifactDir)+`
    `+testutil.JSONLSuccess+`
fi
`)

	// Review script: all axes approve (iteration 2 is the first reviewed round).
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh", `
`+testutil.JSONLInit+`
`+testutil.WriteReviewApproved(artifactDir)+`
`+testutil.JSONLSuccess+`
`)

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	cfg := ImplementConfig{
		Feature:             f,
		WorkDir:             workDir,
		PlanPath:            planPath,
		MaxIterations:       5,
		MaxConsecFails:      3,
		MaxConsecNoProgress: 3,
		ExitCriteria:        "Tests pass",
		Model:               "agent",
		ReviewModel:         "reviewer",
		ArtifactDir:         artifactDir,
		StateDir:            stateDir,
		BuildSession:        mockBuildSession(agentScript, reviewScript),
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("expected FinalStatus=review_passed, got %s (LastError: %s)", result.FinalStatus, result.LastError)
	}
	if result.Iterations != 2 {
		t.Fatalf("expected Iterations=2, got %d", result.Iterations)
	}

	// Iteration 1 (RETRY): no anchors.
	am := NewArtifactManager(artifactDir)
	iter1Dir := filepath.Join(artifactDir, "iteration-01")
	meta1, _ := am.ReadMeta(iter1Dir)
	if len(meta1.Anchors) > 0 {
		t.Errorf("RETRY iteration should have no anchors, got %v", meta1.Anchors)
	}
	if meta1.AgentStatus != "RETRY" {
		t.Errorf("expected AgentStatus=RETRY, got %s", meta1.AgentStatus)
	}

	// Iteration 2 (APPROVED): anchors present and include the RETRY's edits.
	iter2Dir := filepath.Join(artifactDir, "iteration-02")
	meta2, _ := am.ReadMeta(iter2Dir)
	anchor, ok := meta2.Anchors[repoName]
	if !ok {
		t.Fatal("iteration-02: expected anchors for repo")
	}
	if anchor.Base == "" || anchor.Head == "" {
		t.Errorf("iteration-02: anchor base/head must be non-empty (base=%s head=%s)", anchor.Base, anchor.Head)
	}

	// The RETRY iteration wrote fix.go to the worktree but did not commit.
	// The anchor commit on iteration 2 must contain that file.
	if !fileExists(filepath.Join(repoPath, "fix.go")) {
		t.Error("fix.go from RETRY iteration should exist in the repo worktree")
	}
	logOutput := gitLog(t, repoPath, "--format=%s", "-5")
	if !strings.Contains(logOutput, "Phase 0 implement iteration 2: approved") {
		t.Errorf("expected deterministic commit message for iteration 2, got:\n%s", logOutput)
	}
}

// TestImplementIncrementalContext_RestartResume verifies that incremental
// context resolves from on-disk artifacts after a simulated restart.
func TestImplementIncrementalContext_RestartResume(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-resume")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	const repoName = "api"
	f, repoPath := newImplementTestFeatureWithGitRepo(t, stateDir, "test-feat-resume", repoName)

	planPath := writePlanFile(t, artifactDir, "Implement with error handling")

	// Seed iteration 1 on disk: commit a change, write meta with anchors,
	// write per-axis feedback and aggregate feedback.
	am := NewArtifactManager(artifactDir)
	iter1Dir, _ := am.CreateIterationDir(1)

	writeChangeToFile(t, repoPath, "round1.go", "package main\n")
	baseSHA, _ := git.CurrentHeadSHA(repoPath)
	headSHA, _ := git.CommitAllAndGetHead(repoPath, "Phase 0 implement iteration 1: changes requested")

	am.WriteMeta(iter1Dir, IterationMeta{
		Iteration:    1,
		AgentStatus:   agentStatusSuccess,
		ReviewStatus: agentStatusChangesRequested,
		StartedAt:    time.Now(),
		Anchors: RepoAnchors{
			repoName: RepoAnchor{Base: baseSHA, Head: headSHA},
		},
	})

	// Write aggregate review-feedback.md.
	aggFeedback := testutil.StructuredReviewFeedbackWithScope("- **High**: Please add error handling", "", "CHANGES_REQUESTED", "full", "Round 1 — no prior context exists.")
	os.WriteFile(filepath.Join(iter1Dir, "review-feedback.md"), []byte(aggFeedback), 0o644)

	// Write per-axis feedback files under review/<axis-slug>/.
	for _, axisSlug := range []string{"craft", "functionality-evidence", "cleanliness"} {
		axisDir := filepath.Join(iter1Dir, "review", axisSlug)
		os.MkdirAll(axisDir, 0o755)
		axisFeedback := testutil.StructuredReviewFeedbackWithScope("- "+axisSlug+" finding", "", "CHANGES_REQUESTED", "full", "Round 1 — no prior context exists.")
		os.WriteFile(filepath.Join(axisDir, "review-feedback.md"), []byte(axisFeedback), 0o644)
	}

	// Now run the loop: it should resume from iteration 1 (meta exists),
	// skip straight to iteration 2 (startIter=1), and produce incremental
	// context on round 2 from the on-disk artifacts.
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh", `
`+testutil.JSONLInit+`
echo 'package main' > '`+filepath.Join(repoPath, "round2.go")+`'
`+testutil.WriteImplementSuccessArtifacts(artifactDir)+`
`+testutil.JSONLSuccess+`
`)

	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh", `
`+testutil.JSONLInit+`
`+testutil.WriteReviewApproved(artifactDir)+`
`+testutil.JSONLSuccess+`
`)

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	capturingBuild, captured := capturingBuildSession(agentScript, reviewScript)

	cfg := ImplementConfig{
		Feature:             f,
		WorkDir:             workDir,
		PlanPath:            planPath,
		MaxIterations:       5,
		MaxConsecFails:      3,
		MaxConsecNoProgress: 3,
		ExitCriteria:        "Tests pass",
		Model:               "agent",
		ReviewModel:         "reviewer",
		ArtifactDir:         artifactDir,
		StateDir:            stateDir,
		BuildSession:        capturingBuild,
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("expected FinalStatus=review_passed, got %s (LastError: %s)", result.FinalStatus, result.LastError)
	}

	// Verify round-2 prompts contain incremental context resolved from disk.
	var round2Prompts []string
	for _, opts := range *captured {
		if !isReviewHelper(opts.PermHandler) {
			continue
		}
		if strings.Contains(opts.LogPath, "iteration-02") {
			round2Prompts = append(round2Prompts, opts.Prompt)
		}
	}
	if len(round2Prompts) == 0 {
		t.Fatal("expected at least one round-2 review prompt")
	}

	foundPriorAxisReport := false
	foundDelta := false
	foundPriorAggregate := false
	for _, p := range round2Prompts {
		if strings.Contains(p, "## Prior Axis Report (Round N-1)") {
			foundPriorAxisReport = true
		}
		if strings.Contains(p, "## Delta Since Your Last Reviewed Round") {
			foundDelta = true
		}
		if strings.Contains(p, "## Prior Aggregate Feedback") {
			foundPriorAggregate = true
		}
	}
	if !foundPriorAxisReport {
		t.Error("restart-resume round-2 prompts: expected Prior Axis Report")
	}
	if !foundDelta {
		t.Error("restart-resume round-2 prompts: expected Delta section")
	}
	if !foundPriorAggregate {
		t.Error("restart-resume round-2 prompts: expected Prior Aggregate Feedback")
	}
}
