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

// IsAncestor reports whether ancestor is an ancestor of descendant in the
// repository at repoPath. It shells out to
// `git merge-base --is-ancestor <ancestor> <descendant>`, which exits 0 when
// the relationship holds and non-zero otherwise.
//
// The primitive is conservative: it returns false on any git error, unknown
// commit, or when either argument is empty. Callers that need to distinguish
// "definitely not an ancestor" from "git could not answer" should run their
// own command; this boolean is for safety gates where a false result simply
// withholds a positive assertion.
func IsAncestor(repoPath, ancestor, descendant string) bool {
	if ancestor == "" || descendant == "" {
		return false
	}
	cmd := readGitCmd(repoPath, "merge-base", "--is-ancestor", ancestor, descendant)
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}
