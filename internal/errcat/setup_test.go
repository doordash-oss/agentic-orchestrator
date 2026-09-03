// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package errcat

import (
	"sort"
	"testing"
)

// setupFailureCodes are the three setup failure codes. Each is blocking,
// references the setup action, and declares exactly the repositories,
// command, and setup_task blocks.
var setupCodes = []Code{
	WorktreeSetupFailed,
	SetupAssetCopyFailed,
	SetupInterrupted,
}

func TestSetupFailureCodesContract(t *testing.T) {
	wantBlocks := []Block{BlockRepositories, BlockCommand, BlockSetupTask}
	for _, code := range setupCodes {
		entry, ok := Lookup(code)
		if !ok {
			t.Fatalf("%s: missing from catalog", code)
		}
		if entry.Class != ClassBlocking {
			t.Errorf("%s: class is %q; want blocking", code, entry.Class)
		}
		if len(entry.Actions) != 1 || entry.Actions[0] != "setup" {
			t.Errorf("%s: actions = %#v; want [setup]", code, entry.Actions)
		}
		got := append([]Block(nil), entry.Blocks...)
		sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
		sortedWant := append([]Block(nil), wantBlocks...)
		sort.Slice(sortedWant, func(i, j int) bool { return sortedWant[i] < sortedWant[j] })
		if len(got) != len(sortedWant) {
			t.Fatalf("%s: blocks = %#v; want exactly %#v", code, entry.Blocks, wantBlocks)
		}
		for i := range got {
			if got[i] != sortedWant[i] {
				t.Fatalf("%s: blocks = %#v; want exactly %#v", code, entry.Blocks, wantBlocks)
			}
		}
	}
}

func TestIsSetupFailureReturnsTrueForExactlyTheSetupCodes(t *testing.T) {
	want := map[Code]bool{
		WorktreeSetupFailed:  true,
		SetupAssetCopyFailed: true,
		SetupInterrupted:     true,
	}
	for _, code := range Codes() {
		if got := IsSetupFailure(code); got != want[code] {
			t.Errorf("IsSetupFailure(%s) = %v; want %v", code, got, want[code])
		}
	}
}

func TestSetupFailureSummariesNameTheOwningTask(t *testing.T) {
	params := SetupFailureParams{TaskLabel: "Worktree: beta"}
	want := `Setup task "Worktree: beta" failed.`
	for _, code := range setupCodes {
		rendered := New(code, WithParams(params))
		if rendered.Summary != want {
			t.Errorf("%s: task-label summary = %q; want %q", code, rendered.Summary, want)
		}
	}

	// Without a task label the worktree code names the repository and the
	// other two fall back to their static summaries.
	rendered := New(WorktreeSetupFailed, WithParams(SetupFailureParams{Repositories: []string{"beta"}}))
	if rendered.Summary != `Setting up the worktree for repository "beta" failed.` {
		t.Fatalf("worktree repo summary = %q", rendered.Summary)
	}
	for _, code := range []Code{SetupAssetCopyFailed, SetupInterrupted} {
		entry, ok := Lookup(code)
		if !ok {
			t.Fatalf("%s: missing from catalog", code)
		}
		rendered := New(code, WithParams(SetupFailureParams{Repositories: []string{"beta"}}))
		if rendered.Summary != entry.Summary {
			t.Errorf("%s: no-label summary = %q; want static %q", code, rendered.Summary, entry.Summary)
		}
	}
}

func TestNewCarriesAndDropsSetupTaskBlock(t *testing.T) {
	task := CodeSetupTask{Key: "worktree:beta", Kind: "worktree", Label: "Worktree: beta"}
	rendered := New(WorktreeSetupFailed, WithSetupTask(task))
	if rendered.Context == nil || rendered.Context.SetupTask == nil || rendered.Context.SetupTask.Key != "worktree:beta" {
		t.Fatalf("worktree_setup_failed declares the setup_task block; got %#v", rendered.Context)
	}

	rendered = New(IterationBudgetExhausted, WithSetupTask(task))
	if rendered.Context != nil && rendered.Context.SetupTask != nil {
		t.Fatalf("iteration_budget_exhausted does not declare the setup_task block; got %#v", rendered.Context)
	}
}
