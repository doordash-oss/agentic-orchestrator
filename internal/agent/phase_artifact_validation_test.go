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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestInquiryArtifactContract(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		body string
		want string
	}{
		{name: "codebase questions", body: "# Research Questions\n\n1. Where is repository initialization implemented?\n"},
		{name: "codebase and web questions", body: "# Codebase Research Questions\n\n1. Which git library is in use?\n\n# Web Research Questions\n\n1. What authentication methods does its documentation support?\n"},
		{name: "unfinished interview", body: "# Inquiry: Repository management\n\n## Findings\nThe picker supports initialization.\n\n## Pending design decision\nHow should users choose the workspace root?\n\nThis question was presented to the user; no answer has been received.\n\n## Remaining inquiry scope\nClarify authentication and collision handling.\n", want: "inquiry must contain either"},
		{name: "empty questions section", body: "# Research Questions\n", want: "nonempty numbered list"},
		{name: "empty list item", body: "# Research Questions\n\n1. \n", want: "nonempty numbered list"},
		{name: "unordered notes", body: "# Research Questions\n\n- The picker supports initialization.\n", want: "nonempty numbered list"},
		{name: "missing web section", body: "# Codebase Research Questions\n\n1. Which library is used?\n", want: "inquiry must contain either"},
		{name: "empty web section", body: "# Codebase Research Questions\n\n1. Which library is used?\n\n# Web Research Questions\n", want: "`# Web Research Questions` must contain"},
		{name: "mixed layouts", body: "# Research Questions\n\n1. Which library is used?\n\n# Web Research Questions\n\n1. What does the documentation say?\n", want: "inquiry must contain either"},
		{name: "fenced template", body: "```markdown\n# Research Questions\n\n1. Which components exist?\n```\n", want: "inquiry must contain either"},
		{name: "fenced list", body: "# Research Questions\n\n~~~markdown\n1. Which components exist?\n~~~\n", want: "nonempty numbered list"},
		{name: "list outside section", body: "# Research Questions\n\n# Interview notes\n\n1. Which components exist?\n", want: "nonempty numbered list"},
		{name: "heading case", body: "# Research questions:\n\n1. Which library is used?\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertPhaseArtifactContract(t, feature.PhaseInquire, RoleInquirer, tt.body, tt.want)
		})
	}
}

func TestDesignArtifactContract(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		body string
		want string
	}{
		{name: "complete design", body: testutil.DesignDocumentMarkdown},
		{name: "provisional notes", body: "# Design\n\n## Pending user question\nHow should users choose the workspace root?\n", want: "`## Problem Statement`"},
		{name: "missing acceptance criteria", body: strings.Replace(testutil.DesignDocumentMarkdown, "## Acceptance Criteria", "## Draft acceptance", 1), want: "`## Acceptance Criteria`"},
		{name: "empty acceptance criteria", body: strings.Replace(testutil.DesignDocumentMarkdown, "Cloning a valid URL makes the new repository available in the picker.", "", 1), want: "`## Acceptance Criteria`"},
		{name: "fenced document", body: "````markdown\n" + testutil.DesignDocumentMarkdown + "````\n", want: "`## Problem Statement`"},
		{name: "only a subsection heading", body: strings.Replace(testutil.DesignDocumentMarkdown, "Cloning a valid URL makes the new repository available in the picker.", "### Cloning", 1), want: "`## Acceptance Criteria`"},
		{name: "heading case and trailing colon", body: strings.NewReplacer("## Acceptance Criteria", "## Acceptance criteria:", "## Out of Scope", "##  OUT OF SCOPE").Replace(testutil.DesignDocumentMarkdown)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertPhaseArtifactContract(t, feature.PhaseDesign, RoleDesigner, tt.body, tt.want)
		})
	}
}

func assertPhaseArtifactContract(t *testing.T, phase feature.Phase, role Role, body, wantViolation string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, validate := range []func(feature.Phase, Role, string) (Outcome, []ProtocolViolation, error){Validate, ValidateArtifactsPreflight} {
		out, violations, err := validate(phase, role, dir)
		if err != nil {
			t.Fatal(err)
		}
		if wantViolation == "" {
			if !out.OK || len(violations) != 0 || out.PhaseArtifactPath != path {
				t.Fatalf("valid artifact rejected: outcome=%+v violations=%v", out, violations)
			}
		} else if out.OK || out.PhaseArtifactPath != "" || !strings.Contains(JoinProtocolViolations(violations), wantViolation) {
			t.Fatalf("outcome=%+v violations=%v, want rejection containing %q", out, violations, wantViolation)
		}
	}
}
