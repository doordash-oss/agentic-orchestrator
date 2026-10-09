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

// Package mutations implements the server's mutation target: the REST
// feature actions, control answers, and runtime-config updates the HTTP
// handler delegates to. It adapts wire DTOs onto single orchestrator
// operations so the orchestrator never learns REST shapes and the module
// never sequences locks, admission, or dispatch.
package mutations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// Wire-level result/status/action strings shared across mutation handlers (and
// reused by their tests) to avoid duplicated literals.
const (
	resultFailed       = "failed"
	resultAnswered     = "answered"
	resultConflict     = "conflict"
	resultCleaned      = "cleaned"
	resultStarted      = "started"
	resultUpdated      = "updated"
	resultSent         = "sent"
	resultRetried      = "retried"
	resultSetupStarted = "setup_started"
	resultCreated      = "created"

	dispatchNone = "none"

	toolNameBash            = "Bash"
	toolNameAskUserQuestion = "AskUserQuestion"

	phaseNameInquire   = "inquire"
	phaseNameImplement = "implement"
	phaseNameResearch  = "research"
	phaseNamePlan      = "plan"

	cleanupTargetWorktrees = "worktrees"
)

// Deps names the production handles the mutation module drives. Orchestrator
// is required; the remaining fields degrade the actions that need them to
// explicit "not available" errors.
type Deps struct {
	Orchestrator *orchestrator.Orchestrator
	Sessions     ports.SessionManager
	// PermissionCache records "always allow" answers.
	PermissionCache *permission.Cache
	// Config is the loaded runtime config; ConfigPath is where config and
	// pipeline-preference updates persist.
	Config     *config.Config
	ConfigPath string
}

// New returns the server mutation target backed by deps.
func New(deps Deps) serverruntime.MutationTarget {
	return &mutationTarget{
		orch:            deps.Orchestrator,
		cfg:             deps.Config,
		configPath:      deps.ConfigPath,
		sessions:        deps.Sessions,
		permissionCache: deps.PermissionCache,
	}
}

type mutationTarget struct {
	mu              sync.Mutex
	orch            *orchestrator.Orchestrator
	cfg             *config.Config
	configPath      string
	sessions        ports.SessionManager
	permissionCache *permission.Cache
}

func (t *mutationTarget) CreateFeature(req serverruntime.CreateFeatureRequest) (serverruntime.CreateFeatureResponse, error) {
	cfg := t.cfg
	if cfg == nil {
		cfg = config.NewDefault()
	}
	models := cfg.Defaults.Models
	if !req.Models.IsEmpty() {
		models = mergeModelConfig(models, req.Models)
	}
	effort := cfg.Defaults.Effort
	effort = config.OverlayEffortConfig(effort, req.Effort)
	pipeline := effectiveCreatePipeline(req.Pipeline, cfg)
	checkpoints := pipeline.ProjectGates(req.Checkpoints, true).Checkpoints
	sourceExpectations, err := createSourceExpectations(req.RepositorySources)
	if err != nil {
		return serverruntime.CreateFeatureResponse{}, err
	}
	f, err := t.orch.CreateFeature(req.Name, req.Description, req.Repos, models, req.ExitCriteria, req.Inquireness, req.Images, feature.CreateOptions{
		UseCurrentBranch:        req.UseCurrentBranch,
		UseCurrentBranchPerRepo: req.UseCurrentBranchPerRepo,
		Checkpoints:             checkpoints,
		Effort:                  effort,
		Attachments:             req.Attachments,
		QueueSetup:              true,
		RiskLevel:               req.RiskLevel,
		Pipeline:                req.Pipeline,
		SourceExpectations:      sourceExpectations,
		// Every ordinary server creation is accepted against immutable local
		// commits. Requests from source-aware clients revalidate their displayed
		// expectations; trusted compatibility callers without expectations use
		// the same guarded local capture at acceptance time.
		PinLocalSources: true,
	})
	if err != nil {
		if errors.Is(err, git.ErrLocalSourceStale) {
			var sourceErr *feature.RepoSourceAcceptanceError
			var staleErr *git.LocalSourceStaleError
			var options []errcat.Option
			if errors.As(err, &sourceErr) && errors.As(err, &staleErr) && staleErr.Refreshed.Commit != "" {
				options = append(options, errcat.WithRepositories(errcat.CodeRepository{
					Name: sourceErr.Repo, Branch: staleErr.Refreshed.Branch, ObservedSHA: staleErr.Refreshed.Commit,
				}))
			}
			return serverruntime.CreateFeatureResponse{}, &serverruntime.ActionConflictError{
				Err: err, Code: errcat.LocalSourceStale,
				Detail: "The selected local source changed. Refresh repository sources and submit again.", Options: options,
			}
		}
		return serverruntime.CreateFeatureResponse{}, err
	}
	if err := t.persistPipelinePreferences(featureRepoNames(f), f.EffectivePipeline(), f.Models, f.Effort, f.Inquireness, f.Checkpoints, true); err != nil {
		return serverruntime.CreateFeatureResponse{}, err
	}
	return serverruntime.CreateFeatureResponse{
		FeatureID: f.ID, Result: "created", Warnings: wireCreationWarnings(f.CreationWarnings),
	}, nil
}

func wireCreationWarnings(warnings []git.BranchProbeWarning) []serverruntime.Error {
	if len(warnings) == 0 {
		return nil
	}
	result := make([]serverruntime.Error, 0, len(warnings))
	for _, warning := range warnings {
		repositories := []errcat.CodeRepository{{Name: warning.Repository, Branch: warning.Branch}}
		result = append(result, serverruntime.WireCanonicalError(errcat.New(
			errcat.BranchCollisionProbeUnavailable,
			errcat.WithRepositories(repositories...),
			errcat.WithParams(errcat.WarningRepoParams{Repositories: repositories}),
			errcat.WithDiagnostics(serverruntime.SafeDisplayText(warning.Diagnostics, 300)),
		)))
	}
	return result
}

func createSourceExpectations(sources []serverruntime.RepositorySource) ([]feature.RepoSourceExpectation, error) {
	result := make([]feature.RepoSourceExpectation, 0, len(sources))
	for _, source := range sources {
		device, err := strconv.ParseUint(source.Identity.Device, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid repository source device for %q: %w", source.RepoKey, err)
		}
		inode, err := strconv.ParseUint(source.Identity.Inode, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid repository source inode for %q: %w", source.RepoKey, err)
		}
		result = append(result, feature.RepoSourceExpectation{
			RepoKey: source.RepoKey,
			Source: git.LocalSourceExpectation{
				Identity: git.RepoIdentity{
					Path: source.Identity.Path, CommonDir: source.Identity.CommonDir,
					Device: device, Inode: inode, BirthTime: source.Identity.BirthTime,
				},
				Mode: git.LocalSourceMode(source.Mode), Kind: string(source.Kind),
				Branch: source.Branch, ObservedCommit: source.ObservedSha,
			},
		})
	}
	return result, nil
}

// SetupFeature dispatches server-owned durable setup for a freshly created
// feature — or a retry of a failed setup that reruns only the unfinished
// tasks — without starting orchestration. On success the feature returns to
// the startable StatusCreated state; failures are persisted on the feature's
// setup state and surfaced through setup events, so the HTTP response only
// acknowledges the dispatch.
func (t *mutationTarget) SetupFeature(featureID string) (serverruntime.FeatureSetupResponse, error) {
	resp := serverruntime.FeatureSetupResponse{FeatureID: featureID}
	if err := t.orch.DispatchSetup(featureID); err != nil {
		if errors.Is(err, orchestrator.ErrNoSetupWork) {
			return resp, &serverruntime.ActionConflictError{
				Detail: fmt.Sprintf("feature %q has no pending or failed setup work", featureID),
			}
		}
		return resp, err
	}
	resp.Result = resultSetupStarted
	return resp, nil
}

func (t *mutationTarget) StartFeature(featureID string) (serverruntime.FeatureStartResponse, error) {
	if err := t.orch.StartFeature(featureID); err != nil {
		return serverruntime.FeatureStartResponse{}, err
	}
	return serverruntime.FeatureStartResponse{FeatureID: featureID, Result: resultStarted}, nil
}

func (t *mutationTarget) ResumeFeature(featureID string) (serverruntime.FeatureStartResponse, error) {
	return t.StartFeature(featureID)
}

func (t *mutationTarget) StopFeature(featureID string) (serverruntime.FeatureStopResponse, error) {
	if err := t.orch.StopFeature(featureID); err != nil {
		return serverruntime.FeatureStopResponse{}, err
	}
	return serverruntime.FeatureStopResponse{FeatureID: featureID, Result: "stopped"}, nil
}

func (t *mutationTarget) RestartFeature(featureID string, req serverruntime.RestartFeatureRequest) (serverruntime.FeatureRestartResponse, error) {
	outcome, err := t.orch.RestartFeature(featureID, req.MaxIterationsDelta, req.MaxPlanIterationsDelta)
	if err != nil && outcome.Action != orchestrator.RestartDispatchPhase {
		return serverruntime.FeatureRestartResponse{}, err
	}
	resp := serverruntime.FeatureRestartResponse{FeatureID: featureID, Result: "restarted", Phase: outcome.Phase.String()}
	if outcome.Action == orchestrator.RestartDispatchPhase {
		resp.Dispatch = "phase"
	} else {
		resp.Dispatch = dispatchNone
	}
	if err != nil {
		resp.Result = resultFailed
		return resp, err
	}
	return resp, nil
}

func (t *mutationTarget) ReviewDecision(featureID string, req serverruntime.ReviewDecisionRequest) error {
	decision := orchestrator.ReviewDecision{
		Decision:    req.Decision,
		TargetPhase: parseServerPhase(req.Phase),
		IsRewind:    req.IsRewind,
		PhasePlan:   req.PhasePlan,
		Roadmap:     req.Roadmap,
		Comment:     req.Comment,
	}
	return t.orch.HandleReviewDecision(featureID, decision)
}

func (t *mutationTarget) UpdateFeatureConfig(featureID string, req serverruntime.FeatureConfigMutationRequest) (serverruntime.FeatureConfigUpdateResponse, error) {
	input := orchestrator.UpdateFeatureConfigInput{
		Models:             req.Models,
		Effort:             req.Effort,
		Inquireness:        feature.Inquireness(req.Inquireness),
		Checkpoints:        req.Checkpoints,
		InputNotifications: feature.InputNotificationsMode(req.InputNotifications),
		Pipeline:           req.Pipeline,
	}
	if req.AutomaticReviewMode != nil {
		mode, err := feature.ParseAutomaticReviewMode(*req.AutomaticReviewMode)
		if err != nil {
			return serverruntime.FeatureConfigUpdateResponse{}, err
		}
		input.AutomaticReviewMode = &mode
	}
	f, err := t.orch.UpdateFeatureConfig(featureID, input)
	if err != nil {
		return serverruntime.FeatureConfigUpdateResponse{}, err
	}
	pipeline := req.Pipeline
	if pipeline == "" {
		pipeline = f.EffectivePipeline()
	}
	if err := t.persistPipelinePreferences(featureRepoNames(f), pipeline, f.Models, f.Effort, f.Inquireness, f.Checkpoints, f.IsPublishable()); err != nil {
		return serverruntime.FeatureConfigUpdateResponse{}, err
	}
	return serverruntime.FeatureConfigUpdateResponse{FeatureID: featureID, Result: resultUpdated}, nil
}

func (t *mutationTarget) ResumeNeedUserInput(featureID string, req serverruntime.NeedUserInputResumeRequest) (serverruntime.NeedUserInputResumeResponse, error) {
	if err := t.orch.ResumeNeedUserInput(featureID, orchestrator.NeedUserInputResume{}); err != nil {
		return serverruntime.NeedUserInputResumeResponse{}, err
	}
	return serverruntime.NeedUserInputResumeResponse{FeatureID: featureID, Result: "resumed"}, nil
}

func (t *mutationTarget) WaiveTestingContractItems(featureID string, req serverruntime.TestingContractWaiveRequest) (serverruntime.TestingContractWaiveResponse, error) {
	result, err := t.orch.WaiveTestingContractItems(featureID, orchestrator.TestingContractWaiver{
		ItemIDs: req.ItemIDs, Reason: req.Reason,
		ExpectedRun: req.ActiveRun, ExpectedPhase: req.RoadmapPhase, ExpectedRevision: req.ContractRevision,
	})
	if err != nil {
		if errors.Is(err, orchestrator.ErrStaleTestingContract) {
			err = &serverruntime.ActionConflictError{Err: err, Code: errcat.Conflict}
		}
		return serverruntime.TestingContractWaiveResponse{FeatureID: featureID, Result: resultFailed}, err
	}
	return serverruntime.TestingContractWaiveResponse{
		FeatureID: featureID, Result: "waived",
		ContractRevision: result.Revision, WaivedItems: result.WaivedItems,
	}, nil
}

func (t *mutationTarget) DraftNeedUserInputAnswers(featureID string, req serverruntime.NeedUserInputDraftRequest) (serverruntime.NeedUserInputDraftResponse, error) {
	gatePath, err := t.orch.PendingNeedUserInputGatePath(featureID)
	if err != nil {
		return serverruntime.NeedUserInputDraftResponse{}, err
	}
	rec, err := agent.ReadNeedUserInputRecord(gatePath)
	if err != nil {
		return serverruntime.NeedUserInputDraftResponse{}, fmt.Errorf("read need-user-input gate: %w", err)
	}
	if err := applyNeedUserInputDraftAnswers(&rec, req.Answers); err != nil {
		return serverruntime.NeedUserInputDraftResponse{}, err
	}
	if err := agent.WriteNeedUserInputRecord(gatePath, rec); err != nil {
		return serverruntime.NeedUserInputDraftResponse{}, fmt.Errorf("write need-user-input gate: %w", err)
	}
	return serverruntime.NeedUserInputDraftResponse{FeatureID: featureID, Result: "drafted"}, nil
}

func (t *mutationTarget) AnswerPermission(req serverruntime.PermissionAnswerRequest) (serverruntime.PermissionAnswerResponse, error) {
	sess, pending, err := t.findPendingControlRequest(req.SessionID, req.RequestID, false)
	if err != nil {
		return serverruntime.PermissionAnswerResponse{}, err
	}
	if req.Decision == "retry_auto_review" {
		if req.AutoApproveScope != "" || req.RememberScope != nil || req.RememberPattern != "" {
			return serverruntime.PermissionAnswerResponse{}, errors.New("retry cannot change permission settings")
		}
		retry, ok := sess.(interface{ RetryAutomaticReview(string) error })
		if !ok || pending.AutomaticReview == nil {
			return serverruntime.PermissionAnswerResponse{}, errors.New("automatic review cannot be retried for this request")
		}
		if err := retry.RetryAutomaticReview(pending.RequestID); err != nil {
			return serverruntime.PermissionAnswerResponse{}, err
		}
		return serverruntime.PermissionAnswerResponse{SessionID: sess.ID(), RequestID: pending.RequestID, Decision: req.Decision, Result: "reviewed"}, nil
	}
	if req.AutoApproveScope != "" {
		if err := t.enableAutomaticReview(req.AutoApproveScope, sess.FeatureID()); err != nil {
			return serverruntime.PermissionAnswerResponse{}, err
		}
	}
	rememberScope := ""
	if req.RememberScope != nil {
		rememberScope = *req.RememberScope
	}
	result, err := t.permissionAnswerService().Answer(permission.AnswerRequest{
		RequestID:        pending.RequestID,
		SessionID:        sess.ID(),
		FeatureID:        sess.FeatureID(),
		ToolName:         pending.Request.ToolName,
		ToolInput:        string(pending.Request.Input),
		Decision:         req.Decision,
		RememberPattern:  req.RememberPattern,
		RememberScope:    rememberScope,
		RememberScopeSet: req.RememberScope != nil,
	}, func(requestID string, allow bool, reason string) error {
		if allow && req.Decision == "allow_remember" {
			if remembering, ok := sess.(interface{ RespondToControlRemember(string) error }); ok {
				return remembering.RespondToControlRemember(requestID)
			}
		}
		return sess.RespondToControl(requestID, allow, reason)
	})
	if err != nil {
		return serverruntime.PermissionAnswerResponse{}, err
	}
	return serverruntime.PermissionAnswerResponse{
		SessionID:      sess.ID(),
		RequestID:      pending.RequestID,
		Decision:       result.Decision,
		Result:         resultAnswered,
		AlreadyExisted: result.AlreadyExisted,
		AuditWarning:   result.AuditWarning,
	}, nil
}

// enableAutomaticReview turns automatic Bash review on for one feature or for
// the workspace default. Running sessions read the setting live, so the change
// applies to the next Bash request.
func (t *mutationTarget) enableAutomaticReview(scope, featureID string) error {
	switch scope {
	case serverruntime.AutoApproveScopeWorkspace:
		enabled := true
		skip := false
		_, err := t.RuntimeConfig(serverruntime.RuntimeConfigMutationRequest{
			Defaults: serverruntime.RuntimeDefaultsMutation{AutomaticReviewEnabled: &enabled, DangerouslySkipPermissions: &skip},
		})
		return err
	case serverruntime.AutoApproveScopeFeature:
		if featureID == "" {
			return errors.New("this request has no feature; enable auto-approve for the workspace instead")
		}
		return t.orch.EnableFeatureAutomaticReview(featureID)
	default:
		return fmt.Errorf("unknown auto_approve_scope %q", scope)
	}
}

func (t *mutationTarget) permissionAnswerService() *permission.AnswerService {
	var audit *permission.AuditSink
	if t != nil && t.permissionCache != nil && t.permissionCache.StoreRef() != nil {
		audit = permission.NewAuditSink(t.permissionCache.StoreRef().BaseDir)
	}
	return permission.NewAnswerService(t.permissionCache, audit)
}

func (t *mutationTarget) AnswerAskUser(req serverruntime.AskUserAnswerRequest) (serverruntime.AskUserAnswerResponse, error) {
	sess, pending, err := t.findPendingControlRequest(req.SessionID, req.RequestID, true)
	if err != nil {
		return serverruntime.AskUserAnswerResponse{}, err
	}
	answers := normalizeAskUserAnswerKeys(pending.Request.Input, req.Answers)
	if err := sess.RespondToAskUser(pending.RequestID, pending.Request.Input, answers, nil); err != nil {
		return serverruntime.AskUserAnswerResponse{}, fmt.Errorf("answer ask-user question: %w", err)
	}
	return serverruntime.AskUserAnswerResponse{SessionID: sess.ID(), RequestID: pending.RequestID, Result: resultAnswered}, nil
}

func normalizeAskUserAnswerKeys(input json.RawMessage, answers map[string]string) map[string]string {
	if len(input) == 0 || len(answers) == 0 {
		return answers
	}
	var envelope struct {
		Questions []struct {
			Question string `json:"question"`
			Header   string `json:"header"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(input, &envelope); err != nil || len(envelope.Questions) == 0 {
		return answers
	}
	keys := make([]string, 0, len(envelope.Questions))
	for _, q := range envelope.Questions {
		key := q.Question
		if strings.TrimSpace(key) == "" {
			key = q.Header
		}
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return answers
	}

	normalized := make(map[string]string, len(answers))
	remaining := make(map[string]string, len(answers))
	for key, answer := range answers {
		remaining[key] = answer
	}
	for _, originalKey := range keys {
		if answer, ok := remaining[originalKey]; ok {
			normalized[originalKey] = answer
			delete(remaining, originalKey)
			continue
		}
		var matchedKey string
		for submittedKey := range remaining {
			if askUserSubmittedKeyMatchesOriginal(submittedKey, originalKey) {
				if matchedKey != "" {
					matchedKey = ""
					break
				}
				matchedKey = submittedKey
			}
		}
		if matchedKey != "" {
			normalized[originalKey] = remaining[matchedKey]
			delete(remaining, matchedKey)
		}
	}
	if len(keys) == 1 && len(normalized) == 0 && len(answers) == 1 {
		for _, answer := range answers {
			return map[string]string{keys[0]: answer}
		}
	}
	for key, answer := range remaining {
		normalized[key] = answer
	}
	return normalized
}

func askUserSubmittedKeyMatchesOriginal(submittedKey, originalKey string) bool {
	submittedKey = strings.TrimSpace(submittedKey)
	originalKey = strings.TrimSpace(originalKey)
	if submittedKey == "" || originalKey == "" {
		return false
	}
	if submittedKey == originalKey {
		return true
	}
	if strings.HasSuffix(submittedKey, "...") {
		prefix := strings.TrimSuffix(submittedKey, "...")
		return prefix != "" && strings.HasPrefix(originalKey, prefix)
	}
	return false
}

func (t *mutationTarget) SendHelp(req serverruntime.HelpAnswerRequest) (serverruntime.HelpSendResponse, error) {
	sess, err := t.helpSession(req)
	if err != nil {
		if resp, ok, queueErr := t.sendQueuedFeatureHelp(req); ok || queueErr != nil {
			return resp, queueErr
		}
		return serverruntime.HelpSendResponse{}, err
	}
	if err := sess.SendUserMessage(req.Message); err != nil {
		return serverruntime.HelpSendResponse{}, fmt.Errorf("send help message: %w", err)
	}
	return serverruntime.HelpSendResponse{FeatureID: sess.FeatureID(), SessionID: sess.ID(), Result: resultSent}, nil
}

func (t *mutationTarget) RuntimeConfig(req serverruntime.RuntimeConfigMutationRequest) (serverruntime.RuntimeConfigUpdateResponse, error) {
	if t.configPath == "" {
		return serverruntime.RuntimeConfigUpdateResponse{}, errors.New("config path is not available")
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	cfg := t.cfg
	if cfg == nil {
		cfg = config.NewDefault()
	}
	changed := mergeRuntimeDefaultsMutation(&cfg.Defaults, req.Defaults)
	if req.WorkspaceRoots != nil {
		if !slices.Equal(cfg.WorkspaceRoots, *req.WorkspaceRoots) {
			changed = true
		}
		cfg.WorkspaceRoots = append([]string(nil), (*req.WorkspaceRoots)...)
		config.DiscoverReposFromRoots(cfg)
	}
	if req.Notifications != nil && cfg.Notifications.MuteFeatureInput != req.Notifications.MuteFeatureInput {
		cfg.Notifications.MuteFeatureInput = req.Notifications.MuteFeatureInput
		changed = true
	}
	if err := config.Save(t.configPath, cfg); err != nil {
		return serverruntime.RuntimeConfigUpdateResponse{}, err
	}
	t.cfg = cfg
	status := "unchanged"
	if changed {
		status = resultUpdated
	}
	return serverruntime.RuntimeConfigUpdateResponse{Result: status}, nil
}

func (t *mutationTarget) ScanRecovery(ctx context.Context) ([]ports.RecoveryItem, error) {
	return t.orch.ScanRecovery(ctx)
}

func (t *mutationTarget) ExecuteRecovery(ctx context.Context, items []ports.RecoveryItem, actions map[string]ports.RecoveryAction) (serverruntime.RecoveryActionResponse, error) {
	if err := t.orch.ExecuteRecovery(ctx, items, actions); err != nil {
		return serverruntime.RecoveryActionResponse{}, err
	}
	return serverruntime.RecoveryActionResponse{Result: "recovered"}, nil
}

func (t *mutationTarget) PublishFeature(featureID string, req serverruntime.PublishFeatureRequest) (serverruntime.PublishFeatureResponse, error) {
	if err := t.rejectStaleCompletionPreflight(featureID, req.SourceRevision); err != nil {
		return serverruntime.PublishFeatureResponse{FeatureID: featureID, Result: resultFailed}, err
	}
	if err := t.orch.PublishWithOptions(featureID, orchestrator.PublishOptions{
		Repos: req.Repos,
		Title: req.Title,
		Body:  req.Body,
	}); err != nil {
		if conflict := actionConflictError(err); conflict != nil {
			return serverruntime.PublishFeatureResponse{FeatureID: featureID, Result: resultConflict}, conflict
		}
		return serverruntime.PublishFeatureResponse{FeatureID: featureID, Result: resultFailed}, err
	}
	return serverruntime.PublishFeatureResponse{FeatureID: featureID, Result: "published"}, nil
}

func (t *mutationTarget) GeneratePublishDescription(featureID string, req serverruntime.PublishDescriptionRequest) (serverruntime.PublishDescriptionResponse, error) {
	title, body, err := t.orch.GeneratePublishDescription(featureID, orchestrator.PublishDescriptionOptions{
		Repos: req.Repos,
	})
	if err != nil {
		return serverruntime.PublishDescriptionResponse{FeatureID: featureID, Title: title, Body: body, Result: "generated"}, err
	}
	return serverruntime.PublishDescriptionResponse{FeatureID: featureID, Title: title, Body: body, Result: "generated"}, nil
}

func (t *mutationTarget) MergeFeature(featureID string, req serverruntime.GuardedFeatureActionRequest) (serverruntime.MergeFeatureResponse, error) {
	if err := t.rejectStaleCompletionPreflight(featureID, req.SourceRevision); err != nil {
		return serverruntime.MergeFeatureResponse{FeatureID: featureID, Result: resultFailed}, err
	}
	if err := t.orch.MergeFeatureLocal(featureID); err != nil {
		return serverruntime.MergeFeatureResponse{FeatureID: featureID, Result: resultFailed}, err
	}
	return serverruntime.MergeFeatureResponse{FeatureID: featureID, Result: "merged"}, nil
}

func (t *mutationTarget) RepositoryPath(featureID, repoName string) (serverruntime.RepositoryPathResponse, error) {
	resp := serverruntime.RepositoryPathResponse{FeatureID: featureID, Repo: repoName}
	path, err := t.orch.RepositoryWorktreePath(featureID, repoName)
	if err != nil {
		return resp, err
	}
	resp.Path = path
	return resp, nil
}

func (t *mutationTarget) RewindFeature(featureID string, req serverruntime.RewindFeatureRequest) (serverruntime.RewindFeatureResponse, error) {
	requestedTarget := strings.ToLower(strings.TrimSpace(req.TargetPhase))
	targetPhase, err := feature.ParsePhaseName(req.TargetPhase)
	resp := serverruntime.RewindFeatureResponse{FeatureID: featureID, TargetPhase: requestedTarget, RoadmapPhase: req.RoadmapPhase}
	if err == nil {
		resp.TargetPhase = targetPhase.DirName()
	}
	if req.UpgradePipeline != "" {
		resp.UpgradePipeline = string(req.UpgradePipeline)
	}
	if err != nil {
		resp.Result = resultFailed
		return resp, err
	}
	result, err := t.orch.Rewind(featureID, orchestrator.RewindInput{
		Request:         feature.RewindRequest{TargetPhase: targetPhase, RoadmapPhase: req.RoadmapPhase},
		UpgradePipeline: feature.PipelineProfile(req.UpgradePipeline),
		SourceRevision:  req.SourceRevision,
		SourceRunNumber: req.SourceRunNumber,
	})
	if errors.Is(err, orchestrator.ErrStaleRewindPreview) {
		resp.Result = resultFailed
		return resp, err
	}
	if result.EffectivePhase != 0 || strings.EqualFold(req.TargetPhase, phaseNameResearch) {
		resp.EffectivePhase = result.EffectivePhase.DirName()
	}
	resp.SourceRunNumber = result.SourceRunNumber
	resp.Warnings = wireRewindWarnings(result.Warnings)
	if err != nil {
		resp.Result = resultFailed
		return resp, err
	}
	resp.Result = "rewound"
	resp.NewRunNumber = result.NewRunNumber
	return resp, nil
}

// wireRewindWarnings classifies the feature manager's typed rewind warnings
// once at this boundary into the canonical rewind warning codes, with the
// repositories block and the raw cause as bounded, redacted diagnostics.
func wireRewindWarnings(warnings []feature.RewindWarning) []serverruntime.Error {
	if len(warnings) == 0 {
		return nil
	}
	out := make([]serverruntime.Error, 0, len(warnings))
	for _, warning := range warnings {
		var code errcat.Code
		switch warning.Kind {
		case feature.RewindWarningPullRequestClose:
			code = errcat.RewindPullRequestCloseFailed
		case feature.RewindWarningBackupBranch:
			code = errcat.RewindBackupBranchFailed
		default:
			code = errcat.RewindWorktreeResetFailed
		}
		diagnostics := ""
		if warning.Err != nil {
			diagnostics = serverruntime.SafeDisplayText(warning.Err.Error(), 300)
		}
		rendered := errcat.New(
			code,
			errcat.WithRepositories(errcat.CodeRepository{Name: warning.Repo, Branch: warning.Branch}),
			errcat.WithParams(errcat.WarningRepoParams{
				Repositories: []errcat.CodeRepository{{Name: warning.Repo, Branch: warning.Branch}},
			}),
			errcat.WithDiagnostics(diagnostics),
		)
		out = append(out, serverruntime.WireCanonicalError(rendered))
	}
	return out
}

// wireRepositoryDiffFailure classifies one typed repository-diff partial
// failure into its canonical warning code with the repositories block and
// the raw cause as bounded, redacted diagnostics.
func wireRepositoryDiffFailure(repoName string, failure *orchestrator.RepositoryDiffFailure) *serverruntime.Error {
	if failure == nil {
		return nil
	}
	code := errcat.RepositoryDiffFailed
	if failure.Kind == orchestrator.RepositoryDiffWorktreeUnavailable {
		code = errcat.RepositoryWorktreeUnavailable
	}
	diagnostics := ""
	if failure.Err != nil {
		diagnostics = serverruntime.SafeDisplayText(failure.Err.Error(), 200)
	}
	repos := []errcat.CodeRepository{{Name: repoName}}
	rendered := errcat.New(
		code,
		errcat.WithRepositories(repos...),
		errcat.WithParams(errcat.WarningRepoParams{Repositories: repos}),
		errcat.WithDiagnostics(diagnostics),
	)
	wire := serverruntime.WireCanonicalError(rendered)
	return &wire
}

func (t *mutationTarget) RetryFeature(featureID string) (serverruntime.RetryFeatureResponse, error) {
	if err := t.orch.RetryFeature(featureID); err != nil {
		return serverruntime.RetryFeatureResponse{FeatureID: featureID, Result: resultFailed}, err
	}
	return serverruntime.RetryFeatureResponse{FeatureID: featureID, Result: resultRetried}, nil
}

func (t *mutationTarget) CompletionPreflight(featureID string) (serverruntime.CompletionPreflightResponse, error) {
	result, err := t.orch.CompletionPreflight(featureID)
	if err != nil {
		return serverruntime.CompletionPreflightResponse{FeatureID: featureID}, err
	}
	resp := serverruntime.CompletionPreflightResponse{
		APIVersion:      serverruntime.APIVersion,
		FeatureID:       result.FeatureID,
		SourceRevision:  result.SourceRevision,
		CanMarkDone:     result.CanMarkDone,
		MarkDoneBlocker: result.MarkDoneBlocker,
	}
	for _, r := range result.Repos {
		resp.Repos = append(resp.Repos, serverruntime.CompletionPreflightRepo{
			Repo:                  r.Repo,
			Publishable:           r.Publishable,
			Touched:               r.Touched,
			Status:                r.Status,
			PrURL:                 r.PRURL,
			Blocker:               r.Blocker,
			Freshness:             r.Freshness,
			Error:                 serverruntime.WireRepoError(r.Error),
			BaseBranch:            r.BaseBranch,
			Branch:                r.Branch,
			PendingCommits:        r.PendingCommits,
			PendingDirty:          r.PendingDirty,
			PushMode:              r.PushMode,
			PendingDirtyFiles:     r.PendingDirtyFiles,
			PendingDirtyFileTotal: r.PendingDirtyFileTotal,
		})
	}
	return resp, nil
}

func (t *mutationTarget) RepositoryDiff(featureID, repoName, filePath string) (serverruntime.RepositoryDiffResponse, error) {
	result, err := t.orch.RepositoryDiff(featureID, repoName, filePath)
	if err != nil {
		return serverruntime.RepositoryDiffResponse{FeatureID: featureID, Repo: repoName}, err
	}
	resp := serverruntime.RepositoryDiffResponse{
		APIVersion:      serverruntime.APIVersion,
		FeatureID:       result.FeatureID,
		Repo:            result.Repo,
		SourceRevision:  result.SourceRevision,
		Truncated:       result.Truncated,
		FileDiff:        result.FileDiff,
		FileTruncated:   result.FileTruncated,
		FileBinary:      result.FileBinary,
		FileUnavailable: result.FileUnavailable,
		Error:           wireRepositoryDiffFailure(result.Repo, result.PartialFailure),
	}
	for _, f := range result.Files {
		resp.Files = append(resp.Files, serverruntime.RepositoryDiffFile{
			Path:         f.Path,
			OldPath:      f.OldPath,
			Operation:    f.Operation,
			AddedLines:   f.AddedLines,
			RemovedLines: f.RemovedLines,
			Binary:       f.Binary,
			Fingerprint:  f.Fingerprint,
		})
	}
	return resp, nil
}

func (t *mutationTarget) RefactorFeature(featureID string, req serverruntime.RefactorFeatureRequest) (serverruntime.RefactorFeatureResponse, error) {
	resp := serverruntime.RefactorFeatureResponse{ParentID: featureID, Result: resultFailed}
	spec, err := serverruntime.RefactorChildSpecFromRequest(req)
	if err != nil {
		return resp, err
	}
	launch, err := t.orch.LaunchChild(featureID, orchestrator.ChildLaunch{Kind: feature.ChildKindRefactor, Refactor: spec})
	if err != nil {
		return resp, err
	}
	resp.FeatureID = launch.Child.ID
	resp.Result = resultCreated
	return resp, nil
}

func (t *mutationTarget) ReviewFeedbackFeature(featureID string, req serverruntime.ReviewFeedbackFeatureRequest) (serverruntime.ReviewFeedbackFeatureResponse, error) {
	resp := serverruntime.ReviewFeedbackFeatureResponse{ParentID: featureID, Result: resultFailed}
	launch, err := t.orch.LaunchChild(featureID, orchestrator.ChildLaunch{
		Kind:             feature.ChildKindReviewFeedback,
		ExpectedRevision: int64(req.ExpectedRevision),
		Gate:             serverruntime.ReviewFeedbackGateFromRequest(req),
	})
	if err != nil {
		return resp, err
	}
	resp.FeatureID = launch.Child.ID
	resp.ChildID = launch.Child.ID
	resp.Changed = launch.Changed
	resp.Omitted = launch.Omitted
	resp.Deferred = launch.Deferred
	resp.Result = resultCreated
	return resp, nil
}

func (t *mutationTarget) RebaseFeature(featureID string, _ serverruntime.RebaseFeatureRequest) (serverruntime.RebaseFeatureResponse, error) {
	resp := serverruntime.RebaseFeatureResponse{ParentID: featureID, Result: resultFailed}
	launch, err := t.orch.LaunchChild(featureID, orchestrator.ChildLaunch{Kind: feature.ChildKindRebase})
	if err != nil {
		return resp, err
	}
	resp.FeatureID = launch.Child.ID
	resp.Result = resultCreated
	return resp, nil
}

func (t *mutationTarget) MarkDone(featureID string, req serverruntime.GuardedFeatureActionRequest) (serverruntime.MarkDoneResponse, error) {
	if err := t.rejectStaleCompletionPreflight(featureID, req.SourceRevision); err != nil {
		return serverruntime.MarkDoneResponse{FeatureID: featureID, Result: resultFailed}, err
	}
	if err := t.orch.MarkDone(featureID); err != nil {
		return serverruntime.MarkDoneResponse{FeatureID: featureID, Result: resultFailed}, err
	}
	return serverruntime.MarkDoneResponse{FeatureID: featureID, Result: "done"}, nil
}

func (t *mutationTarget) CleanupFeature(featureID string, req serverruntime.CleanupActionRequest) (serverruntime.CleanupFeatureResponse, error) {
	target := strings.ToLower(strings.TrimSpace(req.Target))
	if target == "" {
		target = cleanupTargetWorktrees
	}
	resp := serverruntime.CleanupFeatureResponse{FeatureID: featureID, Target: target}
	if err := t.rejectStaleCompletionPreflight(featureID, req.SourceRevision); err != nil {
		resp.Result = resultFailed
		return resp, err
	}
	switch target {
	case cleanupTargetWorktrees:
		if err := t.orch.CleanWorktree(featureID); err != nil {
			resp.Result = resultFailed
			return resp, err
		}
	default:
		resp.Result = resultFailed
		return resp, fmt.Errorf("unknown cleanup target %q", req.Target)
	}
	resp.Result = resultCleaned
	return resp, nil
}

func (t *mutationTarget) DeleteFeature(featureID string, req serverruntime.GuardedFeatureActionRequest) (serverruntime.DeleteFeatureResponse, error) {
	if err := t.rejectStaleCompletionPreflight(featureID, req.SourceRevision); err != nil {
		return serverruntime.DeleteFeatureResponse{FeatureID: featureID}, err
	}
	result, err := t.orch.Delete(featureID)
	if err != nil {
		return serverruntime.DeleteFeatureResponse{FeatureID: featureID}, err
	}
	return serverruntime.DeleteFeatureResponse{
		FeatureID:   result.ParentID,
		OperationID: result.OperationID,
		Status:      result.Status,
		Diagnostics: result.Diagnostics,
	}, nil
}

func (t *mutationTarget) DiscardChild(featureID string) (serverruntime.DiscardChildResponse, error) {
	if err := t.orch.DiscardChild(featureID); err != nil {
		return serverruntime.DiscardChildResponse{FeatureID: featureID, Result: resultFailed}, err
	}
	return serverruntime.DiscardChildResponse{FeatureID: featureID, Result: "discarded"}, nil
}

func (t *mutationTarget) rejectStaleCompletionPreflight(featureID, sourceRevision string) error {
	if sourceRevision == "" {
		return nil
	}
	current, err := t.orch.CompletionPreflightSourceRevision(featureID)
	if err != nil {
		return err
	}
	if current == sourceRevision {
		return nil
	}
	return &serverruntime.ActionConflictError{
		Err:    orchestrator.ErrStalePreflight,
		Detail: fmt.Sprintf("stale completion preflight: source revision %q is not current (%q)", sourceRevision, current),
	}
}

func actionConflictError(err error) error {
	if err == nil {
		return nil
	}
	// The envelope's code and context derive from the same classification
	// that stores the repository's record, so the HTTP rejection and the
	// stored record agree.
	if record, ok := orchestrator.PublishConflictRecord(err); ok {
		options := errcat.RecordOptions(record)
		options = append(options, errcat.WithDiagnostics(err.Error()))
		return &serverruntime.ActionConflictError{
			Err:     err,
			Code:    record.Code,
			Options: options,
		}
	}
	return nil
}

func (t *mutationTarget) findPendingControlRequest(sessionID, requestID string, wantAskUser bool) (ports.SessionView, *llm.ControlRequestMessage, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, nil, errors.New("request_id is required")
	}
	if t.sessions == nil {
		return nil, nil, errors.New("session manager is not available")
	}
	var candidates []ports.SessionView
	if strings.TrimSpace(sessionID) != "" {
		sess := t.sessions.GetSession(strings.TrimSpace(sessionID))
		if sess == nil {
			return nil, nil, fmt.Errorf("session %s not found", sessionID)
		}
		candidates = []ports.SessionView{sess}
	} else {
		candidates = t.sessions.ActiveSessions()
	}
	for _, sess := range candidates {
		if sess == nil {
			continue
		}
		for _, pending := range sess.PendingControlRequests() {
			if pending == nil || pending.RequestID != requestID {
				continue
			}
			isAskUser := pending.Request.ToolName == toolNameAskUserQuestion
			if isAskUser != wantAskUser {
				return nil, nil, fmt.Errorf("request %s has incompatible control type", requestID)
			}
			return sess, pending, nil
		}
	}
	return nil, nil, fmt.Errorf("pending request %s not found", requestID)
}

func (t *mutationTarget) sendQueuedFeatureHelp(req serverruntime.HelpAnswerRequest) (serverruntime.HelpSendResponse, bool, error) {
	featureID := strings.TrimSpace(req.FeatureID)
	if featureID == "" || strings.TrimSpace(req.SessionID) != "" {
		return serverruntime.HelpSendResponse{}, false, nil
	}
	found, err := t.orch.AnswerQueuedHelp(featureID, strings.TrimSpace(req.Message))
	if err != nil {
		return serverruntime.HelpSendResponse{}, true, fmt.Errorf("answer feature help queue: %w", err)
	}
	if !found {
		return serverruntime.HelpSendResponse{}, false, nil
	}
	return serverruntime.HelpSendResponse{FeatureID: featureID, Result: resultSent}, true, nil
}

func (t *mutationTarget) helpSession(req serverruntime.HelpAnswerRequest) (ports.SessionView, error) {
	if t.sessions == nil {
		return nil, errors.New("session manager is not available")
	}
	if id := strings.TrimSpace(req.SessionID); id != "" {
		sess := t.sessions.GetSession(id)
		if sess == nil {
			return nil, fmt.Errorf("session %s not found", id)
		}
		return sess, nil
	}
	featureID := strings.TrimSpace(req.FeatureID)
	if featureID == "" {
		return nil, errors.New("session_id or feature_id is required")
	}
	var active []ports.SessionView
	for _, sess := range t.sessions.FeatureSessions(featureID) {
		if sess != nil && sess.IsActive() {
			active = append(active, sess)
		}
	}
	switch len(active) {
	case 0:
		return nil, fmt.Errorf("no active session for feature %s", featureID)
	case 1:
		return active[0], nil
	default:
		return nil, fmt.Errorf("multiple active sessions for feature %s; session_id is required", featureID)
	}
}

func applyNeedUserInputDraftAnswers(rec *agent.NeedUserInputRecord, answers map[string]string) error {
	if rec == nil {
		return errors.New("nil need-user-input record")
	}
	questionByKey := make(map[string]*agent.NeedUserInputQuestion)
	for i := range rec.Questions {
		q := &rec.Questions[i]
		if q.Index > 0 {
			questionByKey[strconv.Itoa(q.Index)] = q
			questionByKey[fmt.Sprintf("q%d", q.Index)] = q
		} else {
			ordinal := i + 1
			questionByKey[strconv.Itoa(ordinal)] = q
			questionByKey[fmt.Sprintf("q%d", ordinal)] = q
		}
		if prompt := strings.TrimSpace(q.Prompt); prompt != "" {
			questionByKey[prompt] = q
		}
	}
	for key, answer := range answers {
		q := questionByKey[strings.TrimSpace(key)]
		if q == nil {
			return fmt.Errorf("answer key %q does not match a need-user-input question", key)
		}
		q.Answer = answer
	}
	return nil
}

func mergeRuntimeDefaultsMutation(dst *config.DefaultsConfig, patch serverruntime.RuntimeDefaultsMutation) bool {
	if dst == nil {
		return false
	}
	changed := false
	if patch.Models != nil {
		next := serverruntime.ApplyModelConfigPatch(dst.Models, *patch.Models)
		if next != dst.Models {
			dst.Models = next
			changed = true
		}
	}
	if hasAnyEffortConfig(patch.Effort) {
		next := config.OverlayEffortConfig(dst.Effort, patch.Effort)
		if next != dst.Effort {
			dst.Effort = next
			changed = true
		}
	}
	if patch.ExitCriteria != "" && setIfChanged(&dst.ExitCriteria, patch.ExitCriteria) {
		changed = true
	}
	if patch.Inquireness != "" && setIfChanged(&dst.Inquireness, patch.Inquireness) {
		changed = true
	}
	if patch.Pipeline != "" && setIfChanged(&dst.Pipeline, patch.Pipeline) {
		changed = true
	}
	if patch.MaxIterations > 0 && setIfChanged(&dst.MaxIterations, patch.MaxIterations) {
		changed = true
	}
	if patch.MaxConsecutiveFailures > 0 && setIfChanged(&dst.MaxConsecutiveFailures, patch.MaxConsecutiveFailures) {
		changed = true
	}
	if patch.MaxConsecutiveNoProgress > 0 && setIfChanged(&dst.MaxConsecutiveNoProgress, patch.MaxConsecutiveNoProgress) {
		changed = true
	}
	if patch.MaxPhasePlanIterations > 0 && setIfChanged(&dst.MaxPhasePlanIterations, patch.MaxPhasePlanIterations) {
		changed = true
	}
	if patch.Checkpoints != nil && setCheckpointsIfChanged(&dst.Checkpoints, *patch.Checkpoints) {
		changed = true
	}
	if len(patch.PipelinePreferences) > 0 {
		dst.PipelinePreferences = patch.PipelinePreferences
		changed = true
	}
	if patch.DangerouslySkipPermissions != nil && setIfChanged(&dst.DangerouslySkipPermissions, *patch.DangerouslySkipPermissions) {
		changed = true
	}
	if patch.AutomaticReviewEnabled != nil && setIfChanged(&dst.AutomaticReviewEnabled, *patch.AutomaticReviewEnabled) {
		changed = true
	}
	return changed
}

func setCheckpointsIfChanged(dst *config.Checkpoints, val config.Checkpoints) bool {
	if dst.InquiryReview == val.InquiryReview &&
		dst.ResearchReview == val.ResearchReview &&
		dst.DesignReview == val.DesignReview &&
		dst.RoadmapReview == val.RoadmapReview &&
		dst.PhasePlanReview == val.PhasePlanReview &&
		dst.ManualPublish == val.ManualPublish &&
		dst.DraftPublish == val.DraftPublish {
		return false
	}
	*dst = val
	return true
}

// setIfChanged assigns val to *dst and reports true if that changed dst's value.
func setIfChanged[T comparable](dst *T, val T) bool {
	if val == *dst {
		return false
	}
	*dst = val
	return true
}

func effectiveCreatePipeline(requested feature.PipelineProfile, cfg *config.Config) feature.PipelineProfile {
	if requested.IsValid() {
		return requested
	}
	if cfg != nil && cfg.Defaults.Pipeline != "" {
		if parsed, err := feature.ParsePipelineProfile(cfg.Defaults.Pipeline); err == nil {
			return parsed
		}
	}
	return feature.PipelineMoonshot
}

func featureRepoNames(f *feature.Feature) []string {
	if f == nil {
		return nil
	}
	repos := make([]string, 0, len(f.Repos))
	for _, repo := range f.Repos {
		repos = append(repos, repo.Name)
	}
	return repos
}

func (t *mutationTarget) persistPipelinePreferences(repos []string, pipeline feature.PipelineProfile, models config.ModelConfig, effort config.EffortConfig, inquireness feature.Inquireness, checkpoints feature.Checkpoints, publishable bool) error {
	if t.configPath == "" {
		return errors.New("config path is not available")
	}
	if !pipeline.IsValid() {
		return fmt.Errorf("invalid pipeline profile %q", pipeline)
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	cfg := t.cfg
	if cfg == nil {
		cfg = config.NewDefault()
	}
	if cfg.Defaults.PipelinePreferences == nil {
		cfg.Defaults.PipelinePreferences = make(map[string]config.PipelinePreference)
	}
	if cfg.Repos == nil {
		cfg.Repos = make(map[string]config.RepoConfig)
	}
	projection := pipeline.ProjectGates(checkpoints, publishable)
	profileKey := string(pipeline)
	cfg.Defaults.PipelinePreferences[profileKey] = config.PipelinePreference{
		Models:      models,
		Effort:      effort,
		Inquireness: string(inquireness),
	}
	configGates := feature.FeatureCheckpointsToConfig(projection.Checkpoints)
	for _, repoName := range repos {
		rc := cfg.Repos[repoName]
		if rc.PipelineGates == nil {
			rc.PipelineGates = make(map[string]config.Checkpoints)
		}
		rc.PipelineGates[profileKey] = configGates
		cfg.Repos[repoName] = rc
	}
	if err := config.Save(t.configPath, cfg); err != nil {
		return err
	}
	t.cfg = cfg
	return nil
}

func parseServerPhase(in string) feature.Phase {
	phase, err := feature.ParsePhaseName(in)
	if err != nil {
		return feature.Phase(0)
	}
	return phase
}

func hasAnyEffortConfig(e config.EffortConfig) bool {
	return e.Inquiry != "" ||
		e.Research != "" ||
		e.Planning != "" ||
		e.Implementation != "" ||
		e.Review != "" ||
		e.Utilities != "" ||
		e.KBBuild != ""
}

func mergeModelConfig(base, overlay config.ModelConfig) config.ModelConfig {
	return serverruntime.ApplyModelConfigPatch(base, serverruntime.ModelConfigToPatch(overlay))
}
