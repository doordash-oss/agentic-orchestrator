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
	"strings"
	"testing"
)

// TestWelcomePanelUsesRenamedProductName guards the empty-dashboard
// explainer copy so it advertises the renamed product instead of the
// bare "Agentic" word that overlaps with the parent CLI name.
func TestWelcomePanelUsesRenamedProductName(t *testing.T) {
	t.Parallel()
	m := NewDashboardModel(nil, "")
	panel := m.renderWelcomePanel(60)
	if !strings.Contains(panel, "What does Agentic Orchestrator do?") {
		t.Errorf("welcome panel missing renamed title:\n%s", panel)
	}
	if !strings.Contains(panel, "Agentic Orchestrator works best") {
		t.Errorf("welcome panel missing renamed product framing:\n%s", panel)
	}
}

// TestNotificationTerminalNotifierArgsUseRenamedBrand asserts the
// terminal-notifier invocation surfaces the renamed product title and a
// disambiguating notification group identifier so coalescing groups stay
// distinct from any concurrently installed legacy build.
func TestNotificationTerminalNotifierArgsUseRenamedBrand(t *testing.T) {
	t.Parallel()
	args := buildTerminalNotifierArgs("my-feature", "needs input", "com.example.terminal")
	if got := positional(args, "-title"); got != "Agentic Orchestrator" {
		t.Errorf("title = %q, want \"Agentic Orchestrator\"", got)
	}
	if got := positional(args, "-group"); got != "agentico-my-feature" {
		t.Errorf("group = %q, want \"agentico-my-feature\"", got)
	}
}

// TestNotificationFallbackTitleUsesRenamedBrand checks the osascript
// fallback path that runs when terminal-notifier is unavailable.
func TestNotificationFallbackTitleUsesRenamedBrand(t *testing.T) {
	t.Parallel()
	title, _ := buildOsascriptNotification("my-feature", "needs input")
	if title != "Agentic Orchestrator: input needed" {
		t.Errorf("title = %q, want \"Agentic Orchestrator: input needed\"", title)
	}
}

// TestDashboardHeaderAdvertisesOrchestratorBrand asserts that the
// dashboard header surface visibly carries "Orchestrator" branding next to
// the ASCII logo, including in dangerous-skip-permissions mode where the
// header switches to the red/black DSP theme.
func TestDashboardHeaderAdvertisesOrchestratorBrand(t *testing.T) {
	t.Parallel()
	for _, dsp := range []bool{false, true} {
		m := NewDashboardModel(nil, "")
		m.width = 120
		m.height = 30
		m.dangerouslySkipPerms = dsp
		header := m.renderHeader(120)
		if !strings.Contains(header, "Orchestrator") {
			t.Errorf("dsp=%v: header missing renamed product branding:\n%s", dsp, header)
		}
	}
}

func positional(args []string, key string) string {
	for i, a := range args {
		if a == key && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
