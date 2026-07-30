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
	"errors"
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
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
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

func TestRecoveryResumeDesignAcrossOrchestratorRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tests := []struct {
		name               string
		recordModel        string
		wantResumeID       string
		wantProviderID     string
		wantResumeCount    int
		wantFallbackCount  int
		wantFallbackReason string
		wantResumeEvents   int
	}{
		{
			name:             "eligible continuation",
			recordModel:      "model-a",
			wantResumeID:     "design-thread-before-restart",
			wantProviderID:   "design-thread-before-restart",
			wantResumeCount:  1,
			wantResumeEvents: 1,
		},
		{
			name:               "model changed uses fresh session",
			recordModel:        "model-b",
			wantProviderID:     "design-thread-fresh",
			wantFallbackCount:  1,
			wantFallbackReason: string(agent.ResumeReasonModelChanged),
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
				ID:            "restart-design-" + strings.ReplaceAll(test.name, " ", "-"),
				Name:          "Recovery Resume Design Restart",
				Slug:          "recovery-resume-design-restart",
				Description:   "cross-orchestrator design restart integration coverage",
				Status:        feature.StatusDesigning,
				CurrentPhase:  feature.PhaseDesign,
				ActiveRun:     1,
				RunCount:      1,
				SchemaVersion: feature.SchemaVersionCurrent,
				Pipeline:      feature.PipelineLarge,
				Models: config.ModelConfig{
					Planning: "codex:model-a",
					Review:   "codex:model-a",
				},
				Repos: []feature.FeatureRepo{{
					Name:         "repo",
					Path:         repoDir,
					WorktreePath: repoDir,
				}},
				RepoStates: map[string]*feature.RepoState{"repo": {}},
			}

			runDir := agent.ActiveRunDir(stateDir, f)
			researchDir := filepath.Join(runDir, "research")
			designDir := filepath.Join(runDir, "design")
			if err := os.MkdirAll(researchDir, 0o755); err != nil {
				t.Fatalf("mkdir research dir: %v", err)
			}
			researchPath := filepath.Join(researchDir, "research.md")
			if err := os.WriteFile(researchPath, []byte("# Research\n\nEvidence for design.\n"), 0o644); err != nil {
				t.Fatalf("write research: %v", err)
			}
			f.Artifacts = map[string]string{"research": researchPath}

			seedStore := feature.NewStore(stateDir)
			if err := seedStore.Save(f); err != nil {
				t.Fatalf("seed feature: %v", err)
			}
			now := time.Now()
			if err := agent.WriteResumeRecord(designDir, agent.ResumeRecord{
				ProviderSessionID:     "design-thread-before-restart",
				Provider:              "codex",
				ResolvedModel:         test.recordModel,
				PhaseKey:              "design",
				RunNumber:             1,
				OrchestratorSessionID: f.ID + "-design",
				CreatedAt:             now,
				UpdatedAt:             now,
			}); err != nil {
				t.Fatalf("seed design resume record: %v", err)
			}

			initSessionID := test.wantProviderID
			agentScript := testutil.WriteScript(t, scriptsDir, "design-agent.sh",
				"read -r _initialize\n"+
					"read -r _prompt\n"+
					"echo '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\""+initSessionID+"\",\"model\":\"model-a\"}'\n"+
					"cat > \""+filepath.Join(designDir, "design.md")+"\" <<'DESIGN_EOF'\n"+
					"# Design\n\nRecovered design output.\n"+
					"DESIGN_EOF\n"+
					testutil.TouchPhaseCompleteInDir(designDir)+"\n"+
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
					Phase:     feature.PhaseDesign.String(),
				},
				ProcessAlive: false,
				Feature:      restartedFeature,
			}}}

			var captureMu sync.Mutex
			var designBuilds []agent.BuildSessionOpts
			var resumeEvents []ports.FeatureResumedData
			phaseRunner := &agent.PhaseRunner{
				SessionManager: sessionManager,
				FeatureStore:   restartedStore,
				StateDir:       stateDir,
				Registry:       registry,
				BuildSessionFn: func(opts agent.BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
					captureMu.Lock()
					if opts.Phase == feature.PhaseDesign {
						designBuilds = append(designBuilds, opts)
					}
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
				},
				OnFeatureResumed: func(data ports.FeatureResumedData) {
					captureMu.Lock()
					resumeEvents = append(resumeEvents, data)
					captureMu.Unlock()
				},
			}
			restarted := orchestrator.New(orchestrator.Deps{
				Lifecycle:   restartedManager,
				Store:       restartedStore,
				Sessions:    sessionManager,
				Recovery:    recovery,
				PhaseRunner: phaseRunner,
			}, orchestrator.Hooks{})

			items, err := restarted.ScanRecovery(context.Background())
			if err != nil {
				t.Fatalf("ScanRecovery() error = %v", err)
			}
			if err := restarted.ExecuteRecovery(context.Background(), items, map[string]ports.RecoveryAction{
				ports.RecoveryActionKey(f.ID, ""): ports.RecoveryResume,
			}); err != nil {
				t.Fatalf("ExecuteRecovery() error = %v", err)
			}

			waitForRestartResumeCondition(t, func() bool {
				record, readErr := agent.ReadResumeRecord(designDir)
				return readErr == nil && record != nil && record.Completed
			}, "completed design resume record")

			captureMu.Lock()
			capturedBuilds := append([]agent.BuildSessionOpts(nil), designBuilds...)
			capturedEvents := append([]ports.FeatureResumedData(nil), resumeEvents...)
			captureMu.Unlock()
			if len(capturedBuilds) != 1 {
				t.Fatalf("design BuildSession() calls = %d, want 1", len(capturedBuilds))
			}
			if capturedBuilds[0].ResumeSessionID != test.wantResumeID {
				t.Errorf("design ResumeSessionID = %q, want %q", capturedBuilds[0].ResumeSessionID, test.wantResumeID)
			}
			if len(capturedEvents) != test.wantResumeEvents {
				t.Fatalf("FeatureResumed callbacks = %d, want %d", len(capturedEvents), test.wantResumeEvents)
			}
			if test.wantResumeEvents == 1 &&
				(capturedEvents[0].PhaseKey != "design" || capturedEvents[0].ResumeCount != 1) {
				t.Errorf("FeatureResumed callback = %#v, want design resume count 1", capturedEvents[0])
			}

			record, err := agent.ReadResumeRecord(designDir)
			if err != nil {
				t.Fatalf("ReadResumeRecord() error = %v", err)
			}
			if record == nil {
				t.Fatal("ReadResumeRecord() = nil, want completed design record")
			}
			if record.ProviderSessionID != test.wantProviderID ||
				record.PendingResume ||
				!record.Completed ||
				record.ResumeCount != test.wantResumeCount ||
				record.FreshFallbackCount != test.wantFallbackCount ||
				record.FreshFallbackReason != test.wantFallbackReason {
				t.Errorf("design resume record = %#v", record)
			}
		})
	}
}

func TestRecoveryResumeKnowledgeBaseRepositoriesAcrossOrchestratorRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, dir := range []string{stateDir, scriptsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	repos := []feature.FeatureRepo{
		{Name: "repo-a", Path: filepath.Join(tmpDir, "repo-a")},
		{Name: "repo-b", Path: filepath.Join(tmpDir, "repo-b")},
	}
	for _, repo := range repos {
		if err := os.MkdirAll(repo.Path, 0o755); err != nil {
			t.Fatalf("mkdir repo %s: %v", repo.Name, err)
		}
	}
	f := &feature.Feature{
		ID:            "recovery-kb-restart",
		Name:          "Recovery KB Restart",
		Slug:          "recovery-kb-restart",
		Status:        feature.StatusBuildingKB,
		CurrentPhase:  feature.PhaseKnowledgeBase,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Pipeline:      feature.PipelineLarge,
		Models:        config.ModelConfig{KBBuild: "codex:model-a"},
		Repos:         repos,
		RepoStates: map[string]*feature.RepoState{
			"repo-a": {},
			"repo-b": {},
		},
		KBStatus: map[string]string{
			"repo-a": "pending",
			"repo-b": "pending",
		},
	}
	seedStore := feature.NewStore(stateDir)
	if err := seedStore.Save(f); err != nil {
		t.Fatalf("seed feature: %v", err)
	}

	scripts := make(map[string]string, len(repos))
	now := time.Now()
	for _, repo := range repos {
		threadID := "thread-" + repo.Name + "-before-restart"
		resumeDir := agent.KBResumeDir(stateDir, f, repo.Name)
		if err := agent.WriteResumeRecord(resumeDir, agent.ResumeRecord{
			ProviderSessionID:     threadID,
			Provider:              "codex",
			ResolvedModel:         "model-a",
			PhaseKey:              feature.PhaseKnowledgeBase.DirName(),
			ChildKey:              repo.Name,
			RunNumber:             1,
			OrchestratorSessionID: f.ID + "-kb-" + repo.Name,
			CreatedAt:             now,
			UpdatedAt:             now,
		}); err != nil {
			t.Fatalf("seed %s resume record: %v", repo.Name, err)
		}
		kbDir := agent.KBStateDir(stateDir, repo.Name)
		scripts[repo.Name] = testutil.WriteScript(t, scriptsDir, repo.Name+"-kb-agent.sh",
			"read -r _initialize\n"+
				"read -r _prompt\n"+
				"echo '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\""+threadID+"\",\"model\":\"model-a\"}'\n"+
				"mkdir -p \""+kbDir+"\"\n"+
				"echo '# Recovered Knowledge Base' > \""+agent.KBPath(kbDir)+"\"\n"+
				testutil.TouchPhaseCompleteInDir(kbDir)+"\n"+
				testutil.JSONLSuccess+"\n")
	}

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
	recoveryItems := make([]ports.RecoveryItem, 0, len(repos))
	actions := make(map[string]ports.RecoveryAction, len(repos))
	for _, repo := range repos {
		recoveryItems = append(recoveryItems, ports.RecoveryItem{
			PIDFile: ports.PIDFile{
				FeatureID: f.ID,
				Phase:     feature.PhaseKnowledgeBase.String(),
				RepoName:  repo.Name,
			},
			Feature:  restartedFeature,
			RepoName: repo.Name,
		})
		actions[ports.RecoveryActionKey(f.ID, repo.Name)] = ports.RecoveryResume
	}
	recovery := &restartRecoveryOperator{items: recoveryItems}
	commandRunner := mocks.NewMockCommandRunner()
	commandRunner.RunFn = func(context.Context, string, []string, ports.CommandOpts) ([]byte, error) {
		return nil, nil
	}
	var captureMu sync.Mutex
	var kbBuilds []agent.BuildSessionOpts
	var resumeEvents []ports.FeatureResumedData
	phaseRunner := &agent.PhaseRunner{
		SessionManager: sessionManager,
		FeatureStore:   restartedStore,
		CommandRunner:  commandRunner,
		StateDir:       stateDir,
		Registry:       registry,
		BuildSessionFn: func(opts agent.BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
			if opts.Phase != feature.PhaseKnowledgeBase {
				return nil, nil, nil, errors.New("test stops after KB completion")
			}
			captureMu.Lock()
			kbBuilds = append(kbBuilds, opts)
			captureMu.Unlock()
			return []string{"bash", scripts[opts.RepoName]}, nil, &ports.SessionOpts{
				PIDDir:                opts.PIDDir,
				InitialPrompt:         opts.Prompt,
				ProviderName:          "codex",
				ResolvedModel:         "model-a",
				RepoName:              opts.RepoName,
				SupportsSessionResume: true,
				Protocol: claude.NewProtocol(llm.ProtocolOpts{
					InitialPrompt: opts.Prompt,
					WorkDir:       opts.WorkDir,
					Model:         "model-a",
				}),
			}, nil
		},
		OnFeatureResumed: func(data ports.FeatureResumedData) {
			captureMu.Lock()
			resumeEvents = append(resumeEvents, data)
			captureMu.Unlock()
		},
	}
	restarted := orchestrator.New(orchestrator.Deps{
		Lifecycle:   restartedManager,
		Store:       restartedStore,
		Sessions:    sessionManager,
		Recovery:    recovery,
		PhaseRunner: phaseRunner,
		CmdRunner:   commandRunner,
	}, orchestrator.Hooks{})

	items, err := restarted.ScanRecovery(context.Background())
	if err != nil {
		t.Fatalf("ScanRecovery() error = %v", err)
	}
	if err := restarted.ExecuteRecovery(context.Background(), items, actions); err != nil {
		t.Fatalf("ExecuteRecovery() error = %v", err)
	}
	waitForRestartResumeCondition(t, func() bool {
		for _, repo := range repos {
			record, readErr := agent.ReadResumeRecord(agent.KBResumeDir(stateDir, f, repo.Name))
			if readErr != nil || record == nil || !record.Completed {
				return false
			}
		}
		return true
	}, "completed KB repository resume records")
	waitForRestartResumeCondition(t, func() bool {
		loaded, loadErr := restartedStore.Load(f.ID)
		return loadErr == nil && loaded.CurrentPhase != feature.PhaseKnowledgeBase
	}, "post-KB phase transition")

	captureMu.Lock()
	capturedBuilds := append([]agent.BuildSessionOpts(nil), kbBuilds...)
	capturedEvents := append([]ports.FeatureResumedData(nil), resumeEvents...)
	captureMu.Unlock()
	if len(capturedBuilds) != len(repos) {
		t.Fatalf("KB BuildSession() calls = %d, want %d", len(capturedBuilds), len(repos))
	}
	resumeIDs := make(map[string]string, len(capturedBuilds))
	for _, build := range capturedBuilds {
		resumeIDs[build.RepoName] = build.ResumeSessionID
	}
	eventChildren := make(map[string]int, len(capturedEvents))
	for _, event := range capturedEvents {
		if event.PhaseKey == feature.PhaseKnowledgeBase.DirName() {
			eventChildren[event.ChildKey] = event.ResumeCount
		}
	}
	for _, repo := range repos {
		wantID := "thread-" + repo.Name + "-before-restart"
		if got := resumeIDs[repo.Name]; got != wantID {
			t.Errorf("repo %s ResumeSessionID = %q, want %q", repo.Name, got, wantID)
		}
		if got := eventChildren[repo.Name]; got != 1 {
			t.Errorf("repo %s feature.resumed count = %d, want 1", repo.Name, got)
		}
		record, readErr := agent.ReadResumeRecord(agent.KBResumeDir(stateDir, f, repo.Name))
		if readErr != nil {
			t.Fatalf("ReadResumeRecord(%s) error = %v", repo.Name, readErr)
		}
		if record == nil ||
			record.ProviderSessionID != wantID ||
			record.PendingResume ||
			!record.Resumed ||
			record.ResumeCount != 1 ||
			!record.Completed {
			t.Errorf("repo %s resume record = %#v", repo.Name, record)
		}
		if owner := agent.ReadKBLockOwner(agent.KBStateDir(stateDir, repo.Name)); owner != "" {
			t.Errorf("repo %s KB lock owner = %q, want released", repo.Name, owner)
		}
	}
}

func TestRecoveryResumeRoadmapAttemptAcrossOrchestratorRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

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
		ID:            "restart-roadmap-attempt",
		Name:          "Recovery Resume Roadmap Restart",
		Slug:          "recovery-resume-roadmap-restart",
		Description:   "cross-orchestrator roadmap attempt restart coverage",
		Status:        feature.StatusPlanning,
		CurrentPhase:  feature.PhasePlan,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Pipeline:      feature.PipelineMedium,
		Checkpoints:   feature.Checkpoints{RoadmapReview: true},
		Models: config.ModelConfig{
			Planning: "codex:model-a",
			Review:   "codex:model-a",
		},
		Repos: []feature.FeatureRepo{{
			Name:         "repo",
			Path:         repoDir,
			WorktreePath: repoDir,
		}},
		RepoStates: map[string]*feature.RepoState{"repo": {}},
	}
	seedStore := feature.NewStore(stateDir)
	if err := seedStore.Save(f); err != nil {
		t.Fatalf("seed feature: %v", err)
	}

	roadmapDir := filepath.Join(agent.ActiveRunDir(stateDir, f), "roadmap")
	attemptDir := filepath.Join(roadmapDir, "attempt-01")
	now := time.Now()
	if err := agent.WriteResumeRecord(attemptDir, agent.ResumeRecord{
		ProviderSessionID:     "roadmap-thread-before-restart",
		Provider:              "codex",
		ResolvedModel:         "model-a",
		PhaseKey:              "roadmap-plan",
		Iteration:             1,
		RunNumber:             1,
		OrchestratorSessionID: f.ID + "-roadmap-01",
		CreatedAt:             now,
		UpdatedAt:             now,
	}); err != nil {
		t.Fatalf("seed roadmap resume record: %v", err)
	}

	plannerScript := testutil.WriteScript(t, scriptsDir, "roadmap-agent.sh",
		"read -r _initialize\n"+
			"read -r _prompt\n"+
			"echo '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"roadmap-thread-before-restart\",\"model\":\"model-a\"}'\n"+
			"mkdir -p \""+roadmapDir+"\"\n"+
			"cat > \""+filepath.Join(roadmapDir, "roadmap.md")+"\" <<'ROADMAP_EOF'\n"+
			"# Roadmap\n\n## Phase 1: Recovered phase\n\n### Goal\n\nComplete resumed work.\n"+
			"ROADMAP_EOF\n"+
			testutil.TouchPhaseCompleteInDir(attemptDir)+"\n"+
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
			Phase:     feature.PhasePlan.String(),
		},
		ProcessAlive: false,
		Feature:      restartedFeature,
	}}}

	var captureMu sync.Mutex
	var planBuilds []agent.BuildSessionOpts
	var resumeEvents []ports.FeatureResumedData
	phaseRunner := &agent.PhaseRunner{
		SessionManager: sessionManager,
		FeatureStore:   restartedStore,
		StateDir:       stateDir,
		Registry:       registry,
		BuildSessionFn: func(opts agent.BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
			captureMu.Lock()
			planBuilds = append(planBuilds, opts)
			captureMu.Unlock()
			return []string{"bash", plannerScript}, nil, &ports.SessionOpts{
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
		},
		OnFeatureResumed: func(data ports.FeatureResumedData) {
			captureMu.Lock()
			resumeEvents = append(resumeEvents, data)
			captureMu.Unlock()
		},
	}
	restarted := orchestrator.New(orchestrator.Deps{
		Lifecycle:   restartedManager,
		Store:       restartedStore,
		Sessions:    sessionManager,
		Recovery:    recovery,
		PhaseRunner: phaseRunner,
	}, orchestrator.Hooks{})

	items, err := restarted.ScanRecovery(context.Background())
	if err != nil {
		t.Fatalf("ScanRecovery() error = %v", err)
	}
	if err := restarted.ExecuteRecovery(context.Background(), items, map[string]ports.RecoveryAction{
		ports.RecoveryActionKey(f.ID, ""): ports.RecoveryResume,
	}); err != nil {
		t.Fatalf("ExecuteRecovery() error = %v", err)
	}

	waitForRestartResumeCondition(t, func() bool {
		record, readErr := agent.ReadResumeRecord(attemptDir)
		return readErr == nil && record != nil && record.Completed &&
			agent.LatestCompletedPlanAttempt(roadmapDir) == 1
	}, "completed roadmap attempt-01")
	waitForRestartResumeCondition(t, func() bool {
		loaded, loadErr := restartedStore.Load(f.ID)
		return loadErr == nil && loaded.Status == feature.StatusPlanNeedsReview
	}, "post-roadmap planning transition")

	captureMu.Lock()
	capturedBuilds := append([]agent.BuildSessionOpts(nil), planBuilds...)
	capturedEvents := append([]ports.FeatureResumedData(nil), resumeEvents...)
	captureMu.Unlock()
	if len(capturedBuilds) != 1 {
		t.Fatalf("plan BuildSession() calls = %d, want 1", len(capturedBuilds))
	}
	if capturedBuilds[0].ResumeSessionID != "roadmap-thread-before-restart" {
		t.Errorf("plan ResumeSessionID = %q, want seeded provider identity", capturedBuilds[0].ResumeSessionID)
	}
	if len(capturedEvents) != 1 ||
		capturedEvents[0].PhaseKey != "roadmap-plan" ||
		capturedEvents[0].Iteration != 1 ||
		capturedEvents[0].ResumeCount != 1 {
		t.Errorf("FeatureResumed callbacks = %#v, want one roadmap attempt-01 resume", capturedEvents)
	}
	record, err := agent.ReadResumeRecord(attemptDir)
	if err != nil {
		t.Fatalf("ReadResumeRecord() error = %v", err)
	}
	if record == nil ||
		record.ProviderSessionID != "roadmap-thread-before-restart" ||
		record.PendingResume ||
		!record.Completed ||
		!record.Resumed ||
		record.ResumeCount != 1 {
		t.Errorf("roadmap resume record = %#v, want completed resumed attempt-01", record)
	}
	if _, err := os.Stat(filepath.Join(roadmapDir, "attempt-02")); !os.IsNotExist(err) {
		t.Errorf("attempt-02 exists after cross-restart resume: %v", err)
	}
}

func waitForRestartResumeCondition(t *testing.T, condition func() bool, description string) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", description)
		case <-ticker.C:
		}
	}
}
