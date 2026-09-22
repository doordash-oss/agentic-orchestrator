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
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// The helper lists a range's commits oldest-first with each commit's
// Stack-Layer trailer value, and empty values for commits without one.
func TestCommitsBetweenWithStackLayer_ReturnsOldestFirstWithTrailerValues(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	base := testutil.CommitFile(t, repo, "base.txt", "base\n", "base cut point")
	first := testutil.CommitFile(t, repo, "a.txt", "a\n",
		"first fix\n\nFeature: rf-child\n\nStack-Layer: 2\n\nCo-authored-by: Agentico <noreply@doordash-oss.github.com>")
	second := testutil.CommitFile(t, repo, "b.txt", "b\n",
		"second fix without a trailer\n\nFeature: rf-child\n\nCo-authored-by: Agentico <noreply@doordash-oss.github.com>")
	third := testutil.CommitFile(t, repo, "c.txt", "c\n", "third fix\n\nStack-Layer: 1")

	got, err := CommitsBetweenWithStackLayer(repo, base, third)
	if err != nil {
		t.Fatalf("CommitsBetweenWithStackLayer() error = %v", err)
	}
	wantSHAs := []string{first, second, third}
	wantLayers := []string{"2", "", "1"}
	if len(got) != len(wantSHAs) {
		t.Fatalf("commits = %d, want %d: %+v", len(got), len(wantSHAs), got)
	}
	for i := range wantSHAs {
		if got[i].SHA != wantSHAs[i] {
			t.Errorf("commit %d SHA = %s, want %s (oldest-first order)", i, got[i].SHA, wantSHAs[i])
		}
		if got[i].Layer != wantLayers[i] {
			t.Errorf("commit %d Stack-Layer = %q, want %q", i, got[i].Layer, wantLayers[i])
		}
	}
}

// The last Stack-Layer line of a message wins, and an empty range returns no
// commits.
func TestCommitsBetweenWithStackLayer_LastTrailerWinsAndEmptyRangeIsEmpty(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	base := testutil.CommitFile(t, repo, "base.txt", "base\n", "base cut point")
	tip := testutil.CommitFile(t, repo, "a.txt", "a\n",
		"fix mentioning Stack-Layer: 9 in the subject\n\nStack-Layer: 3")

	got, err := CommitsBetweenWithStackLayer(repo, base, tip)
	if err != nil {
		t.Fatalf("CommitsBetweenWithStackLayer() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("commits = %d, want 1: %+v", len(got), got)
	}
	if got[0].Layer != "3" {
		t.Fatalf("Stack-Layer = %q, want the last trailer line's value %q", got[0].Layer, "3")
	}

	empty, err := CommitsBetweenWithStackLayer(repo, tip, tip)
	if err != nil {
		t.Fatalf("CommitsBetweenWithStackLayer(tip, tip) error = %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty range commits = %+v, want none", empty)
	}
}
