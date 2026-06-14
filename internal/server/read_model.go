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
	"strconv"
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
	}
	if f.ActiveCycle != nil {
		detail.Cycle = &CycleDTO{
			Type:      string(f.ActiveCycle.Type),
			Status:    f.ActiveCycle.Status,
			Count:     f.ActiveCycle.Count,
			Iteration: f.ActiveCycle.Iteration,
		}
	}
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
	return RunSummaryDTO{
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
	}
}

func repoStatusDTOs(f *feature.Feature) []RepoStatusDTO {
	out := make([]RepoStatusDTO, 0, len(f.Repos))
	for _, repo := range f.Repos {
		state := f.RepoStates[repo.Name]
		cycle := f.RepoCycles[repo.Name]
		dto := RepoStatusDTO{Name: repo.Name, Publishable: repo.Publishable == nil || *repo.Publishable}
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
		APIVersion: APIVersion,
		Runtime:    h.runtime,
		Defaults:   cfg.Defaults.Models,
		Repos:      repos,
		UI:         cfg.UI,
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
		for _, role := range []llm.PhaseRole{llm.PhaseResearch, llm.PhasePlanning, llm.PhaseImplementation, llm.PhaseReview, llm.PhaseKBBuild} {
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

func (h *apiHandler) handleOperations(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if state != "" && !validOperationState(state) {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid operation state filter", map[string]any{"state": state})
		return
	}
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if r.URL.Query().Get("limit") == "" {
		limit = 0
	} else if err != nil || limit < 0 {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid operation limit", nil)
		return
	}
	operations := []OperationDTO{}
	nextCursor := ""
	if h.operations != nil {
		page, err := h.operations.List(OperationListOptions{
			State:     OperationStatus(state),
			FeatureID: r.URL.Query().Get("feature_id"),
			Kind:      r.URL.Query().Get("kind"),
			Cursor:    r.URL.Query().Get("cursor"),
			Limit:     limit,
		})
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid operation list request", nil)
			return
		}
		operations = page.Operations
		nextCursor = page.NextCursor
	}
	resp := OperationSnapshotResponse{
		APIVersion: APIVersion,
		Schema: OperationSchemaDTO{
			Version: "phase3",
			States:  []string{"queued", "running", "succeeded", "failed", "rejected", "interrupted"},
			Filters: []string{"state", "feature_id", "kind"},
		},
		Operations: operations,
		NextCursor: nextCursor,
	}
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
	repos := make([]ConfigRepoDTO, 0, len(cfg.Repos)+len(cfg.DiscoveredRepos))
	for name, repo := range cfg.Repos {
		repos = append(repos, ConfigRepoDTO{Name: name, Path: repo.Path})
	}
	for name, repo := range cfg.DiscoveredRepos {
		if _, ok := cfg.Repos[name]; ok {
			continue
		}
		repos = append(repos, ConfigRepoDTO{Name: name, Path: repo.Path})
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })
	return repos
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
	return ControlRequestDTO{
		RequestID: req.RequestID,
		SessionID: sess.ID(),
		FeatureID: sess.FeatureID(),
		Phase:     sess.Phase().String(),
		ToolName:  req.Request.ToolName,
		Status:    "pending",
		Summary:   safeControlSummary(req),
	}
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
	return safeDisplayText(req.Request.ToolName+" requested", 180)
}

func validOperationState(state string) bool {
	switch state {
	case "queued", "running", "succeeded", "failed", "rejected", "interrupted":
		return true
	default:
		return false
	}
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
