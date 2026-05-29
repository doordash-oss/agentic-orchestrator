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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

func TestParsePlanningHandoffMd_RoundTripStates(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state PlanningHandoffState
		token string
	}{
		{name: "continue", state: PlanningHandoffContinue, token: "CONTINUE"},
		{name: "complete", state: PlanningHandoffComplete, token: "COMPLETE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writePlanningHandoff(t, t.TempDir(), tc.token, "- canonical facts\n", "- wrote overview\n")

			parsed, err := ParsePlanningHandoffMd(path)
			if err != nil {
				t.Fatalf("ParsePlanningHandoffMd() error = %v", err)
			}
			if !parsed.OK() {
				t.Fatalf("parsed violations = %v, want none", parsed.ProtocolViolations)
			}
			if parsed.State != tc.state {
				t.Fatalf("State = %v, want %v", parsed.State, tc.state)
			}
			if !strings.Contains(parsed.ProgressRegion, "## Understanding") {
				t.Fatalf("ProgressRegion missing Understanding section:\n%s", parsed.ProgressRegion)
			}
			if !strings.Contains(parsed.ProgressRegion, "## Plan Progress") {
				t.Fatalf("ProgressRegion missing Plan Progress section:\n%s", parsed.ProgressRegion)
			}
		})
	}
}

func TestParsePlanningHandoffMd_RejectsMissingSectionsAndInvalidState(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "missing_understanding",
			body: "# Planning Handoff\n\n## Plan Progress\n### Drafted\n- draft\n### Remaining\n- rest\n### Where I stopped\n- next\n\n## Handoff State\nCONTINUE\n",
			want: "## Understanding",
		},
		{
			name: "missing_state",
			body: "# Planning Handoff\n\n## Understanding\n- facts\n\n## Plan Progress\n### Drafted\n- draft\n### Remaining\n- rest\n### Where I stopped\n- next\n",
			want: "## Handoff State",
		},
		{
			name: "invalid_state",
			body: "# Planning Handoff\n\n## Understanding\n- facts\n\n## Plan Progress\n### Drafted\n- draft\n### Remaining\n- rest\n### Where I stopped\n- next\n\n## Handoff State\nRETRY\n",
			want: "CONTINUE, COMPLETE",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), PlanningHandoffFilename)
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatalf("write handoff: %v", err)
			}

			parsed, err := ParsePlanningHandoffMd(path)
			if err != nil {
				t.Fatalf("ParsePlanningHandoffMd() error = %v", err)
			}
			if parsed.OK() {
				t.Fatalf("parsed OK, want protocol violation")
			}
			got := strings.Join(parsed.ProtocolViolations, "\n")
			if !strings.Contains(got, tc.want) {
				t.Fatalf("violations = %q, want substring %q", got, tc.want)
			}
		})
	}
}

func TestPlanningHandoffFingerprint_StableAcrossVolatileAttemptContent(t *testing.T) {
	dir := t.TempDir()
	pathA := writePlanningHandoff(t, dir, "CONTINUE",
		"- read /tmp/run-001/phase-02/plan/attempt-01/validation-feedback.md\n",
		"- wrote /tmp/run-001/phase-02/plan/attempt-01/roadmap.md\n")
	pathB := filepath.Join(dir, "b-"+PlanningHandoffFilename)
	writePlanningHandoffAt(t, pathB, "CONTINUE",
		"- read /private/var/folders/run-009/phase-02/plan/attempt-07/validation-scope-feedback.md\n",
		"- wrote /private/var/folders/run-009/phase-02/plan/attempt-07/roadmap.md\n")

	fpA, err := PlanningHandoffFingerprint(pathA)
	if err != nil {
		t.Fatalf("PlanningHandoffFingerprint(A) error = %v", err)
	}
	fpB, err := PlanningHandoffFingerprint(pathB)
	if err != nil {
		t.Fatalf("PlanningHandoffFingerprint(B) error = %v", err)
	}
	if fpA != fpB {
		t.Fatalf("fingerprints differ for volatile-only changes:\nA=%s\nB=%s", fpA, fpB)
	}

	pathC := filepath.Join(dir, "c-"+PlanningHandoffFilename)
	writePlanningHandoffAt(t, pathC, "CONTINUE", "- read product facts\n", "- wrote task list\n")
	fpC, err := PlanningHandoffFingerprint(pathC)
	if err != nil {
		t.Fatalf("PlanningHandoffFingerprint(C) error = %v", err)
	}
	if fpC == fpA {
		t.Fatalf("fingerprint unchanged after real narrative change: %s", fpC)
	}
}

func TestPlanningContinuationNoProgressRail(t *testing.T) {
	tmpDir := t.TempDir()
	attemptDir := filepath.Join(tmpDir, "attempt-01")
	if err := os.MkdirAll(attemptDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(attemptDir) error = %v", err)
	}

	f := &feature.Feature{
		ID:            "feat-planning-rail",
		Name:          "Planning Rail",
		ActiveRun:     1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Models:        config.ModelConfig{Planning: "planner"},
	}

	starts := 0
	cfg := PlanLoopConfig{
		Feature:             f,
		StateDir:            tmpDir,
		WorkDir:             tmpDir,
		MaxConsecNoProgress: 1,
		BuildSession: func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
			return []string{"mock-planner"}, nil, &session.SessionOpts{
				PIDDir:            opts.PIDDir,
				ProviderName:      "codex",
				DebugSystemPrompt: opts.SystemPrompt,
			}, nil
		},
		SessionStartFunc: func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (session.SessionHandle, error) {
			starts++
			sess := session.NewSession(id, featureID, phase)
			go func() {
				err := os.WriteFile(filepath.Join(attemptDir, PlanningHandoffFilename), []byte(planningHandoffText("CONTINUE")), 0o644)
				if err == nil {
					err = os.WriteFile(filepath.Join(attemptDir, PhaseCompleteFile), []byte("complete\n"), 0o644)
				}
				if err != nil {
					sess.SendStatus(agentStatusFailed)
					return
				}
				sess.SendStatus(agentStatusSuccess)
			}()
			return sess, nil
		},
	}

	outcome, err := runPlanningSessionWithContinuations(planningContinuationInput{
		Config:        cfg,
		Attempt:       1,
		AttemptDir:    attemptDir,
		ArtifactDir:   tmpDir,
		Prompt:        "write a plan",
		SystemPrompt:  "system",
		PlannerSpec:   PhasePlanCreatorRoleSpec(),
		SessionIDBase: "feat-planning-rail-plan-01",
		Model:         "planner",
		CanonicalPath: filepath.Join(tmpDir, "plan.md"),
	})
	if err != nil {
		t.Fatalf("runPlanningSessionWithContinuations() error = %v", err)
	}
	if outcome.AgentStatus != planningAgentStatusSafetyRail {
		t.Fatalf("AgentStatus = %q, want %q", outcome.AgentStatus, planningAgentStatusSafetyRail)
	}
	if starts != 2 {
		t.Fatalf("sessions started = %d, want 2", starts)
	}
	if !strings.Contains(outcome.LastError, "no progress") {
		t.Fatalf("LastError = %q, want no-progress rail message", outcome.LastError)
	}
}

func TestPlanningContinuationMalformedHandoffFailureRail(t *testing.T) {
	tmpDir := t.TempDir()
	attemptDir := filepath.Join(tmpDir, "attempt-01")
	if err := os.MkdirAll(attemptDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(attemptDir) error = %v", err)
	}

	f := &feature.Feature{
		ID:            "feat-planning-handoff-failure",
		Name:          "Planning Handoff Failure",
		ActiveRun:     1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Models:        config.ModelConfig{Planning: "planner"},
	}

	starts := 0
	cfg := PlanLoopConfig{
		Feature:        f,
		StateDir:       tmpDir,
		WorkDir:        tmpDir,
		MaxConsecFails: 2,
		BuildSession: func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
			return []string{"mock-planner"}, nil, &session.SessionOpts{
				PIDDir:            opts.PIDDir,
				ProviderName:      "codex",
				DebugSystemPrompt: opts.SystemPrompt,
			}, nil
		},
		SessionStartFunc: func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (session.SessionHandle, error) {
			starts++
			sess := session.NewSession(id, featureID, phase)
			go func() {
				body := "# Planning Handoff\n\n" +
					"## Understanding\n- read context\n\n" +
					"## Plan Progress\n" +
					"### Drafted\n- started\n\n" +
					"### Remaining\n- finish\n\n" +
					"### Where I stopped\n- continue\n"
				err := os.WriteFile(filepath.Join(attemptDir, PlanningHandoffFilename), []byte(body), 0o644)
				if err == nil {
					err = os.WriteFile(filepath.Join(attemptDir, PhaseCompleteFile), []byte("complete\n"), 0o644)
				}
				if err != nil {
					sess.SendStatus(agentStatusFailed)
					return
				}
				sess.SendStatus(agentStatusSuccess)
			}()
			return sess, nil
		},
	}

	outcome, err := runPlanningSessionWithContinuations(planningContinuationInput{
		Config:        cfg,
		Attempt:       1,
		AttemptDir:    attemptDir,
		ArtifactDir:   tmpDir,
		Prompt:        "write a plan",
		SystemPrompt:  "system",
		PlannerSpec:   PhasePlanCreatorRoleSpec(),
		SessionIDBase: "feat-planning-handoff-failure-plan-01",
		Model:         "planner",
		CanonicalPath: filepath.Join(tmpDir, "plan.md"),
	})
	if err != nil {
		t.Fatalf("runPlanningSessionWithContinuations() error = %v", err)
	}
	if outcome.AgentStatus != planningAgentStatusSafetyRail {
		t.Fatalf("AgentStatus = %q, want %q", outcome.AgentStatus, planningAgentStatusSafetyRail)
	}
	if starts != 2 {
		t.Fatalf("sessions started = %d, want 2", starts)
	}
	if !strings.Contains(outcome.LastError, "planning handoff protocol violation repeated 2 consecutive times") {
		t.Fatalf("LastError = %q, want handoff failure rail message", outcome.LastError)
	}
}

func writePlanningHandoff(t *testing.T, dir, state, understanding, drafted string) string {
	t.Helper()
	path := filepath.Join(dir, PlanningHandoffFilename)
	writePlanningHandoffAt(t, path, state, understanding, drafted)
	return path
}

func writePlanningHandoffAt(t *testing.T, path, state, understanding, drafted string) {
	t.Helper()
	body := planningHandoffTextWithContent(state, understanding, drafted)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func planningHandoffText(state string) string {
	return planningHandoffTextWithContent(state, "- canonical facts\n", "- wrote overview\n")
}

func planningHandoffTextWithContent(state, understanding, drafted string) string {
	return "# Planning Handoff\n\n" +
		"## Understanding\n" + understanding + "\n" +
		"## Plan Progress\n" +
		"### Drafted\n" + drafted + "\n" +
		"### Remaining\n- finish verification\n\n" +
		"### Where I stopped\n- resume at next task\n\n" +
		"## Handoff State\n" + state + "\n"
}
