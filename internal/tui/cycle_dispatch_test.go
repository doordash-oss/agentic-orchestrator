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

package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func newDispatchTestModel(t *testing.T, repos []feature.FeatureRepo) (AppModel, string) {
	t.Helper()
	dir := t.TempDir()
	store := feature.NewStore(dir)
	cfg := config.NewDefault()
	fm := feature.NewManager(store, cfg)
	f := &feature.Feature{
		ID:            "feat-disp",
		Name:          "feat-disp",
		Slug:          "feat-disp",
		Repos:         repos,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	app := AppModel{featureManager: fm}
	return app, f.ID
}

func runCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	return cmd()
}

func TestDispatchCycleKey_AllCycleTypes_TableDriven(t *testing.T) {
	t.Parallel()
	type expectedMsgKind int
	const (
		kindNil expectedMsgKind = iota
		kindOpenSelector
		kindShowRefactor
		kindOther // dispatched via dispatchRepoCycleCmd
	)

	cases := []struct {
		name     string
		repos    []feature.FeatureRepo
		action   feature.RepoCycleType
		expected expectedMsgKind
	}{
		// N=0: defensive nil for every action.
		{"N0_Rebase", nil, feature.CycleRebase, kindNil},
		{"N0_ReviewComments", nil, feature.CycleReviewComments, kindNil},
		{"N0_Tweak", nil, feature.CycleTweak, kindNil},
		{"N0_Refactor", nil, feature.CycleRefactor, kindNil},

		// N=1 + non-Refactor: dispatch direct via dispatchRepoCycleCmd.
		{"N1_Rebase", []feature.FeatureRepo{{Name: "r1"}}, feature.CycleRebase, kindOther},
		{"N1_ReviewComments", []feature.FeatureRepo{{Name: "r1"}}, feature.CycleReviewComments, kindOther},
		{"N1_Tweak", []feature.FeatureRepo{{Name: "r1"}}, feature.CycleTweak, kindOther},

		// N=1 + Refactor: showRefactorForRepoMsg.
		{"N1_Refactor", []feature.FeatureRepo{{Name: "r1"}}, feature.CycleRefactor, kindShowRefactor},

		// N=2 + any action: openCycleSelectorMsg.
		{"N2_Rebase", []feature.FeatureRepo{{Name: "ra"}, {Name: "rb"}}, feature.CycleRebase, kindOpenSelector},
		{"N2_ReviewComments", []feature.FeatureRepo{{Name: "ra"}, {Name: "rb"}}, feature.CycleReviewComments, kindOpenSelector},
		{"N2_Tweak", []feature.FeatureRepo{{Name: "ra"}, {Name: "rb"}}, feature.CycleTweak, kindOpenSelector},
		{"N2_Refactor", []feature.FeatureRepo{{Name: "ra"}, {Name: "rb"}}, feature.CycleRefactor, kindOpenSelector},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, fid := newDispatchTestModel(t, tc.repos)
			cmd := m.dispatchCycleKey(fid, tc.action)
			switch tc.expected {
			case kindNil:
				if cmd != nil {
					t.Fatalf("expected nil cmd, got %T", cmd)
				}
			case kindOpenSelector:
				msg := runCmd(t, cmd)
				got, ok := msg.(openCycleSelectorMsg)
				if !ok {
					t.Fatalf("expected openCycleSelectorMsg, got %T", msg)
				}
				if got.FeatureID != fid || got.Action != tc.action {
					t.Errorf("openCycleSelectorMsg = %+v, want {FeatureID: %q, Action: %q}", got, fid, tc.action)
				}
			case kindShowRefactor:
				msg := runCmd(t, cmd)
				got, ok := msg.(showRefactorForRepoMsg)
				if !ok {
					t.Fatalf("expected showRefactorForRepoMsg, got %T", msg)
				}
				if got.FeatureID != fid || got.RepoName != tc.repos[0].Name {
					t.Errorf("showRefactorForRepoMsg = %+v, want {FeatureID: %q, RepoName: %q}", got, fid, tc.repos[0].Name)
				}
			case kindOther:
				if cmd == nil {
					t.Fatalf("expected non-nil cmd for N=1 non-Refactor")
				}
				// Verify the helper dispatches the same way as the existing
				// per-repo dispatcher. We don't execute the cmd because it
				// depends on orchestrator/git wiring outside this test.
				direct := m.dispatchRepoCycleCmd(fid, tc.repos[0].Name, tc.action)
				if (direct == nil) != (cmd == nil) {
					t.Errorf("helper cmd nilness (%v) differs from direct dispatch (%v)", cmd == nil, direct == nil)
				}
			}
		})
	}
}
