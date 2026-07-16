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

package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const maxFeatureDetailHistoricalRuns = 5

// Tool names as reported by the LLM control protocol; shared across
// read_model.go, session_model.go, sse.go and their tests.
const (
	toolNameAskUserQuestion = "AskUserQuestion"
	toolNameBash            = "Bash"
	toolNameWrite           = "Write"
	toolNameEdit            = "Edit"
	toolNameMultiEdit       = "MultiEdit"
)

// agentQuestionPrompt is the synthetic question text shown when an
// AskUserQuestion control request has no readable question of its own.
const agentQuestionPrompt = "Agent has a question"

// actionInputKindEnum and actionInputKindString are ActionInputDTO.Kind
// values: the former for inputs whose value must be one of
// ActionInputDTO.Options, the latter for free-form string inputs.
const (
	actionInputKindEnum   = "enum"
	actionInputKindString = "string"
)

// disabledCycleActive is the ActionDisabledReasonDTO.Code used when a
// post-publish action is blocked by another active feature cycle.
const disabledCycleActive = "cycle_active"

// disabledNotLocalOnly is the ActionDisabledReasonDTO.Code used when merge
// is requested on a feature that is not local-only.
const disabledNotLocalOnly = "not_local_only"

// disabledStatusNotAllowed is the ActionDisabledReasonDTO.Code used when an
// action is blocked solely by the feature's current status.
const disabledStatusNotAllowed = "status_not_allowed"

// controlRequestStatusPending is the ControlRequestDTO/TranscriptMessageDTO
// Status value for a control request awaiting a user decision.
const controlRequestStatusPending = "pending"

// actionCleanup, actionDelete, actionMarkDone, actionMerge, actionPauseStop
// and the entries below are feature action IDs shared between the action
// catalog, the mutation dispatcher and the client request builder.
const (
	actionCleanup        = "cleanup"
	actionDelete         = "delete"
	actionMarkDone       = "mark-done"
	actionMerge          = "merge"
	actionNeedUserInput  = "need-user-input"
	actionNeedInputDraft = "need-user-input-draft"
	actionPauseStop      = "pause-stop"
	actionPublish        = "publish"
	actionRebase         = "rebase"
	actionRefactor       = "refactor"
	actionRestart        = "restart"
	actionResume         = "resume"
	actionStart          = "start"
	actionRetry          = "retry"
	actionReviewComments = "review-comments"
	actionReviewDecision = "review-decision"
	actionRewind         = "rewind"
)

func revisionForAny(v any) string {
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:12])
}

func (h *apiHandler) featureDetailDTO(f *feature.Feature) FeatureDetailDTO {
	active := runSummaryDTO(f.Run(), f)
	historyRunNumbers := boundedHistoricalRunNumbers(f.ActiveRun, f.RunCount, maxFeatureDetailHistoricalRuns)
	history := make([]RunSummaryDTO, 0, len(historyRunNumbers))
	if h.store != nil {
		for _, n := range historyRunNumbers {
			run, err := h.store.LoadRun(f.ID, n)
			if err == nil {
				history = append(history, runSummaryDTO(run, f))
			}
		}
	}
	detail := featureDetailFromSummary(summarizeFeature(f))
	detail.Description = safeDisplayText(f.Description, 500)
	detail.Summary = safeDisplayText(f.Summary, 500)
	detail.Pipeline = string(f.Pipeline)
	detail.Models = f.Models
	detail.ActiveRunDetail = &active
	detail.HistoricalRuns = history
	detail.RepoStatus = h.repoStatusDTOs(f)
	detail.Timing = timingDTO(f)
	detail.Cost = costDTO(f)
	detail.ReviewGate = ReviewGateDTO{
		ReviewingGate:     f.ReviewingGate,
		ReviewFixing:      f.ReviewFixing,
		ValidatingPlan:    f.ValidatingPlan,
		ValidatorStatuses: copyStringMap(f.ValidatorStatuses),
	}
	for _, item := range f.VerificationItems {
		detail.VerificationItems = append(detail.VerificationItems, VerificationItem{Name: item.Name, State: item.State})
	}
	detail.Actions = actionCatalogDTOs(f)
	detail.Cycle = activeCycleDTO(f)
	if f.HasTerminalFailure() {
		detail.Failure = &FailureDTO{
			Type:    f.FailureType,
			Message: safeDisplayText(f.LastError, 240),
		}
	}
	if f.PendingNeedUserInputPath != "" {
		gate := needUserInputGateDTO(f.ID, entityFeature, "", "", f.CurrentIteration, f.InputNotifications, f.PendingNeedUserInputPath)
		detail.NeedUserInput = &gate
	}
	return detail
}

func featureDetailFromSummary(summary FeatureSummary) FeatureDetailDTO {
	return FeatureDetailDTO{
		ID:           summary.ID,
		Name:         summary.Name,
		Slug:         summary.Slug,
		Status:       summary.Status,
		CurrentPhase: summary.CurrentPhase,
		Cycle:        summary.Cycle,
		ActiveRun:    summary.ActiveRun,
		RunCount:     summary.RunCount,
		Repos:        summary.Repos,
		CreatedAt:    summary.CreatedAt,
		Checkpoints:  summary.Checkpoints,
		Progress:     summary.Progress,
		Warnings:     summary.Warnings,
	}
}

func activeCycleDTO(f *feature.Feature) *CycleDTO {
	if f == nil {
		return nil
	}
	if f.ActiveCycle != nil {
		return &CycleDTO{
			Type:      string(f.ActiveCycle.Type),
			Status:    f.ActiveCycle.Status,
			Count:     f.ActiveCycle.Count,
			Iteration: f.ActiveCycle.Iteration,
		}
	}
	for _, repo := range f.Repos {
		if dto := activeRepoCycleDTO(f, f.RepoCycles[repo.Name]); dto != nil {
			return dto
		}
	}
	if len(f.RepoCycles) == 0 {
		return nil
	}
	names := make([]string, 0, len(f.RepoCycles))
	for repoName := range f.RepoCycles {
		names = append(names, repoName)
	}
	sort.Strings(names)
	for _, repoName := range names {
		if dto := activeRepoCycleDTO(f, f.RepoCycles[repoName]); dto != nil {
			return dto
		}
	}
	return nil
}

func activeRepoCycleDTO(f *feature.Feature, cycle *feature.RepoCycleState) *CycleDTO {
	if cycle == nil || !isActiveRepoCycleStatus(cycle.Status) {
		return nil
	}
	return &CycleDTO{
		Type:      string(cycle.Type),
		Status:    cycle.Status,
		Count:     activeCycleCount(f, cycle),
		Iteration: cycle.Iteration,
	}
}

func isActiveRepoCycleStatus(status string) bool {
	switch status {
	case feature.RepoCycleRunning, feature.RepoCycleReviewing, feature.RepoCycleNeedUserInput:
		return true
	default:
		return false
	}
}

func activeCycleCount(f *feature.Feature, cycle *feature.RepoCycleState) int {
	if f == nil || cycle == nil {
		return 0
	}
	count := cycle.Count
	switch cycle.Type {
	case feature.CycleRebase:
		if f.RebaseCount() > count {
			count = f.RebaseCount()
		}
	case feature.CycleRefactor:
		if f.RefactorCount() > count {
			count = f.RefactorCount()
		}
	case feature.CycleReviewComments:
		if f.ReviewCommentsCount() > count {
			count = f.ReviewCommentsCount()
		}
	}
	if count <= 0 && cycle.Type != "" {
		return 1
	}
	return count
}

func actionCatalogDTOs(f *feature.Feature) []ActionDTO {
	if f == nil {
		return nil
	}
	status := f.Status
	running := status.IsRunning()
	activeCycle := f.HasActiveRepoCycles() || f.ActiveCycleType() != ""
	publishedOrManualReady := status == feature.StatusPublished ||
		(status == feature.StatusCodeReady && !f.Checkpoints.AutoPublish())

	action := func(id string, enabled bool, scope ActionScopeDTO, inputs []ActionInputDTO, disabled ...ActionDisabledReasonDTO) ActionDTO {
		if inputs == nil {
			inputs = []ActionInputDTO{}
		}
		dto := ActionDTO{
			ID:             id,
			Enabled:        enabled,
			Scope:          scope,
			RequiredInputs: inputs,
		}
		if !enabled {
			dto.DisabledReasons = disabled
			if len(dto.DisabledReasons) == 0 {
				dto.DisabledReasons = []ActionDisabledReasonDTO{disabledStatusReason(status)}
			}
		}
		return dto
	}
	featureScope := ActionScopeDTO{Type: entityFeature}
	repoOptional := ActionScopeDTO{Type: entityFeature, RepoSelection: "optional"}
	repoRequired := ActionScopeDTO{Type: entityFeature, RepoSelection: "required"}

	canStart := !running && !activeCycle && (status == feature.StatusCreated ||
		status == feature.StatusInquireReady ||
		status == feature.StatusPlanReady ||
		status == feature.StatusDesignReady ||
		status == feature.StatusImplementReady ||
		status == feature.StatusReviewPassed)
	canStop := running || activeCycle
	canResume := status == feature.StatusInterrupted ||
		status == feature.StatusNeedUserInput ||
		f.PendingNeedUserInputPath != "" ||
		len(f.PendingUserInputCycles()) > 0
	canRestart := !running
	canPublish := f.IsPublishable() && status == feature.StatusCodeReady && f.Checkpoints.AutoPublish()
	canMerge := !f.IsPublishable() && (status == feature.StatusCodeReady || status == feature.StatusPublished)
	canRewind := !running && (len(feature.RewindChoicesForFeature(f)) > 0 || hasRewindUpgradeTarget(f))
	canPostPublishCycle := publishedOrManualReady && !activeCycle
	canRefactor := publishedOrManualReady && !activeCycle
	canRetry := status == feature.StatusFailed
	canMarkDone := publishedOrManualReady
	canCleanup := !running
	canDelete := !running

	return []ActionDTO{
		action(actionStart, canStart, featureScope, nil, disabledStatusReason(status)),
		action(actionPauseStop, canStop, featureScope, nil, ActionDisabledReasonDTO{Code: "not_running", Message: "feature has no active work to pause or stop"}),
		action(actionResume, canResume, featureScope, nil, ActionDisabledReasonDTO{Code: "not_paused", Message: "feature has no paused session or input gate"}),
		action(actionRestart, canRestart, featureScope, nil, ActionDisabledReasonDTO{Code: feature.RepoCycleRunning, Message: "feature must stop before restart"}),
		action(actionPublish, canPublish, featureScope, nil, publishDisabledReason(f)),
		action(actionMerge, canMerge, featureScope, nil, mergeDisabledReason(f)),
		action(actionRewind, canRewind, featureScope, []ActionInputDTO{
			{Name: "target_phase", Kind: actionInputKindEnum, Required: true, Options: rewindPhaseOptions(f)},
			{Name: "roadmap_phase", Kind: "integer", Required: false},
			{Name: "upgrade_pipeline", Kind: actionInputKindEnum, Required: false, Options: rewindUpgradePipelineOptions(f)},
		}, ActionDisabledReasonDTO{Code: "no_rewind_targets", Message: "feature has no valid rewind targets"}),
		action(actionRebase, canPostPublishCycle, featureScope, nil, postPublishCycleDisabledReason(f, actionRebase)),
		action(actionReviewComments, canPostPublishCycle && f.IsPublishable(), repoRequired, []ActionInputDTO{
			{Name: "repo", Kind: actionInputKindString, Required: true},
			{Name: "mode", Kind: actionInputKindEnum, Required: true, Options: []string{reviewCommentsModeAuto, "address_all"}},
		}, postPublishCycleDisabledReason(f, actionReviewComments)),
		action(actionRefactor, canRefactor, repoOptional, []ActionInputDTO{
			{Name: "repo", Kind: actionInputKindString, Required: false},
			{Name: "prompt", Kind: actionInputKindString, Required: true, MaxLength: MaxActionTextBytes},
			{Name: "pipeline", Kind: actionInputKindEnum, Required: false, Options: []string{string(feature.PipelineMedium), string(feature.PipelineLarge), string(feature.PipelineMoonshot)}},
		}, postPublishCycleDisabledReason(f, actionRefactor)),
		action(actionRetry, canRetry, featureScope, nil, ActionDisabledReasonDTO{Code: "not_failed", Message: "retry is only available for failed features"}),
		action(actionMarkDone, canMarkDone, featureScope, nil, ActionDisabledReasonDTO{Code: "not_complete", Message: "feature is not ready to mark done"}),
		action(actionCleanup, canCleanup, featureScope, []ActionInputDTO{
			{Name: "target", Kind: actionInputKindEnum, Required: false, Options: []string{"worktrees", "cycles"}},
		}, ActionDisabledReasonDTO{Code: feature.RepoCycleRunning, Message: "cleanup is disabled while work is running"}),
		action(actionDelete, canDelete, featureScope, nil, ActionDisabledReasonDTO{Code: feature.RepoCycleRunning, Message: "delete is disabled while work is running"}),
	}
}

func disabledStatusReason(status feature.Status) ActionDisabledReasonDTO {
	return ActionDisabledReasonDTO{
		Code:    disabledStatusNotAllowed,
		Message: "action is not available while feature status is " + status.String(),
	}
}

func publishDisabledReason(f *feature.Feature) ActionDisabledReasonDTO {
	if f == nil {
		return disabledStatusReason(feature.StatusCreated)
	}
	if !f.IsPublishable() {
		return ActionDisabledReasonDTO{Code: "local_only", Message: "feature has at least one local-only repo"}
	}
	if f.Status == feature.StatusPublished || f.Status == feature.StatusDone {
		return ActionDisabledReasonDTO{Code: "already_published", Message: "feature already has a published terminal state"}
	}
	if f.Checkpoints.ManualPublish && f.Status == feature.StatusCodeReady {
		return ActionDisabledReasonDTO{Code: "manual_publish_required", Message: "feature is waiting for manual publish confirmation"}
	}
	return disabledStatusReason(f.Status)
}

func mergeDisabledReason(f *feature.Feature) ActionDisabledReasonDTO {
	if f == nil {
		return disabledStatusReason(feature.StatusCreated)
	}
	if f.IsPublishable() {
		return ActionDisabledReasonDTO{Code: disabledNotLocalOnly, Message: "merge is only available for local-only features"}
	}
	return disabledStatusReason(f.Status)
}

func postPublishCycleDisabledReason(f *feature.Feature, cycle string) ActionDisabledReasonDTO {
	if f == nil {
		return disabledStatusReason(feature.StatusCreated)
	}
	if f.HasActiveRepoCycles() || f.ActiveCycleType() != "" {
		return ActionDisabledReasonDTO{Code: disabledCycleActive, Message: "another feature cycle is active"}
	}
	if cycle == actionReviewComments && !f.IsPublishable() {
		return ActionDisabledReasonDTO{Code: "not_publishable", Message: "review-comment actions require a published PR"}
	}
	return disabledStatusReason(f.Status)
}

func rewindPhaseOptions(f *feature.Feature) []string {
	choices := feature.RewindChoicesForFeature(f)
	out := make([]string, 0, len(choices))
	for _, choice := range choices {
		out = append(out, choice.Phase.DirName())
	}
	return out
}

func rewindUpgradePipelineOptions(f *feature.Feature) []string {
	switch f.EffectivePipeline() {
	case feature.PipelineMedium:
		return []string{string(feature.PipelineLarge), string(feature.PipelineMoonshot)}
	case feature.PipelineLarge:
		return []string{string(feature.PipelineMoonshot)}
	default:
		return nil
	}
}

func hasRewindUpgradeTarget(f *feature.Feature) bool {
	if f == nil {
		return false
	}
	current := f.EffectivePipeline()
	for _, option := range rewindUpgradePipelineOptions(f) {
		upgraded := *f
		upgraded.Pipeline = feature.PipelineProfile(option)
		if upgraded.PipelineUpgradedFrom == "" {
			upgraded.PipelineUpgradedFrom = current
		}
		for _, choice := range feature.RewindChoicesForFeature(&upgraded) {
			if choice.Phase == feature.PhaseInquire {
				return true
			}
		}
	}
	return false
}

func boundedHistoricalRunNumbers(activeRun, runCount, limit int) []int {
	if runCount <= 0 || limit <= 0 {
		return nil
	}
	runNumbers := make([]int, 0, min(limit, max(runCount-1, 0)))
	for n := runCount; n >= 1 && len(runNumbers) < limit; n-- {
		if n == activeRun {
			continue
		}
		runNumbers = append(runNumbers, n)
	}
	sort.Ints(runNumbers)
	return runNumbers
}

func runSummaryDTO(run *feature.Run, f *feature.Feature) RunSummaryDTO {
	if run == nil {
		return RunSummaryDTO{}
	}
	dto := RunSummaryDTO{
		RunNumber:       run.RunNumber,
		StartedAt:       run.StartedAt,
		SealedAt:        run.SealedAt,
		SealReason:      string(run.SealReason),
		CurrentPhase:    f.CurrentPhase.String(),
		PhaseStatus:     run.CurrentPhaseStatus,
		Iteration:       run.CurrentIteration,
		RoadmapPhase:    run.CurrentRoadmapPhase,
		RoadmapTotal:    run.TotalRoadmapPhases,
		ArtifactCount:   len(run.Artifacts),
		HasNeedUserGate: run.PendingNeedUserInputPath != "",
		Setup:           setupDTO(run.Setup),
	}
	if run.PendingReviewPhase != nil {
		dto.PendingReviewPhase = run.PendingReviewPhase.DirName()
	}
	if run.PendingRewindReviewRoadmapPhase != nil {
		dto.PendingRewindReviewRoadmapPhase = *run.PendingRewindReviewRoadmapPhase
	}
	dto.IsRewind = run.IsRewind
	return dto
}

func setupDTO(setup *feature.SetupState) *SetupDTO {
	if setup == nil {
		return nil
	}
	tasks := make(map[string]SetupTaskDTO, len(setup.Tasks))
	for key, task := range setup.Tasks {
		tasks[key] = setupTaskDTO(task)
	}
	return &SetupDTO{
		Status:        string(setup.Status),
		Attempt:       setup.Attempt,
		StartedAt:     setup.StartedAt,
		CompletedAt:   setup.CompletedAt,
		LatestLogPath: safeDisplayText(setup.LatestLogPath, 1000),
		Tasks:         tasks,
		TaskOrder:     append([]string(nil), setup.TaskOrder...),
		LastError:     safeDisplayText(setup.LastError, 500),
	}
}

func setupTaskDTO(task feature.SetupTask) SetupTaskDTO {
	return SetupTaskDTO{
		Key:              task.Key,
		Kind:             string(task.Kind),
		Label:            safeDisplayText(task.Label, 200),
		Repo:             safeDisplayText(task.Repo, 200),
		Status:           string(task.Status),
		Path:             safeDisplayText(task.Path, 1000),
		SourcePath:       safeDisplayText(task.SourcePath, 1000),
		Branch:           safeDisplayText(task.Branch, 500),
		StartPoint:       safeDisplayText(task.StartPoint, 500),
		UseCurrentBranch: task.UseCurrentBranch,
		Attempt:          task.Attempt,
		StartedAt:        task.StartedAt,
		EndedAt:          task.EndedAt,
		LastError:        safeDisplayText(task.LastError, 500),
	}
}

func (h *apiHandler) repoStatusDTOs(f *feature.Feature) []RepoStatusDTO {
	if f == nil {
		return nil
	}
	out := make([]RepoStatusDTO, 0, len(f.Repos))
	for _, repo := range f.Repos {
		state := f.RepoStates[repo.Name]
		cycle := f.RepoCycles[repo.Name]
		dto := RepoStatusDTO{
			Name:        repo.Name,
			Publishable: repo.Publishable == nil || *repo.Publishable,
		}
		if state != nil {
			dto.Touched = state.Touched
			dto.PRURL = state.PRURL
			dto.LastError = safeDisplayText(state.LastError, 200)
		}
		if h.freshness != nil {
			dto.Freshness = string(h.freshness.Freshness(f, repo))
		}
		if cycle != nil {
			dto.CycleType = string(cycle.Type)
			dto.CycleStatus = cycle.Status
		}
		if f.RebaseOperation != nil {
			progress := f.RebaseOperation.Repos[repo.Name]
			if progress != nil {
				dto.RebaseStatus = string(progress.Status)
				dto.RebaseTarget = safeDisplayText(progress.RebaseTarget, 128)
				dto.ConflictFiles = append([]string(nil), progress.ConflictFiles...)
				if dto.LastError == "" {
					dto.LastError = safeDisplayText(progress.LastError, 200)
				}
			}
		}
		out = append(out, dto)
	}
	return out
}

func timingDTO(f *feature.Feature) TimingDTO {
	byPhase := make(map[string]int64, len(f.PhaseTimings))
	var total int64
	for phase, dur := range f.PhaseTimings {
		seconds := int64(dur.Seconds())
		byPhase[phase] = seconds
		total += seconds
	}
	return TimingDTO{TotalSeconds: total, ByPhase: byPhase}
}

func costDTO(f *feature.Feature) CostDTO {
	byPhase := make(map[string]float64, len(f.PhaseCosts))
	var total float64
	for phase, cost := range f.PhaseCosts {
		byPhase[phase] = cost
		total += cost
	}
	return CostDTO{TotalUSD: total, ByPhase: byPhase}
}

func (h *apiHandler) handleRuntimeConfig(w http.ResponseWriter, r *http.Request) {
	cfg := h.configOrDefault()
	repos := configRepoDTOs(cfg)
	providers := providerNames(h.registry)
	resp := RuntimeConfigResponse{
		APIVersion:      APIVersion,
		Runtime:         h.runtime,
		Defaults:        cfg.Defaults.Models,
		FeatureDefaults: featureDefaultsDTO(cfg.Defaults),
		Repos:           repos,
		WorkspaceRoots:  append([]string(nil), cfg.WorkspaceRoots...),
		UI:              cfg.UI,
		Notifications: NotificationConfigDTO{
			MuteFeatureInput: cfg.Notifications.MuteFeatureInput,
		},
		Observability: ObservabilityDTO{
			Events:          cfg.Observability.Events,
			OTelEnabled:     cfg.Observability.OTelEnabled,
			OTelServiceName: cfg.Observability.OTelServiceName,
		},
		Providers: providers,
	}
	revision := revisionForAny(resp)
	resp.Meta = h.responseMeta(revision)
	h.writeRevisionedJSON(w, r, revision, resp)
}

func (h *apiHandler) handleFeatureConfig(w http.ResponseWriter, r *http.Request, featureID string) {
	f, err := h.loadFeature(featureID)
	if err != nil {
		writeStoreError(w, err, featureID)
		return
	}
	cfg := h.configOrDefault()
	defaults := FeatureConfigDTO{
		Models:             cfg.Defaults.Models,
		Inquireness:        cfg.Defaults.Inquireness,
		Checkpoints:        checkpointsDTO(featureCheckpoints(cfg.Defaults.Checkpoints)),
		Pipeline:           cfg.Defaults.Pipeline,
		InputNotifications: FeatureConfigInputNotifications(feature.InputNotificationsModeForMuted(cfg.Notifications.MuteFeatureInput)),
	}
	current := featureConfigDTO(f)
	resp := FeatureConfigResponse{
		APIVersion: APIVersion,
		FeatureID:  f.ID,
		Current:    current,
		Defaults:   defaults,
		Original:   current,
		Publish: PublishabilityDTO{
			ManualPublish: f.Checkpoints.ManualPublish,
			Repos:         publishabilityByRepo(f),
		},
	}
	revision := revisionForAny(resp)
	resp.Meta = h.responseMeta(revision)
	h.writeRevisionedJSON(w, r, revision, resp)
}

func (h *apiHandler) handleModelCatalog(w http.ResponseWriter, r *http.Request) {
	resp := ModelCatalogResponse{
		APIVersion:          APIVersion,
		ProviderModels:      map[string][]ModelDTO{},
		PhaseProviderModels: map[string]map[string][]string{},
		PhaseDefaults:       h.configOrDefault().Defaults.Models,
	}
	if h.registry != nil {
		defaults := h.registry.CatalogDefaultModels()
		if defaults != (config.ModelConfig{}) {
			resp.PhaseDefaults = defaults
		}
		for _, provider := range h.registry.DetectedProviders() {
			name := provider.Name()
			resp.ProviderOrder = append(resp.ProviderOrder, name)
			for _, model := range h.registry.ModelsForProvider(name) {
				resp.ProviderModels[name] = append(resp.ProviderModels[name], modelDTO(model))
			}
			if len(resp.ProviderModels[name]) == 0 {
				for _, id := range provider.AvailableModels() {
					resp.ProviderModels[name] = append(resp.ProviderModels[name], ModelDTO{ID: id})
				}
			}
		}
		for _, role := range []llm.PhaseRole{llm.PhaseInquiry, llm.PhaseResearch, llm.PhasePlanning, llm.PhaseImplementation, llm.PhaseReview, llm.PhaseChat, llm.PhaseKBBuild} {
			resp.PhaseProviderModels[string(role)] = h.registry.EligibleModelsForPhase(role)
		}
	}
	revision := revisionForAny(resp)
	resp.Meta = h.responseMeta(revision)
	h.writeRevisionedJSON(w, r, revision, resp)
}

func (h *apiHandler) handlePrompts(w http.ResponseWriter, r *http.Request) {
	asks, perms := h.pendingControls()
	_ = perms
	help, gates := h.featureQueues()
	resp := PromptSnapshotResponse{
		APIVersion:       APIVersion,
		AskUserQuestions: asks,
		HelpQueue:        help,
		NeedUserInputs:   gates,
	}
	revision := revisionForAny(resp)
	resp.Meta = h.responseMeta(revision)
	h.writeRevisionedJSON(w, r, revision, resp)
}

func (h *apiHandler) handlePermissions(w http.ResponseWriter, r *http.Request) {
	_, perms := h.pendingControls()
	resp := PermissionSnapshotResponse{APIVersion: APIVersion, Requests: perms}
	revision := revisionForAny(resp)
	resp.Meta = h.responseMeta(revision)
	h.writeRevisionedJSON(w, r, revision, resp)
}

func (h *apiHandler) configOrDefault() *config.Config {
	if h.cfg != nil {
		return h.cfg
	}
	return config.NewDefault()
}

func configRepoDTOs(cfg *config.Config) []ConfigRepoDTO {
	cfg = runtimeConfigRepoSnapshot(cfg)
	allRepos := config.AllRepos(cfg)
	repos := make([]ConfigRepoDTO, 0, len(allRepos))
	for name, repo := range allRepos {
		repos = append(repos, ConfigRepoDTO{Name: name, Path: repo.Path, PipelineGates: copyConfigPipelineGates(repo.PipelineGates)})
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })
	return repos
}

func runtimeConfigRepoSnapshot(cfg *config.Config) *config.Config {
	if cfg == nil {
		return config.NewDefault()
	}
	snapshot := *cfg
	snapshot.WorkspaceRoots = append([]string(nil), cfg.WorkspaceRoots...)
	snapshot.Repos = copyConfigRepoMap(cfg.Repos)
	snapshot.DiscoveredRepos = copyConfigRepoMap(cfg.DiscoveredRepos)
	if len(snapshot.WorkspaceRoots) > 0 {
		config.DiscoverReposFromRoots(&snapshot)
	}
	return &snapshot
}

func copyConfigRepoMap(in map[string]config.RepoConfig) map[string]config.RepoConfig {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]config.RepoConfig, len(in))
	for name, repo := range in {
		out[name] = repo
	}
	return out
}

func copyConfigPipelineGates(in map[string]config.Checkpoints) map[string]config.Checkpoints {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]config.Checkpoints, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func featureDefaultsDTO(defaults config.DefaultsConfig) FeatureDefaultsDTO {
	var prefs map[string]config.PipelinePreference
	if len(defaults.PipelinePreferences) > 0 {
		prefs = make(map[string]config.PipelinePreference, len(defaults.PipelinePreferences))
		for key, value := range defaults.PipelinePreferences {
			prefs[key] = value
		}
	}
	return FeatureDefaultsDTO{
		Models:              defaults.Models,
		PipelinePreferences: prefs,
		Inquireness:         defaults.Inquireness,
		Pipeline:            defaults.Pipeline,
		Checkpoints:         defaults.Checkpoints,
	}
}

func providerNames(reg *llm.Registry) []string {
	if reg == nil {
		return nil
	}
	providers := reg.DetectedProviders()
	out := make([]string, 0, len(providers))
	for _, provider := range providers {
		out = append(out, provider.Name())
	}
	return out
}

func featureCheckpoints(c config.Checkpoints) feature.Checkpoints {
	return feature.Checkpoints{
		InquiryReview:   c.InquiryReview,
		ResearchReview:  c.ResearchReview,
		DesignReview:    c.DesignReview,
		RoadmapReview:   c.RoadmapReview,
		PhasePlanReview: c.PhasePlanReview,
		ManualPublish:   c.ManualPublish,
		DraftPublish:    c.DraftPublish,
	}
}

func publishabilityByRepo(f *feature.Feature) map[string]bool {
	out := make(map[string]bool, len(f.Repos))
	for _, repo := range f.Repos {
		out[repo.Name] = repo.Publishable == nil || *repo.Publishable
	}
	return out
}

func modelDTO(model llm.ModelInfo) ModelDTO {
	return ModelDTO{
		ID:            model.ID,
		DisplayName:   model.DisplayName,
		ContextWindow: model.ContextWindow,
		Aliases:       append([]string(nil), model.Aliases...),
		Category:      model.Category,
	}
}

func (h *apiHandler) pendingControls() ([]ControlRequestDTO, []ControlRequestDTO) {
	if h.sessions == nil {
		return nil, nil
	}
	var asks []orderedControlRequest
	var perms []orderedControlRequest
	for _, sess := range h.sessions.ActiveSessions() {
		for i, req := range sess.PendingControlRequests() {
			dto := controlRequestDTO(sess, req)
			entry := orderedControlRequest{
				dto:       dto,
				startedAt: sess.StartedAt(),
				sessionID: sess.ID(),
				index:     i,
			}
			if req.Request.ToolName == toolNameAskUserQuestion {
				asks = append(asks, entry)
			} else {
				perms = append(perms, entry)
			}
		}
	}
	sortOrderedControlRequests(asks)
	sortOrderedControlRequests(perms)
	dto := func(w orderedControlRequest) ControlRequestDTO { return w.dto }
	return dtosOf(asks, dto), dtosOf(perms, dto)
}

func (h *apiHandler) featureQueues() ([]HelpQueueDTO, []NeedInputGateDTO) {
	features, _, _ := listFeatures(h.features)
	var help []orderedHelpQueue
	var gates []orderedNeedInputGate
	for _, f := range features {
		for i, req := range f.HelpQueue {
			if !req.Pending {
				continue
			}
			help = append(help, orderedHelpQueue{
				dto: HelpQueueDTO{
					FeatureID: f.ID,
					Question:  safeDisplayText(req.Question, 300),
					Pending:   req.Pending,
					Time:      req.Time,
				},
				featureID: f.ID,
				index:     i,
			})
		}
		if f.PendingNeedUserInputPath != "" {
			gates = append(gates, orderedNeedInputGate{
				dto:       needUserInputGateDTO(f.ID, entityFeature, "", "", f.CurrentIteration, f.InputNotifications, f.PendingNeedUserInputPath),
				featureID: f.ID,
				created:   f.Created,
				gateTime:  gateFileTime(f.PendingNeedUserInputPath),
			})
		}
		for _, cycle := range f.PendingUserInputCycles() {
			iteration := 0
			if rc := f.RepoCycles[cycle.RepoName]; rc != nil {
				iteration = rc.Iteration
			}
			gates = append(gates, orderedNeedInputGate{
				dto:       needUserInputGateDTO(f.ID, entityFeature, cycle.RepoName, cycle.CycleType, iteration, f.InputNotifications, cycle.GatePath),
				featureID: f.ID,
				created:   f.Created,
				gateTime:  gateFileTime(cycle.GatePath),
			})
		}
	}
	if h.sessions != nil {
		for _, sess := range h.sessions.ActiveSessions() {
			if sess == nil || sess.Status() != ports.SessionWaitingHelp || sessionHasPendingAskUserControl(sess) {
				continue
			}
			help = append(help, orderedHelpQueue{
				dto: HelpQueueDTO{
					FeatureID: sess.FeatureID(),
					Question:  agentQuestionPrompt,
					Pending:   true,
					Time:      sess.StartedAt(),
				},
				featureID: sess.FeatureID(),
				index:     len(help),
			})
		}
	}
	sortOrderedHelpQueue(help)
	sortOrderedNeedInputGates(gates)
	return dtosOf(help, func(w orderedHelpQueue) HelpQueueDTO { return w.dto }),
		dtosOf(gates, func(w orderedNeedInputGate) NeedInputGateDTO { return w.dto })
}

func sessionHasPendingAskUserControl(sess ports.SessionView) bool {
	if sess == nil {
		return false
	}
	for _, req := range sess.PendingControlRequests() {
		if req != nil && req.Request.ToolName == toolNameAskUserQuestion {
			return true
		}
	}
	return false
}

func needUserInputGateDTO(featureID, scope, repoName string, cycleType feature.RepoCycleType, iteration int, inputNotifications feature.InputNotificationsMode, gatePath string) NeedInputGateDTO {
	dto := NeedInputGateDTO{
		FeatureID:          featureID,
		Open:               true,
		Scope:              scope,
		RepoName:           repoName,
		CycleType:          string(cycleType),
		InputNotifications: string(feature.NormalizeInputNotificationsMode(inputNotifications)),
		Iteration:          iteration,
	}
	rec, err := agent.ReadNeedUserInputRecord(gatePath)
	if err != nil {
		return dto
	}
	if dto.Iteration == 0 {
		dto.Iteration = rec.Iteration
	}
	dto.Summary = strings.TrimSpace(rec.Summary)
	dto.Questions = make([]NeedUserInputQuestionDTO, 0, len(rec.Questions))
	for _, q := range rec.Questions {
		prompt := strings.TrimSpace(q.Prompt)
		if prompt == "" {
			continue
		}
		dto.Questions = append(dto.Questions, NeedUserInputQuestionDTO{
			Index:  q.Index,
			Prompt: prompt,
			Answer: strings.TrimSpace(q.Answer),
		})
	}
	return dto
}

type orderedControlRequest struct {
	dto       ControlRequestDTO
	startedAt time.Time
	sessionID string
	index     int
}

func sortOrderedControlRequests(items []orderedControlRequest) {
	sort.SliceStable(items, func(i, j int) bool {
		if beforeByKnownTime(items[i].startedAt, items[j].startedAt) {
			return true
		}
		if beforeByKnownTime(items[j].startedAt, items[i].startedAt) {
			return false
		}
		if items[i].sessionID != items[j].sessionID {
			return items[i].sessionID < items[j].sessionID
		}
		if items[i].index != items[j].index {
			return items[i].index < items[j].index
		}
		return items[i].dto.RequestID < items[j].dto.RequestID
	})
}

type orderedHelpQueue struct {
	dto       HelpQueueDTO
	featureID string
	index     int
}

func sortOrderedHelpQueue(items []orderedHelpQueue) {
	sort.SliceStable(items, func(i, j int) bool {
		if beforeByKnownTime(items[i].dto.Time, items[j].dto.Time) {
			return true
		}
		if beforeByKnownTime(items[j].dto.Time, items[i].dto.Time) {
			return false
		}
		if items[i].featureID != items[j].featureID {
			return items[i].featureID < items[j].featureID
		}
		if items[i].index != items[j].index {
			return items[i].index < items[j].index
		}
		return items[i].dto.Question < items[j].dto.Question
	})
}

type orderedNeedInputGate struct {
	dto       NeedInputGateDTO
	featureID string
	created   time.Time
	gateTime  time.Time
}

func sortOrderedNeedInputGates(items []orderedNeedInputGate) {
	sort.SliceStable(items, func(i, j int) bool {
		if beforeByKnownTime(items[i].gateTime, items[j].gateTime) {
			return true
		}
		if beforeByKnownTime(items[j].gateTime, items[i].gateTime) {
			return false
		}
		if beforeByKnownTime(items[i].created, items[j].created) {
			return true
		}
		if beforeByKnownTime(items[j].created, items[i].created) {
			return false
		}
		if items[i].featureID != items[j].featureID {
			return items[i].featureID < items[j].featureID
		}
		return items[i].dto.Iteration < items[j].dto.Iteration
	})
}

func dtosOf[W any, D any](items []W, get func(W) D) []D {
	out := make([]D, 0, len(items))
	for _, item := range items {
		out = append(out, get(item))
	}
	return out
}

func gateFileTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func beforeByKnownTime(a, b time.Time) bool {
	if a.IsZero() {
		return false
	}
	if b.IsZero() {
		return true
	}
	return a.Before(b)
}

func featureConfigDTO(f *feature.Feature) FeatureConfigDTO {
	pipeline := f.Pipeline
	return FeatureConfigDTO{
		Models:             f.Models,
		Inquireness:        string(f.Inquireness),
		Checkpoints:        checkpointsDTO(pipeline.NormalizeCheckpoints(f.Checkpoints, f.IsPublishable())),
		Pipeline:           string(pipeline),
		InputNotifications: FeatureConfigInputNotifications(feature.NormalizeInputNotificationsMode(f.InputNotifications)),
	}
}

func checkpointsDTO(c feature.Checkpoints) CheckpointsDTO {
	return CheckpointsDTO{
		InquiryReview:   c.InquiryReview,
		ResearchReview:  c.ResearchReview,
		DesignReview:    c.DesignReview,
		RoadmapReview:   c.RoadmapReview,
		PhasePlanReview: c.PhasePlanReview,
		ManualPublish:   c.ManualPublish,
		DraftPublish:    c.DraftPublish,
	}
}

func controlRequestDTO(sess ports.SessionView, req *llm.ControlRequestMessage) ControlRequestDTO {
	if req == nil {
		return ControlRequestDTO{}
	}
	dto := ControlRequestDTO{
		RequestID: req.RequestID,
		SessionID: sess.ID(),
		FeatureID: sess.FeatureID(),
		Phase:     sess.Phase().String(),
		ToolName:  req.Request.ToolName,
		Status:    controlRequestStatusPending,
		Summary:   safeControlSummary(req),
	}
	if req.Request.ToolName == toolNameAskUserQuestion {
		dto.Questions = safeAskUserQuestions(sess, req)
	} else {
		dto.Input = safeControlInput(req)
		scope := sess.PermCacheScope()
		dto.Remember = &PermissionRememberPreviewDTO{
			Pattern:      safeRememberPattern(req),
			Scope:        scope,
			ScopeDisplay: permissionScopeDisplay(scope),
		}
	}
	return dto
}

func safeRememberPattern(req *llm.ControlRequestMessage) string {
	if req == nil {
		return ""
	}
	// The snapshot exposes a client-submitted default policy preview, so infer
	// from sanitized input when possible rather than leaking raw tool arguments.
	if safeInput := safeControlInput(req); safeInput != nil {
		if data, err := json.Marshal(safeInput); err == nil {
			return permission.InferBashPattern(req.Request.ToolName, string(data))
		}
	}
	return permission.InferBashPattern(req.Request.ToolName, safeRememberFallbackInput(req.Request.ToolName))
}

func safeRememberFallbackInput(toolName string) string {
	if toolName == toolNameBash {
		return ""
	}
	return "*"
}

func permissionScopeDisplay(scope string) string {
	if scope == "" {
		return "global"
	}
	return "repo: " + scope
}

func safeControlSummary(req *llm.ControlRequestMessage) string {
	if req == nil {
		return ""
	}
	if req.Request.ToolName == toolNameAskUserQuestion {
		var envelope struct {
			Questions []struct {
				Question string `json:"question"`
				Header   string `json:"header"`
			} `json:"questions"`
		}
		if json.Unmarshal(req.Request.Input, &envelope) == nil && len(envelope.Questions) > 0 {
			q := envelope.Questions[0].Question
			if q == "" {
				q = envelope.Questions[0].Header
			}
			return safeDisplayText(q, 180)
		}
		return "user input requested"
	}
	if detail := safeControlInputDetail(req.Request.ToolName, req.Request.Input); detail != "" {
		return safeDisplayText(detail, 180)
	}
	return safeDisplayText(req.Request.ToolName+" requested", 180)
}

func safeControlInput(req *llm.ControlRequestMessage) map[string]any {
	if req == nil || len(req.Request.Input) == 0 {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(req.Request.Input, &parsed); err != nil {
		return nil
	}
	return safeControlInputValue(parsed).(map[string]any)
}

func safeControlInputValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = safeControlInputValue(item)
		}
		return out
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, safeControlInputValue(item))
		}
		return out
	case string:
		return safeDisplayText(v, 2000)
	default:
		return v
	}
}

func safeControlInputDetail(toolName string, input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var fields map[string]any
	if err := json.Unmarshal(input, &fields); err != nil {
		return ""
	}
	switch toolName {
	case toolNameBash:
		return safeStringField(fields, "command")
	case toolNameWrite, toolNameEdit:
		return firstSafeStringField(fields, "file_path", "path")
	default:
		return firstSafeStringField(fields, "description", "summary", "title", "command", "file_path", "path")
	}
}

func firstSafeStringField(fields map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := safeStringField(fields, key); value != "" {
			return value
		}
	}
	return ""
}

func safeStringField(fields map[string]any, key string) string {
	value, ok := fields[key].(string)
	if !ok {
		return ""
	}
	return safeDisplayText(value, 2000)
}

const (
	askUserQuestionDisplayLimit          = 4000
	askUserHeaderDisplayLimit            = 1000
	askUserOptionLabelDisplayLimit       = 1000
	askUserOptionDescriptionDisplayLimit = 4000
)

func safeAskUserQuestions(sess ports.SessionView, req *llm.ControlRequestMessage) []AskUserQuestionDTO {
	if req == nil || req.Request.ToolName != toolNameAskUserQuestion {
		return nil
	}
	questions := safeAskUserQuestionsFromInput(req.Request.Input)
	if !askUserQuestionDTOsNeedConfidence(questions) || sess == nil || sess.MessageLog() == nil {
		return questions
	}
	return enrichAskUserQuestionDTOConfidence(questions, sess.MessageLog())
}

func safeAskUserQuestionsFromInput(input json.RawMessage) []AskUserQuestionDTO {
	if len(input) == 0 {
		return nil
	}
	var envelope struct {
		Questions []struct {
			Question    string `json:"question"`
			Header      string `json:"header"`
			MultiSelect bool   `json:"multiSelect"`
			Options     []struct {
				Label       string   `json:"label"`
				Description string   `json:"description"`
				Confidence  *float64 `json:"confidence"`
			} `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(input, &envelope); err != nil || len(envelope.Questions) == 0 {
		return nil
	}
	questions := make([]AskUserQuestionDTO, 0, len(envelope.Questions))
	for _, rawQuestion := range envelope.Questions {
		question := AskUserQuestionDTO{
			Question:    safeDisplayText(rawQuestion.Question, askUserQuestionDisplayLimit),
			Header:      safeDisplayText(rawQuestion.Header, askUserHeaderDisplayLimit),
			MultiSelect: rawQuestion.MultiSelect,
		}
		for _, rawOption := range rawQuestion.Options {
			option := AskUserOptionDTO{
				Label:       safeDisplayText(rawOption.Label, askUserOptionLabelDisplayLimit),
				Description: safeDisplayText(rawOption.Description, askUserOptionDescriptionDisplayLimit),
				Confidence:  rawOption.Confidence,
			}
			if option.Label == "" && option.Description == "" && option.Confidence == nil {
				continue
			}
			question.Options = append(question.Options, option)
		}
		if question.Question == "" && question.Header == "" && len(question.Options) == 0 {
			continue
		}
		questions = append(questions, question)
	}
	return questions
}

func askUserQuestionDTOsNeedConfidence(questions []AskUserQuestionDTO) bool {
	for _, q := range questions {
		for _, opt := range q.Options {
			if opt.Confidence == nil {
				return true
			}
		}
	}
	return false
}

func enrichAskUserQuestionDTOConfidence(questions []AskUserQuestionDTO, log ports.MessageLog) []AskUserQuestionDTO {
	if log == nil {
		return questions
	}
	blocks := log.ToolUseBlocks()
	for i := len(blocks) - 1; i >= 0; i-- {
		block := blocks[i]
		if block.Name != toolNameAskUserQuestion || len(block.Input) == 0 {
			continue
		}
		source := safeAskUserQuestionsFromInput(block.Input)
		if !askUserQuestionDTOBundlesMatch(questions, source) {
			continue
		}
		return copyAskUserQuestionDTOConfidence(questions, source)
	}
	return questions
}

func askUserQuestionDTOBundlesMatch(a, b []AskUserQuestionDTO) bool {
	if len(a) == 0 || len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.TrimSpace(a[i].Question) != strings.TrimSpace(b[i].Question) ||
			strings.TrimSpace(a[i].Header) != strings.TrimSpace(b[i].Header) ||
			a[i].MultiSelect != b[i].MultiSelect ||
			len(a[i].Options) != len(b[i].Options) {
			return false
		}
		for j := range a[i].Options {
			if strings.TrimSpace(a[i].Options[j].Label) != strings.TrimSpace(b[i].Options[j].Label) ||
				strings.TrimSpace(a[i].Options[j].Description) != strings.TrimSpace(b[i].Options[j].Description) {
				return false
			}
		}
	}
	return true
}

func copyAskUserQuestionDTOConfidence(questions, source []AskUserQuestionDTO) []AskUserQuestionDTO {
	enriched := make([]AskUserQuestionDTO, len(questions))
	for i := range questions {
		enriched[i] = questions[i]
		enriched[i].Options = append([]AskUserOptionDTO(nil), questions[i].Options...)
		for j := range enriched[i].Options {
			if enriched[i].Options[j].Confidence == nil {
				enriched[i].Options[j].Confidence = source[i].Options[j].Confidence
			}
		}
	}
	return enriched
}

func safeDisplayText(s string, limit int) string {
	s = strings.ReplaceAll(s, "private-token", "[redacted]")
	s = strings.ReplaceAll(s, "raw initial prompt", "[redacted prompt]")
	s = strings.TrimSpace(s)
	if limit > 0 && len(s) > limit {
		return s[:limit] + "..."
	}
	return s
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
