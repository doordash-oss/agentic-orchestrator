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
	Name          string
	DisplayPath   string
	HideFromSkill bool
	ResolvePath   func(iterDir string) string
	Validate      func(iterDir, path string, out *Outcome) ([]ProtocolViolation, error)
}

// OptionalArtifact describes an artifact that is valid for a role but not
// required. If present, it must parse cleanly.
type OptionalArtifact struct {
	Name          string
	DisplayPath   string
	HideFromSkill bool
	ResolvePath   func(iterDir string) string
	Validate      func(iterDir, path string, out *Outcome) ([]ProtocolViolation, error)
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

	return validateContractArtifacts(contract, iterDir, true)
}

func validateContractArtifacts(contract RoleContract, iterDir string, includeHidden bool) (Outcome, []ProtocolViolation, error) {
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
	for _, artifact := range contract.Optional {
		if artifact.HideFromSkill && !includeHidden {
			continue
		}
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
// execute before creating phase_complete. It is intentionally stricter than
// Validate for YAML syntax so self-checks catch malformed reports even when the
// harness would tolerate a blocking CHANGES_REQUESTED verdict to route a fix.
func ValidateArtifactsPreflight(phase feature.Phase, role Role, iterDir string) (Outcome, []ProtocolViolation, error) {
	contract, ok := Lookup(phase, role)
	if !ok {
		return Outcome{}, []ProtocolViolation{{Artifact: string(role), Reason: "no contract registered"}}, nil
	}
	if contract.NoOp {
		return Outcome{OK: true}, nil, nil
	}

	var violations []ProtocolViolation
	for _, artifact := range contract.Required {
		if artifact.HideFromSkill {
			continue
		}
		path := artifact.ResolvePath(iterDir)
		violations = append(violations, validateYAMLArtifactSyntax(artifact.DisplayPath, path, true)...)
	}
	for _, artifact := range contract.Optional {
		if artifact.HideFromSkill {
			continue
		}
		path := artifact.ResolvePath(iterDir)
		violations = append(violations, validateYAMLArtifactSyntax(artifact.DisplayPath, path, false)...)
	}
	if len(violations) > 0 {
		return Outcome{OK: false}, violations, nil
	}
	out, contractViolations, err := validateContractArtifacts(contract, iterDir, false)
	if err != nil {
		return out, nil, err
	}
	violations = append(violations, contractViolations...)
	if phase == feature.PhaseImplement && role == RoleImplementer {
		extra, extraErr := validateImplementerAgentOwnedEvidencePreflight(iterDir)
		if extraErr != nil {
			return out, nil, extraErr
		}
		violations = append(violations, extra...)
	}
	out.OK = len(violations) == 0
	return out, violations, nil
}

func validateImplementerAgentOwnedEvidencePreflight(iterDir string) ([]ProtocolViolation, error) {
	contractPath := ""
	for _, candidate := range implementerTestingContractPathCandidates(iterDir) {
		if _, err := os.Stat(candidate); err == nil {
			contractPath = candidate
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("checking testing contract for artifact preflight: %w", err)
		}
	}
	if strings.TrimSpace(contractPath) == "" {
		return nil, nil
	}
	contract, err := ReadTestingContract(contractPath)
	if err != nil {
		return nil, fmt.Errorf("reading testing contract for artifact preflight: %w", err)
	}
	return ValidateRequiredAgentOwnedEvidence(contract, iterDir), nil
}

func implementerTestingContractPathCandidates(iterDir string) []string {
	// Roadmap phase iterations live at <phase>/implement/iteration-N with the
	// contract at <phase>/testing-contract.yaml. Cycle iterations use the same
	// phase-dir progress layout and keep the contract beside progress.md.
	parent := filepath.Dir(iterDir)
	phaseDir := filepath.Dir(parent)
	candidates := []string{}
	for _, dir := range []string{phaseDir, parent} {
		if dir == "." || dir == string(filepath.Separator) {
			continue
		}
		candidate := filepath.Join(dir, "testing-contract.yaml")
		duplicate := false
		for _, existing := range candidates {
			if existing == candidate {
				duplicate = true
			}
		}
		if !duplicate {
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

func validateYAMLArtifactSyntax(displayPath, path string, required bool) []ProtocolViolation {
	if !isYAMLArtifact(displayPath, path) {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if required {
				return []ProtocolViolation{{Artifact: displayPath, Reason: missingArtifactReason(displayPath, filepath.Dir(path))}}
			}
			return nil
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
	for _, heading := range []string{"### Automated Verification", "### Manual Verification"} {
		if !hasMarkdownHeading(success, heading) {
			violations = append(violations, ProtocolViolation{
				Artifact: "phase plan markdown",
				Reason:   fmt.Sprintf("phase plan markdown is missing top-level `%s` under `## Success Criteria`", heading),
			})
			continue
		}
		var reason string
		if heading == "### Automated Verification" {
			reason = validateAutomatedVerificationSection(success)
		} else {
			reason = validateManualVerificationSection(success)
		}
		if reason != "" {
			violations = append(violations, ProtocolViolation{Artifact: "phase plan markdown", Reason: reason})
		}
	}
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
	violations = append(violations, verificationScopeViolations(planText)...)
	return violations
}

func validateAutomatedVerificationSection(successCriteria string) string {
	const heading = "### Automated Verification"
	body := extractMarkdownSection(successCriteria, heading)
	requirements, noneMarkers, invalidNoneMarkers := countEvidenceChecklistItems(body)
	switch {
	case invalidNoneMarkers > 0:
		return "phase plan markdown `### Automated Verification` must give every `None required:` item a concrete reason"
	case noneMarkers == 1 && requirements == 0:
		return ""
	case noneMarkers > 0:
		return "phase plan markdown `### Automated Verification` must contain executable checklist commands or exactly one `None required: <reason>` checklist item, not both"
	case requirements == 0:
		return "phase plan markdown `### Automated Verification` must contain executable checklist commands or exactly one `None required: <reason>` checklist item"
	}
	parsed := ParsePlanVerification(heading + "\n" + body)
	if len(parsed) != requirements {
		return "phase plan markdown `### Automated Verification` checklist items must each end with one complete executable command in backticks"
	}
	return ""
}

func validateManualVerificationSection(successCriteria string) string {
	const heading = "### Manual Verification"
	body := extractMarkdownSection(successCriteria, heading)
	requirements, noneMarkers, invalidNoneMarkers := countEvidenceChecklistItems(body)
	switch {
	case invalidNoneMarkers > 0:
		return "phase plan markdown `### Manual Verification` must give every `None required:` item a concrete reason"
	case noneMarkers == 1 && requirements == 0:
		return ""
	case noneMarkers > 0:
		return "phase plan markdown `### Manual Verification` must contain one consolidated checklist requirement or exactly one `None required: <reason>` checklist item, not both"
	case requirements == 1:
		var fence fenceState
		for _, line := range strings.Split(body, "\n") {
			if fence.update(line) || fence.inside() {
				continue
			}
			description, ok := parseChecklistDescription(strings.TrimSpace(line))
			if ok && !isNoneRequiredDescription(description) && descriptionContainsCommandSpan(description) {
				return "phase plan markdown `### Manual Verification` must describe semantic judgment without executable backtick commands"
			}
		}
		return ""
	case requirements > 1:
		return "phase plan markdown `### Manual Verification` must use a single consolidated requirement so the implementer produces one semantic observation artifact"
	default:
		return "phase plan markdown `### Manual Verification` must contain one consolidated checklist requirement or exactly one `None required: <reason>` checklist item"
	}
}

// descriptionContainsCommandSpan reports whether any backtick span in the
// description looks like an executable shell command. Inline code references
// (symbol names, UI labels) are allowed in manual checks.
func descriptionContainsCommandSpan(description string) bool {
	for _, span := range findBacktickSpans(description) {
		if looksLikeShellCommand(strings.TrimSpace(description[span[2]:span[3]])) {
			return true
		}
	}
	return false
}

// verificationScopeViolations checks every automated-verification command the
// contract compiler would consume: per-Task blocks plus all content outside
// the `## Tasks` section (the compiler's top-level scope — not just Success
// Criteria). Repo names compare case-insensitively so tag-case drift does not
// reject an otherwise valid plan.
func verificationScopeViolations(planText string) []ProtocolViolation {
	tasks := ParsePlanTasks(planText)
	repos := make(map[string]bool)
	var violations []ProtocolViolation
	for _, task := range tasks {
		if len(task.Repos) > 1 {
			violations = append(violations, ProtocolViolation{Artifact: "phase plan markdown", Reason: fmt.Sprintf(
				"%s declares multiple `**Repo:**` tags; split cross-repo work into one task per repo", strings.TrimSpace(task.Heading))})
		}
		for _, declaredRepo := range task.Repos {
			repo := strings.ToLower(strings.TrimSpace(declaredRepo))
			repos[repo] = true
		}
	}
	for _, task := range tasks {
		taskRepo := ""
		if len(task.Repos) == 1 {
			taskRepo = strings.TrimSpace(task.Repos[0])
		}
		for _, step := range ParsePlanVerification(strings.Join(task.Body, "\n")) {
			if step.Repo != "" && taskRepo != "" && !strings.EqualFold(step.Repo, taskRepo) {
				violations = append(violations, ProtocolViolation{Artifact: "phase plan markdown", Reason: fmt.Sprintf(
					"verification command scoped to repo %q appears under a task scoped to repo %q", step.Repo, taskRepo)})
			}
		}
	}
	topLevel, _ := splitPlanForVerification(planText)
	for _, step := range ParsePlanVerification(topLevel) {
		repo := strings.TrimSpace(step.Repo)
		if len(repos) > 1 && repo == "" {
			violations = append(violations, ProtocolViolation{Artifact: "phase plan markdown", Reason: "multi-repo phase automated verification must scope every command with `[repo: <name>]`"})
			continue
		}
		if repo != "" && len(repos) > 0 && !repos[strings.ToLower(repo)] {
			violations = append(violations, ProtocolViolation{Artifact: "phase plan markdown", Reason: fmt.Sprintf(
				"verification command references repo %q, which is not assigned to any phase task", repo)})
		}
	}
	return violations
}

func validateFrontendVisualEvidenceSection(successCriteria string) string {
	const heading = "### Visual Evidence"
	if !hasMarkdownHeading(successCriteria, heading) {
		return frontendVisualEvidenceRuleMessage()
	}
	body := extractMarkdownSection(successCriteria, heading)
	requirements, _, _ := countEvidenceChecklistItems(body)
	if requirements > 0 {
		if evidenceBulletMissingProse(body) {
			return evidenceBulletMissingProseMessage(heading)
		}
		return ""
	}
	return frontendVisualEvidenceRuleMessage()
}

func frontendVisualEvidenceRuleMessage() string {
	return "frontend/visual-evidence rule: phase plan metadata declares `frontend: true`, so `### Visual Evidence` must contain at least one real checklist visual evidence requirement; `None required` or an empty/missing section is not allowed"
}

// phasePlanEvidenceModeViolations enforces the profile-aware evidence mode
// for a phase plan. Evidence-contracting profiles must give frontend phases
// real `### Visual Evidence` rows; automated-only profiles must never
// declare agent-owned evidence rows at all.
func phasePlanEvidenceModeViolations(planText string, contractAgentEvidence bool) []ProtocolViolation {
	if !contractAgentEvidence {
		return agentEvidencePlanViolations(planText)
	}
	if !ParsePhasePlanFrontend(planText) {
		return nil
	}
	success := extractMarkdownSection(planText, "## Success Criteria")
	if reason := validateFrontendVisualEvidenceSection(success); reason != "" {
		return []ProtocolViolation{{Artifact: "phase plan markdown", Reason: reason}}
	}
	return nil
}

// agentEvidencePlanViolations rejects agent-owned evidence contracting for
// features whose profile never consumes it: Manual/Visual/Behavioral
// sections must each be a single None-required marker. Automated
// Verification is unrestricted.
func agentEvidencePlanViolations(planText string) []ProtocolViolation {
	success := extractMarkdownSection(planText, "## Success Criteria")
	var violations []ProtocolViolation
	for _, heading := range []string{"### Manual Verification", "### Visual Evidence", "### Behavioral Evidence"} {
		body := extractMarkdownSection(success, heading)
		requirements, _, _ := countEvidenceChecklistItems(body)
		if requirements > 0 {
			violations = append(violations, ProtocolViolation{
				Artifact: "phase plan markdown",
				Reason:   fmt.Sprintf("phase plan markdown `%s` must contain exactly one `None required: <reason>` item — this feature verifies through automated tests only; move executable checks under `### Automated Verification`", heading),
			})
		}
	}
	return violations
}

func validateEvidenceSectionBody(successCriteria, heading string) string {
	body := extractMarkdownSection(successCriteria, heading)
	requirements, noneMarkers, invalidNoneMarkers := countEvidenceChecklistItems(body)
	switch {
	case invalidNoneMarkers > 0:
		return fmt.Sprintf("phase plan markdown `%s` must give every `None required:` item a concrete reason", heading)
	case noneMarkers == 1 && requirements == 0:
		return ""
	case noneMarkers > 0:
		return fmt.Sprintf("phase plan markdown `%s` must contain checklist requirements or exactly one `None required: <reason>` checklist item, not both", heading)
	case requirements > 0 && evidenceBulletMissingProse(body):
		return evidenceBulletMissingProseMessage(heading)
	case heading == "### Behavioral Evidence" && requirements > 1:
		if behavioralItemsAllCarryCommands(body) {
			return ""
		}
		return "phase plan markdown `### Behavioral Evidence` may list multiple requirements only when every item ends with its packaged executable command in backticks; otherwise use one consolidated primary-journey artifact"
	case requirements > 0:
		return ""
	default:
		return fmt.Sprintf("phase plan markdown `%s` must contain checklist evidence requirements or exactly one `None required: <reason>` checklist item", heading)
	}
}

func behavioralItemsAllCarryCommands(body string) bool {
	var fence fenceState
	saw := false
	for _, line := range strings.Split(body, "\n") {
		if fence.update(line) || fence.inside() {
			continue
		}
		req, ok := parseEvidenceChecklistItem(strings.TrimSpace(line))
		if !ok {
			continue
		}
		if strings.TrimSpace(req.Command) == "" {
			return false
		}
		saw = true
	}
	return saw
}

// evidenceBulletMissingProse reports whether body contains a checklist bullet
// that countEvidenceChecklistItems counts as a requirement but that
// parseEvidenceChecklistItem (the testing-contract compiler's parser) rejects
// because stripping its trailing executable command leaves an empty
// description. Such a bullet passes structural validation yet compiles to
// zero contract rows, so evidence sections must reject it explicitly. Only
// callers validating Visual/Behavioral Evidence sections use this — Manual
// and Automated Verification never strip a trailing command out of the
// description, so they cannot hit this mismatch.
func evidenceBulletMissingProse(body string) bool {
	var fence fenceState
	for _, line := range strings.Split(body, "\n") {
		if fence.update(line) || fence.inside() {
			continue
		}
		trimmed := strings.TrimSpace(line)
		description, ok := parseChecklistDescription(trimmed)
		if !ok || isNoneRequiredDescription(description) {
			continue
		}
		if _, ok := parseEvidenceChecklistItem(trimmed); !ok {
			return true
		}
	}
	return false
}

func evidenceBulletMissingProseMessage(heading string) string {
	return fmt.Sprintf("phase plan markdown `%s` checklist items must include prose describing the evidence before the trailing executable command; a bullet consisting only of `` `command` `` compiles to zero evidence rows in the testing contract", heading)
}

func countEvidenceChecklistItems(body string) (requirements int, noneMarkers int, invalidNoneMarkers int) {
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
		if marker, valid := noneRequiredMarker(description); marker {
			if valid {
				noneMarkers++
			} else {
				invalidNoneMarkers++
			}
			continue
		}
		requirements++
	}
	return requirements, noneMarkers, invalidNoneMarkers
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
	parsed, err := ParseProgressMd(path)
	if err != nil {
		return nil, err
	}
	out.Progress = parsed
	if parsed == nil || !parsed.OK() {
		return progressViolations(parsed), nil
	}
	return nil, nil
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
