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

// RoleContract declares the artifacts a role must emit after phase_complete.
type RoleContract struct {
	Role        Role
	Required    []RequiredArtifact
	Optional    []OptionalArtifact
	Conditional []ConditionalArtifact
	NoOp        bool
	NoOpReason  string
}

// RequiredArtifact describes one artifact in a role's completion contract.
type RequiredArtifact struct {
	Name        string
	DisplayPath string
	ResolvePath func(iterDir string) string
	Validate    func(iterDir, path string, out *Outcome) ([]ProtocolViolation, error)
}

// OptionalArtifact describes an artifact that is valid for a role but not
// required. If present, it must parse cleanly.
type OptionalArtifact struct {
	Name        string
	DisplayPath string
	ResolvePath func(iterDir string) string
	Validate    func(iterDir, path string, out *Outcome) ([]ProtocolViolation, error)
}

// ConditionalArtifact adds a required artifact when When matches the parsed
// payloads produced by the unconditional artifacts.
type ConditionalArtifact struct {
	Name     string
	When     func(Outcome) bool
	Artifact RequiredArtifact
}

// ProtocolViolation describes one deterministic contract violation.
type ProtocolViolation struct {
	Artifact string
	Reason   string
}

// Outcome carries parsed artifacts from a successful contract validation.
type Outcome struct {
	OK                 bool
	Progress           *ParsedProgress
	VerificationReport *VerificationReport
	ReviewFeedback     *ParsedReviewFeedback
	NeedUserInput      *NeedUserInputRecord
	RoadmapPhases      []RoadmapPhase
	PlanAttemptMeta    *PlanAttemptMeta
	PlanMarkdownPath   string
	PhaseArtifactPath  string
	AxisApproval       *AxisApproval
}

// Lookup returns the registered contract for a phase and role.
func Lookup(phase feature.Phase, role Role) (RoleContract, bool) {
	if spec, ok := lookupRoleSpec(phase, role); ok {
		return spec.Contract(), true
	}
	return RoleContract{}, false
}

// Validate checks the registered contract for a phase and role.
//
// STUB(Phase 1): validators receive only iterDir; later phases may introduce
// IterationContext for plan-path, repo, state-dir, axis, and helper-dir
// resolution.
func Validate(phase feature.Phase, role Role, iterDir string) (Outcome, []ProtocolViolation, error) {
	contract, ok := Lookup(phase, role)
	if !ok {
		return Outcome{}, []ProtocolViolation{{Artifact: string(role), Reason: "no contract registered"}}, nil
	}
	if contract.NoOp {
		return Outcome{OK: true}, nil, nil
	}

	out := Outcome{OK: true}
	var violations []ProtocolViolation
	for _, artifact := range contract.Required {
		path := artifact.ResolvePath(iterDir)
		v, err := artifact.Validate(iterDir, path, &out)
		if err != nil {
			return out, nil, err
		}
		violations = append(violations, v...)
	}
	for _, artifact := range contract.Optional {
		path := artifact.ResolvePath(iterDir)
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return out, nil, fmt.Errorf("checking optional artifact %s: %w", artifact.DisplayPath, err)
		}
		v, err := artifact.Validate(iterDir, path, &out)
		if err != nil {
			return out, nil, err
		}
		violations = append(violations, v...)
	}
	for _, conditional := range contract.Conditional {
		if conditional.When == nil || !conditional.When(out) {
			continue
		}
		artifact := conditional.Artifact
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

func phasePlanArtifactDir(attemptDir string) string {
	return filepath.Dir(attemptDir)
}

func planValidatorAxis(iterDir string) (string, bool) {
	axis, ok := strings.CutPrefix(filepath.Base(iterDir), "validate-")
	axis = strings.TrimSpace(axis)
	return axis, ok && axis != ""
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

func validateRoadmapArtifact(iterDir string, path string, out *Outcome) ([]ProtocolViolation, error) {
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

func validatePlanAttemptMetaArtifact(_ string, path string, out *Outcome) ([]ProtocolViolation, error) {
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

func validatePhasePlanMarkdownArtifact(iterDir string, path string, _ *Outcome) ([]ProtocolViolation, error) {
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
	return validatePhasePlanEvidenceContract(string(data)), nil
}

func validatePhasePlanEvidenceContract(planText string) []ProtocolViolation {
	var violations []ProtocolViolation
	success := extractMarkdownSection(planText, "## Success Criteria")
	for _, heading := range []string{"### Visual Evidence", "### Behavioral Evidence"} {
		if !hasMarkdownHeading(success, heading) {
			violations = append(violations, ProtocolViolation{
				Artifact: "phase plan markdown",
				Reason:   fmt.Sprintf("phase plan markdown is missing top-level `%s` under `## Success Criteria`", heading),
			})
			continue
		}
		if reason := validateEvidenceSectionBody(success, heading); reason != "" {
			violations = append(violations, ProtocolViolation{
				Artifact: "phase plan markdown",
				Reason:   reason,
			})
		}
	}
	violations = append(violations, taskScopedEvidenceViolations(planText)...)
	return violations
}

func validateEvidenceSectionBody(successCriteria, heading string) string {
	body := extractMarkdownSection(successCriteria, heading)
	requirements, noneMarkers := countEvidenceChecklistItems(body)
	switch {
	case noneMarkers == 1 && requirements == 0:
		return ""
	case noneMarkers > 0:
		return fmt.Sprintf("phase plan markdown `%s` must contain checklist requirements or exactly one `None required: <reason>` checklist item, not both", heading)
	case requirements > 0:
		return ""
	default:
		return fmt.Sprintf("phase plan markdown `%s` must contain checklist evidence requirements or exactly one `None required: <reason>` checklist item", heading)
	}
}

func countEvidenceChecklistItems(body string) (requirements int, noneMarkers int) {
	lines := strings.Split(body, "\n")
	var fence fenceState
	for _, line := range lines {
		if fence.update(line) {
			continue
		}
		if fence.inside() {
			continue
		}
		description, ok := parseChecklistDescription(strings.TrimSpace(line))
		if !ok {
			continue
		}
		if isNoneRequiredDescription(description) {
			noneMarkers++
			continue
		}
		requirements++
	}
	return requirements, noneMarkers
}

func taskScopedEvidenceViolations(planText string) []ProtocolViolation {
	var violations []ProtocolViolation
	for _, task := range ParsePlanTasks(planText) {
		var fence fenceState
		for _, line := range task.Body {
			if fence.update(line) {
				continue
			}
			if fence.inside() {
				continue
			}
			trimmed := strings.TrimSpace(line)
			if !isVisualEvidenceHeader(trimmed) && !isBehavioralEvidenceHeader(trimmed) {
				continue
			}
			violations = append(violations, ProtocolViolation{
				Artifact: "phase plan markdown",
				Reason:   fmt.Sprintf("phase plan markdown %s contains task-scoped `%s`; evidence requirements are phase-level and must live under `## Success Criteria`", strings.TrimSpace(task.Heading), trimmed),
			})
		}
	}
	return violations
}

func hasMarkdownHeading(body, heading string) bool {
	lines := strings.Split(body, "\n")
	var fence fenceState
	for _, line := range lines {
		if fence.update(line) {
			continue
		}
		if fence.inside() {
			continue
		}
		if strings.TrimRight(line, " \t") == heading {
			return true
		}
	}
	return false
}

func validateRefactorPlanMarkdownArtifact(iterDir string, path string, out *Outcome) ([]ProtocolViolation, error) {
	if path == "" {
		return []ProtocolViolation{{Artifact: "refactor-plan.md", Reason: missingArtifactReason("refactor-plan.md", iterDir)}}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []ProtocolViolation{{Artifact: "refactor-plan.md", Reason: missingArtifactReason("refactor-plan.md", filepath.Dir(path))}}, nil
		}
		return nil, fmt.Errorf("reading refactor-plan.md: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return []ProtocolViolation{{Artifact: "refactor-plan.md", Reason: "refactor-plan.md is empty"}}, nil
	}
	out.PlanMarkdownPath = path
	return nil, nil
}

func validateKnowledgeBaseIndexArtifact(_ string, path string, _ *Outcome) ([]ProtocolViolation, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []ProtocolViolation{{Artifact: "index.md", Reason: missingArtifactReason("index.md", filepath.Dir(path))}}, nil
		}
		return nil, fmt.Errorf("checking index.md: %w", err)
	}
	if info.IsDir() {
		return []ProtocolViolation{{Artifact: "index.md", Reason: "index.md is a directory"}}, nil
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

func validateProgressArtifact(iterDir, path string, out *Outcome) ([]ProtocolViolation, error) {
	parsed, err := ParseProgressMd(path, filepath.Join(iterDir, "verification-report.yaml"))
	if err != nil {
		return nil, err
	}
	out.Progress = parsed
	if parsed == nil || !parsed.OK() {
		return progressViolations(parsed), nil
	}
	return nil, nil
}

func validateVerificationReportArtifact(_ string, path string, out *Outcome) ([]ProtocolViolation, error) {
	report, err := ReadVerificationReport(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []ProtocolViolation{{Artifact: "verification-report.yaml", Reason: missingArtifactReason("verification-report.yaml", filepath.Dir(path))}}, nil
		}
		return []ProtocolViolation{{Artifact: "verification-report.yaml", Reason: fmt.Sprintf("verification-report.yaml is unparseable: %v", err)}}, nil
	}
	out.VerificationReport = report
	contract, contractErr := readVerificationReportTestingContract(report, filepath.Dir(path))
	if contractErr != nil {
		return []ProtocolViolation{{Artifact: "verification-report.yaml", Reason: fmt.Sprintf("testing contract referenced by verification-report.yaml is unreadable: %v", contractErr)}}, nil
	}
	gate := ValidateVerificationReportWithContext(report, nil, verificationReportComplete(out), VerificationReportValidationContext{
		IterationDir: filepath.Dir(path),
		Contract:     contract,
	})
	if !gate.Rejected {
		return nil, nil
	}
	return reportGateViolations(gate), nil
}

func readVerificationReportTestingContract(report *VerificationReport, iterDir string) (*TestingContract, error) {
	expectedPath := verificationReportSiblingTestingContractPath(iterDir)
	reportPath := strings.TrimSpace(report.ContractPath)
	if expectedPath != "" {
		if reportPath != "" && !sameCleanPath(reportPath, expectedPath) {
			return nil, fmt.Errorf("contract_path %q does not match expected testing contract %q", reportPath, expectedPath)
		}
		contract, err := ReadTestingContract(expectedPath)
		if err != nil {
			return nil, fmt.Errorf("reading testing contract %s: %w", expectedPath, err)
		}
		return contract, nil
	}
	return readBoundTestingContract(report)
}

func verificationReportSiblingTestingContractPath(iterDir string) string {
	if strings.TrimSpace(iterDir) == "" {
		return ""
	}
	phaseDir := filepath.Dir(iterDir)
	candidates := []string{filepath.Join(phaseDir, "testing-contract.yaml")}
	if filepath.Base(phaseDir) == feature.PhaseImplement.DirName() {
		candidates = append([]string{filepath.Join(filepath.Dir(phaseDir), "testing-contract.yaml")}, candidates...)
	}
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

func validateReviewFeedbackArtifactWithDisplay(_ string, path, displayPath string, out *Outcome) ([]ProtocolViolation, error) {
	parsed, err := ParseReviewFeedback(path)
	if err != nil {
		return nil, err
	}
	out.ReviewFeedback = parsed
	if parsed == nil || parsed.OK() {
		return nil, nil
	}
	violations := make([]ProtocolViolation, 0, len(parsed.ProtocolViolations))
	for _, reason := range parsed.ProtocolViolations {
		violations = append(violations, ProtocolViolation{Artifact: displayPath, Reason: reason})
	}
	return violations, nil
}

func validatePlanValidatorAxisApprovalArtifact(iterDir, path string, out *Outcome) ([]ProtocolViolation, error) {
	axis, ok := planValidatorAxis(iterDir)
	displayPath := "axis-approved-<axis>.md"
	if ok {
		displayPath = fmt.Sprintf("axis-approved-%s.md", axis)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", displayPath, err)
	}
	approval := parseAxisApprovalArtifact(string(data))
	if approval.Axis == "" {
		return []ProtocolViolation{{Artifact: displayPath, Reason: "axis approval artifact is unparseable"}}, nil
	}
	if ok && approval.Axis != axis {
		return []ProtocolViolation{{Artifact: displayPath, Reason: fmt.Sprintf("axis approval declares axis %q, want %q", approval.Axis, axis)}}, nil
	}
	out.AxisApproval = &approval
	return nil, nil
}

func validateNeedUserInputArtifact(_ string, path string, out *Outcome) ([]ProtocolViolation, error) {
	rec, err := ReadNeedUserInputRecord(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []ProtocolViolation{{Artifact: NeedUserInputArtifactName, Reason: missingArtifactReason("need-user-input.yaml", filepath.Dir(path))}}, nil
		}
		return []ProtocolViolation{{Artifact: NeedUserInputArtifactName, Reason: fmt.Sprintf("need-user-input.yaml is unparseable: %v", err)}}, nil
	}
	out.NeedUserInput = &rec
	return nil, nil
}

func verificationReportComplete(out *Outcome) bool {
	if out.Progress == nil {
		return true
	}
	return out.Progress.State == StateSuccess
}

func progressViolations(parsed *ParsedProgress) []ProtocolViolation {
	if parsed == nil {
		return []ProtocolViolation{{Artifact: "progress.md", Reason: "progress.md could not be parsed"}}
	}
	out := make([]ProtocolViolation, 0, len(parsed.ProtocolViolations))
	for _, reason := range parsed.ProtocolViolations {
		out = append(out, ProtocolViolation{Artifact: "progress.md", Reason: reason})
	}
	return out
}

func reportGateViolations(gate ReportGateResult) []ProtocolViolation {
	out := make([]ProtocolViolation, 0, len(gate.Findings))
	for _, finding := range gate.Findings {
		reason := finding.Detail
		if finding.CheckName != "" {
			reason = finding.CheckName + ": " + reason
		}
		reason = "Report Integrity Gate: " + reason
		out = append(out, ProtocolViolation{Artifact: "verification-report.yaml", Reason: reason})
	}
	return out
}

type protocolViolationError struct {
	msg string
}

func (e *protocolViolationError) Error() string { return e.msg }

func newProtocolViolationError(role Role, iterDir string, violations []ProtocolViolation) error {
	return &protocolViolationError{msg: formatProtocolViolationError(role, iterDir, violations)}
}

func isProtocolViolationError(err error) bool {
	var target *protocolViolationError
	return errors.As(err, &target)
}

// JoinProtocolViolations renders protocol violations for LastError and review
// feedback text.
func JoinProtocolViolations(violations []ProtocolViolation) string {
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
