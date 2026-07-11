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
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const MaxMutationBodyBytes = 64 * 1024
const MaxActionTextBytes = 4000

type MutationTarget interface {
	CreateFeature(CreateFeatureRequest) (CreateFeatureResponse, error)
	StartFeature(featureID string) (FeatureStartResponse, error)
	StopFeature(featureID string) (FeatureStopResponse, error)
	RestartFeature(featureID string, req RestartFeatureRequest) (FeatureRestartResponse, error)
	ReviewDecision(featureID string, req ReviewDecisionRequest) (ReviewDecisionResponse, error)
	UpdateFeatureConfig(featureID string, req FeatureConfigMutationRequest) (FeatureConfigUpdateResponse, error)
	NeedUserInputDecision(featureID string, req NeedUserInputDecisionRequest) (NeedUserInputDecisionResponse, error)
	DraftNeedUserInputAnswers(featureID string, req NeedUserInputDraftRequest) (NeedUserInputDraftResponse, error)
	ToggleInputNotifications(featureID string) (InputNotificationsToggleResponse, error)
	AnswerPermission(req PermissionAnswerRequest) (PermissionAnswerResponse, error)
	AnswerAskUser(req AskUserAnswerRequest) (AskUserAnswerResponse, error)
	SendHelp(req HelpAnswerRequest) (HelpSendResponse, error)
	StartChat(req ChatStartRequest) (ChatStartResponse, error)
	RuntimeConfig(req RuntimeConfigMutationRequest) (RuntimeConfigUpdateResponse, error)
	GeneratePublishDescription(featureID string, req PublishDescriptionRequest) (PublishDescriptionResponse, error)
	PublishFeature(featureID string, req PublishFeatureRequest) (PublishFeatureResponse, error)
	MergeFeature(featureID string) (MergeFeatureResponse, error)
	RewindFeature(featureID string, req RewindFeatureRequest) (RewindFeatureResponse, error)
	RetryFeature(featureID string) (RetryFeatureResponse, error)
	StartRebase(featureID string, req RebaseActionRequest) (RebaseStartResponse, error)
	FetchReviewComments(featureID string, req ReviewCommentsFetchRequest) (ReviewCommentsFetchResponse, error)
	StartReviewComments(featureID string, req ReviewCommentsActionRequest) (ReviewCommentsStartResponse, error)
	StartTweak(featureID string, req TweakActionRequest) (TweakStartResponse, error)
	FinishTweak(featureID string, req TweakFinishRequest) (TweakFinishResponse, error)
	StartRefactor(featureID string, req RefactorActionRequest) (RefactorStartResponse, error)
	RestartRefactor(featureID string, req RefactorActionRequest) (RefactorRestartResponse, error)
	MarkDone(featureID string) (MarkDoneResponse, error)
	CleanupFeature(featureID string, req CleanupActionRequest) (CleanupFeatureResponse, error)
	DeleteFeature(featureID string) (DeleteFeatureResponse, error)
	ScanRecovery(ctx context.Context) ([]ports.RecoveryItem, error)
	ExecuteRecovery(ctx context.Context, items []ports.RecoveryItem, actions map[string]ports.RecoveryAction) (RecoveryActionResponse, error)
}

type ActionConflictError struct {
	Err     error
	Message string
	Target  map[string]any
}

func (e *ActionConflictError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return "conflict"
}

func (e *ActionConflictError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type CreateFeatureRequest struct {
	Name                    string                  `json:"name"`
	Description             string                  `json:"description,omitempty"`
	Repos                   []string                `json:"repos,omitempty"`
	Models                  config.ModelConfig      `json:"models,omitempty"`
	ExitCriteria            string                  `json:"exit_criteria,omitempty"`
	Inquireness             string                  `json:"inquireness,omitempty"`
	Images                  []string                `json:"images,omitempty"`
	UseCurrentBranch        bool                    `json:"use_current_branch,omitempty"`
	UseCurrentBranchPerRepo map[string]bool         `json:"use_current_branch_per_repo,omitempty"`
	Checkpoints             feature.Checkpoints     `json:"checkpoints,omitempty"`
	Attachments             []string                `json:"attachments,omitempty"`
	RiskLevel               feature.RiskLevel       `json:"risk_level,omitempty"`
	Pipeline                feature.PipelineProfile `json:"pipeline,omitempty"`
}

type RestartFeatureRequest struct {
	MaxIterationsDelta     int `json:"max_iterations_delta,omitempty"`
	MaxPlanIterationsDelta int `json:"max_plan_iterations_delta,omitempty"`
}

type ReviewDecisionRequest struct {
	Decision  string `json:"decision"`
	Phase     string `json:"phase,omitempty"`
	PhasePlan bool   `json:"phase_plan,omitempty"`
	Roadmap   bool   `json:"roadmap,omitempty"`
	IsRewind  bool   `json:"is_rewind,omitempty"`
	Comment   string `json:"comment,omitempty"`
}

type FeatureConfigMutationRequest struct {
	Models      config.ModelConfig      `json:"models,omitempty"`
	Inquireness string                  `json:"inquireness,omitempty"`
	Checkpoints feature.Checkpoints     `json:"checkpoints,omitempty"`
	Pipeline    feature.PipelineProfile `json:"pipeline,omitempty"`
}

type NeedUserInputDecisionRequest struct {
	Decision  string `json:"decision"`
	RepoName  string `json:"repo_name,omitempty"`
	CycleType string `json:"cycle_type,omitempty"`
}

type NeedUserInputDraftRequest struct {
	RepoName  string            `json:"repo_name,omitempty"`
	CycleType string            `json:"cycle_type,omitempty"`
	Answers   map[string]string `json:"answers"`
}

type PermissionAnswerRequest struct {
	RequestID string `json:"request_id"`
	SessionID string `json:"session_id,omitempty"`
	Decision  string `json:"decision"`
}

type AskUserAnswerRequest struct {
	RequestID string            `json:"request_id"`
	SessionID string            `json:"session_id,omitempty"`
	Answers   map[string]string `json:"answers"`
}

type HelpAnswerRequest struct {
	FeatureID string `json:"feature_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Message   string `json:"message"`
}

type ChatStartRequest struct {
	Message string `json:"message"`
}

type RuntimeConfigMutationRequest struct {
	Defaults       config.DefaultsConfig  `json:"defaults,omitempty"`
	WorkspaceRoots *[]string              `json:"workspace_roots,omitempty"`
	UI             *config.UIConfig       `json:"ui,omitempty"`
	Notifications  *NotificationConfigDTO `json:"notifications,omitempty"`
}

type PublishFeatureRequest struct {
	Repos []string `json:"repos,omitempty"`
	Title string   `json:"title,omitempty"`
	Body  string   `json:"body,omitempty"`
}

type PublishDescriptionRequest struct {
	Model              string `json:"model,omitempty"`
	FeatureName        string `json:"feature_name,omitempty"`
	FeatureDescription string `json:"feature_description,omitempty"`
	Roadmap            string `json:"roadmap,omitempty"`
	CommitBodies       string `json:"commit_bodies,omitempty"`
	DiffStat           string `json:"diff_stat,omitempty"`
}

type RewindFeatureRequest struct {
	TargetPhase     string                  `json:"target_phase"`
	RoadmapPhase    int                     `json:"roadmap_phase,omitempty"`
	UpgradePipeline feature.PipelineProfile `json:"upgrade_pipeline,omitempty"`
}

type RebaseActionRequest struct {
	Repo          string   `json:"repo,omitempty"`
	RebaseTarget  string   `json:"rebase_target,omitempty"`
	ConflictFiles []string `json:"conflict_files,omitempty"`
}

type ReviewCommentsFetchRequest struct {
	Repo string `json:"repo"`
}

type ReviewCommentsActionRequest struct {
	Repo     string             `json:"repo"`
	Mode     string             `json:"mode"`
	Comments []ReviewCommentDTO `json:"comments,omitempty"`
}

type TweakActionRequest struct{}

type TweakFinishRequest struct {
	Decision   string `json:"decision"`
	HadChanges bool   `json:"had_changes,omitempty"`
}

type RefactorActionRequest struct {
	Repo        string                  `json:"repo,omitempty"`
	Prompt      string                  `json:"prompt"`
	Images      []string                `json:"images,omitempty"`
	Attachments []string                `json:"attachments,omitempty"`
	Pipeline    feature.PipelineProfile `json:"pipeline,omitempty"`
}

type CleanupActionRequest struct {
	Target string `json:"target,omitempty"`
	Repo   string `json:"repo,omitempty"`
}

type actionResponse interface {
	setAPIVersion()
}

func writeActionJSON(w http.ResponseWriter, status int, resp actionResponse) {
	resp.setAPIVersion()
	writeJSON(w, status, resp)
}

func writeMutationError(w http.ResponseWriter, err error) {
	if err == nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "mutation failed", nil)
		return
	}
	var conflict *ActionConflictError
	if errors.As(err, &conflict) {
		writeAPIError(w, http.StatusConflict, "conflict", conflict.Error(), conflict.Target)
		return
	}
	writeAPIError(w, http.StatusBadRequest, "bad_request", err.Error(), nil)
}

func (h *apiHandler) handleMutationPreflight(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodOptions {
		return false
	}
	methods, ok := mutationRouteMethods(r.URL.Path)
	if !ok {
		return false
	}
	if h.mutations == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "unavailable", "mutation service unavailable", nil)
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" || !isLoopbackOrigin(origin) {
		writeAPIError(w, http.StatusForbidden, "forbidden", "browser origin is not trusted", nil)
		return true
	}
	requestMethod := strings.ToUpper(strings.TrimSpace(r.Header.Get("Access-Control-Request-Method")))
	if !containsMethod(methods, requestMethod) {
		w.Header().Set("Allow", strings.Join(methods, ", "))
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return true
	}
	if !isAllowedMutationPreflightHeaders(r.Header.Get("Access-Control-Request-Headers")) {
		writeAPIError(w, http.StatusForbidden, "forbidden", "mutation preflight headers are not trusted", nil)
		return true
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Methods", requestMethod)
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Agentico-Client")
	w.WriteHeader(http.StatusNoContent)
	return true
}

func (h *apiHandler) applyMutationCORS(w http.ResponseWriter, r *http.Request) bool {
	methods, ok := mutationRouteMethods(r.URL.Path)
	if !ok || !containsMethod(methods, r.Method) {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	if !isLoopbackOrigin(origin) {
		writeAPIError(w, http.StatusForbidden, "forbidden", "browser origin is not trusted", nil)
		return true
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	return false
}

func mutationRouteMethods(path string) ([]string, bool) {
	switch path {
	case "/api/v1/features":
		return []string{http.MethodPost}, true
	case "/api/v1/config/runtime":
		return []string{http.MethodPatch, http.MethodPut}, true
	case "/api/v1/permissions/answer":
		return []string{http.MethodPost}, true
	case "/api/v1/recovery/actions":
		return []string{http.MethodPost}, true
	case "/api/v1/shutdown":
		return []string{http.MethodPost}, true
	case "/api/v1/prompts/ask-user/answer", "/api/v1/prompts/help/send", "/api/v1/prompts/chat/start":
		return []string{http.MethodPost}, true
	}
	if !strings.HasPrefix(path, "/api/v1/features/") {
		return nil, false
	}
	parts := splitPath(strings.TrimPrefix(path, "/api/v1/features/"))
	if invalidPathParts(parts) || len(parts) < 2 || !validEntityID(parts[0]) {
		return nil, false
	}
	switch parts[1] {
	case "start", "resume", "stop", "interrupt", "restart", "review-decision", "config", "need-user-input", "need-user-input-draft", "input-notifications":
		return []string{http.MethodPost}, true
	case "actions":
		if len(parts) < 3 || len(parts) > 4 {
			return nil, false
		}
		switch parts[2] {
		case "start", "pause-stop", "resume", "restart", "publish", "merge", "rewind", "rebase", "review-comments", "tweak", "refactor", "retry", "mark-done", "cleanup", "delete":
			if len(parts) == 3 {
				return []string{http.MethodPost}, true
			}
			if (parts[2] == "publish" && parts[3] == "description") ||
				(parts[2] == "review-comments" && parts[3] == "fetch") ||
				(parts[2] == "tweak" && parts[3] == "finish") ||
				(parts[2] == "refactor" && parts[3] == "restart") {
				return []string{http.MethodPost}, true
			}
		}
	default:
		return nil, false
	}
	return nil, false
}

func containsMethod(methods []string, method string) bool {
	for _, allowed := range methods {
		if method == allowed {
			return true
		}
	}
	return false
}

func isAllowedMutationPreflightHeaders(raw string) bool {
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		header := strings.ToLower(strings.TrimSpace(part))
		if header == "" {
			continue
		}
		switch header {
		case "authorization", "content-type", "x-agentico-client":
			seen[header] = true
		default:
			return false
		}
	}
	return seen["content-type"] && seen["x-agentico-client"]
}

func (h *apiHandler) handleFeaturesRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleFeatureList(w, r)
	case http.MethodPost:
		h.handleCreateFeatureMutation(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
	}
}

func (h *apiHandler) handleCreateFeatureMutation(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength > MaxMutationBodyBytes {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "request_too_large", "mutation body is too large", nil)
		return
	}
	var req CreateFeatureRequest
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "name is required", nil)
		return
	}
	if !validatePipelineProfile(w, req.Pipeline) || !validateRiskLevel(w, req.RiskLevel) {
		return
	}
	if !h.requireTrustedMutation(w, r) {
		return
	}
	resp, err := h.mutations.CreateFeature(req)
	if err != nil {
		writeMutationError(w, err)
		return
	}
	if resp.Result == "" {
		resp.Result = "created"
	}
	writeActionJSON(w, http.StatusCreated, &resp)
}

func (h *apiHandler) handleFeatureMutationRoute(w http.ResponseWriter, r *http.Request, featureID string, parts []string) bool {
	if r.Method != http.MethodPost {
		return false
	}
	if len(parts) > 0 && parts[0] == "actions" {
		return h.handleFeatureActionRoute(w, r, featureID, parts[1:])
	}
	if len(parts) != 1 {
		return false
	}
	switch parts[0] {
	case "start", "resume":
		h.handleStartFeatureMutation(w, r, featureID)
	case "stop", "interrupt":
		h.handleStopFeatureMutation(w, r, featureID)
	case "restart":
		var req RestartFeatureRequest
		if !h.requireTrustedMutation(w, r) || !decodeMutationJSON(w, r, &req) {
			return true
		}
		h.writeRestartFeature(w, featureID, req)
	case "review-decision":
		var req ReviewDecisionRequest
		if !h.requireTrustedMutation(w, r) || !decodeMutationJSON(w, r, &req) {
			return true
		}
		resp, err := h.mutations.ReviewDecision(featureID, req)
		if err != nil {
			writeMutationError(w, err)
			return true
		}
		if resp.FeatureID == "" {
			resp.FeatureID = featureID
		}
		if resp.Result == "" {
			resp.Result = "submitted"
		}
		writeActionJSON(w, http.StatusOK, &resp)
	case "config":
		var req FeatureConfigMutationRequest
		if !h.requireTrustedMutation(w, r) || !decodeMutationJSON(w, r, &req) {
			return true
		}
		if !validatePipelineProfile(w, req.Pipeline) {
			return true
		}
		resp, err := h.mutations.UpdateFeatureConfig(featureID, req)
		if err != nil {
			writeMutationError(w, err)
			return true
		}
		if resp.FeatureID == "" {
			resp.FeatureID = featureID
		}
		if resp.Result == "" {
			resp.Result = "updated"
		}
		writeActionJSON(w, http.StatusOK, &resp)
	case "need-user-input":
		var req NeedUserInputDecisionRequest
		if !h.requireTrustedMutation(w, r) || !decodeMutationJSON(w, r, &req) {
			return true
		}
		if req.Decision != "resume" && req.Decision != "abort" {
			writeAPIError(w, http.StatusBadRequest, "bad_request", "decision must be resume or abort", nil)
			return true
		}
		resp, err := h.mutations.NeedUserInputDecision(featureID, req)
		if err != nil {
			writeMutationError(w, err)
			return true
		}
		if resp.FeatureID == "" {
			resp.FeatureID = featureID
		}
		if resp.Result == "" {
			resp.Result = "decided"
		}
		writeActionJSON(w, http.StatusOK, &resp)
	case "need-user-input-draft":
		var req NeedUserInputDraftRequest
		if !h.requireTrustedMutation(w, r) || !decodeMutationJSON(w, r, &req) {
			return true
		}
		if len(req.Answers) == 0 {
			writeAPIError(w, http.StatusBadRequest, "bad_request", "answers are required", nil)
			return true
		}
		resp, err := h.mutations.DraftNeedUserInputAnswers(featureID, req)
		if err != nil {
			writeMutationError(w, err)
			return true
		}
		if resp.FeatureID == "" {
			resp.FeatureID = featureID
		}
		if resp.Result == "" {
			resp.Result = "drafted"
		}
		writeActionJSON(w, http.StatusOK, &resp)
	case "input-notifications":
		h.handleToggleInputNotifications(w, r, featureID)
	default:
		return false
	}
	return true
}

func (h *apiHandler) handleFeatureActionRoute(w http.ResponseWriter, r *http.Request, featureID string, parts []string) bool {
	if len(parts) == 0 || len(parts) > 2 {
		return false
	}
	action := parts[0]
	subaction := ""
	if len(parts) == 2 {
		subaction = parts[1]
	}
	if !h.requireTrustedMutation(w, r) {
		return true
	}
	switch action {
	case "start", "resume":
		if subaction != "" {
			return false
		}
		h.handleStartFeatureMutationTrusted(w, r, featureID)
	case "pause-stop":
		if subaction != "" {
			return false
		}
		h.handleStopFeatureMutationTrusted(w, r, featureID)
	case "restart":
		if subaction != "" {
			return false
		}
		var req RestartFeatureRequest
		if !decodeMutationJSON(w, r, &req) {
			return true
		}
		h.writeRestartFeature(w, featureID, req)
	case "publish":
		if subaction == "description" {
			var req PublishDescriptionRequest
			if !decodeMutationJSON(w, r, &req) {
				return true
			}
			resp, err := h.mutations.GeneratePublishDescription(featureID, req)
			if err != nil {
				writeMutationError(w, err)
				return true
			}
			if resp.FeatureID == "" {
				resp.FeatureID = featureID
			}
			if resp.Result == "" {
				resp.Result = "generated"
			}
			writeActionJSON(w, http.StatusOK, &resp)
			return true
		}
		if subaction != "" {
			return false
		}
		var req PublishFeatureRequest
		if !decodeMutationJSON(w, r, &req) || !validateRepoList(w, req.Repos, false) {
			return true
		}
		resp, err := h.mutations.PublishFeature(featureID, req)
		if err != nil {
			writeMutationError(w, err)
			return true
		}
		if resp.FeatureID == "" {
			resp.FeatureID = featureID
		}
		if resp.Result == "" {
			resp.Result = "published"
		}
		writeActionJSON(w, http.StatusOK, &resp)
	case "merge":
		if subaction != "" {
			return false
		}
		var req map[string]any
		if !decodeMutationJSON(w, r, &req) {
			return true
		}
		resp, err := h.mutations.MergeFeature(featureID)
		if err != nil {
			writeMutationError(w, err)
			return true
		}
		if resp.FeatureID == "" {
			resp.FeatureID = featureID
		}
		if resp.Result == "" {
			resp.Result = "merged"
		}
		writeActionJSON(w, http.StatusOK, &resp)
	case "rewind":
		if subaction != "" {
			return false
		}
		var req RewindFeatureRequest
		if !decodeMutationJSON(w, r, &req) ||
			!validatePhaseName(w, req.TargetPhase) ||
			!validatePositiveOptionalInt(w, "roadmap_phase", req.RoadmapPhase) ||
			!validatePipelineProfile(w, req.UpgradePipeline) {
			return true
		}
		resp, err := h.mutations.RewindFeature(featureID, req)
		if err != nil {
			writeMutationError(w, err)
			return true
		}
		if resp.FeatureID == "" {
			resp.FeatureID = featureID
		}
		if resp.Result == "" {
			resp.Result = "rewound"
		}
		writeActionJSON(w, http.StatusOK, &resp)
	case "retry":
		if subaction != "" {
			return false
		}
		var req map[string]any
		if !decodeMutationJSON(w, r, &req) {
			return true
		}
		resp, err := h.mutations.RetryFeature(featureID)
		if err != nil {
			writeMutationError(w, err)
			return true
		}
		if resp.FeatureID == "" {
			resp.FeatureID = featureID
		}
		if resp.Result == "" {
			resp.Result = "retried"
		}
		writeActionJSON(w, http.StatusOK, &resp)
	case "rebase":
		if subaction != "" {
			return false
		}
		var req RebaseActionRequest
		if !decodeRebaseActionRequest(w, r, &req) {
			return true
		}
		resp, err := h.mutations.StartRebase(featureID, req)
		if err != nil {
			writeMutationError(w, err)
			return true
		}
		if resp.FeatureID == "" {
			resp.FeatureID = featureID
		}
		if resp.Result == "" {
			resp.Result = "started"
		}
		writeActionJSON(w, http.StatusOK, &resp)
	case "review-comments":
		return h.handleReviewCommentsAction(w, r, featureID, subaction)
	case "tweak":
		return h.handleTweakAction(w, r, featureID, subaction)
	case "refactor":
		return h.handleRefactorAction(w, r, featureID, subaction)
	case "mark-done":
		if subaction != "" {
			return false
		}
		var req map[string]any
		if !decodeMutationJSON(w, r, &req) {
			return true
		}
		resp, err := h.mutations.MarkDone(featureID)
		if err != nil {
			writeMutationError(w, err)
			return true
		}
		if resp.FeatureID == "" {
			resp.FeatureID = featureID
		}
		if resp.Result == "" {
			resp.Result = "done"
		}
		writeActionJSON(w, http.StatusOK, &resp)
	case "cleanup":
		if subaction != "" {
			return false
		}
		var req CleanupActionRequest
		if !decodeMutationJSON(w, r, &req) || !validateCleanupRequest(w, req) {
			return true
		}
		resp, err := h.mutations.CleanupFeature(featureID, req)
		if err != nil {
			writeMutationError(w, err)
			return true
		}
		if resp.FeatureID == "" {
			resp.FeatureID = featureID
		}
		if resp.Result == "" {
			resp.Result = "cleaned"
		}
		writeActionJSON(w, http.StatusOK, &resp)
	case "delete":
		if subaction != "" {
			return false
		}
		var req map[string]any
		if !decodeMutationJSON(w, r, &req) {
			return true
		}
		resp, err := h.mutations.DeleteFeature(featureID)
		if err != nil {
			writeMutationError(w, err)
			return true
		}
		if resp.FeatureID == "" {
			resp.FeatureID = featureID
		}
		if resp.Result == "" {
			resp.Result = "deleted"
		}
		writeActionJSON(w, http.StatusOK, &resp)
	default:
		return false
	}
	return true
}

func (h *apiHandler) handleRuntimeConfigRoute(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleRuntimeConfig(w, r)
	case http.MethodPatch, http.MethodPut:
		if !h.requireTrustedMutation(w, r) {
			return
		}
		var req RuntimeConfigMutationRequest
		if !decodeMutationJSON(w, r, &req) {
			return
		}
		resp, err := h.mutations.RuntimeConfig(req)
		if err != nil {
			writeMutationError(w, err)
			return
		}
		if resp.Result == "" {
			resp.Result = "updated"
		}
		writeActionJSON(w, http.StatusOK, &resp)
	default:
		w.Header().Set("Allow", "GET, PATCH, PUT")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
	}
}

func (h *apiHandler) handleShutdownMutationRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	if !h.requireTrustedMutation(w, r) {
		return
	}
	var req map[string]any
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	resp := ShutdownResponse{Result: "shutdown_scheduled"}
	writeActionJSON(w, http.StatusOK, &resp)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	if h.requestShutdown != nil {
		go h.requestShutdown()
	}
}

func (h *apiHandler) handlePermissionMutationRoutes(w http.ResponseWriter, r *http.Request) {
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/api/v1/permissions/"))
	if r.Method != http.MethodPost || len(parts) != 1 || parts[0] != "answer" {
		w.Header().Set("Allow", "POST")
		writeAPIError(w, http.StatusNotFound, "not_found", "endpoint not found", nil)
		return
	}
	if !h.requireTrustedMutation(w, r) {
		return
	}
	var req PermissionAnswerRequest
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	req.Decision = strings.ToLower(strings.TrimSpace(req.Decision))
	if strings.TrimSpace(req.RequestID) == "" {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "request_id is required", nil)
		return
	}
	if req.Decision != "allow" && req.Decision != "deny" {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "decision must be allow or deny", nil)
		return
	}
	resp, err := h.mutations.AnswerPermission(req)
	if err != nil {
		writeMutationError(w, err)
		return
	}
	if resp.Result == "" {
		resp.Result = "answered"
	}
	writeActionJSON(w, http.StatusOK, &resp)
}

func (h *apiHandler) handlePromptMutationRoutes(w http.ResponseWriter, r *http.Request) {
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/api/v1/prompts/"))
	if r.Method != http.MethodPost || len(parts) != 2 {
		w.Header().Set("Allow", "POST")
		writeAPIError(w, http.StatusNotFound, "not_found", "endpoint not found", nil)
		return
	}
	if !h.requireTrustedMutation(w, r) {
		return
	}
	switch strings.Join(parts, "/") {
	case "ask-user/answer":
		var req AskUserAnswerRequest
		if !decodeMutationJSON(w, r, &req) {
			return
		}
		if strings.TrimSpace(req.RequestID) == "" {
			writeAPIError(w, http.StatusBadRequest, "bad_request", "request_id is required", nil)
			return
		}
		if len(req.Answers) == 0 {
			writeAPIError(w, http.StatusBadRequest, "bad_request", "answers are required", nil)
			return
		}
		resp, err := h.mutations.AnswerAskUser(req)
		if err != nil {
			writeMutationError(w, err)
			return
		}
		if resp.Result == "" {
			resp.Result = "answered"
		}
		writeActionJSON(w, http.StatusOK, &resp)
	case "help/send":
		var req HelpAnswerRequest
		if !decodeMutationJSON(w, r, &req) {
			return
		}
		if strings.TrimSpace(req.Message) == "" {
			writeAPIError(w, http.StatusBadRequest, "bad_request", "message is required", nil)
			return
		}
		if strings.TrimSpace(req.SessionID) == "" && strings.TrimSpace(req.FeatureID) == "" {
			writeAPIError(w, http.StatusBadRequest, "bad_request", "session_id or feature_id is required", nil)
			return
		}
		resp, err := h.mutations.SendHelp(req)
		if err != nil {
			writeMutationError(w, err)
			return
		}
		if resp.Result == "" {
			resp.Result = "sent"
		}
		writeActionJSON(w, http.StatusOK, &resp)
	case "chat/start":
		var req ChatStartRequest
		if !decodeMutationJSON(w, r, &req) {
			return
		}
		if strings.TrimSpace(req.Message) == "" {
			writeAPIError(w, http.StatusBadRequest, "bad_request", "message is required", nil)
			return
		}
		resp, err := h.mutations.StartChat(req)
		if err != nil {
			writeMutationError(w, err)
			return
		}
		if resp.Result == "" {
			resp.Result = "started"
		}
		writeActionJSON(w, http.StatusOK, &resp)
	default:
		writeAPIError(w, http.StatusNotFound, "not_found", "endpoint not found", nil)
	}
}

func (h *apiHandler) handleStartFeatureMutation(w http.ResponseWriter, r *http.Request, featureID string) {
	if !h.requireTrustedMutation(w, r) {
		return
	}
	h.handleStartFeatureMutationTrusted(w, r, featureID)
}

func (h *apiHandler) handleStartFeatureMutationTrusted(w http.ResponseWriter, r *http.Request, featureID string) {
	var req map[string]any
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	resp, err := h.mutations.StartFeature(featureID)
	if err != nil {
		writeMutationError(w, err)
		return
	}
	if resp.FeatureID == "" {
		resp.FeatureID = featureID
	}
	if resp.Result == "" {
		resp.Result = "started"
	}
	writeActionJSON(w, http.StatusOK, &resp)
}

func (h *apiHandler) handleStopFeatureMutation(w http.ResponseWriter, r *http.Request, featureID string) {
	if !h.requireTrustedMutation(w, r) {
		return
	}
	h.handleStopFeatureMutationTrusted(w, r, featureID)
}

func (h *apiHandler) handleStopFeatureMutationTrusted(w http.ResponseWriter, r *http.Request, featureID string) {
	var req map[string]any
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	resp, err := h.mutations.StopFeature(featureID)
	if err != nil {
		writeMutationError(w, err)
		return
	}
	if resp.FeatureID == "" {
		resp.FeatureID = featureID
	}
	if resp.Result == "" {
		resp.Result = "stopped"
	}
	writeActionJSON(w, http.StatusOK, &resp)
}

func (h *apiHandler) handleToggleInputNotifications(w http.ResponseWriter, r *http.Request, featureID string) {
	if !h.requireTrustedMutation(w, r) {
		return
	}
	var req map[string]any
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	resp, err := h.mutations.ToggleInputNotifications(featureID)
	if err != nil {
		writeMutationError(w, err)
		return
	}
	if resp.FeatureID == "" {
		resp.FeatureID = featureID
	}
	if resp.Result == "" {
		resp.Result = "updated"
	}
	writeActionJSON(w, http.StatusOK, &resp)
}

func (h *apiHandler) writeRestartFeature(w http.ResponseWriter, featureID string, req RestartFeatureRequest) {
	resp, err := h.mutations.RestartFeature(featureID, req)
	if err != nil {
		writeMutationError(w, err)
		return
	}
	if resp.FeatureID == "" {
		resp.FeatureID = featureID
	}
	if resp.Result == "" {
		resp.Result = "restarted"
	}
	writeActionJSON(w, http.StatusOK, &resp)
}

func (h *apiHandler) handleReviewCommentsAction(w http.ResponseWriter, r *http.Request, featureID, subaction string) bool {
	if subaction == "fetch" {
		var req ReviewCommentsFetchRequest
		if !decodeMutationJSON(w, r, &req) || !validateRepoName(w, req.Repo, true) {
			return true
		}
		resp, err := h.mutations.FetchReviewComments(featureID, req)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "bad_request", "fetch review comments failed", nil)
			return true
		}
		if resp.APIVersion == "" {
			resp.APIVersion = APIVersion
		}
		if resp.FeatureID == "" {
			resp.FeatureID = featureID
		}
		if resp.Comments == nil {
			resp.Comments = []ReviewCommentDTO{}
		}
		writeJSON(w, http.StatusOK, resp)
		return true
	}
	if subaction != "" {
		return false
	}
	var req ReviewCommentsActionRequest
	if !decodeMutationJSON(w, r, &req) || !validateRepoName(w, req.Repo, true) || !validateReviewCommentsMode(w, req.Mode) {
		return true
	}
	resp, err := h.mutations.StartReviewComments(featureID, req)
	if err != nil {
		writeMutationError(w, err)
		return true
	}
	if resp.FeatureID == "" {
		resp.FeatureID = featureID
	}
	if resp.Result == "" {
		resp.Result = "started"
	}
	writeActionJSON(w, http.StatusOK, &resp)
	return true
}

func (h *apiHandler) handleTweakAction(w http.ResponseWriter, r *http.Request, featureID, subaction string) bool {
	if subaction == "finish" {
		var req TweakFinishRequest
		if !decodeMutationJSON(w, r, &req) || !validateTweakDecision(w, req.Decision) {
			return true
		}
		resp, err := h.mutations.FinishTweak(featureID, req)
		if err != nil {
			writeMutationError(w, err)
			return true
		}
		if resp.FeatureID == "" {
			resp.FeatureID = featureID
		}
		if resp.Result == "" {
			resp.Result = "finished"
		}
		writeActionJSON(w, http.StatusOK, &resp)
		return true
	}
	if subaction != "" {
		return false
	}
	var req TweakActionRequest
	if !decodeMutationJSON(w, r, &req) {
		return true
	}
	resp, err := h.mutations.StartTweak(featureID, req)
	if err != nil {
		writeMutationError(w, err)
		return true
	}
	if resp.FeatureID == "" {
		resp.FeatureID = featureID
	}
	if resp.Result == "" {
		resp.Result = "started"
	}
	writeActionJSON(w, http.StatusOK, &resp)
	return true
}

func (h *apiHandler) handleRefactorAction(w http.ResponseWriter, r *http.Request, featureID, subaction string) bool {
	var req RefactorActionRequest
	if !decodeMutationJSON(w, r, &req) || !validateRefactorRequest(w, req) {
		return true
	}
	if subaction == "restart" {
		resp, err := h.mutations.RestartRefactor(featureID, req)
		if err != nil {
			writeMutationError(w, err)
			return true
		}
		if resp.FeatureID == "" {
			resp.FeatureID = featureID
		}
		if resp.Result == "" {
			resp.Result = "restarted"
		}
		writeActionJSON(w, http.StatusOK, &resp)
		return true
	}
	if subaction != "" {
		return false
	}
	resp, err := h.mutations.StartRefactor(featureID, req)
	if err != nil {
		writeMutationError(w, err)
		return true
	}
	if resp.FeatureID == "" {
		resp.FeatureID = featureID
	}
	if resp.Result == "" {
		resp.Result = "started"
	}
	writeActionJSON(w, http.StatusOK, &resp)
	return true
}

func validatePipelineProfile(w http.ResponseWriter, profile feature.PipelineProfile) bool {
	if profile == "" || profile.IsValid() {
		return true
	}
	writeAPIError(w, http.StatusBadRequest, "bad_request", "pipeline must be medium, large, or moonshot", nil)
	return false
}

func validateRiskLevel(w http.ResponseWriter, risk feature.RiskLevel) bool {
	switch risk {
	case "", feature.RiskLow, feature.RiskMedium, feature.RiskHigh:
		return true
	default:
		writeAPIError(w, http.StatusBadRequest, "bad_request", "risk_level must be low, medium, or high", nil)
		return false
	}
}

func validateRepoList(w http.ResponseWriter, repos []string, required bool) bool {
	if required && len(repos) == 0 {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "repos are required", nil)
		return false
	}
	if len(repos) > 50 {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "too many repos", nil)
		return false
	}
	for _, repo := range repos {
		if !validateRepoName(w, repo, true) {
			return false
		}
	}
	return true
}

func decodeRebaseActionRequest(w http.ResponseWriter, r *http.Request, out *RebaseActionRequest) bool {
	fields, ok := decodeMutationObject(w, r)
	if !ok {
		return false
	}
	for _, name := range []string{"repo", "rebase_target", "conflict_files"} {
		if _, exists := fields[name]; exists {
			writeAPIError(w, http.StatusBadRequest, "bad_request", "rebase is feature-scoped; repo and conflict inputs are internal state", nil)
			return false
		}
	}
	if len(fields) > 0 {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid JSON request", nil)
		return false
	}
	*out = RebaseActionRequest{}
	return true
}

func validateRepoName(w http.ResponseWriter, repo string, required bool) bool {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		if required {
			writeAPIError(w, http.StatusBadRequest, "bad_request", "repo is required", nil)
			return false
		}
		return true
	}
	if !safeActionToken(repo, false) {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "repo has invalid characters", nil)
		return false
	}
	return true
}

func validatePhaseName(w http.ResponseWriter, phase string) bool {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "knowledge-base", "knowledgebase", "inquire", "research", "design", "plan", "implement", "review", "final-review", "publish":
		return true
	default:
		writeAPIError(w, http.StatusBadRequest, "bad_request", "target_phase is invalid", nil)
		return false
	}
}

func validatePositiveOptionalInt(w http.ResponseWriter, field string, value int) bool {
	if value >= 0 {
		return true
	}
	writeAPIError(w, http.StatusBadRequest, "bad_request", field+" must be positive", nil)
	return false
}

func validateReviewCommentsMode(w http.ResponseWriter, mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "auto", "address_all":
		return true
	default:
		writeAPIError(w, http.StatusBadRequest, "bad_request", "mode must be auto or address_all", nil)
		return false
	}
}

func validateTweakDecision(w http.ResponseWriter, decision string) bool {
	switch strings.ToLower(strings.TrimSpace(decision)) {
	case "commit", "final-review", "skip-review", "restore-from-review", "fail":
		return true
	default:
		writeAPIError(w, http.StatusBadRequest, "bad_request", "decision is invalid", nil)
		return false
	}
}

func validateRefactorRequest(w http.ResponseWriter, req RefactorActionRequest) bool {
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "prompt is required", nil)
		return false
	}
	if len(prompt) > MaxActionTextBytes {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "prompt is too long", nil)
		return false
	}
	if !validateRepoName(w, req.Repo, false) || !validatePipelineProfile(w, req.Pipeline) {
		return false
	}
	return true
}

func validateCleanupRequest(w http.ResponseWriter, req CleanupActionRequest) bool {
	if !validateRepoName(w, req.Repo, false) {
		return false
	}
	if strings.TrimSpace(req.Repo) != "" {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "repo-scoped cleanup is not supported", nil)
		return false
	}
	switch strings.ToLower(strings.TrimSpace(req.Target)) {
	case "", "worktrees", "cycles":
		return true
	default:
		writeAPIError(w, http.StatusBadRequest, "bad_request", "cleanup target is invalid", nil)
		return false
	}
}

func safeActionToken(value string, allowSlash bool) bool {
	if value == "" || len(value) > 200 || strings.Contains(value, "..") || strings.ContainsAny(value, "\\\x00\r\n\t ") {
		return false
	}
	if strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			continue
		}
		switch r {
		case '-', '_', '.':
			continue
		case '/':
			if allowSlash {
				continue
			}
		}
		return false
	}
	return true
}

func (h *apiHandler) requireTrustedMutation(w http.ResponseWriter, r *http.Request) bool {
	if h.mutations == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "unavailable", "mutation service unavailable", nil)
		return false
	}
	if r.Header.Get("X-Agentico-Client") != "local" {
		writeAPIError(w, http.StatusForbidden, "forbidden", "trusted local client header is required", nil)
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" && !isLoopbackOrigin(origin) {
		writeAPIError(w, http.StatusForbidden, "forbidden", "browser origin is not trusted", nil)
		return false
	}
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	if !strings.HasPrefix(ct, "application/json") {
		writeAPIError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "JSON body is required", nil)
		return false
	}
	if r.ContentLength > MaxMutationBodyBytes {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "request_too_large", "mutation body is too large", nil)
		return false
	}
	return true
}

func decodeMutationJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	limited := http.MaxBytesReader(w, r.Body, MaxMutationBodyBytes)
	defer limited.Close()
	dec := json.NewDecoder(limited)
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		status := http.StatusBadRequest
		code := "bad_request"
		message := "invalid JSON request"
		if errors.Is(err, io.ErrUnexpectedEOF) {
			message = "truncated JSON request"
		}
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			status = http.StatusRequestEntityTooLarge
			code = "request_too_large"
			message = "mutation body is too large"
		}
		writeAPIError(w, status, code, message, nil)
		return false
	}
	var extra struct{}
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid JSON request", nil)
		return false
	}
	return true
}

func decodeMutationObject(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, bool) {
	limited := http.MaxBytesReader(w, r.Body, MaxMutationBodyBytes)
	defer limited.Close()
	dec := json.NewDecoder(limited)
	var fields map[string]json.RawMessage
	if err := dec.Decode(&fields); err != nil {
		status := http.StatusBadRequest
		code := "bad_request"
		message := "invalid JSON request"
		if errors.Is(err, io.ErrUnexpectedEOF) {
			message = "truncated JSON request"
		}
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			status = http.StatusRequestEntityTooLarge
			code = "request_too_large"
			message = "mutation body is too large"
		}
		writeAPIError(w, status, code, message, nil)
		return nil, false
	}
	var extra struct{}
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid JSON request", nil)
		return nil, false
	}
	return fields, true
}
