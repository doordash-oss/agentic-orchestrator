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
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var updateRoleSpecGenerated = flag.Bool("update", false, "rewrite generated RoleSpec-backed artifacts")

// renderOutputFilesSection renders the generated SKILL.md section derived
// from the spec's artifacts. Every declared artifact is required.
func renderOutputFilesSection(spec roleSpec) string {
	var b strings.Builder
	b.WriteString("## Output Files\n\n")
	b.WriteString("| Artifact | Path | Requirement | Purpose |\n")
	b.WriteString("|----------|------|-------------|---------|\n")
	for _, artifact := range spec.Artifacts {
		if artifact.HideFromSkill {
			continue
		}
		fmt.Fprintf(&b, "| `%s` | `{%s}/%s` | required | %s |\n",
			artifact.DisplayPath,
			artifact.RootName,
			artifact.RelativePath,
			artifact.Description,
		)
	}
	b.WriteString("\n")
	return b.String()
}

// skillOutputRoleSpecs returns the roles whose SKILL.md files carry generated
// Output Files sections.
func skillOutputRoleSpecs() []roleSpec {
	seen := map[string]bool{}
	out := make([]roleSpec, 0, len(roleSpecs))
	for _, spec := range roleSpecs {
		if spec.SkillName == "" || len(spec.Artifacts) == 0 || seen[spec.SkillName] {
			continue
		}
		seen[spec.SkillName] = true
		out = append(out, spec)
	}
	return out
}

func TestSkillOutputFilesMatchRoleSpec(t *testing.T) {
	for _, spec := range skillOutputRoleSpecs() {
		t.Run(spec.SkillName, func(t *testing.T) {
			path := repoRootPath(t, "skills", spec.SkillName, "SKILL.md")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}
			want := renderOutputFilesSection(spec)
			got, ok := extractOutputFilesSection(string(data))
			if *updateRoleSpecGenerated {
				updated, err := replaceOutputFilesSection(string(data), want)
				if err != nil {
					t.Fatalf("updating Output Files section: %v", err)
				}
				if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
					t.Fatalf("writing %s: %v", path, err)
				}
				return
			}
			if !ok || got != want {
				t.Fatalf("the '## Output Files' section in skills/%s/SKILL.md does not match RoleSpec.Artifacts. Run 'go test ./internal/agent/... -update' to refresh it.\n--- WANT ---\n%s\n--- GOT ---\n%s", spec.SkillName, want, got)
			}
		})
	}
}

func TestOutputFilesSectionHidesHarnessArtifacts(t *testing.T) {
	t.Parallel()
	for _, spec := range []roleSpec{roadmapCreatorRoleSpec, roadmapReviserRoleSpec, phasePlanCreatorRoleSpec, phasePlanReviserRoleSpec} {
		if section := renderOutputFilesSection(spec); strings.Contains(section, "meta.yaml") {
			t.Fatalf("%s Output Files section exposes harness-authored meta.yaml:\n%s", spec.Role, section)
		}
	}
	for _, spec := range []roleSpec{knowledgeBaseBuilderRoleSpec, inquirerRoleSpec, researcherRoleSpec, designerRoleSpec} {
		if section := renderOutputFilesSection(spec); !strings.Contains(section, "{phase_dir}/") {
			t.Fatalf("%s generated section missing phase_dir path:\n%s", spec.Role, section)
		}
	}
}

func TestImplementSkillDiscoversTestingContractDirectly(t *testing.T) {
	path := repoRootPath(t, "skills", "implement", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	content := string(data)
	for _, want := range []string{
		"{phase_dir}/../testing-contract.yaml",
		"owner: agent",
		"Never create or edit `verification-report.yaml`",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("skills/implement/SKILL.md missing %q", want)
		}
	}
}

func TestImplementSkillDocumentsEvidenceFiles(t *testing.T) {
	path := repoRootPath(t, "skills", "implement", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	content := string(data)
	for _, want := range []string{
		"`screenshots/`",
		"`behaviors/`",
		"expected_evidence.path",
		"Never create or edit `verification-report.yaml`",
		"Never create placeholder evidence",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("skills/implement/SKILL.md missing evidence-file guidance %q", want)
		}
	}
}

func TestImplementSkillReadsPhaseProgressOnResume(t *testing.T) {
	path := repoRootPath(t, "skills", "implement", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	content := string(data)
	for _, want := range []string{
		"{phase_dir}/progress.md",
		"### Where I stopped",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("skills/implement/SKILL.md missing resume instruction %q", want)
		}
	}
	if strings.Contains(content, "Read any inlined prior `progress.md` first") {
		t.Fatalf("skills/implement/SKILL.md still depends on inlined progress.md for resume state")
	}
}

func repoRootPath(t testing.TB, elems ...string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	parts := append([]string{filepath.Dir(filepath.Dir(filepath.Dir(file)))}, elems...)
	return filepath.Join(parts...)
}

func extractOutputFilesSection(content string) (string, bool) {
	start := strings.Index(content, "## Output Files\n")
	if start < 0 {
		return "", false
	}
	rest := content[start+len("## Output Files\n"):]
	end := nextMarkdownHeadingIndex(rest)
	if end < 0 {
		return content[start:], true
	}
	return content[start : start+len("## Output Files\n")+end+1], true
}

func replaceOutputFilesSection(content, section string) (string, error) {
	start := strings.Index(content, "## Output Files\n")
	if start < 0 {
		insertAt := strings.Index(content, "\n# ")
		if insertAt < 0 {
			if frontmatterEnd := strings.Index(content, "\n---\n"); frontmatterEnd >= 0 {
				pos := frontmatterEnd + len("\n---\n")
				return content[:pos] + "\n" + section + "\n" + content[pos:], nil
			}
			return content + "\n\n" + section, nil
		}
		lineEnd := strings.Index(content[insertAt+1:], "\n")
		if lineEnd < 0 {
			return content + "\n\n" + section, nil
		}
		pos := insertAt + 1 + lineEnd + 1
		return content[:pos] + "\n" + section + "\n" + content[pos:], nil
	}
	rest := content[start+len("## Output Files\n"):]
	end := nextMarkdownHeadingIndex(rest)
	if end < 0 {
		return content[:start] + section, nil
	}
	endPos := start + len("## Output Files\n") + end + 1
	return content[:start] + section + content[endPos:], nil
}

func nextMarkdownHeadingIndex(s string) int {
	offset := 0
	for {
		i := strings.Index(s[offset:], "\n#")
		if i < 0 {
			return -1
		}
		pos := offset + i
		lineStart := pos + 1
		j := lineStart
		for j < len(s) && s[j] == '#' {
			j++
		}
		if j > lineStart && j < len(s) && s[j] == ' ' {
			return pos
		}
		offset = lineStart + 1
	}
}
