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
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestDesignValidatorsVisualAxisIsConditional(t *testing.T) {
	withoutMockups := designValidators("")
	if len(withoutMockups) != 1 || withoutMockups[0].Template != "validate-design-integrity" {
		t.Fatalf("designValidators(no manifest) = %+v, want integrity only", withoutMockups)
	}

	withMockups := designValidators("/design/mockups/manifest.yaml")
	if len(withMockups) != 2 ||
		withMockups[0].Template != "validate-design-integrity" ||
		withMockups[1].Template != "validate-design-visual" {
		t.Fatalf("designValidators(manifest) = %+v, want integrity + visual", withMockups)
	}
}

func TestRunDesignValidationLoopResumesApprovedAttempt(t *testing.T) {
	stateDir := t.TempDir()
	f := newTestDesignFeature(filepath.Join(stateDir, "work"))
	designDir := filepath.Join(ActiveRunDir(stateDir, f), feature.PhaseDesign.DirName())
	if err := os.MkdirAll(designDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(design dir): %v", err)
	}
	if err := os.WriteFile(filepath.Join(designDir, "design.md"), []byte("# Design\n"), 0o644); err != nil {
		t.Fatalf("write design: %v", err)
	}
	if err := WritePlanAttemptMeta(designDir, PlanAttemptMeta{
		Attempt:      2,
		AgentStatus:  agentStatusSuccess,
		ReviewStatus: agentStatusApproved,
	}); err != nil {
		t.Fatalf("WritePlanAttemptMeta(): %v", err)
	}

	result, err := RunDesignValidationLoop(DesignLoopConfig{PlanLoopConfig: PlanLoopConfig{
		Feature:  f,
		StateDir: stateDir,
	}}, nil)
	if err != nil {
		t.Fatalf("RunDesignValidationLoop() error = %v", err)
	}
	if result.FinalStatus != "approved" || result.Iterations != 2 {
		t.Fatalf("RunDesignValidationLoop() = %+v, want resumed approval at attempt 2", result)
	}
}

func TestRunDesignValidationLoopApprovesIntegrityOnlyDesign(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process-backed Design loop test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	f := newTestDesignFeature(workDir)
	designDir := filepath.Join(ActiveRunDir(tmpDir, f), feature.PhaseDesign.DirName())
	for _, dir := range []string{workDir, scriptsDir, designDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", dir, err)
		}
	}

	designScript := testutil.WriteScript(t, scriptsDir, "design.sh", fmt.Sprintf(`%s
cat > %q <<'DESIGNEOF'
# Design

## Problem and Outcomes
Deliver the requested behavior.

## Final Design
Use the existing boundary.

## Contracts
- None

## User Experience
**Visual mockups:** not-required — no rendered surface changes.

## Conditional Concerns
- None beyond repository defaults

## Testing Strategy
- Verify observable behavior.

## Implementation Latitude
- Routine private details.

## Out of Scope
- Unrelated work.
DESIGNEOF
cat > %q <<'LEDGEREOF'
# Poisoned agent-authored ledger
LEDGEREOF
%s
%s
`, testutil.JSONLInit, filepath.Join(designDir, "design.md"), filepath.Join(designDir, designDecisionLedgerFile), testutil.TouchPhaseCompleteInLatestAttemptDir(designDir), testutil.JSONLSuccess))
	criticScript := testutil.WriteScript(
		t,
		scriptsDir,
		"critic.sh",
		testutil.JSONLInit+"\n"+testutil.WriteAnyValidatorApproved(tmpDir)+"\n"+testutil.JSONLSuccess+"\n",
	)

	store := feature.NewStore(tmpDir)
	if err := store.Save(f); err != nil {
		t.Fatalf("Save(feature): %v", err)
	}
	sm := session.NewManager(make(chan interface{}, 100))
	defer sm.Shutdown()

	result, err := RunDesignValidationLoop(DesignLoopConfig{PlanLoopConfig: PlanLoopConfig{
		Feature:                    f,
		FeatureStore:               store,
		StateDir:                   tmpDir,
		WorkDir:                    workDir,
		MaxAttempts:                1,
		DangerouslySkipPermissions: true,
		BuildSession:               mockBuildSession(designScript, criticScript),
	}}, sm)
	if err != nil {
		t.Fatalf("RunDesignValidationLoop() error = %v", err)
	}
	if result.FinalStatus != "approved" {
		t.Fatalf("FinalStatus = %q, want approved (LastError=%q)", result.FinalStatus, result.LastError)
	}
	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load(feature): %v", err)
	}
	if got := loaded.DesignArtifactPath(); got != filepath.Join(designDir, "design.md") {
		t.Fatalf("DesignArtifactPath() = %q, want canonical design path", got)
	}
	if got := loaded.DesignMockupsArtifactPath(); got != "" {
		t.Fatalf("DesignMockupsArtifactPath() = %q, want empty", got)
	}
	ledger, err := os.ReadFile(filepath.Join(designDir, designDecisionLedgerFile))
	if err != nil {
		t.Fatalf("read regenerated decision ledger: %v", err)
	}
	if strings.Contains(string(ledger), "Poisoned") || !strings.Contains(string(ledger), "### REQ-001") {
		t.Fatalf("agent modification of harness-owned decision ledger survived regeneration:\n%s", ledger)
	}
}

func TestRunDesignValidationLoopEscalatesMaterialCriticFindingBeforeRevision(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process-backed Design loop test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	f := newTestDesignFeature(workDir)
	designDir := filepath.Join(ActiveRunDir(tmpDir, f), feature.PhaseDesign.DirName())
	for _, dir := range []string{workDir, scriptsDir, designDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", dir, err)
		}
	}

	designScript := testutil.WriteScript(t, scriptsDir, "design.sh", fmt.Sprintf(`%s
cat > %q <<'DESIGNEOF'
# Design

## Problem and Outcomes
Deliver the requested behavior.

## Final Design
Use the selected existing boundary.

## Contracts
- None

## User Experience
**Visual mockups:** not-required — no rendered surface changes.

## Conditional Concerns
- None beyond repository defaults

## Testing Strategy
- Verify observable behavior.

## Implementation Latitude
- Routine private details.

## Out of Scope
- Unrelated work.
DESIGNEOF
%s
%s
`, testutil.JSONLInit, filepath.Join(designDir, "design.md"), testutil.TouchPhaseCompleteInLatestAttemptDir(designDir), testutil.JSONLSuccess))
	criticScript := testutil.WriteScript(
		t,
		scriptsDir,
		"critic.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteAnyValidatorChangesRequested(
				tmpDir,
				"- **High**: [DECISION_CONFLICT] [REQ-001] Observable failure: satisfying the proposed replacement would contradict the selected delivery semantics.",
			)+"\n"+
			testutil.JSONLSuccess+"\n",
	)
	buildSession, captured := capturingBuildSession(designScript, criticScript)
	store := feature.NewStore(tmpDir)
	if err := store.Save(f); err != nil {
		t.Fatalf("Save(feature): %v", err)
	}
	sm := session.NewManager(make(chan interface{}, 100))
	defer sm.Shutdown()

	result, err := RunDesignValidationLoop(DesignLoopConfig{PlanLoopConfig: PlanLoopConfig{
		Feature:                    f,
		FeatureStore:               store,
		StateDir:                   tmpDir,
		WorkDir:                    workDir,
		MaxAttempts:                2,
		DangerouslySkipPermissions: true,
		BuildSession:               buildSession,
	}}, sm)
	if err != nil {
		t.Fatalf("RunDesignValidationLoop() error = %v", err)
	}
	if result.FinalStatus != "needs_human_review" || result.Iterations != 1 {
		t.Fatalf("RunDesignValidationLoop() = %+v, want needs_human_review after attempt 1", result)
	}
	if !strings.Contains(result.LastError, "DECISION_CONFLICT") {
		t.Fatalf("LastError = %q, want DECISION_CONFLICT", result.LastError)
	}
	if len(*captured) != 2 {
		t.Fatalf("BuildSession calls = %d, want one designer and one validator with no autonomous reviser", len(*captured))
	}
	if _, err := os.Stat(filepath.Join(designDir, "attempt-02")); !os.IsNotExist(err) {
		t.Fatalf("attempt-02 should not exist after material escalation; stat error = %v", err)
	}
	history, err := os.ReadFile(filepath.Join(designDir, "attempt-01", "design.md"))
	if err != nil {
		t.Fatalf("read attempt-01 historical design: %v", err)
	}
	if !strings.Contains(string(history), "Use the selected existing boundary.") {
		t.Fatalf("historical design snapshot does not contain attempt-01 content:\n%s", history)
	}
}

func TestBuildDesignRevisionPromptCarriesAuthorityAndMaterialChangeGuard(t *testing.T) {
	f := newTestDesignFeature("/work")
	f.ExitCriteria = "No committed mutation is dropped."
	prompt := BuildDesignRevisionPrompt(
		f,
		"/design/design.md",
		"/design/mockups/manifest.yaml",
		"/research/research.md",
		"/design/decision-ledger.md",
		"Clarify the error contract.",
		3,
	)
	for _, want := range []string{
		"revision attempt 3",
		"Test Design Feature",
		"Exercise the Design validation loop",
		"No committed mutation is dropped.",
		"/design/design.md",
		"/design/mockups/manifest.yaml",
		"/research/research.md",
		"/design/decision-ledger.md",
		"Clarify the error contract.",
		"binding product behavior",
		"AskUserQuestion",
		"Human Direction",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("BuildDesignRevisionPrompt() missing %q:\n%s", want, prompt)
		}
	}
	for _, unwanted := range []string{
		"Prior Axis Approvals",
		"Sticky Approval",
		"frozen_sections",
		"Resolve all ambiguity autonomously",
		"Do NOT ask the user",
	} {
		if strings.Contains(prompt, unwanted) {
			t.Errorf("BuildDesignRevisionPrompt() unexpectedly contains %q:\n%s", unwanted, prompt)
		}
	}
}

func TestDesignReviewDispositionAllowsOnlyClassifiedNonMaterialCorrections(t *testing.T) {
	decisionIDs := map[string]struct{}{
		"REQ-001": {},
		"DEC-002": {},
		"DEC-003": {},
		"DEC-004": {},
	}
	tests := []struct {
		name           string
		findings       string
		wantHuman      bool
		wantReasonPart string
	}{
		{
			name:      "contract defect can revise",
			findings:  "- **High**: [CONTRACT_DEFECT] [DEC-003] Observable failure: the state table contradicts the selected watermark rule.",
			wantHuman: false,
		},
		{
			name:      "grounding error can revise",
			findings:  "- **High**: [GROUNDING_ERROR] [REQ-001] Observable failure: the named API does not exist at the cited repository path.",
			wantHuman: false,
		},
		{
			name:           "decision conflict escalates",
			findings:       "- **High**: [DECISION_CONFLICT] [DEC-004] Observable failure: the selected retention cannot satisfy the binding replay horizon.",
			wantHuman:      true,
			wantReasonPart: "DECISION_CONFLICT",
		},
		{
			name:           "missing material decision escalates",
			findings:       "- **High**: [MISSING_DECISION] [REQ-001] Observable failure: no owner is selected for a new persisted cross-service schema.",
			wantHuman:      true,
			wantReasonPart: "MISSING_DECISION",
		},
		{
			name:           "unclassified finding cannot drive autonomous revision",
			findings:       "- **High**: Add a dedicated outcome table and HMAC proof.",
			wantHuman:      true,
			wantReasonPart: "unclassified",
		},
		{
			name:           "missing decision reference cannot drive autonomous revision",
			findings:       "- **High**: [CONTRACT_DEFECT] Observable failure: the design is inconsistent.",
			wantHuman:      true,
			wantReasonPart: "REQ-### or DEC-###",
		},
		{
			name:           "missing observable failure cannot drive autonomous revision",
			findings:       "- **High**: [CONTRACT_DEFECT] [DEC-002] Prefer a different schema.",
			wantHuman:      true,
			wantReasonPart: "Observable failure",
		},
		{
			name:           "unknown decision reference cannot drive autonomous revision",
			findings:       "- **High**: [CONTRACT_DEFECT] [DEC-999] Observable failure: the design contradicts a decision that is not in the ledger.",
			wantHuman:      true,
			wantReasonPart: "not present",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			feedback := FormatStructuredReviewFeedback(
				"Integrity Validation",
				tt.findings,
				"",
				ReviewChangesRequested,
			)
			got := designReviewDisposition([]ValidatorResult{{
				Domain:   "Integrity",
				Status:   ReviewChangesRequested,
				Feedback: feedback,
			}}, decisionIDs)
			if got.RequiresHuman != tt.wantHuman {
				t.Fatalf("designReviewDisposition().RequiresHuman = %v, want %v (reason=%q)", got.RequiresHuman, tt.wantHuman, got.Reason)
			}
			if tt.wantReasonPart != "" && !strings.Contains(got.Reason, tt.wantReasonPart) {
				t.Fatalf("designReviewDisposition().Reason = %q, want substring %q", got.Reason, tt.wantReasonPart)
			}
		})
	}
}

func TestLinearizableCDCRegressionRoutesAttemptsToReopenHumanDecisionsToReview(t *testing.T) {
	t.Parallel()

	decisionIDs := map[string]struct{}{
		"DEC-001": {},
		"DEC-002": {},
		"DEC-003": {},
		"DEC-004": {},
		"DEC-005": {},
	}
	cases := []struct {
		name      string
		reference string
		failure   string
	}{
		{
			name:      "replace watermark proof with applied receipts",
			reference: "DEC-001",
			failure:   "requiring exact APPLIED outcomes would replace the selected state-convergent delivery semantics",
		},
		{
			name:      "reuse customer idempotency UUID as attempt identity",
			reference: "DEC-002",
			failure:   "reusing the customer idempotency UUID would contradict the selected per-proxy-invocation attempt identity",
		},
		{
			name:      "move receipts to a dedicated outcome table",
			reference: "DEC-003",
			failure:   "moving rejection receipts out of the customer K/V table would replace the selected persistence placement",
		},
		{
			name:      "look up receipt before the watermark",
			reference: "DEC-004",
			failure:   "consulting the rejection receipt first would reverse the selected watermark-first resolution order",
		},
		{
			name:      "reinstate waived acceptance requirements",
			reference: "DEC-005",
			failure:   "requiring a composed E2E harness or performance gate would reinstate explicitly waived acceptance requirements",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			feedback := fmt.Sprintf(
				"## Findings\n- **High**: [DECISION_CONFLICT] [%s] Observable failure: %s.\n\n## Suggestions\n- (none)\n\n## Verdict\nCHANGES_REQUESTED\n",
				tc.reference,
				tc.failure,
			)
			got := designReviewDisposition([]ValidatorResult{{
				Domain:   "Integrity",
				Status:   ReviewChangesRequested,
				Feedback: feedback,
			}}, decisionIDs)
			if !got.RequiresHuman {
				t.Fatalf("designReviewDisposition() = %+v, want human review before any CDC redesign", got)
			}
		})
	}
}

func TestArchiveDesignAttemptKeepsHistoryWithoutReplacingCanonicalArtifact(t *testing.T) {
	artifactDir := t.TempDir()
	canonical := filepath.Join(artifactDir, "design.md")
	attemptOne := filepath.Join(artifactDir, "attempt-01")
	attemptTwo := filepath.Join(artifactDir, "attempt-02")
	for _, dir := range []string{attemptOne, attemptTwo} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", dir, err)
		}
	}
	if err := os.WriteFile(canonical, []byte("version one\n"), 0o644); err != nil {
		t.Fatalf("write canonical v1: %v", err)
	}
	if _, err := archiveDesignAttempt(canonical, attemptOne); err != nil {
		t.Fatalf("archive attempt one: %v", err)
	}
	if err := os.WriteFile(canonical, []byte("version two\n"), 0o644); err != nil {
		t.Fatalf("write canonical v2: %v", err)
	}
	if _, err := archiveDesignAttempt(canonical, attemptTwo); err != nil {
		t.Fatalf("archive attempt two: %v", err)
	}

	for path, want := range map[string]string{
		filepath.Join(attemptOne, "design.md"): "version one\n",
		filepath.Join(attemptTwo, "design.md"): "version two\n",
		canonical:                              "version two\n",
	} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %q: %v", path, err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", path, got, want)
		}
	}

	if err := WritePlanAttemptMeta(artifactDir, PlanAttemptMeta{
		Attempt:      2,
		AgentStatus:  agentStatusSuccess,
		ReviewStatus: "VALIDATION_PENDING",
	}); err != nil {
		t.Fatalf("WritePlanAttemptMeta(): %v", err)
	}
	outcome, violations, err := Validate(feature.PhaseDesign, RoleDesignReviser, attemptTwo)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if !outcome.OK || len(violations) != 0 {
		t.Fatalf("Validate() = (%+v, %v), want canonical artifact accepted while historical designs are ignored", outcome, violations)
	}
	if outcome.PhaseArtifactPath != canonical {
		t.Fatalf("Validate() PhaseArtifactPath = %q, want canonical %q", outcome.PhaseArtifactPath, canonical)
	}
}

func TestResolveDesignArtifactPathFallbackIgnoresLedgerAndAttemptHistory(t *testing.T) {
	artifactDir := t.TempDir()
	fallback := filepath.Join(artifactDir, "legacy-design.md")
	if err := os.WriteFile(fallback, []byte("# Legacy Design\n"), 0o644); err != nil {
		t.Fatalf("write fallback design: %v", err)
	}
	if err := os.WriteFile(filepath.Join(artifactDir, designDecisionLedgerFile), []byte("# Design Decision Ledger\n"), 0o644); err != nil {
		t.Fatalf("write decision ledger: %v", err)
	}
	attemptDir := filepath.Join(artifactDir, "attempt-01")
	if err := os.MkdirAll(attemptDir, 0o755); err != nil {
		t.Fatalf("create attempt history: %v", err)
	}
	if err := os.WriteFile(filepath.Join(attemptDir, "design.md"), []byte("# Historical Design\n"), 0o644); err != nil {
		t.Fatalf("write historical design: %v", err)
	}

	if got := resolveDesignArtifactPath(nil, "", artifactDir); got != fallback {
		t.Fatalf("resolveDesignArtifactPath() = %q, want fallback %q; ledger and attempt history must be ignored", got, fallback)
	}
}

func newTestDesignFeature(repoPath string) *feature.Feature {
	return &feature.Feature{
		ID:            "test-design-001",
		Name:          "Test Design Feature",
		Slug:          "test-design-feature",
		Description:   "Exercise the Design validation loop",
		Status:        feature.StatusDesigning,
		CurrentPhase:  feature.PhaseDesign,
		ActiveRun:     1,
		RunCount:      1,
		Pipeline:      feature.PipelineLarge,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{{
			Name: "test-repo",
			Path: repoPath,
		}},
		Models: config.ModelConfig{
			Planning: "planner",
			Review:   "reviewer",
		},
	}
}
