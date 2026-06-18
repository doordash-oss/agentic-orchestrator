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

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const maxFeatureDetailHistoricalRuns = 5

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
	detail := FeatureDetailDTO{
		FeatureSummary: summarizeFeature(f),
		Description:    safeDisplayText(f.Description, 500),
		Summary:        safeDisplayText(f.Summary, 500),
		Pipeline:       string(f.Pipeline),
		Models:         f.Models,
		ActiveRun:      &active,
		HistoricalRuns: history,
		RepoStatus:     repoStatusDTOs(f),
		Timing:         timingDTO(f),
		Cost:           costDTO(f),
		ReviewGate: ReviewGateDTO{
			ReviewingGate:     f.ReviewingGate,
			ReviewFixing:      f.ReviewFixing,
			ValidatingPlan:    f.ValidatingPlan,
			ValidatorStatuses: copyStringMap(f.ValidatorStatuses),
		},
		Actions: actionCatalogDTOs(f),
	}
	detail.Cycle = activeCycleDTO(f)
	if f.HasTerminalFailure() {
		detail.Failure = &FailureDTO{
			Type:    f.FailureType,
			Message: safeDisplayText(f.LastError, 240),
		}
	}
	if f.PendingNeedUserInputPath != "" {
		detail.NeedUserInput = &NeedInputGateDTO{FeatureID: f.ID, Open: true, Scope: "feature", Iteration: f.CurrentIteration}
	}
	return detail
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
	case feature.CycleTweak:
		if f.TweakCount() > count {
			count = f.TweakCount()
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
	featureScope := ActionScopeDTO{Type: "feature"}
	repoOptional := ActionScopeDTO{Type: "feature", RepoSelection: "optional"}
	repoRequired := ActionScopeDTO{Type: "feature", RepoSelection: "required"}

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
		action("start", canStart, featureScope, nil, disabledStatusReason(status)),
		action("pause-stop", canStop, featureScope, nil, ActionDisabledReasonDTO{Code: "not_running", Message: "feature has no active work to pause or stop"}),
		action("resume", canResume, featureScope, nil, ActionDisabledReasonDTO{Code: "not_paused", Message: "feature has no paused session or input gate"}),
		action("restart", canRestart, featureScope, nil, ActionDisabledReasonDTO{Code: "running", Message: "feature must stop before restart"}),
		action("publish", canPublish, featureScope, nil, publishDisabledReason(f)),
		action("merge", canMerge, featureScope, nil, mergeDisabledReason(f)),
		action("rewind", canRewind, featureScope, []ActionInputDTO{
			{Name: "target_phase", Kind: "enum", Required: true, Options: rewindPhaseOptions(f)},
			{Name: "roadmap_phase", Kind: "integer", Required: false},
			{Name: "upgrade_pipeline", Kind: "enum", Required: false, Options: rewindUpgradePipelineOptions(f)},
		}, ActionDisabledReasonDTO{Code: "no_rewind_targets", Message: "feature has no valid rewind targets"}),
		action("rebase", canPostPublishCycle, repoOptional, []ActionInputDTO{
			{Name: "repo", Kind: "string", Required: false},
			{Name: "rebase_target", Kind: "string", Required: false, MaxLength: 128},
			{Name: "conflict_files", Kind: "string_list", Required: false},
		}, postPublishCycleDisabledReason(f, "rebase")),
		action("review-comments", canPostPublishCycle && f.IsPublishable(), repoRequired, []ActionInputDTO{
			{Name: "repo", Kind: "string", Required: true},
			{Name: "mode", Kind: "enum", Required: true, Options: []string{"auto", "address_all"}},
		}, postPublishCycleDisabledReason(f, "review-comments")),
		action("tweak", canPostPublishCycle, featureScope, nil, postPublishCycleDisabledReason(f, "tweak")),
		action("refactor", canRefactor, featureScope, []ActionInputDTO{
			{Name: "repo", Kind: "string", Required: false},
			{Name: "prompt", Kind: "string", Required: true, MaxLength: MaxActionTextBytes},
			{Name: "pipeline", Kind: "enum", Required: false, Options: []string{"medium", "large", "moonshot"}},
		}, postPublishCycleDisabledReason(f, "refactor")),
		action("retry", canRetry, featureScope, nil, ActionDisabledReasonDTO{Code: "not_failed", Message: "retry is only available for failed features"}),
		action("mark-done", canMarkDone, featureScope, nil, ActionDisabledReasonDTO{Code: "not_complete", Message: "feature is not ready to mark done"}),
		action("cleanup", canCleanup, featureScope, []ActionInputDTO{
			{Name: "target", Kind: "enum", Required: false, Options: []string{"worktrees", "cycles"}},
		}, ActionDisabledReasonDTO{Code: "running", Message: "cleanup is disabled while work is running"}),
		action("delete", canDelete, featureScope, nil, ActionDisabledReasonDTO{Code: "running", Message: "delete is disabled while work is running"}),
	}
}

func disabledStatusReason(status feature.Status) ActionDisabledReasonDTO {
	return ActionDisabledReasonDTO{
		Code:    "status_not_allowed",
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
		return ActionDisabledReasonDTO{Code: "not_local_only", Message: "merge is only available for local-only features"}
	}
	return disabledStatusReason(f.Status)
}

func postPublishCycleDisabledReason(f *feature.Feature, cycle string) ActionDisabledReasonDTO {
	if f == nil {
		return disabledStatusReason(feature.StatusCreated)
	}
	if f.HasActiveRepoCycles() || f.ActiveCycleType() != "" {
		return ActionDisabledReasonDTO{Code: "cycle_active", Message: "another feature cycle is active"}
	}
	if cycle == "review-comments" && !f.IsPublishable() {
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

func repoStatusDTOs(f *feature.Feature) []RepoStatusDTO {
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
		if cycle != nil {
			dto.CycleType = string(cycle.Type)
			dto.CycleStatus = cycle.Status
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
	resp.Meta = responseMeta(revision)
	writeRevisionedJSON(w, r, http.StatusOK, revision, resp)
}

func (h *apiHandler) handleFeatureConfig(w http.ResponseWriter, r *http.Request, featureID string) {
	f, err := h.loadFeature(featureID)
	if err != nil {
		writeStoreError(w, err, "feature", featureID)
		return
	}
	cfg := h.configOrDefault()
	defaults := FeatureConfigDTO{
		Models:      cfg.Defaults.Models,
		Inquireness: cfg.Defaults.Inquireness,
		Checkpoints: checkpointsDTO(featureCheckpoints(cfg.Defaults.Checkpoints)),
		Pipeline:    cfg.Defaults.Pipeline,
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
	resp.Meta = responseMeta(revision)
	writeRevisionedJSON(w, r, http.StatusOK, revision, resp)
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
		for _, role := range []llm.PhaseRole{llm.PhaseResearch, llm.PhasePlanning, llm.PhaseImplementation, llm.PhaseReview, llm.PhaseChat, llm.PhaseKBBuild} {
			resp.PhaseProviderModels[string(role)] = h.registry.EligibleModelsForPhase(role)
		}
	}
	revision := revisionForAny(resp)
	resp.Meta = responseMeta(revision)
	writeRevisionedJSON(w, r, http.StatusOK, revision, resp)
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
	resp.Meta = responseMeta(revision)
	writeRevisionedJSON(w, r, http.StatusOK, revision, resp)
}

func (h *apiHandler) handlePermissions(w http.ResponseWriter, r *http.Request) {
	_, perms := h.pendingControls()
	resp := PermissionSnapshotResponse{APIVersion: APIVersion, Requests: perms}
	revision := revisionForAny(resp)
	resp.Meta = responseMeta(revision)
	writeRevisionedJSON(w, r, http.StatusOK, revision, resp)
}

func (h *apiHandler) configOrDefault() *config.Config {
	if h.cfg != nil {
		return h.cfg
	}
	return config.NewDefault()
}

func configRepoDTOs(cfg *config.Config) []ConfigRepoDTO {
	cfg = runtimeConfigRepoSnapshot(cfg)
	repos := make([]ConfigRepoDTO, 0, len(cfg.Repos)+len(cfg.DiscoveredRepos))
	for name, repo := range cfg.Repos {
		repos = append(repos, ConfigRepoDTO{Name: name, Path: repo.Path, PipelineGates: copyConfigPipelineGates(repo.PipelineGates)})
	}
	for name, repo := range cfg.DiscoveredRepos {
		if _, ok := cfg.Repos[name]; ok {
			continue
		}
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
			if req.Request.ToolName == "AskUserQuestion" {
				asks = append(asks, entry)
			} else {
				perms = append(perms, entry)
			}
		}
	}
	sortOrderedControlRequests(asks)
	sortOrderedControlRequests(perms)
	return controlRequestDTOs(asks), controlRequestDTOs(perms)
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
				dto:       NeedInputGateDTO{FeatureID: f.ID, Open: true, Scope: "feature", Iteration: f.CurrentIteration},
				featureID: f.ID,
				created:   f.Created,
				gateTime:  gateFileTime(f.PendingNeedUserInputPath),
			})
		}
	}
	sortOrderedHelpQueue(help)
	sortOrderedNeedInputGates(gates)
	return helpQueueDTOs(help), needInputGateDTOs(gates)
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

func controlRequestDTOs(items []orderedControlRequest) []ControlRequestDTO {
	out := make([]ControlRequestDTO, 0, len(items))
	for _, item := range items {
		out = append(out, item.dto)
	}
	return out
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

func helpQueueDTOs(items []orderedHelpQueue) []HelpQueueDTO {
	out := make([]HelpQueueDTO, 0, len(items))
	for _, item := range items {
		out = append(out, item.dto)
	}
	return out
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

func needInputGateDTOs(items []orderedNeedInputGate) []NeedInputGateDTO {
	out := make([]NeedInputGateDTO, 0, len(items))
	for _, item := range items {
		out = append(out, item.dto)
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
		Models:      f.Models,
		Inquireness: string(f.Inquireness),
		Checkpoints: checkpointsDTO(pipeline.NormalizeCheckpoints(f.Checkpoints, f.IsPublishable())),
		Pipeline:    string(pipeline),
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
		Status:    "pending",
		Summary:   safeControlSummary(req),
	}
	if req.Request.ToolName == "AskUserQuestion" {
		dto.Questions = safeAskUserQuestions(sess, req)
	} else {
		dto.Input = safeControlInput(req)
	}
	return dto
}

func safeControlSummary(req *llm.ControlRequestMessage) string {
	if req == nil {
		return ""
	}
	if req.Request.ToolName == "AskUserQuestion" {
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
	case "Bash":
		return safeStringField(fields, "command")
	case "Write", "Edit":
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
	if req == nil || req.Request.ToolName != "AskUserQuestion" {
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
		if block.Name != "AskUserQuestion" || len(block.Input) == 0 {
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
