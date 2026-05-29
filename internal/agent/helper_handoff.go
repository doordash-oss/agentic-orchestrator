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
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	// ReviewProgressHandoffFilename is the rolling scratch artifact used by
	// verdict-style review helpers during Smart Zone continuations.
	ReviewProgressHandoffFilename = "review-progress.md"
	// ProducerProgressHandoffFilename is the rolling scratch artifact used by
	// producer-style helpers during Smart Zone continuations.
	ProducerProgressHandoffFilename = "producer-progress.md"
	// InquireProgressHandoffFilename is the rolling scratch artifact used by
	// Inquire's blocking loop during Smart Zone continuations.
	InquireProgressHandoffFilename = "inquire-progress.md"
	// ResearchProgressHandoffFilename is the rolling scratch artifact used by
	// Research's blocking loop during Smart Zone continuations.
	ResearchProgressHandoffFilename = "research-progress.md"
	// DesignProgressHandoffFilename is the rolling scratch artifact used by
	// Design's blocking loop during Smart Zone continuations.
	DesignProgressHandoffFilename = "design-progress.md"
)

// HelperHandoffState is the continuation routing token parsed from helper
// Smart Zone handoff artifacts.
type HelperHandoffState int

const (
	HelperHandoffInvalid HelperHandoffState = iota
	HelperHandoffContinue
	HelperHandoffComplete
)

func (s HelperHandoffState) String() string {
	switch s {
	case HelperHandoffContinue:
		return "CONTINUE"
	case HelperHandoffComplete:
		return "COMPLETE"
	default:
		return "INVALID"
	}
}

// ParsedHelperHandoff is the deterministic parse result for helper-level
// Smart Zone handoff artifacts.
type ParsedHelperHandoff struct {
	State              HelperHandoffState
	ProgressRegion     string
	ProtocolViolations []string
}

// OK reports whether the handoff satisfies the helper continuation contract.
func (p *ParsedHelperHandoff) OK() bool {
	return p != nil && p.State != HelperHandoffInvalid && len(p.ProtocolViolations) == 0
}

var (
	reviewProgressHandoffSections = []string{
		"## Examined Work",
		"## Advisory Findings",
		"## Where I Stopped",
		"## Handoff State",
	}
	producerProgressHandoffSections = []string{
		"## Completed Fix Work",
		"## Remaining Fix Work",
		"## Where I Stopped",
		"## Handoff State",
	}
	inquireProgressHandoffSections = []string{
		"## Clarified Requirements",
		"## Open Questions",
		"## Where I Stopped",
		"## Gotchas",
		"## Handoff State",
	}
	researchProgressHandoffSections = []string{
		"## Completed Findings",
		"## Remaining Areas",
		"## Where I Stopped",
		"## Gotchas",
		"## Handoff State",
	}
	designProgressHandoffSections = []string{
		"## Decisions Made",
		"## Open Design Areas",
		"## Where I Stopped",
		"## Gotchas",
		"## Handoff State",
	}
	validHelperHandoffStateTokens = map[string]HelperHandoffState{
		"CONTINUE": HelperHandoffContinue,
		"COMPLETE": HelperHandoffComplete,
	}
)

// ParseReviewProgressHandoffMd parses review-progress.md.
func ParseReviewProgressHandoffMd(path string) (*ParsedHelperHandoff, error) {
	return parseHelperHandoffMd(path, ReviewProgressHandoffFilename, reviewProgressHandoffSections)
}

// ParseProducerProgressHandoffMd parses producer-progress.md.
func ParseProducerProgressHandoffMd(path string) (*ParsedHelperHandoff, error) {
	return parseHelperHandoffMd(path, ProducerProgressHandoffFilename, producerProgressHandoffSections)
}

// ParseInquireProgressHandoffMd parses inquire-progress.md.
func ParseInquireProgressHandoffMd(path string) (*ParsedHelperHandoff, error) {
	return parseHelperHandoffMd(path, InquireProgressHandoffFilename, inquireProgressHandoffSections)
}

// ParseResearchProgressHandoffMd parses research-progress.md.
func ParseResearchProgressHandoffMd(path string) (*ParsedHelperHandoff, error) {
	return parseHelperHandoffMd(path, ResearchProgressHandoffFilename, researchProgressHandoffSections)
}

// ParseDesignProgressHandoffMd parses design-progress.md.
func ParseDesignProgressHandoffMd(path string) (*ParsedHelperHandoff, error) {
	return parseHelperHandoffMd(path, DesignProgressHandoffFilename, designProgressHandoffSections)
}

func parseHelperHandoffMd(path, filename string, requiredSections []string) (*ParsedHelperHandoff, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("parse %s: empty path", filename)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ParsedHelperHandoff{
				ProtocolViolations: []string{
					fmt.Sprintf("%s not found at %s", filename, path),
				},
			}, nil
		}
		return nil, fmt.Errorf("reading %s at %s: %w", filename, path, err)
	}

	body := string(data)
	parsed := &ParsedHelperHandoff{}

	positions := findSectionHeadings(body, requiredSections)
	lastPos := -1
	for _, heading := range requiredSections {
		pos, ok := positions[heading]
		if !ok {
			parsed.ProtocolViolations = append(parsed.ProtocolViolations,
				fmt.Sprintf("%s missing required section %q", filename, heading))
			continue
		}
		if pos < lastPos {
			parsed.ProtocolViolations = append(parsed.ProtocolViolations,
				fmt.Sprintf("%s section %q appears out of order; required order is: %s",
					filename, heading, strings.Join(requiredSections, ", ")))
		}
		lastPos = pos
	}

	if stateBody := extractMarkdownSection(body, "## Handoff State"); stateBody != "" {
		token, _ := splitStateTokenAndNote(stateBody)
		if state, ok := validHelperHandoffStateTokens[token]; ok {
			parsed.State = state
		} else {
			parsed.ProtocolViolations = append(parsed.ProtocolViolations, fmt.Sprintf(
				"%s `## Handoff State` body must be exactly one of {CONTINUE, COMPLETE} on its own line; got %q",
				filename, token))
		}
	} else if _, ok := positions["## Handoff State"]; ok {
		parsed.ProtocolViolations = append(parsed.ProtocolViolations,
			fmt.Sprintf("%s `## Handoff State` section is empty; emit CONTINUE or COMPLETE", filename))
	}

	var progress []string
	for _, heading := range requiredSections {
		if heading == "## Handoff State" {
			continue
		}
		if section := extractMarkdownSection(body, heading); section != "" {
			progress = append(progress, heading+"\n"+strings.TrimSpace(section))
		}
	}
	parsed.ProgressRegion = strings.TrimSpace(strings.Join(progress, "\n\n"))
	return parsed, nil
}

// ReviewProgressHandoffFingerprint computes a stable fingerprint over the
// review helper's advisory progress narrative.
func ReviewProgressHandoffFingerprint(path string) (string, error) {
	return helperHandoffFingerprint(path, ParseReviewProgressHandoffMd)
}

// ProducerProgressHandoffFingerprint computes a stable fingerprint over the
// producer helper's progress narrative.
func ProducerProgressHandoffFingerprint(path string) (string, error) {
	return helperHandoffFingerprint(path, ParseProducerProgressHandoffMd)
}

// InquireProgressHandoffFingerprint computes a stable fingerprint over the
// inquiry loop's progress narrative.
func InquireProgressHandoffFingerprint(path string) (string, error) {
	return helperHandoffFingerprint(path, ParseInquireProgressHandoffMd)
}

// ResearchProgressHandoffFingerprint computes a stable fingerprint over the
// research loop's progress narrative.
func ResearchProgressHandoffFingerprint(path string) (string, error) {
	return helperHandoffFingerprint(path, ParseResearchProgressHandoffMd)
}

// DesignProgressHandoffFingerprint computes a stable fingerprint over the
// design loop's progress narrative.
func DesignProgressHandoffFingerprint(path string) (string, error) {
	return helperHandoffFingerprint(path, ParseDesignProgressHandoffMd)
}

func helperHandoffFingerprint(path string, parse func(string) (*ParsedHelperHandoff, error)) (string, error) {
	parsed, err := parse(path)
	if err != nil {
		return "", err
	}
	if parsed == nil {
		return "", nil
	}
	region := normalizeHelperHandoffProgressRegion(parsed.ProgressRegion)
	h := sha256.Sum256([]byte(region))
	return fmt.Sprintf("%x", h), nil
}

var (
	helperAbsolutePathRE = regexp.MustCompile(`(?:/[^/\s` + "`" + `]+)+`)
	helperIterationDirRE = regexp.MustCompile(`iteration-\d+`)
	helperAttemptDirRE   = regexp.MustCompile(`attempt-\d+`)
	helperRunDirRE       = regexp.MustCompile(`run-\d+`)
	helperPhaseDirRE     = regexp.MustCompile(`phase-\d+`)
	helperTimestampRE    = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})?`)
)

func normalizeHelperHandoffProgressRegion(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = helperAbsolutePathRE.ReplaceAllString(s, "<path>")
	s = helperIterationDirRE.ReplaceAllString(s, "iteration-NN")
	s = helperAttemptDirRE.ReplaceAllString(s, "attempt-NN")
	s = helperRunDirRE.ReplaceAllString(s, "run-NNN")
	s = helperPhaseDirRE.ReplaceAllString(s, "phase-NN")
	s = helperTimestampRE.ReplaceAllString(s, "<timestamp>")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

type helperContinuationRunInput struct {
	Continuation int
	SessionID    string
	Prompt       string
}

type helperContinuationRunResult struct {
	Status  string
	Handoff contextSnapshot
}

type helperContinuationResult struct {
	Status     string
	Handoff    contextSnapshot
	Iterations int
}

type helperContinuationConfig struct {
	Label                string
	SessionIDBase        string
	HandoffPath          string
	CanonicalPaths       []string
	ParseHandoff         func(string) (*ParsedHelperHandoff, error)
	Fingerprint          func(string) (string, error)
	MaxConsecNoProgress  int
	MaxConsecMalformed   int
	ContinuationSkill    string
	ContinuationArtifact string
	ForbiddenOnContinue  []string
	RunSession           func(context.Context, helperContinuationRunInput) (helperContinuationRunResult, error)
}

type helperContinuationSafetyRailError struct {
	message string
}

func (e helperContinuationSafetyRailError) Error() string {
	return e.message
}

func isHelperContinuationSafetyRailError(err error) bool {
	var target helperContinuationSafetyRailError
	return errors.As(err, &target)
}

func runHelperWithContinuations(ctx context.Context, cfg helperContinuationConfig) (helperContinuationResult, error) {
	if cfg.SessionIDBase == "" {
		return helperContinuationResult{}, fmt.Errorf("running helper continuation: missing session id base")
	}
	if strings.TrimSpace(cfg.HandoffPath) == "" {
		return helperContinuationResult{}, fmt.Errorf("running helper continuation: missing handoff path")
	}
	if cfg.ParseHandoff == nil {
		return helperContinuationResult{}, fmt.Errorf("running helper continuation: missing handoff parser")
	}
	if cfg.Fingerprint == nil {
		return helperContinuationResult{}, fmt.Errorf("running helper continuation: missing handoff fingerprint")
	}
	if cfg.RunSession == nil {
		return helperContinuationResult{}, fmt.Errorf("running helper continuation: missing session runner")
	}
	label := cfg.Label
	if label == "" {
		label = "helper"
	}
	maxNoProgress := cfg.MaxConsecNoProgress
	if maxNoProgress <= 0 {
		maxNoProgress = defaultPlanningMaxConsecutiveNoProgress
	}
	maxMalformed := cfg.MaxConsecMalformed
	if maxMalformed <= 0 {
		maxMalformed = defaultPlanningMaxConsecutiveFailures
	}
	artifact := cfg.ContinuationArtifact
	if artifact == "" {
		artifact = filepath.Base(cfg.HandoffPath)
	}

	_ = os.Remove(cfg.HandoffPath)
	tracker := NewProgressTracker()
	consecutiveMalformed := 0
	var nextPrompt string
	var lastHandoff contextSnapshot
	for continuation := 0; ; continuation++ {
		RemovePhaseComplete(filepath.Dir(cfg.HandoffPath))
		sessionID := cfg.SessionIDBase
		if continuation > 0 {
			sessionID = fmt.Sprintf("%s-c%02d", cfg.SessionIDBase, continuation+1)
		}
		runResult, err := cfg.RunSession(ctx, helperContinuationRunInput{
			Continuation: continuation,
			SessionID:    sessionID,
			Prompt:       nextPrompt,
		})
		if err != nil {
			return helperContinuationResult{Status: runResult.Status, Handoff: runResult.Handoff, Iterations: continuation + 1}, err
		}
		if runResult.Handoff.ThresholdTokens > 0 {
			lastHandoff = runResult.Handoff
		}
		if runResult.Status != "" && runResult.Status != agentStatusSuccess {
			return helperContinuationResult{Status: runResult.Status, Handoff: lastHandoff, Iterations: continuation + 1}, nil
		}

		if _, err := os.Stat(cfg.HandoffPath); err != nil {
			if os.IsNotExist(err) {
				return helperContinuationResult{Status: agentStatusSuccess, Handoff: lastHandoff, Iterations: continuation + 1}, nil
			}
			return helperContinuationResult{}, fmt.Errorf("stat %s: %w", artifact, err)
		}
		parsed, err := cfg.ParseHandoff(cfg.HandoffPath)
		if err != nil {
			return helperContinuationResult{}, err
		}
		if !parsed.OK() {
			consecutiveMalformed++
			if consecutiveMalformed >= maxMalformed {
				return helperContinuationResult{Status: planningAgentStatusSafetyRail, Handoff: lastHandoff, Iterations: continuation + 1}, helperContinuationSafetyRailError{
					message: fmt.Sprintf("%s handoff protocol violation repeated %d consecutive times: %s", label, consecutiveMalformed, strings.Join(parsed.ProtocolViolations, "; ")),
				}
			}
			nextPrompt = buildHelperHandoffRepairPrompt(cfg.HandoffPath, cfg.CanonicalPaths, parsed.ProtocolViolations, artifact)
			continue
		}
		consecutiveMalformed = 0
		if parsed.State == HelperHandoffComplete {
			return helperContinuationResult{Status: agentStatusSuccess, Handoff: lastHandoff, Iterations: continuation + 1}, nil
		}
		if violations := helperContinueForbiddenViolations(cfg.ForbiddenOnContinue, artifact); len(violations) > 0 {
			consecutiveMalformed++
			if consecutiveMalformed >= maxMalformed {
				return helperContinuationResult{Status: planningAgentStatusSafetyRail, Handoff: lastHandoff, Iterations: continuation + 1}, helperContinuationSafetyRailError{
					message: fmt.Sprintf("%s handoff protocol violation repeated %d consecutive times: %s", label, consecutiveMalformed, strings.Join(violations, "; ")),
				}
			}
			nextPrompt = buildHelperHandoffRepairPrompt(cfg.HandoffPath, cfg.CanonicalPaths, violations, artifact)
			continue
		}

		progressMade, err := tracker.CheckWithFingerprint(cfg.HandoffPath, cfg.Fingerprint)
		if err != nil {
			return helperContinuationResult{}, err
		}
		if !progressMade && tracker.NoProgressCount() >= maxNoProgress {
			return helperContinuationResult{Status: planningAgentStatusSafetyRail, Handoff: lastHandoff, Iterations: continuation + 1}, helperContinuationSafetyRailError{
				message: fmt.Sprintf("%s handoff made no progress for %d consecutive continuations", label, tracker.NoProgressCount()),
			}
		}
		nextPrompt = buildHelperContinuationPrompt(cfg.HandoffPath, cfg.CanonicalPaths, cfg.ContinuationSkill, artifact)
	}
}

func helperContinueForbiddenViolations(paths []string, artifact string) []string {
	var violations []string
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			violations = append(violations, fmt.Sprintf("%s CONTINUE must not be accompanied by binding artifact %s", artifact, path))
		}
	}
	return violations
}

func buildHelperContinuationPrompt(handoffPath string, canonicalPaths []string, skill, artifact string) string {
	var b strings.Builder
	b.WriteString("# Helper Smart Zone Continuation\n\n")
	b.WriteString("A previous helper agent wound down inside this same parent iteration. Continue the same helper role with a fresh session; do not advance the parent iteration counter, flip review/fix ordering, or treat advisory handoff notes as a binding verdict.\n\n")
	b.WriteString("Read the rolling handoff scratch first:\n")
	fmt.Fprintf(&b, "- `%s`\n\n", handoffPath)
	if len(canonicalPaths) > 0 {
		b.WriteString("Then read the canonical artifacts already in place:\n")
		for _, path := range canonicalPaths {
			if strings.TrimSpace(path) != "" {
				fmt.Fprintf(&b, "- `%s`\n", path)
			}
		}
		b.WriteString("\n")
	}
	if skill != "" {
		fmt.Fprintf(&b, "Continue following `skills/%s/HANDOFF.md` if another wind-down is needed.\n\n", skill)
	}
	fmt.Fprintf(&b, "Resume from `## Where I Stopped`. When you need another Smart Zone continuation, overwrite `%s` with `CONTINUE`; when this helper is ready for its normal validation path, overwrite `%s` with `COMPLETE`. Touch `phase_complete` last.", artifact, artifact)
	return b.String()
}

func buildHelperHandoffRepairPrompt(handoffPath string, canonicalPaths []string, violations []string, artifact string) string {
	var b strings.Builder
	b.WriteString("# Helper Smart Zone Continuation Repair\n\n")
	b.WriteString("The previous helper handoff did not satisfy the continuation contract. Stay inside the same parent iteration; do not advance counters or discard canonical artifacts.\n\n")
	b.WriteString("Fix these handoff contract violations:\n")
	if len(violations) == 0 {
		fmt.Fprintf(&b, "- %s did not satisfy the continuation contract\n", artifact)
	} else {
		for _, violation := range violations {
			fmt.Fprintf(&b, "- %s\n", violation)
		}
	}
	b.WriteString("\nRead the current rolling handoff scratch:\n")
	fmt.Fprintf(&b, "- `%s`\n\n", handoffPath)
	if len(canonicalPaths) > 0 {
		b.WriteString("Then read the canonical artifacts already in place:\n")
		for _, path := range canonicalPaths {
			if strings.TrimSpace(path) != "" {
				fmt.Fprintf(&b, "- `%s`\n", path)
			}
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Overwrite `%s` with the required sections and a `CONTINUE` or `COMPLETE` state. Touch `phase_complete` last.", artifact)
	return b.String()
}
