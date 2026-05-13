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
	"os"
	"path/filepath"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// WriteQAFile writes accumulated Q&A pairs to a markdown file in dir.
// Returns the file path, or "" if there are no Q&A pairs.
func WriteQAFile(qaLog []ports.QAPair, dir string) (string, error) {
	if len(qaLog) == 0 {
		return "", nil
	}

	var b strings.Builder
	b.WriteString("# User Q&A — Phase Clarifications\n\n")

	for _, pair := range qaLog {
		fmt.Fprintf(&b, "## Q: %s\n\n", pair.Question)
		fmt.Fprintf(&b, "**A:** %s\n\n", pair.Answer)
		if pair.Notes != "" {
			fmt.Fprintf(&b, "**Notes:** %s\n\n", pair.Notes)
		}
		if pair.AutoPicked {
			fmt.Fprintf(&b, "_(auto-picked, confidence: %.2f)_\n\n", pair.Confidence)
		}
	}

	path := filepath.Join(dir, "qa-answers.md")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", fmt.Errorf("writing qa-answers.md: %w", err)
	}
	return path, nil
}
