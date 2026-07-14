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

// DashboardLeftSection defines keybindings shown when the dashboard left panel is focused.
var DashboardLeftSection = HelpSection{
	Title: helpContextDashboard,
	Bindings: []HelpBinding{
		{"n", "New feature (launch wizard)"},
		{"enter", "Focus right panel / expand"},
		{"a", "Watch active work; Answer, Approve, or Review when prompted"},
		{"Shift+R", "Resume all interrupted features"},
		{"tab", "Switch panel"},
		{"↑/k", "Move up"},
		{"↓/j", "Move down"},
		{"v", "View diff"},
		{"p", "Publish (when code ready)"},
		{"y", "Approve pending permissions"},
		{"Shift+A", "Approve & remember permissions"},
		{"Shift+E", "Edit workspace config"},
		{"d", "Delete feature"},
	},
}

// DetailSection defines keybindings shown for the feature detail / right panel view.
var DetailSection = HelpSection{
	Title: "Feature Detail",
	Bindings: []HelpBinding{
		{"a", "Watch active work; Answer, Approve, or Review when prompted"},
		{"o", "Show overview"},
		{"y", "Approve pending permissions"},
		{"Shift+A", "Approve & remember permissions"},
		{"h", "Answer agent's help question"},
		{"r", "Restart current phase"},
		{"s", "Stop running feature"},
		{"ctrl+r", "Rewind to phase"},
		{"l", "Live Preview / View logs"},
		{"v", "View diff"},
		{"p", "Publish (when code ready)"},
		{"b", "Rebase on main (code ready or published)"},
		{"e", "Edit config"},
		{"Shift+E", "Edit workspace config"},
		{"Shift+M", "Merge to base branch (local repos)"},
		{"Shift+D", "Mark as done"},
		{"g", "Review comments (published)"},
		{"c", "Clean worktree"},
		{"d", "Delete feature"},
		{"esc", "Back to dashboard"},
	},
}

// GeneralSection defines keybindings available in all views.
var GeneralSection = HelpSection{
	Title: "General",
	Bindings: []HelpBinding{
		{"/", "Ask me Anything (AI chat)"},
		{"?", "Show this help"},
		{"q", "Quit"},
		{"ctrl+c", "Force quit"},
	},
}

// AttachSection defines keybindings available in the attach (PTY) view.
var AttachSection = HelpSection{
	Title: "Watch View",
	Bindings: []HelpBinding{
		{"ctrl+]/ctrl+x/esc", "Stop watching and return to dashboard"},
	},
}

// WizardSection defines keybindings available in the feature creation wizard.
var WizardSection = HelpSection{
	Title: "Wizard",
	Bindings: []HelpBinding{
		{"enter", "Next step"},
		{"shift+tab", "Previous step"},
		{"tab", "Toggle / cycle selection"},
		{"↑/↓", "Move selection"},
		{"←/→", "Cycle model"},
		{"ctrl+v", "Paste image"},
		{"@", "File picker"},
		{"esc", "Cancel"},
	},
}

// ConfirmationSection defines keybindings for confirmation dialogs.
var ConfirmationSection = HelpSection{
	Title: "Confirmations",
	Bindings: []HelpBinding{
		{"y / Y", "Confirm"},
		{"any other key", "Cancel"},
	},
}
