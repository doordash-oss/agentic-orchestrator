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

package agent_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
)

func TestPruneOrphanedSystemPrompts(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	old := now.Add(-time.Hour)
	fresh := now.Add(-time.Minute)

	type entry struct {
		name  string
		dir   bool
		mtime time.Time
	}
	tests := []struct {
		name        string
		entries     []entry
		referenced  []string
		argsErr     error
		wantRemoved int
		wantErr     error
		wantKept    []string
		wantGone    []string
	}{
		{
			name:        "old orphan is removed",
			entries:     []entry{{name: "agentico-system-prompt-1", dir: true, mtime: old}},
			wantRemoved: 1,
			wantGone:    []string{"agentico-system-prompt-1"},
		},
		{
			name:       "dir referenced by a live process is kept",
			entries:    []entry{{name: "agentico-system-prompt-2", dir: true, mtime: old}},
			referenced: []string{"agentico-system-prompt-2"},
			wantKept:   []string{"agentico-system-prompt-2"},
		},
		{
			name:     "fresh dir within the grace period is kept",
			entries:  []entry{{name: "agentico-system-prompt-3", dir: true, mtime: fresh}},
			wantKept: []string{"agentico-system-prompt-3"},
		},
		{
			name: "non-matching entries are left alone",
			entries: []entry{
				{name: "other-dir", dir: true, mtime: old},
				{name: "agentico-system-prompt-file", mtime: old},
			},
			wantKept: []string{"other-dir", "agentico-system-prompt-file"},
		},
		{
			name:     "unreadable process list prunes nothing",
			entries:  []entry{{name: "agentico-system-prompt-4", dir: true, mtime: old}},
			argsErr:  errors.New("denied"),
			wantErr:  agent.ErrProcessListUnavailable,
			wantKept: []string{"agentico-system-prompt-4"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			featureStateDir := filepath.Join(t.TempDir(), "features")
			base := filepath.Join(filepath.Dir(featureStateDir), "provider-state", "system-prompts")
			if err := os.MkdirAll(base, 0o700); err != nil {
				t.Fatal(err)
			}
			for _, e := range tt.entries {
				p := filepath.Join(base, e.name)
				if e.dir {
					if err := os.Mkdir(p, 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(p, "system-prompt.md"), []byte("x"), 0o600); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(p, e.mtime, e.mtime); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"/usr/bin/unrelated --flag"}
			for _, name := range tt.referenced {
				args = append(args, "claude --append-system-prompt-file "+filepath.Join(base, name, "system-prompt.md"))
			}

			removed, err := agent.PruneOrphanedSystemPrompts(featureStateDir, agent.SystemPromptPruneOpts{
				ProcessArgs: func() ([]string, error) { return args, tt.argsErr },
				Now:         func() time.Time { return now },
			})

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if removed != tt.wantRemoved {
				t.Errorf("removed = %d, want %d", removed, tt.wantRemoved)
			}
			for _, name := range tt.wantKept {
				if _, err := os.Stat(filepath.Join(base, name)); err != nil {
					t.Errorf("%s should be kept: %v", name, err)
				}
			}
			for _, name := range tt.wantGone {
				if _, err := os.Stat(filepath.Join(base, name)); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("%s should be removed, stat err = %v", name, err)
				}
			}
		})
	}
}

func TestPruneOrphanedSystemPromptsReadsRealProcessList(t *testing.T) {
	featureStateDir := filepath.Join(t.TempDir(), "features")
	dir := filepath.Join(filepath.Dir(featureStateDir), "provider-state", "system-prompts", "agentico-system-prompt-9")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}

	removed, err := agent.PruneOrphanedSystemPrompts(featureStateDir, agent.SystemPromptPruneOpts{})

	if err != nil {
		t.Fatalf("real process list should be readable: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
}
