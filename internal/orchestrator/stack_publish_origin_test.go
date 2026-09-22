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

package orchestrator_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// ---------------------------------------------------------------------------
// Appended-layer publish — fixtures
// ---------------------------------------------------------------------------

const (
	originChildFeatureName = "child refactor pass"
	originChildDescription = "Refactor the delivery path."
	originRowOneRationale  = "Child layer one rationale."
	originRowTwoRationale  = "Child layer two rationale."
)

// newStackedPublishRepoChain builds a real git repository wired to a bare
// origin with one branch and one commit per given branch name, each stacked
// on the previous one, and returns the per-branch tips. The worktree is
// left on the last branch, matching the parent's post-closure top layer.
func newStackedPublishRepoChain(t *testing.T, branches ...string) (string, []string) {
	t.Helper()
	repoPath, _ := testutil.InitPublishReadyGitRepo(t)
	tips := make([]string, 0, len(branches))
	for i, branch := range branches {
		testutil.CreateBranch(t, repoPath, branch)
		tip := testutil.CommitFile(t, repoPath,
			fmt.Sprintf("layer-%d.txt", i+1),
			fmt.Sprintf("layer %d\n", i+1),
			fmt.Sprintf("layer %d", i+1))
		tips = append(tips, tip)
	}
	return repoPath, tips
}

// writeChildRoadmap writes the origin child's roadmap artifact — a two-row
// `## Pull Requests` table — and returns its absolute path.
func writeChildRoadmap(t *testing.T) string {
	t.Helper()
	content := `# Roadmap

## Phase 1: First slice

### Goal

Ship the first slice.

## Phase 2: Second slice

### Goal

Ship the second slice.

## Pull Requests

| # | Title | Phases | Rationale |
|---|---|---|---|
| 1 | Child first | 1 | Child layer one rationale. |
| 2 | Child second | 2 | Child layer two rationale. |

## Overall Exit Criteria

- Done.
`
	path := filepath.Join(t.TempDir(), "roadmap.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write child roadmap: %v", err)
	}
	return path
}

// stackPublishLifecycleWithOrigin wires stackPublishLifecycle's parent
// stack writes plus a Get that dispatches by feature id, so the walk's
// origin lookups load the child feature through the same lifecycle the
// publish pass uses. A nil child makes every non-parent lookup fail, the
// shape of a parent whose origin feature is gone.
func stackPublishLifecycleWithOrigin(f, child *feature.Feature) *mocks.MockFeatureLifecycle {
	lc := stackPublishLifecycle(f)
	lc.GetFn = func(id string) (*feature.Feature, error) {
		if id == f.ID {
			return f, nil
		}
		if child != nil && id == child.ID {
			return child, nil
		}
		return nil, errors.New("feature not found: " + id)
	}
	return lc
}

// newPromptRecordingDescriptionPhaseRunner scripts the description session
// with a canned body while recording each session's prompt, so tests can
// assert exactly what the description generator was asked to describe.
func newPromptRecordingDescriptionPhaseRunner(t *testing.T, body string) (*agent.PhaseRunner, *[]string) {
	t.Helper()
	var prompts []string
	newSession := func() *publishDescriptionSessionHandle {
		sess := newPublishDescriptionSessionHandle()
		sess.msgLog.Append(mocks.AssistantTextMessage(body))
		sess.result = &llm.ResultMessage{
			Type:       "result",
			Subtype:    "success",
			Result:     "done",
			StopReason: "end_turn",
		}
		sess.statusCh <- "SUCCESS"
		return sess
	}

	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		sess := newSession()
		sess.id = id
		sess.featureID = featureID
		sess.phase = phase
		return sess, nil
	}

	pr := &agent.PhaseRunner{
		SessionManager: sm,
		StateDir:       t.TempDir(),
	}
	pr.BuildSessionFn = func(opts agent.BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
		prompts = append(prompts, opts.Prompt)
		return []string{"mock"}, nil, &ports.SessionOpts{RepoName: opts.RepoName}, nil
	}
	return pr, &prompts
}

// openLayerEntry shapes a delivered roadmap layer entry: a recorded open
// pull request whose tip equals its last-pushed SHA, so the walk refreshes
// it without pushing.
func openLayerEntry(tip, prURL string) feature.StackRepoEntry {
	return feature.StackRepoEntry{
		TipSHA:        tip,
		LastPushedSHA: tip,
		PRURL:         prURL,
		PRState:       feature.StackPRStateOpen,
	}
}

// stackSectionWrites returns the fake API request lines whose bodies carry
// a stack section, so tests can assert what was injected into which PR.
func stackSectionWrites(fake *testutil.FakeGitHubAPI) []string {
	var out []string
	for _, line := range fake.Requests() {
		if strings.Contains(line, "## Stack") {
			out = append(out, line)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Appended-layer publish — the walk
// ---------------------------------------------------------------------------

// A parent whose stack has two roadmap-derived layers with open PRs and
// two appended layers: only the appended branches are pushed, their PRs
// chain onto the previous top's branch and then the first appended branch,
// every open PR's stack section lists all four layers, and the description
// prompt for an appended layer names the origin child's feature and its
// roadmap rationale while the stack listing still covers the whole parent
// stack.
func TestPublishStackAppendedLayersDescribedFromOrigin(t *testing.T) {
	branches := [4]string{
		"feature/stack-o/1-foundation",
		"feature/stack-o/2-fix-auth",
		"feature/stack-o/3-child-first",
		"feature/stack-o/4-child-second",
	}
	repoPath, tips := newStackedPublishRepoChain(t, branches[:]...)
	child := &feature.Feature{
		ID:          "feat-origin-child",
		Name:        originChildFeatureName,
		Description: originChildDescription,
		Artifacts:   map[string]string{"roadmap": writeChildRoadmap(t)},
	}
	f := &feature.Feature{
		ID:     "feat-stack-origin",
		Name:   "parent origin walk",
		Slug:   "stack-origin",
		Status: feature.StatusReviewPassed,
		Stack: []feature.StackLayer{
			{
				Position: 1, Title: "Foundation", Slug: "foundation", Phases: []int{1}, Branch: branches[0],
				Repos: map[string]feature.StackRepoEntry{
					"r1": openLayerEntry(tips[0], "https://github.com/org/r1/pull/1"),
				},
			},
			{
				Position: 2, Title: "Fix auth", Slug: "fix-auth", Phases: []int{2}, Branch: branches[1],
				Repos: map[string]feature.StackRepoEntry{
					"r1": openLayerEntry(tips[1], "https://github.com/org/r1/pull/2"),
				},
			},
			{
				Position: 3, Title: "Child first", Slug: "child-first", Branch: branches[2],
				Origin: &feature.StackLayerOrigin{SourceFeatureID: child.ID, SourceLayerPosition: 1},
				Repos:  map[string]feature.StackRepoEntry{"r1": {TipSHA: tips[2]}},
			},
			{
				Position: 4, Title: "Child second", Slug: "child-second", Branch: branches[3],
				Origin: &feature.StackLayerOrigin{SourceFeatureID: child.ID, SourceLayerPosition: 2},
				Repos:  map[string]feature.StackRepoEntry{"r1": {TipSHA: tips[3]}},
			},
		},
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: branches[3], BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{"r1": {Touched: true}},
	}
	lc := stackPublishLifecycleWithOrigin(f, child)
	fs := newFeatureStore(f, child)
	fake := installStackPublishFakeGitHub(t)
	fake.HandleJSON("/repos/org/r1/pulls/4", 200, `{"body":"","state":"open"}`)

	pub := mocks.NewMockRemoteOps()
	pub.PRStateFn = func(repoPath, prURL string) (string, error) {
		return git.PRStateOpen, nil
	}
	pub.PushLayerBranchFn = func(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
		return localSHA, nil
	}
	var gotBases []string
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		gotBases = append(gotBases, baseBranch)
		switch branch {
		case branches[2]:
			return "https://github.com/org/r1/pull/3", nil
		case branches[3]:
			return "https://github.com/org/r1/pull/4", nil
		}
		t.Fatalf("unexpected CreatePR branch %q", branch)
		return "", nil
	}

	pr, prompts := newPromptRecordingDescriptionPhaseRunner(t, "## Summary\n\nGenerated body")
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Remote:      pub,
		PhaseRunner: pr,
	}, orchestrator.Hooks{})

	if err := o.Publish(f.ID); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Only the appended branches are pushed; the roadmap layers' open PRs
	// are up to date.
	pushes := stackRemoteCalls(pub, "PushLayerBranch")
	if len(pushes) != 2 || pushes[0].Args[1] != branches[2] || pushes[1].Args[1] != branches[3] {
		t.Fatalf("PushLayerBranch calls = %+v, want exactly the two appended branches in order", pushes)
	}
	if pushes[0].Args[2] != tips[2] || pushes[1].Args[2] != tips[3] {
		t.Fatalf("pushed tips = %v %v, want the appended layers' tips", pushes[0].Args[2], pushes[1].Args[2])
	}

	// The appended PRs chain onto the previous top's branch and then the
	// first appended branch.
	if len(gotBases) != 2 || gotBases[0] != branches[1] || gotBases[1] != branches[2] {
		t.Fatalf("CreatePR bases = %v, want [%s %s]", gotBases, branches[1], branches[2])
	}

	// Both appended layers are described from the origin child: the child's
	// feature name and roadmap row supply the context, while the position,
	// title, and stack listing stay the parent's.
	if len(*prompts) != 2 {
		t.Fatalf("description prompts = %d, want 2 (one per appended layer)", len(*prompts))
	}
	first, second := (*prompts)[0], (*prompts)[1]
	if !strings.Contains(first, "Name: "+originChildFeatureName) {
		t.Errorf("layer 3 prompt does not name the origin feature:\n%s", first)
	}
	if !strings.Contains(first, originChildDescription) {
		t.Errorf("layer 3 prompt does not carry the origin description:\n%s", first)
	}
	if !strings.Contains(first, "Rationale: "+originRowOneRationale) {
		t.Errorf("layer 3 prompt does not carry the origin roadmap rationale:\n%s", first)
	}
	if !strings.Contains(first, "Phases: [1]") {
		t.Errorf("layer 3 prompt does not carry the origin roadmap phases:\n%s", first)
	}
	if strings.Contains(first, "parent origin walk") {
		t.Errorf("layer 3 prompt names the parent feature; the origin feature must supply the context:\n%s", first)
	}
	if !strings.Contains(second, "Rationale: "+originRowTwoRationale) || !strings.Contains(second, "Phases: [2]") {
		t.Errorf("layer 4 prompt does not carry the origin roadmap row two:\n%s", second)
	}
	if !strings.Contains(first, "Position: Layer 3 of 4") {
		t.Errorf("layer 3 prompt does not place the layer inside the four-layer stack:\n%s", first)
	}
	if !strings.Contains(second, "Position: Layer 4 of 4") {
		t.Errorf("layer 4 prompt does not place the layer inside the four-layer stack:\n%s", second)
	}
	for _, prompt := range []string{first, second} {
		for _, want := range []string{
			"- Layer 1: Foundation",
			"- Layer 2: Fix auth",
			"- Layer 3: Child first",
			"- Layer 4: Child second",
		} {
			if !strings.Contains(prompt, want) {
				t.Errorf("prompt stack listing misses %q:\n%s", want, prompt)
			}
		}
	}

	// Every open PR — the two roadmap layers' and the two just-created
	// appended PRs — receives a stack section listing all four layers.
	sectionWrites := stackSectionWrites(fake)
	if len(sectionWrites) != 4 {
		t.Fatalf("stack-section body writes = %d, want 4 (one per open PR)", len(sectionWrites))
	}
	for _, write := range sectionWrites {
		for _, want := range []string{"1. Foundation", "2. Fix auth", "3. Child first", "4. Child second"} {
			if !strings.Contains(write, want) {
				t.Errorf("stack-section write misses layer line %q: %s", want, write)
			}
		}
	}

	entry3 := f.Stack[2].Repos["r1"]
	if entry3.PRURL != "https://github.com/org/r1/pull/3" || entry3.LastPushedSHA != tips[2] || entry3.PRState != feature.StackPRStateOpen {
		t.Fatalf("layer 3 entry = %+v, want the PR, pushed SHA, and open state", entry3)
	}
	entry4 := f.Stack[3].Repos["r1"]
	if entry4.PRURL != "https://github.com/org/r1/pull/4" || entry4.LastPushedSHA != tips[3] {
		t.Fatalf("layer 4 entry = %+v, want the PR and pushed SHA", entry4)
	}
	if f.Status != feature.StatusPublished {
		t.Fatalf("feature status = %s, want Published", f.Status)
	}
}

// An appended layer whose origin feature exists but whose roadmap artifact
// is unreadable: the description prompt carries no feature context and no
// rationale, and the walk still delivers the layer.
func TestPublishStackAppendedLayerUnreadableOriginRoadmapLeavesContextEmpty(t *testing.T) {
	branches := [2]string{
		"feature/stack-ur/1-foundation",
		"feature/stack-ur/2-child-first",
	}
	repoPath, tips := newStackedPublishRepoChain(t, branches[:]...)
	child := &feature.Feature{
		ID:          "feat-unreadable-child",
		Name:        originChildFeatureName,
		Description: originChildDescription,
		Artifacts:   map[string]string{"roadmap": filepath.Join(t.TempDir(), "missing-roadmap.md")},
	}
	f := &feature.Feature{
		ID:     "feat-stack-unreadable",
		Name:   "parent unreadable roadmap",
		Slug:   "stack-unreadable",
		Status: feature.StatusReviewPassed,
		Stack: []feature.StackLayer{
			{
				Position: 1, Title: "Foundation", Slug: "foundation", Phases: []int{1}, Branch: branches[0],
				Repos: map[string]feature.StackRepoEntry{
					"r1": openLayerEntry(tips[0], "https://github.com/org/r1/pull/1"),
				},
			},
			{
				Position: 2, Title: "Child first", Slug: "child-first", Branch: branches[1],
				Origin: &feature.StackLayerOrigin{SourceFeatureID: child.ID, SourceLayerPosition: 1},
				Repos:  map[string]feature.StackRepoEntry{"r1": {TipSHA: tips[1]}},
			},
		},
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: branches[1], BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{"r1": {Touched: true}},
	}
	lc := stackPublishLifecycleWithOrigin(f, child)
	fs := newFeatureStore(f, child)
	installStackPublishFakeGitHub(t)

	pub := mocks.NewMockRemoteOps()
	pub.PRStateFn = func(repoPath, prURL string) (string, error) {
		return git.PRStateOpen, nil
	}
	pub.PushLayerBranchFn = func(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
		return localSHA, nil
	}
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		if branch != branches[1] {
			t.Fatalf("unexpected CreatePR branch %q", branch)
		}
		return "https://github.com/org/r1/pull/2", nil
	}

	pr, prompts := newPromptRecordingDescriptionPhaseRunner(t, "## Summary\n\nGenerated body")
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Remote:      pub,
		PhaseRunner: pr,
	}, orchestrator.Hooks{})

	if err := o.Publish(f.ID); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if len(*prompts) != 1 {
		t.Fatalf("description prompts = %d, want 1 (the appended layer)", len(*prompts))
	}
	prompt := (*prompts)[0]
	if strings.Contains(prompt, "## Feature") {
		t.Errorf("prompt carries a feature section despite the unreadable origin roadmap:\n%s", prompt)
	}
	if strings.Contains(prompt, "Rationale: ") || strings.Contains(prompt, "Phases: ") {
		t.Errorf("prompt carries a rationale or phases despite the unreadable origin roadmap:\n%s", prompt)
	}
	if strings.Contains(prompt, originChildFeatureName) || strings.Contains(prompt, "parent unreadable roadmap") {
		t.Errorf("prompt names a feature despite the unreadable origin roadmap:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Title: Child first") || !strings.Contains(prompt, "Layer 2 of 2") {
		t.Errorf("prompt does not carry the parent layer's own title and position:\n%s", prompt)
	}
	if entry := f.Stack[1].Repos["r1"]; entry.PRURL != "https://github.com/org/r1/pull/2" {
		t.Fatalf("layer 2 entry = %+v, want the created PR", entry)
	}
	if f.Status != feature.StatusPublished {
		t.Fatalf("feature status = %s, want Published", f.Status)
	}
}

// A parent whose repository had no pull request: the first appended
// layer's PR bases on the repository's base branch, and the second chains
// onto the first appended branch. The roadmap layer carries no commits
// here, so only the appended branches are pushed.
func TestPublishStackAppendedLayersBaseOnRepoBaseBranchWithoutLowerPRs(t *testing.T) {
	branches := [3]string{
		"feature/stack-b/1-foundation",
		"feature/stack-b/2-child-first",
		"feature/stack-b/3-child-second",
	}
	repoPath, tips := newStackedPublishRepoChain(t, branches[:]...)
	baseSHA := runPublishGitOutput(t, repoPath, "rev-parse", "refs/remotes/origin/"+mainBranch)
	f := &feature.Feature{
		ID:     "feat-stack-baseless",
		Name:   "parent baseless",
		Slug:   "stack-baseless",
		Status: feature.StatusReviewPassed,
		Stack: []feature.StackLayer{
			{
				Position: 1, Title: "Foundation", Slug: "foundation", Phases: []int{1}, Branch: branches[0],
				Repos: map[string]feature.StackRepoEntry{"r1": {TipSHA: baseSHA}},
			},
			{
				Position: 2, Title: "Child first", Slug: "child-first", Branch: branches[1],
				Origin: &feature.StackLayerOrigin{SourceFeatureID: "feat-missing-child", SourceLayerPosition: 1},
				Repos:  map[string]feature.StackRepoEntry{"r1": {TipSHA: tips[1]}},
			},
			{
				Position: 3, Title: "Child second", Slug: "child-second", Branch: branches[2],
				Origin: &feature.StackLayerOrigin{SourceFeatureID: "feat-missing-child", SourceLayerPosition: 2},
				Repos:  map[string]feature.StackRepoEntry{"r1": {TipSHA: tips[2]}},
			},
		},
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: branches[2], BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{"r1": {Touched: true}},
	}
	lc := stackPublishLifecycleWithOrigin(f, nil)
	fs := newFeatureStore(f)
	installStackPublishFakeGitHub(t)

	pub := mocks.NewMockRemoteOps()
	pub.PushLayerBranchFn = func(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
		return localSHA, nil
	}
	var gotBranches, gotBases []string
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		gotBranches = append(gotBranches, branch)
		gotBases = append(gotBases, baseBranch)
		switch branch {
		case branches[1]:
			return "https://github.com/org/r1/pull/1", nil
		case branches[2]:
			return "https://github.com/org/r1/pull/2", nil
		}
		t.Fatalf("unexpected CreatePR branch %q", branch)
		return "", nil
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Remote:      pub,
		PhaseRunner: newPublishDescriptionPhaseRunner(t, "## Summary\n\nGenerated body", false),
	}, orchestrator.Hooks{})

	if err := o.Publish(f.ID); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	pushes := stackRemoteCalls(pub, "PushLayerBranch")
	if len(pushes) != 2 || pushes[0].Args[1] != branches[1] || pushes[1].Args[1] != branches[2] {
		t.Fatalf("PushLayerBranch calls = %+v, want exactly the two appended branches", pushes)
	}
	if len(gotBases) != 2 || gotBases[0] != mainBranch || gotBases[1] != branches[1] {
		t.Fatalf("CreatePR bases = %v, want [%s %s] — no lower PR exists", gotBases, mainBranch, branches[1])
	}
	if len(gotBranches) != 2 {
		t.Fatalf("CreatePR branches = %v, want the two appended branches", gotBranches)
	}
	if entry := f.Stack[0].Repos["r1"]; !entry.NoCommits || entry.PRURL != "" {
		t.Fatalf("layer 1 entry = %+v, want the no-commits marker and no PR", entry)
	}
	if f.Status != feature.StatusPublished {
		t.Fatalf("feature status = %s, want Published", f.Status)
	}
}

// A single-delivery parent with one appended layer: both PRs — the
// original and the appended one — gain a stack section, the appended PR
// bases on the original PR's branch, and a missing origin feature leaves
// the description context empty without failing the walk.
func TestPublishSingleParentWithAppendedLayerGainsStackSectionInBothPRs(t *testing.T) {
	branches := [2]string{
		"feature/single-app/1-foundation",
		"feature/single-app/2-child-first",
	}
	repoPath, tips := newStackedPublishRepoChain(t, branches[:]...)
	f := &feature.Feature{
		ID:     "feat-single-appended",
		Name:   "single appended",
		Slug:   "single-appended",
		Status: feature.StatusReviewPassed,
		Stack: []feature.StackLayer{
			{
				Position: 1, Title: "Foundation", Slug: "foundation", Phases: []int{1}, Branch: branches[0],
				Repos: map[string]feature.StackRepoEntry{
					"r1": openLayerEntry(tips[0], "https://github.com/org/r1/pull/1"),
				},
			},
			{
				Position: 2, Title: "Child first", Slug: "child-first", Branch: branches[1],
				Origin: &feature.StackLayerOrigin{SourceFeatureID: "feat-missing-child", SourceLayerPosition: 1},
				Repos:  map[string]feature.StackRepoEntry{"r1": {TipSHA: tips[1]}},
			},
		},
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: branches[1], BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{"r1": {Touched: true}},
	}
	lc := stackPublishLifecycleWithOrigin(f, nil)
	fs := newFeatureStore(f)
	fake := installStackPublishFakeGitHub(t)

	pub := mocks.NewMockRemoteOps()
	pub.PRStateFn = func(repoPath, prURL string) (string, error) {
		return git.PRStateOpen, nil
	}
	pub.PushLayerBranchFn = func(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
		return localSHA, nil
	}
	var gotBase string
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		if branch != branches[1] {
			t.Fatalf("unexpected CreatePR branch %q", branch)
		}
		gotBase = baseBranch
		return "https://github.com/org/r1/pull/2", nil
	}

	pr, prompts := newPromptRecordingDescriptionPhaseRunner(t, "## Summary\n\nGenerated body")
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Remote:      pub,
		PhaseRunner: pr,
	}, orchestrator.Hooks{})

	if err := o.Publish(f.ID); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if gotBase != branches[0] {
		t.Fatalf("CreatePR base = %q, want the original PR's branch %s", gotBase, branches[0])
	}
	if len(*prompts) != 1 {
		t.Fatalf("description prompts = %d, want 1 (the appended layer)", len(*prompts))
	}
	if prompt := (*prompts)[0]; strings.Contains(prompt, "## Feature") || strings.Contains(prompt, "Rationale: ") {
		t.Fatalf("prompt carries feature context despite the missing origin feature:\n%s", prompt)
	}

	// Both open PRs gain a stack section listing both layers.
	sectionWrites := stackSectionWrites(fake)
	if len(sectionWrites) != 2 {
		t.Fatalf("stack-section body writes = %d, want 2 (both PRs)", len(sectionWrites))
	}
	for _, write := range sectionWrites {
		if !strings.Contains(write, "1. Foundation") || !strings.Contains(write, "2. Child first") {
			t.Errorf("stack-section write does not list both layers: %s", write)
		}
	}
	if f.Status != feature.StatusPublished {
		t.Fatalf("feature status = %s, want Published", f.Status)
	}
}
