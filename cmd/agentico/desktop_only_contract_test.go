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

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDistributionHasNoLegacyTerminalUI(t *testing.T) {
	repoRoot := filepath.Join("..", "..")

	if _, err := os.Stat(filepath.Join(repoRoot, "internal", "tui")); !os.IsNotExist(err) {
		t.Fatalf("legacy terminal UI package still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "docs", "keybindings.md")); !os.IsNotExist(err) {
		t.Fatalf("legacy terminal keybinding reference still exists: %v", err)
	}

	for _, rel := range []string{"go.mod", "Makefile", ".goreleaser.yaml"} {
		body, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", rel, err)
		}
		for _, legacy := range []string{"internal/tui", "bubbletea", "charm.land/bubbles", "charm.land/lipgloss"} {
			if strings.Contains(string(body), legacy) {
				t.Errorf("%s still references legacy terminal UI dependency %q", rel, legacy)
			}
		}
	}

	var stdout, stderr bytes.Buffer
	serverCalls := 0
	code := runArgs(nil, &stdout, &stderr, func(string, string, bool, []string, bool) int {
		serverCalls++
		return 0
	}, failingUpdater(t))
	if code != 0 {
		t.Fatalf("runArgs() code = %d; stderr = %q", code, stderr.String())
	}
	if serverCalls != 1 {
		t.Fatalf("default server launch calls = %d, want 1", serverCalls)
	}
}

func TestOrchestratorSourceHasNoDeletedTerminalClientReferences(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	// Keep this list assembled so the contract test itself is not the only
	// match for the retired package path it protects against.
	retiredPackage := strings.Join([]string{"internal", "tui"}, "/")
	retiredFramework := "bubble" + "tea"
	for _, dir := range []string{"agents", "cmd", "docs", "internal", "skills", "test"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, dir), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || path == filepath.Join(repoRoot, "cmd", "agentico", "desktop_only_contract_test.go") {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			contents := string(body)
			if strings.Contains(contents, retiredPackage) || strings.Contains(strings.ToLower(contents), retiredFramework) {
				rel, relErr := filepath.Rel(repoRoot, path)
				if relErr != nil {
					return relErr
				}
				t.Errorf("%s references retired terminal client material", rel)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
}
