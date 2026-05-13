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
	tea "charm.land/bubbletea/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// openCycleSelectorMsg signals the Bubbletea Update root to activate the
// cycle-selector overlay. dispatchCycleKey returns this message (wrapped in
// a tea.Cmd) for N>1 features so that the helper itself does not mutate
// AppModel — Bubbletea's Update loop owns mutation.
type openCycleSelectorMsg struct {
	FeatureID string
	Action    feature.RepoCycleType
}

// dispatchCycleKey routes any of the four cycle keys (Rebase, ReviewComments,
// Tweak, Refactor) to the appropriate per-repo entry point.
//
// Behavior by repo count:
//
//   - N=0: returns nil (defensive — cycle keys should be guarded upstream).
//   - N=1, action ∈ {CycleRebase, CycleReviewComments, CycleTweak}: dispatches
//     per-repo directly via dispatchRepoCycleCmd (same path the cycle-selector
//     overlay's enter handler uses for N>1).
//   - N=1, action == CycleRefactor: returns a tea.Cmd whose tea.Msg is
//     showRefactorForRepoMsg, activating the inline refactor textarea via the
//     existing handler — same handler the cycle-selector overlay's enter
//     dispatch uses for N>1 today.
//   - N>1, any action: returns a tea.Cmd whose tea.Msg is openCycleSelectorMsg
//     so the dashboard activates the cycle-selector overlay. The overlay's
//     enter handler then dispatches per-repo via dispatchRepoCycleCmd.
//
// Status preconditions (cycle running, multi-repo Published-only, etc.) stay
// at the call site — the helper returns tea.Cmd and cannot mutate
// AppModel.statusMessage.
func (m AppModel) dispatchCycleKey(featureID string, action feature.RepoCycleType) tea.Cmd {
	f, err := m.featureManager.Get(featureID)
	if err != nil || f == nil || len(f.Repos) == 0 {
		return nil
	}
	if len(f.Repos) > 1 {
		return func() tea.Msg {
			return openCycleSelectorMsg{FeatureID: featureID, Action: action}
		}
	}
	repoName := f.Repos[0].Name
	if action == feature.CycleRefactor {
		return func() tea.Msg {
			return showRefactorForRepoMsg{FeatureID: featureID, RepoName: repoName}
		}
	}
	return m.dispatchRepoCycleCmd(featureID, repoName, action)
}
