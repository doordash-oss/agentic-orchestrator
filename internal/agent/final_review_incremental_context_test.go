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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// TestFinalReviewIncrementalContext_TwoRoundLoop verifies that the Final
// Review loop's incremental-context wiring works end-to-end across a
// two-round loop: round-1 axes request changes, the fix agent writes a
// file (creating a real git diff), and round-2 axes receive their own
// round-1 report, the round-1 aggregate, and the per-repo delta — while
// round-1 prompts contain none of these sections.
func TestFinalReviewIncrementalContext_TwoRoundLoop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, repoPaths := newFRTestFeatureWithGitRepos(t, env.stateDir, "fr-incremental", []string{testRepoNameAPI})
	f.Pipeline = feature.PipelineMedium
	f.TraceID = "trace-fr-incremental"
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}

	// Review axes: CHANGES_REQUESTED on iteration-01, APPROVED on iteration-02.
	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review-axes.sh",
		fmt.Sprintf(`if [ -d "%s/iteration-02" ]; then
%s
%s
else
%s
%s
fi`,
			artDir,
			testutil.JSONLInit,
			writeFinalAxisFeedbackAllApproved(artDir),
			testutil.JSONLInit,
			writeFinalAxisFeedbackAllChangesRequested(artDir),
		)+"\n"+
			testutil.JSONLSuccess+"\n")

	// Fix agent writes a file to the repo worktree (creating a real diff).
	fixScript := testutil.WriteScript(t, env.scriptsDir, "fix.sh",
		testutil.JSONLInit+"\n"+
			fmt.Sprintf("echo 'package main' > '%s/fix.go'\n", repoPaths[testRepoNameAPI])+
			testutil.JSONLSuccess+"\n")

	bs, captured := capturingBuildSessionByModel(map[string]string{
		"reviewer": reviewScript,
		"agent":    fixScript,
	})

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	cfg := OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		Model:          "agent",
		ReviewModel:    "reviewer",
		MaxIterations:  5,
		MaxConsecFails: 3,
		BuildSession:   bs,
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("FinalStatus = %q, want review_passed; LastError=%q", result.FinalStatus, result.LastError)
	}
	if result.Iterations != 2 {
		t.Fatalf("Iterations = %d, want 2", result.Iterations)
	}

	// Collect reviewer prompts by iteration.
	var round1Prompts, round2Prompts []string
	for _, opts := range *captured {
		if opts.Model != "reviewer" {
			continue
		}
		// Determine which iteration this prompt belongs to by checking
		// the persisted review-prompt.md path.
		promptPath := opts.LogPath
		if strings.Contains(promptPath, "iteration-01") {
			round1Prompts = append(round1Prompts, opts.Prompt)
		} else if strings.Contains(promptPath, "iteration-02") {
			round2Prompts = append(round2Prompts, opts.Prompt)
		}
	}

	if len(round1Prompts) == 0 {
		t.Fatal("no round-1 reviewer prompts captured")
	}
	if len(round2Prompts) == 0 {
		t.Fatal("no round-2 reviewer prompts captured")
	}

	// Round-1 prompts must NOT contain incremental sections.
	for i, p := range round1Prompts {
		if strings.Contains(p, "## Prior Axis Report (Round N-1)") {
			t.Errorf("round-1 prompt %d contains Prior Axis Report section", i)
		}
		if strings.Contains(p, "## Delta Since Your Last Reviewed Round") {
			t.Errorf("round-1 prompt %d contains Delta section", i)
		}
	}

	// Round-2 prompts MUST contain incremental sections.
	for i, p := range round2Prompts {
		if !strings.Contains(p, "## Prior Axis Report (Round N-1)") {
			t.Errorf("round-2 prompt %d missing Prior Axis Report section", i)
		}
		if !strings.Contains(p, "## Delta Since Your Last Reviewed Round") {
			t.Errorf("round-2 prompt %d missing Delta section", i)
		}
		if !strings.Contains(p, "## Prior Aggregate Feedback") {
			t.Errorf("round-2 prompt %d missing Prior Aggregate Feedback section", i)
		}
		if !strings.Contains(p, "### "+testRepoNameAPI) {
			t.Errorf("round-2 prompt %d missing per-repo delta subsection for %s", i, testRepoNameAPI)
		}
	}

	// Verify persisted review-prompt.md artifacts.
	for _, axisSlug := range []string{"craft", "cleanliness", "qa"} {
		round1PromptPath := filepath.Join(artDir, "iteration-01", axisSlug, "review-prompt.md")
		round1Prompt, err := os.ReadFile(round1PromptPath)
		if err != nil {
			t.Fatalf("read round-1 prompt %s: %v", round1PromptPath, err)
		}
		if strings.Contains(string(round1Prompt), "## Prior Axis Report (Round N-1)") {
			t.Errorf("round-1 persisted prompt %s contains incremental section", axisSlug)
		}

		round2PromptPath := filepath.Join(artDir, "iteration-02", axisSlug, "review-prompt.md")
		round2Prompt, err := os.ReadFile(round2PromptPath)
		if err != nil {
			t.Fatalf("read round-2 prompt %s: %v", round2PromptPath, err)
		}
		round2Str := string(round2Prompt)
		if !strings.Contains(round2Str, "## Prior Axis Report (Round N-1)") {
			t.Errorf("round-2 persisted prompt %s missing Prior Axis Report", axisSlug)
		}
		if !strings.Contains(round2Str, "## Delta Since Your Last Reviewed Round") {
			t.Errorf("round-2 persisted prompt %s missing Delta section", axisSlug)
		}
		if !strings.Contains(round2Str, "### "+testRepoNameAPI) {
			t.Errorf("round-2 persisted prompt %s missing repo delta subsection", axisSlug)
		}
	}

	// Verify the aggregate feedback format is unchanged (strict union).
	aggregatePath := filepath.Join(artDir, "iteration-01", "review-feedback.md")
	aggregate, err := os.ReadFile(aggregatePath)
	if err != nil {
		t.Fatalf("read aggregate feedback: %v", err)
	}
	aggStr := string(aggregate)
	if !strings.Contains(aggStr, "### Craft") {
		t.Error("aggregate feedback missing Craft axis section")
	}
	if !strings.Contains(aggStr, "### Cleanliness") {
		t.Error("aggregate feedback missing Cleanliness axis section")
	}
	if !strings.Contains(aggStr, "### QA") {
		t.Error("aggregate feedback missing QA axis section")
	}
	if !strings.Contains(aggStr, "## Verdict\nCHANGES_REQUESTED") {
		t.Error("aggregate feedback should be CHANGES_REQUESTED for round 1")
	}
}

// TestFinalReviewIncrementalContext_RestartResume verifies that round-2
// incremental context resolves identically when the process is
// interrupted after round 1 and resumed.
func TestFinalReviewIncrementalContext_RestartResume(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, repoPaths := newFRTestFeatureWithGitRepos(t, env.stateDir, "fr-incremental-restart", []string{testRepoNameAPI})
	f.Pipeline = feature.PipelineMedium
	f.TraceID = "trace-fr-incremental-restart"
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}

	// Review: CHANGES_REQUESTED on iter-01, APPROVED on iter-02+.
	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review-axes.sh",
		fmt.Sprintf(`if [ -d "%s/iteration-02" ]; then
%s
%s
else
%s
%s
fi`,
			artDir,
			testutil.JSONLInit,
			writeFinalAxisFeedbackAllApproved(artDir),
			testutil.JSONLInit,
			writeFinalAxisFeedbackAllChangesRequested(artDir),
		)+"\n"+
			testutil.JSONLSuccess+"\n")

	fixScript := testutil.WriteScript(t, env.scriptsDir, "fix.sh",
		testutil.JSONLInit+"\n"+
			fmt.Sprintf("echo 'package main' > '%s/fix.go'\n", repoPaths[testRepoNameAPI])+
			testutil.JSONLSuccess+"\n")

	// Phase 1: Run the full loop to completion and capture round-2 prompts.
	bs1, captured1 := capturingBuildSessionByModel(map[string]string{
		"reviewer": reviewScript,
		"agent":    fixScript,
	})
	eventCh1 := make(chan any, 100)
	sm1 := session.NewManager(eventCh1)
	defer sm1.Shutdown()

	cfg1 := OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		Model:          "agent",
		ReviewModel:    "reviewer",
		MaxIterations:  5,
		MaxConsecFails: 3,
		BuildSession:   bs1,
	}

	result1, err := RunFeatureFinalReviewLoop(cfg1, sm1)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if result1.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("first run FinalStatus = %q, want review_passed", result1.FinalStatus)
	}

	// Capture round-2 prompts from the first (no-crash) run.
	firstRound2Prompts := make(map[string]string) // axisSlug -> prompt
	for _, opts := range *captured1 {
		if opts.Model != "reviewer" {
			continue
		}
		promptPath := opts.LogPath
		if !strings.Contains(promptPath, "iteration-02") {
			continue
		}
		// Extract axis slug from the path: .../iteration-02/<axis>/review-output.txt
		parts := strings.Split(filepath.Dir(promptPath), string(filepath.Separator))
		axisSlug := parts[len(parts)-1]
		firstRound2Prompts[axisSlug] = opts.Prompt
	}

	// Phase 2: Reset the feature to pre-loop state and re-run, simulating
	// a restart after round-1 completed (iteration-01 dir + meta exist on disk).
	// We need a fresh feature state with the same repos but no iteration dirs.
	env2 := newFRLoopEnv(t)
	store2, f2, repoPaths2 := newFRTestFeatureWithGitRepos(t, env2.stateDir, "fr-incremental-restart", []string{testRepoNameAPI})
	f2.Pipeline = feature.PipelineMedium
	f2.TraceID = "trace-fr-incremental-restart"
	if err := store2.Save(f2); err != nil {
		t.Fatalf("save feature 2: %v", err)
	}

	artDir2 := frArtifactDir(env2.stateDir, f2)
	if err := os.MkdirAll(artDir2, 0o755); err != nil {
		t.Fatalf("mkdir artifact 2: %v", err)
	}

	// Seed iteration-01 on disk with CHANGES_REQUESTED meta and anchors,
	// simulating a completed round 1 that was interrupted before round 2.
	iter01 := filepath.Join(artDir2, "iteration-01")
	if err := os.MkdirAll(iter01, 0o755); err != nil {
		t.Fatalf("mkdir iter01: %v", err)
	}
	am := NewArtifactManager(artDir2)

	// Write a file to the repo and commit so the anchor has a real head SHA.
	writeChangeToFile(t, repoPaths2[testRepoNameAPI], "fix.go", "package main\n")
	headSHA, err := git.CommitAllAndGetHead(repoPaths2[testRepoNameAPI], "Final review iteration 1: changes requested")
	if err != nil {
		t.Fatalf("commit for anchor: %v", err)
	}
	baseSHA, err := git.CurrentHeadSHA(repoPaths2[testRepoNameAPI])
	if err != nil {
		t.Fatalf("get base SHA: %v", err)
	}

	if err := am.WriteMeta(iter01, IterationMeta{
		Iteration:    1,
		ReviewStatus: "changes_requested",
		StartedAt:    time.Now(),
		Anchors: RepoAnchors{
			testRepoNameAPI: RepoAnchor{Base: baseSHA, Head: headSHA},
		},
	}); err != nil {
		t.Fatalf("write iter01 meta: %v", err)
	}

	// Write the aggregate feedback for round 1.
	aggFeedback := FormatStructuredReviewFeedback("Multi-Axis Final Review",
		"### Craft\n- **High**: Craft issues\n### Cleanliness\n- **High**: Cleanliness issues\n### QA\n- **High**: QA issues",
		"", ReviewChangesRequested)
	if err := os.WriteFile(filepath.Join(iter01, "review-feedback.md"), []byte(aggFeedback), 0o644); err != nil {
		t.Fatalf("write aggregate feedback: %v", err)
	}

	// Write per-axis round-1 feedback files.
	for _, axisSlug := range []string{"craft", "cleanliness", "qa"} {
		axisDir := filepath.Join(iter01, axisSlug)
		if err := os.MkdirAll(axisDir, 0o755); err != nil {
			t.Fatalf("mkdir axis dir: %v", err)
		}
		axisFeedback := FormatStructuredReviewFeedback(
			fmt.Sprintf("%s Final Review", axisSlug),
			fmt.Sprintf("- **High**: %s finding", axisSlug),
			"", ReviewChangesRequested)
		if err := os.WriteFile(filepath.Join(axisDir, "review-feedback.md"), []byte(axisFeedback), 0o644); err != nil {
			t.Fatalf("write axis feedback: %v", err)
		}
	}

	// Now run the loop — it should resume at iteration-02.
	reviewScript2 := testutil.WriteScript(t, env2.scriptsDir, "review-axes.sh",
		testutil.JSONLInit+"\n"+
			writeFinalAxisFeedbackAllApproved(artDir2)+"\n"+
			testutil.JSONLSuccess+"\n")

	bs2, captured2 := capturingBuildSessionByModel(map[string]string{
		"reviewer": reviewScript2,
	})
	eventCh2 := make(chan any, 100)
	sm2 := session.NewManager(eventCh2)
	defer sm2.Shutdown()

	cfg2 := OrchestratorConfig{
		Feature:        f2,
		FeatureStore:   store2,
		StateDir:       env2.stateDir,
		Model:          "agent",
		ReviewModel:    "reviewer",
		MaxIterations:  5,
		MaxConsecFails: 3,
		BuildSession:   bs2,
	}

	result2, err := RunFeatureFinalReviewLoop(cfg2, sm2)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if result2.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("second run FinalStatus = %q, want review_passed", result2.FinalStatus)
	}
	if result2.Iterations != 2 {
		t.Errorf("second run Iterations = %d, want 2 (resumed from iter-01)", result2.Iterations)
	}

	// Verify round-2 prompts from the restart run contain incremental context.
	for _, opts := range *captured2 {
		if opts.Model != "reviewer" {
			continue
		}
		promptPath := opts.LogPath
		if !strings.Contains(promptPath, "iteration-02") {
			continue
		}
		p := opts.Prompt
		if !strings.Contains(p, "## Prior Axis Report (Round N-1)") {
			t.Error("restart round-2 prompt missing Prior Axis Report")
		}
		if !strings.Contains(p, "## Delta Since Your Last Reviewed Round") {
			t.Error("restart round-2 prompt missing Delta section")
		}
		if !strings.Contains(p, "### "+testRepoNameAPI) {
			t.Error("restart round-2 prompt missing repo delta subsection")
		}
		if !strings.Contains(p, "## Prior Aggregate Feedback") {
			t.Error("restart round-2 prompt missing Prior Aggregate Feedback")
		}
	}
}

// writeFinalAxisFeedbackAllApproved writes APPROVED feedback for all
// final-gate axes in the latest iteration directory.
func writeFinalAxisFeedbackAllApproved(artDir string) string {
	return writeFinalAxisFeedbackWithVerdict(artDir, "APPROVED", "- (none)")
}

// writeFinalAxisFeedbackAllChangesRequested writes CHANGES_REQUESTED
// feedback for all final-gate axes in the latest iteration directory.
func writeFinalAxisFeedbackAllChangesRequested(artDir string) string {
	return writeFinalAxisFeedbackWithVerdict(artDir, "CHANGES_REQUESTED", "- **High**: issues found")
}

func writeFinalAxisFeedbackWithVerdict(artDir, verdict, findings string) string {
	return fmt.Sprintf(`for _prompt in $(find "%s" -mindepth 3 -maxdepth 3 -name review-prompt.md -type f 2>/dev/null); do
  _dir=$(dirname "$_prompt")
  _axis=$(basename "$_dir")
  _fb="$_dir/review-feedback.md"
  if [ -f "$_fb" ]; then continue; fi
  _tmp="$_fb.tmp.$$"
  cat > "$_tmp" << REVIEWEOF
## Findings
%s

## Suggestions
- (none)

## Verdict
%s
REVIEWEOF
  mv "$_tmp" "$_fb"
done`, artDir, findings, verdict)
}
