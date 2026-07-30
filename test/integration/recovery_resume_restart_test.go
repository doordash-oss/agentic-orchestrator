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

package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/claude"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/codex"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

type restartRecoveryOperator struct {
	items        []ports.RecoveryItem
	scanCalls    int
	executeCalls int
}

func (r *restartRecoveryOperator) ScanForRecovery(context.Context) ([]ports.RecoveryItem, error) {
	r.scanCalls++
	return r.items, nil
}

func (r *restartRecoveryOperator) ExecuteRecovery(
	_ context.Context,
	_ []ports.RecoveryItem,
	_ map[string]ports.RecoveryAction,
) error {
	r.executeCalls++
	return nil
}

// TestRecoveryResumeAcrossOrchestratorRestart covers the durable hand-off from
// the recovery relaunch into the restarted implement loop. Each case seeds the
// same crashed iteration on disk, constructs new store/manager/orchestrator
// instances, scans recovery, and executes RecoveryResume.
func TestRecoveryResumeAcrossOrchestratorRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tests := []struct {
		name                 string
		recordModel          string
		recordRun            int
		providerSessionID    string
		wantResume           bool
		wantFallbackReason   string
		wantProviderIdentity string
	}{
		{
			name:                 "eligible provider continuation",
			recordModel:          "model-a",
			recordRun:            1,
			providerSessionID:    "thread-before-restart",
			wantResume:           true,
			wantProviderIdentity: "thread-before-restart",
		},
		{
			name:                 "model changed falls back fresh",
			recordModel:          "model-b",
			recordRun:            1,
			providerSessionID:    "thread-old-model",
			wantFallbackReason:   string(agent.ResumeReasonModelChanged),
			wantProviderIdentity: "thread-fresh-model",
		},
		{
			name:                 "sealed run falls back fresh",
			recordModel:          "model-a",
			recordRun:            2,
			providerSessionID:    "thread-sealed-run",
			wantFallbackReason:   string(agent.ResumeReasonRunSealed),
			wantProviderIdentity: "thread-fresh-run",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			stateDir := filepath.Join(tmpDir, "state")
			repoDir := filepath.Join(tmpDir, "repo")
			scriptsDir := filepath.Join(tmpDir, "scripts")
			for _, dir := range []string{stateDir, repoDir, scriptsDir} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("mkdir %s: %v", dir, err)
				}
			}

			f := &feature.Feature{
				ID:                  "restart-" + strings.ReplaceAll(test.name, " ", "-"),
				Name:                "Recovery Resume Restart",
				Slug:                "recovery-resume-restart",
				Description:         "cross-orchestrator restart integration coverage",
				Status:              feature.StatusImplementing,
				CurrentPhase:        feature.PhaseImplement,
				CurrentIteration:    2,
				CurrentRoadmapPhase: 1,
				ActiveTimingKey:     "phase-1-impl",
				ActiveRun:           1,
				RunCount:            1,
				SchemaVersion:       feature.SchemaVersionCurrent,
				Pipeline:            feature.PipelineLarge,
				MaxIterations:       2,
				ExitCriteria:        "integration handoff parses",
				Models: config.ModelConfig{
					Implementation: "codex:model-a",
					Review:         "codex:model-a",
				},
				Repos: []feature.FeatureRepo{{
					Name:         "repo",
					Path:         repoDir,
					WorktreePath: repoDir,
				}},
				RepoStates: map[string]*feature.RepoState{"repo": {}},
			}

			implementDir := agent.ActiveImplementDir(stateDir, f)
			planDir := filepath.Dir(implementDir)
			if err := os.MkdirAll(planDir, 0o755); err != nil {
				t.Fatalf("mkdir plan dir: %v", err)
			}
			planPath := filepath.Join(planDir, "phase-plan.md")
			plan := "# Phase Plan\n\n## Tasks\n\n### Task 1: recover work\n\n" +
				"**Repo:** repo\n\nFinish the interrupted implementation.\n"
			if err := os.WriteFile(planPath, []byte(plan), 0o644); err != nil {
				t.Fatalf("write plan: %v", err)
			}
			f.Artifacts = map[string]string{"plan": planPath}

			seedStore := feature.NewStore(stateDir)
			if err := seedStore.Save(f); err != nil {
				t.Fatalf("seed feature: %v", err)
			}
			if err := os.MkdirAll(implementDir, 0o755); err != nil {
				t.Fatalf("mkdir implement dir: %v", err)
			}
			iterOneDir := filepath.Join(implementDir, "iteration-01")
			if err := os.MkdirAll(iterOneDir, 0o755); err != nil {
				t.Fatalf("mkdir iteration 1: %v", err)
			}
			if err := agent.NewArtifactManager(implementDir).WriteMeta(iterOneDir, agent.IterationMeta{
				Iteration:   1,
				AgentStatus: "SUCCESS",
			}); err != nil {
				t.Fatalf("seed iteration 1 meta: %v", err)
			}
			iterTwoDir := filepath.Join(implementDir, "iteration-02")
			now := time.Now()
			if err := agent.WriteResumeRecord(iterTwoDir, agent.ResumeRecord{
				ProviderSessionID:     test.providerSessionID,
				Provider:              "codex",
				ResolvedModel:         test.recordModel,
				PhaseKey:              "phase-1-impl",
				Iteration:             2,
				RunNumber:             test.recordRun,
				OrchestratorSessionID: f.ID + "-phase-01-impl-02",
				CreatedAt:             now,
				UpdatedAt:             now,
			}); err != nil {
				t.Fatalf("seed resume record: %v", err)
			}

			initSessionID := test.wantProviderIdentity
			agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
				"read -r _initialize\n"+
					"read -r _prompt\n"+
					"echo '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\""+initSessionID+"\",\"model\":\"model-a\"}'\n"+
					testutil.WriteImplementSuccessArtifacts(implementDir)+"\n"+
					testutil.JSONLSuccess+"\n")

			eventCh := make(chan interface{}, 100)
			sessionManager := session.NewManager(eventCh)
			t.Cleanup(sessionManager.Shutdown)

			registry := llm.NewRegistry()
			registry.Register(&codex.Provider{})
			restartedStore := feature.NewStore(stateDir)
			restartedManager := feature.NewManager(restartedStore, &config.Config{})
			restartedFeature, err := restartedStore.Load(f.ID)
			if err != nil {
				t.Fatalf("load feature after restart: %v", err)
			}
			recovery := &restartRecoveryOperator{items: []ports.RecoveryItem{{
				PIDFile: ports.PIDFile{
					FeatureID: f.ID,
					Phase:     feature.PhaseImplement.String(),
					Iteration: 2,
				},
				ProcessAlive: false,
				Feature:      restartedFeature,
			}}}

			var captureMu sync.Mutex
			var buildOpts []agent.BuildSessionOpts
			buildSession := func(opts agent.BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
				captureMu.Lock()
				buildOpts = append(buildOpts, opts)
				captureMu.Unlock()
				return []string{"bash", agentScript}, nil, &ports.SessionOpts{
					PIDDir:                opts.PIDDir,
					InitialPrompt:         opts.Prompt,
					ProviderName:          "codex",
					ResolvedModel:         "model-a",
					SupportsSessionResume: true,
					Protocol: claude.NewProtocol(llm.ProtocolOpts{
						InitialPrompt: opts.Prompt,
						WorkDir:       opts.WorkDir,
						Model:         "model-a",
					}),
				}, nil
			}

			phaseRunner := &agent.PhaseRunner{
				SessionManager: sessionManager,
				FeatureStore:   restartedStore,
				StateDir:       stateDir,
				Registry:       registry,
			}
			restarted := orchestrator.New(orchestrator.Deps{
				Lifecycle:   restartedManager,
				Store:       restartedStore,
				Sessions:    sessionManager,
				Recovery:    recovery,
				PhaseRunner: phaseRunner,
			}, orchestrator.Hooks{})
			restarted.SetRunMultiRepoImplFn(func(
				current *feature.Feature,
				currentPlan string,
				_ ...agent.KBInfo,
			) (chan *agent.OrchestratorResult, error) {
				result, runErr := agent.RunPhaseImplementLoop(agent.OrchestratorConfig{
					Feature:             current,
					FeatureStore:        restartedStore,
					PlanPath:            currentPlan,
					StateDir:            stateDir,
					Model:               "codex:model-a",
					ReviewModel:         "codex:model-a",
					MaxIterations:       2,
					MaxConsecFails:      3,
					MaxConsecNoProgress: 3,
					BuildSession:        buildSession,
				}, sessionManager)
				if runErr != nil {
					return nil, runErr
				}
				if result.FinalStatus != "review_passed" {
					t.Errorf("RunPhaseImplementLoop().FinalStatus = %q, want review_passed", result.FinalStatus)
				}
				resultCh := make(chan *agent.OrchestratorResult)
				close(resultCh)
				return resultCh, nil
			})

			items, err := restarted.ScanRecovery(context.Background())
			if err != nil {
				t.Fatalf("ScanRecovery() error = %v", err)
			}
			if recovery.scanCalls != 1 || len(items) != 1 {
				t.Fatalf("ScanRecovery() calls/items = %d/%d, want 1/1", recovery.scanCalls, len(items))
			}
			err = restarted.ExecuteRecovery(context.Background(), items, map[string]ports.RecoveryAction{
				ports.RecoveryActionKey(f.ID, ""): ports.RecoveryResume,
			})
			if err != nil {
				t.Fatalf("ExecuteRecovery() error = %v", err)
			}
			if recovery.executeCalls != 1 {
				t.Errorf("RecoveryOperator.ExecuteRecovery() calls = %d, want 1", recovery.executeCalls)
			}

			captureMu.Lock()
			captured := append([]agent.BuildSessionOpts(nil), buildOpts...)
			captureMu.Unlock()
			if len(captured) != 1 {
				t.Fatalf("BuildSession() calls = %d, want 1", len(captured))
			}
			if test.wantResume {
				if captured[0].ResumeSessionID != test.providerSessionID {
					t.Errorf("BuildSession().ResumeSessionID = %q, want %q",
						captured[0].ResumeSessionID, test.providerSessionID)
				}
				if !strings.Contains(captured[0].Prompt, "previous process terminated unexpectedly mid-turn") {
					t.Errorf("BuildSession().Prompt = %q, want recovery resume prompt", captured[0].Prompt)
				}
			} else {
				if captured[0].ResumeSessionID != "" {
					t.Errorf("BuildSession().ResumeSessionID = %q, want fresh session", captured[0].ResumeSessionID)
				}
				if strings.Contains(captured[0].Prompt, "previous process terminated unexpectedly mid-turn") {
					t.Errorf("BuildSession().Prompt = %q, want standard implement prompt", captured[0].Prompt)
				}
			}

			record, err := agent.ReadResumeRecord(iterTwoDir)
			if err != nil {
				t.Fatalf("ReadResumeRecord() error = %v", err)
			}
			if record == nil {
				t.Fatal("ReadResumeRecord() = nil, want completed iteration record")
			}
			if record.ProviderSessionID != test.wantProviderIdentity {
				t.Errorf("resume provider identity = %q, want %q",
					record.ProviderSessionID, test.wantProviderIdentity)
			}
			if record.PendingResume || !record.Completed {
				t.Errorf("resume record pending/completed = %v/%v, want false/true", record.PendingResume, record.Completed)
			}
			if test.wantResume {
				if !record.Resumed || record.ResumeCount != 1 || record.FreshFallbackCount != 0 {
					t.Errorf("resume lineage = resumed:%v count:%d fallback:%d, want true/1/0",
						record.Resumed, record.ResumeCount, record.FreshFallbackCount)
				}
			} else {
				if record.Resumed || record.ResumeCount != 0 ||
					record.FreshFallbackCount != 1 ||
					record.FreshFallbackReason != test.wantFallbackReason {
					t.Errorf("fresh lineage = %#v, want fallback 1 reason %q", record, test.wantFallbackReason)
				}
			}

			meta, err := agent.NewArtifactManager(implementDir).ReadMeta(iterTwoDir)
			if err != nil {
				t.Fatalf("ReadMeta(iteration-02) error = %v", err)
			}
			if meta.ProviderSessionID != test.wantProviderIdentity ||
				meta.Resumed != test.wantResume ||
				meta.ResumeCount != record.ResumeCount ||
				meta.FreshFallbackCount != record.FreshFallbackCount {
				t.Errorf("iteration meta = %#v, want mirrored resume lineage %#v", meta, record)
			}
			if _, err := os.Stat(filepath.Join(implementDir, "iteration-03")); !os.IsNotExist(err) {
				t.Errorf("iteration-03 exists after replaying iteration 2: %v", err)
			}
		})
	}
}
