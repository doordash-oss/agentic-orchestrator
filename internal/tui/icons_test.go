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
	"testing"
)

func TestHasNerdFontExplicitOn(t *testing.T) {
	t.Setenv("NERD_FONT", "1")
	if !hasNerdFont() {
		t.Error("expected true for NERD_FONT=1")
	}
}

func TestHasNerdFontExplicitTrue(t *testing.T) {
	t.Setenv("NERD_FONT", "true")
	if !hasNerdFont() {
		t.Error("expected true for NERD_FONT=true")
	}
}

func TestHasNerdFontExplicitOff(t *testing.T) {
	t.Setenv("NERD_FONT", "0")
	t.Setenv("TERM_PROGRAM", "Apple_Terminal")
	if hasNerdFont() {
		t.Error("expected false for NERD_FONT=0")
	}
}

func TestHasNerdFontExplicitFalse(t *testing.T) {
	t.Setenv("NERD_FONT", "false")
	t.Setenv("TERM_PROGRAM", "kitty")
	if hasNerdFont() {
		t.Error("expected false for NERD_FONT=false")
	}
}

func TestHasNerdFontHeuristicWezterm(t *testing.T) {
	t.Setenv("NERD_FONT", "")
	t.Setenv("TERM_PROGRAM", "WezTerm")
	if !hasNerdFont() {
		t.Error("expected true for WezTerm")
	}
}

func TestHasNerdFontHeuristicKitty(t *testing.T) {
	t.Setenv("NERD_FONT", "")
	t.Setenv("TERM_PROGRAM", "kitty")
	if !hasNerdFont() {
		t.Error("expected true for kitty")
	}
}

func TestHasNerdFontFallback(t *testing.T) {
	t.Setenv("NERD_FONT", "")
	t.Setenv("TERM_PROGRAM", "Apple_Terminal")
	if hasNerdFont() {
		t.Error("expected false for Apple_Terminal without NERD_FONT")
	}
}

func TestIconSetPopulated(t *testing.T) {
	// Regardless of detection, the icons struct should have non-empty values
	if icons.Created == "" {
		t.Error("icons.Created should not be empty")
	}
	if icons.Done == "" {
		t.Error("icons.Done should not be empty")
	}
	if icons.Failed == "" {
		t.Error("icons.Failed should not be empty")
	}
}
