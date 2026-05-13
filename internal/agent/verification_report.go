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

	"gopkg.in/yaml.v3"
)

// RequiredVerificationItem is a verification requirement the implementation
// iteration must account for in its verification report.
type RequiredVerificationItem struct {
	Name        string `yaml:"name,omitempty"`
	Requirement string `yaml:"requirement"`
}

type VerificationMode string

const (
	VerificationModeUnknown VerificationMode = "unknown"
	VerificationModeCommand VerificationMode = "command"
	VerificationModeSkill   VerificationMode = "skill"
	VerificationModeManual  VerificationMode = "manual"
	VerificationModeOther   VerificationMode = "other"
)

type VerificationRunStatus string

const (
	VerificationStatusPassed       VerificationRunStatus = "passed"
	VerificationStatusFailed       VerificationRunStatus = "failed"
	VerificationStatusBlocked      VerificationRunStatus = "blocked"
	VerificationStatusWaived       VerificationRunStatus = "waived"
	VerificationStatusNotRun       VerificationRunStatus = "not_run"
	VerificationStatusPendingHuman VerificationRunStatus = "pending_human"
)

type VerificationEvidence struct {
	ExitCode *int   `yaml:"exit_code,omitempty"`
	Summary  string `yaml:"summary,omitempty"`
}

type VerificationMismatch struct {
	ItemID               string `yaml:"item_id"`
	ImplementationStatus string `yaml:"implementation_status"`
	FinalReviewStatus    string `yaml:"final_review_status"`
	Note                 string `yaml:"note,omitempty"`
}

// VerificationCheckResult captures the implementation agent's evidence for one
// verification requirement. Evidence keeps both the legacy scalar form and the
// structured v2 form in-memory so the reader can normalize old and new reports.
type VerificationCheckResult struct {
	ItemID           string                `yaml:"-"`
	Name             string                `yaml:"-"`
	Requirement      string                `yaml:"-"`
	Command          string                `yaml:"-"`
	Mode             VerificationMode      `yaml:"-"`
	Status           VerificationRunStatus `yaml:"-"`
	Evidence         string                `yaml:"-"`
	EvidenceData     VerificationEvidence  `yaml:"-"`
	Notes            string                `yaml:"-"`
	BlockedReason    string                `yaml:"-"`
	SubstituteItemID string                `yaml:"-"`
}

type verificationCheckResultWire struct {
	ItemID           string                `yaml:"item_id,omitempty"`
	Name             string                `yaml:"name,omitempty"`
	Requirement      string                `yaml:"requirement,omitempty"`
	Command          string                `yaml:"command,omitempty"`
	Mode             VerificationMode      `yaml:"mode,omitempty"`
	Status           VerificationRunStatus `yaml:"status,omitempty"`
	Evidence         any                   `yaml:"evidence,omitempty"`
	Notes            string                `yaml:"notes,omitempty"`
	BlockedReason    string                `yaml:"blocked_reason,omitempty"`
	SubstituteItemID string                `yaml:"substitute_item_id,omitempty"`
}

func (r VerificationCheckResult) MarshalYAML() (any, error) {
	wire := verificationCheckResultWire{
		ItemID:           r.ItemID,
		Name:             r.Name,
		Requirement:      strings.TrimSpace(r.Requirement),
		Command:          strings.TrimSpace(r.Command),
		Mode:             r.Mode,
		Status:           r.Status,
		Notes:            r.Notes,
		BlockedReason:    r.BlockedReason,
		SubstituteItemID: r.SubstituteItemID,
	}
	if wire.Command == "" && wire.Requirement != "" {
		wire.Command = wire.Requirement
	}
	if hasStructuredEvidence(r.EvidenceData) {
		wire.Evidence = r.EvidenceData
	} else if strings.TrimSpace(r.Evidence) != "" {
		wire.Evidence = strings.TrimSpace(r.Evidence)
	}
	return wire, nil
}

func (r *VerificationCheckResult) UnmarshalYAML(node *yaml.Node) error {
	var wire verificationCheckResultWire
	if err := node.Decode(&wire); err != nil {
		return err
	}
	*r = VerificationCheckResult{
		ItemID:           strings.TrimSpace(wire.ItemID),
		Name:             wire.Name,
		Requirement:      strings.TrimSpace(wire.Requirement),
		Command:          strings.TrimSpace(wire.Command),
		Mode:             wire.Mode,
		Status:           wire.Status,
		Notes:            wire.Notes,
		BlockedReason:    strings.TrimSpace(wire.BlockedReason),
		SubstituteItemID: strings.TrimSpace(wire.SubstituteItemID),
	}
	if r.Command == "" {
		r.Command = r.Requirement
	}
	if r.Requirement == "" {
		r.Requirement = r.Command
	}

	switch evidence := wire.Evidence.(type) {
	case nil:
		return nil
	case string:
		r.Evidence = strings.TrimSpace(evidence)
		r.EvidenceData.Summary = r.Evidence
		return nil
	case map[string]any:
		if summary, ok := evidence["summary"].(string); ok {
			r.EvidenceData.Summary = strings.TrimSpace(summary)
			r.Evidence = r.EvidenceData.Summary
		}
		if exit, ok := evidence["exit_code"]; ok {
			if exitCode, ok := toInt(exit); ok {
				r.EvidenceData.ExitCode = &exitCode
			}
		}
		return nil
	default:
		var structured VerificationEvidence
		if err := nodeDecodeEvidence(node, &structured); err == nil && hasStructuredEvidence(structured) {
			r.EvidenceData = structured
			r.Evidence = strings.TrimSpace(structured.Summary)
		}
		return nil
	}
}

func nodeDecodeEvidence(node *yaml.Node, out *VerificationEvidence) error {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value != "evidence" {
			continue
		}
		if err := node.Content[i+1].Decode(out); err != nil {
			return err
		}
		return nil
	}
	return nil
}

func hasStructuredEvidence(e VerificationEvidence) bool {
	return e.ExitCode != nil || strings.TrimSpace(e.Summary) != ""
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case uint64:
		return int(n), true
	case float64:
		return int(n), true
	default:
		return 0, false
	}
}

// VerificationReport is written per implementation iteration. Cross-phase
// deferrals (new and closed) live in progress.md's `## Deferrals` section,
// not here — verification-report.yaml is purely about verification check
// results and contract metadata.
type VerificationReport struct {
	Version          int                       `yaml:"version"`
	ContractPath     string                    `yaml:"contract_path,omitempty"`
	ContractRevision int                       `yaml:"contract_revision,omitempty"`
	Results          []VerificationCheckResult `yaml:"results,omitempty"`
	Mismatches       []VerificationMismatch    `yaml:"mismatches,omitempty"`
	RequiredChecks   []VerificationCheckResult `yaml:"required_checks,omitempty"`
	AdditionalChecks []VerificationCheckResult `yaml:"additional_checks,omitempty"`
	Summary          string                    `yaml:"summary,omitempty"`
	KnownCaveats     KnownCaveats              `yaml:"known_caveats,omitempty"`
}

// KnownCaveats is a key→prose map of out-of-phase deferrals the agent
// declares. It accepts two wire formats because real-world verification
// reports emit both.
type KnownCaveats map[string]string

func (kc *KnownCaveats) UnmarshalYAML(node *yaml.Node) error {
	if node == nil || node.Kind == 0 {
		return nil
	}
	switch node.Kind {
	case yaml.MappingNode:
		out := make(map[string]string, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			out[node.Content[i].Value] = node.Content[i+1].Value
		}
		*kc = out
		return nil
	case yaml.SequenceNode:
		out := make(map[string]string, len(node.Content))
		for _, entry := range node.Content {
			if entry.Kind != yaml.MappingNode {
				return fmt.Errorf("known_caveats: sequence entry must be a map, got kind %d", entry.Kind)
			}
			for i := 0; i+1 < len(entry.Content); i += 2 {
				out[entry.Content[i].Value] = entry.Content[i+1].Value
			}
		}
		*kc = out
		return nil
	default:
		return fmt.Errorf("known_caveats: expected map or sequence of maps, got node kind %d", node.Kind)
	}
}

// NormalizeStatus collapses common short-form status values into their
// canonical long form.
func NormalizeStatus(s VerificationRunStatus) VerificationRunStatus {
	switch strings.ToLower(strings.TrimSpace(string(s))) {
	case "pass", "passed", "ok", "success":
		return VerificationStatusPassed
	case "fail", "failed", "failure", "error":
		return VerificationStatusFailed
	case "blocked":
		return VerificationStatusBlocked
	case "waived", "waive":
		return VerificationStatusWaived
	case "skip", "skipped", "not_run", "notrun", "pending":
		return VerificationStatusNotRun
	case "pending_human", "manual", "human":
		return VerificationStatusPendingHuman
	default:
		return s
	}
}

// BuildRequiredVerification merges plan-extracted automated verification with
// the feature's configured verification text.
func BuildRequiredVerification(planText string) []RequiredVerificationItem {
	seen := make(map[string]bool)
	var required []RequiredVerificationItem

	add := func(item RequiredVerificationItem) {
		key := strings.TrimSpace(item.Requirement)
		if key == "" || seen[key] {
			return
		}
		item.Requirement = key
		seen[key] = true
		required = append(required, item)
	}

	for _, step := range ParsePlanVerification(planText) {
		add(RequiredVerificationItem{
			Name:        step.Description,
			Requirement: step.Command,
		})
	}

	return required
}

func BuildVerificationReportStub(required []RequiredVerificationItem) VerificationReport {
	report := VerificationReport{Version: 1}
	for _, item := range required {
		report.RequiredChecks = append(report.RequiredChecks, VerificationCheckResult{
			Name:        item.Name,
			Requirement: item.Requirement,
			Command:     item.Requirement,
			Mode:        VerificationModeUnknown,
			Status:      VerificationStatusNotRun,
		})
	}
	return report
}

func BuildContractVerificationReportStub(contract *TestingContract, contractPath string) VerificationReport {
	report := VerificationReport{
		Version:          2,
		ContractPath:     strings.TrimSpace(contractPath),
		ContractRevision: contract.Revision,
	}
	for _, item := range contract.Items {
		mode := VerificationModeCommand
		if item.ExpectedEvidence.Kind == testingContractManualKind || item.Source == testingContractManualSource {
			mode = VerificationModeManual
		}
		report.Results = append(report.Results, VerificationCheckResult{
			ItemID:      item.ID,
			Name:        item.Name,
			Requirement: item.Command,
			Command:     item.Command,
			Mode:        mode,
			Status:      VerificationStatusNotRun,
		})
	}
	return report
}

func WriteVerificationReport(path string, report VerificationReport) error {
	data, err := yaml.Marshal(report)
	if err != nil {
		return fmt.Errorf("marshaling verification report: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing verification report: %w", err)
	}
	return nil
}

func WriteVerificationReportStub(path string, required []RequiredVerificationItem) error {
	return WriteVerificationReport(path, BuildVerificationReportStub(required))
}

func WriteVerificationReportStubFromContract(path, contractPath string, contract *TestingContract) error {
	return WriteVerificationReport(path, BuildContractVerificationReportStub(contract, contractPath))
}

func ReadVerificationReport(path string) (*VerificationReport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var report VerificationReport
	if err := yaml.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("parsing verification report: %w", err)
	}
	if report.Version == 0 {
		report.Version = 1
	}
	normalizeChecks(report.Results)
	normalizeChecks(report.RequiredChecks)
	normalizeChecks(report.AdditionalChecks)
	if len(report.Results) > 0 && len(report.RequiredChecks) == 0 {
		report.RequiredChecks = append([]VerificationCheckResult(nil), report.Results...)
	}
	report.Mismatches = normalizeMismatches(report.Mismatches)
	return &report, nil
}

func normalizeChecks(checks []VerificationCheckResult) {
	for i := range checks {
		checks[i].Status = NormalizeStatus(checks[i].Status)
		if checks[i].Command == "" {
			checks[i].Command = checks[i].Requirement
		}
		if checks[i].Requirement == "" {
			checks[i].Requirement = checks[i].Command
		}
		if checks[i].Evidence == "" {
			checks[i].Evidence = strings.TrimSpace(checks[i].EvidenceData.Summary)
		}
		if checks[i].EvidenceData.Summary == "" {
			checks[i].EvidenceData.Summary = strings.TrimSpace(checks[i].Evidence)
		}
	}
}

func normalizeMismatches(in []VerificationMismatch) []VerificationMismatch {
	if len(in) == 0 {
		return nil
	}
	out := make([]VerificationMismatch, 0, len(in))
	for _, mismatch := range in {
		itemID := strings.TrimSpace(mismatch.ItemID)
		if itemID == "" {
			continue
		}
		out = append(out, VerificationMismatch{
			ItemID:               itemID,
			ImplementationStatus: strings.TrimSpace(mismatch.ImplementationStatus),
			FinalReviewStatus:    strings.TrimSpace(mismatch.FinalReviewStatus),
			Note:                 strings.TrimSpace(mismatch.Note),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
