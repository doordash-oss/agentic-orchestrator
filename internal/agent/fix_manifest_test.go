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
	"reflect"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

const testFixManifestTwoEntries = `entries:
  - layer: 1
    repository: agentic-orchestrator
    paths:
      - internal/git/restack.go
  - layer: 2
    repository: agentic-orchestrator
    paths:
      - internal/agent/round_commit.go
      - internal/agent/fix_manifest.go
`

func writeFixManifestFile(t *testing.T, dir, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, "fix-manifest.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestReadFixManifest(t *testing.T) {
	t.Run("two_entries_parse_into_typed_value", func(t *testing.T) {
		path := writeFixManifestFile(t, t.TempDir(), testFixManifestTwoEntries)
		manifest, diagnostic := ReadFixManifest(path)
		if diagnostic != "" {
			t.Fatalf("diagnostic = %q, want empty", diagnostic)
		}
		want := FixManifest{Entries: []FixManifestEntry{
			{Layer: 1, Repository: "agentic-orchestrator", Paths: []string{"internal/git/restack.go"}},
			{Layer: 2, Repository: "agentic-orchestrator", Paths: []string{"internal/agent/round_commit.go", "internal/agent/fix_manifest.go"}},
		}}
		if !reflect.DeepEqual(manifest, want) {
			t.Fatalf("manifest = %+v, want %+v", manifest, want)
		}
	})

	t.Run("missing_file_yields_empty_manifest_without_diagnostic", func(t *testing.T) {
		manifest, diagnostic := ReadFixManifest(filepath.Join(t.TempDir(), "fix-manifest.yaml"))
		if diagnostic != "" {
			t.Fatalf("diagnostic = %q, want empty for a missing file", diagnostic)
		}
		if len(manifest.Entries) != 0 {
			t.Fatalf("entries = %+v, want empty", manifest.Entries)
		}
	})

	t.Run("malformed_file_yields_empty_manifest_with_diagnostic", func(t *testing.T) {
		path := writeFixManifestFile(t, t.TempDir(), "entries: [unclosed")
		manifest, diagnostic := ReadFixManifest(path)
		if diagnostic == "" {
			t.Fatalf("diagnostic = %q, want a parse diagnostic for a malformed file", diagnostic)
		}
		if len(manifest.Entries) != 0 {
			t.Fatalf("entries = %+v, want empty", manifest.Entries)
		}
	})
}

// TestFinalReviewFixerFixManifestOptionalContract pins the artifact contract
// across the manifest's three states: a present manifest with entries, an
// absent manifest, and a semantically-malformed but YAML-parseable manifest.
// All three must pass both the post-session Validate and the agent-run
// ValidateArtifactsPreflight without violations — the manifest is optional
// advisory input, never a protocol gate.
func TestFinalReviewFixerFixManifestOptionalContract(t *testing.T) {
	cases := []struct {
		name    string
		content string // empty string writes no file
	}{
		{name: "present_with_two_entries", content: testFixManifestTwoEntries},
		{name: "absent", content: ""},
		{name: "semantically_malformed_but_valid_yaml", content: "entries: not-a-list\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			iterDir := t.TempDir()
			if tc.content != "" {
				writeFixManifestFile(t, iterDir, tc.content)
			}

			out, violations, err := Validate(feature.PhaseReview, RoleFinalReviewFixer, iterDir)
			if err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if len(violations) != 0 || !out.OK {
				t.Fatalf("Validate() = (%+v, %v), want OK without violations", out, violations)
			}

			preflight, preflightViolations, err := ValidateArtifactsPreflight(feature.PhaseReview, RoleFinalReviewFixer, iterDir)
			if err != nil {
				t.Fatalf("ValidateArtifactsPreflight() error = %v", err)
			}
			if len(preflightViolations) != 0 || !preflight.OK {
				t.Fatalf("ValidateArtifactsPreflight() = (%+v, %v), want OK without violations", preflight, preflightViolations)
			}

			switch tc.name {
			case "present_with_two_entries":
				if out.FixManifest == nil || len(out.FixManifest.Entries) != 2 {
					t.Fatalf("Outcome.FixManifest = %+v, want the parsed two-entry manifest", out.FixManifest)
				}
			case "absent":
				if out.FixManifest != nil {
					t.Fatalf("Outcome.FixManifest = %+v, want nil for an absent manifest", out.FixManifest)
				}
			case "semantically_malformed_but_valid_yaml":
				// A parse diagnostic is not a violation; the validator may
				// record nothing.
				if out.FixManifest != nil {
					t.Fatalf("Outcome.FixManifest = %+v, want nil for an unparseable-typed manifest", out.FixManifest)
				}
			}
		})
	}
}

// TestFinalReviewFixerSpecDeclaresOptionalFixManifest pins the RoleSpec
// registration: the fixer's contract carries exactly one optional artifact
// resolving to fix-manifest.yaml inside the iteration directory.
func TestFinalReviewFixerSpecDeclaresOptionalFixManifest(t *testing.T) {
	spec := FinalReviewFixerRoleSpec()
	if len(spec.Artifacts) != 1 {
		t.Fatalf("artifacts = %+v, want exactly the fix manifest", spec.Artifacts)
	}
	artifact := spec.Artifacts[0]
	if artifact.Name != "fix_manifest" || artifact.DisplayPath != "fix-manifest.yaml" || artifact.RelativePath != "fix-manifest.yaml" {
		t.Fatalf("artifact identity = %+v", artifact)
	}
	if artifact.Presence != ArtifactOptional {
		t.Fatalf("presence = %q, want optional", artifact.Presence)
	}
	if artifact.HideFromSkill {
		t.Fatal("HideFromSkill = true, want false so the SKILL.md table lists it")
	}
	iterDir := "/state/feat-x/run-001/review/iteration-02"
	if got := spec.ArtifactPath(RoleRuntime{IterationDir: iterDir}, artifact); got != filepath.Join(iterDir, "fix-manifest.yaml") {
		t.Fatalf("artifact path = %q, want %q", got, filepath.Join(iterDir, "fix-manifest.yaml"))
	}
}
