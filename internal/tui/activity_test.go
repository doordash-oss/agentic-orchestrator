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

func TestExtractActivityLinesCleanInput(t *testing.T) {
	t.Parallel()
	// With JSON protocol, input from MessageLog is already clean text
	raw := "Read src/auth/handler.go\nFound 3 functions in /tmp/test.go\n"
	lines := extractActivityLines(raw, 10)
	if len(lines) < 1 {
		t.Error("expected at least 1 meaningful line")
	}
	for _, line := range lines {
		if strings.Contains(line, "\x1b") {
			t.Errorf("line contains ANSI: %q", line)
		}
	}
}

func TestExtractActivityLinesLastN(t *testing.T) {
	t.Parallel()
	raw := "Read file one.go\nRead file two.go\nRead file three.go\nRead file four.go\nRead file five.go\n"
	lines := extractActivityLines(raw, 3)
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d", len(lines))
	}
	if len(lines) > 0 && !strings.Contains(lines[0], "three") {
		t.Errorf("expected 'three' first, got %q", lines[0])
	}
}

func TestExtractActivityLinesEmpty(t *testing.T) {
	t.Parallel()
	lines := extractActivityLines("", 5)
	if len(lines) != 0 {
		t.Errorf("expected 0 lines for empty input, got %d", len(lines))
	}
}

func TestExtractActivityLinesFiltersNoise(t *testing.T) {
	t.Parallel()
	raw := "*(thinking)*\nRead src/main.go\n⠋ Working...\nFound 5 files in /tmp/test.go\ncost: $0.02\n"
	lines := extractActivityLines(raw, 10)
	for _, line := range lines {
		if strings.Contains(line, "thinking") {
			t.Errorf("should filter thinking: %q", line)
		}
		if strings.Contains(line, "⠋") {
			t.Errorf("should filter spinner: %q", line)
		}
		if strings.Contains(line, "cost:") {
			t.Errorf("should filter cost: %q", line)
		}
	}
}

func TestExtractActivityLinesAllowsToolOutput(t *testing.T) {
	t.Parallel()
	raw := "Read internal/tui/styles.go\nEdit internal/tui/api_app.go\nBash go test ./...\nok  pkg/foo 0.5s\n"
	lines := extractActivityLines(raw, 10)
	if len(lines) < 3 {
		t.Errorf("expected at least 3 tool output lines, got %d: %v", len(lines), lines)
	}
}

func TestExtractActivityLinesDedups(t *testing.T) {
	t.Parallel()
	raw := "Read file.go\nRead file.go\nRead file.go\nRead other.go\n"
	lines := extractActivityLines(raw, 10)
	if len(lines) != 2 {
		t.Errorf("expected 2 deduped lines, got %d: %v", len(lines), lines)
	}
}
