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
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

type RecoveryModel struct {
	items   []session.RecoveryItem
	actions map[string]session.RecoveryAction
	cursor  int
	done    bool
}

func NewRecoveryModel(items []session.RecoveryItem) RecoveryModel {
	actions := make(map[string]session.RecoveryAction)
	for _, item := range items {
		defaultAction := session.RecoverySkip
		// Tweak sessions are interactive and cannot be resumed or skipped —
		// they must be killed to safely clear ActiveCycleType.
		if isRecoveryTweakSession(item) {
			defaultAction = session.RecoveryKill
		}
		actions[session.RecoveryActionKey(item.PIDFile.FeatureID, item.RepoName)] = defaultAction
	}
	return RecoveryModel{
		items:   items,
		actions: actions,
	}
}

func (m RecoveryModel) Init() tea.Cmd {
	return nil
}

func (m RecoveryModel) Update(msg tea.Msg) (RecoveryModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		// Recovery-specific keys must be checked before Up/Down to avoid
		// collisions (e.g. "k" is in both keys.Up and keys.RecoveryKill).
		case key.Matches(msg, keys.RecoveryResume):
			if m.cursor < len(m.items) {
				item := m.items[m.cursor]
				// Tweak sessions are interactive — cannot be resumed autonomously
				if isRecoveryTweakSession(item) {
					break
				}
				m.actions[session.RecoveryActionKey(item.PIDFile.FeatureID, item.RepoName)] = session.RecoveryResume
			}
		case key.Matches(msg, keys.RecoveryKill):
			if m.cursor < len(m.items) {
				item := m.items[m.cursor]
				m.actions[session.RecoveryActionKey(item.PIDFile.FeatureID, item.RepoName)] = session.RecoveryKill
			}
		case key.Matches(msg, keys.RecoverySkip):
			if m.cursor < len(m.items) {
				item := m.items[m.cursor]
				// Tweak sessions must be killed — skip would leave stale ActiveCycleType
				if isRecoveryTweakSession(item) {
					break
				}
				m.actions[session.RecoveryActionKey(item.PIDFile.FeatureID, item.RepoName)] = session.RecoverySkip
			}
		case key.Matches(msg, keys.Up):
			if m.cursor > 0 {
				m.cursor--
			}
		case key.Matches(msg, keys.Down):
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
		case key.Matches(msg, keys.Enter):
			m.done = true
		}
	}
	return m, nil
}

func (m RecoveryModel) View() string {
	var b strings.Builder

	b.WriteString(TitleStyle.Render(" Session Recovery"))
	b.WriteString("\n\n")

	var itemsContent strings.Builder
	for i, item := range m.items {
		cursor := "  "
		if i == m.cursor {
			cursor = SelectedRowStyle.Render("\u25b8 ")
		}

		alive := MutedStyle.Render("NOT running")
		if item.ProcessAlive {
			alive = WarningStyle.Render("STILL RUNNING (orphan)")
		}

		actionKey := session.RecoveryActionKey(item.PIDFile.FeatureID, item.RepoName)
		action := m.actions[actionKey]
		actionStr := actionString(action)

		repoSuffix := ""
		if item.RepoName != "" && item.Feature != nil && len(item.Feature.Repos) > 1 {
			repoSuffix = fmt.Sprintf(" (repo: %s)", item.RepoName)
		}

		itemContent := fmt.Sprintf("%s%d. %s (%s, iter %d)%s\n", cursor, i+1,
			item.PIDFile.FeatureID, item.PIDFile.Phase, item.PIDFile.Iteration, repoSuffix)
		itemContent += fmt.Sprintf("   PID %d: %s\n", item.PIDFile.PID, alive)
		itemContent += fmt.Sprintf("   Action: %s", actionStr)
		if isRecoveryTweakSession(item) {
			itemContent += "\n   " + MutedStyle.Render("(interactive tweak — kill only)")
		}

		itemBox := panelStyle(i == m.cursor).Render(itemContent)
		itemsContent.WriteString(itemBox + "\n")
	}

	b.WriteString(itemsContent.String())
	b.WriteString("\n")
	// Show context-aware footer: tweak items only support Kill
	if m.cursor < len(m.items) && isRecoveryTweakSession(m.items[m.cursor]) {
		b.WriteString(KeyHelpStyle.Render(" [k] Kill   [enter] Continue"))
	} else {
		b.WriteString(KeyHelpStyle.Render(" [r] Resume   [k] Kill   [s] Skip   [enter] Continue"))
	}
	b.WriteString("\n")

	return b.String()
}

func (m RecoveryModel) IsDone() bool {
	return m.done
}

func (m RecoveryModel) Actions() map[string]session.RecoveryAction {
	return m.actions
}

// Items returns the recovery items originally displayed to the user. The
// view layer captures the scan result once at entry (NewRecoveryModel) and
// replays the same slice on submission so a concurrent rescan cannot alter
// the set of items the user's action map applies to.
func (m RecoveryModel) Items() []session.RecoveryItem {
	return m.items
}

// isRecoveryTweakSession returns true when a recovery item belongs to an
// interactive tweak session — either single-repo (ActiveCycleType) or
// multi-repo (RepoCycles entry).
func isRecoveryTweakSession(item session.RecoveryItem) bool {
	if item.Feature == nil {
		return false
	}
	if item.Feature.ActiveCycleType() == feature.CycleTweak {
		return true // single-repo
	}
	if item.RepoName != "" && item.Feature.RepoCycles != nil {
		if rc, ok := item.Feature.RepoCycles[item.RepoName]; ok && rc.Type == feature.CycleTweak {
			return true // multi-repo
		}
	}
	return false
}

func actionString(a session.RecoveryAction) string {
	switch a {
	case session.RecoveryResume:
		return "[R]esume"
	case session.RecoveryKill:
		return "[K]ill"
	case session.RecoverySkip:
		return "[S]kip"
	default:
		return "Unknown"
	}
}
