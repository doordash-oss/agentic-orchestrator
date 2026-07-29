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
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"gopkg.in/yaml.v3"
)

// NeedUserInputRecord is the harness-owned persisted gate for a deterministic
// verification capability blocker. Root-agent questions use the live
// AskUserQuestion control protocol instead.
type NeedUserInputRecord struct {
	Summary              string                            `yaml:"summary"`
	Questions            []NeedUserInputQuestion           `yaml:"questions"`
	Iteration            int                               `yaml:"iteration"`
	WaitingSince         time.Time                         `yaml:"waiting_since,omitempty"`
	VerificationDecision *NeedUserVerificationDecision     `yaml:"verification_decision,omitempty"`
	Verification         *NeedUserInputVerificationContext `yaml:"verification,omitempty"`
}

// NeedUserInputVerificationContext is the persisted, sanitized explanation
// of verification blockers attached to a harness-owned gate artifact.
type NeedUserInputVerificationContext struct {
	Blockers []NeedUserInputVerificationBlocker `yaml:"blockers"`
}

// NeedUserInputVerificationBlocker describes one blocked verification item
// without exposing executable probes or contract paths.
type NeedUserInputVerificationBlocker struct {
	ItemID       string   `yaml:"item_id"`
	Name         string   `yaml:"name"`
	RepoName     string   `yaml:"repo_name,omitempty"`
	Command      string   `yaml:"command"`
	Reason       string   `yaml:"reason"`
	Capabilities []string `yaml:"capabilities,omitempty"`
	Remediation  string   `yaml:"remediation"`
}

// NeedUserVerificationDecision is harness-authored decision context. It binds
// a pause to a particular contract revision so a user answer cannot silently
// waive a changed requirement.
type NeedUserVerificationDecision struct {
	ContractPath     string   `yaml:"contract_path"`
	ContractRevision int      `yaml:"contract_revision"`
	ItemIDs          []string `yaml:"item_ids"`
	AllowedActions   []string `yaml:"allowed_actions"`
}

const (
	NeedUserVerificationWaive          = "WAIVE"
	NeedUserVerificationRetryAfterAuth = "RETRY_AFTER_AUTH"
)

// Verification gate context limits match the public API and desktop IPC
// contracts. Trusted decision ItemIDs are intentionally not bounded by these
// display-only limits.
const (
	NeedUserInputVerificationMaxBlockers          = 100
	NeedUserInputVerificationMaxCapabilities      = 20
	NeedUserInputVerificationItemIDMaxLength      = 200
	NeedUserInputVerificationRepoNameMaxLength    = 500
	NeedUserInputVerificationContextTextMaxLength = 64 * 1024
	NeedUserInputGateMaxQuestions                 = 100
	NeedUserInputGateDisplayMaxBytes              = 1024 * 1024
	// NeedUserInputGateCollectionMaxBytes leaves two MiB of the desktop's
	// five-MiB response limit for response metadata and the other prompt queues.
	NeedUserInputGateCollectionMaxBytes = 3 * 1024 * 1024
)

// NeedUserInputQuestion is one prompt-and-answer pair the user fills in
// before resuming.
type NeedUserInputQuestion struct {
	Index  int    `yaml:"index"`
	Prompt string `yaml:"prompt"`
	Answer string `yaml:"answer"`
}

// NeedUserInputArtifactName is the canonical filename for the gate artifact
// inside an iteration directory.
const NeedUserInputArtifactName = "need-user-input.yaml"

// SynthesizeVerificationNeedUserInputGate creates a same-iteration pause for
// declared capability failures found by the deterministic executor.
func SynthesizeVerificationNeedUserInputGate(contractPath string, revision int, itemIDs []string, iteration int) NeedUserInputRecord {
	ids := append([]string(nil), itemIDs...)
	sort.Strings(ids)
	return NeedUserInputRecord{
		Summary: fmt.Sprintf("Required verification is blocked for %d item(s) by a missing capability or environment limitation.", len(ids)),
		Questions: []NeedUserInputQuestion{{
			Index:  1,
			Prompt: "Enter WAIVE to authorize waiving these blocked checks, or RETRY_AFTER_AUTH after making the required login/permission available.",
		}},
		Iteration: iteration,
		VerificationDecision: &NeedUserVerificationDecision{
			ContractPath: strings.TrimSpace(contractPath), ContractRevision: revision, ItemIDs: ids,
			AllowedActions: []string{NeedUserVerificationWaive, NeedUserVerificationRetryAfterAuth},
		},
	}
}

// SynthesizeVerificationNeedUserInputGateWithContext creates a same-iteration
// pause with sanitized, user-actionable descriptions of blocked checks.
func SynthesizeVerificationNeedUserInputGateWithContext(contractPath string, contract *TestingContract, report *VerificationReport, itemIDs []string, iteration int) NeedUserInputRecord {
	rec := SynthesizeVerificationNeedUserInputGate(contractPath, contract.Revision, itemIDs, iteration)
	itemsByID := make(map[string]TestingContractItem, len(contract.Items))
	for _, item := range contract.Items {
		itemsByID[item.ID] = item
	}
	resultsByID := make(map[string]VerificationCheckResult, len(report.Results))
	for _, result := range report.Results {
		resultsByID[result.ItemID] = result
	}

	blockers := make(
		[]NeedUserInputVerificationBlocker,
		0,
		min(len(rec.VerificationDecision.ItemIDs), NeedUserInputVerificationMaxBlockers),
	)
	for _, itemID := range rec.VerificationDecision.ItemIDs {
		if len(blockers) == NeedUserInputVerificationMaxBlockers {
			break
		}
		item := itemsByID[itemID]
		result := resultsByID[itemID]
		name := item.Name
		if name == "" {
			name = itemID
		}
		capabilities := make(
			[]string,
			0,
			min(len(item.Capabilities), NeedUserInputVerificationMaxCapabilities),
		)
		for _, capability := range item.Capabilities {
			if len(capabilities) == NeedUserInputVerificationMaxCapabilities {
				break
			}
			capabilities = append(
				capabilities,
				BoundNeedUserInputVerificationString(
					capability.Name,
					NeedUserInputVerificationContextTextMaxLength,
				),
			)
		}
		blockers = append(blockers, NeedUserInputVerificationBlocker{
			ItemID: BoundNeedUserInputVerificationString(
				itemID,
				NeedUserInputVerificationItemIDMaxLength,
			),
			Name: BoundNeedUserInputVerificationString(
				name,
				NeedUserInputVerificationContextTextMaxLength,
			),
			RepoName: BoundNeedUserInputVerificationString(
				item.Repo,
				NeedUserInputVerificationRepoNameMaxLength,
			),
			Command: BoundNeedUserInputVerificationString(
				item.Command,
				NeedUserInputVerificationContextTextMaxLength,
			),
			Reason: BoundNeedUserInputVerificationString(
				result.BlockedReason,
				NeedUserInputVerificationContextTextMaxLength,
			),
			Capabilities: capabilities,
			Remediation: BoundNeedUserInputVerificationString(
				verificationBlockerRemediation(result.BlockedReason, capabilities),
				NeedUserInputVerificationContextTextMaxLength,
			),
		})
	}
	if len(blockers) > 0 {
		rec.Verification = &NeedUserInputVerificationContext{Blockers: blockers}
	}
	return rec
}

// BoundNeedUserInputVerificationString returns valid UTF-8 whose UTF-16 code
// unit count does not exceed maxLength, matching JavaScript string validation.
// A truncated value includes its ellipsis within that limit.
func BoundNeedUserInputVerificationString(value string, maxLength int) string {
	if maxLength <= 0 {
		return ""
	}
	runes := []rune(value)
	length := 0
	for _, r := range runes {
		length += utf16.RuneLen(r)
	}
	if length <= maxLength {
		return string(runes)
	}
	if maxLength == 1 {
		return "…"
	}
	limit := maxLength - 1
	length = 0
	end := 0
	for i, r := range runes {
		runeLength := utf16.RuneLen(r)
		if length+runeLength > limit {
			break
		}
		length += runeLength
		end = i + 1
	}
	return string(runes[:end]) + "…"
}

func verificationBlockerRemediation(reason string, capabilities []string) string {
	if strings.Contains(reason, "missing declared capability") {
		return fmt.Sprintf("Make %s available, then retry verification.", strings.Join(capabilities, ", "))
	}
	return "Resolve the environment limitation described above, then retry verification."
}

// ApplyNeedUserVerificationDecision applies a user-authorized waiver, or
// leaves the contract unchanged for an after-auth retry. The caller must only
// invoke this for a trusted harness-authored gate artifact.
func ApplyNeedUserVerificationDecision(rec NeedUserInputRecord) error {
	decision := rec.VerificationDecision
	if decision == nil {
		return errors.New("need-user-input gate is not a harness verification decision")
	}
	action, err := needUserVerificationAction(rec)
	if err != nil {
		return err
	}
	if action == NeedUserVerificationRetryAfterAuth {
		return nil
	}
	contract, err := ReadTestingContract(decision.ContractPath)
	if err != nil {
		return fmt.Errorf("reading verification decision contract: %w", err)
	}
	if contract.Revision == decision.ContractRevision+1 && verificationWaiverAlreadyApplied(contract, decision.ItemIDs) {
		return nil
	}
	if contract.Revision != decision.ContractRevision {
		return fmt.Errorf("verification contract changed from revision %d to %d; review the updated requirements before resuming", decision.ContractRevision, contract.Revision)
	}
	changes := make([]TestingContractChange, 0, len(decision.ItemIDs))
	for _, itemID := range decision.ItemIDs {
		changes = append(changes, TestingContractChange{
			ItemID: itemID, Action: TestingContractChangeWaive,
			ChangeReason: "user authorized waiver at verification capability gate", ChangedBy: "user",
		})
	}
	revised, err := ReviseTestingContract(contract, changes)
	if err != nil {
		return err
	}
	return WriteTestingContract(decision.ContractPath, *revised)
}

func needUserVerificationAction(rec NeedUserInputRecord) (string, error) {
	if rec.VerificationDecision == nil {
		return "", errors.New("verification gate has no decision context")
	}
	if len(rec.Questions) != 1 {
		return "", errors.New("verification gate must contain exactly one decision question")
	}
	action := strings.ToUpper(strings.TrimSpace(rec.Questions[0].Answer))
	allowed := false
	for _, candidate := range rec.VerificationDecision.AllowedActions {
		allowed = allowed || action == strings.ToUpper(strings.TrimSpace(candidate))
	}
	if !allowed {
		return "", fmt.Errorf("verification gate answer %q is not one of %s", action, strings.Join(rec.VerificationDecision.AllowedActions, ", "))
	}
	return action, nil
}

func verificationWaiverAlreadyApplied(contract *TestingContract, itemIDs []string) bool {
	for _, itemID := range itemIDs {
		idx := testingContractItemIndex(contract.Items, itemID)
		if idx < 0 || !IsTestingContractItemWaived(contract.Items[idx]) {
			return false
		}
	}
	return true
}

// NeedUserInputPath returns the absolute path of the gate artifact for the
// supplied iteration directory.
func NeedUserInputPath(iterDir string) string {
	return filepath.Join(iterDir, NeedUserInputArtifactName)
}

// WriteNeedUserInputRecord serialises rec as YAML and writes it to path.
func WriteNeedUserInputRecord(path string, rec NeedUserInputRecord) error {
	if rec.WaitingSince.IsZero() {
		if info, err := os.Stat(path); err == nil {
			rec.WaitingSince = info.ModTime().UTC()
		} else {
			rec.WaitingSince = time.Now().UTC()
		}
	}
	data, err := yaml.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal need-user-input: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir for need-user-input: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// ReadNeedUserInputRecord loads the gate artifact at path.
func ReadNeedUserInputRecord(path string) (NeedUserInputRecord, error) {
	var rec NeedUserInputRecord
	data, err := os.ReadFile(path)
	if err != nil {
		return rec, err
	}
	if err := yaml.Unmarshal(data, &rec); err != nil {
		return rec, fmt.Errorf("parse need-user-input: %w", err)
	}
	return rec, nil
}

// AllAnswered reports whether every question has a non-empty answer.
// Returns false on a record with no questions to keep resume safe — the
// gate must collect at least one structured answer before resume.
func (r NeedUserInputRecord) AllAnswered() bool {
	if len(r.Questions) == 0 {
		return false
	}
	for _, q := range r.Questions {
		if q.Answer == "" {
			return false
		}
	}
	return true
}
