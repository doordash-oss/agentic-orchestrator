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

package skilldef

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const expectedEmbeddedSkillCount = 28

func TestParseSkillFile(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		dirName   string
		want      SkillDef
		wantError bool
	}{
		{
			name:    "full frontmatter",
			content: "---\nname: my-skill\ndescription: A test skill\nlicense: MIT\n---\nThis is the body.",
			dirName: "fallback",
			want:    SkillDef{Name: "my-skill", Description: "A test skill", License: "MIT", Body: "This is the body."},
		},
		{
			name:    "name fallback to dirName",
			content: "---\ndescription: A skill without a name\n---\nBody text.",
			dirName: "my-skill",
			want:    SkillDef{Name: "my-skill", Description: "A skill without a name", License: "", Body: "Body text."},
		},
		{
			name:      "no frontmatter",
			content:   "Just plain text without delimiters",
			dirName:   "whatever",
			wantError: true,
		},
		{
			name:      "unterminated frontmatter",
			content:   "---\nname: broken\ndescription: no closing delimiter",
			dirName:   "broken",
			wantError: true,
		},
		{
			name:      "empty description",
			content:   "---\nname: no-desc\n---\nBody here.",
			dirName:   "no-desc",
			wantError: true,
		},
		{
			name:    "extra fields ignored",
			content: "---\nname: extra\ndescription: Has extras\ncolor: blue\npriority: high\n---\nBody.",
			dirName: "extra",
			want:    SkillDef{Name: "extra", Description: "Has extras", License: "", Body: "Body."},
		},
		{
			name:    "body only after closing delimiter",
			content: "---\nname: bodied\ndescription: Has body\n---\n\nMulti-line\nbody content\nhere.",
			dirName: "bodied",
			want:    SkillDef{Name: "bodied", Description: "Has body", License: "", Body: "Multi-line\nbody content\nhere."},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSkillFile(tt.content, tt.dirName)
			if tt.wantError {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Name != tt.want.Name {
				t.Errorf("Name = %q, want %q", got.Name, tt.want.Name)
			}
			if got.Description != tt.want.Description {
				t.Errorf("Description = %q, want %q", got.Description, tt.want.Description)
			}
			if got.License != tt.want.License {
				t.Errorf("License = %q, want %q", got.License, tt.want.License)
			}
			if got.Body != tt.want.Body {
				t.Errorf("Body = %q, want %q", got.Body, tt.want.Body)
			}
		})
	}
}

func TestParseEmbedded(t *testing.T) {
	skills, err := ParseEmbedded()
	if err != nil {
		t.Fatalf("ParseEmbedded() error: %v", err)
	}
	if len(skills) != expectedEmbeddedSkillCount {
		t.Fatalf("ParseEmbedded() returned %d skills, want %d", len(skills), expectedEmbeddedSkillCount)
	}

	fd, ok := skills["frontend-design"]
	if !ok {
		t.Fatal("missing expected skill: frontend-design")
	}
	if fd.Name != "frontend-design" {
		t.Errorf("frontend-design Name = %q, want %q", fd.Name, "frontend-design")
	}
	if fd.Description == "" {
		t.Error("frontend-design has empty Description")
	}
	if fd.Body == "" {
		t.Error("frontend-design has empty Body")
	}

	// All entries must have non-empty Description and Body
	for name, def := range skills {
		if def.Description == "" {
			t.Errorf("skill %q has empty Description", name)
		}
		if def.Body == "" {
			t.Errorf("skill %q has empty Body", name)
		}
	}
}

func TestParseEmbedded_Cached(t *testing.T) {
	a, err1 := ParseEmbedded()
	b, err2 := ParseEmbedded()
	if err1 != nil {
		t.Fatalf("first call error: %v", err1)
	}
	if err2 != nil {
		t.Fatalf("second call error: %v", err2)
	}
	if len(a) != len(b) {
		t.Fatalf("different lengths: %d vs %d", len(a), len(b))
	}
	for name, defA := range a {
		defB, ok := b[name]
		if !ok {
			t.Errorf("second call missing skill %q", name)
			continue
		}
		if defA != defB {
			t.Errorf("skill %q differs between calls", name)
		}
	}
}

func TestBuildPreamble(t *testing.T) {
	skillsDir := "/test/path/to/skills"
	preamble := BuildPreamble(skillsDir, []string{"frontend-design", "implement", "research-codebase"})

	for _, want := range []string{
		"## Available Skills",
		"frontend-design",
		"/test/path/to/skills/frontend-design/SKILL.md",
		"To use a skill",
		"implement",
		"research-codebase",
	} {
		if !strings.Contains(preamble, want) {
			t.Errorf("preamble missing %q", want)
		}
	}

	// Check that the description from the embedded skill is present
	skills, _ := ParseEmbedded()
	fd := skills["frontend-design"]
	if !strings.Contains(preamble, fd.Description) {
		t.Errorf("preamble missing frontend-design description %q", fd.Description)
	}
}

func TestBuildPreamble_DifferentPaths(t *testing.T) {
	names := []string{"frontend-design"}
	a := BuildPreamble("/path/alpha/skills", names)
	b := BuildPreamble("/path/beta/skills", names)
	if a == b {
		t.Error("expected different preambles for different skillsDir values")
	}
	if !strings.Contains(a, "/path/alpha/skills") {
		t.Error("preamble A missing alpha path")
	}
	if !strings.Contains(b, "/path/beta/skills") {
		t.Error("preamble B missing beta path")
	}
}

func TestBuildPreamble_EmptyNames(t *testing.T) {
	preamble := BuildPreamble("/any/path", nil)
	if preamble != "" {
		t.Error("BuildPreamble returned non-empty string for nil skillNames")
	}
	preamble = BuildPreamble("/any/path", []string{})
	if preamble != "" {
		t.Error("BuildPreamble returned non-empty string for empty skillNames")
	}
}

func TestBuildPreamble_FiltersToRequestedSkills(t *testing.T) {
	preamble := BuildPreamble("/any/path", []string{"frontend-design"})
	if !strings.Contains(preamble, "frontend-design") {
		t.Error("preamble missing requested skill frontend-design")
	}
	if strings.Contains(preamble, "| implement |") {
		t.Error("preamble should not contain unrequested skill implement")
	}
}

func TestReadBody(t *testing.T) {
	body, err := ReadBody("frontend-design")
	if err != nil {
		t.Fatalf("ReadBody(frontend-design) error: %v", err)
	}
	if body == "" {
		t.Error("ReadBody(frontend-design) returned empty body")
	}

	// Verify it matches ParseEmbedded
	defs, _ := ParseEmbedded()
	if body != defs["frontend-design"].Body {
		t.Error("ReadBody body does not match ParseEmbedded body")
	}
}

func TestReadBody_AllCommandSkills(t *testing.T) {
	commandSkills := []string{
		"design", "build-knowledge-base", "chat", "create-roadmap", "design",
		"final-fix", "implement", "inquire", "plan-phase", "research-codebase",
		"revise-phase-plan", "revise-roadmap",
		"validate-phase-plan-grounding", "validate-phase-plan-scope",
		"validate-phase-plan-structural",
		"validate-plan-performance",
		"validate-plan-security", "validate-plan-testing",
		"validate-roadmap-architecture", "validate-roadmap-scope",
	}
	for _, name := range commandSkills {
		t.Run(name, func(t *testing.T) {
			body, err := ReadBody(name)
			if err != nil {
				t.Fatalf("ReadBody(%s) error: %v", name, err)
			}
			if body == "" {
				t.Errorf("ReadBody(%s) returned empty body", name)
			}
		})
	}
}

func TestReadBody_NotFound(t *testing.T) {
	_, err := ReadBody("nonexistent_skill_xyz")
	if err == nil {
		t.Fatal("expected error for nonexistent skill, got nil")
	}
}

func TestReconcileSkills(t *testing.T) {
	skillsDir := filepath.Join(t.TempDir(), "skills")

	// First call succeeds
	if err := ReconcileSkills(skillsDir); err != nil {
		t.Fatalf("first ReconcileSkills() error: %v", err)
	}

	// Verify all skill directories exist
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		t.Fatalf("reading skills dir: %v", err)
	}
	if len(entries) != expectedEmbeddedSkillCount {
		t.Fatalf("expected %d skill directories, got %d", expectedEmbeddedSkillCount, len(entries))
	}

	// frontend-design/SKILL.md exists with correct content
	skillPath := filepath.Join(skillsDir, "frontend-design", "SKILL.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("reading reconciled file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "name: frontend-design") {
		t.Error("reconciled file missing 'name: frontend-design'")
	}
	if !strings.Contains(content, "description:") {
		t.Error("reconciled file missing 'description:'")
	}

	// Idempotent on second call (modtime unchanged)
	info, err := os.Stat(skillPath)
	if err != nil {
		t.Fatalf("stat reconciled file: %v", err)
	}
	before := info.ModTime()
	// Retained: creates a visible filesystem mtime boundary for idempotence.
	time.Sleep(20 * time.Millisecond)
	if err := ReconcileSkills(skillsDir); err != nil {
		t.Fatalf("second ReconcileSkills() error: %v", err)
	}
	info, err = os.Stat(skillPath)
	if err != nil {
		t.Fatalf("stat after second reconcile: %v", err)
	}
	after := info.ModTime()
	if !before.Equal(after) {
		t.Error("modtime changed on idempotent reconcile")
	}

	// No leftover temp files
	matches, _ := filepath.Glob(filepath.Join(skillsDir, "*", ".tmp.*"))
	if len(matches) > 0 {
		t.Errorf("leftover temp files: %v", matches)
	}

	// Verify user guide files are reconciled alongside SKILL.md
	indexPath := filepath.Join(skillsDir, "chat", "user-guide", "index.md")
	indexData, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("reading chat/user-guide/index.md: %v", err)
	}
	if !strings.Contains(string(indexData), "Agentic Orchestrator User Guide") {
		t.Error("chat/user-guide/index.md missing expected content")
	}

	gettingStartedPath := filepath.Join(skillsDir, "chat", "user-guide", "getting-started.md")
	gsData, err := os.ReadFile(gettingStartedPath)
	if err != nil {
		t.Fatalf("reading chat/user-guide/getting-started.md: %v", err)
	}
	if !strings.Contains(string(gsData), "Getting Started with Agentic") {
		t.Error("chat/user-guide/getting-started.md missing expected content")
	}
	if strings.Contains(string(gsData), "STUB(Phase 2)") {
		t.Error("chat/user-guide/getting-started.md still contains STUB(Phase 2)")
	}

	// Verify new Phase 2 topic files are reconciled
	lifecyclePath := filepath.Join(skillsDir, "chat", "user-guide", "feature-lifecycle.md")
	lcData, err := os.ReadFile(lifecyclePath)
	if err != nil {
		t.Fatalf("reading chat/user-guide/feature-lifecycle.md: %v", err)
	}
	if !strings.Contains(string(lcData), "Pipeline Profiles") {
		t.Error("chat/user-guide/feature-lifecycle.md missing expected content")
	}

	tuiNavPath := filepath.Join(skillsDir, "chat", "user-guide", "tui-navigation.md")
	tnData, err := os.ReadFile(tuiNavPath)
	if err != nil {
		t.Fatalf("reading chat/user-guide/tui-navigation.md: %v", err)
	}
	if !strings.Contains(string(tnData), "Dashboard") {
		t.Error("chat/user-guide/tui-navigation.md missing expected content")
	}

	configPath := filepath.Join(skillsDir, "chat", "user-guide", "configuration.md")
	cfgData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading chat/user-guide/configuration.md: %v", err)
	}
	if !strings.Contains(string(cfgData), "config.yaml") {
		t.Error("chat/user-guide/configuration.md missing expected content")
	}

	postPubPath := filepath.Join(skillsDir, "chat", "user-guide", "post-publish.md")
	ppData, err := os.ReadFile(postPubPath)
	if err != nil {
		t.Fatalf("reading chat/user-guide/post-publish.md: %v", err)
	}
	if !strings.Contains(string(ppData), "Tweak") {
		t.Error("chat/user-guide/post-publish.md missing expected content")
	}

	permsPath := filepath.Join(skillsDir, "chat", "user-guide", "permissions.md")
	pmData, err := os.ReadFile(permsPath)
	if err != nil {
		t.Fatalf("reading chat/user-guide/permissions.md: %v", err)
	}
	if !strings.Contains(string(pmData), "Allow & Remember") {
		t.Error("chat/user-guide/permissions.md missing expected content")
	}
}

func TestReconcileSkills_CreatesSubdirectories(t *testing.T) {
	skillsDir := filepath.Join(t.TempDir(), "deep", "nested", "skills")
	if err := ReconcileSkills(skillsDir); err != nil {
		t.Fatalf("ReconcileSkills() error: %v", err)
	}
	skillPath := filepath.Join(skillsDir, "frontend-design", "SKILL.md")
	if _, err := os.Stat(skillPath); err != nil {
		t.Errorf("expected file at %s: %v", skillPath, err)
	}
}

func TestReconcileSkills_WritesSubdirectoryFiles(t *testing.T) {
	skillsDir := filepath.Join(t.TempDir(), "skills")

	if err := ReconcileSkills(skillsDir); err != nil {
		t.Fatalf("ReconcileSkills() error: %v", err)
	}

	// Verify user-guide index exists with expected content
	indexPath := filepath.Join(skillsDir, "chat", "user-guide", "index.md")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("reading %s: %v", indexPath, err)
	}
	if len(data) == 0 {
		t.Fatal("user-guide/index.md is empty")
	}
	if !strings.Contains(string(data), "Getting Started") {
		t.Error("user-guide/index.md missing 'Getting Started'")
	}

	// Verify getting-started.md exists with expected content
	gsPath := filepath.Join(skillsDir, "chat", "user-guide", "getting-started.md")
	data, err = os.ReadFile(gsPath)
	if err != nil {
		t.Fatalf("reading %s: %v", gsPath, err)
	}
	if len(data) == 0 {
		t.Fatal("user-guide/getting-started.md is empty")
	}
	if !strings.Contains(string(data), "Getting Started with Agentic") {
		t.Error("user-guide/getting-started.md missing expected heading")
	}

	// Verify SKILL.md still exists alongside subdirectory files
	skillMDPath := filepath.Join(skillsDir, "chat", "SKILL.md")
	if _, err := os.Stat(skillMDPath); err != nil {
		t.Errorf("chat/SKILL.md should still exist: %v", err)
	}

	// Verify total skill directory count is unchanged
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		t.Fatalf("reading skills dir: %v", err)
	}
	if len(entries) != expectedEmbeddedSkillCount {
		t.Errorf("expected %d skill directories, got %d", expectedEmbeddedSkillCount, len(entries))
	}
}

func TestReconcileSkills_SubdirectoryIdempotent(t *testing.T) {
	skillsDir := filepath.Join(t.TempDir(), "skills")

	if err := ReconcileSkills(skillsDir); err != nil {
		t.Fatalf("first ReconcileSkills() error: %v", err)
	}

	indexPath := filepath.Join(skillsDir, "chat", "user-guide", "index.md")
	info, err := os.Stat(indexPath)
	if err != nil {
		t.Fatalf("stat %s: %v", indexPath, err)
	}
	before := info.ModTime()

	// Retained: creates a visible filesystem mtime boundary for idempotence.
	time.Sleep(20 * time.Millisecond)

	if err := ReconcileSkills(skillsDir); err != nil {
		t.Fatalf("second ReconcileSkills() error: %v", err)
	}

	info, err = os.Stat(indexPath)
	if err != nil {
		t.Fatalf("stat after second reconcile: %v", err)
	}
	after := info.ModTime()

	if !before.Equal(after) {
		t.Error("subdirectory file modtime changed on idempotent reconcile")
	}

	// No leftover temp files in subdirectories
	matches, _ := filepath.Glob(filepath.Join(skillsDir, "chat", "user-guide", ".tmp.*"))
	if len(matches) > 0 {
		t.Errorf("leftover temp files in user-guide: %v", matches)
	}
}

func TestReconcileSkills_PartialFailureThenRetry(t *testing.T) {
	skillsDir := filepath.Join(t.TempDir(), "skills")
	os.MkdirAll(filepath.Join(skillsDir, "frontend-design"), 0o755)
	// Block SKILL.md path with a directory
	blockedPath := filepath.Join(skillsDir, "frontend-design", "SKILL.md")
	os.Mkdir(blockedPath, 0o755)

	err := ReconcileSkills(skillsDir)
	if err == nil {
		t.Fatal("ReconcileSkills() should fail with blocked path")
	}

	// Unblock and retry
	os.RemoveAll(blockedPath)
	if err := ReconcileSkills(skillsDir); err != nil {
		t.Fatalf("retry ReconcileSkills() error: %v", err)
	}

	// Assert file now has correct content
	data, err := os.ReadFile(filepath.Join(skillsDir, "frontend-design", "SKILL.md"))
	if err != nil {
		t.Fatalf("reading retried file: %v", err)
	}
	if !strings.Contains(string(data), "frontend-design") {
		t.Error("retried file missing expected content")
	}
}

func TestReconcileSkills_NonSkillMdFiles(t *testing.T) {
	skillsDir := filepath.Join(t.TempDir(), "skills")

	if err := ReconcileSkills(skillsDir); err != nil {
		t.Fatalf("ReconcileSkills() error: %v", err)
	}

	tests := []struct {
		name    string
		path    string
		wantSub string
	}{
		{
			name:    "user guide index",
			path:    filepath.Join(skillsDir, "chat", "user-guide", "index.md"),
			wantSub: "Agentic Orchestrator User Guide",
		},
		{
			name:    "getting started topic",
			path:    filepath.Join(skillsDir, "chat", "user-guide", "getting-started.md"),
			wantSub: "Getting Started with Agentic Orchestrator",
		},
		{
			name:    "feature lifecycle topic",
			path:    filepath.Join(skillsDir, "chat", "user-guide", "feature-lifecycle.md"),
			wantSub: "Pipeline Profiles",
		},
		{
			name:    "tui navigation topic",
			path:    filepath.Join(skillsDir, "chat", "user-guide", "tui-navigation.md"),
			wantSub: "Dashboard",
		},
		{
			name:    "configuration topic",
			path:    filepath.Join(skillsDir, "chat", "user-guide", "configuration.md"),
			wantSub: "config.yaml",
		},
		{
			name:    "post-publish topic",
			path:    filepath.Join(skillsDir, "chat", "user-guide", "post-publish.md"),
			wantSub: "Tweak",
		},
		{
			name:    "permissions topic",
			path:    filepath.Join(skillsDir, "chat", "user-guide", "permissions.md"),
			wantSub: "Allow & Remember",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := os.ReadFile(tt.path)
			if err != nil {
				t.Fatalf("reading %s: %v", tt.path, err)
			}
			if !strings.Contains(string(data), tt.wantSub) {
				t.Errorf("file %s missing expected content %q", tt.path, tt.wantSub)
			}
		})
	}

	// Idempotent: second call doesn't change modtimes
	info1, err := os.Stat(tests[0].path)
	if err != nil {
		t.Fatalf("stat %s: %v", tests[0].path, err)
	}
	before := info1.ModTime()
	// Retained: creates a visible filesystem mtime boundary for idempotence.
	time.Sleep(20 * time.Millisecond)
	if err := ReconcileSkills(skillsDir); err != nil {
		t.Fatalf("second ReconcileSkills() error: %v", err)
	}
	info2, err := os.Stat(tests[0].path)
	if err != nil {
		t.Fatalf("stat after second reconcile: %v", err)
	}
	if !before.Equal(info2.ModTime()) {
		t.Error("modtime changed on idempotent reconcile for non-SKILL.md file")
	}

	// No leftover temp files in subdirectories
	matches, _ := filepath.Glob(filepath.Join(skillsDir, "chat", "user-guide", ".tmp.*"))
	if len(matches) > 0 {
		t.Errorf("leftover temp files in user-guide: %v", matches)
	}
}

func TestReconcileSkills_SubdirectoryStructure(t *testing.T) {
	skillsDir := filepath.Join(t.TempDir(), "skills")

	if err := ReconcileSkills(skillsDir); err != nil {
		t.Fatalf("ReconcileSkills() error: %v", err)
	}

	// SKILL.md still exists and is correct
	skillMd := filepath.Join(skillsDir, "chat", "SKILL.md")
	data, err := os.ReadFile(skillMd)
	if err != nil {
		t.Fatalf("reading chat/SKILL.md: %v", err)
	}
	if !strings.Contains(string(data), "Agentic Orchestrator Expert Assistant") {
		t.Error("chat/SKILL.md missing expected content")
	}

	// user-guide directory is created
	info, err := os.Stat(filepath.Join(skillsDir, "chat", "user-guide"))
	if err != nil {
		t.Fatalf("stat chat/user-guide: %v", err)
	}
	if !info.IsDir() {
		t.Error("chat/user-guide is not a directory")
	}

	// Files inside nested directory have correct content
	indexData, err := os.ReadFile(filepath.Join(skillsDir, "chat", "user-guide", "index.md"))
	if err != nil {
		t.Fatalf("reading user-guide/index.md: %v", err)
	}
	if !strings.Contains(string(indexData), "Getting Started") {
		t.Error("user-guide/index.md missing topic reference")
	}
	if !strings.Contains(string(indexData), "getting-started.md") {
		t.Error("user-guide/index.md missing file link")
	}

	// Phase 2 and Phase 3 topic files are reachable through the reconciled directory
	for _, tc := range []struct {
		file    string
		wantSub string
	}{
		{"feature-lifecycle.md", "Pipeline Profiles"},
		{"tui-navigation.md", "Dashboard"},
		{"configuration.md", "config.yaml"},
		{"post-publish.md", "Tweak"},
		{"permissions.md", "Allow & Remember"},
	} {
		data, err := os.ReadFile(filepath.Join(skillsDir, "chat", "user-guide", tc.file))
		if err != nil {
			t.Fatalf("reading user-guide/%s: %v", tc.file, err)
		}
		if !strings.Contains(string(data), tc.wantSub) {
			t.Errorf("user-guide/%s missing expected content %q", tc.file, tc.wantSub)
		}
	}
}

// TestReconcileSkills_DoesNotWriteMultiRepo guards against the multi-repo
// user-guide topic being reintroduced. The unified per-repo lifecycle is the
// default voice; topic-level multi/single splits are gone.
func TestReconcileSkills_DoesNotWriteMultiRepo(t *testing.T) {
	skillsDir := filepath.Join(t.TempDir(), "skills")
	if err := ReconcileSkills(skillsDir); err != nil {
		t.Fatalf("ReconcileSkills() error: %v", err)
	}
	multiRepoPath := filepath.Join(skillsDir, "chat", "user-guide", "multi-repo.md")
	if _, err := os.Stat(multiRepoPath); err == nil {
		t.Errorf("user-guide/multi-repo.md should not be written by ReconcileSkills (path exists)")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat user-guide/multi-repo.md: %v", err)
	}
}
