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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

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
	StartedAt      time.Time `yaml:"started_at"`
	Duration       string    `yaml:"duration"`
	ExitCode       int       `yaml:"exit_code"`
	Classification string    `yaml:"classification"`
	StdoutPath     string    `yaml:"stdout_path"`
	StderrPath     string    `yaml:"stderr_path"`
	BaselineRunID  string    `yaml:"baseline_run_id,omitempty"`
}

type capturedVerificationRun struct {
	record verificationRunRecord
	stdout string
	stderr string
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

	resultIndexes := make(map[string]int, len(report.Results))
	for i := range report.Results {
		resultIndexes[strings.TrimSpace(report.Results[i].ItemID)] = i
	}
	for _, item := range contract.Items {
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

		repo, workDir, err := verificationItemWorkDir(item, workspaceDir, repos)
		if err != nil {
			return nil, err
		}
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
			run, runErr := captureVerificationCommand(ctx, runner, item.ID, "capability", probe, cwd, "30s")
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

		candidate, candidateErr := captureVerificationCommand(ctx, runner, item.ID, "candidate", item.Run.Shell, cwd, item.Run.Timeout)
		if candidateErr == nil {
			candidate.record.Classification = VerificationClassificationPassed
			if err := persistVerificationRun(contractPath, item.ID, &candidate); err != nil {
				return nil, err
			}
			report.Results[idx] = machineContractResult(item, VerificationStatusPassed, candidate, "")
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
				baseRun, baseErr := runVerificationAtBase(ctx, runner, contractPath, item, *repo, baseCommit)
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
	sort.Strings(out.BlockedItems)
	sort.Strings(out.RegressionItems)
	sort.Strings(out.InheritedItems)
	sort.Slice(out.ContractErrors, func(i, j int) bool { return out.ContractErrors[i].ItemID < out.ContractErrors[j].ItemID })
	return out, nil
}

func finalizeAgentOwnedEvidence(item TestingContractItem, iterationDir string) VerificationCheckResult {
	result := contractResultStub(item)
	rel := strings.TrimSpace(item.ExpectedEvidence.Path)
	if rel == "" {
		result.Status = VerificationStatusFailed
		result.Evidence = "agent-owned evidence item has no canonical path"
		result.EvidenceData.Summary = result.Evidence
		return result
	}
	result.EvidenceData.Primary = rel
	path := filepath.Join(iterationDir, filepath.FromSlash(rel))
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
	result.Status = VerificationStatusPassed
	result.Evidence = fmt.Sprintf("agent-owned evidence captured at %s", rel)
	result.EvidenceData.Summary = result.Evidence
	return result
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
		Mode: VerificationModeCommand, Status: status, Evidence: summary,
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
		if repos[i].Name != name {
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

func captureVerificationCommand(ctx context.Context, runner ports.CommandRunner, itemID, kind, command, cwd, timeoutText string) (capturedVerificationRun, error) {
	timeout := 10 * time.Minute
	if parsed, err := time.ParseDuration(strings.TrimSpace(timeoutText)); err == nil && parsed > 0 {
		timeout = parsed
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	started := time.Now().UTC()
	err := func() error {
		// Non-login shell: sourcing the user's profile would make runs
		// machine-dependent and pollute captured output.
		_, runErr := runner.Run(runCtx, "/bin/sh", []string{"-c", command}, ports.CommandOpts{Dir: cwd, Stdout: &stdout, Stderr: &stderr})
		return runErr
	}()
	exitCode := commandExitCode(err)
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		exitCode = 124
	}
	run := capturedVerificationRun{
		record: verificationRunRecord{ItemID: itemID, Kind: kind, Command: command, Cwd: cwd, StartedAt: started, Duration: time.Since(started).String(), ExitCode: exitCode},
		stdout: redactVerificationOutput(stdout.String()), stderr: redactVerificationOutput(stderr.String()),
	}
	return run, err
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

func outputContainsAny(output string, values ...string) bool {
	output = strings.ToLower(output)
	for _, value := range values {
		if strings.Contains(output, value) {
			return true
		}
	}
	return false
}

func runVerificationAtBase(ctx context.Context, runner ports.CommandRunner, contractPath string, item TestingContractItem, repo feature.FeatureRepo, commit string) (*capturedVerificationRun, error) {
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
	run, runErr := captureVerificationCommand(ctx, runner, item.ID, "baseline", item.Run.Shell, cwd, item.Run.Timeout)
	run.record.Classification = "baseline"
	if err := persistVerificationRun(contractPath, item.ID, &run); err != nil {
		return nil, err
	}
	return &run, runErr
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

func persistVerificationRun(contractPath, itemID string, run *capturedVerificationRun) error {
	root := ""
	if strings.TrimSpace(contractPath) != "" {
		root = filepath.Join(filepath.Dir(contractPath), "verification-evidence", safeEvidenceComponent(itemID))
	} else {
		root = filepath.Join(os.TempDir(), "agentico-verification-evidence", safeEvidenceComponent(itemID))
	}
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
	runDir := filepath.Join(root, fmt.Sprintf("run-%03d", next))
	if err := os.Mkdir(runDir, 0o755); err != nil {
		return err
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
