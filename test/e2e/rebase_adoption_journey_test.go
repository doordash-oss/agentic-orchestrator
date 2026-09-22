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

// End-to-end journeys of foreign-commit adoption: a published three-layer
// stack whose layer 2 remote branch gained reviewer work refuses Publish
// with the remote-diverged record naming the rebase pass, the rebase pass
// launches on divergence alone, adopts the reviewer's commits onto the
// replayed layer, and the closure tail republishes over the adopted tip.
// Variants cover the no-local-change shape, the merge-only divergence, a
// conflicting reviewer commit resolved by a scripted session, and a second
// reviewer push after preflight leaving the tail's republish refused.

package e2e

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// rewriteLayer2ThroughRoundCommit rewrites layer 2 locally so its tip
// differs from its last-pushed SHA: l2.txt is dirtied in the parent worktree
// and committed through the real round-commit hook with a fix manifest
// naming layer 2, so the hook's relocation lands the fix on layer 2's branch
// exactly like a Final Review fix round.
func (fx *rebaseHarnessJourney) rewriteLayer2ThroughRoundCommit(t *testing.T) {
	t.Helper()
	writeJourneyFile(t, fx.repoDir, "l2.txt", "layer2 v2\nlocal fix\n")
	iterDir := filepath.Join(fx.store.BaseDir, fx.parentID, "final-review", "iterations", "001")
	if err := os.MkdirAll(iterDir, 0o755); err != nil {
		t.Fatalf("mkdir iteration dir: %v", err)
	}
	manifest := "entries:\n" +
		"  - layer: 2\n" +
		"    repository: repo-a\n" +
		"    paths:\n" +
		"      - l2.txt\n"
	if err := os.WriteFile(filepath.Join(iterDir, agent.FixManifestFilename), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write fix manifest: %v", err)
	}
	if fx.pr == nil || fx.pr.RoundCommitHook == nil {
		t.Fatal("the orchestrator never wired the round-commit hook")
	}
	if err := fx.pr.RoundCommitHook(agent.RoundCommitInput{
		FeatureID:       fx.parentID,
		Iteration:       1,
		Kind:            agent.RoundCommitFinalReviewFix,
		FixNumber:       1,
		FixIterationDir: iterDir,
		IterationDir:    iterDir,
		Repos:           map[string]string{"repo-a": fx.repoDir},
	}); err != nil {
		t.Fatalf("round-commit hook: %v", err)
	}
}

// assertAdoptedRecord pins one adopted reviewer commit's relationship record
// and the preserved identity of its replayed copy.
func assertAdoptedRecord(t *testing.T, child *feature.Feature, originalSHA, wantSubject, wantAuthor string) feature.RebaseAdoptedCommit {
	t.Helper()
	if len(child.Parent.RebaseRestacks) != 1 {
		t.Fatalf("restacks = %+v, want one entry for repo-a", child.Parent.RebaseRestacks)
	}
	adopted := child.Parent.RebaseRestacks[0].AdoptedCommits
	if len(adopted) != 1 {
		t.Fatalf("adopted commits = %+v, want exactly the reviewer commit", adopted)
	}
	ac := adopted[0]
	if ac.OriginalSHA != originalSHA || ac.Dropped || ac.NewSHA == "" {
		t.Fatalf("adopted commit = %+v, want the reviewer commit %s landed with a new SHA", ac, originalSHA)
	}
	if ac.LayerPosition != 2 || ac.Subject != wantSubject || ac.Author != wantAuthor {
		t.Fatalf("adopted commit identity = %+v, want layer 2 with the reviewer's subject and author", ac)
	}
	return ac
}

// TestRebaseAdoptionMainJourney drives the main foreign-commit journey: the
// published stack's layer 2 remote branch gains a reviewer commit, layer 2
// is rewritten locally through the round-commit hook, Publish refuses with
// the remote-diverged record naming layer 2 and the rebase-pass remediation
// while the remote stays untouched, and the rebase pass — launched although
// the parent is neither behind nor holds a merged layer — adopts the
// reviewer's commit, republishes over it, and settles cleanly.
func TestRebaseAdoptionMainJourney(t *testing.T) {
	fx := newRebaseHarnessJourney(t, rebaseHarnessOpts{UpToDate: true, ReviewerCommitLayer2: true})

	// Rewrite layer 2 locally so its tip differs from its last-pushed SHA.
	fx.rewriteLayer2ThroughRoundCommit(t)
	parent := fx.reloadParent()
	localFixTip := parent.Stack[1].Repos["repo-a"].TipSHA
	if localFixTip == "" || localFixTip == fx.layerTips[1] {
		t.Fatalf("layer 2 local tip = %q, want the round-committed fix beyond the pushed tip %s", localFixTip, fx.layerTips[1])
	}

	// Publish refuses: the remote-diverged record names layer 2, its
	// rendered remediation names the rebase pass, and the remote branch is
	// untouched.
	status, payload := postActionStatus(t, fx.srv.URL, fx.parentID, "publish", `{}`)
	if status == http.StatusOK {
		t.Fatalf("publish action status = %d, want a refusal; body: %s", status, payload)
	}
	parent = fx.reloadParent()
	state := parent.RepoStates["repo-a"]
	if state == nil || state.Error == nil || state.Error.Code != errcat.PublishRemoteDiverged {
		t.Fatalf("repo-a stored record = %+v, want publish_remote_diverged", state)
	}
	repoBlock := state.Error.Context.Repositories[0]
	if repoBlock.LayerPosition != 2 || repoBlock.LayerTitle != "Layer two" {
		t.Fatalf("stored record block = %+v, want layer 2 (Layer two) named", repoBlock)
	}
	rendered := errcat.RenderRecord(*state.Error)
	if rendered.Remediation == nil || !strings.Contains(rendered.Remediation.Hint, "rebase pass") {
		t.Fatalf("rendered remediation = %+v, want the rebase-pass remediation", rendered.Remediation)
	}
	if got := journeyGit(t, fx.bareRemote, "rev-parse", "refs/heads/stack/2"); got != fx.reviewerTip {
		t.Fatalf("remote stack/2 = %s, want the untouched reviewer tip %s", got, fx.reviewerTip)
	}

	// Launch the pass: the parent is neither behind nor holds a merged
	// layer, yet the launch succeeds on divergence alone.
	resp, err := fx.client.RebaseFeature(t.Context(), fx.parentID)
	if err != nil {
		t.Fatalf("RebaseFeature() error = %v, want launch on divergence alone", err)
	}
	childID := resp.FeatureID
	child := fx.loadChild(childID)
	if len(child.Parent.RebaseWorkRepos) != 1 || child.Parent.RebaseWorkRepos[0] != "repo-a" {
		t.Fatalf("work repos = %+v, want [repo-a] by divergence alone", child.Parent.RebaseWorkRepos)
	}
	var layer2Class *feature.RebaseLayerClassification
	for i := range child.Parent.RebaseLayerStates {
		c := &child.Parent.RebaseLayerStates[i]
		if c.LayerPosition == 2 {
			layer2Class = c
		}
	}
	if layer2Class == nil || !layer2Class.Diverged || layer2Class.RemoteTip != fx.reviewerTip {
		t.Fatalf("layer 2 classification = %+v, want diverged at the observed reviewer tip %s", layer2Class, fx.reviewerTip)
	}
	if len(layer2Class.ForeignCommits) != 1 || layer2Class.ForeignCommits[0].SHA != fx.reviewerTip {
		t.Fatalf("layer 2 foreign commits = %+v, want the reviewer commit %s", layer2Class.ForeignCommits, fx.reviewerTip)
	}

	// The start response reports the restack dispatch; the scripted Final
	// Review approves, integration lands, and the closure tail republishes.
	waitForJourneySetupComplete(t, fx.srv.URL, childID)
	startResp := postActionJSON(t, fx.srv.URL, childID, "restart", `{}`)
	if startResp["dispatch"] != "restack" {
		t.Fatalf("restart response dispatch = %v, want restack", startResp["dispatch"])
	}
	waitForJourneyChildClosed(t, fx.srv.URL, fx.store, childID)
	fx.waitForRebaseTailSettled(childID)

	child = fx.loadChild(childID)
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed", child.Parent.CloseOutcome)
	}
	ac := assertAdoptedRecord(t, child, fx.reviewerTip, "reviewer fix one", "Reviewer One <reviewer1@example.com>")

	// The remote's layer 2 branch contains the reviewer's file change and
	// the local fix; layer 2's local tip equals the remote tip.
	parent = fx.reloadParent()
	layer2Tip := parent.Stack[1].Repos["repo-a"].TipSHA
	layer3Tip := parent.Stack[2].Repos["repo-a"].TipSHA
	if layer2Tip == "" || layer3Tip == "" {
		t.Fatalf("rebuilt tips missing: layer2=%q layer3=%q", layer2Tip, layer3Tip)
	}
	if got := journeyGit(t, fx.repoDir, "rev-parse", "stack/2"); got != layer2Tip {
		t.Fatalf("local stack/2 = %s, want the rebuilt tip %s", got, layer2Tip)
	}
	if got := journeyGit(t, fx.bareRemote, "rev-parse", "refs/heads/stack/2"); got != layer2Tip {
		t.Fatalf("remote stack/2 = %s, want the rebuilt tip %s (the local tip)", got, layer2Tip)
	}
	if got := journeyGit(t, fx.bareRemote, "show", "refs/heads/stack/2:review.txt"); got != "reviewer fix" {
		t.Fatalf("remote layer 2 review.txt = %q, want the reviewer's file change", got)
	}
	if got := journeyGit(t, fx.bareRemote, "show", "refs/heads/stack/2:l2.txt"); got != "layer2 v2\nlocal fix" {
		t.Fatalf("remote layer 2 l2.txt = %q, want the local fix's content", got)
	}

	// Layer 3's remote branch is replayed above layer 2 with linear history
	// from the base.
	if !git.IsAncestor(fx.repoDir, fx.forkSHA, layer2Tip) || !git.IsAncestor(fx.repoDir, layer2Tip, layer3Tip) {
		t.Fatal("the rebuilt refs do not form a linear chain from the base through layer 2 to layer 3")
	}
	if got := journeyGit(t, fx.bareRemote, "rev-parse", "refs/heads/stack/3"); got != layer3Tip {
		t.Fatalf("remote stack/3 = %s, want the rebuilt tip %s", got, layer3Tip)
	}

	// The reviewer's original SHA is no longer reachable from any layer
	// branch while its author and message are preserved on the adopted copy.
	for _, branch := range []string{"stack/1", "stack/2", "stack/3"} {
		if git.IsAncestor(fx.repoDir, fx.reviewerTip, branch) {
			t.Fatalf("the reviewer's original commit is still reachable from %s", branch)
		}
	}
	if got := journeyGit(t, fx.repoDir, "log", "-1", "--format=%an <%ae>", ac.NewSHA); got != "Reviewer One <reviewer1@example.com>" {
		t.Fatalf("adopted copy author = %q, want the reviewer's identity preserved", got)
	}
	if got := journeyGit(t, fx.repoDir, "log", "-1", "--format=%s", ac.NewSHA); got != "reviewer fix one" {
		t.Fatalf("adopted copy subject = %q, want the reviewer's message preserved", got)
	}

	// The parent's stack records layer 2's last-pushed SHA equal to its new
	// tip, the stored remote-diverged record is cleared, and every open
	// pull request body carries a refreshed stack section.
	entry := parent.Stack[1].Repos["repo-a"]
	if entry.LastPushedSHA != entry.TipSHA {
		t.Fatalf("layer 2 last-pushed SHA = %s, want the new tip %s", entry.LastPushedSHA, entry.TipSHA)
	}
	if state := parent.RepoStates["repo-a"]; state != nil && state.Error != nil {
		t.Fatalf("repo-a stored record = %+v, want cleared by the successful republish", state.Error)
	}
	for _, num := range []int{fx.prNum1, fx.prNum2, fx.prNum3} {
		pr, ok := fx.pulls.Pull("repo-a", num)
		if !ok || !strings.Contains(pr.Body, "## Stack") {
			t.Fatalf("pull request %d body does not carry a refreshed stack section:\n%s", num, pr.Body)
		}
	}
}

// TestRebaseAdoptionNoLocalChangeJourney drives the no-local-change variant:
// with layer 2's tip still equal to its last-pushed SHA, Publish pushes
// nothing and reports nothing, and the pass still launches on divergence
// alone, adopts the reviewer's commit, and republishes it.
func TestRebaseAdoptionNoLocalChangeJourney(t *testing.T) {
	fx := newRebaseHarnessJourney(t, rebaseHarnessOpts{UpToDate: true, ReviewerCommitLayer2: true})

	status, payload := postActionStatus(t, fx.srv.URL, fx.parentID, "publish", `{}`)
	if status != http.StatusOK || !strings.Contains(string(payload), `"published"`) {
		t.Fatalf("publish action status = %d body = %s, want 200 published (nothing to push)", status, payload)
	}
	parent := fx.reloadParent()
	if state := parent.RepoStates["repo-a"]; state != nil && state.Error != nil {
		t.Fatalf("repo-a stored record = %+v, want none (nothing was pushed)", state.Error)
	}
	if got := journeyGit(t, fx.bareRemote, "rev-parse", "refs/heads/stack/2"); got != fx.reviewerTip {
		t.Fatalf("remote stack/2 = %s, want the untouched reviewer tip %s", got, fx.reviewerTip)
	}

	childID := fx.driveToCompletion(t)
	child := fx.loadChild(childID)
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed", child.Parent.CloseOutcome)
	}
	ac := assertAdoptedRecord(t, child, fx.reviewerTip, "reviewer fix one", "Reviewer One <reviewer1@example.com>")

	// The reviewer's commit was adopted and republished: the remote's layer
	// 2 branch holds the rebuilt tip containing the adopted copy.
	parent = fx.reloadParent()
	layer2Tip := parent.Stack[1].Repos["repo-a"].TipSHA
	if layer2Tip == "" {
		t.Fatal("layer 2 rebuilt tip missing")
	}
	if got := journeyGit(t, fx.bareRemote, "rev-parse", "refs/heads/stack/2"); got != layer2Tip {
		t.Fatalf("remote stack/2 = %s, want the rebuilt tip %s", got, layer2Tip)
	}
	if got := journeyGit(t, fx.bareRemote, "show", "refs/heads/stack/2:review.txt"); got != "reviewer fix" {
		t.Fatalf("remote layer 2 review.txt = %q, want the adopted reviewer change", got)
	}
	if git.IsAncestor(fx.repoDir, fx.reviewerTip, "stack/2") {
		t.Fatal("the reviewer's original commit is still reachable from the rebuilt layer 2 branch")
	}
	if got := journeyGit(t, fx.repoDir, "log", "-1", "--format=%an <%ae>", ac.NewSHA); got != "Reviewer One <reviewer1@example.com>" {
		t.Fatalf("adopted copy author = %q, want the reviewer's identity preserved", got)
	}
}

// TestRebaseAdoptionMergeOnlyJourney drives the merge-only variant: the only
// remote-only commit on layer 2 is a content-free "Update branch" merge of
// the base, so the layer diverges with nothing to adopt, the pass lands
// with no adopted commit, and the tail republishes over the merge — the
// remote replaced by the rebuilt tip with the merge's SHA unreachable.
func TestRebaseAdoptionMergeOnlyJourney(t *testing.T) {
	fx := newRebaseHarnessJourney(t, rebaseHarnessOpts{UpToDate: true, ReviewerMergeOnlyLayer2: true})

	childID := fx.driveToCompletion(t)
	child := fx.loadChild(childID)
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed", child.Parent.CloseOutcome)
	}
	if len(child.Parent.RebaseRestacks) != 1 {
		t.Fatalf("restacks = %+v, want one entry for repo-a", child.Parent.RebaseRestacks)
	}
	rs := child.Parent.RebaseRestacks[0]
	if len(rs.AdoptedCommits) != 0 {
		t.Fatalf("adopted commits = %+v, want none (the merge is never adopted)", rs.AdoptedCommits)
	}
	if rs.SkippedMergeCommits != 1 {
		t.Fatalf("skipped merge commits = %d, want 1 (the Update branch merge)", rs.SkippedMergeCommits)
	}
	if !strings.Contains(child.Description, "1 remote-only merge commit(s) were skipped") {
		t.Fatalf("description does not name the skipped merge count:\n%s", child.Description)
	}

	// The tail republished over the merge: the remote holds the rebuilt tip
	// and the merge's SHA is unreachable from every layer branch.
	parent := fx.reloadParent()
	layer2Tip := parent.Stack[1].Repos["repo-a"].TipSHA
	if layer2Tip == "" {
		t.Fatal("layer 2 rebuilt tip missing")
	}
	if got := journeyGit(t, fx.bareRemote, "rev-parse", "refs/heads/stack/2"); got != layer2Tip {
		t.Fatalf("remote stack/2 = %s, want the rebuilt tip %s (replaced over the merge)", got, layer2Tip)
	}
	for _, branch := range []string{"stack/1", "stack/2", "stack/3"} {
		if git.IsAncestor(fx.repoDir, fx.reviewerTip, branch) {
			t.Fatalf("the merge commit %s is still reachable from %s", fx.reviewerTip, branch)
		}
	}
}

// TestRebaseAdoptionConflictingReviewerCommitJourney drives the conflicting
// variant: the reviewer's commit touches the same file as the locally
// round-committed fix, so its adoption conflicts and runs through the
// scripted resolution session; the pass lands with the resolved content and
// one recorded resolution.
func TestRebaseAdoptionConflictingReviewerCommitJourney(t *testing.T) {
	fx := newRebaseHarnessJourney(t, rebaseHarnessOpts{
		UpToDate:                  true,
		ReviewerCommitLayer2:      true,
		ReviewerConflictingCommit: true,
		resolutionScript: func(prompt string) string {
			return "printf 'locally fixed with reviewer take\\n' > l2.txt"
		},
	})
	fx.rewriteLayer2ThroughRoundCommit(t)

	childID := fx.driveToCompletion(t)
	child := fx.loadChild(childID)
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed", child.Parent.CloseOutcome)
	}

	// One recorded resolution for the conflicting foreign commit.
	if len(child.Parent.RebaseRestacks) != 1 {
		t.Fatalf("restacks = %+v, want one entry for repo-a", child.Parent.RebaseRestacks)
	}
	rs := child.Parent.RebaseRestacks[0]
	if len(rs.ResolvedConflicts) != 1 || rs.ResolvedConflicts[0].Commit != fx.reviewerTip || rs.ResolvedConflicts[0].Dropped {
		t.Fatalf("resolved conflicts = %+v, want the reviewer commit landed after resolution", rs.ResolvedConflicts)
	}
	ac := assertAdoptedRecord(t, child, fx.reviewerTip, "reviewer fix one", "Reviewer One <reviewer1@example.com>")

	// The adopted copy carries the resolved content and the reviewer's
	// identity, and the remote was republished to the rebuilt tip.
	if got := journeyGit(t, fx.repoDir, "show", ac.NewSHA+":l2.txt"); got != "locally fixed with reviewer take" {
		t.Fatalf("resolved adoption content = %q, want the scripted resolution", got)
	}
	parent := fx.reloadParent()
	layer2Tip := parent.Stack[1].Repos["repo-a"].TipSHA
	if layer2Tip == "" || layer2Tip != ac.NewSHA {
		t.Fatalf("layer 2 rebuilt tip = %q, want the resolved adoption %s", layer2Tip, ac.NewSHA)
	}
	if got := journeyGit(t, fx.bareRemote, "rev-parse", "refs/heads/stack/2"); got != layer2Tip {
		t.Fatalf("remote stack/2 = %s, want the rebuilt tip %s", got, layer2Tip)
	}
}

// TestRebaseAdoptionPostPreflightPushJourney drives the post-preflight push
// variant: a second reviewer commit lands on the same branch after the
// preflight observed the first, so the closure pins the first observed tip,
// the tail's republish is refused with the diverged record, the remote stays
// at the second commit, and the child still closes completed with the tail
// settled.
func TestRebaseAdoptionPostPreflightPushJourney(t *testing.T) {
	fx := newRebaseHarnessJourney(t, rebaseHarnessOpts{UpToDate: true, ReviewerCommitLayer2: true})

	// Launch: the preflight observes the first reviewer tip.
	resp, err := fx.client.RebaseFeature(t.Context(), fx.parentID)
	if err != nil {
		t.Fatalf("RebaseFeature() error = %v", err)
	}
	childID := resp.FeatureID
	waitForJourneySetupComplete(t, fx.srv.URL, childID)

	// The second reviewer commit lands after the preflight.
	lateTip := fx.pushReviewerCommitOntoLayer2(t, "review2.txt", "late reviewer fix\n", "reviewer fix two", "Reviewer Two", "reviewer2@example.com")

	postAction(t, fx.srv.URL, childID, "start", `{}`)
	waitForJourneyChildClosed(t, fx.srv.URL, fx.store, childID)
	fx.waitForRebaseTailSettled(childID)

	child := fx.loadChild(childID)
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed", child.Parent.CloseOutcome)
	}
	// The relationship still lists the adopted first commit.
	assertAdoptedRecord(t, child, fx.reviewerTip, "reviewer fix one", "Reviewer One <reviewer1@example.com>")

	// The tail's republish was refused: the diverged record is stored and
	// the remote keeps the second reviewer commit.
	parent := fx.reloadParent()
	state := parent.RepoStates["repo-a"]
	if state == nil || state.Error == nil || state.Error.Code != errcat.PublishRemoteDiverged {
		t.Fatalf("repo-a stored record = %+v, want publish_remote_diverged after the post-preflight push", state)
	}
	if got := journeyGit(t, fx.bareRemote, "rev-parse", "refs/heads/stack/2"); got != lateTip {
		t.Fatalf("remote stack/2 = %s, want the second reviewer commit %s untouched", got, lateTip)
	}
}
