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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "image/png"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"gopkg.in/yaml.v3"
)

const (
	VerificationClassificationPassed            = "passed"
	VerificationClassificationRegression        = "regression"
	VerificationClassificationInherited         = "inherited_failure"
	VerificationClassificationUnclassified      = "unclassified_failure"
	VerificationClassificationMissingCapability = "missing_capability"
	VerificationClassificationContractError     = "contract_error"
	// EnvironmentLimited marks host limitations (sandbox write denials,
	// profile-managed tools missing from PATH) that neither the implementer
	// nor the planner can repair; they route to the user gate as blocked.
	VerificationClassificationEnvironmentLimited = "environment_limited"
	// Flaky marks a candidate failure that did not reproduce on the
	// confirmation re-run after the baseline passed.
	VerificationClassificationFlaky = "flaky_failure"
)

// VerificationExecutionOutcome is the deterministic portion of the
// implementation report. BlockedItems require a user/environment decision;
// RegressionItems remain reviewer input and do not automatically consume a
// second implementer iteration.
type VerificationExecutionOutcome struct {
	Report          *VerificationReport
	BlockedItems    []string
	RegressionItems []string
	InheritedItems  []string
	ContractErrors  []VerificationContractError
}

// VerificationContractError is a planner-authored command defect. It is kept
// separate from capability blocks so users are never asked to waive a command
// that did not execute meaningfully.
type VerificationContractError struct {
	ItemID     string
	Repo       string
	Cwd        string
	Reason     string
	Suggestion string
}

type verificationRunRecord struct {
	RunID          string    `yaml:"run_id"`
	ItemID         string    `yaml:"item_id"`
	Kind           string    `yaml:"kind"`
	Command        string    `yaml:"command"`
	Cwd            string    `yaml:"cwd"`
	RelCwd         string    `yaml:"rel_cwd,omitempty"`
	StartedAt      time.Time `yaml:"started_at"`
	Duration       string    `yaml:"duration"`
	ExitCode       int       `yaml:"exit_code"`
	Classification string    `yaml:"classification"`
	Sandboxed      bool      `yaml:"sandboxed,omitempty"`
	BaseCommit     string    `yaml:"base_commit,omitempty"`
	StdoutPath     string    `yaml:"stdout_path"`
	StderrPath     string    `yaml:"stderr_path"`
	BaselineRunID  string    `yaml:"baseline_run_id,omitempty"`
}

type capturedVerificationRun struct {
	record verificationRunRecord
	stdout string
	stderr string
}

// VerificationProgress receives high-level execution updates: the item's
// human-readable name and a state ("running", then the final report status).
// It exists purely for user-facing progress; classification never depends on it.
type VerificationProgress func(name, state string)

type verificationProgressKey struct{}

// WithVerificationProgress attaches a progress callback for
// ExecuteTestingContract to invoke as contract items start and finish.
func WithVerificationProgress(ctx context.Context, fn VerificationProgress) context.Context {
	return context.WithValue(ctx, verificationProgressKey{}, fn)
}

func verificationProgressFromContext(ctx context.Context) VerificationProgress {
	if fn, ok := ctx.Value(verificationProgressKey{}).(VerificationProgress); ok && fn != nil {
		return fn
	}
	return func(string, string) {}
}

// ExecuteTestingContract runs only explicit contract run declarations. It
// never guesses a project command from prose and never infers an auth problem
// from arbitrary stderr: authorization is a block only when a declared
// capability probe fails.
func ExecuteTestingContract(
	ctx context.Context,
	runner ports.CommandRunner,
	contract *TestingContract,
	report *VerificationReport,
	contractPath string,
	iterationDir string,
	workspaceDir string,
	repos []feature.FeatureRepo,
) (*VerificationExecutionOutcome, error) {
	if contract == nil {
		return nil, errors.New("executing testing contract: contract is nil")
	}
	if testingContractRequiresCommandRunner(contract) && runner == nil {
		return nil, errors.New("executing testing contract: command runner is required for harness-owned commands")
	}
	if report == nil {
		stub := BuildContractVerificationReportStub(contract, contractPath)
		report = &stub
	}
	out := &VerificationExecutionOutcome{Report: report}
	report.ContractPath = strings.TrimSpace(contractPath)
	report.ContractRevision = contract.Revision
	grantRoots := loadSandboxGrantRoots(contractPath)
	unsandboxedItems := loadUnsandboxedDispositions(contractPath)
	if unsandboxedItems == nil {
		unsandboxedItems = make(map[string]bool)
	}
	emitProgress := verificationProgressFromContext(ctx)
	// The item currently marked "running" gets its final report status
	// emitted when the next item starts (or after the loop): every outcome
	// path finalizes report.Results before continuing, so the flush reads
	// the settled status without instrumenting each branch.
	lastProgressIdx := -1
	flushProgress := func() {
		if lastProgressIdx >= 0 {
			result := report.Results[lastProgressIdx]
			emitProgress(result.Name, string(result.Status))
			lastProgressIdx = -1
		}
	}
	userHome, homeErr := os.UserHomeDir()
	if homeErr != nil {
		userHome = ""
	}

	resultIndexes := make(map[string]int, len(report.Results))
	for i := range report.Results {
		resultIndexes[strings.TrimSpace(report.Results[i].ItemID)] = i
	}
	for _, item := range contract.Items {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("executing testing contract: %w", ctxErr)
		}
		idx, ok := resultIndexes[item.ID]
		if !ok {
			report.Results = append(report.Results, contractResultStub(item))
			idx = len(report.Results) - 1
			resultIndexes[item.ID] = idx
		}
		if IsTestingContractItemWaived(item) {
			report.Results[idx] = waivedContractResult(item)
			continue
		}
		if item.Owner == TestingContractOwnerAgent {
			report.Results[idx] = finalizeAgentOwnedEvidence(item, iterationDir)
			continue
		}
		if item.Run == nil || strings.TrimSpace(item.Run.Shell) == "" {
			continue
		}
		flushProgress()
		emitProgress(item.Name, "running")
		lastProgressIdx = idx

		repo, workDir, workDirErr := verificationItemWorkDir(item, workspaceDir, repos)
		if workDirErr != nil {
			recordVerificationContractError(out, report, idx, item, "", workDirErr.Error(), "Tag the item with one repository from the feature scope.")
			continue
		}
		itemEnv, evidenceDir := verificationItemEnv(item, iterationDir)
		if evidenceDir != "" {
			if mkErr := os.MkdirAll(evidenceDir, 0o755); mkErr != nil {
				itemEnv, evidenceDir = nil, ""
			}
		}
		buildWritableRoots := func() []string {
			roots := append(itemWritableWorkRoots(item, workDir, repos), grantRoots...)
			if evidenceDir != "" {
				roots = append(roots, evidenceDir)
			}
			return verificationWritableRoots(roots...)
		}
		writableRoots := buildWritableRoots()
		cwd, err := resolveVerificationCwd(workDir, item.Run.Cwd)
		if err != nil {
			recordVerificationContractError(out, report, idx, item, workDir, err.Error(), "Use a cwd relative to the tagged repository root.")
			continue
		}
		if reason := validateVerificationCwd(workDir, cwd); reason != "" {
			recordVerificationContractError(out, report, idx, item, cwd, reason, "Use an existing directory inside the tagged repository worktree.")
			continue
		}
		if reason, preflightErr := preflightVerificationShell(ctx, runner, item.Run.Shell, cwd); preflightErr != nil {
			return nil, fmt.Errorf("preflight verification item %s: %w", item.ID, preflightErr)
		} else if reason != "" {
			recordVerificationContractError(out, report, idx, item, cwd, reason, "Fix the shell command in the phase plan before verification runs.")
			continue
		}

		blocked := false
		for _, capability := range item.Capabilities {
			probe := strings.TrimSpace(capability.Probe)
			if probe == "" {
				continue
			}
			if reason, preflightErr := preflightVerificationShell(ctx, runner, probe, cwd); preflightErr != nil {
				return nil, fmt.Errorf("preflight capability probe for verification item %s: %w", item.ID, preflightErr)
			} else if reason != "" {
				recordVerificationContractError(out, report, idx, item, cwd,
					"invalid capability probe: "+strings.TrimPrefix(reason, "invalid verification shell syntax: "),
					"Fix the capability probe in the phase plan before verification runs.")
				blocked = true
				break
			}
			run, runErr := captureVerificationCommand(ctx, runner, item.ID, "capability", probe, cwd, "30s", writableRoots, true, nil)
			if runErr != nil {
				var exitCoder interface{ ExitCode() int }
				if !errors.As(runErr, &exitCoder) && !errors.Is(runErr, context.DeadlineExceeded) {
					recordVerificationContractError(out, report, idx, item, cwd,
						"capability probe shell failed to start: "+runErr.Error(),
						"Fix the capability probe in the phase plan before verification runs.")
					blocked = true
					break
				}
			}
			if runErr == nil {
				run.record.Classification = "capability_available"
			} else {
				run.record.Classification = VerificationClassificationMissingCapability
			}
			if persistErr := persistVerificationRun(contractPath, item.ID, &run); persistErr != nil {
				return nil, persistErr
			}
			if runErr != nil {
				name := strings.TrimSpace(capability.Name)
				if name == "" {
					name = probe
				}
				report.Results[idx] = machineContractResult(item, VerificationStatusBlocked, run,
					fmt.Sprintf("missing declared capability %q", name))
				out.BlockedItems = append(out.BlockedItems, item.ID)
				blocked = true
				break
			}
		}
		if blocked {
			continue
		}

		runSandboxed := !unsandboxedItems[item.ID]
		candidate, candidateErr := captureVerificationCommand(ctx, runner, item.ID, "candidate", item.Run.Shell, cwd, item.Run.Timeout, writableRoots, runSandboxed, itemEnv)
		// Sandbox self-expansion: an OS write denial on a grantable path is a
		// harness limitation, not a finding. Grant the minimal root, retry,
		// and persist so later runs (and baselines) start with it. Protected
		// paths never grant and fall through to the environment gate below.
		var grantedThisItem []string
		for candidateErr != nil && runSandboxed && len(grantedThisItem) < maxSandboxGrantsPerItem {
			_, deniedPath, denied := verificationWriteDenial(candidate, writableRoots)
			if !denied || deniedPath == "" {
				break
			}
			root, grantable := sandboxGrantRootForDeniedPath(userHome, deniedPath)
			if !grantable {
				break
			}
			candidate.record.Classification = "sandbox_write_denied"
			if err := persistVerificationRun(contractPath, item.ID, &candidate); err != nil {
				return nil, err
			}
			if err := os.MkdirAll(root, 0o755); err != nil {
				break
			}
			if err := appendSandboxGrant(contractPath, sandboxGrant{Root: root, ItemID: item.ID, DeniedPath: deniedPath}); err != nil {
				return nil, err
			}
			grantRoots = append(grantRoots, root)
			grantedThisItem = append(grantedThisItem, root)
			writableRoots = buildWritableRoots()
			candidate, candidateErr = captureVerificationCommand(ctx, runner, item.ID, "candidate", item.Run.Shell, cwd, item.Run.Timeout, writableRoots, runSandboxed, itemEnv)
		}
		if candidateErr == nil {
			candidate.record.Classification = VerificationClassificationPassed
			if err := persistVerificationRun(contractPath, item.ID, &candidate); err != nil {
				return nil, err
			}
			note := ""
			if len(grantedThisItem) > 0 {
				note = "passed after sandbox write grants: " + strings.Join(grantedThisItem, ", ")
			} else if !runSandboxed {
				note = "executed unsandboxed per recorded environment disposition"
			}
			report.Results[idx] = machineContractResult(item, VerificationStatusPassed, candidate, note)
			continue
		}
		if repo != nil {
			if defect, ok := repoScopedVerificationFailure(item.Run.Shell, repo.Name, cwd, candidate); ok {
				candidate.record.Classification = VerificationClassificationContractError
				if err := persistVerificationRun(contractPath, item.ID, &candidate); err != nil {
					return nil, err
				}
				recordVerificationRunContractError(out, report, idx, item, candidate, defect.reason, defect.suggestion)
				continue
			}
		}
		// Escalation triage: a write denial whose path only the sandbox
		// protects (user-writable outside the grant policy) gates to the
		// user; everything else is eligible for the environment differential.
		if detail, deniedPath, denied := verificationWriteDenial(candidate, writableRoots); denied && !sandboxEscalationSafe(userHome, deniedPath) {
			if err := recordEnvironmentLimitedBlock(out, report, idx, item, contractPath, &candidate,
				"verification environment denies required writes: "+detail); err != nil {
				return nil, err
			}
			continue
		}
		if tool, missing := envManagedToolNotFound(candidate); missing {
			if err := recordEnvironmentLimitedBlock(out, report, idx, item, contractPath, &candidate,
				fmt.Sprintf("required tool %q is profile-managed and not on the harness PATH", tool)); err != nil {
				return nil, err
			}
			continue
		}
		// Environment differential: vary only the sandbox and observe. An
		// unsandboxed pass with a reproducing sandboxed failure is a proven
		// environment limitation (recorded as a disposition); an unsandboxed
		// pass with a sandboxed re-pass was a flake; a second failure stands
		// in for the regression confirmation below.
		var ladderFailure *capturedVerificationRun
		if candidate.record.Sandboxed && candidate.record.ExitCode != 124 && ctx.Err() == nil {
			escalated, escalatedErr := captureVerificationCommand(ctx, runner, item.ID, "unsandboxed", item.Run.Shell, cwd, item.Run.Timeout, writableRoots, false, itemEnv)
			if escalatedErr == nil {
				confirm, confirmErr := captureVerificationCommand(ctx, runner, item.ID, "confirmation", item.Run.Shell, cwd, item.Run.Timeout, writableRoots, true, itemEnv)
				if confirmErr == nil {
					candidate.record.Classification = VerificationClassificationFlaky
					escalated.record.Classification = VerificationClassificationPassed
					confirm.record.Classification = VerificationClassificationPassed
					for _, run := range []*capturedVerificationRun{&candidate, &escalated, &confirm} {
						if err := persistVerificationRun(contractPath, item.ID, run); err != nil {
							return nil, err
						}
					}
					report.Results[idx] = machineContractResult(item, VerificationStatusPassed, confirm,
						"passed on confirmation re-run; the initial failure did not reproduce")
					continue
				}
				candidate.record.Classification = VerificationClassificationEnvironmentLimited
				confirm.record.Classification = VerificationClassificationEnvironmentLimited
				escalated.record.Classification = VerificationClassificationPassed
				for _, run := range []*capturedVerificationRun{&candidate, &confirm, &escalated} {
					if err := persistVerificationRun(contractPath, item.ID, run); err != nil {
						return nil, err
					}
				}
				if err := recordUnsandboxedDisposition(contractPath, sandboxUnsandboxedDisposition{
					ItemID: item.ID,
					Reason: "command fails under the verification sandbox and passes unsandboxed",
				}); err != nil {
					return nil, err
				}
				unsandboxedItems[item.ID] = true
				report.Results[idx] = machineContractResult(item, VerificationStatusPassed, escalated,
					"passed unsandboxed; sandboxed execution is environment-limited (disposition recorded)")
				continue
			}
			escalated.record.Classification = "unsandboxed_failure"
			if err := persistVerificationRun(contractPath, item.ID, &escalated); err != nil {
				return nil, err
			}
			ladderFailure = &escalated
		}
		candidateContractReason := verificationRuntimeContractError(candidateErr, candidate)

		classification := VerificationClassificationUnclassified
		status := VerificationStatusFailed
		note := classification
		var baseline *capturedVerificationRun
		baselineAttempted := false
		if repo != nil {
			baseCommit := strings.TrimSpace(contract.BaseCommits[repo.Name])
			if baseCommit != "" {
				baselineAttempted = true
				baseRun, baseErr := runVerificationAtBase(ctx, runner, contractPath, item, *repo, baseCommit, runSandboxed)
				if baseRun != nil {
					baseline = baseRun
					baselineContractReason := verificationRuntimeContractError(baseErr, *baseRun)
					if baseErr != nil && sameVerificationFailure(candidate, *baseRun) && candidateContractReason != "" && baselineContractReason != "" {
						candidate.record.Classification = VerificationClassificationContractError
						candidate.record.BaselineRunID = baseRun.record.RunID
						if err := persistVerificationRun(contractPath, item.ID, &candidate); err != nil {
							return nil, err
						}
						recordVerificationRunContractError(out, report, idx, item, candidate,
							candidateContractReason,
							"Correct the executable command or declare its prerequisite as an explicit capability probe.")
						continue
					}
					if baseErr != nil && sameVerificationFailure(candidate, *baseRun) && repoLocalExecutableSetupFailure(candidate) {
						note = VerificationClassificationUnclassified + ": repository-scoped executable is unavailable"
					} else if baseErr != nil && sameVerificationFailure(candidate, *baseRun) {
						classification = VerificationClassificationInherited
						status = VerificationStatusInheritedFailure
						out.InheritedItems = append(out.InheritedItems, item.ID)
					} else if baseErr == nil {
						// The baseline passed: confirm the failure once before
						// declaring a regression, so a flake does not send the
						// implementer chasing a defect that is not there. The
						// ladder's unsandboxed failure already re-observed the
						// failure, so it stands in for the confirmation.
						if ladderFailure == nil {
							confirmation, confirmationErr := captureVerificationCommand(ctx, runner, item.ID, "confirmation", item.Run.Shell, cwd, item.Run.Timeout, writableRoots, runSandboxed, itemEnv)
							confirmation.record.BaselineRunID = baseRun.record.RunID
							if confirmationErr == nil {
								candidate.record.Classification = VerificationClassificationFlaky
								candidate.record.BaselineRunID = baseRun.record.RunID
								if err := persistVerificationRun(contractPath, item.ID, &candidate); err != nil {
									return nil, err
								}
								confirmation.record.Classification = VerificationClassificationPassed
								if err := persistVerificationRun(contractPath, item.ID, &confirmation); err != nil {
									return nil, err
								}
								report.Results[idx] = machineContractResult(item, VerificationStatusPassed, confirmation,
									"passed on confirmation re-run; the initial failure did not reproduce")
								continue
							}
							confirmation.record.Classification = VerificationClassificationRegression
							if err := persistVerificationRun(contractPath, item.ID, &confirmation); err != nil {
								return nil, err
							}
						}
						classification = VerificationClassificationRegression
						out.RegressionItems = append(out.RegressionItems, item.ID)
					}
				} else if baseErr != nil {
					note = fmt.Sprintf("%s: baseline unavailable: %v", classification, baseErr)
				}
			}
		}
		if classification == VerificationClassificationUnclassified && !baselineAttempted && candidateContractReason != "" {
			candidate.record.Classification = VerificationClassificationContractError
			if err := persistVerificationRun(contractPath, item.ID, &candidate); err != nil {
				return nil, err
			}
			recordVerificationRunContractError(out, report, idx, item, candidate, candidateContractReason,
				"Correct the executable command or declare its prerequisite as an explicit capability probe.")
			continue
		}
		if classification != VerificationClassificationUnclassified {
			note = classification
		}
		candidate.record.Classification = classification
		if baseline != nil {
			candidate.record.BaselineRunID = baseline.record.RunID
		}
		if err := persistVerificationRun(contractPath, item.ID, &candidate); err != nil {
			return nil, err
		}
		report.Results[idx] = machineContractResult(item, status, candidate, note)
	}
	flushProgress()
	sort.Strings(out.BlockedItems)
	sort.Strings(out.RegressionItems)
	sort.Strings(out.InheritedItems)
	sort.Slice(out.ContractErrors, func(i, j int) bool { return out.ContractErrors[i].ItemID < out.ContractErrors[j].ItemID })
	return out, nil
}

// ReconstructVerificationOutcome rebuilds the routing outcome from a
// previously persisted report, so a stop/restart resume can reuse results
// already on disk instead of re-executing every contract command.
func ReconstructVerificationOutcome(report *VerificationReport) *VerificationExecutionOutcome {
	out := &VerificationExecutionOutcome{Report: report}
	for _, result := range report.Results {
		switch result.Status {
		case VerificationStatusBlocked:
			out.BlockedItems = append(out.BlockedItems, result.ItemID)
		case VerificationStatusInheritedFailure:
			out.InheritedItems = append(out.InheritedItems, result.ItemID)
		}
		switch result.Notes {
		case VerificationClassificationRegression:
			out.RegressionItems = append(out.RegressionItems, result.ItemID)
		case VerificationClassificationContractError:
			out.ContractErrors = append(out.ContractErrors, VerificationContractError{ItemID: result.ItemID, Reason: result.Evidence})
		}
	}
	return out
}

func finalizeAgentOwnedEvidence(item TestingContractItem, iterationDir string) VerificationCheckResult {
	result := contractResultStub(item)
	rel := strings.TrimSpace(item.ExpectedEvidence.Path)
	if rel == "" {
		result.Status = VerificationStatusFailed
		result.Evidence = "agent-owned evidence item has no path"
		result.EvidenceData.Summary = result.Evidence
		return result
	}
	path, err := evidencePathUnderIteration(iterationDir, rel)
	if err != nil {
		result.Status = VerificationStatusFailed
		result.Evidence = err.Error()
		result.EvidenceData.Summary = result.Evidence
		return result
	}
	result.EvidenceData.Primary = rel
	data, err := os.ReadFile(path)
	if err != nil {
		result.Status = VerificationStatusFailed
		result.Evidence = fmt.Sprintf("required agent-owned evidence is missing at %s", rel)
		result.EvidenceData.Summary = result.Evidence
		return result
	}
	if len(bytes.TrimSpace(data)) == 0 {
		result.Status = VerificationStatusFailed
		result.Evidence = fmt.Sprintf("required agent-owned evidence is empty at %s", rel)
		result.EvidenceData.Summary = result.Evidence
		return result
	}
	if item.Source == testingContractVisualSource || item.ExpectedEvidence.Kind == testingContractVisualKind {
		cfg, _, decodeErr := image.DecodeConfig(bytes.NewReader(data))
		if decodeErr != nil {
			result.Status = VerificationStatusFailed
			result.Evidence = fmt.Sprintf("required visual evidence at %s is not a decodable image: %v", rel, decodeErr)
			result.EvidenceData.Summary = result.Evidence
			return result
		}
		w, h := item.ExpectedEvidence.Width, item.ExpectedEvidence.Height
		if w > 0 && h > 0 &&
			!(cfg.Width == w && cfg.Height == h) &&
			!(cfg.Width == 2*w && cfg.Height == 2*h) {
			result.Status = VerificationStatusFailed
			result.Evidence = fmt.Sprintf("visual evidence at %s is %dx%d but the contract requires %dx%d (or %dx%d at 2x)",
				rel, cfg.Width, cfg.Height, w, h, 2*w, 2*h)
			result.EvidenceData.Summary = result.Evidence
			return result
		}
	}
	result.Status = VerificationStatusPassed
	result.Evidence = fmt.Sprintf("agent-owned evidence captured at %s", rel)
	result.EvidenceData.Summary = result.Evidence
	sum := sha256.Sum256(data)
	result.EvidenceData.Sha256 = hex.EncodeToString(sum[:])
	return result
}

func ValidateRequiredAgentOwnedEvidence(contract *TestingContract, iterationDir string) []ProtocolViolation {
	if contract == nil {
		return nil
	}
	var violations []ProtocolViolation
	for _, item := range contract.Items {
		if item.Owner != TestingContractOwnerAgent || !item.Policy.Required || IsTestingContractItemWaived(item) {
			continue
		}
		result := finalizeAgentOwnedEvidence(item, iterationDir)
		if result.Status == VerificationStatusPassed {
			continue
		}
		artifact := strings.TrimSpace(item.ExpectedEvidence.Path)
		if artifact == "" {
			artifact = item.ID
		}
		reason := strings.TrimSpace(result.EvidenceData.Summary)
		if reason == "" {
			reason = strings.TrimSpace(result.Evidence)
		}
		violations = append(violations, ProtocolViolation{Artifact: artifact, Reason: reason})
	}
	return violations
}

// PreflightAgentEvidence is the read-only, in-session equivalent of the
// file-backed checks the report-integrity gate runs during completion commit. It
// lets the implementer confirm — before signaling SUCCESS — that every
// required agent-owned capture is present, well-formed, correctly sized, and
// not a byte-identical copy of another contracted row. Catching those here
// costs seconds; catching them at the post-handoff gate costs a whole
// iteration (a fresh session plus a consecutive-failure increment).
func PreflightAgentEvidence(contract *TestingContract, iterationDir string) []ProtocolViolation {
	if contract == nil {
		return nil
	}
	violations := ValidateRequiredAgentOwnedEvidence(contract, iterationDir)
	digestRows := make(map[string][]string)
	for _, item := range contract.Items {
		if item.Owner != TestingContractOwnerAgent || !item.Policy.Required || IsTestingContractItemWaived(item) {
			continue
		}
		if _, ok := evidenceFileRootForContractItem(item); !ok {
			continue
		}
		result := finalizeAgentOwnedEvidence(item, iterationDir)
		if result.Status != VerificationStatusPassed || result.EvidenceData.Sha256 == "" {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = item.ID
		}
		digestRows[result.EvidenceData.Sha256] = append(digestRows[result.EvidenceData.Sha256], name)
	}
	for _, names := range digestRows {
		if len(names) < 2 {
			continue
		}
		sort.Strings(names)
		violations = append(violations, ProtocolViolation{
			Artifact: strings.Join(names, "; "),
			Reason:   "these captures are byte-identical, so they cannot each depict their own contracted surface — recapture each distinctly",
		})
	}
	return violations
}

func evidencePathUnderIteration(iterationDir, rel string) (string, error) {
	cleanRel := filepath.Clean(filepath.FromSlash(strings.TrimSpace(rel)))
	if cleanRel == "." || filepath.IsAbs(cleanRel) || strings.HasPrefix(cleanRel, ".."+string(filepath.Separator)) || cleanRel == ".." {
		return "", fmt.Errorf("agent-owned evidence path %q must stay under the iteration directory", rel)
	}
	return filepath.Join(iterationDir, cleanRel), nil
}

func contractResultStub(item TestingContractItem) VerificationCheckResult {
	return VerificationCheckResult{ItemID: item.ID, Name: item.Name, Requirement: item.Command, Command: item.Command, Mode: verificationModeForContractItem(item), Status: VerificationStatusNotRun}
}

func waivedContractResult(item TestingContractItem) VerificationCheckResult {
	reason := strings.TrimSpace(item.Disposition.Reason)
	return VerificationCheckResult{
		ItemID: item.ID, Name: item.Name, Requirement: item.Command, Command: item.Command,
		Mode: verificationModeForContractItem(item), Status: VerificationStatusWaived,
		Evidence: reason, EvidenceData: VerificationEvidence{Summary: "user-authorized waiver: " + reason}, Notes: reason,
	}
}

func machineContractResult(item TestingContractItem, status VerificationRunStatus, run capturedVerificationRun, note string) VerificationCheckResult {
	summary := fmt.Sprintf("harness run %s: exit %d (%s)", run.record.RunID, run.record.ExitCode, run.record.Classification)
	return VerificationCheckResult{
		ItemID: item.ID, Name: item.Name, Requirement: item.Command, Command: item.Command,
		Mode: verificationModeForContractItem(item), Status: status, Evidence: summary,
		EvidenceData: VerificationEvidence{
			ExitCode: &run.record.ExitCode, Summary: summary, Primary: run.record.StdoutPath,
			Attachments: []string{run.record.StderrPath, filepath.Join(filepath.Dir(run.record.StdoutPath), "run.yaml")},
		},
		Notes: note, BlockedReason: func() string {
			if status == VerificationStatusBlocked {
				return note
			}
			return ""
		}(),
	}
}

// recordEnvironmentLimitedBlock persists a run blocked by a host environment
// limitation and routes the item to the user gate.
func recordEnvironmentLimitedBlock(out *VerificationExecutionOutcome, report *VerificationReport, resultIndex int, item TestingContractItem, contractPath string, run *capturedVerificationRun, note string) error {
	run.record.Classification = VerificationClassificationEnvironmentLimited
	if err := persistVerificationRun(contractPath, item.ID, run); err != nil {
		return err
	}
	report.Results[resultIndex] = machineContractResult(item, VerificationStatusBlocked, *run, note)
	out.BlockedItems = append(out.BlockedItems, item.ID)
	return nil
}

func recordVerificationContractError(out *VerificationExecutionOutcome, report *VerificationReport, resultIndex int, item TestingContractItem, cwd, reason, suggestion string) {
	contractErr := VerificationContractError{
		ItemID: item.ID, Repo: strings.TrimSpace(item.Repo), Cwd: strings.TrimSpace(cwd),
		Reason: strings.TrimSpace(reason), Suggestion: strings.TrimSpace(suggestion),
	}
	out.ContractErrors = append(out.ContractErrors, contractErr)
	result := contractResultStub(item)
	result.Status = VerificationStatusFailed
	result.Notes = VerificationClassificationContractError
	result.Evidence = contractErr.Reason
	result.EvidenceData.Summary = contractErr.Reason
	report.Results[resultIndex] = result
}

func recordVerificationRunContractError(out *VerificationExecutionOutcome, report *VerificationReport, resultIndex int, item TestingContractItem, run capturedVerificationRun, reason, suggestion string) {
	recordVerificationContractError(out, report, resultIndex, item, run.record.Cwd, reason, suggestion)
	result := machineContractResult(item, VerificationStatusFailed, run, VerificationClassificationContractError)
	result.Notes = VerificationClassificationContractError
	result.EvidenceData.Summary += ": " + strings.TrimSpace(reason)
	report.Results[resultIndex] = result
}

// VerificationContractPlanRevisionFeedback renders all command defects in one
// revision request so the planner repairs the contract once and the harness can
// resume the already-completed implementation iteration.
func VerificationContractPlanRevisionFeedback(contractErrors []VerificationContractError) string {
	errs := append([]VerificationContractError(nil), contractErrors...)
	sort.Slice(errs, func(i, j int) bool { return errs[i].ItemID < errs[j].ItemID })
	var b strings.Builder
	b.WriteString("## Verification Contract Errors\n\n")
	b.WriteString("The implementation completed, but these planner-authored commands could not execute meaningfully. Repair every command without changing implementation scope. Repo-scoped commands run from the tagged repository root.\n")
	for _, contractErr := range errs {
		fmt.Fprintf(&b, "\n- `%s`", contractErr.ItemID)
		if contractErr.Repo != "" {
			fmt.Fprintf(&b, " repo `%s`", contractErr.Repo)
		}
		if contractErr.Cwd != "" {
			fmt.Fprintf(&b, " cwd `%s`", contractErr.Cwd)
		}
		fmt.Fprintf(&b, ": %s", contractErr.Reason)
		if contractErr.Suggestion != "" {
			fmt.Fprintf(&b, " %s", contractErr.Suggestion)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func verificationItemWorkDir(item TestingContractItem, workspaceDir string, repos []feature.FeatureRepo) (*feature.FeatureRepo, string, error) {
	name := strings.TrimSpace(item.Repo)
	if name == TestingContractCrossRepoTag {
		if strings.TrimSpace(workspaceDir) == "" {
			return nil, "", fmt.Errorf("verification item %s: cross-repo command has no workspace directory", item.ID)
		}
		return nil, workspaceDir, nil
	}
	if name == "" && len(repos) == 1 {
		name = repos[0].Name
	}
	for i := range repos {
		if !strings.EqualFold(repos[i].Name, name) {
			continue
		}
		path := strings.TrimSpace(repos[i].WorktreePath)
		if path == "" {
			path = strings.TrimSpace(repos[i].Path)
		}
		if path == "" {
			return nil, "", fmt.Errorf("verification item %s: repository %q has no worktree path", item.ID, name)
		}
		return &repos[i], path, nil
	}
	return nil, "", fmt.Errorf("verification item %s: repository %q is not in feature scope", item.ID, name)
}

// itemWritableWorkRoots returns the work directories a verification command
// may mutate. Cross-repo items span every repo worktree in scope; repo items
// get their own worktree only.
func itemWritableWorkRoots(item TestingContractItem, workDir string, repos []feature.FeatureRepo) []string {
	if strings.TrimSpace(item.Repo) != TestingContractCrossRepoTag {
		return []string{workDir}
	}
	roots := []string{workDir}
	for _, repo := range repos {
		path := strings.TrimSpace(repo.WorktreePath)
		if path == "" {
			path = strings.TrimSpace(repo.Path)
		}
		if path != "" {
			roots = append(roots, path)
		}
	}
	return roots
}

func resolveVerificationCwd(root, relative string) (string, error) {
	if strings.TrimSpace(relative) == "" || strings.TrimSpace(relative) == "." {
		return root, nil
	}
	if filepath.IsAbs(relative) {
		return "", fmt.Errorf("run cwd must be relative, got %q", relative)
	}
	clean := filepath.Clean(relative)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("run cwd escapes repository: %q", relative)
	}
	return filepath.Join(root, clean), nil
}

func validateVerificationCwd(root, cwd string) string {
	info, err := os.Stat(cwd)
	if err != nil {
		return fmt.Sprintf("verification cwd %q is unavailable: %v", cwd, err)
	}
	if !info.IsDir() {
		return fmt.Sprintf("verification cwd %q is not a directory", cwd)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Sprintf("verification repository root %q cannot be resolved: %v", root, err)
	}
	resolvedCwd, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return fmt.Sprintf("verification cwd %q cannot be resolved: %v", cwd, err)
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedCwd)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Sprintf("verification cwd %q escapes repository root %q", cwd, root)
	}
	return ""
}

type repoPrefixedCommandPath struct {
	prefixed string
	relative string
}

func repoPrefixedCommandPaths(command, repoName string) []repoPrefixedCommandPath {
	repoName = strings.TrimSpace(repoName)
	if repoName == "" {
		return nil
	}
	pattern := regexp.MustCompile(`(?:^|[^A-Za-z0-9._+@%/-])((?:\./)?` + regexp.QuoteMeta(repoName) + `/([A-Za-z0-9._+@%/-]+))`)
	matches := pattern.FindAllStringSubmatch(command, -1)
	out := make([]repoPrefixedCommandPath, 0, len(matches))
	for _, match := range matches {
		if len(match) == 3 {
			out = append(out, repoPrefixedCommandPath{prefixed: match[1], relative: match[2]})
		}
	}
	return out
}

type verificationCommandDefect struct {
	reason     string
	suggestion string
}

func repoScopedVerificationFailure(command, repoName, cwd string, run capturedVerificationRun) (verificationCommandDefect, bool) {
	repoName = strings.TrimSpace(repoName)
	if repoName == "" {
		return verificationCommandDefect{}, false
	}
	missingDiagnostic := func(value string) bool {
		return failureOutputReferencesPath(run.stderr, value)
	}
	cdPattern := regexp.MustCompile(`(?:^|[\s;&|])cd[\t ]+(?:--[\t ]+)?["']?(?:\./)?` + regexp.QuoteMeta(repoName) + `["']?(?:[\t ]|[;&|]|$)`)
	if cdPattern.MatchString(command) {
		if _, err := os.Stat(filepath.Join(cwd, repoName)); os.IsNotExist(err) && missingDiagnostic(repoName) {
			return verificationCommandDefect{
				reason:     fmt.Sprintf("command changes into repository %q even though it already runs from that repository root", repoName),
				suggestion: fmt.Sprintf("Remove the `cd %s` wrapper and run the command directly.", repoName),
			}, true
		}
	}
	for _, match := range repoPrefixedCommandPaths(command, repoName) {
		prefixed := strings.TrimPrefix(match.prefixed, "./")
		if _, err := os.Stat(filepath.Join(cwd, filepath.FromSlash(prefixed))); err == nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(cwd, filepath.FromSlash(match.relative))); err == nil && missingDiagnostic(match.prefixed) {
			return verificationCommandDefect{
				reason: fmt.Sprintf("command path %q is repository-prefixed even though the command already runs from repository %q",
					match.prefixed, repoName),
				suggestion: fmt.Sprintf("Replace %q with %q.", match.prefixed, match.relative),
			}, true
		}
	}
	return verificationCommandDefect{}, false
}

func failureOutputReferencesPath(output, path string) bool {
	path = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(path), "./"))
	if path == "" {
		return false
	}
	for _, line := range strings.Split(strings.ToLower(output), "\n") {
		if strings.Contains(line, path) && outputContainsAny(line,
			"no such file", "not found", "cannot cd", "can't cd", "cannot access", "does not exist") {
			return true
		}
	}
	return false
}

func preflightVerificationShell(ctx context.Context, runner ports.CommandRunner, command, cwd string) (string, error) {
	var stderr bytes.Buffer
	_, err := runner.Run(ctx, "/bin/sh", []string{"-n", "-c", command}, ports.CommandOpts{Dir: cwd, Stderr: &stderr})
	if err == nil {
		return "", nil
	}
	var exitCoder interface{ ExitCode() int }
	if !errors.As(err, &exitCoder) {
		return "", fmt.Errorf("shell syntax checker failed to start: %w", err)
	}
	detail := strings.TrimSpace(redactVerificationOutput(stderr.String()))
	if detail == "" {
		detail = err.Error()
	}
	return "invalid verification shell syntax: " + detail, nil
}

func captureVerificationCommand(ctx context.Context, runner ports.CommandRunner, itemID, kind, command, cwd, timeoutText string, writableRoots []string, useSandbox bool, extraEnv []string) (capturedVerificationRun, error) {
	timeout := 10 * time.Minute
	if parsed, err := time.ParseDuration(strings.TrimSpace(timeoutText)); err == nil && parsed > 0 {
		timeout = parsed
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	started := time.Now().UTC()
	sandboxed := false
	err := func() error {
		// Non-login shell: sourcing the user's profile would make runs
		// machine-dependent and pollute captured output.
		argv := []string{"/bin/sh", "-c", command}
		cleanup := func() {}
		if useSandbox {
			argv, sandboxed, cleanup = sandboxVerificationArgv(runner, argv, writableRoots)
		}
		defer cleanup()
		opts := ports.CommandOpts{Dir: cwd, Stdout: &stdout, Stderr: &stderr}
		if len(extraEnv) > 0 {
			opts.Env = append(os.Environ(), extraEnv...)
		}
		_, runErr := runner.Run(runCtx, argv[0], argv[1:], opts)
		return runErr
	}()
	exitCode := commandExitCode(err)
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		exitCode = 124
	}
	run := capturedVerificationRun{
		record: verificationRunRecord{ItemID: itemID, Kind: kind, Command: command, Cwd: cwd, StartedAt: started, Duration: time.Since(started).String(), ExitCode: exitCode, Sandboxed: sandboxed},
		stdout: redactVerificationOutput(stdout.String()), stderr: redactVerificationOutput(stderr.String()),
	}
	return run, err
}

// verificationItemEnv points harness-executed evidence commands at an
// iteration-owned directory so journey traces and screenshots land in
// never-overwritten storage instead of a mutable test-results dir.
func verificationItemEnv(item TestingContractItem, iterationDir string) ([]string, string) {
	if strings.TrimSpace(iterationDir) == "" {
		return nil, ""
	}
	var root string
	switch item.Source {
	case testingContractBehavioralSource:
		root = "behaviors"
	case testingContractVisualSource:
		root = "screenshots"
	default:
		return nil, ""
	}
	dir := filepath.Join(iterationDir, root, safeEvidenceComponent(item.ID))
	return []string{"AGENTICO_EVIDENCE_DIR=" + dir}, dir
}

// sandboxVerificationArgv wraps a planner-authored command in the platform
// write-restricted sandbox. Only the host exec runner is wrapped: fake
// runners in tests never execute on the host, and wrapping would break their
// argv expectations.
func sandboxVerificationArgv(runner ports.CommandRunner, argv []string, writableRoots []string) ([]string, bool, func()) {
	if _, ok := runner.(*execCommandRunner); !ok {
		return argv, false, func() {}
	}
	return wrapVerificationSandbox(argv, writableRoots)
}

// verificationWritableRoots lists the directories a verification command may
// write to: its repo worktree (or workspace), the system temp dir, and the
// user cache and Go toolchain dirs so builds and tests keep their caches.
// Read access is not restricted — denying reads breaks toolchains — so the
// sandbox mitigates host mutation, not exfiltration by a hostile command.
func verificationWritableRoots(workRoots ...string) []string {
	roots := make([]string, 0, len(workRoots)+3)
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		if _, err := os.Stat(dir); err != nil {
			return
		}
		roots = append(roots, dir)
	}
	for _, root := range workRoots {
		add(root)
	}
	add(os.TempDir())
	if cache, err := os.UserCacheDir(); err == nil {
		add(cache)
	}
	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, "go"))
		for _, root := range toolchainCacheRoots(home) {
			add(root)
		}
	}
	return roots
}

var verificationSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)\S+`),
	regexp.MustCompile(`(?i)((?:api[_-]?key|token|password|secret)\s*[:=]\s*)\S+`),
}

func redactVerificationOutput(value string) string {
	for _, pattern := range verificationSecretPatterns {
		value = pattern.ReplaceAllString(value, `${1}[REDACTED]`)
	}
	return value
}

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return 124
	}
	var exitCoder interface{ ExitCode() int }
	if errors.As(err, &exitCoder) {
		return exitCoder.ExitCode()
	}
	return 1
}

func verificationRuntimeContractError(err error, run capturedVerificationRun) string {
	if err == nil || errors.Is(err, context.DeadlineExceeded) || run.record.ExitCode == 124 {
		return ""
	}
	var exitCoder interface{ ExitCode() int }
	if errors.As(err, &exitCoder) {
		if repoLocalExecutableSetupFailure(run) {
			return ""
		}
		switch exitCoder.ExitCode() {
		case 126:
			if outputContainsAny(run.stderr, "permission denied", "cannot execute", "not executable") {
				return "verification command is not executable"
			}
		case 127:
			if outputContainsAny(run.stderr, "not found", "no such file or directory") {
				return "verification command was not found"
			}
		default:
			return ""
		}
		return ""
	}
	return "verification shell failed to start: " + err.Error()
}

func repoLocalExecutableSetupFailure(run capturedVerificationRun) bool {
	if run.record.ExitCode != 126 && run.record.ExitCode != 127 {
		return false
	}
	if !outputContainsAny(run.stderr, "permission denied", "cannot execute", "not executable", "not found", "no such file or directory") {
		return false
	}
	return repoLocalExecutableCommandRE.MatchString(strings.TrimSpace(run.record.Command))
}

var repoLocalExecutableCommandRE = regexp.MustCompile(`(?:^|[;&|][;&|]?[\t ]*)(?:env[\t ]+(?:[A-Za-z_][A-Za-z0-9_]*=[^\t ]+[\t ]+)*)?["']?((?:\.{1,2}/|[A-Za-z0-9._-]+/)[^\t ;&|"']+)`)

var verificationWriteDenialPath = regexp.MustCompile(`/[^\s:'"]+`)

// verificationWriteDenial reports a failure line that names a filesystem write
// denial outside every writable root. Under the write-restricted sandbox that
// pattern means the command needs writes the harness denies — an environment
// limit, not a code defect — so it must not masquerade as an inherited or
// unclassified failure. Denials on writable paths are left alone: the sandbox
// cannot be their cause.
func verificationWriteDenial(run capturedVerificationRun, writableRoots []string) (string, string, bool) {
	roots := make([]string, 0, len(writableRoots)*2)
	for _, root := range writableRoots {
		if root = strings.TrimSpace(root); root == "" {
			continue
		}
		roots = append(roots, filepath.Clean(root))
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			roots = append(roots, resolved)
		}
	}
	underRoot := func(path string) bool {
		for _, root := range roots {
			if path == root || strings.HasPrefix(path, root+string(filepath.Separator)) {
				return true
			}
		}
		return false
	}
	for _, line := range strings.Split(run.stdout+"\n"+run.stderr, "\n") {
		lower := strings.ToLower(line)
		switch {
		case strings.Contains(lower, "read-only file system"):
			for _, path := range verificationWriteDenialPath.FindAllString(line, -1) {
				if !underRoot(filepath.Clean(path)) {
					return strings.TrimSpace(line), filepath.Clean(path), true
				}
			}
			return strings.TrimSpace(line), "", true
		case strings.Contains(lower, "operation not permitted") || strings.Contains(lower, "permission denied"):
			for _, path := range verificationWriteDenialPath.FindAllString(line, -1) {
				if !underRoot(filepath.Clean(path)) {
					return strings.TrimSpace(line), filepath.Clean(path), true
				}
			}
		}
	}
	return "", "", false
}

// envManagedVerificationTools are launchers that shell profiles typically put
// on PATH. The harness runs a non-login shell, so a missing one is a host
// environment gap that neither the implementer nor the planner can repair by
// editing code or the plan.
var envManagedVerificationTools = []string{"asdf", "devbox", "direnv", "mise", "nodenv", "nvm", "pyenv", "rbenv", "rtx", "volta"}

// envManagedToolNotFound reports an exit-127 failure whose output names a
// profile-managed tool as not found, including from wrapper scripts that
// invoke the tool indirectly.
func envManagedToolNotFound(run capturedVerificationRun) (string, bool) {
	if run.record.ExitCode != 127 {
		return "", false
	}
	for _, tool := range envManagedVerificationTools {
		if outputContainsAny(run.stderr, tool+": command not found", tool+": not found", "command not found: "+tool) {
			return tool, true
		}
	}
	return "", false
}

func outputContainsAny(output string, values ...string) bool {
	output = strings.ToLower(output)
	for _, value := range values {
		if strings.Contains(output, value) {
			return true
		}
	}
	return false
}

func runVerificationAtBase(ctx context.Context, runner ports.CommandRunner, contractPath string, item TestingContractItem, repo feature.FeatureRepo, commit string, useSandbox bool) (*capturedVerificationRun, error) {
	// A baseline result is a function of (command, relative cwd, base
	// commit): the anchor is immutable, so a prior run this iteration or a
	// previous one answers the same question without another 10-minute
	// worktree run. Flaky baselines re-run only when the anchor moves.
	if cached, ok := cachedBaselineRun(contractPath, item, commit); ok {
		return cached, cachedBaselineErr(cached)
	}
	repoPath := strings.TrimSpace(repo.Path)
	if repoPath == "" {
		repoPath = strings.TrimSpace(repo.WorktreePath)
	}
	tmp, err := os.MkdirTemp("", "agentico-verification-base-")
	if err != nil {
		return nil, err
	}
	_ = os.Remove(tmp)
	defer os.RemoveAll(tmp)
	var stderr bytes.Buffer
	if _, err := runner.Run(ctx, "git", []string{"-C", repoPath, "worktree", "add", "--detach", tmp, commit}, ports.CommandOpts{Stderr: &stderr}); err != nil {
		return nil, fmt.Errorf("creating baseline worktree for %s: %s: %w", item.ID, strings.TrimSpace(stderr.String()), err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = runner.Run(cleanupCtx, "git", []string{"-C", repoPath, "worktree", "remove", "--force", tmp}, ports.CommandOpts{})
	}()
	cwd, err := resolveVerificationCwd(tmp, item.Run.Cwd)
	if err != nil {
		return nil, err
	}
	run, runErr := captureVerificationCommand(ctx, runner, item.ID, "baseline", item.Run.Shell, cwd, item.Run.Timeout,
		verificationWritableRoots(append([]string{tmp}, loadSandboxGrantRoots(contractPath)...)...), useSandbox, nil)
	run.record.Classification = "baseline"
	run.record.BaseCommit = commit
	run.record.RelCwd = strings.TrimSpace(item.Run.Cwd)
	if err := persistVerificationRun(contractPath, item.ID, &run); err != nil {
		return nil, err
	}
	return &run, runErr
}

// cachedBaselineErr reconstructs the error contract of a live baseline run
// from a cached record: nil on exit 0, an exit-coded error otherwise.
func cachedBaselineErr(run *capturedVerificationRun) error {
	if run.record.ExitCode == 0 {
		return nil
	}
	return cachedExitError{code: run.record.ExitCode}
}

type cachedExitError struct{ code int }

func (e cachedExitError) Error() string { return fmt.Sprintf("cached baseline run exited %d", e.code) }
func (e cachedExitError) ExitCode() int { return e.code }

// cachedBaselineRun returns the most recent persisted baseline run for the
// same (command, relative cwd, base commit) tuple, if any.
func cachedBaselineRun(contractPath string, item TestingContractItem, commit string) (*capturedVerificationRun, bool) {
	root := verificationEvidenceRoot(contractPath, item.ID)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, false
	}
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "run-") {
			continue
		}
		runDir := filepath.Join(root, entry.Name())
		data, readErr := os.ReadFile(filepath.Join(runDir, "run.yaml"))
		if readErr != nil {
			continue
		}
		var record verificationRunRecord
		if yaml.Unmarshal(data, &record) != nil {
			continue
		}
		if record.Kind != "baseline" || record.BaseCommit != commit ||
			record.Command != item.Run.Shell || record.RelCwd != strings.TrimSpace(item.Run.Cwd) {
			continue
		}
		stdout, outErr := os.ReadFile(record.StdoutPath)
		stderr, errErr := os.ReadFile(record.StderrPath)
		if outErr != nil || errErr != nil {
			continue
		}
		return &capturedVerificationRun{record: record, stdout: string(stdout), stderr: string(stderr)}, true
	}
	return nil, false
}

var unstableFailureText = regexp.MustCompile(`(?m)(/[^\s:]+|[A-Za-z]:\\[^\s:]+|\b\d+(?:\.\d+)?(?:ms|s|m)\b)`)

// sameVerificationFailure is a heuristic: equal exit codes plus normalized
// (volatile-scrubbed, line-sorted) output. Failures with little or no output
// — including mutual timeouts (exit 124) — can match even when the underlying
// causes differ, which biases classification toward inherited over regression.
func sameVerificationFailure(a, b capturedVerificationRun) bool {
	if a.record.ExitCode != b.record.ExitCode {
		return false
	}
	normalize := func(s string) string {
		s = unstableFailureText.ReplaceAllString(s, "<volatile>")
		lines := strings.Split(strings.ToLower(s), "\n")
		normalized := make([]string, 0, len(lines))
		for _, line := range lines {
			if line = strings.Join(strings.Fields(line), " "); line != "" {
				normalized = append(normalized, line)
			}
		}
		sort.Strings(normalized)
		return strings.Join(normalized, "\n")
	}
	return normalize(a.stdout+"\n"+a.stderr) == normalize(b.stdout+"\n"+b.stderr)
}

func verificationEvidenceRoot(contractPath, itemID string) string {
	if strings.TrimSpace(contractPath) != "" {
		return filepath.Join(filepath.Dir(contractPath), "verification-evidence", safeEvidenceComponent(itemID))
	}
	return filepath.Join(os.TempDir(), "agentico-verification-evidence", safeEvidenceComponent(itemID))
}

func persistVerificationRun(contractPath, itemID string, run *capturedVerificationRun) error {
	root := verificationEvidenceRoot(contractPath, itemID)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("creating verification evidence root: %w", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	next := 1
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "run-") {
			continue
		}
		if n, parseErr := strconv.Atoi(strings.TrimPrefix(entry.Name(), "run-")); parseErr == nil && n >= next {
			next = n + 1
		}
	}
	// Claim the run number via Mkdir: under the shared temp-dir fallback two
	// features can race the same root, so EEXIST just means try the next slot.
	var runDir string
	for {
		runDir = filepath.Join(root, fmt.Sprintf("run-%03d", next))
		mkdirErr := os.Mkdir(runDir, 0o755)
		if mkdirErr == nil {
			break
		}
		if !errors.Is(mkdirErr, os.ErrExist) {
			return mkdirErr
		}
		next++
	}
	run.record.RunID = fmt.Sprintf("%s/run-%03d", safeEvidenceComponent(itemID), next)
	run.record.StdoutPath = filepath.Join(runDir, "stdout.log")
	run.record.StderrPath = filepath.Join(runDir, "stderr.log")
	if err := os.WriteFile(run.record.StdoutPath, []byte(run.stdout), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(run.record.StderrPath, []byte(run.stderr), 0o644); err != nil {
		return err
	}
	data, err := yaml.Marshal(run.record)
	if err != nil {
		return err
	}
	tmp := filepath.Join(runDir, ".run.yaml.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(runDir, "run.yaml")); err != nil {
		return err
	}
	return nil
}

var unsafeEvidenceComponent = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func safeEvidenceComponent(value string) string {
	value = strings.TrimSpace(value)
	value = unsafeEvidenceComponent.ReplaceAllString(value, "-")
	if value == "" {
		return "unknown"
	}
	return value
}
