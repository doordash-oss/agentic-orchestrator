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
	"fmt"
	"strings"
)

// StackLayerTrailerKey is the commit-message trailer key carrying the
// parent-stack layer position a review-feedback child commit targets. The
// round-commit hook appends "Stack-Layer: <position>" to each child commit;
// integration reads it back through CommitsBetweenWithStackLayer.
const StackLayerTrailerKey = "Stack-Layer"

// StackLayerCommit pairs one commit of a range with its Stack-Layer trailer
// value, empty when the commit carries no such trailer.
type StackLayerCommit struct {
	SHA   string
	Layer string
}

// CommitsBetweenWithStackLayer lists the commits of one linear range —
// strictly above lower and up to and including upper — oldest first, each
// with its Stack-Layer trailer value. The value is the last "Stack-Layer:"
// line of the commit message, trimmed; commits without one carry an empty
// value.
func CommitsBetweenWithStackLayer(repoPath, lower, upper string) ([]StackLayerCommit, error) {
	cmd := readGitCmd(repoPath, "log", "--reverse", "--format=%H%x1f%B%x1e", lower+".."+upper)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("listing commits %s..%s with %s trailers: %w", lower, upper, StackLayerTrailerKey, err)
	}
	var commits []StackLayerCommit
	for _, record := range strings.Split(string(out), "\x1e") {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		sep := strings.IndexByte(record, '\x1f')
		if sep < 0 {
			continue
		}
		commits = append(commits, StackLayerCommit{
			SHA:   record[:sep],
			Layer: stackLayerTrailerValue(record[sep+1:]),
		})
	}
	return commits, nil
}

// stackLayerTrailerValue extracts the last Stack-Layer trailer line's value
// from a commit message body.
func stackLayerTrailerValue(body string) string {
	value := ""
	prefix := StackLayerTrailerKey + ":"
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		value = strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
	}
	return value
}
