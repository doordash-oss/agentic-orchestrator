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
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// TestOrchestrator_RoadmapFinalAutoPublishFailureIsNeverTerminal pins the
// never-terminal contract: a roadmap-final auto-publish whose pull-request
// creation fails leaves the feature at CodeReady with the repository's
// stored record as the sole owner — no run-level failure — and the
// publish-completed event carries the first failed repository's canonical
// error. A subsequent single-repository publish clears the record and
// completes the feature-level publish.
func TestOrchestrator_RoadmapFinalAutoPublishFailureIsNeverTerminal(t *testing.T) {
	repoPath, _ := testutil.InitPublishReadyGitRepo(t)
	if err := os.WriteFile(filepath.Join(repoPath, "change.txt"), []byte("change\n"), 0o644); err != nil {
		t.Fatalf("write change: %v", err)
	}
	f := &feature.Feature{
		ID:                  "feat-rf-nf",
		Name:                "never-terminal",
		Slug:                "never-terminal",
		Status:              feature.StatusImplementing,
		CurrentPhase:        feature.PhaseImplement,
		CurrentRoadmapPhase: 2,
		TotalRoadmapPhases:  2,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: "feature/cool-feature", BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{
			"r1": {Touched: true},
		},
	}
	lc := lifecycleForFeature(f)
	lc.CompleteImplementationFn = func(id string) error { return nil }
	lc.MarkCodeReadyFn = func(id string) error {
		f.Status = feature.StatusCodeReady
		return nil
	}
	lc.SetRepoPublishErrorFn = func(id, repo string, record errcat.FailureRecord) error {
		if f.RepoStates == nil {
			f.RepoStates = make(map[string]*feature.RepoState)
		}
		st := f.RepoStates[repo]
		if st == nil {
			st = &feature.RepoState{}
			f.RepoStates[repo] = st
		}
		stored := record
		st.Error = &stored
		return nil
	}
	lc.SetRepoPublishedFn = func(id, repo, url string) error {
		if f.RepoStates == nil {
			f.RepoStates = make(map[string]*feature.RepoState)
		}
		st := f.RepoStates[repo]
		if st == nil {
			st = &feature.RepoState{}
			f.RepoStates[repo] = st
		}
		st.Touched = true
		st.PRURL = url
		st.Error = nil
		return nil
	}
	lc.TryCompletePublishFn = func(id string) (bool, error) {
		if !f.AllReposPublished() {
			return false, nil
		}
		f.Status = feature.StatusPublished
		return true, nil
	}
	fs := newFeatureStore(f)

	pub := mocks.NewMockRemoteOps()
	pub.PushFn = func(path, branch string) error { return nil }
	createPRCalls := 0
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		createPRCalls++
		if createPRCalls == 1 {
			return "", errors.New("POST /repos/org/r1/pulls: 502 Bad Gateway")
		}
		return "https://github.com/org/r1/pull/1", nil
	}

	pr := newPublishDescriptionPhaseRunner(t, "TITLE: Session Title\nBODY:\n## Summary\n\nGenerated body", false)
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Remote:      pub,
		PhaseRunner: pr,
	}, orchestrator.Hooks{})
	// The deferred end-of-feature Final Review pass stubs to all_passed so
	// the flow reaches the roadmap-final auto-publish under test.
	o.SetRunMultiRepoFinalReviewFn(func(*feature.Feature, ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
		ch := make(chan *agent.OrchestratorResult, 1)
		ch <- &agent.OrchestratorResult{FinalStatus: "all_passed"}
		return ch, nil
	})

	err := o.HandlePhaseCompletion("feat-rf-nf", orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "all_passed"},
	})
	if err == nil {
		t.Fatal("HandlePhaseCompletion error = nil, want the wrapped publish failure")
	}
	var dispatch *orchestrator.PublishDispatchError
	if !errors.As(err, &dispatch) {
		t.Fatalf("HandlePhaseCompletion error = %T %v, want PublishDispatchError", err, err)
	}
	var create *orchestrator.PublishPRCreateError
	if !errors.As(err, &create) {
		t.Fatalf("HandlePhaseCompletion error = %T %v, want the PR creation failure in the chain", err, err)
	}

	// The feature stays at CodeReady with no run-level failure record.
	if f.Status != feature.StatusCodeReady {
		t.Fatalf("feature status = %v, want CodeReady (publish failures are never terminal)", f.Status)
	}
	if f.Run().Failure != nil {
		t.Fatalf("run failure record = %+v, want none (the repository owns the condition)", f.Run().Failure)
	}
	refuteLifecycleCall(t, lc, "MarkFailed")

	// The repository carries the classified record.
	state := f.RepoStates["r1"]
	if state.Error == nil || state.Error.Code != errcat.PublishPullRequestFailed {
		t.Fatalf("repo record = %+v, want publish_pull_request_failed", state.Error)
	}
	if state.Error.Context == nil || len(state.Error.Context.Repositories) != 1 ||
		state.Error.Context.Repositories[0].Name != "r1" ||
		state.Error.Context.Repositories[0].Branch != "feature/cool-feature" {
		t.Fatalf("repo record block = %+v, want r1 on its feature branch", state.Error.Context)
	}
	if !strings.Contains(state.Error.Diagnostics, "502 Bad Gateway") {
		t.Errorf("repo record diagnostics = %q, want the raw server error", state.Error.Diagnostics)
	}

	// The publish-completed event carries the first failed repository's
	// rendered canonical error.
	var completed *ports.Event
	for _, ev := range drainEvents(o) {
		if ev.Type == ports.PublishCompleted {
			event := ev
			completed = &event
		}
	}
	if completed == nil {
		t.Fatal("no PublishCompleted event observed")
	}
	if completed.Error == nil {
		t.Fatal("PublishCompleted event carries no error, want the publish failure")
	}
	if completed.CanonicalError == nil || completed.CanonicalError.Code != errcat.PublishPullRequestFailed {
		t.Fatalf("PublishCompleted canonical error = %+v, want publish_pull_request_failed", completed.CanonicalError)
	}
	if completed.CanonicalError.Title == "" || completed.CanonicalError.Class != errcat.ClassNeedsAction {
		t.Fatalf("PublishCompleted canonical error = %+v, want the catalog title and needs_action class", completed.CanonicalError)
	}

	// A subsequent single-repository publish clears the record and completes
	// the feature-level publish.
	if err := o.PublishWithOptions("feat-rf-nf", orchestrator.PublishOptions{Repos: []string{"r1"}}); err != nil {
		t.Fatalf("retry PublishWithOptions: %v", err)
	}
	state = f.RepoStates["r1"]
	if state.Error != nil {
		t.Fatalf("repo record = %+v, want cleared by the successful publish", state.Error)
	}
	if state.PRURL != "https://github.com/org/r1/pull/1" {
		t.Fatalf("repo PRURL = %q, want the pull-request link", state.PRURL)
	}
	if f.Status != feature.StatusPublished {
		t.Fatalf("feature status = %v, want Published after the repo-scoped retry", f.Status)
	}
}

// TestOrchestrator_RoadmapFinalScrubFailureStillEmitsPublishCompleted pins
// the emission contract on the pre-Publish failure site: when the roadmap
// final auto-publish aborts because scrubbing a stranded final-review
// artifact fails after MarkCodeReady, Publish never runs — so the scrub site
// itself must emit PublishCompleted (carrying the stored record's canonical
// error) and fire OnPublishCompleted, matching what surfaceDispatchCompletionError assumes the Publish pipeline already did.
func TestOrchestrator_RoadmapFinalScrubFailureStillEmitsPublishCompleted(t *testing.T) {
	repoPath, _ := testutil.InitPublishReadyGitRepo(t)
	// A stranded untracked final-review artifact: its presence makes the
	// scrub reach the ls-files check, which the runner fails.
	if err := os.WriteFile(filepath.Join(repoPath, "progress.md"), []byte("stranded\n"), 0o644); err != nil {
		t.Fatalf("write stranded artifact: %v", err)
	}
	f := &feature.Feature{
		ID:                  "feat-rf-scrub",
		Name:                "scrub-failure",
		Slug:                "scrub-failure",
		Status:              feature.StatusImplementing,
		CurrentPhase:        feature.PhaseImplement,
		CurrentRoadmapPhase: 2,
		TotalRoadmapPhases:  2,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: "feature/cool-feature", BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{
			"r1": {Touched: true},
		},
	}
	lc := lifecycleForFeature(f)
	lc.CompleteImplementationFn = func(id string) error { return nil }
	lc.MarkCodeReadyFn = func(id string) error {
		f.Status = feature.StatusCodeReady
		return nil
	}
	lc.SetRepoPublishErrorFn = func(id, repo string, record errcat.FailureRecord) error {
		if f.RepoStates == nil {
			f.RepoStates = make(map[string]*feature.RepoState)
		}
		st := f.RepoStates[repo]
		if st == nil {
			st = &feature.RepoState{}
			f.RepoStates[repo] = st
		}
		stored := record
		st.Error = &stored
		return nil
	}
	fs := newFeatureStore(f)

	// The command runner fails every ls-files invocation, so the scrub of the
	// stranded artifact fails closed.
	cmd := mocks.NewMockCommandRunner()
	cmd.RunFn = func(_ context.Context, name string, args []string, _ ports.CommandOpts) ([]byte, error) {
		for _, arg := range args {
			if arg == "ls-files" {
				return nil, errors.New("git ls-files: exit status 128")
			}
		}
		return nil, nil
	}

	pub := mocks.NewMockRemoteOps()
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		t.Fatal("CreatePR called; the scrub failure must abort before the publish pipeline runs")
		return "", nil
	}

	pr := newPublishDescriptionPhaseRunner(t, "TITLE: Session Title\nBODY:\n## Summary\n\nGenerated body", false)
	publishCompletedHook := 0
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Remote:      pub,
		PhaseRunner: pr,
		CmdRunner:   cmd,
	}, orchestrator.Hooks{
		OnPublishCompleted: func(featureID string, prURLs map[string]string, err error) {
			publishCompletedHook++
			if err == nil {
				t.Fatal("OnPublishCompleted err = nil, want the scrub failure")
			}
		},
	})
	o.SetRunMultiRepoFinalReviewFn(func(*feature.Feature, ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
		ch := make(chan *agent.OrchestratorResult, 1)
		ch <- &agent.OrchestratorResult{FinalStatus: "all_passed"}
		return ch, nil
	})

	err := o.HandlePhaseCompletion("feat-rf-scrub", orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "all_passed"},
	})
	var dispatch *orchestrator.PublishDispatchError
	if err == nil || !errors.As(err, &dispatch) {
		t.Fatalf("HandlePhaseCompletion error = %v, want PublishDispatchError", err)
	}

	// The feature stays at CodeReady with the repository owning the record.
	if f.Status != feature.StatusCodeReady {
		t.Fatalf("feature status = %v, want CodeReady", f.Status)
	}
	if f.Run().Failure != nil {
		t.Fatalf("run failure record = %+v, want none", f.Run().Failure)
	}
	refuteLifecycleCall(t, lc, "MarkFailed")
	state := f.RepoStates["r1"]
	if state.Error == nil || state.Error.Code != errcat.PublishPushFailed {
		t.Fatalf("repo record = %+v, want publish_push_failed (artifact scrub class)", state.Error)
	}

	// Publish never ran: no PublishStarted, and the completion event carries
	// the stored record's canonical error.
	var completed *ports.Event
	for _, ev := range drainEvents(o) {
		if ev.Type == ports.PublishStarted {
			t.Fatal("PublishStarted observed; the scrub failure must abort before the publish pipeline runs")
		}
		if ev.Type == ports.PublishCompleted {
			event := ev
			completed = &event
		}
	}
	if completed == nil {
		t.Fatal("no PublishCompleted event observed, want the scrub site to emit it")
	}
	if completed.Error == nil {
		t.Fatal("PublishCompleted event carries no error, want the scrub failure")
	}
	if completed.CanonicalError == nil || completed.CanonicalError.Code != errcat.PublishPushFailed {
		t.Fatalf("PublishCompleted canonical error = %+v, want publish_push_failed", completed.CanonicalError)
	}
	if publishCompletedHook != 1 {
		t.Fatalf("OnPublishCompleted calls = %d, want exactly one", publishCompletedHook)
	}
}
