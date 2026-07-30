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
	"errors"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
)

// refactorLaunchState bundles the in-flight refactor launch context: the
// wizard values awaiting mutation completion and the dirty-worktree
// remediation panel shown when the launch is blocked. Mutation, retry, and
// cancellation paths must transition it as one unit so no stale pending
// value or parent pairing can survive into the next launch attempt.
type refactorLaunchState struct {
	pendingResult   *WizardResult
	pendingParentID string
	remediation     *refactorRemediationModel
	// entryRetryParentID marks an entry-time remediation retry awaiting the
	// refreshed parent detail: the snapshot handler re-evaluates the guard
	// (dirty → rebuild remediation, clean → open the wizard) once the
	// refreshed snapshot lands.
	entryRetryParentID string
}

// reset clears the whole launch state: every terminal outcome (success,
// non-dirty failure, or user cancellation) leaves nothing behind.
func (s *refactorLaunchState) reset() {
	s.pendingResult = nil
	s.pendingParentID = ""
	s.remediation = nil
	s.entryRetryParentID = ""
}

type refactorRemediationModel struct {
	repos        []refactorDirtyRepo
	wizardResult *WizardResult
	parentID     string
}

type refactorDirtyRepo struct {
	name           string
	path           string
	stagedTotal    int
	unstagedTotal  int
	untrackedTotal int
}

func parseDirtyRemediation(err error, result *WizardResult, parentID string) *refactorRemediationModel {
	if err == nil {
		return nil
	}
	var apiErr *server.APIError
	if !errors.As(err, &apiErr) {
		return nil
	}
	if apiErr.Code != "parent_worktrees_dirty" {
		return nil
	}
	return &refactorRemediationModel{
		repos:        parseDirtyRepos(apiErr.Target["repos"]),
		wizardResult: result,
		parentID:     parentID,
	}
}

// newDirtyEntryRemediation builds the remediation panel for a refactor entry
// blocked before the wizard opens: the server flags the action disabled with
// the dirty_parent reason and carries the same structured diagnostics the
// launch-time error returns. No wizard values exist yet, so the panel omits
// the value-preservation copy and its retry re-evaluates entry instead of
// resubmitting a launch.
func newDirtyEntryRemediation(parentID string, reason server.ActionDisabledReason) *refactorRemediationModel {
	return &refactorRemediationModel{
		repos:    parseDirtyRepos(reason.Target["repos"]),
		parentID: parentID,
	}
}

func parseDirtyRepos(raw any) []refactorDirtyRepo {
	rawRepos, _ := raw.([]any)
	repos := make([]refactorDirtyRepo, 0, len(rawRepos))
	for _, entry := range rawRepos {
		fields, _ := entry.(map[string]any)
		if fields == nil {
			continue
		}
		repos = append(repos, refactorDirtyRepo{
			name:           remediationString(fields, "repo"),
			path:           remediationString(fields, "path"),
			stagedTotal:    remediationInt(fields, "staged_total"),
			unstagedTotal:  remediationInt(fields, "unstaged_total"),
			untrackedTotal: remediationInt(fields, "untracked_total"),
		})
	}
	return repos
}

func remediationString(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func remediationInt(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}

func (rm refactorRemediationModel) View(width int) string {
	var b strings.Builder
	b.WriteString(TitleStyle.Render(" Refactor Launch Blocked — Dirty Worktrees"))
	b.WriteString("\n\n")
	b.WriteString("The following repositories have uncommitted changes.\n")
	b.WriteString("Clean those worktrees externally and retry the launch.\n\n")
	for _, repo := range rm.repos {
		// SectionHeaderStyle has no fixed width, so long repository
		// identities stay on one scannable line at every modal width.
		b.WriteString(SectionHeaderStyle.Render(repo.name) + "\n")
		if repo.path != "" {
			b.WriteString(fmt.Sprintf("  Path:      %s\n", repo.path))
		}
		b.WriteString(fmt.Sprintf("  Staged:    %d\n", repo.stagedTotal))
		b.WriteString(fmt.Sprintf("  Unstaged:  %d\n", repo.unstagedTotal))
		b.WriteString(fmt.Sprintf("  Untracked: %d\n\n", repo.untrackedTotal))
	}
	if rm.wizardResult != nil {
		b.WriteString(MutedStyle.Render("All entered wizard values are preserved."))
		b.WriteString("\n")
	}
	b.WriteString(KeyHelpStyle.Render(" [r] Retry   [esc] Cancel"))
	b.WriteString("\n")
	content := b.String()
	style := lipgloss.NewStyle().
		Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorWarning).
		Width(min(width-4, 72))
	return style.Render(content)
}
