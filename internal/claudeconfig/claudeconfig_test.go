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

package claudeconfig

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestProjectDirName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{"/Users/alice/Projects/myapp", "-Users-alice-Projects-myapp"},
		{"/Users/bob.smith/code", "-Users-bob-smith-code"},
		{"/home/user/.hidden/dir", "-home-user--hidden-dir"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := ProjectDirName(tt.input); got != tt.want {
			t.Errorf("ProjectDirName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestProjectsDir(t *testing.T) {
	t.Parallel()
	got := ProjectsDir("/cfg", "/Users/a.b/repo")
	want := filepath.Join("/cfg", "projects", "-Users-a-b-repo")
	if got != want {
		t.Errorf("ProjectsDir = %q, want %q", got, want)
	}
}

func TestDir(t *testing.T) {
	t.Parallel()
	home := func() (string, error) { return "/home/u", nil }
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == EnvConfigDir {
				return v
			}
			return ""
		}
	}

	if got, err := Dir(env("/custom/claude"), home); err != nil || got != "/custom/claude" {
		t.Errorf("Dir with CLAUDE_CONFIG_DIR = %q, %v; want /custom/claude", got, err)
	}
	if got, err := Dir(env(""), home); err != nil || got != filepath.Join("/home/u", ".claude") {
		t.Errorf("Dir with empty CLAUDE_CONFIG_DIR = %q, %v; want /home/u/.claude", got, err)
	}
	if got, err := Dir(nil, home); err != nil || got != filepath.Join("/home/u", ".claude") {
		t.Errorf("Dir with nil getenv = %q, %v; want /home/u/.claude", got, err)
	}

	boom := errors.New("no home")
	if _, err := Dir(env(""), func() (string, error) { return "", boom }); !errors.Is(err, boom) {
		t.Errorf("Dir home error = %v, want %v", err, boom)
	}
	if _, err := Dir(env(""), func() (string, error) { return "", nil }); err == nil {
		t.Error("Dir with empty home: want error")
	}
	if _, err := Dir(env(""), nil); err == nil {
		t.Error("Dir with nil home resolver: want error")
	}
}

func TestDefaultDirHonoursEnvironment(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv(EnvConfigDir, cfg)
	if got, err := DefaultDir(); err != nil || got != cfg {
		t.Errorf("DefaultDir = %q, %v; want %q", got, err, cfg)
	}

	home := t.TempDir()
	t.Setenv(EnvConfigDir, "")
	t.Setenv("HOME", home)
	if got, err := DefaultDir(); err != nil || got != filepath.Join(home, ".claude") {
		t.Errorf("DefaultDir = %q, %v; want %q", got, err, filepath.Join(home, ".claude"))
	}
}

func TestProjectsDirResolvesSymlinks(t *testing.T) {
	t.Parallel()
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/cfg", "projects", ProjectDirName(resolved))
	if got := ProjectsDir("/cfg", link); got != want {
		t.Fatalf("ProjectsDir(link) = %q, want %q", got, want)
	}
	if got := ProjectsDir("/cfg", "/does/not/exist"); got != filepath.Join("/cfg", "projects", "-does-not-exist") {
		t.Fatalf("unresolvable dir = %q", got)
	}
}
