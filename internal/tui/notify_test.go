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

import "testing"

func TestDetectTerminalBundleID(t *testing.T) {
	tests := []struct {
		termProgram string
		want        string
	}{
		{"iTerm.app", "com.googlecode.iterm2"},
		{"Apple_Terminal", "com.apple.Terminal"},
		{"ghostty", "com.mitchellh.ghostty"},
		{"Ghostty", "com.mitchellh.ghostty"},
		{"alacritty", "org.alacritty"},
		{"WezTerm", "org.wezfurlong.wezterm"},
		{"kitty", "net.kovidgoyal.kitty"},
		{"warp", "dev.warp.Warp-Stable"},
		{"tmux", ""},
		{"unknown-terminal", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.termProgram, func(t *testing.T) {
			t.Setenv("TERM_PROGRAM", tt.termProgram)
			got := detectTerminalBundleID()
			if got != tt.want {
				t.Errorf("detectTerminalBundleID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSetTerminalBundleID(t *testing.T) {
	original := overrideBundleID
	defer func() { overrideBundleID = original }()

	SetTerminalBundleID("com.example.test")
	if overrideBundleID != "com.example.test" {
		t.Errorf("overrideBundleID = %q, want %q", overrideBundleID, "com.example.test")
	}
}
