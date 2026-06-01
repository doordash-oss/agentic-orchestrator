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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseReviewProgressHandoffMd(t *testing.T) {
	path := writeHelperHandoff(t, t.TempDir(), ReviewProgressHandoffFilename, validReviewProgressHandoff("CONTINUE", "reviewed verification rows"))

	parsed, err := ParseReviewProgressHandoffMd(path)
	if err != nil {
		t.Fatalf("ParseReviewProgressHandoffMd() error = %v", err)
	}
	if !parsed.OK() {
		t.Fatalf("ParseReviewProgressHandoffMd() violations = %v", parsed.ProtocolViolations)
	}
	if parsed.State != HelperHandoffContinue {
		t.Fatalf("State = %s, want CONTINUE", parsed.State)
	}
	if !strings.Contains(parsed.ProgressRegion, "reviewed verification rows") {
		t.Fatalf("ProgressRegion missing examined work:\n%s", parsed.ProgressRegion)
	}

	missing := writeHelperHandoff(t, t.TempDir(), ReviewProgressHandoffFilename, strings.ReplaceAll(validReviewProgressHandoff("CONTINUE", "x"), "## Advisory Findings\n- Finding A\n\n", ""))
	parsed, err = ParseReviewProgressHandoffMd(missing)
	if err != nil {
		t.Fatalf("ParseReviewProgressHandoffMd(missing) error = %v", err)
	}
	if parsed.OK() || !containsViolation(parsed.ProtocolViolations, "missing required section") {
		t.Fatalf("missing section parse = OK %v violations %v, want missing-section violation", parsed.OK(), parsed.ProtocolViolations)
	}

	invalid := writeHelperHandoff(t, t.TempDir(), ReviewProgressHandoffFilename, validReviewProgressHandoff("DONE", "x"))
	parsed, err = ParseReviewProgressHandoffMd(invalid)
	if err != nil {
		t.Fatalf("ParseReviewProgressHandoffMd(invalid) error = %v", err)
	}
	if parsed.OK() || !containsViolation(parsed.ProtocolViolations, "CONTINUE, COMPLETE") {
		t.Fatalf("invalid state parse = OK %v violations %v, want state violation", parsed.OK(), parsed.ProtocolViolations)
	}
}

func TestReviewProgressHandoffFingerprint(t *testing.T) {
	a := writeHelperHandoff(t, filepath.Join(t.TempDir(), "run-001", "review", "iteration-01"), ReviewProgressHandoffFilename, validReviewProgressHandoff("CONTINUE", "checked `/tmp/a/run-001/review/iteration-01/verification-report.yaml` at 2026-05-29T10:11:12Z"))
	b := writeHelperHandoff(t, filepath.Join(t.TempDir(), "run-002", "review", "iteration-07"), ReviewProgressHandoffFilename, validReviewProgressHandoff("CONTINUE", "checked `/var/folders/x/run-002/review/iteration-07/verification-report.yaml` at 2026-05-30T01:02:03-07:00"))

	fpA, err := ReviewProgressHandoffFingerprint(a)
	if err != nil {
		t.Fatalf("ReviewProgressHandoffFingerprint(a) error = %v", err)
	}
	fpB, err := ReviewProgressHandoffFingerprint(b)
	if err != nil {
		t.Fatalf("ReviewProgressHandoffFingerprint(b) error = %v", err)
	}
	if fpA != fpB {
		t.Fatalf("volatile-only fingerprints differ:\n%s\n%s", fpA, fpB)
	}

	c := writeHelperHandoff(t, t.TempDir(), ReviewProgressHandoffFilename, validReviewProgressHandoff("CONTINUE", "checked a new failure mode"))
	fpC, err := ReviewProgressHandoffFingerprint(c)
	if err != nil {
		t.Fatalf("ReviewProgressHandoffFingerprint(c) error = %v", err)
	}
	if fpA == fpC {
		t.Fatal("fingerprint did not change after examined work changed")
	}
}

func TestValidatorHandoffAssetsShareReviewProgressContract(t *testing.T) {
	skills := []string{
		"validate-roadmap-architecture",
		"validate-roadmap-scope",
		"validate-phase-plan-structural",
		"validate-phase-plan-scope",
		"validate-phase-plan-grounding",
		"validate-plan-security",
		"validate-plan-performance",
		"validate-plan-testing",
	}

	var baseline []byte
	for _, skill := range skills {
		t.Run(skill, func(t *testing.T) {
			path := repoRootPath(t, "skills", skill, "HANDOFF.md")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}
			if baseline == nil {
				baseline = append([]byte(nil), data...)
			} else if string(data) != string(baseline) {
				t.Fatalf("%s HANDOFF.md differs from first validator handoff", skill)
			}
			text := string(data)
			for _, want := range []string{
				"## Examined Work",
				"## Advisory Findings",
				"## Where I Stopped",
				"## Handoff State",
				"CONTINUE",
				"COMPLETE",
				"advisory only",
				"validation-<axis>-feedback.md",
				"phase_complete",
			} {
				if !strings.Contains(text, want) {
					t.Fatalf("%s HANDOFF.md missing %q", skill, want)
				}
			}
		})
	}

	sample := extractFirstMarkdownFence(t, string(baseline))
	path := writeHelperHandoff(t, t.TempDir(), ReviewProgressHandoffFilename, sample)
	parsed, err := ParseReviewProgressHandoffMd(path)
	if err != nil {
		t.Fatalf("ParseReviewProgressHandoffMd() error = %v", err)
	}
	if !parsed.OK() {
		t.Fatalf("validator HANDOFF.md sample violations = %v", parsed.ProtocolViolations)
	}
	if parsed.State != HelperHandoffContinue {
		t.Fatalf("State = %s, want CONTINUE", parsed.State)
	}
}

func TestParseProducerProgressHandoffMd(t *testing.T) {
	path := writeHelperHandoff(t, t.TempDir(), ProducerProgressHandoffFilename, validProducerProgressHandoff("COMPLETE", "updated config loader"))

	parsed, err := ParseProducerProgressHandoffMd(path)
	if err != nil {
		t.Fatalf("ParseProducerProgressHandoffMd() error = %v", err)
	}
	if !parsed.OK() {
		t.Fatalf("ParseProducerProgressHandoffMd() violations = %v", parsed.ProtocolViolations)
	}
	if parsed.State != HelperHandoffComplete {
		t.Fatalf("State = %s, want COMPLETE", parsed.State)
	}
	if !strings.Contains(parsed.ProgressRegion, "updated config loader") {
		t.Fatalf("ProgressRegion missing completed work:\n%s", parsed.ProgressRegion)
	}

	invalid := writeHelperHandoff(t, t.TempDir(), ProducerProgressHandoffFilename, validProducerProgressHandoff("RETRY", "x"))
	parsed, err = ParseProducerProgressHandoffMd(invalid)
	if err != nil {
		t.Fatalf("ParseProducerProgressHandoffMd(invalid) error = %v", err)
	}
	if parsed.OK() || !containsViolation(parsed.ProtocolViolations, "CONTINUE, COMPLETE") {
		t.Fatalf("invalid state parse = OK %v violations %v, want state violation", parsed.OK(), parsed.ProtocolViolations)
	}
}

func TestParseResearchProgressHandoffMd(t *testing.T) {
	path := writeHelperHandoff(t, t.TempDir(), ResearchProgressHandoffFilename, validResearchProgressHandoff("CONTINUE", "mapped repo topology"))

	parsed, err := ParseResearchProgressHandoffMd(path)
	if err != nil {
		t.Fatalf("ParseResearchProgressHandoffMd() error = %v", err)
	}
	if !parsed.OK() {
		t.Fatalf("ParseResearchProgressHandoffMd() violations = %v", parsed.ProtocolViolations)
	}
	if parsed.State != HelperHandoffContinue {
		t.Fatalf("State = %s, want CONTINUE", parsed.State)
	}
	if !strings.Contains(parsed.ProgressRegion, "mapped repo topology") {
		t.Fatalf("ProgressRegion missing completed findings:\n%s", parsed.ProgressRegion)
	}

	complete := writeHelperHandoff(t, t.TempDir(), ResearchProgressHandoffFilename, validResearchProgressHandoff("COMPLETE", "answered all questions"))
	parsed, err = ParseResearchProgressHandoffMd(complete)
	if err != nil {
		t.Fatalf("ParseResearchProgressHandoffMd(complete) error = %v", err)
	}
	if !parsed.OK() || parsed.State != HelperHandoffComplete {
		t.Fatalf("complete parse = state %s OK %v violations %v, want COMPLETE without violations", parsed.State, parsed.OK(), parsed.ProtocolViolations)
	}

	missing := writeHelperHandoff(t, t.TempDir(), ResearchProgressHandoffFilename, strings.ReplaceAll(validResearchProgressHandoff("CONTINUE", "x"), "## Remaining Areas\n- none\n\n", ""))
	parsed, err = ParseResearchProgressHandoffMd(missing)
	if err != nil {
		t.Fatalf("ParseResearchProgressHandoffMd(missing) error = %v", err)
	}
	if parsed.OK() || !containsViolation(parsed.ProtocolViolations, "missing required section") {
		t.Fatalf("missing section parse = OK %v violations %v, want missing-section violation", parsed.OK(), parsed.ProtocolViolations)
	}

	invalid := writeHelperHandoff(t, t.TempDir(), ResearchProgressHandoffFilename, validResearchProgressHandoff("DONE", "x"))
	parsed, err = ParseResearchProgressHandoffMd(invalid)
	if err != nil {
		t.Fatalf("ParseResearchProgressHandoffMd(invalid) error = %v", err)
	}
	if parsed.OK() || !containsViolation(parsed.ProtocolViolations, "CONTINUE, COMPLETE") {
		t.Fatalf("invalid state parse = OK %v violations %v, want state violation", parsed.OK(), parsed.ProtocolViolations)
	}
}

func TestParseKnowledgeBaseProgressHandoffMd(t *testing.T) {
	path := writeHelperHandoff(t, t.TempDir(), KBProgressHandoffFilename, validKBProgressHandoff("CONTINUE", "architecture: index.md, data-flow.md"))

	parsed, err := ParseKBProgressHandoffMd(path)
	if err != nil {
		t.Fatalf("ParseKBProgressHandoffMd() error = %v", err)
	}
	if !parsed.OK() {
		t.Fatalf("ParseKBProgressHandoffMd() violations = %v", parsed.ProtocolViolations)
	}
	if parsed.State != HelperHandoffContinue {
		t.Fatalf("State = %s, want CONTINUE", parsed.State)
	}
	if !strings.Contains(parsed.ProgressRegion, "architecture: index.md, data-flow.md") {
		t.Fatalf("ProgressRegion missing completed category:\n%s", parsed.ProgressRegion)
	}

	complete := writeHelperHandoff(t, t.TempDir(), KBProgressHandoffFilename, validKBProgressHandoff("COMPLETE", "verification: index.md"))
	parsed, err = ParseKBProgressHandoffMd(complete)
	if err != nil {
		t.Fatalf("ParseKBProgressHandoffMd(complete) error = %v", err)
	}
	if !parsed.OK() || parsed.State != HelperHandoffComplete {
		t.Fatalf("complete parse = state %s OK %v violations %v, want COMPLETE without violations", parsed.State, parsed.OK(), parsed.ProtocolViolations)
	}

	missing := writeHelperHandoff(t, t.TempDir(), KBProgressHandoffFilename, strings.ReplaceAll(validKBProgressHandoff("CONTINUE", "x"), "## Remaining Categories\n- none\n\n", ""))
	parsed, err = ParseKBProgressHandoffMd(missing)
	if err != nil {
		t.Fatalf("ParseKBProgressHandoffMd(missing) error = %v", err)
	}
	if parsed.OK() || !containsViolation(parsed.ProtocolViolations, "missing required section") {
		t.Fatalf("missing section parse = OK %v violations %v, want missing-section violation", parsed.OK(), parsed.ProtocolViolations)
	}

	invalid := writeHelperHandoff(t, t.TempDir(), KBProgressHandoffFilename, validKBProgressHandoff("DONE", "x"))
	parsed, err = ParseKBProgressHandoffMd(invalid)
	if err != nil {
		t.Fatalf("ParseKBProgressHandoffMd(invalid) error = %v", err)
	}
	if parsed.OK() || !containsViolation(parsed.ProtocolViolations, "CONTINUE, COMPLETE") {
		t.Fatalf("invalid state parse = OK %v violations %v, want state violation", parsed.OK(), parsed.ProtocolViolations)
	}
}

func TestParseInquireProgressHandoffMd(t *testing.T) {
	path := writeHelperHandoff(t, t.TempDir(), InquireProgressHandoffFilename, validInquireProgressHandoff("CONTINUE", "captured auth requirements"))

	parsed, err := ParseInquireProgressHandoffMd(path)
	if err != nil {
		t.Fatalf("ParseInquireProgressHandoffMd() error = %v", err)
	}
	if !parsed.OK() {
		t.Fatalf("ParseInquireProgressHandoffMd() violations = %v", parsed.ProtocolViolations)
	}
	if parsed.State != HelperHandoffContinue {
		t.Fatalf("State = %s, want CONTINUE", parsed.State)
	}
	if !strings.Contains(parsed.ProgressRegion, "captured auth requirements") {
		t.Fatalf("ProgressRegion missing clarified requirements:\n%s", parsed.ProgressRegion)
	}

	missing := writeHelperHandoff(t, t.TempDir(), InquireProgressHandoffFilename, strings.ReplaceAll(validInquireProgressHandoff("CONTINUE", "x"), "## Open Questions\n- none\n\n", ""))
	parsed, err = ParseInquireProgressHandoffMd(missing)
	if err != nil {
		t.Fatalf("ParseInquireProgressHandoffMd(missing) error = %v", err)
	}
	if parsed.OK() || !containsViolation(parsed.ProtocolViolations, "missing required section") {
		t.Fatalf("missing section parse = OK %v violations %v, want missing-section violation", parsed.OK(), parsed.ProtocolViolations)
	}

	invalid := writeHelperHandoff(t, t.TempDir(), InquireProgressHandoffFilename, validInquireProgressHandoff("DONE", "x"))
	parsed, err = ParseInquireProgressHandoffMd(invalid)
	if err != nil {
		t.Fatalf("ParseInquireProgressHandoffMd(invalid) error = %v", err)
	}
	if parsed.OK() || !containsViolation(parsed.ProtocolViolations, "CONTINUE, COMPLETE") {
		t.Fatalf("invalid state parse = OK %v violations %v, want state violation", parsed.OK(), parsed.ProtocolViolations)
	}
}

func TestParseDesignProgressHandoffMd(t *testing.T) {
	path := writeHelperHandoff(t, t.TempDir(), DesignProgressHandoffFilename, validDesignProgressHandoff("COMPLETE", "selected adapter boundary"))

	parsed, err := ParseDesignProgressHandoffMd(path)
	if err != nil {
		t.Fatalf("ParseDesignProgressHandoffMd() error = %v", err)
	}
	if !parsed.OK() {
		t.Fatalf("ParseDesignProgressHandoffMd() violations = %v", parsed.ProtocolViolations)
	}
	if parsed.State != HelperHandoffComplete {
		t.Fatalf("State = %s, want COMPLETE", parsed.State)
	}
	if !strings.Contains(parsed.ProgressRegion, "selected adapter boundary") {
		t.Fatalf("ProgressRegion missing decisions made:\n%s", parsed.ProgressRegion)
	}

	missing := writeHelperHandoff(t, t.TempDir(), DesignProgressHandoffFilename, strings.ReplaceAll(validDesignProgressHandoff("CONTINUE", "x"), "## Open Design Areas\n- none\n\n", ""))
	parsed, err = ParseDesignProgressHandoffMd(missing)
	if err != nil {
		t.Fatalf("ParseDesignProgressHandoffMd(missing) error = %v", err)
	}
	if parsed.OK() || !containsViolation(parsed.ProtocolViolations, "missing required section") {
		t.Fatalf("missing section parse = OK %v violations %v, want missing-section violation", parsed.OK(), parsed.ProtocolViolations)
	}

	invalid := writeHelperHandoff(t, t.TempDir(), DesignProgressHandoffFilename, validDesignProgressHandoff("DONE", "x"))
	parsed, err = ParseDesignProgressHandoffMd(invalid)
	if err != nil {
		t.Fatalf("ParseDesignProgressHandoffMd(invalid) error = %v", err)
	}
	if parsed.OK() || !containsViolation(parsed.ProtocolViolations, "CONTINUE, COMPLETE") {
		t.Fatalf("invalid state parse = OK %v violations %v, want state violation", parsed.OK(), parsed.ProtocolViolations)
	}
}

func TestProducerProgressHandoffFingerprint(t *testing.T) {
	a := writeHelperHandoff(t, filepath.Join(t.TempDir(), "iteration-01"), ProducerProgressHandoffFilename, validProducerProgressHandoff("CONTINUE", "wrote `/tmp/a/iteration-01/verification-report.yaml` at 2026-05-29T10:11:12Z"))
	b := writeHelperHandoff(t, filepath.Join(t.TempDir(), "iteration-02"), ProducerProgressHandoffFilename, validProducerProgressHandoff("CONTINUE", "wrote `/tmp/b/iteration-02/verification-report.yaml` at 2026-05-30T01:02:03Z"))

	fpA, err := ProducerProgressHandoffFingerprint(a)
	if err != nil {
		t.Fatalf("ProducerProgressHandoffFingerprint(a) error = %v", err)
	}
	fpB, err := ProducerProgressHandoffFingerprint(b)
	if err != nil {
		t.Fatalf("ProducerProgressHandoffFingerprint(b) error = %v", err)
	}
	if fpA != fpB {
		t.Fatalf("volatile-only fingerprints differ:\n%s\n%s", fpA, fpB)
	}
}

func TestInquireProgressHandoffFingerprint(t *testing.T) {
	a := writeHelperHandoff(t, filepath.Join(t.TempDir(), "run-001", "inquire", "iteration-01"), InquireProgressHandoffFilename, validInquireProgressHandoff("CONTINUE", "documented `/tmp/a/run-001/inquire/iteration-01/questions.md` at 2026-05-29T10:11:12Z"))
	b := writeHelperHandoff(t, filepath.Join(t.TempDir(), "run-002", "inquire", "iteration-07"), InquireProgressHandoffFilename, validInquireProgressHandoff("CONTINUE", "documented `/var/folders/x/run-002/inquire/iteration-07/questions.md` at 2026-05-30T01:02:03-07:00"))

	fpA, err := InquireProgressHandoffFingerprint(a)
	if err != nil {
		t.Fatalf("InquireProgressHandoffFingerprint(a) error = %v", err)
	}
	fpB, err := InquireProgressHandoffFingerprint(b)
	if err != nil {
		t.Fatalf("InquireProgressHandoffFingerprint(b) error = %v", err)
	}
	if fpA != fpB {
		t.Fatalf("volatile-only fingerprints differ:\n%s\n%s", fpA, fpB)
	}

	c := writeHelperHandoff(t, t.TempDir(), InquireProgressHandoffFilename, validInquireProgressHandoff("CONTINUE", "documented a different requirement"))
	fpC, err := InquireProgressHandoffFingerprint(c)
	if err != nil {
		t.Fatalf("InquireProgressHandoffFingerprint(c) error = %v", err)
	}
	if fpA == fpC {
		t.Fatal("fingerprint did not change after clarified requirements changed")
	}
}

func TestDesignProgressHandoffFingerprint(t *testing.T) {
	a := writeHelperHandoff(t, filepath.Join(t.TempDir(), "run-001", "design", "iteration-01"), DesignProgressHandoffFilename, validDesignProgressHandoff("CONTINUE", "documented `/tmp/a/run-001/design/iteration-01/design.md` at 2026-05-29T10:11:12Z"))
	b := writeHelperHandoff(t, filepath.Join(t.TempDir(), "run-002", "design", "iteration-07"), DesignProgressHandoffFilename, validDesignProgressHandoff("CONTINUE", "documented `/var/folders/x/run-002/design/iteration-07/design.md` at 2026-05-30T01:02:03-07:00"))

	fpA, err := DesignProgressHandoffFingerprint(a)
	if err != nil {
		t.Fatalf("DesignProgressHandoffFingerprint(a) error = %v", err)
	}
	fpB, err := DesignProgressHandoffFingerprint(b)
	if err != nil {
		t.Fatalf("DesignProgressHandoffFingerprint(b) error = %v", err)
	}
	if fpA != fpB {
		t.Fatalf("volatile-only fingerprints differ:\n%s\n%s", fpA, fpB)
	}

	c := writeHelperHandoff(t, t.TempDir(), DesignProgressHandoffFilename, validDesignProgressHandoff("CONTINUE", "documented a different decision"))
	fpC, err := DesignProgressHandoffFingerprint(c)
	if err != nil {
		t.Fatalf("DesignProgressHandoffFingerprint(c) error = %v", err)
	}
	if fpA == fpC {
		t.Fatal("fingerprint did not change after decisions changed")
	}
}

func TestResearchHandoffAssetMatchesResearchProgressContract(t *testing.T) {
	path := repoRootPath(t, "skills", "research-codebase", "HANDOFF.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	text := string(data)
	for _, want := range []string{
		ResearchProgressHandoffFilename,
		"## Completed Findings",
		"## Remaining Areas",
		"## Where I Stopped",
		"## Gotchas",
		"## Handoff State",
		"CONTINUE",
		"phase_complete",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("research HANDOFF.md missing %q", want)
		}
	}

	sample := extractFirstMarkdownFence(t, text)
	handoffPath := writeHelperHandoff(t, t.TempDir(), ResearchProgressHandoffFilename, sample)
	parsed, err := ParseResearchProgressHandoffMd(handoffPath)
	if err != nil {
		t.Fatalf("ParseResearchProgressHandoffMd() error = %v", err)
	}
	if !parsed.OK() {
		t.Fatalf("research HANDOFF.md sample violations = %v", parsed.ProtocolViolations)
	}
	if parsed.State != HelperHandoffContinue {
		t.Fatalf("State = %s, want CONTINUE", parsed.State)
	}
}

func TestInquireAndDesignHandoffAssetsMatchProgressContracts(t *testing.T) {
	tests := []struct {
		skill    string
		filename string
		sections []string
		parse    func(string) (*ParsedHelperHandoff, error)
	}{
		{
			skill:    "inquire",
			filename: InquireProgressHandoffFilename,
			sections: []string{
				"## Clarified Requirements",
				"## Open Questions",
				"## Where I Stopped",
				"## Gotchas",
				"## Handoff State",
			},
			parse: ParseInquireProgressHandoffMd,
		},
		{
			skill:    "design",
			filename: DesignProgressHandoffFilename,
			sections: []string{
				"## Decisions Made",
				"## Open Design Areas",
				"## Where I Stopped",
				"## Gotchas",
				"## Handoff State",
			},
			parse: ParseDesignProgressHandoffMd,
		},
	}

	for _, tt := range tests {
		t.Run(tt.skill, func(t *testing.T) {
			path := repoRootPath(t, "skills", tt.skill, "HANDOFF.md")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}
			text := string(data)
			for _, want := range append([]string{tt.filename, "CONTINUE", "phase_complete"}, tt.sections...) {
				if !strings.Contains(text, want) {
					t.Fatalf("%s HANDOFF.md missing %q", tt.skill, want)
				}
			}

			sample := extractFirstMarkdownFence(t, text)
			handoffPath := writeHelperHandoff(t, t.TempDir(), tt.filename, sample)
			parsed, err := tt.parse(handoffPath)
			if err != nil {
				t.Fatalf("parse %s error = %v", tt.filename, err)
			}
			if !parsed.OK() {
				t.Fatalf("%s HANDOFF.md sample violations = %v", tt.skill, parsed.ProtocolViolations)
			}
			if parsed.State != HelperHandoffContinue {
				t.Fatalf("State = %s, want CONTINUE", parsed.State)
			}
		})
	}
}

func TestBuildKnowledgeBaseSkillDocumentsParserValidCompleteProgress(t *testing.T) {
	path := repoRootPath(t, "skills", "build-knowledge-base", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	text := string(data)
	for _, want := range []string{
		KBProgressHandoffFilename,
		"## Completed Categories",
		"## Remaining Categories",
		"## Where I Stopped",
		"## Gotchas",
		"## Handoff State",
		"COMPLETE",
		"phase_complete",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("build-knowledge-base SKILL.md missing %q", want)
		}
	}

	sample := extractMarkdownFenceContaining(t, text, "COMPLETE")
	handoffPath := writeHelperHandoff(t, t.TempDir(), KBProgressHandoffFilename, sample)
	parsed, err := ParseKBProgressHandoffMd(handoffPath)
	if err != nil {
		t.Fatalf("ParseKBProgressHandoffMd() error = %v", err)
	}
	if !parsed.OK() {
		t.Fatalf("build-knowledge-base SKILL.md COMPLETE sample violations = %v", parsed.ProtocolViolations)
	}
	if parsed.State != HelperHandoffComplete {
		t.Fatalf("State = %s, want COMPLETE", parsed.State)
	}
}

func TestResearchProgressHandoffFingerprint(t *testing.T) {
	a := writeHelperHandoff(t, filepath.Join(t.TempDir(), "run-001", "research", "iteration-01"), ResearchProgressHandoffFilename, validResearchProgressHandoff("CONTINUE", "documented `/tmp/a/run-001/research/iteration-01/service.md` at 2026-05-29T10:11:12Z"))
	b := writeHelperHandoff(t, filepath.Join(t.TempDir(), "run-002", "research", "iteration-07"), ResearchProgressHandoffFilename, validResearchProgressHandoff("CONTINUE", "documented `/var/folders/x/run-002/research/iteration-07/service.md` at 2026-05-30T01:02:03-07:00"))

	fpA, err := ResearchProgressHandoffFingerprint(a)
	if err != nil {
		t.Fatalf("ResearchProgressHandoffFingerprint(a) error = %v", err)
	}
	fpB, err := ResearchProgressHandoffFingerprint(b)
	if err != nil {
		t.Fatalf("ResearchProgressHandoffFingerprint(b) error = %v", err)
	}
	if fpA != fpB {
		t.Fatalf("volatile-only fingerprints differ:\n%s\n%s", fpA, fpB)
	}

	c := writeHelperHandoff(t, t.TempDir(), ResearchProgressHandoffFilename, validResearchProgressHandoff("CONTINUE", "documented a different subsystem"))
	fpC, err := ResearchProgressHandoffFingerprint(c)
	if err != nil {
		t.Fatalf("ResearchProgressHandoffFingerprint(c) error = %v", err)
	}
	if fpA == fpC {
		t.Fatal("fingerprint did not change after completed findings changed")
	}
}

func TestKBProgressHandoffFingerprint(t *testing.T) {
	a := writeHelperHandoff(t, filepath.Join(t.TempDir(), "run-001", "knowledge-base", "repo", "iteration-01"), KBProgressHandoffFilename, validKBProgressHandoff("CONTINUE", "architecture: `/tmp/a/run-001/knowledge-base/repo/iteration-01/index.md` at 2026-05-29T10:11:12Z"))
	b := writeHelperHandoff(t, filepath.Join(t.TempDir(), "run-002", "knowledge-base", "repo", "iteration-07"), KBProgressHandoffFilename, validKBProgressHandoff("CONTINUE", "architecture: `/var/folders/x/run-002/knowledge-base/repo/iteration-07/index.md` at 2026-05-30T01:02:03-07:00"))

	fpA, err := KBProgressHandoffFingerprint(a)
	if err != nil {
		t.Fatalf("KBProgressHandoffFingerprint(a) error = %v", err)
	}
	fpB, err := KBProgressHandoffFingerprint(b)
	if err != nil {
		t.Fatalf("KBProgressHandoffFingerprint(b) error = %v", err)
	}
	if fpA != fpB {
		t.Fatalf("volatile-only fingerprints differ:\n%s\n%s", fpA, fpB)
	}

	c := writeHelperHandoff(t, t.TempDir(), KBProgressHandoffFilename, validKBProgressHandoff("CONTINUE", "verification: index.md, test-suites.md"))
	fpC, err := KBProgressHandoffFingerprint(c)
	if err != nil {
		t.Fatalf("KBProgressHandoffFingerprint(c) error = %v", err)
	}
	if fpA == fpC {
		t.Fatal("fingerprint did not change after category progress changed")
	}
}

func TestRunHelperWithContinuations(t *testing.T) {
	tmpDir := t.TempDir()
	handoffPath := filepath.Join(tmpDir, ReviewProgressHandoffFilename)
	canonicalPath := filepath.Join(tmpDir, "review-feedback.md")
	if err := os.WriteFile(canonicalPath, []byte("# Review\n"), 0o644); err != nil {
		t.Fatalf("write canonical: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, PhaseCompleteFile), []byte("stale\n"), 0o644); err != nil {
		t.Fatalf("write stale phase_complete: %v", err)
	}

	var prompts []string
	parentCounter := 41
	result, err := runHelperWithContinuations(context.Background(), helperContinuationConfig{
		Label:                "review helper",
		SessionIDBase:        "review-01",
		HandoffPath:          handoffPath,
		CanonicalPaths:       []string{canonicalPath},
		ParseHandoff:         ParseReviewProgressHandoffMd,
		Fingerprint:          ReviewProgressHandoffFingerprint,
		MaxConsecNoProgress:  3,
		MaxConsecMalformed:   3,
		ContinuationSkill:    "review-implementation",
		ContinuationArtifact: ReviewProgressHandoffFilename,
		RunSession: func(_ context.Context, in helperContinuationRunInput) (helperContinuationRunResult, error) {
			prompts = append(prompts, in.Prompt)
			if HasPhaseComplete(tmpDir) {
				t.Fatalf("phase_complete should be cleared before continuation %d starts", in.Continuation)
			}
			switch in.Continuation {
			case 0:
				if in.SessionID != "review-01" {
					t.Fatalf("initial SessionID = %q, want review-01", in.SessionID)
				}
				writeHelperHandoff(t, tmpDir, ReviewProgressHandoffFilename, validReviewProgressHandoff("CONTINUE", "checked report"))
				if err := os.WriteFile(filepath.Join(tmpDir, PhaseCompleteFile), []byte("continue\n"), 0o644); err != nil {
					t.Fatalf("write phase_complete: %v", err)
				}
			case 1:
				if in.SessionID != "review-01-c02" {
					t.Fatalf("continuation SessionID = %q, want review-01-c02", in.SessionID)
				}
				if !strings.Contains(in.Prompt, ReviewProgressHandoffFilename) || !strings.Contains(in.Prompt, canonicalPath) {
					t.Fatalf("continuation prompt missing handoff or canonical path:\n%s", in.Prompt)
				}
				writeHelperHandoff(t, tmpDir, ReviewProgressHandoffFilename, validReviewProgressHandoff("COMPLETE", "checked report and tests"))
				if err := os.WriteFile(filepath.Join(tmpDir, PhaseCompleteFile), []byte("complete\n"), 0o644); err != nil {
					t.Fatalf("write phase_complete: %v", err)
				}
			default:
				t.Fatalf("unexpected continuation %d", in.Continuation)
			}
			return helperContinuationRunResult{Status: agentStatusSuccess}, nil
		},
	})
	if err != nil {
		t.Fatalf("runHelperWithContinuations() error = %v", err)
	}
	if result.Iterations != 2 || len(prompts) != 2 {
		t.Fatalf("iterations/prompts = %d/%d, want 2/2", result.Iterations, len(prompts))
	}
	if parentCounter != 41 {
		t.Fatalf("parentCounter = %d, want untouched 41", parentCounter)
	}
}

func TestRunHelperWithContinuations_CleanFinishWithoutScratch(t *testing.T) {
	tmpDir := t.TempDir()
	calls := 0
	result, err := runHelperWithContinuations(context.Background(), helperContinuationConfig{
		Label:                "fix helper",
		SessionIDBase:        "fix-01",
		HandoffPath:          filepath.Join(tmpDir, ProducerProgressHandoffFilename),
		ParseHandoff:         ParseProducerProgressHandoffMd,
		Fingerprint:          ProducerProgressHandoffFingerprint,
		MaxConsecNoProgress:  3,
		MaxConsecMalformed:   3,
		ContinuationSkill:    "final-fix",
		ContinuationArtifact: ProducerProgressHandoffFilename,
		RunSession: func(context.Context, helperContinuationRunInput) (helperContinuationRunResult, error) {
			calls++
			return helperContinuationRunResult{Status: agentStatusSuccess}, nil
		},
	})
	if err != nil {
		t.Fatalf("runHelperWithContinuations() error = %v", err)
	}
	if result.Iterations != 1 || calls != 1 {
		t.Fatalf("iterations/calls = %d/%d, want 1/1", result.Iterations, calls)
	}
}

func TestRunHelperWithContinuationsSafetyRails(t *testing.T) {
	t.Run("no progress", func(t *testing.T) {
		tmpDir := t.TempDir()
		calls := 0
		_, err := runHelperWithContinuations(context.Background(), helperContinuationConfig{
			Label:                "review helper",
			SessionIDBase:        "review-rail",
			HandoffPath:          filepath.Join(tmpDir, ReviewProgressHandoffFilename),
			ParseHandoff:         ParseReviewProgressHandoffMd,
			Fingerprint:          ReviewProgressHandoffFingerprint,
			MaxConsecNoProgress:  1,
			MaxConsecMalformed:   3,
			ContinuationSkill:    "review-implementation",
			ContinuationArtifact: ReviewProgressHandoffFilename,
			RunSession: func(context.Context, helperContinuationRunInput) (helperContinuationRunResult, error) {
				calls++
				writeHelperHandoff(t, tmpDir, ReviewProgressHandoffFilename, validReviewProgressHandoff("CONTINUE", "same work"))
				return helperContinuationRunResult{Status: agentStatusSuccess}, nil
			},
		})
		if err == nil || !isHelperContinuationSafetyRailError(err) {
			t.Fatalf("error = %T %v, want helper continuation safety rail", err, err)
		}
		if calls != 2 {
			t.Fatalf("calls = %d, want 2", calls)
		}
	})

	t.Run("malformed", func(t *testing.T) {
		tmpDir := t.TempDir()
		var prompts []string
		_, err := runHelperWithContinuations(context.Background(), helperContinuationConfig{
			Label:                "review helper",
			SessionIDBase:        "review-malformed",
			HandoffPath:          filepath.Join(tmpDir, ReviewProgressHandoffFilename),
			ParseHandoff:         ParseReviewProgressHandoffMd,
			Fingerprint:          ReviewProgressHandoffFingerprint,
			MaxConsecNoProgress:  3,
			MaxConsecMalformed:   2,
			ContinuationSkill:    "review-implementation",
			ContinuationArtifact: ReviewProgressHandoffFilename,
			RunSession: func(_ context.Context, in helperContinuationRunInput) (helperContinuationRunResult, error) {
				prompts = append(prompts, in.Prompt)
				writeHelperHandoff(t, tmpDir, ReviewProgressHandoffFilename, validReviewProgressHandoff("INVALID", "same work"))
				return helperContinuationRunResult{Status: agentStatusSuccess}, nil
			},
		})
		if err == nil || !isHelperContinuationSafetyRailError(err) {
			t.Fatalf("error = %T %v, want helper continuation safety rail", err, err)
		}
		if len(prompts) != 2 || !strings.Contains(prompts[1], "did not satisfy the continuation contract") {
			t.Fatalf("repair prompt not issued after malformed handoff: %#v", prompts)
		}
	})

	t.Run("binding artifact on continue", func(t *testing.T) {
		tmpDir := t.TempDir()
		forbiddenPath := filepath.Join(tmpDir, "review-feedback.md")
		var prompts []string
		result, err := runHelperWithContinuations(context.Background(), helperContinuationConfig{
			Label:                "review helper",
			SessionIDBase:        "review-binding",
			HandoffPath:          filepath.Join(tmpDir, ReviewProgressHandoffFilename),
			ParseHandoff:         ParseReviewProgressHandoffMd,
			Fingerprint:          ReviewProgressHandoffFingerprint,
			MaxConsecNoProgress:  3,
			MaxConsecMalformed:   3,
			ContinuationSkill:    "review-implementation",
			ContinuationArtifact: ReviewProgressHandoffFilename,
			ForbiddenOnContinue:  []string{forbiddenPath},
			RunSession: func(_ context.Context, in helperContinuationRunInput) (helperContinuationRunResult, error) {
				prompts = append(prompts, in.Prompt)
				switch in.Continuation {
				case 0:
					writeHelperHandoff(t, tmpDir, ReviewProgressHandoffFilename, validReviewProgressHandoff("CONTINUE", "checked report"))
					if err := os.WriteFile(forbiddenPath, []byte("# Review Feedback\n"), 0o644); err != nil {
						t.Fatalf("write forbidden artifact: %v", err)
					}
				case 1:
					if !strings.Contains(in.Prompt, "binding artifact") {
						t.Fatalf("repair prompt missing binding artifact violation:\n%s", in.Prompt)
					}
					if err := os.Remove(forbiddenPath); err != nil {
						t.Fatalf("remove forbidden artifact: %v", err)
					}
					writeHelperHandoff(t, tmpDir, ReviewProgressHandoffFilename, validReviewProgressHandoff("COMPLETE", "checked report"))
				default:
					t.Fatalf("unexpected continuation %d", in.Continuation)
				}
				return helperContinuationRunResult{Status: agentStatusSuccess}, nil
			},
		})
		if err != nil {
			t.Fatalf("runHelperWithContinuations() error = %v", err)
		}
		if result.Iterations != 2 || len(prompts) != 2 {
			t.Fatalf("iterations/prompts = %d/%d, want 2/2", result.Iterations, len(prompts))
		}
	})
}

func validReviewProgressHandoff(state, examined string) string {
	return fmt.Sprintf(`# Review Progress

## Examined Work
- %s

## Advisory Findings
- Finding A

## Where I Stopped
Continue with the next check.

## Handoff State
%s
`, examined, state)
}

func validProducerProgressHandoff(state, completed string) string {
	return fmt.Sprintf(`# Producer Progress

## Completed Fix Work
- %s

## Remaining Fix Work
- none

## Where I Stopped
At verification.

## Handoff State
%s
`, completed, state)
}

func validResearchProgressHandoff(state, completed string) string {
	return fmt.Sprintf(`# Research Progress

## Completed Findings
- %s

## Remaining Areas
- none

## Where I Stopped
Continue with the next research question.

## Gotchas
- no blockers

## Handoff State
%s
`, completed, state)
}

func validInquireProgressHandoff(state, clarified string) string {
	return fmt.Sprintf(`# Inquire Progress

## Clarified Requirements
- %s

## Open Questions
- none

## Where I Stopped
Continue with the next requirement.

## Gotchas
- no blockers

## Handoff State
%s
`, clarified, state)
}

func validDesignProgressHandoff(state, decisions string) string {
	return fmt.Sprintf(`# Design Progress

## Decisions Made
- %s

## Open Design Areas
- none

## Where I Stopped
Continue with the next design area.

## Gotchas
- no blockers

## Handoff State
%s
`, decisions, state)
}

func validKBProgressHandoff(state, completed string) string {
	return fmt.Sprintf(`# Knowledge Base Progress

## Completed Categories
- %s

## Remaining Categories
- none

## Where I Stopped
Continue with the next category.

## Gotchas
- no blockers

## Handoff State
%s
`, completed, state)
}

func writeHelperHandoff(t testing.TB, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", dir, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	return path
}

func extractFirstMarkdownFence(t testing.TB, body string) string {
	t.Helper()
	start := strings.Index(body, "```markdown\n")
	if start < 0 {
		t.Fatal("missing markdown fence")
	}
	start += len("```markdown\n")
	end := strings.Index(body[start:], "\n```")
	if end < 0 {
		t.Fatal("unterminated markdown fence")
	}
	return body[start : start+end]
}

func extractMarkdownFenceContaining(t testing.TB, body, want string) string {
	t.Helper()
	remaining := body
	for {
		start := strings.Index(remaining, "```markdown\n")
		if start < 0 {
			t.Fatalf("missing markdown fence containing %q", want)
		}
		start += len("```markdown\n")
		end := strings.Index(remaining[start:], "\n```")
		if end < 0 {
			t.Fatal("unterminated markdown fence")
		}
		fence := remaining[start : start+end]
		if strings.Contains(fence, want) {
			return fence
		}
		remaining = remaining[start+end+len("\n```"):]
	}
}

func containsViolation(violations []string, want string) bool {
	for _, violation := range violations {
		if strings.Contains(violation, want) {
			return true
		}
	}
	return false
}
