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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"gopkg.in/yaml.v3"
)

// roleContract declares the artifacts a role must emit before requesting a
// completion commit.
type roleContract struct {
	Role     Role
	Required []requiredArtifact
}

// requiredArtifact describes one artifact in a role's completion contract.
type requiredArtifact struct {
	Name          string
	DisplayPath   string
	HideFromSkill bool
	ResolvePath   func(iterDir string) string
	Validate      func(iterDir, path string, out *Outcome) ([]ProtocolViolation, error)
}

// ProtocolViolation describes one deterministic contract violation.
type ProtocolViolation struct {
	Artifact string
	Reason   string
}

// Outcome carries parsed artifacts from a successful contract validation.
type Outcome struct {
	OK                bool
	ReviewFeedback    *parsedReviewFeedback
	RoadmapPhases     []RoadmapPhase
	PlanAttemptMeta   *PlanAttemptMeta
	PlanMarkdownPath  string
	PhaseArtifactPath string
}

// lookup returns the registered contract for a phase and role.
func lookup(phase feature.Phase, role Role) (roleContract, bool) {
	if spec, ok := lookupRoleSpec(phase, role); ok {
		return spec.contract(), true
	}
	return roleContract{}, false
}

// Validate checks the registered contract for a phase and role.
//
// Validators receive only iterDir. The registry can later accept richer
// context for plan-path, repo, state-dir, axis, and helper-dir resolution.
func Validate(phase feature.Phase, role Role, iterDir string) (Outcome, []ProtocolViolation, error) {
	contract, ok := lookup(phase, role)
	if !ok {
		return Outcome{}, []ProtocolViolation{{Artifact: string(role), Reason: "no contract registered"}}, nil
	}
	return validateContractArtifacts(contract, iterDir, true)
}

func validateContractArtifacts(contract roleContract, iterDir string, includeHidden bool) (Outcome, []ProtocolViolation, error) {
	out := Outcome{OK: true}
	var violations []ProtocolViolation
	for _, artifact := range contract.Required {
		if artifact.HideFromSkill && !includeHidden {
			continue
		}
		path := artifact.ResolvePath(iterDir)
		v, err := artifact.Validate(iterDir, path, &out)
		if err != nil {
			return out, nil, err
		}
		violations = append(violations, v...)
	}
	out.OK = len(violations) == 0
	return out, violations, nil
}

// ValidateArtifactsPreflight runs cheap artifact checks intended for agents to
// execute before emitting a structured outcome. It is intentionally stricter
// than Validate for YAML syntax so self-checks catch malformed reports even
// when the harness would tolerate a blocking CHANGES_REQUESTED verdict to
// route a fix.
func ValidateArtifactsPreflight(phase feature.Phase, role Role, iterDir string) (Outcome, []ProtocolViolation, error) {
	contract, ok := lookup(phase, role)
	if !ok {
		return Outcome{}, []ProtocolViolation{{Artifact: string(role), Reason: "no contract registered"}}, nil
	}
	var violations []ProtocolViolation
	for _, artifact := range contract.Required {
		if artifact.HideFromSkill {
			continue
		}
		path := artifact.ResolvePath(iterDir)
		violations = append(violations, validateYAMLArtifactSyntax(artifact.DisplayPath, path)...)
	}
	if len(violations) > 0 {
		return Outcome{OK: false}, violations, nil
	}
	out, contractViolations, err := validateContractArtifacts(contract, iterDir, false)
	if err != nil {
		return out, nil, err
	}
	violations = append(violations, contractViolations...)
	out.OK = len(violations) == 0
	return out, violations, nil
}

func validateYAMLArtifactSyntax(displayPath, path string) []ProtocolViolation {
	if !isYAMLArtifact(displayPath, path) {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []ProtocolViolation{{Artifact: displayPath, Reason: missingArtifactReason(displayPath, filepath.Dir(path))}}
		}
		return []ProtocolViolation{{Artifact: displayPath, Reason: fmt.Sprintf("reading %s: %v", displayPath, err)}}
	}
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return []ProtocolViolation{{Artifact: displayPath, Reason: fmt.Sprintf("%s is unparseable: %v", displayPath, err)}}
	}
	return nil
}

func isYAMLArtifact(displayPath, path string) bool {
	for _, value := range []string{displayPath, path} {
		switch strings.ToLower(filepath.Ext(value)) {
		case ".yaml", ".yml":
			return true
		}
	}
	return false
}

func phasePlanArtifactDir(attemptDir string) string {
	return filepath.Dir(attemptDir)
}

// missingArtifactReason builds a "missing artifact" reason that names the
// directory the validator looked in. Spelling out the expected directory
// turns "X is missing" into actionable feedback the next attempt can act
// on without having to guess paths.
func missingArtifactReason(artifactLabel, expectedDir string) string {
	if expectedDir == "" {
		return fmt.Sprintf("%s is missing", artifactLabel)
	}
	return fmt.Sprintf("%s is missing — expected at %s/", artifactLabel, expectedDir)
}

func validateRoadmapArtifact(_ roleArtifactSpec, iterDir string, path string, out *Outcome) ([]ProtocolViolation, error) {
	if path == "" {
		return []ProtocolViolation{{Artifact: "roadmap markdown", Reason: missingArtifactReason("roadmap markdown", filepath.Dir(iterDir))}}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []ProtocolViolation{{Artifact: "roadmap markdown", Reason: missingArtifactReason("roadmap markdown", filepath.Dir(iterDir))}}, nil
		}
		return nil, fmt.Errorf("reading roadmap markdown: %w", err)
	}
	phases, err := ParseRoadmap(string(data))
	if err != nil {
		return []ProtocolViolation{{Artifact: "roadmap markdown", Reason: fmt.Sprintf("roadmap markdown is unparseable: %v", err)}}, nil
	}
	out.RoadmapPhases = phases
	return nil, nil
}

func validatePlanAttemptMetaArtifact(_ roleArtifactSpec, _ string, path string, out *Outcome) ([]ProtocolViolation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []ProtocolViolation{{Artifact: "meta.yaml", Reason: missingArtifactReason("meta.yaml", filepath.Dir(path))}}, nil
		}
		return nil, fmt.Errorf("reading plan attempt meta: %w", err)
	}
	var meta PlanAttemptMeta
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return []ProtocolViolation{{Artifact: "meta.yaml", Reason: fmt.Sprintf("meta.yaml is unparseable: %v", err)}}, nil
	}
	out.PlanAttemptMeta = &meta
	return nil, nil
}

func validatePhasePlanMarkdownArtifact(_ roleArtifactSpec, iterDir string, path string, _ *Outcome) ([]ProtocolViolation, error) {
	if path == "" {
		return []ProtocolViolation{{Artifact: "phase plan markdown", Reason: missingArtifactReason("phase plan markdown", phasePlanArtifactDir(iterDir))}}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []ProtocolViolation{{Artifact: "phase plan markdown", Reason: missingArtifactReason("phase plan markdown", phasePlanArtifactDir(iterDir))}}, nil
		}
		return nil, fmt.Errorf("reading phase plan markdown: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return []ProtocolViolation{{Artifact: "phase plan markdown", Reason: "phase plan markdown is empty"}}, nil
	}
	return nil, nil
}

func newestPhaseMarkdownArtifact(dir string) string {
	var bestPath string
	var bestModTime int64

	matches, _ := filepath.Glob(filepath.Join(dir, "*.md"))
	for _, path := range matches {
		if IsArtifactExcluded(filepath.Base(path)) {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		if mt := info.ModTime().UnixNano(); bestPath == "" || mt > bestModTime {
			bestPath = path
			bestModTime = mt
		}
	}
	return bestPath
}

func validateReviewFeedbackArtifact(artifact roleArtifactSpec, _ string, path string, out *Outcome) ([]ProtocolViolation, error) {
	parsed, err := parseReviewFeedback(path)
	if err != nil {
		return nil, err
	}
	out.ReviewFeedback = parsed
	if parsed == nil || parsed.ok() {
		return nil, nil
	}
	violations := make([]ProtocolViolation, 0, len(parsed.ProtocolViolations))
	for _, reason := range parsed.ProtocolViolations {
		violations = append(violations, ProtocolViolation{Artifact: artifact.DisplayPath, Reason: reason})
	}
	return violations, nil
}

type protocolViolationError struct {
	msg        string
	violations []ProtocolViolation
}

func (e *protocolViolationError) Error() string { return e.msg }

func newProtocolViolationError(role Role, iterDir string, violations []ProtocolViolation) error {
	return &protocolViolationError{
		msg:        formatProtocolViolationError(role, iterDir, violations),
		violations: append([]ProtocolViolation(nil), violations...),
	}
}

func isProtocolViolationError(err error) bool {
	var target *protocolViolationError
	return errors.As(err, &target)
}

func protocolViolationsFromError(err error) []ProtocolViolation {
	var target *protocolViolationError
	if !errors.As(err, &target) {
		return nil
	}
	return append([]ProtocolViolation(nil), target.violations...)
}

// joinProtocolViolations renders protocol violations for LastError and review
// feedback text.
func joinProtocolViolations(violations []ProtocolViolation) string {
	parts := make([]string, 0, len(violations))
	for _, v := range violations {
		if v.Artifact == "" {
			parts = append(parts, v.Reason)
			continue
		}
		parts = append(parts, v.Artifact+": "+v.Reason)
	}
	return strings.Join(parts, "; ")
}

// FormatSingleShotProtocolViolationError renders the canonical protocol violation LastError.
func FormatSingleShotProtocolViolationError(role Role, dir string, violations []ProtocolViolation) string {
	if dir == "" {
		dir = "<unresolved>"
	}
	reason := joinProtocolViolations(violations)
	if strings.TrimSpace(reason) == "" {
		reason = "contract validation failed"
	}
	return fmt.Sprintf("protocol violation: %s @ %s: %s", role, dir, reason)
}
