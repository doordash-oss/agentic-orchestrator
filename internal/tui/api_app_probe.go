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
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
)

// RemediationDirtyRepo is one dirty-repository diagnostic row shown by the
// refactor remediation overlay, exposed structurally so programmatic drivers
// can assert exact paths and counts without fighting the panel's text
// wrapping.
type RemediationDirtyRepo struct {
	Name      string
	Path      string
	Staged    int
	Unstaged  int
	Untracked int
}

// The methods below are the narrow read-only observation surface of
// APIAppModel: they let an external driver (a real Bubble Tea program or a
// test harness) verify which overlay is active and which record is selected
// without reaching into model internals. They carry no behavior — all
// interaction still flows through Update and RefreshCmd.

// RefreshCmd returns the command that pulls the read-model snapshot for one
// refresh signal and folds it back into the model. It is the same command a
// live Bubble Tea program runs when an SSE refresh signal arrives, exposed so
// synchronous drivers can pump it explicitly instead of waiting on the event
// stream.
func (m APIAppModel) RefreshCmd(signal server.RefreshSignal) tea.Cmd {
	return m.fetchRefreshSnapshotCmd(signal)
}

// SelectedFeatureID reports the model's currently selected feature id.
func (m APIAppModel) SelectedFeatureID() string {
	return m.selectedFeature
}

// StatusMessage is the dashboard status line the live app would show.
func (m APIAppModel) StatusMessage() string {
	return m.statusMessage
}

// WizardActive reports whether the create/refactor wizard is open.
func (m APIAppModel) WizardActive() bool {
	return m.wizard != nil
}

// EditorOpen reports whether the config editor overlay is open.
func (m APIAppModel) EditorOpen() bool {
	return m.configEditor != nil
}

// ReviewOpen reports whether a review session overlay is open.
func (m APIAppModel) ReviewOpen() bool {
	return m.artifactReview != nil && !m.artifactReview.Detached()
}

// ReviewMenuOpen reports whether the review overlay's decision menu is
// currently showing (after esc from the artifact editor).
func (m APIAppModel) ReviewMenuOpen() bool {
	return m.artifactReview != nil && !m.artifactReview.Detached() && m.artifactReview.showMenu
}

// ReviewMenuLabels exposes the review overlay's ordered decision menu labels
// so drivers can navigate the menu by name via real key presses.
func (m APIAppModel) ReviewMenuLabels() []string {
	if m.artifactReview == nil {
		return nil
	}
	items := m.artifactReview.menuItems()
	labels := make([]string, 0, len(items))
	for _, item := range items {
		labels = append(labels, item.label)
	}
	return labels
}

// RemediationActive reports whether the dirty-worktree remediation overlay
// is open after a blocked refactor launch or entry.
func (m APIAppModel) RemediationActive() bool {
	return m.refactorLaunch.remediation != nil
}

// RemediationDirtyRepos returns the ordered dirty-repository diagnostics
// carried by the open remediation overlay, or nil when it is closed.
func (m APIAppModel) RemediationDirtyRepos() []RemediationDirtyRepo {
	if m.refactorLaunch.remediation == nil {
		return nil
	}
	repos := m.refactorLaunch.remediation.repos
	out := make([]RemediationDirtyRepo, 0, len(repos))
	for _, repo := range repos {
		out = append(out, RemediationDirtyRepo{
			Name:      repo.name,
			Path:      repo.path,
			Staged:    repo.stagedTotal,
			Unstaged:  repo.unstagedTotal,
			Untracked: repo.untrackedTotal,
		})
	}
	return out
}

// FeatureDetail returns the cached detail snapshot for one feature, if the
// model has loaded it.
func (m APIAppModel) FeatureDetail(featureID string) (server.FeatureDetailResponse, bool) {
	detail, ok := m.featureDetails[featureID]
	return detail, ok
}
