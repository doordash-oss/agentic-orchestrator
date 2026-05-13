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
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
)

// notifier holds cached notification backend state, resolved once at startup.
type notifier struct {
	hasTerminalNotifier bool
	terminalBundleID    string
	warned              atomic.Bool
}

var defaultNotifier = resolveNotifier()

// overrideBundleID is set from config at startup. Empty means use auto-detection.
var overrideBundleID string

// SetTerminalBundleID allows overriding the auto-detected terminal bundle ID
// from user configuration.
func SetTerminalBundleID(bundleID string) {
	overrideBundleID = bundleID
}

func resolveNotifier() *notifier {
	n := &notifier{}
	if _, err := exec.LookPath("terminal-notifier"); err == nil {
		n.hasTerminalNotifier = true
	}
	n.terminalBundleID = detectTerminalBundleID()
	return n
}

// terminalBundleIDs maps TERM_PROGRAM values (lowercased) to macOS bundle identifiers.
var terminalBundleIDs = map[string]string{
	"apple_terminal": "com.apple.Terminal",
	"iterm.app":      "com.googlecode.iterm2",
	"ghostty":        "com.mitchellh.ghostty",
	"alacritty":      "org.alacritty",
	"warp":           "dev.warp.Warp-Stable",
	"kitty":          "net.kovidgoyal.kitty",
	"wezterm":        "org.wezfurlong.wezterm",
}

func detectTerminalBundleID() string {
	tp := strings.ToLower(os.Getenv("TERM_PROGRAM"))
	if bid, ok := terminalBundleIDs[tp]; ok {
		return bid
	}
	return ""
}

// notifyUserCmd returns a tea.Cmd that sends a terminal bell and
// an OS-level desktop notification to alert the user that an agent
// needs their input.
func notifyUserCmd(featureName, reason string) tea.Cmd {
	return func() tea.Msg {
		// Terminal bell -- written to stderr so it reaches the terminal
		// without interfering with Bubbletea's renderer on stdout.
		fmt.Fprint(os.Stderr, "\a")

		if runtime.GOOS != "darwin" {
			return nil
		}

		bundleID := overrideBundleID
		if bundleID == "" {
			bundleID = defaultNotifier.terminalBundleID
		}

		if defaultNotifier.hasTerminalNotifier {
			sendTerminalNotifier(featureName, reason, bundleID)
		} else {
			sendOsascript(featureName, reason)
			suggestTerminalNotifier()
		}

		return nil
	}
}

func sendTerminalNotifier(featureSlug, reason, bundleID string) {
	startNotificationCommand("terminal-notifier", buildTerminalNotifierArgs(featureSlug, reason, bundleID)...)
}

// buildTerminalNotifierArgs returns the terminal-notifier arg vector. The
// title and group identifier carry the renamed product brand so concurrent
// installs of any legacy build do not coalesce notifications together.
func buildTerminalNotifierArgs(featureSlug, reason, bundleID string) []string {
	args := []string{
		"-title", "Agentic Orchestrator",
		"-subtitle", featureSlug,
		"-message", reason,
		"-group", "agentico-" + featureSlug,
		"-sound", "default",
	}
	if bundleID != "" {
		args = append(args, "-activate", bundleID)
	}
	return args
}

func sendOsascript(featureName, reason string) {
	title, body := buildOsascriptNotification(featureName, reason)
	startNotificationCommand("osascript", "-e",
		fmt.Sprintf(`display notification %q with title %q`, body, title),
	)
}

// buildOsascriptNotification returns the title and body for the osascript
// fallback path. Title carries the renamed product brand.
func buildOsascriptNotification(featureName, reason string) (string, string) {
	return "Agentic Orchestrator: input needed", fmt.Sprintf("%s — %s", featureName, reason)
}

func suggestTerminalNotifier() {
	if !defaultNotifier.warned.CompareAndSwap(false, true) {
		return
	}
	fmt.Fprintln(os.Stderr, "hint: install terminal-notifier for better macOS notifications: brew install terminal-notifier")
}

func startNotificationCommand(name string, args ...string) {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return
	}
	go func() {
		_ = cmd.Wait()
	}()
}
