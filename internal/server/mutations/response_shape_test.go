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

package mutations

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// TestMutationTargetSuccessResponsesNameFeatureAndResult pins the success
// vocabulary the module owns: every method whose response has a result
// fills it with its exact result string, and names the feature (or, for a
// child launch, the created child) wherever the response has a feature id.
func TestMutationTargetSuccessResponsesNameFeatureAndResult(t *testing.T) {
	for _, tc := range []struct {
		name       string
		wantResult string
		// call drives the success path; responses without a FeatureID field
		// return empty feature ids.
		call func(t *testing.T) (gotFeatureID, wantFeatureID, gotResult string)
	}{
		{"CreateFeature", resultCreated, func(t *testing.T) (string, string, string) {
			runtimeDir := t.TempDir()
			configPath := filepath.Join(runtimeDir, "config.yaml")
			repoPath := filepath.Join(runtimeDir, testRepoAName)
			initMutationGitRepo(t, repoPath)
			cfg := config.NewDefault()
			cfg.Repos[testRepoAName] = config.RepoConfig{Path: repoPath}
			if err := config.Save(configPath, cfg); err != nil {
				t.Fatalf("Save config: %v", err)
			}
			store := feature.NewStore(filepath.Join(runtimeDir, "features"))
			target := newRESTCreateFeatureTarget(store, feature.NewManager(store, cfg), cfg, configPath)
			resp, err := target.CreateFeature(serverruntime.CreateFeatureRequest{
				Name: "response shape create", Repos: []string{testRepoAName}, Pipeline: feature.PipelineMedium,
			})
			if err != nil {
				t.Fatalf("CreateFeature() error = %v", err)
			}
			// The id is minted by creation; pin that it names the stored feature.
			if _, err := store.Load(resp.FeatureID); err != nil {
				t.Fatalf("CreateFeature() feature id %q does not name a stored feature: %v", resp.FeatureID, err)
			}
			return resp.FeatureID, resp.FeatureID, resp.Result
		}},
		{"SetupFeature", resultSetupStarted, func(t *testing.T) (string, string, string) {
			runtimeDir := t.TempDir()
			cfg := config.NewDefault()
			cfg.Repos[testRepoAName] = config.RepoConfig{Path: filepath.Join(runtimeDir, testRepoAName)}
			store := feature.NewStore(filepath.Join(runtimeDir, "features"))
			manager := feature.NewManager(store, cfg)
			worktrees := mocks.NewMockWorktreeOps()
			worktrees.CreateFn = func(_, featureSlug, repoName, _ string) (string, error) {
				return filepath.Join(runtimeDir, "worktrees", featureSlug, repoName), nil
			}
			manager.Worktrees = worktrees
			target := mutationTarget{orch: orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{})}
			f, err := manager.Create("response shape setup", "desc", []string{testRepoAName}, cfg.Defaults.Models, "", "", nil, feature.CreateOptions{
				QueueSetup: true, Pipeline: feature.PipelineMedium,
			})
			if err != nil {
				t.Fatalf("Create feature: %v", err)
			}
			resp, err := target.SetupFeature(f.ID)
			target.orch.WaitForCycles()
			if err != nil {
				t.Fatalf("SetupFeature() error = %v", err)
			}
			return resp.FeatureID, f.ID, resp.Result
		}},
		{"StartFeature", resultStarted, func(t *testing.T) (string, string, string) {
			store, manager, f := newMutationTestFeature(t, "response shape start", feature.CreateOptions{Pipeline: feature.PipelineLarge}, feature.StatusCreated, 0)
			target := mutationTarget{orch: orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{})}
			resp, err := target.StartFeature(f.ID)
			if err != nil {
				t.Fatalf("StartFeature() error = %v", err)
			}
			return resp.FeatureID, f.ID, resp.Result
		}},
		{"StopFeature", resultStopped, func(t *testing.T) (string, string, string) {
			store, manager, f := newMutationTestFeature(t, "response shape stop", feature.CreateOptions{Pipeline: feature.PipelineLarge}, feature.StatusCreated, 0)
			target := mutationTarget{orch: orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{})}
			if _, err := target.StartFeature(f.ID); err != nil {
				t.Fatalf("StartFeature() error = %v", err)
			}
			resp, err := target.StopFeature(f.ID)
			if err != nil {
				t.Fatalf("StopFeature() error = %v", err)
			}
			return resp.FeatureID, f.ID, resp.Result
		}},
		{"RestartFeature", resultRestarted, func(t *testing.T) (string, string, string) {
			target, f := newResponseShapeImplementTarget(t, "response shape restart", func(ff *feature.Feature) {
				ff.Status = feature.StatusFailed
				ff.Run().Failure = &errcat.FailureRecord{Code: errcat.SessionCrashed, Diagnostics: "previous worker died"}
			})
			resp, err := target.RestartFeature(f.ID, serverruntime.RestartFeatureRequest{})
			if err != nil {
				t.Fatalf("RestartFeature() error = %v", err)
			}
			return resp.FeatureID, f.ID, resp.Result
		}},
		{"UpdateFeatureConfig", resultUpdated, func(t *testing.T) (string, string, string) {
			runtimeDir := t.TempDir()
			configPath := filepath.Join(runtimeDir, "config.yaml")
			cfg := config.NewDefault()
			cfg.Repos[testRepoAName] = config.RepoConfig{Path: filepath.Join(runtimeDir, testRepoAName)}
			if err := config.Save(configPath, cfg); err != nil {
				t.Fatalf("Save config: %v", err)
			}
			store := feature.NewStore(filepath.Join(runtimeDir, "features"))
			manager := feature.NewManager(store, cfg)
			f, err := manager.Create("response shape config", "desc", []string{testRepoAName}, cfg.Defaults.Models, "", "", nil, feature.CreateOptions{Pipeline: feature.PipelineMedium})
			if err != nil {
				t.Fatalf("Create feature: %v", err)
			}
			target := newRESTCreateFeatureTarget(store, manager, cfg, configPath)
			resp, err := target.UpdateFeatureConfig(f.ID, serverruntime.FeatureConfigMutationRequest{
				Models:      config.ModelConfig{Implementation: testModelClaudeSonnet},
				Inquireness: testInquirenessHigh,
				Pipeline:    feature.PipelineMedium,
			})
			if err != nil {
				t.Fatalf("UpdateFeatureConfig() error = %v", err)
			}
			return resp.FeatureID, f.ID, resp.Result
		}},
		{"ResumeNeedUserInput", resultResumed, func(t *testing.T) (string, string, string) {
			gatePath := filepath.Join(t.TempDir(), agent.NeedUserInputArtifactName)
			if err := agent.WriteNeedUserInputRecord(gatePath, agent.NeedUserInputRecord{
				Summary:   "Implementation needs a deployment window.",
				Questions: []agent.NeedUserInputQuestion{{Index: 1, Prompt: "Deployment window?", Answer: "Tomorrow morning."}},
			}); err != nil {
				t.Fatalf("WriteNeedUserInputRecord() error = %v", err)
			}
			target, f := newResponseShapeImplementTarget(t, "response shape resume", func(ff *feature.Feature) {
				ff.Status = feature.StatusNeedUserInput
				ff.PendingNeedUserInputPath = gatePath
			})
			resp, err := target.ResumeNeedUserInput(f.ID)
			if err != nil {
				t.Fatalf("ResumeNeedUserInput() error = %v", err)
			}
			return resp.FeatureID, f.ID, resp.Result
		}},
		{"DraftNeedUserInputAnswers", resultDrafted, func(t *testing.T) (string, string, string) {
			gatePath := filepath.Join(t.TempDir(), agent.NeedUserInputArtifactName)
			if err := agent.WriteNeedUserInputRecord(gatePath, agent.NeedUserInputRecord{
				Summary:   "Blocked on a product choice.",
				Questions: []agent.NeedUserInputQuestion{{Index: 1, Prompt: "Which database?"}},
			}); err != nil {
				t.Fatalf("WriteNeedUserInputRecord() error = %v", err)
			}
			store := feature.NewStore(t.TempDir())
			f := &feature.Feature{
				ID: "feat-draft-shape", Name: "Draft shape", Slug: "draft-shape", Status: feature.StatusNeedUserInput,
				PendingNeedUserInputPath: gatePath, SchemaVersion: feature.SchemaVersionCurrent,
			}
			if err := store.Save(f); err != nil {
				t.Fatalf("Save feature: %v", err)
			}
			target := mutationTarget{orch: newStoreOrchestrator(store)}
			resp, err := target.DraftNeedUserInputAnswers(f.ID, serverruntime.NeedUserInputDraftRequest{
				Answers: map[string]string{"1": "Postgres."},
			})
			if err != nil {
				t.Fatalf("DraftNeedUserInputAnswers() error = %v", err)
			}
			return resp.FeatureID, f.ID, resp.Result
		}},
		{"WaiveTestingContractItems", resultWaived, func(t *testing.T) (string, string, string) {
			stateRoot := t.TempDir()
			store := feature.NewStore(stateRoot)
			f := &feature.Feature{
				ID: "feat-waive-shape", Name: "Waive shape", Slug: "waive-shape", Status: feature.StatusImplementing,
				SchemaVersion: feature.SchemaVersionCurrent, CurrentPhase: feature.PhaseImplement,
				CurrentRoadmapPhase: 1, ActiveRun: 1, RunCount: 1,
				Repos: []feature.FeatureRepo{{Name: testRepoAName, Path: filepath.Join(stateRoot, testRepoAName)}},
			}
			if err := store.Save(f); err != nil {
				t.Fatalf("Save feature: %v", err)
			}
			if err := agent.WriteTestingContract(agent.PhaseTestingContractPath(stateRoot, f, 1), agent.TestingContract{
				Version: 2, Revision: 1, Items: []agent.TestingContractItem{{
					ID: "visual_1", Source: "visual",
					Policy: agent.TestingContractItemPolicy{Required: true, AllowBlocked: true, AllowWaiver: true},
				}},
			}); err != nil {
				t.Fatalf("WriteTestingContract() error = %v", err)
			}
			target := mutationTarget{orch: newStoreOrchestrator(store)}
			resp, err := target.WaiveTestingContractItems(f.ID, serverruntime.TestingContractWaiveRequest{
				ItemIDs: []string{"visual_1"}, Reason: "the VM lacks a signed-in session",
			})
			if err != nil {
				t.Fatalf("WaiveTestingContractItems() error = %v", err)
			}
			return resp.FeatureID, f.ID, resp.Result
		}},
		{"AnswerPermission", resultAnswered, func(t *testing.T) (string, string, string) {
			sess := newResponseShapePermissionSession()
			sessions := &mutationTargetSessionManager{sessions: []ports.SessionView{sess}}
			target := mutationTarget{orch: mutationTargetOrchestrator(sessions), sessions: sessions}
			resp, err := target.AnswerPermission(serverruntime.PermissionAnswerRequest{
				RequestID: testPermRequestID, SessionID: testSessionPermissionID, Decision: "allow_once",
			})
			if err != nil {
				t.Fatalf("AnswerPermission() error = %v", err)
			}
			return "", "", resp.Result
		}},
		{"AnswerPermission retry_auto_review", resultReviewed, func(t *testing.T) (string, string, string) {
			sess := &responseShapeRetryReviewSession{mutationTargetSessionView: newResponseShapePermissionSession()}
			sess.pending[0].AutomaticReview = &llm.AutomaticReviewStatus{Outcome: "deferred"}
			sessions := &mutationTargetSessionManager{sessions: []ports.SessionView{sess}}
			target := mutationTarget{orch: mutationTargetOrchestrator(sessions), sessions: sessions}
			resp, err := target.AnswerPermission(serverruntime.PermissionAnswerRequest{
				RequestID: testPermRequestID, SessionID: testSessionPermissionID, Decision: "retry_auto_review",
			})
			if err != nil {
				t.Fatalf("AnswerPermission(retry_auto_review) error = %v", err)
			}
			if len(sess.retried) != 1 || sess.retried[0] != testPermRequestID {
				t.Fatalf("RetryAutomaticReview calls = %v; want %s", sess.retried, testPermRequestID)
			}
			return "", "", resp.Result
		}},
		{"AnswerAskUser", resultAnswered, func(t *testing.T) (string, string, string) {
			sess := &mutationTargetSessionView{
				id: testSessionAskID, featureID: "feat-ask", phase: feature.PhaseInquire, status: ports.SessionWaitingHelp, active: true,
				pending: []*llm.ControlRequestMessage{{
					Type: wireTypeControlRequest, RequestID: testAskRequestID,
					Request: llm.ControlRequest{
						Subtype: wireSubtypeCanUseTool, ToolName: toolNameAskUserQuestion,
						Input: json.RawMessage(`{"questions":[{"question":"Which DB?"}]}`),
					},
				}},
			}
			sessions := &mutationTargetSessionManager{sessions: []ports.SessionView{sess}}
			target := mutationTarget{orch: mutationTargetOrchestrator(sessions), sessions: sessions}
			resp, err := target.AnswerAskUser(serverruntime.AskUserAnswerRequest{
				RequestID: testAskRequestID, SessionID: testSessionAskID, Answers: map[string]string{"1": "Postgres"},
			})
			if err != nil {
				t.Fatalf("AnswerAskUser() error = %v", err)
			}
			return "", "", resp.Result
		}},
		{"SendHelp", resultSent, func(t *testing.T) (string, string, string) {
			sess := &mutationTargetSessionView{
				id: testSessionHelpID, featureID: testFeatureHelpID, phase: feature.PhaseImplement, status: ports.SessionWaitingHelp, active: true,
			}
			sessions := &mutationTargetSessionManager{sessions: []ports.SessionView{sess}}
			target := mutationTarget{orch: mutationTargetOrchestrator(sessions), sessions: sessions}
			resp, err := target.SendHelp(serverruntime.HelpAnswerRequest{
				FeatureID: testFeatureHelpID, SessionID: testSessionHelpID, Message: "Use the existing migration path.",
			})
			if err != nil {
				t.Fatalf("SendHelp() error = %v", err)
			}
			return resp.FeatureID, testFeatureHelpID, resp.Result
		}},
		{"RuntimeConfig changed", resultUpdated, func(t *testing.T) (string, string, string) {
			target := newResponseShapeRuntimeConfigTarget(t)
			resp, err := target.RuntimeConfig(serverruntime.RuntimeConfigMutationRequest{
				Defaults: serverruntime.RuntimeDefaultsMutation{MaxIterations: 8},
			})
			if err != nil {
				t.Fatalf("RuntimeConfig() error = %v", err)
			}
			return "", "", resp.Result
		}},
		{"RuntimeConfig unchanged", resultUnchanged, func(t *testing.T) (string, string, string) {
			target := newResponseShapeRuntimeConfigTarget(t)
			resp, err := target.RuntimeConfig(serverruntime.RuntimeConfigMutationRequest{})
			if err != nil {
				t.Fatalf("RuntimeConfig() error = %v", err)
			}
			return "", "", resp.Result
		}},
		{"ExecuteRecovery", resultRecovered, func(t *testing.T) (string, string, string) {
			target := mutationTarget{orch: orchestrator.New(orchestrator.Deps{Recovery: responseShapeRecoveryOperator{}}, orchestrator.Hooks{})}
			resp, err := target.ExecuteRecovery(context.Background(), nil, nil)
			if err != nil {
				t.Fatalf("ExecuteRecovery() error = %v", err)
			}
			return "", "", resp.Result
		}},
		{"GeneratePublishDescription", resultGenerated, func(t *testing.T) (string, string, string) {
			_, manager, store, f := newPublishActionTarget(t)
			target := mutationTarget{orch: orchestrator.New(orchestrator.Deps{
				Lifecycle: manager, Store: store,
				PhaseRunner: newResponseShapeDescriptionRunner(t, "TITLE: Generated title\nBODY:\nGenerated body"),
			}, orchestrator.Hooks{})}
			resp, err := target.GeneratePublishDescription(f.ID, serverruntime.PublishDescriptionRequest{Repos: []string{testRepoAName}})
			if err != nil {
				t.Fatalf("GeneratePublishDescription() error = %v", err)
			}
			return resp.FeatureID, f.ID, resp.Result
		}},
		{"PublishFeature", resultPublished, func(t *testing.T) (string, string, string) {
			target, manager, _, f := newPublishActionTarget(t)
			target.orch.SetPublishRepoFn(func(featureID, repoName string) (string, error) {
				prURL := "https://github.com/acme/repo-a/pull/12"
				return prURL, manager.SetRepoPublished(featureID, repoName, prURL)
			})
			resp, err := target.PublishFeature(f.ID, serverruntime.PublishFeatureRequest{Repos: []string{testRepoAName}})
			if err != nil {
				t.Fatalf("PublishFeature() error = %v", err)
			}
			return resp.FeatureID, f.ID, resp.Result
		}},
		{"MergeFeature", resultMerged, func(t *testing.T) (string, string, string) {
			target, featureID := newResponseShapeLocalMergeTarget(t)
			resp, err := target.MergeFeature(featureID, serverruntime.GuardedFeatureActionRequest{})
			if err != nil {
				t.Fatalf("MergeFeature() error = %v", err)
			}
			return resp.FeatureID, featureID, resp.Result
		}},
		{"RewindFeature", resultRewound, func(t *testing.T) (string, string, string) {
			store, manager, f := newMutationTestFeature(t, "response shape rewind", feature.CreateOptions{Pipeline: feature.PipelineMedium}, feature.StatusCodeReady, feature.PhasePublish)
			if err := store.Modify(f.ID, func(ff *feature.Feature) error {
				ff.RepoStates = map[string]*feature.RepoState{testRepoAName: {Touched: true}}
				return nil
			}); err != nil {
				t.Fatalf("prepare feature: %v", err)
			}
			target := mutationTarget{orch: orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{})}
			resp, err := target.RewindFeature(f.ID, serverruntime.RewindFeatureRequest{TargetPhase: phaseNameImplement})
			if err != nil {
				t.Fatalf("RewindFeature() error = %v", err)
			}
			return resp.FeatureID, f.ID, resp.Result
		}},
		{"RetryFeature", resultRetried, func(t *testing.T) (string, string, string) {
			target, f := newResponseShapeImplementTarget(t, "response shape retry", func(ff *feature.Feature) {
				ff.Status = feature.StatusFailed
				ff.MaxIterations = 10
				ff.Run().Failure = &errcat.FailureRecord{
					Code: errcat.IterationBudgetExhausted,
					Context: &errcat.RecordContext{
						Phase:        &errcat.CodePhase{Name: phaseNameImplement},
						Repositories: []errcat.CodeRepository{{Name: testRepoAName}},
					},
				}
			})
			resp, err := target.RetryFeature(f.ID)
			if err != nil {
				t.Fatalf("RetryFeature() error = %v", err)
			}
			return resp.FeatureID, f.ID, resp.Result
		}},
		{"RebaseFeature", resultCreated, func(t *testing.T) (string, string, string) {
			target, parentID := newResponseShapeRebaseTarget(t)
			resp, err := target.RebaseFeature(parentID)
			if err != nil {
				t.Fatalf("RebaseFeature() error = %v", err)
			}
			if resp.ParentID != parentID {
				t.Fatalf("RebaseFeature() parent = %q; want %q", resp.ParentID, parentID)
			}
			return resp.FeatureID, responseShapeRebaseChildID, resp.Result
		}},
		{"RefactorFeature", resultCreated, func(t *testing.T) (string, string, string) {
			creator := &fakeRefactorChildCreator{child: &feature.Feature{ID: "child-refactor-shape"}}
			target, _ := newChildLaunchTarget(t, childCreatorLifecycle{refactor: creator})
			resp, err := target.RefactorFeature("parent-1", serverruntime.RefactorFeatureRequest{Name: "Rework auth"})
			if err != nil {
				t.Fatalf("RefactorFeature() error = %v", err)
			}
			return resp.FeatureID, "child-refactor-shape", resp.Result
		}},
		{"ReviewFeedbackFeature", resultCreated, func(t *testing.T) (string, string, string) {
			creator := &fakeReviewFeedbackChildCreator{child: &feature.Feature{ID: "child-review-shape"}}
			target, _ := newChildLaunchTarget(t, childCreatorLifecycle{review: creator})
			resp, err := target.ReviewFeedbackFeature("parent-1", serverruntime.ReviewFeedbackFeatureRequest{ExpectedRevision: 1})
			if err != nil {
				t.Fatalf("ReviewFeedbackFeature() error = %v", err)
			}
			return resp.FeatureID, "child-review-shape", resp.Result
		}},
		{"MarkDone", resultDone, func(t *testing.T) (string, string, string) {
			store, manager, f := newMutationTestFeature(t, "response shape done", feature.CreateOptions{}, feature.StatusPublished, feature.PhasePublish)
			target := mutationTarget{orch: orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{})}
			resp, err := target.MarkDone(f.ID, serverruntime.GuardedFeatureActionRequest{})
			if err != nil {
				t.Fatalf("MarkDone() error = %v", err)
			}
			return resp.FeatureID, f.ID, resp.Result
		}},
		{"CleanupFeature", resultCleaned, func(t *testing.T) (string, string, string) {
			target, _, f, _ := newCleanupActionTarget(t)
			resp, err := target.CleanupFeature(f.ID, serverruntime.CleanupActionRequest{})
			if err != nil {
				t.Fatalf("CleanupFeature() error = %v", err)
			}
			return resp.FeatureID, f.ID, resp.Result
		}},
		{"DiscardChild", resultDiscarded, func(t *testing.T) (string, string, string) {
			target, childID := newResponseShapeDiscardTarget(t)
			resp, err := target.DiscardChild(childID)
			if err != nil {
				t.Fatalf("DiscardChild() error = %v", err)
			}
			return resp.FeatureID, childID, resp.Result
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotFeatureID, wantFeatureID, gotResult := tc.call(t)
			if gotResult != tc.wantResult {
				t.Errorf("result = %q; want %q", gotResult, tc.wantResult)
			}
			if gotFeatureID != wantFeatureID {
				t.Errorf("feature id = %q; want %q", gotFeatureID, wantFeatureID)
			}
		})
	}
}

// newResponseShapeImplementTarget creates a feature parked in the implement
// phase with a plan artifact, applies mutate, and returns a target whose
// orchestrator dispatches implementation to a no-op runner.
func newResponseShapeImplementTarget(t *testing.T, name string, mutate func(*feature.Feature)) (mutationTarget, *feature.Feature) {
	t.Helper()
	runtimeDir := t.TempDir()
	cfg := config.NewDefault()
	cfg.Repos[testRepoAName] = config.RepoConfig{Path: filepath.Join(runtimeDir, testRepoAName)}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	f, err := manager.Create(name, "desc", []string{testRepoAName}, cfg.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create feature: %v", err)
	}
	planPath := filepath.Join(runtimeDir, "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan\n\n## Tasks\n\n**Repo:** repo-a\n\n- Implement it.\n"), 0o644); err != nil {
		t.Fatalf("WriteFile plan: %v", err)
	}
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.CurrentPhase = feature.PhaseImplement
		ff.Artifacts = map[string]string{"plan": planPath}
		mutate(ff)
		return nil
	}); err != nil {
		t.Fatalf("prepare feature: %v", err)
	}
	orch := orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{})
	orch.SetRunMultiRepoImplFn(func(*feature.Feature, string, ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
		ch := make(chan *agent.OrchestratorResult)
		close(ch)
		return ch, nil
	})
	return mutationTarget{orch: orch}, f
}

func newResponseShapePermissionSession() *mutationTargetSessionView {
	return &mutationTargetSessionView{
		id: testSessionPermissionID, featureID: testFeaturePermissionID, phase: feature.PhaseImplement,
		status: ports.SessionWaitingPermission, active: true,
		pending: []*llm.ControlRequestMessage{{
			Type: wireTypeControlRequest, RequestID: testPermRequestID,
			Request: llm.ControlRequest{
				Subtype: wireSubtypeCanUseTool, ToolName: toolNameBash,
				Input: json.RawMessage(`{"command":"go test ./cmd/agentico"}`),
			},
		}},
	}
}

// responseShapeRetryReviewSession adds the automatic-review retry capability
// the retry_auto_review decision asserts on the session.
type responseShapeRetryReviewSession struct {
	*mutationTargetSessionView
	retried []string
}

func (s *responseShapeRetryReviewSession) RetryAutomaticReview(requestID string) error {
	s.retried = append(s.retried, requestID)
	return nil
}

func newResponseShapeRuntimeConfigTarget(t *testing.T) mutationTarget {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.NewDefault()
	cfg.Defaults.MaxIterations = 3
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatalf("Save config: %v", err)
	}
	return mutationTarget{cfg: cfg, configPath: configPath}
}

// responseShapeRecoveryOperator is a recovery operator whose execution
// always succeeds.
type responseShapeRecoveryOperator struct{}

func (responseShapeRecoveryOperator) ScanForRecovery(context.Context) ([]ports.RecoveryItem, error) {
	return nil, nil
}

func (responseShapeRecoveryOperator) ExecuteRecovery(context.Context, []ports.RecoveryItem, map[string]ports.RecoveryAction) error {
	return nil
}

// responseShapeDescriptionSession completes a utility session with a
// canned assistant output.
type responseShapeDescriptionSession struct {
	*mocks.MockSessionView
}

func (s *responseShapeDescriptionSession) SetStatus(ports.SessionStatus)                  {}
func (s *responseShapeDescriptionSession) SetLogFile(*os.File)                            {}
func (s *responseShapeDescriptionSession) AddCleanupFunc(func())                          {}
func (s *responseShapeDescriptionSession) SetHasUnansweredQuestion(bool)                  {}
func (s *responseShapeDescriptionSession) CloseStdin()                                    {}
func (s *responseShapeDescriptionSession) SetOnToolAllowed(func(string, json.RawMessage)) {}
func (s *responseShapeDescriptionSession) SetOnFileRead(func(llm.FileReadEvent))          {}
func (s *responseShapeDescriptionSession) SetOnSubagentEvent(func(llm.SDKMessage))        {}

func newResponseShapeDescriptionRunner(t *testing.T, output string) *agent.PhaseRunner {
	t.Helper()
	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, _ []string, _ string, _ []string, _ ...*ports.SessionOpts) (ports.SessionHandle, error) {
		view := mocks.NewMockSessionView(id, featureID)
		view.PhaseVal = phase
		view.MessageLogVal.Append(mocks.AssistantTextMessage(output))
		view.CostVal = &llm.ResultMessage{Type: "result", Subtype: "success", Result: "done", StopReason: "end_turn"}
		view.StatusChVal <- "SUCCESS"
		return &responseShapeDescriptionSession{MockSessionView: view}, nil
	}
	pr := &agent.PhaseRunner{SessionManager: sm, StateDir: t.TempDir()}
	pr.BuildSessionFn = func(opts agent.BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
		return []string{"mock"}, nil, &ports.SessionOpts{RepoName: opts.RepoName}, nil
	}
	return pr
}

func runResponseShapeGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s in %s: %s: %v", strings.Join(args, " "), dir, strings.TrimSpace(string(out)), err)
	}
}

// newResponseShapeLocalMergeTarget builds a non-publishable published
// feature whose branch carries one commit ahead of main.
func newResponseShapeLocalMergeTarget(t *testing.T) (mutationTarget, string) {
	t.Helper()
	runtimeDir := t.TempDir()
	repoPath := filepath.Join(runtimeDir, testRepoAName)
	initMutationGitRepo(t, repoPath)
	runResponseShapeGit(t, repoPath, "checkout", "-b", "feature/local-merge-shape")
	if err := os.WriteFile(filepath.Join(repoPath, "feature.txt"), []byte("merged\n"), 0o644); err != nil {
		t.Fatalf("write feature file: %v", err)
	}
	runResponseShapeGit(t, repoPath, "add", "feature.txt")
	runResponseShapeGit(t, repoPath, "commit", "-m", "feature change")
	runResponseShapeGit(t, repoPath, "checkout", "main")

	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	notPublishable := false
	f := &feature.Feature{
		ID: "feat-local-merge-shape", Name: "Local merge shape", Slug: "local-merge-shape",
		Status: feature.StatusPublished, CurrentPhase: feature.PhasePublish, SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{{
			Name: testRepoAName, Path: repoPath, WorktreePath: repoPath,
			Branch: "feature/local-merge-shape", BaseBranch: "main", Publishable: &notPublishable,
		}},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save feature: %v", err)
	}
	return mutationTarget{orch: newStoreOrchestrator(store)}, f.ID
}

const responseShapeRebaseChildID = "child-rebase-shape"

// responseShapeRebaseLifecycle creates the rebase child the orchestrator's
// child launch asks for.
type responseShapeRebaseLifecycle struct {
	*mocks.MockFeatureLifecycle
}

func (responseShapeRebaseLifecycle) CreateRefactorChild(string, feature.RefactorChildSpec) (*feature.Feature, error) {
	return nil, os.ErrInvalid
}

func (responseShapeRebaseLifecycle) LaunchReviewFeedbackChildFromDraft(string, int64, *bool) (*feature.ReviewFeedbackLaunchResult, error) {
	return nil, os.ErrInvalid
}

func (responseShapeRebaseLifecycle) CreateRebaseChild(parentID string, _ feature.RebaseChildSpec) (*feature.Feature, error) {
	return &feature.Feature{
		ID:     responseShapeRebaseChildID,
		Parent: &feature.ChildRelationship{ParentID: parentID, Kind: feature.ChildKindRebase},
	}, nil
}

// newResponseShapeRebaseTarget builds a published, non-publishable parent
// whose clean worktree sits one commit behind its local base branch, so the
// rebase preflight finds work and launches a child.
func newResponseShapeRebaseTarget(t *testing.T) (mutationTarget, string) {
	t.Helper()
	runtimeDir := t.TempDir()
	repoPath := filepath.Join(runtimeDir, testRepoAName)
	initMutationGitRepo(t, repoPath)
	runResponseShapeGit(t, repoPath, "branch", "feature/rebase-shape")
	runResponseShapeGit(t, repoPath, "commit", "--allow-empty", "-m", "base moved ahead")
	runResponseShapeGit(t, repoPath, "checkout", "feature/rebase-shape")

	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	notPublishable := false
	parent := &feature.Feature{
		ID: "parent-rebase-shape", Name: "Rebase shape", Slug: "rebase-shape",
		Status: feature.StatusPublished, CurrentPhase: feature.PhasePublish, SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{{
			Name: testRepoAName, Path: repoPath, WorktreePath: repoPath,
			Branch: "feature/rebase-shape", BaseBranch: "main", Publishable: &notPublishable,
		}},
	}
	if err := store.Save(parent); err != nil {
		t.Fatalf("Save parent: %v", err)
	}
	worktrees := mocks.NewMockWorktreeOps()
	worktrees.CurrentHeadSHAFn = func(string) (string, error) { return "0123456789abcdef0123456789abcdef01234567", nil }
	lifecycle := mocks.NewMockFeatureLifecycle()
	lifecycle.GetFn = func(id string) (*feature.Feature, error) {
		return &feature.Feature{ID: id, Parent: &feature.ChildRelationship{ParentID: parent.ID, Kind: feature.ChildKindRebase}}, nil
	}
	lifecycle.RunSetupFn = func(string, ...feature.SetupRunnerOptions) error { return nil }
	orch := orchestrator.New(orchestrator.Deps{
		Lifecycle: responseShapeRebaseLifecycle{MockFeatureLifecycle: lifecycle},
		Store:     store,
		Worktrees: worktrees,
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		orch.WaitForCycles()
		_ = orch.Shutdown()
	})
	return mutationTarget{orch: orch}, parent.ID
}

// newResponseShapeDiscardTarget stores an active refactor child (without
// worktrees) under a published parent.
func newResponseShapeDiscardTarget(t *testing.T) (mutationTarget, string) {
	t.Helper()
	runtimeDir := t.TempDir()
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	parent := &feature.Feature{
		ID: "parent-discard-shape", Name: "Discard parent", Slug: "parent-discard-shape",
		Status: feature.StatusPublished, Pipeline: feature.PipelineMoonshot, SchemaVersion: feature.SchemaVersionCurrent,
	}
	child := &feature.Feature{
		ID: "child-discard-shape", Name: "Discard child", Slug: "child-discard-shape",
		Status: feature.StatusCreated, Pipeline: feature.PipelineMedium, SchemaVersion: feature.SchemaVersionCurrent,
		ActiveRun: 1, RunCount: 1,
		Repos: []feature.FeatureRepo{{
			Name: testRepoAName, Path: filepath.Join(runtimeDir, testRepoAName),
			Branch: "feature/child-discard-shape", BaseBranch: "main",
		}},
		Parent: &feature.ChildRelationship{ParentID: parent.ID, Kind: feature.ChildKindRefactor},
	}
	for _, f := range []*feature.Feature{parent, child} {
		if err := store.Save(f); err != nil {
			t.Fatalf("Save %s: %v", f.ID, err)
		}
	}
	return mutationTarget{orch: newStoreOrchestrator(store)}, child.ID
}
