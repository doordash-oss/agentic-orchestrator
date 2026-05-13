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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoReferencesToRetiredAutonomousTweakCode(t *testing.T) {
	t.Parallel()
	bannedSnippets := []string{
		"BuildTweakPlan",
		"tweakInputActive",
		"tweakInput ",
		"startTweakImplementationCmd",
		"completeTweakCmd",
		"renderTweakOverlay",
		"tweakInputView",
		"tweakActive ",
	}

	scanDirs := []string{
		".",        // internal/tui/
		"../agent", // internal/agent/
	}

	for _, dir := range scanDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir(%s): %v", dir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}
			content := string(data)
			for _, snippet := range bannedSnippets {
				if strings.Contains(content, snippet) {
					t.Errorf("%s unexpectedly contains retired autonomous tweak symbol %q", path, snippet)
				}
			}
		}
	}
}
