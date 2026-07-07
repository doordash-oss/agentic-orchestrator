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

import "strings"

const (
	// AgenticURL is the public repository URL for attribution.
	AgenticURL = "https://github.com/doordash-oss/agentic-orchestrator"

	// CommitSignatureTrailer is appended to programmatic commit messages as a git trailer.
	CommitSignatureTrailer = "Generated-by: Agentic (" + AgenticURL + ")"

	// PRSignature is the markdown signature appended to PR bodies and comments.
	PRSignature = "\n\n---\n\n*Generated with [agentic orchestrator](" + AgenticURL + ")*"
)

// InjectPRSignature appends the agentic orchestrator signature to a PR body or comment.
// Idempotent — skips if the signature is already present.
func InjectPRSignature(body string) string {
	if strings.Contains(body, PRSignature) {
		return body
	}
	return body + PRSignature
}
