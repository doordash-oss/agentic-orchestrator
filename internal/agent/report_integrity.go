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
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// ReportGateCategory classifies a gate finding so downstream feedback can
// group by failure mode.
type ReportGateCategory string

const (
	// GateCategorySchema covers structural integrity issues: missing
	// required checks, empty evidence on pass, unknown status values, or
	// not_run entries after phase_complete.
	GateCategorySchema ReportGateCategory = "schema"

	// GateCategoryHedge covers pass claims whose evidence text describes a
	// failure — the agent marking passed while its own prose admits it
	// didn't.
	GateCategoryHedge ReportGateCategory = "hedge"

	// GateCategoryDeferral covers structured cross-phase commitment failures.
	GateCategoryDeferral ReportGateCategory = "deferral"
)

// ReportGateKind is a fine-grained finding type within a category. It
// drives feedback consolidation: findings sharing the same Kind are
// collapsed into a single bullet when many fire at once, so the agent
// reads one actionable signal instead of N repetitions of the same fact.
type ReportGateKind string

const (
	KindMissingRequired       ReportGateKind = "missing_required"
	KindUnknownItemID         ReportGateKind = "unknown_item_id"
	KindEmptyEvidence         ReportGateKind = "empty_evidence"
	KindNotRunAtEnd           ReportGateKind = "not_run_at_phase_complete"
	KindUnknownStatus         ReportGateKind = "unknown_status"
	KindHedgePhrase           ReportGateKind = "hedge_phrase"
	KindStaleRevision         ReportGateKind = "stale_contract_revision"
	KindBlockedReason         ReportGateKind = "missing_blocked_reason"
	KindSubstituteItem        ReportGateKind = "invalid_substitute_item"
	KindPolicyViolation       ReportGateKind = "policy_violation"
	KindEvidenceFile          ReportGateKind = "invalid_evidence_file"
	KindDeferralUnclosed      ReportGateKind = "deferral_unclosed_due_this_phase"
	KindDeferralMissingReason ReportGateKind = "deferral_missing_reason"
)

// ReportGateFinding describes a single integrity issue found in a
// verification report. The CheckName is empty for report-level issues that
// aren't scoped to one check (e.g. "missing required item").
type ReportGateFinding struct {
	CheckName string
	Category  ReportGateCategory
	Kind      ReportGateKind
	Detail    string
}

// ReportGateResult is what ValidateVerificationReport returns. Rejected is
// true iff any blocking findings exist. KnownCaveats is populated with the
// report's known_caveats map for downstream surfacing to the LLM reviewer.
type ReportGateResult struct {
	Rejected     bool
	Findings     []ReportGateFinding
	KnownCaveats map[string]string
}

// VerificationReportValidationContext carries optional filesystem context for
// contract-backed evidence-file validation. IterationDir is the trust root for
// evidence.primary and evidence.attachments paths.
type VerificationReportValidationContext struct {
	IterationDir string
	Contract     *TestingContract
}

// hedgePhrases are case-insensitive needles we refuse to see in the
// Evidence text of a check marked as passed. The list is intentionally
// short — false positives just force the agent to rephrase truthfully
// (cheap), while false negatives cost hour-long LLM review cycles
// (expensive). Broaden this list based on observed rejection logs.
var hedgePhrases = []string{
	"caveat",
	"pre-existing bug",
	"pre-existing environmental bug",
	"fails on ",
	"fails in ",
	"failure on ",
	"does not yet",
	"not yet implemented",
	"orthogonal to",
	"cannot be verified",
}

// ValidateVerificationReport runs the deterministic integrity gate over a
// verification report. Returns a result describing any blocking findings.
//
// The gate is designed to catch two classes of over-claim cheaply:
//
//  1. Schema lies — the agent marking items passed that are empty, missing,
//     or still not_run after it signaled phase_complete.
//  2. Prose lies — the agent marking items passed while the Evidence text
//     itself describes failure ("fails on macOS", "pre-existing bug",
//     "orthogonal to this phase").
//
// phaseComplete reflects whether the agent has signaled it's done (i.e.
// wrote the phase_complete marker). When true, any not_run required check
// is a contradiction and blocks the gate.
func ValidateVerificationReport(report *VerificationReport, required []RequiredVerificationItem, contract *TestingContract, phaseComplete bool) ReportGateResult {
	return ValidateVerificationReportWithContext(report, required, phaseComplete, VerificationReportValidationContext{
		Contract: contract,
	})
}

// ValidateVerificationReportWithContext runs the deterministic integrity gate
// and, when supplied with an iteration directory, validates file-backed visual
// and behavioral evidence under that iteration root.
func ValidateVerificationReportWithContext(report *VerificationReport, required []RequiredVerificationItem, phaseComplete bool, ctx VerificationReportValidationContext) ReportGateResult {
	result := ReportGateResult{KnownCaveats: report.KnownCaveats}

	checks := reportChecks(report)
	contract := ctx.Contract
	contractBacked := contract != nil
	if contractBacked {
		validateContractBackedChecks(&result, report, checks, contract)
	} else {
		validateLegacyRequiredChecks(&result, checks, required)
	}

	// 2. Per-check schema and hedge-phrase checks.
	for i := range checks {
		c := &checks[i]
		name := checkDisplayName(c)
		status := NormalizeStatus(c.Status)

		switch status {
		case VerificationStatusPassed:
			evidenceText := verificationEvidenceText(c)
			if contractBacked && !hasStructuredEvidence(c.EvidenceData) {
				result.Findings = append(result.Findings, ReportGateFinding{
					CheckName: name,
					Category:  GateCategorySchema,
					Kind:      KindEmptyEvidence,
					Detail:    "status is passed but structured evidence is empty — every passed contract item must record structured command evidence",
				})
				continue
			}
			// Empty evidence on pass is a schema lie.
			if strings.TrimSpace(evidenceText) == "" && !artifactEvidenceHasPrimary(c) {
				result.Findings = append(result.Findings, ReportGateFinding{
					CheckName: name,
					Category:  GateCategorySchema,
					Kind:      KindEmptyEvidence,
					Detail:    "status is passed but evidence is empty — every passed check must include what you ran and the result",
				})
				continue
			}
			// Prose lie: hedge phrases inside evidence for a pass claim.
			if evidenceText != "" {
				if hits := findHedgePhrases(evidenceText); len(hits) > 0 {
					result.Findings = append(result.Findings, ReportGateFinding{
						CheckName: name,
						Category:  GateCategoryHedge,
						Kind:      KindHedgePhrase,
						Detail:    fmt.Sprintf("status is passed but evidence text contains hedge phrases that describe failure: %s — if the check actually failed, mark status as failed and say so plainly", strings.Join(quoted(hits), ", ")),
					})
				}
			}

		case VerificationStatusPendingHuman:
			if contractBacked && c.Mode != VerificationModeManual && !isArtifactEvidenceMode(c.Mode) {
				result.Findings = append(result.Findings, ReportGateFinding{
					CheckName: name,
					Category:  GateCategorySchema,
					Kind:      KindPolicyViolation,
					Detail:    "status is pending_human but the contract item is not mode: manual, visual, or behavioral",
				})
				continue
			}
			if contractBacked && c.Mode == VerificationModeManual &&
				strings.TrimSpace(c.BlockedReason) == "" &&
				strings.TrimSpace(c.Notes) == "" &&
				strings.TrimSpace(verificationEvidenceText(c)) == "" {
				result.Findings = append(result.Findings, ReportGateFinding{
					CheckName: name,
					Category:  GateCategorySchema,
					Kind:      KindBlockedReason,
					Detail:    "status is pending_human for a manual item but no blocked_reason, notes, or evidence names the downstream owner",
				})
			}

		case VerificationStatusNotRun:
			if phaseComplete {
				result.Findings = append(result.Findings, ReportGateFinding{
					CheckName: name,
					Category:  GateCategorySchema,
					Kind:      KindNotRunAtEnd,
					Detail:    "status is not_run but the phase is marked complete — either run the check and record the result, or explain why this check cannot run (mark as pending_human if blocked on a human)",
				})
			}

		case VerificationStatusFailed, VerificationStatusInheritedFailure, VerificationStatusBlocked, VerificationStatusWaived:
			// Honest declarations. The LLM reviewer assesses severity. The
			// gate does not block here except for policy/schema checks above.

		default:
			// Unknown status value after normalization.
			result.Findings = append(result.Findings, ReportGateFinding{
				CheckName: name,
				Category:  GateCategorySchema,
				Kind:      KindUnknownStatus,
				Detail:    fmt.Sprintf("status %q is not one of passed, failed, inherited_failure, blocked, waived, not_run, pending_human", string(c.Status)),
			})
		}
	}

	if contractBacked && strings.TrimSpace(ctx.IterationDir) != "" {
		validateEvidenceFiles(&result, checks, contract, ctx.IterationDir)
	}

	result.Rejected = len(result.Findings) > 0
	return result
}

func reportChecks(report *VerificationReport) []VerificationCheckResult {
	if len(report.Results) > 0 {
		return report.Results
	}
	return report.RequiredChecks
}

func validateEvidenceFiles(result *ReportGateResult, checks []VerificationCheckResult, contract *TestingContract, iterDir string) {
	items := make(map[string]TestingContractItem, len(contract.Items))
	for _, item := range contract.Items {
		items[item.ID] = item
	}
	for i := range checks {
		c := &checks[i]
		item, ok := items[strings.TrimSpace(c.ItemID)]
		if !ok {
			continue
		}
		root, ok := evidenceFileRootForContractItem(item)
		if !ok {
			continue
		}
		status := NormalizeStatus(c.Status)
		switch status {
		case VerificationStatusPassed, VerificationStatusFailed, VerificationStatusInheritedFailure, VerificationStatusPendingHuman:
		default:
			continue
		}

		name := checkDisplayName(c)
		if strings.TrimSpace(c.EvidenceData.Primary) == "" {
			result.Findings = append(result.Findings, ReportGateFinding{
				CheckName: name,
				Category:  GateCategorySchema,
				Kind:      KindEvidenceFile,
				Detail:    fmt.Sprintf("status is %s but evidence.primary is empty — file-backed evidence for this row must name a file under %s/", status, root),
			})
			continue
		}
		validateEvidencePath(result, name, iterDir, root, "primary", c.EvidenceData.Primary)
		for idx, attachment := range c.EvidenceData.Attachments {
			field := fmt.Sprintf("attachments[%d]", idx)
			validateEvidencePath(result, name, iterDir, root, field, attachment)
		}
	}
}

func evidenceFileRootForContractItem(item TestingContractItem) (string, bool) {
	source := strings.TrimSpace(item.Source)
	kind := strings.TrimSpace(item.ExpectedEvidence.Kind)
	switch {
	case source == testingContractVisualSource || kind == testingContractVisualKind:
		return "screenshots", true
	case source == testingContractBehavioralSource || kind == testingContractBehavioralKind:
		return "behaviors", true
	default:
		return "", false
	}
}

func validateEvidencePath(result *ReportGateResult, checkName, iterDir, root, field, raw string) bool {
	clean, detail := cleanEvidencePath(raw, root)
	if detail != "" {
		result.Findings = append(result.Findings, ReportGateFinding{
			CheckName: checkName,
			Category:  GateCategorySchema,
			Kind:      KindEvidenceFile,
			Detail:    fmt.Sprintf("evidence.%s path %q is invalid: %s", field, raw, detail),
		})
		return false
	}
	fullPath := filepath.Join(iterDir, filepath.FromSlash(clean))
	info, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			result.Findings = append(result.Findings, ReportGateFinding{
				CheckName: checkName,
				Category:  GateCategorySchema,
				Kind:      KindEvidenceFile,
				Detail:    fmt.Sprintf("evidence.%s path %q is missing under iteration directory", field, clean),
			})
			return false
		}
		result.Findings = append(result.Findings, ReportGateFinding{
			CheckName: checkName,
			Category:  GateCategorySchema,
			Kind:      KindEvidenceFile,
			Detail:    fmt.Sprintf("evidence.%s path %q could not be statted: %v", field, clean, err),
		})
		return false
	}
	if info.IsDir() {
		result.Findings = append(result.Findings, ReportGateFinding{
			CheckName: checkName,
			Category:  GateCategorySchema,
			Kind:      KindEvidenceFile,
			Detail:    fmt.Sprintf("evidence.%s path %q is a directory, not a file", field, clean),
		})
		return false
	}
	return true
}

func cleanEvidencePath(raw, root string) (string, string) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", "path is empty"
	}
	slashPath := strings.ReplaceAll(p, "\\", "/")
	if path.IsAbs(slashPath) || filepath.IsAbs(p) || strings.HasPrefix(slashPath, "//") || isWindowsAbsolutePath(slashPath) {
		return "", "absolute paths are not allowed"
	}
	parts := strings.Split(slashPath, "/")
	for _, part := range parts {
		switch part {
		case "", ".":
			return "", "empty or current-directory path segments are not allowed"
		case "..":
			return "", "traversal segments are not allowed"
		}
	}
	clean := path.Clean(slashPath)
	if clean == "." {
		return "", "path is empty"
	}
	first, _, _ := strings.Cut(clean, "/")
	if first != root {
		return "", fmt.Sprintf("path must be under %s/", root)
	}
	return clean, ""
}

func isWindowsAbsolutePath(p string) bool {
	if len(p) >= 3 && p[1] == ':' && p[2] == '/' {
		c := p[0]
		return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
	}
	return false
}

func readBoundTestingContract(report *VerificationReport) (*TestingContract, error) {
	path := strings.TrimSpace(report.ContractPath)
	if path == "" {
		return nil, nil
	}
	contract, err := ReadTestingContract(path)
	if err != nil {
		return nil, fmt.Errorf("reading testing contract %s: %w", path, err)
	}
	return contract, nil
}

func validateLegacyRequiredChecks(result *ReportGateResult, checks []VerificationCheckResult, required []RequiredVerificationItem) {
	byRequirement := make(map[string]*VerificationCheckResult, len(checks))
	for i := range checks {
		c := &checks[i]
		byRequirement[strings.TrimSpace(c.Requirement)] = c
	}
	for _, item := range required {
		key := strings.TrimSpace(item.Requirement)
		if key == "" {
			continue
		}
		if _, ok := byRequirement[key]; ok {
			continue
		}
		name := item.Name
		if name == "" {
			name = key
		}
		result.Findings = append(result.Findings, ReportGateFinding{
			CheckName: name,
			Category:  GateCategorySchema,
			Kind:      KindMissingRequired,
			Detail:    fmt.Sprintf("required verification item is missing from the report (requirement: %q)", key),
		})
	}
}

func validateContractBackedChecks(result *ReportGateResult, report *VerificationReport, checks []VerificationCheckResult, contract *TestingContract) {
	if report.ContractRevision != contract.Revision {
		result.Findings = append(result.Findings, ReportGateFinding{
			Category: GateCategorySchema,
			Kind:     KindStaleRevision,
			Detail:   fmt.Sprintf("verification report references contract revision %d, but the latest testing contract is revision %d", report.ContractRevision, contract.Revision),
		})
	}

	byID := make(map[string]*VerificationCheckResult, len(checks))
	for i := range checks {
		c := &checks[i]
		if itemID := strings.TrimSpace(c.ItemID); itemID != "" {
			byID[itemID] = c
		}
	}
	for _, item := range contract.Items {
		if !item.Policy.Required {
			continue
		}
		if _, ok := byID[item.ID]; ok {
			continue
		}
		result.Findings = append(result.Findings, ReportGateFinding{
			CheckName: item.Name,
			Category:  GateCategorySchema,
			Kind:      KindMissingRequired,
			Detail:    fmt.Sprintf("required contract item is missing from the report (item_id: %q)", item.ID),
		})
	}
	validIDs := make(map[string]TestingContractItem, len(contract.Items))
	for _, item := range contract.Items {
		validIDs[item.ID] = item
	}
	for i := range checks {
		itemID := strings.TrimSpace(checks[i].ItemID)
		if itemID == "" {
			result.Findings = append(result.Findings, ReportGateFinding{
				CheckName: checkDisplayName(&checks[i]),
				Category:  GateCategorySchema,
				Kind:      KindMissingRequired,
				Detail:    "contract-backed verification result is missing item_id",
			})
			continue
		}
		if _, ok := validIDs[itemID]; ok {
			item := validIDs[itemID]
			if checks[i].Mode == "" || checks[i].Mode == VerificationModeUnknown {
				checks[i].Mode = verificationModeForContractItem(item)
			}
			if NormalizeStatus(checks[i].Status) == VerificationStatusBlocked {
				if strings.TrimSpace(checks[i].BlockedReason) == "" {
					result.Findings = append(result.Findings, ReportGateFinding{
						CheckName: checkDisplayName(&checks[i]),
						Category:  GateCategorySchema,
						Kind:      KindBlockedReason,
						Detail:    "status is blocked but blocked_reason is empty",
					})
				}
				if !item.Policy.AllowBlocked {
					result.Findings = append(result.Findings, ReportGateFinding{
						CheckName: checkDisplayName(&checks[i]),
						Category:  GateCategorySchema,
						Kind:      KindPolicyViolation,
						Detail:    "status is blocked but the bound contract item does not allow blocked results",
					})
				}
			}
			if NormalizeStatus(checks[i].Status) == VerificationStatusWaived && !IsTestingContractItemWaived(item) {
				result.Findings = append(result.Findings, ReportGateFinding{
					CheckName: checkDisplayName(&checks[i]),
					Category:  GateCategorySchema,
					Kind:      KindPolicyViolation,
					Detail:    "status is waived but the bound contract has no user-authorized waiver for this item",
				})
			}
			if substituteID := strings.TrimSpace(checks[i].SubstituteItemID); substituteID != "" {
				if !item.Policy.AllowSubstitution {
					result.Findings = append(result.Findings, ReportGateFinding{
						CheckName: checkDisplayName(&checks[i]),
						Category:  GateCategorySchema,
						Kind:      KindPolicyViolation,
						Detail:    "substitute_item_id is set but the bound contract item does not allow substitution",
					})
				}
				if _, ok := byID[substituteID]; !ok {
					result.Findings = append(result.Findings, ReportGateFinding{
						CheckName: checkDisplayName(&checks[i]),
						Category:  GateCategorySchema,
						Kind:      KindSubstituteItem,
						Detail:    fmt.Sprintf("substitute_item_id %q does not appear as another result row in the report", substituteID),
					})
				}
			}
			continue
		}
		result.Findings = append(result.Findings, ReportGateFinding{
			CheckName: checkDisplayName(&checks[i]),
			Category:  GateCategorySchema,
			Kind:      KindUnknownItemID,
			Detail:    fmt.Sprintf("item_id %q does not exist in the bound testing contract", itemID),
		})
	}
}

// ValidateDeferralLedger performs the cross-phase deferral checks against
// the parsed `## Deferrals` section of progress.md (which is now where
// agents declare new and closed deferrals; verification-report.yaml is
// verification-only). The checks:
//
//  1. Unclosed-due-this-phase: for every entry in the existing ledger
//     whose DueByPhase == currentPhase and whose status is still open,
//     demand the iteration either close it (cite in closedDeferrals) or
//     re-defer it (emit an updated entry in deferrals with a new
//     DueByPhase and cited reason). Silent omission is a rejection.
//
//  2. Missing reason on new or re-deferred entries: every structured
//     deferrals entry this iteration emits must carry a non-empty Reason;
//     reason-less deferrals produce a thin ledger useful to no downstream
//     reader. Reject them at the source.
//
// currentPhase == 0 disables check (1) — useful during pipelines that
// haven't reached the roadmap phase boundary yet.
//
// repoName scopes the unclosed-due-this-phase enforcement via
// feature.DueForPhaseScopedTo so out-of-scope deferrals do not block this
// repo's report. Empty repoName is permissive (every entry is enforced) —
// preserves callers that pre-date per-repo gate threading.
func ValidateDeferralLedger(deferrals []feature.IncomingDeferral, closedDeferrals []string, ledger []feature.Deferral, currentPhase int, repoName string) ReportGateResult {
	var result ReportGateResult

	// Every emitted entry must carry a reason.
	for _, d := range deferrals {
		if strings.TrimSpace(d.Reason) == "" {
			result.Findings = append(result.Findings, ReportGateFinding{
				Category: GateCategoryDeferral,
				Kind:     KindDeferralMissingReason,
				Detail: fmt.Sprintf(
					"deferrals entry with description %q and due_by_phase=%d has an empty `reason` — every deferral must cite why the work is being punted. The reason is read by the target phase's planner and by the reviewer during audit of chronic re-deferrals.",
					truncate(d.Description, 80), d.DueByPhase,
				),
			})
		}
	}

	// Ledger enforcement: every ledger entry due THIS phase must be either
	// closed (cited in closedDeferrals) or re-deferred (emitted with a
	// new DueByPhase in deferrals).
	if currentPhase > 0 {
		due := feature.DueForPhaseScopedTo(ledger, currentPhase, repoName)
		closedSet := map[string]struct{}{}
		for _, id := range closedDeferrals {
			closedSet[id] = struct{}{}
		}
		redeferredSet := map[string]struct{}{}
		dueByID := map[string]struct{}{}
		for _, d := range due {
			dueByID[d.ID] = struct{}{}
		}
		for _, in := range deferrals {
			// Re-defer matching, two-path:
			//
			//  1. The agent cited a ledger ID directly (the strong path —
			//     encouraged by the carry-forward prompt and SKILL.md).
			//     The cited ID must resolve to a due-this-phase entry.
			//
			//  2. Fallback: description-hash match. For agents that
			//     re-typed the description verbatim instead of citing
			//     the ID. Tolerates drift-free re-emission but fails when
			//     the agent paraphrases.
			if in.ID != "" {
				if _, ok := dueByID[in.ID]; ok {
					redeferredSet[in.ID] = struct{}{}
					continue
				}
			}
			for _, d := range due {
				if d.ID == feature.DeferralID(d.CreatedInPhase, in.Description) {
					redeferredSet[d.ID] = struct{}{}
					break
				}
			}
		}
		for _, d := range due {
			if _, ok := closedSet[d.ID]; ok {
				continue
			}
			if _, ok := redeferredSet[d.ID]; ok {
				continue
			}
			result.Findings = append(result.Findings, ReportGateFinding{
				Category: GateCategoryDeferral,
				Kind:     KindDeferralUnclosed,
				Detail: fmt.Sprintf(
					"deferral %s (%q; created in phase %d) is due by this phase (%d) and is still open. Either close it — do the work, verify, and cite `%s` in `closed_deferrals:` — or re-defer it with an updated `deferrals:` entry (same description, new `due_by_phase`, cited `reason`). Silent omission lets the Tailwind failure mode recur.",
					d.ID, truncate(d.Description, 80), d.CreatedInPhase, currentPhase, d.ID,
				),
			})
		}
	}

	result.Rejected = len(result.Findings) > 0
	return result
}

// MergeGateResults concatenates findings from multiple gate runs and
// recomputes Rejected. Used by callers that run ValidateVerificationReport
// and ValidateDeferralLedger in sequence and want a single combined view.
func MergeGateResults(results ...ReportGateResult) ReportGateResult {
	var merged ReportGateResult
	for _, r := range results {
		merged.Findings = append(merged.Findings, r.Findings...)
		// KnownCaveats: first non-nil wins. Downstream surfacing treats
		// the map as advisory metadata, not load-bearing state.
		if merged.KnownCaveats == nil && r.KnownCaveats != nil {
			merged.KnownCaveats = r.KnownCaveats
		}
	}
	merged.Rejected = len(merged.Findings) > 0
	return merged
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func quotedUnique(ss []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range ss {
		k := strings.ToLower(s)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, fmt.Sprintf("%q", s))
	}
	return out
}

// FormatGateFeedback builds the markdown body written to review-feedback.md
// when the gate rejects. Output conforms to the file-based review handoff
// schema (## Findings / ## Suggestions / ## Verdict) so ParseReviewFeedback
// can re-parse it cleanly downstream.
//
// Repetitive findings are consolidated: if many checks all have the same
// failure mode (e.g. all `not_run` at phase_complete, several with empty
// evidence), the feedback shows ONE bullet that lists the affected checks
// — rather than N verbose repetitions of the same detail. Hedge-phrase and
// unknown-status findings stay per-check because each carries a distinct
// explanation.
func FormatGateFeedback(result ReportGateResult) string {
	var b strings.Builder
	b.WriteString("Your verification report did not cleanly support the SUCCESS signal. ")
	b.WriteString("A deterministic pre-review gate rejected it before the LLM reviewer was invoked, ")
	b.WriteString("so no review tokens were spent on this iteration. Address the items below and re-run.\n\n")

	schemaBullets := buildSchemaBullets(result.Findings)
	if len(schemaBullets) > 0 {
		b.WriteString("### Schema integrity\n\n")
		for _, line := range schemaBullets {
			b.WriteString(line)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	hedge := findingsOfCategory(result.Findings, GateCategoryHedge)
	if len(hedge) > 0 {
		b.WriteString("### Pass claims contradicted by their own evidence\n\n")
		for _, f := range hedge {
			writeFindingBullet(&b, f)
		}
		b.WriteString("\n")
		b.WriteString("Guidance: if a check actually failed, mark `status: failed` with honest evidence. ")
		b.WriteString("The review still runs on a `failed` report — the LLM reviewer will then assess whether the failure is truly out-of-scope. ")
		b.WriteString("Do NOT mark `passed` while your evidence text describes the failure.\n\n")
	}

	if len(result.KnownCaveats) > 0 {
		b.WriteString("### Known caveats declared in the report\n\n")
		for _, key := range sortedKeys(result.KnownCaveats) {
			fmt.Fprintf(&b, "- **%s**: %s\n", key, strings.TrimSpace(result.KnownCaveats[key]))
		}
		b.WriteString("\nCross-check each caveat against the phase plan. If the plan requires the deliverable, implement it before marking the phase complete.\n\n")
	}

	if deferralBlock := formatDeferralFindings(result.Findings); deferralBlock != "" {
		b.WriteString(deferralBlock)
	}

	return FormatStructuredReviewFeedback(
		"Report Integrity Gate — CHANGES_REQUESTED",
		strings.TrimRight(b.String(), "\n"),
		"",
		ReviewChangesRequested,
	)
}

// formatDeferralFindings renders the Cross-phase deferrals section of gate
// feedback. Findings are grouped by Kind so repeated failures of the same
// kind collapse into one block with their details listed. Returns "" when
// no deferral-category findings are present.
//
// This closes a regression introduced when F4 added GateCategoryDeferral:
// the gate would reject on deferral findings but FormatGateFeedback had
// no branch for them, producing empty review-feedback.md files containing
// only the header + status line. The agent then had no actionable content
// and the iteration loop stalled.
func formatDeferralFindings(findings []ReportGateFinding) string {
	deferrals := findingsOfCategory(findings, GateCategoryDeferral)
	if len(deferrals) == 0 {
		return ""
	}
	byKind := map[ReportGateKind][]ReportGateFinding{}
	for _, f := range deferrals {
		byKind[f.Kind] = append(byKind[f.Kind], f)
	}

	var b strings.Builder
	b.WriteString("### Cross-phase deferrals (ledger)\n\n")

	if fs := byKind[KindDeferralUnclosed]; len(fs) > 0 {
		fmt.Fprintf(&b, "**%d ledger entr%s due this phase is still open.** Either close it (do the work and cite the ID in `closed_deferrals:`) or re-defer it (emit an entry in `deferrals:` citing the ID as `id:`, with a new `due_by_phase` and a fresh `reason`):\n\n",
			len(fs), pluralY(len(fs)))
		for _, f := range fs {
			fmt.Fprintf(&b, "- %s\n", f.Detail)
		}
		b.WriteString("\n")
	}
	if fs := byKind[KindDeferralMissingReason]; len(fs) > 0 {
		fmt.Fprintf(&b, "**%d deferral%s missing a `reason`.** Every entry must cite why work is being punted (read by the target phase's planner and by chronic-slippage audits):\n\n",
			len(fs), plural(len(fs)))
		for _, f := range fs {
			fmt.Fprintf(&b, "- %s\n", f.Detail)
		}
		b.WriteString("\n")
	}
	// Forward-compat for any future deferral kinds added to the enum.
	handled := map[ReportGateKind]bool{
		KindDeferralUnclosed:      true,
		KindDeferralMissingReason: true,
	}
	for kind, fs := range byKind {
		if handled[kind] {
			continue
		}
		for _, f := range fs {
			fmt.Fprintf(&b, "- %s\n", f.Detail)
		}
	}

	return b.String()
}

// pluralY returns "y" for 1 and "ies" otherwise — shorthand for
// entry/entries.
func pluralY(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// buildSchemaBullets consolidates schema-category findings. Kinds with
// many occurrences collapse to a single actionable bullet; kinds with
// distinct per-check detail (unknown status values) render one bullet per
// finding.
func buildSchemaBullets(findings []ReportGateFinding) []string {
	schema := findingsOfCategory(findings, GateCategorySchema)
	if len(schema) == 0 {
		return nil
	}
	byKind := map[ReportGateKind][]ReportGateFinding{}
	for _, f := range schema {
		byKind[f.Kind] = append(byKind[f.Kind], f)
	}
	var out []string

	if fs := byKind[KindNotRunAtEnd]; len(fs) > 0 {
		names := collectCheckNames(fs)
		out = append(out, fmt.Sprintf(
			"- **%d verification check%s left at `status: not_run` while `phase_complete` was written.** Either run the checks and record results, or emit `## Iteration State: RETRY` in progress.md (with the unfinished work listed under `### Remaining from the plan`) instead of `SUCCESS`. Affected: %s",
			len(fs), plural(len(fs)), joinCheckNames(names),
		))
	}
	if fs := byKind[KindEmptyEvidence]; len(fs) > 0 {
		names := collectCheckNames(fs)
		out = append(out, fmt.Sprintf(
			"- **%d check%s marked `passed` with empty `evidence`.** Every passed check must include what you ran and the result. Affected: %s",
			len(fs), plural(len(fs)), joinCheckNames(names),
		))
	}
	if fs := byKind[KindMissingRequired]; len(fs) > 0 {
		names := collectCheckNames(fs)
		out = append(out, fmt.Sprintf(
			"- **%d required verification item%s missing from the report.** Add entries and record their status. Affected: %s",
			len(fs), plural(len(fs)), joinCheckNames(names),
		))
	}
	// Unknown-status findings stay per-check: each carries a distinct
	// bad status value that the agent needs to fix specifically.
	for _, f := range byKind[KindUnknownStatus] {
		out = append(out, renderFindingBullet(f))
	}
	// Any other kinds under schema (forward-compat) stay per-check.
	for kind, fs := range byKind {
		switch kind {
		case KindNotRunAtEnd, KindEmptyEvidence, KindMissingRequired, KindUnknownStatus:
			continue
		}
		for _, f := range fs {
			out = append(out, renderFindingBullet(f))
		}
	}
	return out
}

func findingsOfCategory(findings []ReportGateFinding, cat ReportGateCategory) []ReportGateFinding {
	var out []ReportGateFinding
	for _, f := range findings {
		if f.Category == cat {
			out = append(out, f)
		}
	}
	return out
}

func collectCheckNames(fs []ReportGateFinding) []string {
	seen := make(map[string]bool, len(fs))
	var names []string
	for _, f := range fs {
		n := cleanCheckName(f.CheckName)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		names = append(names, n)
	}
	return names
}

// joinCheckNames formats a list of check names for inline display. Caps at
// a sensible number so a 20+ item report doesn't produce an unreadable bullet.
func joinCheckNames(names []string) string {
	const maxShown = 6
	if len(names) <= maxShown {
		return strings.Join(quoted(names), ", ")
	}
	shown := quoted(names[:maxShown])
	return fmt.Sprintf("%s, and %d more", strings.Join(shown, ", "), len(names)-maxShown)
}

// cleanCheckName strips the leading markdown bold wrappers that agents
// sometimes include in the check name (e.g. `**Lint clean**: ...`) and
// truncates very long names for display.
func cleanCheckName(s string) string {
	s = strings.TrimSpace(s)
	// If the name is the whole "**Name**: rest of line..." shape, keep
	// only the bold portion as the display name.
	if strings.HasPrefix(s, "**") {
		if end := strings.Index(s[2:], "**"); end > 0 {
			s = s[2 : 2+end]
		}
	}
	s = strings.TrimSpace(s)
	const maxLen = 80
	if len(s) > maxLen {
		s = s[:maxLen-1] + "…"
	}
	return s
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func renderFindingBullet(f ReportGateFinding) string {
	if f.CheckName != "" {
		return fmt.Sprintf("- **%s**: %s", cleanCheckName(f.CheckName), f.Detail)
	}
	return "- " + f.Detail
}

// KnownCaveatsReviewAddendum returns a prompt-level addendum to prepend to
// the LLM reviewer's prompt when the gate did NOT reject but the report
// still declares known_caveats. The addendum tells the reviewer to
// cross-check each caveat against the phase plan rather than silently
// accept the agent's deferrals.
func KnownCaveatsReviewAddendum(result ReportGateResult) string {
	if len(result.KnownCaveats) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Agent-Declared Deferrals (Integrity Gate Surfaced)\n\n")
	b.WriteString("The implementation agent declared the following caveats in its verification report, ")
	b.WriteString("framing them as out-of-scope for this phase:\n\n")
	for _, key := range sortedKeys(result.KnownCaveats) {
		fmt.Fprintf(&b, "- **%s**: %s\n", key, strings.TrimSpace(result.KnownCaveats[key]))
	}
	b.WriteString("\nFor each caveat, check the phase plan and roadmap: ")
	b.WriteString("if the plan requires this deliverable in-scope for this phase, reject with CHANGES_REQUESTED citing the specific plan line that calls it out. ")
	b.WriteString("If the plan defers it to a later phase, the deferral is acceptable.\n")
	return b.String()
}

func checkDisplayName(c *VerificationCheckResult) string {
	if strings.TrimSpace(c.Name) != "" {
		return c.Name
	}
	if strings.TrimSpace(c.Command) != "" {
		return c.Command
	}
	return c.Requirement
}

func verificationEvidenceText(c *VerificationCheckResult) string {
	if text := strings.TrimSpace(c.EvidenceData.Summary); text != "" {
		return text
	}
	return strings.TrimSpace(c.Evidence)
}

func artifactEvidenceHasPrimary(c *VerificationCheckResult) bool {
	return isArtifactEvidenceMode(c.Mode) && strings.TrimSpace(c.EvidenceData.Primary) != ""
}

func isArtifactEvidenceMode(mode VerificationMode) bool {
	return mode == VerificationModeVisual || mode == VerificationModeBehavioral
}

func findHedgePhrases(evidence string) []string {
	lower := strings.ToLower(evidence)
	var hits []string
	seen := make(map[string]bool)
	for _, phrase := range hedgePhrases {
		if strings.Contains(lower, phrase) && !seen[phrase] {
			hits = append(hits, phrase)
			seen[phrase] = true
		}
	}
	return hits
}

func writeFindingBullet(b *strings.Builder, f ReportGateFinding) {
	if f.CheckName != "" {
		fmt.Fprintf(b, "- **%s**: %s\n", cleanCheckName(f.CheckName), f.Detail)
	} else {
		fmt.Fprintf(b, "- %s\n", f.Detail)
	}
}

func quoted(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = fmt.Sprintf("%q", s)
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
