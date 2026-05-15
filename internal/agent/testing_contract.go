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
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	testingContractVersion                   = 1
	testingContractInitialRev                = 1
	testingContractBaselineSource            = "baseline"
	testingContractPlanSource                = "plan"
	testingContractManualSource              = "manual"
	testingContractVisualSource              = "visual"
	testingContractBehavioralSource          = "behavioral"
	testingContractCrossRepoSource           = "cross-repo"
	testingContractEvidenceKind              = "command_result"
	testingContractEvidenceMatcher           = "exit_code_zero"
	testingContractManualKind                = "manual_observation"
	testingContractManualMatcher             = "non_empty_summary"
	testingContractVisualKind                = "visual_artifact"
	testingContractBehavioralKind            = "behavioral_artifact"
	testingContractEvidenceFileExistsMatcher = "file_exists"
	// TestingContractCrossRepoTag is the value put on `repo:` for items
	// that exercise more than one repo. The unified-flow implementer
	// dispatches such items to the main session (no per-repo Task
	// sub-agent), and the verification report cross-checks coverage
	// across all repos.
	TestingContractCrossRepoTag = "cross-repo"
)

type TestingContractSource struct {
	PlanPath        string `yaml:"plan_path,omitempty"`
	BaselineProfile string `yaml:"baseline_profile,omitempty"`
}

type TestingContractExpectedEvidence struct {
	Kind    string `yaml:"kind,omitempty"`
	Matcher string `yaml:"matcher,omitempty"`
}

type TestingContractItemPolicy struct {
	Required          bool `yaml:"required"`
	AllowSubstitution bool `yaml:"allow_substitution"`
	AllowBlocked      bool `yaml:"allow_blocked"`
}

type TestingContractChange struct {
	ItemID       string `yaml:"item_id"`
	Supersedes   string `yaml:"supersedes,omitempty"`
	ChangeReason string `yaml:"change_reason"`
	ChangedBy    string `yaml:"changed_by"`
}

type TestingContract struct {
	Version       int                     `yaml:"version"`
	Revision      int                     `yaml:"revision"`
	PhaseType     string                  `yaml:"phase_type,omitempty"`
	Scope         string                  `yaml:"scope"`
	GeneratedFrom TestingContractSource   `yaml:"generated_from"`
	Items         []TestingContractItem   `yaml:"items"`
	Changes       []TestingContractChange `yaml:"changes,omitempty"`
}

type TestingContractItem struct {
	ID     string `yaml:"id"`
	Source string `yaml:"source"`
	// Repo is the unified-flow target for this item. Values are a repo
	// name from Feature.Repos, or TestingContractCrossRepoTag for items
	// that span repos. Empty for legacy single-repo contracts compiled
	// before the multi-repo cleanup; readers fall back to the feature's
	// sole repo when this is empty in a single-repo context.
	Repo             string                          `yaml:"repo,omitempty"`
	Name             string                          `yaml:"name"`
	Command          string                          `yaml:"command"`
	ExpectedEvidence TestingContractExpectedEvidence `yaml:"expected_evidence"`
	Policy           TestingContractItemPolicy       `yaml:"policy"`
}

func CompileTestingContract(planText, planPath, phaseType string) TestingContract {
	return CompileTestingContractWithBaseline(planText, planPath, phaseType, testingContractBaselineName, DefaultBaselineVerificationSteps())
}

func CompileTestingContractWithBaseline(planText, planPath, phaseType, baselineProfile string, baselineSteps []VerificationStep) TestingContract {
	contract := TestingContract{
		Version:   testingContractVersion,
		Revision:  testingContractInitialRev,
		PhaseType: strings.TrimSpace(phaseType),
		Scope:     testingContractScope(planPath),
		GeneratedFrom: TestingContractSource{
			PlanPath:        strings.TrimSpace(planPath),
			BaselineProfile: strings.TrimSpace(baselineProfile),
		},
	}
	if contract.GeneratedFrom.BaselineProfile == "" {
		contract.GeneratedFrom.BaselineProfile = testingContractBaselineName
	}

	seen := make(map[string]bool)
	add := func(source string, step VerificationStep) {
		command := strings.TrimSpace(step.Command)
		if command == "" || seen[command] {
			return
		}
		seen[command] = true
		name := strings.TrimSpace(step.Description)
		if name == "" {
			name = command
		}
		contract.Items = append(contract.Items, TestingContractItem{
			ID:      testingContractItemID(source, command),
			Source:  source,
			Name:    name,
			Command: command,
			ExpectedEvidence: TestingContractExpectedEvidence{
				Kind:    testingContractEvidenceKind,
				Matcher: testingContractEvidenceMatcher,
			},
			Policy: defaultTestingContractPolicy(source),
		})
	}
	addManual := func(step ManualVerificationStep) {
		description := strings.TrimSpace(step.Description)
		if description == "" {
			return
		}
		command := manualVerificationCommand(description)
		if seen[command] {
			return
		}
		seen[command] = true
		contract.Items = append(contract.Items, TestingContractItem{
			ID:      testingContractItemID(testingContractManualSource, command),
			Source:  testingContractManualSource,
			Name:    description,
			Command: command,
			ExpectedEvidence: TestingContractExpectedEvidence{
				Kind:    testingContractManualKind,
				Matcher: testingContractManualMatcher,
			},
			Policy: defaultTestingContractPolicy(testingContractManualSource),
		})
	}
	addEvidence := func(source, kind string, step EvidenceRequirement) {
		description := strings.TrimSpace(step.Description)
		if description == "" {
			return
		}
		key := source + "\x00" + normalizeVerificationCommand(description)
		if seen[key] {
			return
		}
		seen[key] = true
		command := evidenceRequirementCommand(source, description)
		contract.Items = append(contract.Items, TestingContractItem{
			ID:      testingContractItemID(source, command),
			Source:  source,
			Name:    description,
			Command: command,
			ExpectedEvidence: TestingContractExpectedEvidence{
				Kind:    kind,
				Matcher: testingContractEvidenceFileExistsMatcher,
			},
			Policy: defaultTestingContractPolicy(source),
		})
	}

	for _, step := range baselineSteps {
		add(testingContractBaselineSource, step)
	}
	for _, step := range ParsePlanVerification(planText) {
		add(testingContractPlanSource, step)
	}
	for _, step := range ParsePlanManualVerification(planText) {
		addManual(step)
	}
	for _, step := range ParsePlanVisualEvidence(planText) {
		addEvidence(testingContractVisualSource, testingContractVisualKind, step)
	}
	for _, step := range ParsePlanBehavioralEvidence(planText) {
		addEvidence(testingContractBehavioralSource, testingContractBehavioralKind, step)
	}

	return contract
}

func ReviseTestingContract(contract *TestingContract, changes []TestingContractChange) (*TestingContract, error) {
	if contract == nil {
		return nil, fmt.Errorf("revising testing contract: contract is nil")
	}
	revised := *contract
	revised.Items = append([]TestingContractItem(nil), contract.Items...)
	revised.Changes = append([]TestingContractChange(nil), contract.Changes...)

	validIDs := make(map[string]bool, len(contract.Items))
	for _, item := range contract.Items {
		validIDs[item.ID] = true
	}
	for _, change := range changes {
		itemID := strings.TrimSpace(change.ItemID)
		if itemID == "" {
			return nil, fmt.Errorf("revising testing contract: change item_id is required")
		}
		if !validIDs[itemID] {
			return nil, fmt.Errorf("revising testing contract: unknown item_id %q", itemID)
		}
		if supersedes := strings.TrimSpace(change.Supersedes); supersedes != "" && !validIDs[supersedes] {
			return nil, fmt.Errorf("revising testing contract: unknown supersedes item_id %q", supersedes)
		}
		if strings.TrimSpace(change.ChangeReason) == "" {
			return nil, fmt.Errorf("revising testing contract: change_reason is required for %q", itemID)
		}
		if strings.TrimSpace(change.ChangedBy) == "" {
			return nil, fmt.Errorf("revising testing contract: changed_by is required for %q", itemID)
		}
		revised.Changes = append(revised.Changes, TestingContractChange{
			ItemID:       itemID,
			Supersedes:   strings.TrimSpace(change.Supersedes),
			ChangeReason: strings.TrimSpace(change.ChangeReason),
			ChangedBy:    strings.TrimSpace(change.ChangedBy),
		})
	}
	revised.Revision++
	return &revised, nil
}

func WriteTestingContract(path string, contract TestingContract) error {
	data, err := yaml.Marshal(contract)
	if err != nil {
		return fmt.Errorf("marshaling testing contract: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating testing contract directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing testing contract: %w", err)
	}
	return nil
}

func ReadTestingContract(path string) (*TestingContract, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var contract TestingContract
	if err := yaml.Unmarshal(data, &contract); err != nil {
		return nil, fmt.Errorf("parsing testing contract: %w", err)
	}
	if contract.Version == 0 {
		contract.Version = testingContractVersion
	}
	if contract.Revision == 0 {
		contract.Revision = testingContractInitialRev
	}
	for i := range contract.Items {
		if contract.Items[i].ExpectedEvidence.Kind == "" && contract.Items[i].Source == testingContractManualSource {
			contract.Items[i].ExpectedEvidence.Kind = testingContractManualKind
		}
		if contract.Items[i].ExpectedEvidence.Kind == "" && contract.Items[i].Source == testingContractVisualSource {
			contract.Items[i].ExpectedEvidence.Kind = testingContractVisualKind
		}
		if contract.Items[i].ExpectedEvidence.Kind == "" && contract.Items[i].Source == testingContractBehavioralSource {
			contract.Items[i].ExpectedEvidence.Kind = testingContractBehavioralKind
		}
		if contract.Items[i].ExpectedEvidence.Kind == "" {
			contract.Items[i].ExpectedEvidence.Kind = testingContractEvidenceKind
		}
		if contract.Items[i].ExpectedEvidence.Matcher == "" && contract.Items[i].ExpectedEvidence.Kind == testingContractManualKind {
			contract.Items[i].ExpectedEvidence.Matcher = testingContractManualMatcher
		}
		if contract.Items[i].ExpectedEvidence.Matcher == "" &&
			(contract.Items[i].ExpectedEvidence.Kind == testingContractVisualKind ||
				contract.Items[i].ExpectedEvidence.Kind == testingContractBehavioralKind) {
			contract.Items[i].ExpectedEvidence.Matcher = testingContractEvidenceFileExistsMatcher
		}
		if contract.Items[i].ExpectedEvidence.Matcher == "" {
			contract.Items[i].ExpectedEvidence.Matcher = testingContractEvidenceMatcher
		}
		if contract.Items[i].Policy == (TestingContractItemPolicy{}) {
			contract.Items[i].Policy = defaultTestingContractPolicy(contract.Items[i].Source)
		}
	}
	return &contract, nil
}

func defaultTestingContractPolicy(source string) TestingContractItemPolicy {
	switch strings.TrimSpace(source) {
	case testingContractBaselineSource:
		return TestingContractItemPolicy{Required: true}
	case testingContractPlanSource:
		return TestingContractItemPolicy{Required: true, AllowSubstitution: true, AllowBlocked: true}
	case testingContractManualSource:
		return TestingContractItemPolicy{Required: true, AllowBlocked: true}
	case testingContractVisualSource, testingContractBehavioralSource:
		return TestingContractItemPolicy{Required: true, AllowBlocked: true}
	case testingContractCrossRepoSource:
		return TestingContractItemPolicy{Required: true, AllowSubstitution: true, AllowBlocked: true}
	default:
		return TestingContractItemPolicy{Required: true}
	}
}

// MultiRepoContractInput is the input shape for CompileTestingContractMultiRepo.
// Repos is the phase-declared repo set (typically PhaseScopeResult.Repos).
// PlanText is the phase plan markdown (parsed for per-Task tags + per-Task
// Automated Verification sections). PlanPath is recorded in
// GeneratedFrom.PlanPath for provenance. PhaseType, BaselineProfile, and
// BaselineSteps mirror the existing CompileTestingContractWithBaseline shape.
//
// PlanLess, when true, suppresses plan-source items entirely (used by the
// rebase and review-comments cycles where there is no phase plan). The
// per-repo baseline rows are still emitted.
type MultiRepoContractInput struct {
	Repos           []string
	PlanText        string
	PlanPath        string
	PhaseType       string
	BaselineProfile string
	BaselineSteps   []VerificationStep
	PlanLess        bool
	// CrossRepoSteps are verification commands authored by the planner
	// under a top-level "#### Cross-Repo Verification:" heading. The
	// compiler emits these as `cross-repo` items separately from per-Task
	// commands. May be nil.
	CrossRepoSteps []VerificationStep
}

// CompileTestingContractMultiRepo emits a phase contract whose every item
// carries a `repo:` field. Used by the unified phase implementer.
//
// Emission order:
//  1. Per-repo baseline rows, one set of (build/test/lint) per phase-declared
//     repo (skipped when PlanLess is false-but-no-baseline-steps; PlanLess
//     mode still emits baseline rows because rebase/review-comments still
//     need build/test/lint coverage).
//  2. Plan-source rows: one row per (Task.Repo, command) pair found in the
//     plan's per-Task Automated Verification sections, plus the legacy
//     top-level Automated Verification block (which inherits from each
//     phase-declared repo when no Task tag scopes it). Skipped when PlanLess.
//  3. Cross-repo rows: one row per CrossRepoSteps entry, tagged
//     `repo: cross-repo`.
//  4. Manual rows: one row per top-level Manual Verification bullet,
//     tagged to the lone repo or `repo: cross-repo` for multi-repo phases.
//
// Item IDs are derived from (source + repo + command) so a baseline row in
// repo A and an identical baseline row in repo B do not collide. Duplicate
// (source, repo, command) tuples are deduplicated; a cross-repo command
// that also appears in a per-Task block is emitted only as cross-repo.
func CompileTestingContractMultiRepo(in MultiRepoContractInput) TestingContract {
	contract := TestingContract{
		Version:   testingContractVersion,
		Revision:  testingContractInitialRev,
		PhaseType: strings.TrimSpace(in.PhaseType),
		Scope:     testingContractScope(in.PlanPath),
		GeneratedFrom: TestingContractSource{
			PlanPath:        strings.TrimSpace(in.PlanPath),
			BaselineProfile: strings.TrimSpace(in.BaselineProfile),
		},
	}
	if contract.GeneratedFrom.BaselineProfile == "" {
		contract.GeneratedFrom.BaselineProfile = testingContractBaselineName
	}

	seen := make(map[string]bool)
	repos := append([]string(nil), in.Repos...)
	sort.Strings(repos)

	addItem := func(source, repo string, step VerificationStep) {
		command := strings.TrimSpace(step.Command)
		if command == "" {
			return
		}
		key := source + "\x00" + repo + "\x00" + normalizeVerificationCommand(command)
		if seen[key] {
			return
		}
		seen[key] = true
		name := strings.TrimSpace(step.Description)
		if name == "" {
			name = command
		}
		contract.Items = append(contract.Items, TestingContractItem{
			ID:      testingContractItemIDWithRepo(source, repo, command),
			Source:  source,
			Repo:    repo,
			Name:    name,
			Command: command,
			ExpectedEvidence: TestingContractExpectedEvidence{
				Kind:    testingContractEvidenceKind,
				Matcher: testingContractEvidenceMatcher,
			},
			Policy: defaultTestingContractPolicy(source),
		})
	}
	addManualItem := func(repo string, step ManualVerificationStep) {
		description := strings.TrimSpace(step.Description)
		if description == "" {
			return
		}
		command := manualVerificationCommand(description)
		key := testingContractManualSource + "\x00" + repo + "\x00" + normalizeVerificationCommand(command)
		if seen[key] {
			return
		}
		seen[key] = true
		contract.Items = append(contract.Items, TestingContractItem{
			ID:      testingContractItemIDWithRepo(testingContractManualSource, repo, command),
			Source:  testingContractManualSource,
			Repo:    repo,
			Name:    description,
			Command: command,
			ExpectedEvidence: TestingContractExpectedEvidence{
				Kind:    testingContractManualKind,
				Matcher: testingContractManualMatcher,
			},
			Policy: defaultTestingContractPolicy(testingContractManualSource),
		})
	}
	addEvidenceItem := func(source, repo, kind string, step EvidenceRequirement) {
		description := strings.TrimSpace(step.Description)
		if description == "" {
			return
		}
		key := source + "\x00" + repo + "\x00" + normalizeVerificationCommand(description)
		if seen[key] {
			return
		}
		seen[key] = true
		command := evidenceRequirementCommand(source, description)
		contract.Items = append(contract.Items, TestingContractItem{
			ID:      testingContractItemIDWithRepo(source, repo, command),
			Source:  source,
			Repo:    repo,
			Name:    description,
			Command: command,
			ExpectedEvidence: TestingContractExpectedEvidence{
				Kind:    kind,
				Matcher: testingContractEvidenceFileExistsMatcher,
			},
			Policy: defaultTestingContractPolicy(source),
		})
	}

	baseline := in.BaselineSteps
	if baseline == nil {
		baseline = DefaultBaselineVerificationSteps()
	}

	// 1) Per-repo baseline rows.
	for _, repo := range repos {
		for _, step := range baseline {
			addItem(testingContractBaselineSource, repo, step)
		}
	}

	// 2) Plan-source rows. Skipped when PlanLess.
	if !in.PlanLess {
		// Top-level "#### Automated Verification:" block (everything BEFORE
		// the `## Tasks` heading). Each command is emitted once per
		// phase-declared repo since the unified flow runs verification in
		// every selected worktree.
		preTasks, perTaskBodies := splitPlanForVerification(in.PlanText)
		topSteps := ParsePlanVerification(preTasks)
		for _, step := range topSteps {
			for _, repo := range repos {
				addItem(testingContractPlanSource, repo, step)
			}
		}

		// Per-Task Automated Verification blocks. Each Task with a `**Repo:** <name>`
		// tag contributes commands tagged to that repo only.
		for _, tb := range perTaskBodies {
			repo := strings.TrimSpace(tb.repo)
			steps := ParsePlanVerification(tb.body)
			if len(steps) == 0 {
				continue
			}
			if repo == "" {
				// Untagged tasks in a single-repo phase: route to the lone
				// repo. In multi-repo phases the structural validator
				// rejects untagged tasks; if any sneak through here we
				// emit them once per declared repo so coverage is not
				// silently dropped.
				for _, r := range repos {
					for _, step := range steps {
						addItem(testingContractPlanSource, r, step)
					}
				}
				continue
			}
			for _, step := range steps {
				addItem(testingContractPlanSource, repo, step)
			}
		}
	}

	// 3) Cross-repo rows.
	for _, step := range in.CrossRepoSteps {
		addItem(testingContractCrossRepoSource, TestingContractCrossRepoTag, step)
	}

	// 4) Manual, visual, and behavioral rows. These describe phase-level
	// observations/artifacts, so multi-repo phases carry them as cross-repo
	// items instead of duplicating the same requirement for every repo.
	if !in.PlanLess {
		phaseRepo := ""
		switch {
		case len(repos) > 1:
			phaseRepo = TestingContractCrossRepoTag
		case len(repos) == 1:
			phaseRepo = repos[0]
		}
		for _, step := range ParsePlanManualVerification(in.PlanText) {
			addManualItem(phaseRepo, step)
		}
		for _, step := range ParsePlanVisualEvidence(in.PlanText) {
			addEvidenceItem(testingContractVisualSource, phaseRepo, testingContractVisualKind, step)
		}
		for _, step := range ParsePlanBehavioralEvidence(in.PlanText) {
			addEvidenceItem(testingContractBehavioralSource, phaseRepo, testingContractBehavioralKind, step)
		}
	}

	return contract
}

// splitPlanForVerification splits the plan text into the content outside the
// first `## Tasks` section and per-Task bodies. This lets the contract compiler
// distinguish top-level Automated Verification blocks (which fan out across
// every phase-declared repo, including the new `## Success Criteria` section)
// from per-Task blocks (which inherit the Task's `**Repo:** <name>` tag).
//
// Returns the pre-tasks markdown and a slice of (repo, body) records — one
// per Task in the first `## Tasks` section. The body excludes the heading
// line itself.
func splitPlanForVerification(planText string) (string, []struct {
	repo string
	body string
}) {
	tasks := ParsePlanTasks(planText)
	outsideTasks := planText
	lines := strings.Split(planText, "\n")
	start := -1
	for i, ln := range lines {
		if strings.TrimSpace(ln) == "## Tasks" {
			start = i
			break
		}
	}
	if start >= 0 {
		end := len(lines)
		var fence fenceState
		for i := start + 1; i < len(lines); i++ {
			ln := lines[i]
			if fence.update(ln) {
				continue
			}
			if fence.inside() {
				continue
			}
			if strings.HasPrefix(ln, "## ") && !strings.HasPrefix(ln, "### ") {
				end = i
				break
			}
		}
		parts := append([]string{}, lines[:start]...)
		parts = append(parts, lines[end:]...)
		outsideTasks = strings.Join(parts, "\n")
	}
	out := make([]struct {
		repo string
		body string
	}, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, struct {
			repo string
			body string
		}{repo: t.Repo, body: strings.Join(t.Body, "\n")})
	}
	return outsideTasks, out
}

// testingContractItemIDWithRepo includes the repo in the hash so identical
// commands across different repos get distinct IDs. The legacy ID format
// (no repo) is preserved by the existing testingContractItemID helper for
// backward compatibility with non-multi-repo callers.
func testingContractItemIDWithRepo(source, repo, command string) string {
	salt := strings.TrimSpace(source) + "\x00" + strings.TrimSpace(repo) + "\x00" + normalizeVerificationCommand(command)
	sum := sha1.Sum([]byte(salt))
	return source + "_" + hex.EncodeToString(sum[:])[:12]
}

func testingContractItemID(source, command string) string {
	sum := sha1.Sum([]byte(strings.TrimSpace(source) + "\x00" + normalizeVerificationCommand(command)))
	return source + "_" + hex.EncodeToString(sum[:])[:12]
}

func manualVerificationCommand(description string) string {
	return "manual: " + strings.TrimSpace(description)
}

func evidenceRequirementCommand(source, description string) string {
	return strings.TrimSpace(source) + ": " + strings.TrimSpace(description)
}

func normalizeVerificationCommand(command string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(command)), " ")
}

func testingContractScope(planPath string) string {
	parts := strings.Split(strings.ReplaceAll(planPath, "\\", "/"), "/")
	for i, part := range parts {
		if strings.HasPrefix(part, "phase-") && i+1 < len(parts) && parts[i+1] == "plan" {
			return part
		}
	}
	return "implementation"
}
