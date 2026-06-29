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

package permission

import "regexp"

// denyListPatterns is the curated static deny list of regex patterns that
// flag commands never eligible for auto-approval. Each pattern covers a
// distinct dangerous category.
var denyListPatterns = []*regexp.Regexp{
	// Recursive / forced deletion (e.g. rm -rf, rm -fr, rmdir -r).
	regexp.MustCompile(`(?i)\brm\b.*(?:\s-[a-zA-Z]*[rf]|\s--recursive|\s--force)`),

	// Force or protected-branch pushes (e.g. git push --force, --force-with-lease, -f).
	regexp.MustCompile(`(?i)\bgit\b.*\bpush\b.*(?:\s-[a-zA-Z]*f|\s--force|\s--force-with-lease)`),

	// Privilege escalation (sudo, su).
	regexp.MustCompile(`(?i)\b(sudo|su)\b`),

	// Broad permission changes: recursive or world-writable chmod.
	regexp.MustCompile(`(?i)\bchmod\b.*(?:\s-[a-zA-Z]*R|\s--recursive|\s777|\s666|\sa\+w)`),
	// Recursive chown.
	regexp.MustCompile(`(?i)\bchown\b.*(?:\s-[a-zA-Z]*R|\s--recursive)`),

	// Credential / secret paths.
	regexp.MustCompile(`(?i)(?:~/\.aws/credentials|~/\.ssh/|\.env\b|id_rsa|\.netrc\b|netrc\b)`),

	// Remote-code-execution pipes (e.g. curl … | sh, wget … | bash).
	regexp.MustCompile(`(?i)\b(?:curl|wget)\b.*\|.*\b(?:sh|bash|python|python3|perl|ruby)\b`),

	// Persistence mechanisms: crontab, shell rc writes, launchd/systemd units.
	regexp.MustCompile(`(?i)\bcrontab\b`),
	regexp.MustCompile(`(?i)>>\s*~/\.(?:bashrc|zshrc|bash_profile|profile)`),
	regexp.MustCompile(`(?i)\blaunchctl\s+load\b`),
	regexp.MustCompile(`(?i)\bsystemctl\s+(?:enable|start|install)\b`),
}

// DenyListMatch reports whether the given Bash tool input matches any
// deny-list pattern. It operates on the full extracted command string (after
// JSON extraction but before shell-chaining/pipe stripping) so it can see
// flags, argument shapes, paths, and pipes. A match means the command should
// never be eligible for auto-approval and should defer to the TUI.
//
// The function is a pure package-level helper with no dependency on config,
// sessions, or the handler.
func DenyListMatch(toolInput string) bool {
	cmd := extractBashCommand(toolInput)
	for _, re := range denyListPatterns {
		if re.MatchString(cmd) {
			return true
		}
	}
	return false
}
