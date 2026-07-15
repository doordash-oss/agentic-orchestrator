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
	testingContractVersion                   = 2
	testingContractInitialRev                = 1
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
	testingContractCommandTranscriptMatcher  = "command_transcript"
	// TestingContractCrossRepoTag is the value put on `repo:` for items
	// that exercise more than one repo. The unified-flow implementer
	// dispatches such items to the main session (no per-repo Task
	// sub-agent), and the verification report cross-checks coverage
	// across all repos.
	TestingContractCrossRepoTag = "cross-repo"
)

type TestingContractSource struct {
	PlanPath string `yaml:"plan_path,omitempty"`
}

type TestingContractExpectedEvidence struct {
	Kind    string `yaml:"kind,omitempty"`
	Matcher string `yaml:"matcher,omitempty"`
	Path    string `yaml:"path,omitempty"`
}

// TestingContractRun is an opaque, language-agnostic command owned by the
// deterministic verification executor. Cwd is relative to the item's repo
// worktree; Timeout uses time.ParseDuration syntax.
type TestingContractRun struct {
	Shell   string `yaml:"shell"`
	Cwd     string `yaml:"cwd,omitempty"`
	Timeout string `yaml:"timeout,omitempty"`
}

// TestingContractCapability is an explicitly declared prerequisite. Probe is
// executed before Run; a non-zero probe result is a capability block rather
// than an implementation failure.
type TestingContractCapability struct {
	Name      string `yaml:"name"`
	Probe     string `yaml:"probe"`
	OnMissing string `yaml:"on_missing,omitempty"`
}

type TestingContractItemPolicy struct {
	Required          bool `yaml:"required"`
	AllowSubstitution bool `yaml:"allow_substitution"`
	AllowBlocked      bool `yaml:"allow_blocked"`
	AllowWaiver       bool `yaml:"allow_waiver"`
}

type TestingContractChange struct {
	ItemID           string `yaml:"item_id"`
	Action           string `yaml:"action,omitempty"`
	Supersedes       string `yaml:"supersedes,omitempty"`
	SubstituteItemID string `yaml:"substitute_item_id,omitempty"`
	ChangeReason     string `yaml:"change_reason"`
	ChangedBy        string `yaml:"changed_by"`
}

type TestingContractItemDisposition struct {
	Status    string `yaml:"status,omitempty"`
	Reason    string `yaml:"reason,omitempty"`
	ChangedBy string `yaml:"changed_by,omitempty"`
}

type TestingContract struct {
	Version       int                     `yaml:"version"`
	Revision      int                     `yaml:"revision"`
	PhaseType     string                  `yaml:"phase_type,omitempty"`
	Scope         string                  `yaml:"scope"`
	GeneratedFrom TestingContractSource   `yaml:"generated_from"`
	BaseCommits   map[string]string       `yaml:"base_commits,omitempty"`
	Items         []TestingContractItem   `yaml:"items"`
	Changes       []TestingContractChange `yaml:"changes,omitempty"`
}

type TestingContractItem struct {
	ID     string `yaml:"id"`
	Source string `yaml:"source"`
	Owner  string `yaml:"owner"`
	// Repo is the unified-flow target for this item. Values are a repo
	// name from Feature.Repos, or TestingContractCrossRepoTag for items
	// that span repos. Empty for legacy single-repo contracts compiled
	// before the multi-repo cleanup; readers fall back to the feature's
	// sole repo when this is empty in a single-repo context.
	Repo             string                          `yaml:"repo,omitempty"`
	Name             string                          `yaml:"name"`
	Command          string                          `yaml:"command"`
	Run              *TestingContractRun             `yaml:"run,omitempty"`
	Capabilities     []TestingContractCapability     `yaml:"capabilities,omitempty"`
	ExpectedEvidence TestingContractExpectedEvidence `yaml:"expected_evidence"`
	Policy           TestingContractItemPolicy       `yaml:"policy"`
	Disposition      TestingContractItemDisposition  `yaml:"disposition,omitempty"`
}

const (
	TestingContractChangeWaive       = "waive"
	TestingContractDispositionWaived = "waived"
	TestingContractOwnerHarness      = "harness"
	TestingContractOwnerAgent        = "agent"
)

func CompileTestingContract(planText, planPath, phaseType string) TestingContract {
	contract := TestingContract{
		Version:       testingContractVersion,
		Revision:      testingContractInitialRev,
		PhaseType:     strings.TrimSpace(phaseType),
		Scope:         testingContractScope(planPath),
		GeneratedFrom: TestingContractSource{PlanPath: strings.TrimSpace(planPath)},
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
			ID:           testingContractItemID(source, command),
			Source:       source,
			Name:         name,
			Command:      command,
			Run:          testingContractRunFor(source, command),
			Capabilities: testingContractCapabilitiesFor(step),
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
				Matcher: evidenceRequirementMatcher(source, description),
			},
			Policy: defaultTestingContractPolicy(source),
		})
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

	finalizeTestingContractOwnership(&contract)
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
		action := strings.ToLower(strings.TrimSpace(change.Action))
		if action != "" && action != TestingContractChangeWaive {
			return nil, fmt.Errorf("revising testing contract: unsupported action %q for %q", action, itemID)
		}
		if action == TestingContractChangeWaive {
			idx := testingContractItemIndex(revised.Items, itemID)
			if idx < 0 || !revised.Items[idx].Policy.AllowWaiver {
				return nil, fmt.Errorf("revising testing contract: item %q does not allow waiver", itemID)
			}
			revised.Items[idx].Disposition = TestingContractItemDisposition{
				Status:    TestingContractDispositionWaived,
				Reason:    strings.TrimSpace(change.ChangeReason),
				ChangedBy: strings.TrimSpace(change.ChangedBy),
			}
		}
		normalized := TestingContractChange{
			ItemID:           itemID,
			Action:           action,
			Supersedes:       strings.TrimSpace(change.Supersedes),
			SubstituteItemID: strings.TrimSpace(change.SubstituteItemID),
			ChangeReason:     strings.TrimSpace(change.ChangeReason),
			ChangedBy:        strings.TrimSpace(change.ChangedBy),
		}
		if testingContractHasChange(revised.Changes, normalized) {
			continue
		}
		revised.Changes = append(revised.Changes, normalized)
	}
	if len(revised.Changes) != len(contract.Changes) {
		revised.Revision++
	}
	return &revised, nil
}

func testingContractItemIndex(items []TestingContractItem, itemID string) int {
	for i := range items {
		if items[i].ID == itemID {
			return i
		}
	}
	return -1
}

func testingContractHasChange(changes []TestingContractChange, want TestingContractChange) bool {
	for _, change := range changes {
		if change == want {
			return true
		}
	}
	return false
}

func IsTestingContractItemWaived(item TestingContractItem) bool {
	return strings.EqualFold(strings.TrimSpace(item.Disposition.Status), TestingContractDispositionWaived) &&
		strings.EqualFold(strings.TrimSpace(item.Disposition.ChangedBy), "user")
}

// ReconcileTestingContract refreshes plan-derived item definitions while
// preserving phase-scoped base anchors and durable amendments for stable item
// IDs. A changed item set advances the revision exactly once.
func ReconcileTestingContract(existing *TestingContract, fresh TestingContract) TestingContract {
	if existing == nil {
		return fresh
	}
	merged := fresh
	merged.BaseCommits = make(map[string]string, len(existing.BaseCommits))
	for repo, commit := range existing.BaseCommits {
		merged.BaseCommits[repo] = commit
	}
	unchangedIDs := make(map[string]bool, len(merged.Items))
	for i := range merged.Items {
		if oldIdx := testingContractItemIndex(existing.Items, merged.Items[i].ID); oldIdx >= 0 {
			if testingContractItemDefinitionEqual(existing.Items[oldIdx], merged.Items[i]) {
				merged.Items[i].Disposition = existing.Items[oldIdx].Disposition
				unchangedIDs[merged.Items[i].ID] = true
			}
		}
	}
	for _, change := range existing.Changes {
		if unchangedIDs[change.ItemID] {
			merged.Changes = append(merged.Changes, change)
		}
	}
	merged.Revision = existing.Revision
	if !testingContractDefinitionsEqual(existing.Items, fresh.Items) {
		merged.Revision++
	}
	return merged
}

func testingContractDefinitionsEqual(a, b []TestingContractItem) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !testingContractItemDefinitionEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

func testingContractItemDefinitionEqual(left, right TestingContractItem) bool {
	left.Disposition = TestingContractItemDisposition{}
	right.Disposition = TestingContractItemDisposition{}
	leftYAML, _ := yaml.Marshal(left)
	rightYAML, _ := yaml.Marshal(right)
	return string(leftYAML) == string(rightYAML)
}

func WriteTestingContract(path string, contract TestingContract) error {
	data, err := yaml.Marshal(contract)
	if err != nil {
		return fmt.Errorf("marshaling testing contract: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating testing contract directory: %w", err)
	}
	// Write-then-rename so a crash mid-write cannot leave a truncated
	// contract: a partial file would silently drop BaseCommits and cause the
	// next iteration to re-anchor at post-change HEAD.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing testing contract: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
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
	finalizeTestingContractOwnership(&contract)
	return &contract, nil
}

func defaultTestingContractPolicy(source string) TestingContractItemPolicy {
	switch strings.TrimSpace(source) {
	case testingContractPlanSource:
		return TestingContractItemPolicy{Required: true, AllowSubstitution: true, AllowBlocked: true, AllowWaiver: true}
	case testingContractManualSource:
		return TestingContractItemPolicy{Required: true, AllowBlocked: true, AllowWaiver: true}
	case testingContractVisualSource, testingContractBehavioralSource:
		return TestingContractItemPolicy{Required: true, AllowBlocked: true, AllowWaiver: true}
	case testingContractCrossRepoSource:
		return TestingContractItemPolicy{Required: true, AllowSubstitution: true, AllowBlocked: true, AllowWaiver: true}
	default:
		return TestingContractItemPolicy{Required: true, AllowWaiver: true}
	}
}

// MultiRepoContractInput is the input shape for CompileTestingContractMultiRepo.
// Repos is the phase-declared repo set (typically PhaseScopeResult.Repos).
// PlanText is the phase plan markdown (parsed for per-Task tags + per-Task
// Automated Verification sections). PlanPath is recorded in
// GeneratedFrom.PlanPath for provenance.
//
// PlanLess, when true, suppresses plan-source items entirely (used by the
// rebase and review-comments cycles where there is no phase plan).
type MultiRepoContractInput struct {
	Repos     []string
	PlanText  string
	PlanPath  string
	PhaseType string
	PlanLess  bool
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
//  1. Plan-source rows: one row per (Task.Repo, command) pair found in the
//     plan's per-Task Automated Verification sections, plus the legacy
//     top-level Automated Verification block (which inherits from each
//     phase-declared repo when no Task tag scopes it). Skipped when PlanLess.
//  2. Cross-repo rows: one row per CrossRepoSteps entry, tagged
//     `repo: cross-repo`.
//  3. Manual rows: one row per top-level Manual Verification bullet,
//     tagged to the lone repo or `repo: cross-repo` for multi-repo phases.
//
// Item IDs are derived from (source + repo + command). Duplicate
// (source, repo, command) tuples are deduplicated; a cross-repo command
// that also appears in a per-Task block is emitted only as cross-repo.
func CompileTestingContractMultiRepo(in MultiRepoContractInput) TestingContract {
	contract := TestingContract{
		Version:       testingContractVersion,
		Revision:      testingContractInitialRev,
		PhaseType:     strings.TrimSpace(in.PhaseType),
		Scope:         testingContractScope(in.PlanPath),
		GeneratedFrom: TestingContractSource{PlanPath: strings.TrimSpace(in.PlanPath)},
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
			ID:           testingContractItemIDWithRepo(source, repo, command),
			Source:       source,
			Repo:         repo,
			Name:         name,
			Command:      command,
			Run:          testingContractRunFor(source, command),
			Capabilities: testingContractCapabilitiesFor(step),
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
				Matcher: evidenceRequirementMatcher(source, description),
			},
			Policy: defaultTestingContractPolicy(source),
		})
	}

	// 1) Plan-source rows. Skipped when PlanLess.
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

	// 2) Cross-repo rows.
	for _, step := range in.CrossRepoSteps {
		addItem(testingContractCrossRepoSource, TestingContractCrossRepoTag, step)
	}

	// 3) Manual, visual, and behavioral rows. These describe phase-level
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

	finalizeTestingContractOwnership(&contract)
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

func testingContractRunFor(source, command string) *TestingContractRun {
	switch strings.TrimSpace(source) {
	case testingContractPlanSource, testingContractCrossRepoSource:
		return &TestingContractRun{Shell: strings.TrimSpace(command), Cwd: ".", Timeout: "10m"}
	default:
		return nil
	}
}

func finalizeTestingContractOwnership(contract *TestingContract) {
	if contract == nil {
		return
	}
	runnable := make(map[string]bool)
	for i := range contract.Items {
		item := &contract.Items[i]
		if item.Run != nil && strings.TrimSpace(item.Run.Shell) != "" {
			item.Owner = TestingContractOwnerHarness
			runnable[normalizeVerificationCommand(item.Run.Shell)] = true
		} else {
			item.Owner = TestingContractOwnerAgent
		}
	}
	for i := range contract.Items {
		item := &contract.Items[i]
		if item.ExpectedEvidence.Matcher == testingContractCommandTranscriptMatcher {
			commands := requiredBehavioralTranscriptCommands(item.Name)
			allRunnable := len(commands) > 0
			for _, command := range commands {
				allRunnable = allRunnable && runnable[normalizeVerificationCommand(command)]
			}
			if allRunnable {
				item.Owner = TestingContractOwnerHarness
			}
		}
		item.ExpectedEvidence.Path = testingContractEvidencePath(*item)
	}
}

func testingContractEvidencePath(item TestingContractItem) string {
	switch {
	case item.ExpectedEvidence.Matcher == testingContractCommandTranscriptMatcher && item.Owner == TestingContractOwnerHarness:
		return filepath.ToSlash(filepath.Join("behaviors", "harness-"+safeEvidenceComponent(item.ID)+".log"))
	case item.Source == testingContractManualSource || item.ExpectedEvidence.Kind == testingContractManualKind:
		return filepath.ToSlash(filepath.Join("observations", safeEvidenceComponent(item.ID)+".md"))
	case item.Source == testingContractVisualSource || item.ExpectedEvidence.Kind == testingContractVisualKind:
		return filepath.ToSlash(filepath.Join("screenshots", safeEvidenceComponent(item.ID)+".png"))
	case item.Source == testingContractBehavioralSource || item.ExpectedEvidence.Kind == testingContractBehavioralKind:
		return filepath.ToSlash(filepath.Join("behaviors", safeEvidenceComponent(item.ID)+".log"))
	default:
		return ""
	}
}

func testingContractRequiresCommandRunner(contract *TestingContract) bool {
	if contract == nil {
		return false
	}
	for _, item := range contract.Items {
		if item.Owner == TestingContractOwnerHarness && item.Run != nil && strings.TrimSpace(item.Run.Shell) != "" {
			return true
		}
	}
	return false
}

func testingContractCapabilitiesFor(step VerificationStep) []TestingContractCapability {
	if len(step.Capabilities) == 0 {
		return nil
	}
	out := make([]TestingContractCapability, 0, len(step.Capabilities))
	for _, capability := range step.Capabilities {
		out = append(out, TestingContractCapability{Name: capability.Name, Probe: capability.Probe, OnMissing: "need_user_input"})
	}
	return out
}

func evidenceRequirementMatcher(source, description string) string {
	if source == testingContractBehavioralSource && len(requiredBehavioralTranscriptCommands(description)) > 0 {
		return testingContractCommandTranscriptMatcher
	}
	return testingContractEvidenceFileExistsMatcher
}

// requiredBehavioralTranscriptCommands returns the executable backtick spans
// from behavioral requirements that explicitly ask for command output. Other
// behavioral artifacts remain file-existence checks because their semantics
// are intentionally assessed by the reviewer.
func requiredBehavioralTranscriptCommands(description string) []string {
	normalized := strings.ToLower(strings.TrimSpace(description))
	if !strings.Contains(normalized, "command output") &&
		!strings.Contains(normalized, "command transcript") &&
		!strings.Contains(normalized, "stdout") &&
		!strings.Contains(normalized, "stderr") {
		return nil
	}

	spans := findBacktickSpans(description)
	commands := make([]string, 0, len(spans))
	seen := make(map[string]bool, len(spans))
	for _, span := range spans {
		command := strings.TrimSpace(description[span[2]:span[3]])
		if !looksLikeShellCommand(command) {
			continue
		}
		key := normalizeVerificationCommand(command)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		commands = append(commands, command)
	}
	if len(commands) == 0 && len(spans) == 1 {
		command := strings.TrimSpace(description[spans[0][2]:spans[0][3]])
		if command != "" {
			commands = append(commands, command)
		}
	}
	return commands
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
