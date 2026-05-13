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

//go:generate go run ./genkeybindings

import "charm.land/bubbles/v2/key"

// keyboardLayout tracks the active layout for help text rendering.
// It also serves as an idempotency guard: once set, re-applying the same layout is a no-op.
var keyboardLayout string

// ApplyKeyboardLayout adds alternative keybindings for the specified layout.
// It adds alternatives alongside existing keys, never replacing them.
// Safe to call multiple times — subsequent calls are no-ops.
func ApplyKeyboardLayout(layout string) {
	if keyboardLayout == layout {
		return
	}
	keyboardLayout = layout

	switch layout {
	case "nordic":
		applyNordicLayout()
	}
}

// applyNordicLayout adds Nordic-specific key alternatives.
// Nordic keyboards require AltGr for [ ] { } \ |, making ctrl+] impossible.
func applyNordicLayout() {
	// "/" (Chat) — add "-" as alternative and update help label (single keystroke on Nordic keyboards)
	keys.Chat.SetKeys(append(keys.Chat.Keys(), "-")...)
	keys.Chat.SetHelp("-", keys.Chat.Help().Desc)

	// "ctrl+]" (Detach) — add "ctrl+x" as alternative (] requires AltGr on Nordic keyboards)
	keys.Detach.SetKeys(append(keys.Detach.Keys(), "ctrl+x")...)
}

// ChatKeyHint returns the key label for the chat shortcut, layout-aware.
func ChatKeyHint() string {
	switch keyboardLayout {
	case "nordic":
		return "-"
	default:
		return "/"
	}
}

// HelpKeyHint returns the key label for the help overlay shortcut, layout-aware.
func HelpKeyHint() string {
	return "?"
}

// KeyboardLayoutHint returns a short label for the active layout.
func KeyboardLayoutHint() string {
	switch keyboardLayout {
	case "nordic":
		return "Nordic"
	default:
		return "US"
	}
}

// DetachKeyHint returns the key label for the detach shortcut, layout-aware.
func DetachKeyHint() string {
	switch keyboardLayout {
	case "nordic":
		return "ctrl+x/esc"
	default:
		return "ctrl+]/esc"
	}
}

type keyMap struct {
	Quit              key.Binding
	New               key.Binding
	Enter             key.Binding
	Back              key.Binding
	Publish           key.Binding
	Attach            key.Binding
	Approve           key.Binding
	Help              key.Binding
	Restart           key.Binding
	Stop              key.Binding
	Rewind            key.Binding
	Delete            key.Binding
	Up                key.Binding
	Down              key.Binding
	Advance           key.Binding
	Tab               key.Binding
	ViewDiff          key.Binding
	CleanWorktree     key.Binding
	ManualPublish     key.Binding
	ResumeAll         key.Binding
	Rebase            key.Binding
	MergeLocal        key.Binding
	MarkDone          key.Binding
	ReviewComments    key.Binding
	Tweak             key.Binding
	Refactor          key.Binding
	EditConfig        key.Binding
	ViewLogs          key.Binding
	Chat              key.Binding
	HelpOverlay       key.Binding
	ToggleInputNotify key.Binding

	// Dashboard panel navigation
	PanelLeft  key.Binding
	PanelRight key.Binding

	// Dashboard tools
	WorkspaceManager key.Binding

	// Wizard-specific
	ShiftTab   key.Binding
	Left       key.Binding
	Right      key.Binding
	PasteImage key.Binding

	// Recovery-specific
	RecoveryResume key.Binding
	RecoveryKill   key.Binding
	RecoverySkip   key.Binding

	// Detail view phase retry. Under the unified flow phase atomicity
	// means retry is atomic across every phase-declared repo.
	RetryPhase key.Binding

	// Review Comments-specific
	AutoAddressReview key.Binding

	// Dashboard approve-and-remember
	ApproveAndRemember key.Binding

	// Attach-specific
	Detach key.Binding
}

var keys = keyMap{
	Quit:              key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	New:               key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new feature")),
	Enter:             key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
	Back:              key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
	Publish:           key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "publish")),
	Attach:            key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "attach")),
	Approve:           key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "approve permissions")),
	Help:              key.NewBinding(key.WithKeys("h"), key.WithHelp("h", "answer help")),
	Restart:           key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "restart phase")),
	Stop:              key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "stop feature")),
	Rewind:            key.NewBinding(key.WithKeys("ctrl+r"), key.WithHelp("ctrl+r", "rewind")),
	Delete:            key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete feature")),
	Up:                key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("up", "move up")),
	Down:              key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("down", "move down")),
	Advance:           key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "advance phase")),
	Tab:               key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "switch panel")),
	ViewDiff:          key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "view diff")),
	CleanWorktree:     key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "clean worktree")),
	ManualPublish:     key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "mark manually published")),
	ResumeAll:         key.NewBinding(key.WithKeys("R"), key.WithHelp("Shift+R", "resume all")),
	Rebase:            key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "rebase on main")),
	MergeLocal:        key.NewBinding(key.WithKeys("M"), key.WithHelp("Shift+M", "merge to base branch")),
	MarkDone:          key.NewBinding(key.WithKeys("D"), key.WithHelp("Shift+D", "mark as done")),
	ReviewComments:    key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "review comments")),
	Tweak:             key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "tweak implementation")),
	Refactor:          key.NewBinding(key.WithKeys("F"), key.WithHelp("Shift+F", "refactor")),
	EditConfig:        key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit config")),
	ViewLogs:          key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "view logs")),
	Chat:              key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "ask anything")),
	HelpOverlay:       key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	ToggleInputNotify: key.NewBinding(key.WithKeys("N"), key.WithHelp("Shift+N", "toggle input alerts")),

	// Dashboard panel navigation
	PanelLeft:  key.NewBinding(key.WithKeys("left"), key.WithHelp("←", "focus list")),
	PanelRight: key.NewBinding(key.WithKeys("right"), key.WithHelp("→", "focus detail")),

	// Dashboard tools
	WorkspaceManager: key.NewBinding(key.WithKeys("W"), key.WithHelp("Shift+W", "manage workspaces")),

	// Wizard-specific
	ShiftTab:   key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "previous step")),
	Left:       key.NewBinding(key.WithKeys("left"), key.WithHelp("←", "cycle left")),
	Right:      key.NewBinding(key.WithKeys("right"), key.WithHelp("→", "cycle right")),
	PasteImage: key.NewBinding(key.WithKeys("ctrl+v"), key.WithHelp("ctrl+v", "paste image")),

	// Detail view phase retry (Shift+R) — wired to RetryPhase under the
	// unified flow.
	RetryPhase: key.NewBinding(key.WithKeys("R"), key.WithHelp("Shift+R", "retry phase")),

	// Recovery-specific
	RecoveryResume: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "resume")),
	RecoveryKill:   key.NewBinding(key.WithKeys("k"), key.WithHelp("k", "kill")),
	RecoverySkip:   key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "skip")),

	// Review Comments-specific
	AutoAddressReview: key.NewBinding(key.WithKeys("A"), key.WithHelp("Shift+A", "auto-address")),

	// Dashboard approve-and-remember
	ApproveAndRemember: key.NewBinding(key.WithKeys("A"), key.WithHelp("Shift+A", "approve & remember")),

	// Attach-specific
	Detach: key.NewBinding(key.WithKeys("ctrl+]", "esc"), key.WithHelp("ctrl+]/esc", "detach")),
}
