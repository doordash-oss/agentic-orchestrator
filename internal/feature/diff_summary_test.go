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

package feature

import (
	"fmt"
	"strings"
	"testing"
)

func TestBoundDiffSummarySmallInputUnchanged(t *testing.T) {
	t.Parallel()
	in := "Repository: repo\ndiff --git a/auth.go b/auth.go\n+session rotation\n"
	if got := BoundDiffSummary(in); got != in {
		t.Fatalf("BoundDiffSummary(small) = %q, want unchanged input", got)
	}
	if got := BoundDiffSummary(""); got != "" {
		t.Fatalf("BoundDiffSummary(empty) = %q, want empty", got)
	}
}

func TestBoundDiffSummaryTruncatesOnLineBoundary(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	for i := 0; sb.Len() <= DiffSummaryBudget*2; i++ {
		fmt.Fprintf(&sb, "+line %08d of synthetic diff body\n", i)
	}
	in := sb.String()

	got := BoundDiffSummary(in)
	if len(got) > DiffSummaryBudget {
		t.Fatalf("bounded length = %d, want <= %d", len(got), DiffSummaryBudget)
	}
	idx := strings.LastIndex(got, "[diff truncated: ")
	if idx < 0 || !strings.HasSuffix(got, " bytes omitted]") {
		t.Fatalf("bounded summary missing truncation marker, tail = %q", got[max(0, len(got)-120):])
	}
	kept := got[:idx]
	if !strings.HasPrefix(in, kept) {
		t.Fatal("kept prefix is not a prefix of the input")
	}
	if kept != "" && !strings.HasSuffix(kept, "\n") {
		t.Fatalf("truncation not on a line boundary, kept tail = %q", kept[len(kept)-40:])
	}
	want := fmt.Sprintf("[diff truncated: %d bytes omitted]", len(in)-len(kept))
	if got[idx:] != want {
		t.Fatalf("marker = %q, want %q", got[idx:], want)
	}
}

func TestComposeBoundedDiffSummaryPrefixesStatHeader(t *testing.T) {
	t.Parallel()
	raw := "Repository: repo\n" +
		"diff --git a/auth.go b/auth.go\n--- a/auth.go\n+++ b/auth.go\n@@ -1,2 +1,3 @@\n+added one\n+added two\n-removed one\n" +
		"diff --git a/db.go b/db.go\n--- a/db.go\n+++ b/db.go\n@@ -1 +1 @@\n+db line\n"

	got := ComposeBoundedDiffSummary(raw)
	for _, want := range []string{
		" auth.go | 2+ 1-\n",
		" db.go | 1+ 0-\n",
		" 2 files changed, 3 insertions(+), 1 deletion(-)\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("stat header missing %q in:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(got, raw) {
		t.Fatalf("diff body not preserved after header:\n%s", got)
	}
}

func TestComposeBoundedDiffSummaryBoundsHugeDiff(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	sb.WriteString("diff --git a/huge.txt b/huge.txt\n--- /dev/null\n+++ b/huge.txt\n")
	for i := 0; sb.Len() <= DiffSummaryBudget*3; i++ {
		fmt.Fprintf(&sb, "+huge line %08d\n", i)
	}

	got := ComposeBoundedDiffSummary(sb.String())
	if len(got) > DiffSummaryBudget {
		t.Fatalf("bounded length = %d, want <= %d", len(got), DiffSummaryBudget)
	}
	if !strings.Contains(got, " huge.txt | ") || !strings.Contains(got, " 1 file changed, ") {
		t.Fatalf("stat header missing from bounded summary head:\n%s", got[:200])
	}
	if !strings.HasSuffix(got, " bytes omitted]") {
		t.Fatalf("bounded summary missing truncation marker, tail = %q", got[len(got)-120:])
	}
}
