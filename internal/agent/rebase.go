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

package agent

import (
	"fmt"
	"strings"
)

// BuildRebasePlan generates a plan document for rebase conflict resolution.
// When conflictFiles is non-empty, the rebase is already in progress with
// conflict markers in the worktree. When empty, the agent must start the
// rebase from scratch.
func BuildRebasePlan(baseBranch, prURL string, conflictFiles []string) string {
	var b strings.Builder
	conflictMarkerPattern := "<<<<<" + "<< "

	fmt.Fprintf(&b, "# Rebase Conflict Resolution Plan\n\n")

	if len(conflictFiles) > 0 {
		// Rebase is in progress — agent just needs to resolve
		fmt.Fprintf(&b, "## Status\n\n")
		fmt.Fprintf(&b, "A `git rebase origin/%s` is **already in progress**. The conflicted files have conflict markers in them.\n\n", baseBranch)

		b.WriteString("## Conflicted Files\n\n")
		for _, f := range conflictFiles {
			fmt.Fprintf(&b, "- `%s`\n", f)
		}
		b.WriteString("\n")

		b.WriteString("## Steps\n\n")
		b.WriteString("### 1. Resolve each conflicted file\n\n")
		b.WriteString("For each file listed above:\n")
		b.WriteString("1. Open the file and resolve the conflict markers (`<<<<<<<` / `=======` / `>>>>>>>`)\n")
		b.WriteString("2. Choose the correct resolution based on understanding both sides of the change\n")
		b.WriteString("3. Stage the resolved file: `git add <file>`\n\n")

		b.WriteString("### 2. Continue the rebase\n\n")
		b.WriteString("```bash\ngit rebase --continue\n```\n\n")
		b.WriteString("If more conflicts appear, repeat step 1.\n\n")

		b.WriteString("## Important Notes\n\n")
		b.WriteString("- Do NOT run `git rebase --abort` — the rebase is already started for you.\n")
	} else {
		// Rebase not started — agent must initiate it
		fmt.Fprintf(&b, "## Overview\n\n")
		fmt.Fprintf(&b, "The feature branch needs to be rebased onto origin/%s.\n\n", baseBranch)

		b.WriteString("## Steps\n\n")
		b.WriteString("### 1. Start the rebase\n\n")
		fmt.Fprintf(&b, "```bash\ngit rebase --abort 2>/dev/null || true\ngit fetch origin\ngit rebase origin/%s\n```\n\n", baseBranch)

		b.WriteString("### 2. Resolve conflicts (if any)\n\n")
		b.WriteString("For each conflicting file:\n")
		b.WriteString("1. Open the file and resolve the conflict markers (`<<<<<<<` / `=======` / `>>>>>>>`)\n")
		b.WriteString("2. Stage the resolved file: `git add <file>`\n")
		b.WriteString("3. Continue: `git rebase --continue`\n")
		b.WriteString("4. Repeat until the rebase is complete.\n\n")
	}

	b.WriteString("### Verify\n\n")
	b.WriteString("After the rebase is fully complete:\n\n")
	b.WriteString("#### Automated Verification:\n")
	fmt.Fprintf(&b, "- [ ] No conflict markers remain: `grep -rn %q . --include=\"*.go\" --include=\"*.ts\" --include=\"*.js\" --include=\"*.py\" | head -20`\n", conflictMarkerPattern)
	writeGenericProjectVerificationChecklist(&b)

	b.WriteString("## Success Criteria\n\n")
	fmt.Fprintf(&b, "- The rebase onto origin/%s is complete (no rebase in progress)\n", baseBranch)
	b.WriteString("- No conflict markers in any files\n")
	b.WriteString("- The code compiles and all tests pass\n\n")

	if prURL != "" {
		fmt.Fprintf(&b, "## PR Reference\n\nExisting PR: %s\n\n", prURL)
	}

	b.WriteString("- Do NOT create a new branch. Work on the current branch.\n")
	b.WriteString("- Do NOT amend or squash commits unnecessarily.\n")

	return b.String()
}
