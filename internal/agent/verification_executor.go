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
	machineRuns := make(map[string][]capturedVerificationRun)
	blockedCommands := make(map[string]bool)
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
			return nil, fmt.Errorf("verification item %s: %w", item.ID, err)
		}

		blocked := false
		for _, capability := range item.Capabilities {
			probe := strings.TrimSpace(capability.Probe)
			if probe == "" {
				continue
			}
			run, runErr := captureVerificationCommand(ctx, runner, item.ID, "capability", probe, cwd, "30s")
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
				blockedCommands[normalizeVerificationCommand(item.Run.Shell)] = true
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
			machineRuns[normalizeVerificationCommand(item.Run.Shell)] = append(machineRuns[normalizeVerificationCommand(item.Run.Shell)], candidate)
			continue
		}

		classification := VerificationClassificationUnclassified
		status := VerificationStatusFailed
		note := classification
		var baseline *capturedVerificationRun
		if repo != nil {
			baseCommit := strings.TrimSpace(contract.BaseCommits[repo.Name])
			if baseCommit != "" {
				baseRun, baseErr := runVerificationAtBase(ctx, runner, contractPath, item, *repo, baseCommit)
				if baseRun != nil {
					baseline = baseRun
					if baseErr != nil && sameVerificationFailure(candidate, *baseRun) {
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
		machineRuns[normalizeVerificationCommand(item.Run.Shell)] = append(machineRuns[normalizeVerificationCommand(item.Run.Shell)], candidate)
	}
	markBlockedBehavioralDependencies(contract, report, blockedCommands, &out.BlockedItems)
	if err := synthesizeBehavioralCommandEvidence(contract, report, iterationDir, machineRuns); err != nil {
		return nil, err
	}
	sort.Strings(out.BlockedItems)
	sort.Strings(out.RegressionItems)
	sort.Strings(out.InheritedItems)
	return out, nil
}

func markBlockedBehavioralDependencies(contract *TestingContract, report *VerificationReport, blockedCommands map[string]bool, blockedItems *[]string) {
	if len(blockedCommands) == 0 {
		return
	}
	indexes := make(map[string]int, len(report.Results))
	for i := range report.Results {
		indexes[report.Results[i].ItemID] = i
	}
	for _, item := range contract.Items {
		if item.Owner != TestingContractOwnerHarness || item.ExpectedEvidence.Matcher != testingContractCommandTranscriptMatcher {
			continue
		}
		blocked := false
		for _, command := range requiredBehavioralTranscriptCommands(item.Name) {
			blocked = blocked || blockedCommands[normalizeVerificationCommand(command)]
		}
		if !blocked {
			continue
		}
		if idx, ok := indexes[item.ID]; ok {
			result := contractResultStub(item)
			result.Status = VerificationStatusBlocked
			result.BlockedReason = "a required harness command is blocked by a missing declared capability"
			result.EvidenceData.Summary = result.BlockedReason
			report.Results[idx] = result
		}
		*blockedItems = append(*blockedItems, item.ID)
	}
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

func synthesizeBehavioralCommandEvidence(contract *TestingContract, report *VerificationReport, iterationDir string, runs map[string][]capturedVerificationRun) error {
	if strings.TrimSpace(iterationDir) == "" || len(runs) == 0 {
		return nil
	}
	indexes := make(map[string]int, len(report.Results))
	for i := range report.Results {
		indexes[report.Results[i].ItemID] = i
	}
	for _, item := range contract.Items {
		if item.ExpectedEvidence.Matcher != testingContractCommandTranscriptMatcher {
			continue
		}
		commands := requiredBehavioralTranscriptCommands(item.Name)
		if len(commands) == 0 {
			continue
		}
		var transcript strings.Builder
		hasFailed := false
		hasInherited := false
		allPresent := true
		for _, command := range commands {
			commandRuns := runs[normalizeVerificationCommand(command)]
			if len(commandRuns) == 0 {
				allPresent = false
				break
			}
			for _, run := range commandRuns {
				fmt.Fprintf(&transcript, "COMMAND: %s\nEXIT CODE: %d\nOUTPUT:\n%s%s\n", command, run.record.ExitCode, run.stdout, run.stderr)
				if run.record.Classification == VerificationClassificationInherited {
					hasInherited = true
				} else if run.record.ExitCode != 0 {
					hasFailed = true
				}
			}
		}
		if !allPresent {
			continue
		}
		status := VerificationStatusPassed
		if hasFailed {
			status = VerificationStatusFailed
		} else if hasInherited {
			status = VerificationStatusInheritedFailure
		}
		behaviorDir := filepath.Join(iterationDir, "behaviors")
		if err := os.MkdirAll(behaviorDir, 0o755); err != nil {
			return err
		}
		rel := strings.TrimSpace(item.ExpectedEvidence.Path)
		if rel == "" {
			return fmt.Errorf("behavioral harness item %s has no canonical evidence path", item.ID)
		}
		if err := os.WriteFile(filepath.Join(iterationDir, filepath.FromSlash(rel)), []byte(transcript.String()), 0o644); err != nil {
			return err
		}
		idx, ok := indexes[item.ID]
		if !ok {
			continue
		}
		summary := "harness-generated command transcript from machine-owned runs"
		report.Results[idx] = contractResultStub(item)
		report.Results[idx].Status = status
		report.Results[idx].Evidence = summary
		report.Results[idx].EvidenceData = VerificationEvidence{Summary: summary, Primary: rel}
	}
	return nil
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
