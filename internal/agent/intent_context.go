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
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// promptIntent is the statement of feature intent a downstream prompt
// carries. Raw intent (description, exit criteria) stops at the first
// distillation artifact that actually contains its cited section: a
// design's `## Acceptance Criteria` section, or — when the pipeline
// produced no such design — the roadmap's `## Overall Exit Criteria`
// section. Raw intent flows whenever neither artifact qualifies (missing,
// unreadable, or lacking the section). Cycle loops synthesize their own
// criteria and do not use this resolver.
type promptIntent struct {
	Description      string
	ExitCriteria     string
	AcceptanceClause string
}

func resolvePromptIntent(f *feature.Feature) promptIntent {
	if f == nil {
		return promptIntent{}
	}
	if p := f.DesignArtifactPath(); p != "" && artifactHasSection(p, "## Acceptance Criteria") {
		return promptIntent{AcceptanceClause: acceptanceClause("## Acceptance Criteria", "approved design", p)}
	}
	if p := f.Artifacts["roadmap"]; p != "" && artifactHasSection(p, "## Overall Exit Criteria") {
		return promptIntent{AcceptanceClause: acceptanceClause("## Overall Exit Criteria", "approved roadmap", p)}
	}
	return promptIntent{Description: f.Description, ExitCriteria: f.ExitCriteria}
}

// artifactHasSection reports whether path is a readable file containing a
// line that, trimmed, equals heading exactly. An unreadable file lacks the
// section.
func artifactHasSection(path, heading string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == heading {
			return true
		}
	}
	return false
}

func acceptanceClause(section, artifact, path string) string {
	return fmt.Sprintf("Feature acceptance is defined by the `%s` section of the %s at %s. Judge acceptance against that section, not against any restatement of it.", section, artifact, path)
}
