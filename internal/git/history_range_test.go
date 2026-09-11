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

package git

import (
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestCommitBodiesRange_ReturnsExactlyCommitsBetweenLowerAndTip(t *testing.T) {
	repo, _ := testutil.InitPublishReadyGitRepo(t)
	lower := testutil.CommitFile(t, repo, "base.txt", "base\n", "base cut point")
	testutil.CommitFile(t, repo, "mid.txt", "mid\n", "middle commit")
	tip := testutil.CommitFile(t, repo, "tip.txt", "tip\n", "tip commit")

	bodies, err := CommitBodiesRange(repo, lower, tip)
	if err != nil {
		t.Fatalf("CommitBodiesRange() error = %v", err)
	}
	if !strings.Contains(bodies, "middle commit") || !strings.Contains(bodies, "tip commit") {
		t.Fatalf("CommitBodiesRange() = %q; want the middle and tip commits", bodies)
	}
	if strings.Contains(bodies, "base cut point") {
		t.Fatalf("CommitBodiesRange() = %q; want the lower bound excluded", bodies)
	}

	empty, err := CommitBodiesRange(repo, tip, tip)
	if err != nil {
		t.Fatalf("CommitBodiesRange(tip, tip) error = %v", err)
	}
	if strings.TrimSpace(empty) != "" {
		t.Fatalf("CommitBodiesRange(tip, tip) = %q; want no commits", empty)
	}
}

func TestDiffStatRange_ReturnsExactlyStatBetweenLowerAndTip(t *testing.T) {
	repo, _ := testutil.InitPublishReadyGitRepo(t)
	lower := testutil.CommitFile(t, repo, "base.txt", "base\n", "base cut point")
	testutil.CommitFile(t, repo, "mid.txt", "mid\n", "middle commit")
	tip := testutil.CommitFile(t, repo, "tip.txt", "tip\n", "tip commit")

	stat, err := DiffStatRange(repo, lower, tip)
	if err != nil {
		t.Fatalf("DiffStatRange() error = %v", err)
	}
	if !strings.Contains(stat, "mid.txt") || !strings.Contains(stat, "tip.txt") {
		t.Fatalf("DiffStatRange() = %q; want the middle and tip files", stat)
	}
	if strings.Contains(stat, "base.txt") {
		t.Fatalf("DiffStatRange() = %q; want the lower bound's file excluded", stat)
	}

	empty, err := DiffStatRange(repo, tip, tip)
	if err != nil {
		t.Fatalf("DiffStatRange(tip, tip) error = %v", err)
	}
	if strings.TrimSpace(empty) != "" {
		t.Fatalf("DiffStatRange(tip, tip) = %q; want an empty stat", empty)
	}
}

func TestHasCommitsBeyond(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	cut := testutil.CommitFile(t, repo, "cut.txt", "cut\n", "cut point")
	tip := testutil.CommitFile(t, repo, "tip.txt", "tip\n", "tip beyond cut")
	ahead := testutil.CommitFile(t, repo, "ahead.txt", "ahead\n", "cut point moved ahead")

	tests := []struct {
		name string
		tip  string
		cut  string
		want bool
	}{
		{"tip beyond cut", tip, cut, true},
		{"tip equals cut", cut, cut, false},
		{"tip is ancestor of cut", cut, ahead, false},
		{"empty tip", "", cut, false},
		{"empty cut point", tip, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasCommitsBeyond(repo, tt.tip, tt.cut); got != tt.want {
				t.Fatalf("HasCommitsBeyond(tip=%q, cut=%q) = %v; want %v", tt.tip, tt.cut, got, tt.want)
			}
		})
	}
}
