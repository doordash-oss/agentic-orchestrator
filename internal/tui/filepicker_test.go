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
	"testing"

	tea "charm.land/bubbletea/v2"
)

// singleRepo is a helper that wraps a directory as a single-repo map.
func singleRepo(t *testing.T, dir string) map[string]string {
	t.Helper()
	return map[string]string{"testrepo": dir}
}

// --- Existing tests, updated for new constructor signature ---

func TestFilePickerActivateDeactivate(t *testing.T) {
	fp := NewFilePickerModel(singleRepo(t, t.TempDir()))
	if fp.IsActive() {
		t.Error("expected inactive initially")
	}
	fp.Activate("")
	if !fp.IsActive() {
		t.Error("expected active after Activate")
	}
	fp.Deactivate()
	if fp.IsActive() {
		t.Error("expected inactive after Deactivate")
	}
}

func TestFilePickerMatchesRealDirectory(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"alpha", "bravo", "charlie"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	fp := NewFilePickerModel(singleRepo(t, dir))
	fp.Activate("")

	if len(fp.matches) != 4 {
		t.Errorf("expected 4 matches (3 dirs + 1 file), got %d: %v", len(fp.matches), fp.matches)
	}
	// Paths should be prefixed with "testrepo/" (single-repo auto-descend)
	for _, m := range fp.matches {
		if filepath.IsAbs(m) {
			t.Errorf("expected relative path, got %q", m)
		}
		if len(m) < len("testrepo/") {
			t.Errorf("expected testrepo/ prefix, got %q", m)
		}
	}
}

func TestFilePickerFilterByPrefix(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"alpha", "also-alpha", "bravo"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	fp := NewFilePickerModel(singleRepo(t, dir))
	fp.Activate("")
	fp.SetPrefix("testrepo/al")

	if len(fp.matches) != 2 {
		t.Errorf("expected 2 matches starting with 'al', got %d: %v", len(fp.matches), fp.matches)
	}
}

func TestFilePickerSetPrefix(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"alpha", "also-alpha", "bravo"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	fp := NewFilePickerModel(singleRepo(t, dir))
	fp.Activate("")

	if len(fp.matches) != 3 {
		t.Errorf("expected 3 matches, got %d", len(fp.matches))
	}

	fp.SetPrefix("testrepo/al")
	if len(fp.matches) != 2 {
		t.Errorf("expected 2 matches after SetPrefix('testrepo/al'), got %d: %v", len(fp.matches), fp.matches)
	}

	fp.SetPrefix("testrepo/b")
	if len(fp.matches) != 1 {
		t.Errorf("expected 1 match after SetPrefix('testrepo/b'), got %d: %v", len(fp.matches), fp.matches)
	}
}

func TestFilePickerNavigation(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"aaa", "bbb", "ccc"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	fp := NewFilePickerModel(singleRepo(t, dir))
	fp.Activate("")

	if fp.cursor != 0 {
		t.Errorf("expected cursor at 0, got %d", fp.cursor)
	}

	fp, _, _ = fp.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if fp.cursor != 1 {
		t.Errorf("expected cursor at 1 after down, got %d", fp.cursor)
	}

	fp, _, _ = fp.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if fp.cursor != 0 {
		t.Errorf("expected cursor at 0 after up, got %d", fp.cursor)
	}
}

func TestFilePickerCtrlNCtrlPNavigation(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"aaa", "bbb", "ccc"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	fp := NewFilePickerModel(singleRepo(t, dir))
	fp.Activate("")

	if fp.cursor != 0 {
		t.Errorf("expected cursor at 0, got %d", fp.cursor)
	}

	// ctrl+n moves down
	fp, _, _ = fp.Update(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if fp.cursor != 1 {
		t.Errorf("expected cursor at 1 after ctrl+n, got %d", fp.cursor)
	}

	// ctrl+p moves up
	fp, _, _ = fp.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if fp.cursor != 0 {
		t.Errorf("expected cursor at 0 after ctrl+p, got %d", fp.cursor)
	}

	// ctrl+p at top stays at 0
	fp, _, _ = fp.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if fp.cursor != 0 {
		t.Errorf("expected cursor to stay at 0, got %d", fp.cursor)
	}

	// Navigate to bottom with ctrl+n
	fp, _, _ = fp.Update(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	fp, _, _ = fp.Update(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if fp.cursor != 2 {
		t.Errorf("expected cursor at 2, got %d", fp.cursor)
	}

	// ctrl+n at bottom stays at max
	fp, _, _ = fp.Update(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if fp.cursor != 2 {
		t.Errorf("expected cursor to stay at 2, got %d", fp.cursor)
	}
}

func TestFilePickerSelectionEnter(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "target-dir"), 0o755); err != nil {
		t.Fatal(err)
	}

	fp := NewFilePickerModel(singleRepo(t, dir))
	fp.Activate("")

	fp, selected, consumed := fp.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !consumed {
		t.Error("expected enter to be consumed")
	}
	if selected == "" {
		t.Error("expected non-empty selection")
	}
	if fp.IsActive() {
		t.Error("expected picker to deactivate after enter")
	}
}

func TestFilePickerTabDrillsIntoDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src", "components"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}

	fp := NewFilePickerModel(singleRepo(t, dir))
	fp.Activate("")

	// Only match should be "testrepo/src/"
	if len(fp.matches) != 1 || fp.matches[0] != "testrepo/src/" {
		t.Fatalf("expected [testrepo/src/], got %v", fp.matches)
	}

	// Tab on directory should drill in, NOT deactivate
	fp, selected, consumed := fp.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if !consumed {
		t.Error("expected tab to be consumed")
	}
	if selected != "testrepo/src/" {
		t.Errorf("expected selected = 'testrepo/src/', got %q", selected)
	}
	if !fp.IsActive() {
		t.Error("expected picker to stay active after tab on directory (drill)")
	}
	if fp.prefix != "testrepo/src/" {
		t.Errorf("expected prefix = 'testrepo/src/', got %q", fp.prefix)
	}
	// Should now show contents of src/
	if len(fp.matches) != 2 {
		t.Errorf("expected 2 matches inside src/ (components/ + main.go), got %d: %v", len(fp.matches), fp.matches)
	}
}

func TestFilePickerTabCompletesFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# Hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	fp := NewFilePickerModel(singleRepo(t, dir))
	fp.Activate("")

	fp, selected, consumed := fp.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if !consumed {
		t.Error("expected tab to be consumed")
	}
	if selected != "testrepo/readme.md" {
		t.Errorf("expected selected = 'testrepo/readme.md', got %q", selected)
	}
	if fp.IsActive() {
		t.Error("expected picker to deactivate after tab on file")
	}
}

func TestFilePickerPrefixTrieNavigation(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "internal", "tui"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "internal", "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}

	fp := NewFilePickerModel(singleRepo(t, dir))

	// Start: list root (single-repo auto-descend)
	fp.Activate("")
	if len(fp.matches) != 2 {
		t.Fatalf("expected 2 matches [testrepo/cmd/ testrepo/internal/], got %v", fp.matches)
	}

	// Type "testrepo/i" → filter to "internal/"
	fp.SetPrefix("testrepo/i")
	if len(fp.matches) != 1 || fp.matches[0] != "testrepo/internal/" {
		t.Fatalf("expected [testrepo/internal/], got %v", fp.matches)
	}

	// Type "testrepo/internal/" → list contents
	fp.SetPrefix("testrepo/internal/")
	if len(fp.matches) != 2 {
		t.Fatalf("expected 2 matches [testrepo/internal/config/ testrepo/internal/tui/], got %v", fp.matches)
	}

	// Type "testrepo/internal/t" → filter to "tui/"
	fp.SetPrefix("testrepo/internal/t")
	if len(fp.matches) != 1 || fp.matches[0] != "testrepo/internal/tui/" {
		t.Fatalf("expected [testrepo/internal/tui/], got %v", fp.matches)
	}
}

func TestFilePickerEscCancels(t *testing.T) {
	fp := NewFilePickerModel(singleRepo(t, t.TempDir()))
	fp.Activate("")

	fp, selected, consumed := fp.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !consumed {
		t.Error("expected esc to be consumed")
	}
	if selected != "" {
		t.Error("expected empty selection on esc")
	}
	if fp.IsActive() {
		t.Error("expected picker to deactivate after esc")
	}
}

func TestFilePickerViewRendering(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "mydir"), 0o755); err != nil {
		t.Fatal(err)
	}

	fp := NewFilePickerModel(singleRepo(t, dir))
	fp.Activate("")

	view := fp.View()
	if view == "" {
		t.Error("expected non-empty view when active with matches")
	}
	if !containsString(view, "File completions") {
		t.Error("expected header in view")
	}
}

func TestFilePickerSkipsHiddenFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{".hidden", "visible"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	fp := NewFilePickerModel(singleRepo(t, dir))
	fp.Activate("")

	if len(fp.matches) != 1 {
		t.Errorf("expected 1 match (hidden should be skipped), got %d: %v", len(fp.matches), fp.matches)
	}
}

func TestFilePickerRegularCharsNotConsumed(t *testing.T) {
	fp := NewFilePickerModel(singleRepo(t, t.TempDir()))
	fp.Activate("")

	_, _, consumed := fp.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if consumed {
		t.Error("regular characters should not be consumed by the picker")
	}
}

// --- New tests for virtual repo roots ---

func TestFilePickerRepoNameListing(t *testing.T) {
	alphaDir := t.TempDir()
	bravoDir := t.TempDir()
	repos := map[string]string{
		"alpha": alphaDir,
		"bravo": bravoDir,
	}

	fp := NewFilePickerModel(repos)
	fp.Activate("")

	if len(fp.matches) != 2 {
		t.Fatalf("expected 2 repo matches, got %d: %v", len(fp.matches), fp.matches)
	}
	if fp.matches[0] != "alpha/" || fp.matches[1] != "bravo/" {
		t.Errorf("expected [alpha/ bravo/], got %v", fp.matches)
	}
	if fp.cursor != 0 {
		t.Errorf("expected cursor at 0, got %d", fp.cursor)
	}
}

func TestFilePickerRepoNameFilter(t *testing.T) {
	repos := map[string]string{
		"agentic": t.TempDir(),
		"aurora":  t.TempDir(),
		"bravo":   t.TempDir(),
	}

	fp := NewFilePickerModel(repos)
	fp.Activate("a")

	if len(fp.matches) != 2 {
		t.Errorf("expected 2 repos matching 'a', got %d: %v", len(fp.matches), fp.matches)
	}

	fp.SetPrefix("ag")
	if len(fp.matches) != 1 || fp.matches[0] != "agentic/" {
		t.Errorf("expected [agentic/], got %v", fp.matches)
	}
}

func TestFilePickerDrillIntoRepo(t *testing.T) {
	alphaDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(alphaDir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(alphaDir, "README.md"), []byte("# Hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	repos := map[string]string{
		"alpha": alphaDir,
		"bravo": t.TempDir(),
	}

	fp := NewFilePickerModel(repos)
	fp.Activate("")

	// Shows repo names
	if len(fp.matches) != 2 {
		t.Fatalf("expected 2 repo matches, got %v", fp.matches)
	}

	// Tab on first repo → drills in
	fp, selected, consumed := fp.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if !consumed {
		t.Error("expected tab to be consumed")
	}
	if selected != "alpha/" {
		t.Errorf("expected selected='alpha/', got %q", selected)
	}
	if fp.currentRepo != "alpha" {
		t.Errorf("expected currentRepo='alpha', got %q", fp.currentRepo)
	}
	if !fp.IsActive() {
		t.Error("expected picker to stay active after drill")
	}
	if fp.prefix != "alpha/" {
		t.Errorf("expected prefix='alpha/', got %q", fp.prefix)
	}
	// Should show filesystem entries of alpha repo
	if len(fp.matches) != 2 {
		t.Errorf("expected 2 matches (README.md + src/), got %d: %v", len(fp.matches), fp.matches)
	}
	// Matches should be prefixed with repo name
	for _, m := range fp.matches {
		if m != "alpha/README.md" && m != "alpha/src/" {
			t.Errorf("unexpected match: %q", m)
		}
	}
}

func TestFilePickerWithinRepoNavigation(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src", "utils"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}

	repos := map[string]string{
		"myapp": dir,
		"other": t.TempDir(),
	}

	fp := NewFilePickerModel(repos)
	fp.Activate("")

	// Drill into myapp
	fp, _, _ = fp.Update(tea.KeyPressMsg{Code: tea.KeyTab})

	// Filter within repo
	fp.SetPrefix("myapp/s")
	if len(fp.matches) != 1 || fp.matches[0] != "myapp/src/" {
		t.Errorf("expected [myapp/src/], got %v", fp.matches)
	}

	// Drill into src/
	fp.SetPrefix("myapp/src/")
	if len(fp.matches) != 2 {
		t.Errorf("expected 2 matches in src/, got %d: %v", len(fp.matches), fp.matches)
	}
}

func TestFilePickerFileSelectionReturnsRepoQualifiedPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}

	repos := map[string]string{
		"myapp": dir,
		"other": t.TempDir(),
	}

	fp := NewFilePickerModel(repos)
	fp.Activate("")

	// Drill into myapp
	fp, _, _ = fp.Update(tea.KeyPressMsg{Code: tea.KeyTab})

	// Tab on file should return repo-qualified path
	fp, selected, _ := fp.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if selected != "myapp/main.go" {
		t.Errorf("expected 'myapp/main.go', got %q", selected)
	}
	if fp.IsActive() {
		t.Error("expected picker to deactivate after file selection")
	}
}

func TestFilePickerSingleRepoSkipsRepoLevel(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	repos := map[string]string{"myrepo": dir}

	fp := NewFilePickerModel(repos)
	fp.Activate("")

	// Should skip repo-name level and show filesystem entries directly
	if fp.currentRepo != "myrepo" {
		t.Errorf("expected currentRepo='myrepo', got %q", fp.currentRepo)
	}
	// Matches should be filesystem entries, prefixed with "myrepo/"
	if len(fp.matches) != 2 {
		t.Fatalf("expected 2 matches, got %d: %v", len(fp.matches), fp.matches)
	}
	for _, m := range fp.matches {
		if m != "myrepo/README.md" && m != "myrepo/src/" {
			t.Errorf("unexpected match %q, expected myrepo/README.md or myrepo/src/", m)
		}
	}
}

func TestFilePickerSingleRepoTabDrill(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}

	repos := map[string]string{"myrepo": dir}

	fp := NewFilePickerModel(repos)
	fp.Activate("")

	// Should show "myrepo/src/"
	if len(fp.matches) != 1 || fp.matches[0] != "myrepo/src/" {
		t.Fatalf("expected [myrepo/src/], got %v", fp.matches)
	}

	// Tab drills into src/
	fp, selected, _ := fp.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if selected != "myrepo/src/" {
		t.Errorf("expected selected='myrepo/src/', got %q", selected)
	}
	if !fp.IsActive() {
		t.Error("expected picker to stay active after drill")
	}
	if len(fp.matches) != 1 || fp.matches[0] != "myrepo/src/main.go" {
		t.Errorf("expected [myrepo/src/main.go], got %v", fp.matches)
	}

	// Tab completes file → deactivates
	fp, selected, _ = fp.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if selected != "myrepo/src/main.go" {
		t.Errorf("expected 'myrepo/src/main.go', got %q", selected)
	}
	if fp.IsActive() {
		t.Error("expected picker to deactivate after file completion")
	}
}

func TestFilePickerEscResetsRepoLevel(t *testing.T) {
	repos := map[string]string{
		"alpha": t.TempDir(),
		"bravo": t.TempDir(),
	}

	fp := NewFilePickerModel(repos)
	fp.Activate("")

	// Drill into alpha
	fp, _, _ = fp.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if fp.currentRepo != "alpha" {
		t.Errorf("expected currentRepo='alpha', got %q", fp.currentRepo)
	}

	// Esc should deactivate and reset currentRepo
	fp, _, _ = fp.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if fp.IsActive() {
		t.Error("expected picker to deactivate after esc")
	}
	if fp.currentRepo != "" {
		t.Errorf("expected currentRepo reset to empty, got %q", fp.currentRepo)
	}
}

func TestFilePickerEmptyRepoMap(t *testing.T) {
	fp := NewFilePickerModel(map[string]string{})
	fp.Activate("")

	if len(fp.matches) != 0 {
		t.Errorf("expected no matches with empty repo map, got %d: %v", len(fp.matches), fp.matches)
	}
	if fp.View() != "" {
		t.Error("expected empty view with no matches")
	}
}

func TestFilePickerOverlappingRepoNames(t *testing.T) {
	// Regression: repos "rootA" and "rootA/myrepo" overlap. Typing
	// "rootA/myrepo/" must select the longer repo, not lock into "rootA".
	rootADir := t.TempDir()
	myrepoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(myrepoDir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}

	repos := map[string]string{
		"rootA":        rootADir,
		"rootA/myrepo": myrepoDir,
	}

	fp := NewFilePickerModel(repos)
	fp.Activate("")

	// At repo level, both should appear
	if len(fp.matches) != 2 {
		t.Fatalf("expected 2 repo matches, got %v", fp.matches)
	}

	// Type the qualified repo prefix — must select "rootA/myrepo", not "rootA"
	fp.SetPrefix("rootA/myrepo/")
	if fp.currentRepo != "rootA/myrepo" {
		t.Errorf("expected currentRepo='rootA/myrepo', got %q", fp.currentRepo)
	}
	// Should list filesystem contents of myrepoDir
	if len(fp.matches) != 1 || fp.matches[0] != "rootA/myrepo/src/" {
		t.Errorf("expected [rootA/myrepo/src/], got %v", fp.matches)
	}

	// Backspace past "rootA/myrepo/" to "rootA/" should switch to short repo
	fp.SetPrefix("rootA/")
	if fp.currentRepo != "rootA" {
		t.Errorf("after backspace expected currentRepo='rootA', got %q", fp.currentRepo)
	}
}

func TestFilePickerOverlappingRepoNamesIncremental(t *testing.T) {
	// Regression: simulate keystroke-by-keystroke input where the user types
	// "rootA/" first (which locks currentRepo to "rootA"), then continues
	// typing "myrepo/" so the prefix becomes "rootA/myrepo/". The picker must
	// re-resolve currentRepo to the longer "rootA/myrepo" repo.
	rootADir := t.TempDir()
	myrepoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(myrepoDir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}

	repos := map[string]string{
		"rootA":        rootADir,
		"rootA/myrepo": myrepoDir,
	}

	fp := NewFilePickerModel(repos)
	fp.Activate("")

	// Step 1: type "rootA/" — should lock to "rootA"
	fp.SetPrefix("rootA/")
	if fp.currentRepo != "rootA" {
		t.Fatalf("after 'rootA/' expected currentRepo='rootA', got %q", fp.currentRepo)
	}

	// Step 2: continue typing "m", "my", "myr", ... "myrepo/"
	// Simulate incremental keystrokes
	incremental := []string{
		"rootA/m",
		"rootA/my",
		"rootA/myr",
		"rootA/myre",
		"rootA/myrep",
		"rootA/myrepo",
		"rootA/myrepo/",
	}
	for _, prefix := range incremental {
		fp.SetPrefix(prefix)
	}

	// After typing "rootA/myrepo/", currentRepo must be the longer match
	if fp.currentRepo != "rootA/myrepo" {
		t.Errorf("after incremental 'rootA/myrepo/' expected currentRepo='rootA/myrepo', got %q", fp.currentRepo)
	}
	// Should list filesystem contents of myrepoDir
	if len(fp.matches) != 1 || fp.matches[0] != "rootA/myrepo/src/" {
		t.Errorf("expected [rootA/myrepo/src/], got %v", fp.matches)
	}

	// Step 3: backspace to "rootA/" — should re-resolve to shorter repo
	fp.SetPrefix("rootA/")
	if fp.currentRepo != "rootA" {
		t.Errorf("after backspace to 'rootA/' expected currentRepo='rootA', got %q", fp.currentRepo)
	}
}

func TestFilePickerUpdateRepoRoots(t *testing.T) {
	alphaDir := t.TempDir()
	bravoDir := t.TempDir()

	fp := NewFilePickerModel(map[string]string{
		"alpha": alphaDir,
		"bravo": bravoDir,
	})
	fp.Activate("")

	if len(fp.matches) != 2 {
		t.Fatalf("expected 2 repos, got %d: %v", len(fp.matches), fp.matches)
	}

	fp.Deactivate()

	// Update with 3 repos
	charlieDir := t.TempDir()
	fp.UpdateRepoRoots(map[string]string{
		"alpha":   alphaDir,
		"bravo":   bravoDir,
		"charlie": charlieDir,
	})

	fp.Activate("")
	if len(fp.matches) != 3 {
		t.Errorf("expected 3 repos after update, got %d: %v", len(fp.matches), fp.matches)
	}
	// Verify sorted order
	if fp.matches[0] != "alpha/" || fp.matches[1] != "bravo/" || fp.matches[2] != "charlie/" {
		t.Errorf("expected sorted [alpha/ bravo/ charlie/], got %v", fp.matches)
	}
}
