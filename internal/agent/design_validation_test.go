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
%s
%s
`, testutil.JSONLInit, filepath.Join(designDir, "design.md"), testutil.TouchPhaseCompleteInLatestAttemptDir(designDir), testutil.JSONLSuccess))
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
}

func TestBuildDesignRevisionPromptCarriesFeedbackWithoutStickyApprovals(t *testing.T) {
	prompt := BuildDesignRevisionPrompt(
		"/design/design.md",
		"/design/mockups/manifest.yaml",
		"Clarify the error contract.",
		3,
	)
	for _, want := range []string{
		"revision attempt 3",
		"/design/design.md",
		"/design/mockups/manifest.yaml",
		"Clarify the error contract.",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("BuildDesignRevisionPrompt() missing %q:\n%s", want, prompt)
		}
	}
	for _, unwanted := range []string{"Prior Axis Approvals", "Sticky Approval", "frozen_sections"} {
		if strings.Contains(prompt, unwanted) {
			t.Errorf("BuildDesignRevisionPrompt() unexpectedly contains %q:\n%s", unwanted, prompt)
		}
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
