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
)

func TestLoadPhaseExtraInstructions(t *testing.T) {
	// Not parallel: no globals touched here, but keep consistent with the
	// package-global accessor tests below.
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan-extra.md")
	if err := os.WriteFile(planPath, []byte("  Always include a rollback section.\n"), 0o644); err != nil {
		t.Fatalf("write plan file: %v", err)
	}
	emptyPath := filepath.Join(dir, "empty.md")
	if err := os.WriteFile(emptyPath, []byte("   \n"), 0o644); err != nil {
		t.Fatalf("write empty file: %v", err)
	}

	configured := map[string]string{
		"plan":        "plan-extra.md",               // relative -> resolved against configDir
		"implement":   filepath.Join(dir, "nope.md"), // missing -> warn + skip
		"review":      emptyPath,                     // empty -> warn + skip
		"not_a_phase": planPath,                      // unknown key -> warn + skip
	}

	got, warnings := LoadPhaseExtraInstructions(configured, dir)

	if len(got) != 1 {
		t.Fatalf("expected 1 loaded entry, got %d: %v", len(got), got)
	}
	if got[feature.PhasePlan.InstructionKey()] != "Always include a rollback section." {
		t.Fatalf("plan content not trimmed/loaded correctly: %q", got[feature.PhasePlan.InstructionKey()])
	}
	if _, ok := got["implement"]; ok {
		t.Fatalf("missing file should have been skipped")
	}
	// One warning each for: missing file, empty file, unknown key.
	if len(warnings) != 3 {
		t.Fatalf("expected 3 warnings, got %d: %v", len(warnings), warnings)
	}
}

func TestLoadPhaseExtraInstructionsRejectsBinary(t *testing.T) {
	dir := t.TempDir()

	nulPath := filepath.Join(dir, "nul.md")
	if err := os.WriteFile(nulPath, []byte("plan text\x00more"), 0o644); err != nil {
		t.Fatalf("write nul file: %v", err)
	}
	invalidUTF8Path := filepath.Join(dir, "invalid.md")
	if err := os.WriteFile(invalidUTF8Path, []byte{0xff, 0xfe, 0xfd, 0xfc}, 0o644); err != nil {
		t.Fatalf("write invalid utf8 file: %v", err)
	}

	configured := map[string]string{
		"plan":      nulPath,
		"implement": invalidUTF8Path,
	}

	got, warnings := LoadPhaseExtraInstructions(configured, dir)

	if len(got) != 0 {
		t.Fatalf("binary files should have been skipped, got %d entries: %v", len(got), got)
	}
	if len(warnings) != 2 {
		t.Fatalf("expected 2 binary warnings, got %d: %v", len(warnings), warnings)
	}
	for _, w := range warnings {
		if !strings.Contains(w, "binary content") {
			t.Errorf("warning should mention binary content: %q", w)
		}
	}
}

func TestLooksBinary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"empty", nil, false},
		{"plain ascii", []byte("hello world"), false},
		{"valid utf8", []byte("caffè — 日本語"), false},
		{"nul byte", []byte("abc\x00def"), true},
		{"invalid utf8", []byte{0xff, 0xfe}, true},
	}
	for _, tt := range tests {
		if got := looksBinary(tt.data); got != tt.want {
			t.Errorf("looksBinary(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestLoadPhaseExtraInstructionsEmptyConfig(t *testing.T) {
	got, warnings := LoadPhaseExtraInstructions(nil, "/tmp")
	if got != nil || warnings != nil {
		t.Fatalf("nil config should yield (nil, nil), got (%v, %v)", got, warnings)
	}
}

func TestPhaseExtraInstructionAccessor(t *testing.T) {
	orig := phaseExtraInstructions
	t.Cleanup(func() { SetPhaseExtraInstructions(orig) })

	SetPhaseExtraInstructions(nil)
	if PhaseExtraInstruction(feature.PhasePlan) != "" {
		t.Fatalf("nil store should return empty string")
	}

	SetPhaseExtraInstructions(map[string]string{
		feature.PhaseImplement.InstructionKey(): "Prefer table-driven tests.",
	})
	if got := PhaseExtraInstruction(feature.PhaseImplement); got != "Prefer table-driven tests." {
		t.Fatalf("PhaseExtraInstruction(implement) = %q", got)
	}
	if got := PhaseExtraInstruction(feature.PhasePlan); got != "" {
		t.Fatalf("unconfigured phase should return empty, got %q", got)
	}
}

func TestBuildRoleSystemPromptRendersPhaseExtraInstructions(t *testing.T) {
	orig := phaseExtraInstructions
	t.Cleanup(func() { SetPhaseExtraInstructions(orig) })

	SetPhaseExtraInstructions(map[string]string{
		feature.PhaseImplement.InstructionKey(): "Never touch generated files.",
	})

	got := BuildImplementSystemPrompt(BuildImplementSystemPromptInput{
		IterationDir: "/state/feat-x/run-001/phase-01/implement/iteration-01",
		SkillsDir:    "/skills",
	})
	if !strings.Contains(got, "## Operator Instructions (Highest Priority)") {
		t.Fatalf("implement system prompt missing operator section:\n%s", got)
	}
	if !strings.Contains(got, "Never touch generated files.") {
		t.Fatalf("implement system prompt missing operator text:\n%s", got)
	}

	// A phase with no configured instructions renders no operator section.
	reviewPrompt := BuildRoleSystemPrompt(BuildRoleSystemPromptInput{
		Spec:         FinalReviewerRoleSpec(),
		IterationDir: "/state/feat-x/run-001/review/iteration-01",
		SkillsDir:    "/skills",
	})
	if strings.Contains(reviewPrompt, "## Operator Instructions") {
		t.Fatalf("unconfigured phase should not render operator section:\n%s", reviewPrompt)
	}
}
