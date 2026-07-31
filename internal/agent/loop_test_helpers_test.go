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
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// defaultTestBranch is the base/target branch name used across this
// package's tests wherever the specific name doesn't matter.
const defaultTestBranch = "main"

// testMockIdentifier is the sentinel value tests use to name the mock
// provider/CLI/session in test doubles (ProviderName, command argv[0],
// SessionID) wherever the specific name doesn't matter.
const testMockIdentifier = "mock"

// testResultSuccessValue is the placeholder "success" string tests use for
// llm.ResultMessage.Subtype (and, where the exact text is arbitrary, .Result)
// wherever the specific value doesn't matter.
const testResultSuccessValue = "success"

// testResultMessageType is the SDKMessage/ResultMessage.Type discriminator
// value tests use to build a mock "result" message.
const testResultMessageType = "result"

// testStopReasonEndTurn is the llm.ResultMessage.StopReason value tests use
// for a normal (non-tool-use) turn completion.
const testStopReasonEndTurn = "end_turn"

// testRepoPathAPI is the fixture worktree path tests use for the
// testRepoNameAPI repo wherever the specific path doesn't matter.
const testRepoPathAPI = "/tmp/api"

// testRebaseTargetMaster is the fixture rebase-target branch name tests use
// wherever the specific branch doesn't matter (distinct from
// defaultTestBranch so tests can exercise a non-default target).
const testRebaseTargetMaster = "master"

// testAxisGrounding is the fixture plan-validation axis name tests use
// wherever the specific axis doesn't matter.
const testAxisGrounding = "grounding"

// testRepoNameAPI, testRepoNameWeb, and testRepoNameInfra are the fixture
// repo names tests use across this package wherever a repo (often multiple,
// alongside each other) is needed but the specific name doesn't matter.
const (
	testRepoNameAPI   = "api"
	testRepoNameWeb   = "web"
	testRepoNameInfra = "infra"
)

// Cycle Communication Contract path-template snippets. These describe the
// well-known artifact paths a cross-repo cycle prompt must reference, and
// are checked verbatim in both the rebase-loop and review-comments-loop
// prompt tests.
const (
	wantProgressPathTemplate           = "`progress.md`: `{phase_dir}/progress.md`"
	wantVerificationReportPathTemplate = "`verification-report.yaml`: `{iteration_dir}/verification-report.yaml`"
	wantPhaseCompletePathTemplate      = "`phase_complete`: `{iteration_dir}/phase_complete`"
)

type loopTestFeatureOptions struct {
	Name         string
	Slug         string
	Description  string
	ExitCriteria string

	// Status overrides the feature's lifecycle status. Defaults to
	// feature.StatusPublished when zero.
	Status feature.Status

	// CurrentPhase and CurrentRoadmapPhase set the feature's in-flight
	// phase/roadmap-phase tracking. Zero values match a feature with no
	// active roadmap phase.
	CurrentPhase        feature.Phase
	CurrentRoadmapPhase int

	// OmitPRURL, when true, leaves RepoState.PRURL unset (Touched still
	// set true) instead of the default synthetic PR URL.
	OmitPRURL bool
}

func newLoopTestFeature(t *testing.T, stateDir, featureID string, repoNames []string, opts loopTestFeatureOptions) (*feature.Store, *feature.Feature, []string) {
	t.Helper()

	store := feature.NewStore(stateDir)
	repos := make([]feature.FeatureRepo, 0, len(repoNames))
	repoPaths := make([]string, 0, len(repoNames))
	repoStates := map[string]*feature.RepoState{}
	for _, name := range repoNames {
		repoDir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(repoDir, 0o755); err != nil {
			t.Fatalf("mkdir repo %q: %v", name, err)
		}
		repos = append(repos, feature.FeatureRepo{
			Name:       name,
			Path:       repoDir,
			BaseBranch: defaultTestBranch,
		})
		repoPaths = append(repoPaths, repoDir)
		repoState := &feature.RepoState{Touched: true}
		if !opts.OmitPRURL {
			repoState.PRURL = fmt.Sprintf("https://github.com/example/%s/pull/1", name)
		}
		repoStates[name] = repoState
	}

	status := opts.Status
	if status == 0 {
		status = feature.StatusPublished
	}

	f := &feature.Feature{
		ID:                  featureID,
		Name:                opts.Name,
		Slug:                opts.Slug,
		Description:         opts.Description,
		Status:              status,
		CurrentPhase:        opts.CurrentPhase,
		CurrentRoadmapPhase: opts.CurrentRoadmapPhase,
		ActiveRun:           1,
		RunCount:            1,
		SchemaVersion:       feature.SchemaVersionCurrent,
		Repos:               repos,
		RepoStates:          repoStates,
		ExitCriteria:        opts.ExitCriteria,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	loaded, err := store.Load(featureID)
	if err != nil {
		t.Fatalf("reload feature: %v", err)
	}
	return store, loaded, repoPaths
}
