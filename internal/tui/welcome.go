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
	"charm.land/lipgloss/v2"
)

// WelcomeModel provides the first-launch welcome experience.
// 3-step flow: intro → directory picker → confirmation with "Add another" loop.

type welcomeStep int

const (
	welcomeStepIntro welcomeStep = iota
	welcomeStepPicker
	welcomeStepConfirm
)

type WelcomeModel struct {
	step      welcomeStep
	picker    DirPickerModel
	result    *WelcomeResult
	cancelled bool
	width     int
	height    int

	// Accumulated roots (grows as user adds more via "Add another")
	selectedRoots []string
	repoCounts    []int // parallel to selectedRoots: repo count for each confirmed root

	// Per-root state for the confirmation step
	pendingRoot  string // root just selected from picker, awaiting confirm/add-another
	pendingCount int    // git repo count for pendingRoot (captured from picker)
	rootReady    bool   // true when a new root is ready for the app to consume
}

// WelcomeResult holds all selected workspace roots.
type WelcomeResult struct {
	SelectedRoots []string
}

func NewWelcomeModel() WelcomeModel {
	return WelcomeModel{
		step: welcomeStepIntro,
	}
}

func (m WelcomeModel) Init() tea.Cmd {
	return nil
}

func (m WelcomeModel) Update(msg tea.Msg) (WelcomeModel, tea.Cmd) {
	// Forward window size to stored dimensions and to the picker if active
	if wsm, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = wsm.Width
		m.height = wsm.Height
		if m.step == welcomeStepPicker {
			m.picker, _ = m.picker.Update(wsm)
		}
		return m, nil
	}

	switch m.step {
	case welcomeStepIntro:
		if msg, ok := msg.(tea.KeyPressMsg); ok {
			switch {
			case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
				m.step = welcomeStepPicker
				m.picker = NewDirPickerModel()
				cmds := []tea.Cmd{m.picker.Init()}
				// Apply already-known terminal size to the new picker
				if m.width > 0 && m.height > 0 {
					m.picker, _ = m.picker.Update(tea.WindowSizeMsg{
						Width:  m.width,
						Height: m.height,
					})
				}
				return m, tea.Batch(cmds...)
			case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
				m.cancelled = true
				return m, nil
			}
		}
		return m, nil

	case welcomeStepPicker:
		var cmd tea.Cmd
		m.picker, cmd = m.picker.Update(msg)
		if m.picker.IsDone() {
			if m.picker.IsCancelled() {
				if len(m.selectedRoots) > 0 {
					// Already have roots — go back to confirm
					m.step = welcomeStepConfirm
				} else {
					// No roots yet — go back to intro
					m.step = welcomeStepIntro
				}
				return m, nil
			}
			// Picker completed with a selection — move to confirm
			m.pendingRoot = m.picker.SelectedPath()
			m.pendingCount = m.picker.gitRepoCount
			m.rootReady = true
			m.step = welcomeStepConfirm
		}
		return m, cmd

	case welcomeStepConfirm:
		if msg, ok := msg.(tea.KeyPressMsg); ok {
			switch {
			case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
				// Done — finalize all accumulated roots
				m.result = &WelcomeResult{
					SelectedRoots: m.selectedRoots,
				}
				return m, nil
			case key.Matches(msg, key.NewBinding(key.WithKeys("a"))):
				// Add another — create fresh picker, go back to picker step
				m.step = welcomeStepPicker
				m.picker = NewDirPickerModel()
				cmds := []tea.Cmd{m.picker.Init()}
				if m.width > 0 && m.height > 0 {
					m.picker, _ = m.picker.Update(tea.WindowSizeMsg{
						Width:  m.width,
						Height: m.height,
					})
				}
				return m, tea.Batch(cmds...)
			case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
				// Go back to picker (re-create it)
				m.step = welcomeStepPicker
				m.picker = NewDirPickerModel()
				cmds := []tea.Cmd{m.picker.Init()}
				if m.width > 0 && m.height > 0 {
					m.picker, _ = m.picker.Update(tea.WindowSizeMsg{
						Width:  m.width,
						Height: m.height,
					})
				}
				return m, tea.Batch(cmds...)
			}
		}
		return m, nil
	}
	return m, nil
}

func (m WelcomeModel) View() string {
	switch m.step {
	case welcomeStepIntro:
		return m.viewIntro()
	case welcomeStepPicker:
		return m.picker.View()
	case welcomeStepConfirm:
		return m.viewConfirm()
	}
	return ""
}

func (m WelcomeModel) viewIntro() string {
	artLines := []string{
		" ▄▀█ █▀▀ █▀▀ █▄░█ ▀█▀ █ █▀▀ █▀█",
		" █▀█ █▄█ ██▄ █░▀█ ░█░ █ █▄▄ █▄█",
	}

	brandStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(colorBrand)

	descStyle := lipgloss.NewStyle().
		Faint(true)

	helpStyle := lipgloss.NewStyle().
		Faint(true)

	// Build inner content
	var b strings.Builder
	for _, line := range artLines {
		b.WriteString(brandStyle.Render(line))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(descStyle.Render("Agentic Orchestrator helps you manage AI-assisted development workflows."))
	b.WriteString("\n")
	b.WriteString(descStyle.Render("To get started, add a workspace directory containing your git repositories."))
	b.WriteString("\n\n")
	b.WriteString(helpStyle.Render("enter select workspace directory • esc skip"))

	// Box it
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorBrand).
		Padding(1, 3)

	box := boxStyle.Render(b.String())

	// Center in the terminal
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

func (m WelcomeModel) viewConfirm() string {
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(colorBrand).
		MarginBottom(1)

	checkStyle := lipgloss.NewStyle().
		Foreground(colorSuccess)

	countStyle := lipgloss.NewStyle().
		Faint(true)

	helpStyle := lipgloss.NewStyle().
		Faint(true)

	var b strings.Builder

	b.WriteString(titleStyle.Render("Workspace Roots"))
	b.WriteString("\n\n")

	// Show all previously confirmed roots
	for i, root := range m.selectedRoots {
		count := 0
		if i < len(m.repoCounts) {
			count = m.repoCounts[i]
		}
		b.WriteString(checkStyle.Render("✓ Added: "))
		b.WriteString(compactHome(root))
		b.WriteString("  ")
		b.WriteString(countStyle.Render(repoCountText(count)))
		b.WriteString("\n")
	}

	// Show the pending root (just selected, not yet in selectedRoots)
	if m.pendingRoot != "" {
		b.WriteString(checkStyle.Render("✓ Added: "))
		b.WriteString(compactHome(m.pendingRoot))
		b.WriteString("  ")
		b.WriteString(countStyle.Render(repoCountText(m.pendingCount)))
		b.WriteString("\n")
	}

	// Box it
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorBrand).
		Padding(1, 3)
	box := boxStyle.Render(b.String())

	help := helpStyle.Render("a add another workspace • enter continue to dashboard • esc go back")
	content := box + "\n\n" + help

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
}

// repoCountText returns a human-readable string for the number of git repos found.
func repoCountText(count int) string {
	if count == 1 {
		return "1 git repo found"
	}
	return fmt.Sprintf("%d git repos found", count)
}

func (m WelcomeModel) IsDone() bool {
	return m.cancelled || m.result != nil
}

func (m WelcomeModel) IsCancelled() bool {
	return m.cancelled
}

func (m WelcomeModel) Result() *WelcomeResult {
	return m.result
}

// PendingRoot returns the path of the most recently selected root that hasn't
// been consumed yet. Returns "" if nothing pending.
func (m WelcomeModel) PendingRoot() string {
	if m.rootReady {
		return m.pendingRoot
	}
	return ""
}

// ConsumePendingRoot returns the pending root and clears the flag.
// The app calls this to persist the root immediately.
// Returns "" if the root is a duplicate of an already-selected root.
func (m *WelcomeModel) ConsumePendingRoot() string {
	if !m.rootReady {
		return ""
	}
	root := m.pendingRoot
	count := m.pendingCount
	m.rootReady = false
	m.pendingRoot = ""
	m.pendingCount = 0

	// Deduplicate — skip if already selected
	for _, existing := range m.selectedRoots {
		if existing == root {
			return ""
		}
	}

	// Move pending root into the accumulated list
	m.selectedRoots = append(m.selectedRoots, root)
	m.repoCounts = append(m.repoCounts, count)
	return root
}
