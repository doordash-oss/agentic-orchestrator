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
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/workspace"
)

const MaxMutationBodyBytes = 64 * 1024
const MaxActionTextBytes = 4000

// Stable API codes for publish-time remote branch safety conflicts.
const (
	maxCreationIdempotencyKeyLength = 128
	maxRememberedCreationResults    = 1000
)

// resultCreated is the ActionResult.Result value for a newly created
// feature.
const resultCreated = "created"

// resultAnswered, resultGenerated, resultRecovered, resultRewound,
// resultStarted and resultUpdated are further
// ActionResult/RecoveryActionResponse Result values for
// permission/ask-user answers, generated publish descriptions, and the
// default results of recovery, rewind, start-cycle and update actions.
const (
	resultAnswered     = "answered"
	resultGenerated    = "generated"
	resultRecovered    = "recovered"
	resultRewound      = "rewound"
	resultSetupStarted = "setup_started"
	resultStarted      = "started"
	resultUpdated      = "updated"
)

// decisionAllowOnce, decisionAllowRemember and decisionDeny are the valid
// PermissionAnswerRequest.Decision values. decisionAllowRemember additionally
// requires a RememberScope.
const (
	decisionAllowOnce     = "allow_once"
	decisionAllowRemember = "allow_remember"
	decisionDeny          = "deny"
)

// errMessageInvalidDecision is the bad_request error message returned when
// PermissionAnswerRequest.Decision is not one of the allowed values.
const errMessageInvalidDecision = "decision must be allow_once, allow_remember, or deny"

// targetPhaseImplement, targetPhaseInquire and targetPhasePlan are lowercase
// target_phase values accepted by validatePhaseName, matching
// feature.Phase.DirName().
const (
	targetPhaseImplement = "implement"
	targetPhaseInquire   = "inquire"
	targetPhasePlan      = "plan"
)

// trustedClientHeaderValue is the expected X-Agentico-Client header value
// identifying a trusted local client.
const trustedClientHeaderValue = "local"

// apiPathPermissionsAnswer is the permission-answer mutation route, shared
// between the route matcher and the client request builder.
const apiPathPermissionsAnswer = "/api/v1/permissions/answer"

type MutationTarget interface {
	CreateFeature(CreateFeatureRequest) (CreateFeatureResponse, error)
	// SetupFeature dispatches server-owned durable setup (fresh run or retry
	// of unfinished tasks) without starting orchestration; on success the
	// feature ends in a startable pre-orchestration state.
	SetupFeature(featureID string) (FeatureSetupResponse, error)
	StartFeature(featureID string) (FeatureStartResponse, error)
	ResumeFeature(featureID string) (FeatureStartResponse, error)
	StopFeature(featureID string) (FeatureStopResponse, error)
	RestartFeature(featureID string, req RestartFeatureRequest) (FeatureRestartResponse, error)
	// ReviewDecision applies a review-gate decision; the live caller is the
	// review-session decision endpoint, which uses the request only.
	ReviewDecision(featureID string, req ReviewDecisionRequest) error
	UpdateFeatureConfig(featureID string, req FeatureConfigMutationRequest) (FeatureConfigUpdateResponse, error)
	ResumeNeedUserInput(featureID string, req NeedUserInputResumeRequest) (NeedUserInputResumeResponse, error)
	DraftNeedUserInputAnswers(featureID string, req NeedUserInputDraftRequest) (NeedUserInputDraftResponse, error)
	AnswerPermission(req PermissionAnswerRequest) (PermissionAnswerResponse, error)
	AnswerAskUser(req AskUserAnswerRequest) (AskUserAnswerResponse, error)
	SendHelp(req HelpAnswerRequest) (HelpSendResponse, error)
	// StartChat sends one user turn to the singleton chat session, starting
	// it when none is live. hiddenContext is the server-resolved bundle for
	// the request's context reference: it reaches the provider but never
	// the transcript echo, and is empty when the request carries no
	// reference.
	StartChat(req ChatStartRequest, hiddenContext string) (ChatStartResponse, error)
	EndChat() (ChatEndResponse, error)
	RuntimeConfig(req RuntimeConfigMutationRequest) (RuntimeConfigUpdateResponse, error)
	GeneratePublishDescription(featureID string, req PublishDescriptionRequest) (PublishDescriptionResponse, error)
	PublishFeature(featureID string, req PublishFeatureRequest) (PublishFeatureResponse, error)
	MergeFeature(featureID string, req GuardedFeatureActionRequest) (MergeFeatureResponse, error)
	RewindFeature(featureID string, req RewindFeatureRequest) (RewindFeatureResponse, error)
	RetryFeature(featureID string) (RetryFeatureResponse, error)
	RebaseFeature(featureID string, req RebaseFeatureRequest) (RebaseFeatureResponse, error)
	RefactorFeature(featureID string, req RefactorFeatureRequest) (RefactorFeatureResponse, error)
	ReviewFeedbackFeature(featureID string, req ReviewFeedbackFeatureRequest) (ReviewFeedbackFeatureResponse, error)
	CompletionPreflight(featureID string) (CompletionPreflightResponse, error)
	RepositoryDiff(featureID, repoName, filePath string) (RepositoryDiffResponse, error)
	RepositoryPath(featureID, repoName string) (RepositoryPathResponse, error)
	MarkDone(featureID string, req GuardedFeatureActionRequest) (MarkDoneResponse, error)
	CleanupFeature(featureID string, req CleanupActionRequest) (CleanupFeatureResponse, error)
	DeleteFeature(featureID string, req GuardedFeatureActionRequest) (DeleteFeatureResponse, error)
	DiscardChild(featureID string) (DiscardChildResponse, error)
	ScanRecovery(ctx context.Context) ([]ports.RecoveryItem, error)
	ExecuteRecovery(ctx context.Context, items []ports.RecoveryItem, actions map[string]ports.RecoveryAction) (RecoveryActionResponse, error)
}

// ActionConflictError reports a mutation rejected by conflicting feature
// state. Code selects the catalog entry; Detail becomes the raw diagnostics
// text; Options carry typed summary parameters and context blocks for the
// catalog rendering.
type ActionConflictError struct {
	Err     error
	Code    errcat.Code
	Detail  string
	Options []errcat.Option
}

func (e *ActionConflictError) Error() string {
	if e == nil {
		return ""
	}
	if e.Detail != "" {
		return e.Detail
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return string(errcat.Conflict)
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
	Effort                  config.EffortConfig     `json:"effort,omitempty"`
	ExitCriteria            string                  `json:"exit_criteria,omitempty"`
	Inquireness             string                  `json:"inquireness,omitempty"`
	Images                  []string                `json:"images,omitempty"`
	ImageUploads            []string                `json:"image_uploads,omitempty"`
	UseCurrentBranch        bool                    `json:"use_current_branch,omitempty"`
	UseCurrentBranchPerRepo map[string]bool         `json:"use_current_branch_per_repo,omitempty"`
	Checkpoints             feature.Checkpoints     `json:"checkpoints,omitempty"`
	Attachments             []string                `json:"attachments,omitempty"`
	AttachmentUploads       []string                `json:"attachment_uploads,omitempty"`
	RiskLevel               feature.RiskLevel       `json:"risk_level,omitempty"`
	Pipeline                feature.PipelineProfile `json:"pipeline,omitempty"`
	IdempotencyKey          string                  `json:"idempotency_key,omitempty"`
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
	Models              config.ModelConfig      `json:"models,omitempty"`
	Effort              config.EffortConfig     `json:"effort,omitempty"`
	Inquireness         string                  `json:"inquireness,omitempty"`
	Checkpoints         feature.Checkpoints     `json:"checkpoints,omitempty"`
	Pipeline            feature.PipelineProfile `json:"pipeline,omitempty"`
	InputNotifications  string                  `json:"input_notifications,omitempty"`
	AutomaticReviewMode *string                 `json:"automatic_review_mode,omitempty"`
}

type NeedUserInputResumeRequest struct{}

type NeedUserInputDraftRequest struct {
	Answers map[string]string `json:"answers"`
}

type PermissionAnswerRequest struct {
	RequestID       string  `json:"request_id"`
	SessionID       string  `json:"session_id,omitempty"`
	Decision        string  `json:"decision"`
	RememberPattern string  `json:"remember_pattern,omitempty"`
	RememberScope   *string `json:"remember_scope,omitempty"`
	// AutoApproveScope turns automatic Bash review on for the request's
	// feature ("feature") or the workspace ("workspace") before answering.
	AutoApproveScope string `json:"auto_approve_scope,omitempty"`
}

const (
	AutoApproveScopeFeature   = "feature"
	AutoApproveScopeWorkspace = "workspace"
)

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

type RuntimeConfigMutationRequest struct {
	Defaults       RuntimeDefaultsMutation `json:"defaults,omitempty"`
	WorkspaceRoots *[]string               `json:"workspace_roots,omitempty"`
	Notifications  *NotificationConfig     `json:"notifications,omitempty"`
}

// RuntimeDefaultsMutation is the patch representation of DefaultsConfig.
// Checkpoints is a pointer because its all-false value is a valid update and
// must remain distinguishable from an omitted field. AutomaticReviewEnabled is
// a pointer for the same reason: false is a meaningful value that must remain
// distinguishable from an omitted toggle. Models is a pointer to
// ModelConfigPatch because an all-empty patch is a valid update (e.g. clearing
// an explicit AutomaticReview model back to the meaningful empty "Automatic"
// value). ModelConfigPatch.AutomaticReview is itself a *string so an omitted
// nested property stays distinguishable from an explicit empty value.
type RuntimeDefaultsMutation struct {
	Effort                   config.EffortConfig                  `json:"effort,omitempty"`
	Models                   *ModelConfigPatch                    `json:"models,omitempty"`
	PipelinePreferences      map[string]config.PipelinePreference `json:"pipeline_preferences,omitempty"`
	ExitCriteria             string                               `json:"exit_criteria,omitempty"`
	Inquireness              string                               `json:"inquireness,omitempty"`
	Pipeline                 string                               `json:"pipeline,omitempty"`
	MaxIterations            int                                  `json:"max_iterations,omitempty"`
	MaxConsecutiveFailures   int                                  `json:"max_consecutive_failures,omitempty"`
	MaxConsecutiveNoProgress int                                  `json:"max_consecutive_no_progress,omitempty"`
	MaxPhasePlanIterations   int                                  `json:"max_phase_plan_iterations,omitempty"`
	Checkpoints              *config.Checkpoints                  `json:"checkpoints,omitempty"`
	AutomaticReviewEnabled   *bool                                `json:"automatic_review_enabled,omitempty"`
}

// ModelConfigPatch is the patch representation of config.ModelConfig for
// runtime mutations. Phase-role model fields (Inquiry, Research, etc.) use
// plain strings with empty-meaning-omitted semantics, matching the existing
// mergeModelConfig behavior. AutomaticReview is *string because its empty
// value is meaningful ("Automatic"): nil preserves the existing value while a
// non-nil pointer (including one to "") sets it explicitly.
type ModelConfigPatch struct {
	Inquiry         string  `json:"inquiry,omitempty"`
	Research        string  `json:"research,omitempty"`
	Planning        string  `json:"planning,omitempty"`
	Implementation  string  `json:"implementation,omitempty"`
	Review          string  `json:"review,omitempty"`
	Utilities       string  `json:"utilities,omitempty"`
	KBBuild         string  `json:"kb_build,omitempty"`
	AutomaticReview *string `json:"automatic_review,omitempty"`
}

// ApplyModelConfigPatch applies a ModelConfigPatch to a persisted
// config.ModelConfig. Phase-role model fields use empty-meaning-omitted
// semantics: a non-empty overlay value overwrites the base, an empty value
// preserves the base. AutomaticReview uses *string presence: nil preserves
// the existing value while a non-nil pointer (including one to "") sets it
// explicitly, so a caller that sends only a phase-model change no longer
// silently resets an explicit automatic-review model to "Automatic".
func ApplyModelConfigPatch(base config.ModelConfig, patch ModelConfigPatch) config.ModelConfig {
	if patch.Inquiry != "" {
		base.Inquiry = patch.Inquiry
	}
	if patch.Research != "" {
		base.Research = patch.Research
	}
	if patch.Planning != "" {
		base.Planning = patch.Planning
	}
	if patch.Implementation != "" {
		base.Implementation = patch.Implementation
	}
	if patch.Review != "" {
		base.Review = patch.Review
	}
	if patch.Utilities != "" {
		base.Utilities = patch.Utilities
	}
	if patch.KBBuild != "" {
		base.KBBuild = patch.KBBuild
	}
	if patch.AutomaticReview != nil {
		base.AutomaticReview = *patch.AutomaticReview
	}
	return base
}

// ModelConfigToPatch constructs a non-empty overlay patch from a ModelConfig.
// AutomaticReview stays omitted when empty, matching config overlay semantics:
// a feature-level phase override must not clear the workspace reviewer model.
// Callers that intentionally clear AutomaticReview must construct an explicit
// non-nil pointer to the empty string.
func ModelConfigToPatch(m config.ModelConfig) ModelConfigPatch {
	patch := ModelConfigPatch{
		Inquiry:        m.Inquiry,
		Research:       m.Research,
		Planning:       m.Planning,
		Implementation: m.Implementation,
		Review:         m.Review,
		Utilities:      m.Utilities,
		KBBuild:        m.KBBuild,
	}
	if m.AutomaticReview != "" {
		ar := m.AutomaticReview
		patch.AutomaticReview = &ar
	}
	return patch
}

type PublishFeatureRequest struct {
	SourceRevision string   `json:"source_revision,omitempty"`
	Repos          []string `json:"repos,omitempty"`
	Title          string   `json:"title,omitempty"`
	Body           string   `json:"body,omitempty"`
}

type GuardedFeatureActionRequest struct {
	SourceRevision string `json:"source_revision,omitempty"`
}

type PublishDescriptionRequest struct {
	Repos []string `json:"repos,omitempty"`
}

type RewindFeatureRequest struct {
	TargetPhase     string                  `json:"target_phase"`
	RoadmapPhase    int                     `json:"roadmap_phase,omitempty"`
	UpgradePipeline feature.PipelineProfile `json:"upgrade_pipeline,omitempty"`
	// SourceRunNumber and SourceRevision carry the preview's authoritative
	// source identity. When both are set, execution rejects a stale preview
	// (active run changed or rewind-relevant state advanced) before any
	// side effect. When omitted, the request is treated as unguarded for
	// backward compatibility with older clients.
	SourceRunNumber int    `json:"source_run_number,omitempty"`
	SourceRevision  string `json:"source_revision,omitempty"`
}

type CleanupActionRequest struct {
	SourceRevision string `json:"source_revision,omitempty"`
	Target         string `json:"target,omitempty"`
}

func writeActionJSON(w http.ResponseWriter, status int, resp any) {
	setActionAPIVersion(resp)
	writeJSON(w, status, resp)
}

// setActionAPIVersion defaults the APIVersion field of any action response
// struct via reflection, replacing a per-type switch.
func setActionAPIVersion(resp any) {
	setStringFieldIfEmpty(resp, "APIVersion", APIVersion)
}

// defaultActionFields defaults the FeatureID and Result fields of an action
// response struct, replacing the repeated double-if blocks in the mutation
// route handlers. Types without one of these fields are left untouched.
func defaultActionFields(resp any, featureID, result string) {
	setStringFieldIfEmpty(resp, "FeatureID", featureID)
	setStringFieldIfEmpty(resp, "Result", result)
}

// setStringFieldIfEmpty sets the named string field on resp (a pointer to a
// struct) to value, but only if the field exists, is a string, and is
// currently empty.
func setStringFieldIfEmpty(resp any, name, value string) {
	f := reflect.ValueOf(resp).Elem().FieldByName(name)
	if f.IsValid() && f.Kind() == reflect.String && f.String() == "" {
		f.SetString(value)
	}
}

// deleteActionResponse extends DeleteFeatureResponse with a retry indicator
// so a parked cascade reads as resumable work rather than plain success.
type deleteActionResponse struct {
	DeleteFeatureResponse
	Retryable bool `json:"retryable,omitempty"`
}

func annotateDeleteResponse(r *DeleteFeatureResponse) *deleteActionResponse {
	resp := &deleteActionResponse{DeleteFeatureResponse: *r}
	if r.Status == feature.CascadeDeleteCleanupPending ||
		r.Status == feature.CascadeDeleteAttentionRequired {
		resp.Retryable = true
		if len(resp.Diagnostics) == 0 {
			resp.Diagnostics = []CascadeDiagnostic{{
				Code:    "cascade_" + string(r.Status),
				Message: "cascade delete has not completed; retry delete to resume cleanup",
			}}
		}
	}
	return resp
}

// writeMutationError classifies a mutation failure into a catalog code with
// typed context in one place. An error matching no sentinel maps to the
// cataloged generic bad_request with the raw error text in diagnostics; a
// nil error maps to the fallback internal-error code.
func writeMutationError(w http.ResponseWriter, err error) {
	if err == nil {
		writeAPIError(w, http.StatusInternalServerError, errcat.InternalError)
		return
	}
	if writeChildLaunchError(w, err) {
		return
	}
	if writeRelationshipGuardError(w, err) {
		return
	}
	var conflict *ActionConflictError
	if errors.As(err, &conflict) {
		code := conflict.Code
		if code == "" {
			code = errcat.Conflict
		}
		opts := make([]errcat.Option, 0, 1+len(conflict.Options))
		if detail := conflict.Error(); detail != "" && detail != string(code) {
			opts = append(opts, errcat.WithDiagnostics(detail))
		}
		opts = append(opts, conflict.Options...)
		writeAPIError(w, http.StatusConflict, code, opts...)
		return
	}
	if errors.Is(err, feature.ErrPipelineMismatch) {
		writeAPIError(w, http.StatusConflict, errcat.PipelineMismatch)
		return
	}
	// State-based rejections are conflicts, not malformed input: reporting them
	// as bad_request makes clients ask the user to correct a field they never
	// typed.
	if errors.Is(err, feature.ErrNeedUserInputGateOpen) {
		writeAPIError(w, http.StatusConflict, errcat.NeedUserInputOpen)
		return
	}
	if errors.Is(err, feature.ErrPhaseFinalizing) {
		writeAPIError(w, http.StatusConflict, errcat.PhaseFinalizing)
		return
	}
	if errors.Is(err, feature.ErrInvalidTransition) {
		writeAPIError(w, http.StatusConflict, errcat.InvalidTransition)
		return
	}
	writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics(err.Error()))
}

// writeRelationshipGuardError maps the typed relationship-guard rejections
// to their catalog codes. Returns true when err was a recognized guard
// rejection.
func writeRelationshipGuardError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, feature.ErrChildRelationshipClosed):
		writeAPIError(w, http.StatusConflict, errcat.RelationshipClosed)
	case errors.Is(err, feature.ErrParentMutationLocked):
		writeAPIError(w, http.StatusConflict, errcat.ParentMutationLocked)
	case errors.Is(err, feature.ErrChildMutationRestricted):
		writeAPIError(w, http.StatusConflict, errcat.ChildMutationRestricted)
	case errors.Is(err, feature.ErrCascadeDeleteNotAvailable):
		writeAPIError(w, http.StatusConflict, errcat.CascadeDeleteNotAvailable)
	default:
		return false
	}
	return true
}

// writeChildLaunchError maps the typed child-launch failures of
// feature.CreateRefactorChild and the child-feature execution gate to their
// catalog codes with typed context: dirty parent worktrees and rebase
// repositories populate the repositories block, and the active-child
// rejection names the related features in its summary. It reports whether
// err was a recognized typed failure (and a response was written).
func writeChildLaunchError(w http.ResponseWriter, err error) bool {
	var activeChild *feature.ActiveChildExistsError
	var dirty *feature.ParentWorktreesDirtyError
	var revisionConflict *feature.ReviewFeedbackRevisionConflictError
	var zeroLaunchable *feature.ReviewFeedbackZeroLaunchableSelectionError
	var targetErr *feature.RebaseTargetResolutionError
	var fetchErr *feature.RebaseFetchError
	var upToDate *feature.RebaseAlreadyUpToDateError
	switch {
	case errors.As(err, &activeChild):
		writeAPIError(w, http.StatusConflict, errcat.ActiveChildExists,
			errcat.WithParams(errcat.RelatedFeatureParams{ParentID: activeChild.ParentID, ChildID: activeChild.ChildID}))
	case errors.As(err, &dirty):
		writeAPIError(w, http.StatusConflict, errcat.ParentWorktreesDirty,
			errcat.WithRepositories(dirtyRepoContext(dirty.Repos)...))
	case errors.As(err, &revisionConflict):
		writeAPIError(w, http.StatusConflict, errcat.ReviewFeedbackRevisionConflict,
			errcat.WithDiagnostics(revisionConflict.Error()))
	case errors.As(err, &zeroLaunchable):
		writeAPIError(w, http.StatusBadRequest, errcat.ReviewFeedbackZeroLaunchable,
			errcat.WithDiagnostics(zeroLaunchable.Error()))
	case errors.As(err, &targetErr):
		writeAPIError(w, http.StatusConflict, errcat.RebaseTargetResolutionFailed,
			errcat.WithRepositories(errcat.CodeRepository{Name: targetErr.Repo}),
			errcat.WithDiagnostics(targetErr.Error()))
	case errors.As(err, &fetchErr):
		writeAPIError(w, http.StatusConflict, errcat.RebaseFetchFailed,
			errcat.WithRepositories(errcat.CodeRepository{Name: fetchErr.Repo}),
			errcat.WithDiagnostics(fetchErr.Error()))
	case errors.As(err, &upToDate):
		repos := make([]errcat.CodeRepository, 0, len(upToDate.Targets))
		for _, target := range upToDate.Targets {
			repo := errcat.CodeRepository{Name: target.Repo, Branch: target.Target}
			if target.TargetSHA != "" {
				repo.ExpectedRefSHA = target.TargetSHA
			}
			repos = append(repos, repo)
		}
		writeAPIError(w, http.StatusConflict, errcat.RebaseAlreadyUpToDate,
			errcat.WithRepositories(repos...))
	case errors.Is(err, feature.ErrRefactorParentNotFound):
		writeAPIError(w, http.StatusNotFound, errcat.ParentNotFound,
			errcat.WithDiagnostics(err.Error()))
	case errors.Is(err, feature.ErrRefactorParentIsChild):
		writeAPIError(w, http.StatusConflict, errcat.ParentIsChild)
	case errors.Is(err, feature.ErrRefactorParentStatusIneligible):
		writeAPIError(w, http.StatusConflict, errcat.ParentStatusIneligible)
	case errors.Is(err, feature.ErrReviewFeedbackEmptySelection):
		writeAPIError(w, http.StatusBadRequest, errcat.ReviewFeedbackEmptySelection)
	case errors.Is(err, feature.ErrReviewFeedbackUnsupportedCommentType):
		writeAPIError(w, http.StatusBadRequest, errcat.ReviewFeedbackUnsupportedCommentType)
	case errors.Is(err, feature.ErrReviewFeedbackUnknownRepo):
		writeAPIError(w, http.StatusBadRequest, errcat.ReviewFeedbackUnknownRepo)
	case errors.Is(err, feature.ErrReviewFeedbackRepoHasNoPR):
		writeAPIError(w, http.StatusBadRequest, errcat.ReviewFeedbackRepoHasNoPR)
	case errors.Is(err, feature.ErrReviewFeedbackDraftNotFound):
		writeAPIError(w, http.StatusBadRequest, errcat.ReviewFeedbackDraftNotFound)
	case errors.Is(err, feature.ErrReviewFeedbackUnknownReference):
		writeAPIError(w, http.StatusBadRequest, errcat.ReviewFeedbackUnknownReference)
	case errors.Is(err, feature.ErrChildExecutionBlocked):
		writeAPIError(w, http.StatusConflict, errcat.ChildExecutionBlocked)
	case errors.Is(err, feature.ErrChildExecutionClosed):
		writeAPIError(w, http.StatusConflict, errcat.RelationshipClosed)
	case errors.Is(err, feature.ErrRebaseTargetResolution):
		writeAPIError(w, http.StatusConflict, errcat.RebaseTargetResolutionFailed,
			errcat.WithDiagnostics(err.Error()))
	case errors.Is(err, feature.ErrRebaseFetchFailed):
		writeAPIError(w, http.StatusConflict, errcat.RebaseFetchFailed,
			errcat.WithDiagnostics(err.Error()))
	default:
		return false
	}
	return true
}

// dirtyRepoContext projects the bounded dirty-worktree diagnostics captured
// at launch onto the canonical repositories context block. Dirty file names
// live only here; titles and summaries never carry them.
func dirtyRepoContext(repos []feature.RepoDirtyDiagnostics) []errcat.CodeRepository {
	blocks := make([]errcat.CodeRepository, 0, len(repos))
	for _, repo := range repos {
		dirty := make([]string, 0, len(repo.Staged)+len(repo.Unstaged)+len(repo.Untracked))
		dirty = append(dirty, repo.Staged...)
		dirty = append(dirty, repo.Unstaged...)
		dirty = append(dirty, repo.Untracked...)
		blocks = append(blocks, errcat.CodeRepository{Name: repo.Repo, DirtyFiles: dirty})
	}
	return blocks
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
		writeAPIError(w, http.StatusServiceUnavailable, errcat.Unavailable)
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" || !isLoopbackOrigin(origin) {
		writeAPIError(w, http.StatusForbidden, errcat.Forbidden, errcat.WithDiagnostics("browser origin is not trusted"))
		return true
	}
	requestMethod := strings.ToUpper(strings.TrimSpace(r.Header.Get("Access-Control-Request-Method")))
	if !containsMethod(methods, requestMethod) {
		w.Header().Set("Allow", strings.Join(methods, ", "))
		writeAPIError(w, http.StatusMethodNotAllowed, errcat.MethodNotAllowed)
		return true
	}
	if !isAllowedMutationPreflightHeaders(r.Header.Get("Access-Control-Request-Headers")) {
		writeAPIError(w, http.StatusForbidden, errcat.Forbidden, errcat.WithDiagnostics("mutation preflight headers are not trusted"))
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
		writeAPIError(w, http.StatusForbidden, errcat.Forbidden, errcat.WithDiagnostics("browser origin is not trusted"))
		return true
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	return false
}

func mutationRouteMethods(path string) ([]string, bool) {
	switch path {
	case apiPathFeatures:
		return []string{http.MethodPost}, true
	case apiPathConfigRuntime:
		return []string{http.MethodPatch, http.MethodPut}, true
	case apiPathPermissionsAnswer:
		return []string{http.MethodPost}, true
	case apiPathRecoveryActions:
		return []string{http.MethodPost}, true
	case apiPathReadinessRefresh:
		return []string{http.MethodPost}, true
	case apiPathCatalogRefresh:
		return []string{http.MethodPost}, true
	case apiPathWorkspaceRepositoriesInit:
		return []string{http.MethodPost}, true
	case apiPathUploads:
		return []string{http.MethodPost}, true
	case "/api/v1/prompts/ask-user/answer", "/api/v1/prompts/help/send", "/api/v1/prompts/chat/start", "/api/v1/prompts/chat/end":
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
	case routeSegmentConfig:
		return []string{http.MethodPost}, true
	case "reviews":
		if len(parts) == 2 {
			return []string{http.MethodPost}, true
		}
		if len(parts) == 4 && validEntityID(parts[2]) {
			switch parts[3] {
			case "draft":
				return []string{http.MethodPut}, true
			case "decision":
				return []string{http.MethodPost}, true
			}
		}
	case "actions":
		if len(parts) < 3 || len(parts) > 4 {
			return nil, false
		}
		switch parts[2] {
		case actionSetup, actionStart, actionPauseStop, actionResume, actionRestart, actionPublish, actionMerge, actionRewind, actionRebase, actionRefactor, actionReviewFeedback, actionNeedUserInput, actionNeedInputDraft, actionRetry, actionMarkDone, actionCleanup, actionDelete, actionDiscard:
			if len(parts) == 3 {
				return []string{http.MethodPost}, true
			}
			if parts[2] == actionPublish && parts[3] == phaseNameDescription {
				return []string{http.MethodPost}, true
			}
			if parts[2] == actionReviewFeedback && (parts[3] == reviewFeedbackSubactionFetch || parts[3] == reviewFeedbackSubactionSelection) {
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
		writeAPIError(w, http.StatusMethodNotAllowed, errcat.MethodNotAllowed)
	}
}

func (h *apiHandler) handleCreateFeatureMutation(w http.ResponseWriter, r *http.Request) {
	var req CreateFeatureRequest
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("name is required"))
		return
	}
	if !validatePipelineProfile(w, req.Pipeline) || !validateRiskLevel(w, req.RiskLevel) {
		return
	}
	if !h.validateRequestedModels(w, req.Models) {
		return
	}
	if !validateEffortConfig(w, req.Effort, req.Models, h.registry) {
		return
	}
	if len(req.IdempotencyKey) > maxCreationIdempotencyKeyLength {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
			errcat.WithDiagnostics(fmt.Sprintf("idempotency_key exceeds the %d character limit", maxCreationIdempotencyKeyLength)))
		return
	}
	if !validateCombinedUploadCounts(w, len(req.Images), len(req.ImageUploads), len(req.Attachments), len(req.AttachmentUploads)) {
		return
	}
	if !h.requireTrustedMutation(w, r) {
		return
	}
	if h.rejectNotReadyForCreation(w, r) {
		return
	}
	resp, err := h.createFeatureOnce(req)
	if err != nil {
		writeMutationError(w, err)
		return
	}
	defaultActionFields(&resp, "", resultCreated)
	writeActionJSON(w, http.StatusCreated, &resp)
}

func (h *apiHandler) validateRequestedModels(w http.ResponseWriter, models config.ModelConfig) bool {
	if h.registry == nil {
		return true
	}
	for _, candidate := range []struct {
		phase string
		model string
	}{
		{"inquiry", models.Inquiry},
		{"research", models.Research},
		{"planning", models.Planning},
		{"implementation", models.Implementation},
		{"review", models.Review},
		{"utilities", models.Utilities},
		{"kb_build", models.KBBuild},
	} {
		if strings.TrimSpace(candidate.model) == "" {
			continue
		}
		if _, _, err := h.registry.ResolveModel(candidate.model); err != nil {
			writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
				errcat.WithDiagnostics(fmt.Sprintf("model for %s is unavailable: %s", candidate.phase, candidate.model)))
			return false
		}
	}
	return true
}

func (h *apiHandler) createFeatureOnce(req CreateFeatureRequest) (CreateFeatureResponse, error) {
	if req.IdempotencyKey == "" {
		return h.createFeatureWithUploads(req)
	}
	payload, _ := json.Marshal(req)
	fingerprint := string(payload)
	h.creationMu.Lock()
	defer h.creationMu.Unlock()
	if prior, ok := h.creationResults[req.IdempotencyKey]; ok {
		if prior.fingerprint != fingerprint {
			return CreateFeatureResponse{}, &ActionConflictError{Detail: "idempotency key was already used for different creation input"}
		}
		return prior.response, nil
	}
	resp, err := h.createFeatureWithUploads(req)
	if err == nil {
		if len(h.creationResults) >= maxRememberedCreationResults {
			// Bound process memory at the cost of forgetting older retry identities.
			clear(h.creationResults)
		}
		h.creationResults[req.IdempotencyKey] = creationResult{fingerprint: fingerprint, response: resp}
	}
	return resp, err
}

// createFeatureWithUploads resolves staged upload references into durable
// handoff copies and merges those paths into the local-path inputs the
// existing setup pipeline consumes. Consumption is two-phase so a failed
// creation leaves every staged file intact for retry: the handoff copies are
// rolled back on error, and the staged sources are deleted (single-use) only
// after the mutation succeeds.
func (h *apiHandler) createFeatureWithUploads(req CreateFeatureRequest) (CreateFeatureResponse, error) {
	consumed, err := h.consumeUploadRefs(req.ImageUploads, req.AttachmentUploads, "")
	if err != nil {
		return CreateFeatureResponse{}, err
	}
	if consumed != nil {
		req.Images = append(req.Images, consumed.imagePaths...)
		req.Attachments = append(req.Attachments, consumed.attachmentPaths...)
	}
	resp, err := h.mutations.CreateFeature(req)
	if err != nil {
		consumed.rollback() // nil-safe: nothing to roll back without refs
	} else {
		consumed.commit()
	}
	return resp, err
}

func (h *apiHandler) handleFeatureMutationRoute(w http.ResponseWriter, r *http.Request, featureID string, parts []string) bool {
	if r.Method != http.MethodPost {
		return false
	}
	if len(parts) > 0 && parts[0] == "actions" {
		return h.handleFeatureActionRoute(w, r, featureID, parts[1:])
	}
	if len(parts) != 1 || parts[0] != routeSegmentConfig {
		return false
	}
	var req FeatureConfigMutationRequest
	if !h.requireTrustedMutation(w, r) || !decodeMutationJSON(w, r, &req) {
		return true
	}
	if !validatePipelineProfile(w, req.Pipeline) {
		return true
	}
	if !validateAutomaticReviewMode(w, req.AutomaticReviewMode) {
		return true
	}
	if !validateEffortConfig(w, req.Effort, req.Models, h.registry) {
		return true
	}
	resp, err := h.mutations.UpdateFeatureConfig(featureID, req)
	if err != nil {
		writeMutationError(w, err)
		return true
	}
	defaultActionFields(&resp, featureID, resultUpdated)
	writeActionJSON(w, http.StatusOK, &resp)
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
	case actionSetup:
		if subaction != "" {
			return false
		}
		var req map[string]any
		if !decodeMutationJSON(w, r, &req) {
			return true
		}
		resp, err := h.mutations.SetupFeature(featureID)
		if err != nil {
			writeMutationError(w, err)
			return true
		}
		defaultActionFields(&resp, featureID, resultSetupStarted)
		writeActionJSON(w, http.StatusOK, &resp)
	case actionStart:
		if subaction != "" {
			return false
		}
		h.handleStartFeatureMutationTrusted(w, r, featureID)
	case actionResume:
		if subaction != "" {
			return false
		}
		h.handleResumeFeatureMutationTrusted(w, r, featureID)
	case actionPauseStop:
		if subaction != "" {
			return false
		}
		h.handleStopFeatureMutationTrusted(w, r, featureID)
	case actionRestart:
		if subaction != "" {
			return false
		}
		var req RestartFeatureRequest
		if !decodeMutationJSON(w, r, &req) {
			return true
		}
		h.writeRestartFeature(w, featureID, req)
	case actionNeedUserInput:
		if subaction != "" {
			return false
		}
		var req NeedUserInputResumeRequest
		if !decodeMutationJSON(w, r, &req) {
			return true
		}
		resp, err := h.mutations.ResumeNeedUserInput(featureID, req)
		if err != nil {
			writeMutationError(w, err)
			return true
		}
		defaultActionFields(&resp, featureID, "resumed")
		writeActionJSON(w, http.StatusOK, &resp)
	case actionNeedInputDraft:
		if subaction != "" {
			return false
		}
		var req NeedUserInputDraftRequest
		if !decodeMutationJSON(w, r, &req) {
			return true
		}
		if len(req.Answers) == 0 {
			writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("answers are required"))
			return true
		}
		resp, err := h.mutations.DraftNeedUserInputAnswers(featureID, req)
		if err != nil {
			writeMutationError(w, err)
			return true
		}
		defaultActionFields(&resp, featureID, "drafted")
		writeActionJSON(w, http.StatusOK, &resp)
	case actionPublish:
		if subaction == phaseNameDescription {
			var req PublishDescriptionRequest
			if !decodeMutationJSON(w, r, &req) || !validateRepoList(w, req.Repos, false) {
				return true
			}
			resp, err := h.mutations.GeneratePublishDescription(featureID, req)
			if err != nil {
				writeMutationError(w, err)
				return true
			}
			defaultActionFields(&resp, featureID, resultGenerated)
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
		defaultActionFields(&resp, featureID, "published")
		writeActionJSON(w, http.StatusOK, &resp)
	case actionMerge, actionMarkDone, actionDelete:
		if subaction != "" {
			return false
		}
		var req GuardedFeatureActionRequest
		if !decodeMutationJSON(w, r, &req) {
			return true
		}
		var (
			resp          any
			err           error
			defaultResult string
		)
		switch action {
		case actionMerge:
			r, mergeErr := h.mutations.MergeFeature(featureID, req)
			resp, err, defaultResult = &r, mergeErr, "merged"
		case actionMarkDone:
			r, markDoneErr := h.mutations.MarkDone(featureID, req)
			resp, err, defaultResult = &r, markDoneErr, "done"
		case actionDelete:
			r, deleteErr := h.mutations.DeleteFeature(featureID, req)
			resp, err, defaultResult = annotateDeleteResponse(&r), deleteErr, "deleted"
		}
		if err != nil {
			writeMutationError(w, err)
			return true
		}
		defaultActionFields(resp, featureID, defaultResult)
		writeActionJSON(w, http.StatusOK, resp)
	case actionRetry:
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
		defaultActionFields(&resp, featureID, "retried")
		writeActionJSON(w, http.StatusOK, &resp)
	case actionRewind:
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
		defaultActionFields(&resp, featureID, resultRewound)
		writeActionJSON(w, http.StatusOK, &resp)
	case actionRebase:
		if subaction != "" {
			return false
		}
		var req map[string]any
		if !decodeMutationJSON(w, r, &req) {
			return true
		}
		resp, err := h.mutations.RebaseFeature(featureID, RebaseFeatureRequest{})
		if err != nil {
			writeMutationError(w, err)
			return true
		}
		defaultActionFields(&resp, "", resultCreated)
		writeActionJSON(w, http.StatusCreated, &resp)
	case actionRefactor:
		if subaction != "" {
			return false
		}
		h.handleRefactorFeatureMutationTrusted(w, r, featureID)
	case actionReviewFeedback:
		switch subaction {
		case "":
			h.handleReviewFeedbackFeatureMutationTrusted(w, r, featureID)
		case reviewFeedbackSubactionFetch:
			h.handleReviewFeedbackFetchTrusted(w, r, featureID)
		case reviewFeedbackSubactionSelection:
			h.handleReviewFeedbackSelectionTrusted(w, r, featureID)
		default:
			return false
		}
	case actionCleanup:
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
		defaultActionFields(&resp, featureID, "cleaned")
		writeActionJSON(w, http.StatusOK, &resp)
	case actionDiscard:
		if subaction != "" {
			return false
		}
		var req map[string]any
		if !decodeMutationJSON(w, r, &req) {
			return true
		}
		resp, err := h.mutations.DiscardChild(featureID)
		if err != nil {
			writeMutationError(w, err)
			return true
		}
		defaultActionFields(&resp, featureID, "discarded")
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
		models := h.configOrDefault().Defaults.Models
		if req.Defaults.Models != nil {
			models = ApplyModelConfigPatch(models, *req.Defaults.Models)
		}
		if !validateEffortConfig(w, req.Defaults.Effort, models, h.registry) {
			return
		}
		if req.WorkspaceRoots != nil && !validateWorkspaceRootPaths(w, *req.WorkspaceRoots) {
			return
		}
		resp, err := h.mutations.RuntimeConfig(req)
		if err != nil {
			writeMutationError(w, err)
			return
		}
		defaultActionFields(&resp, "", resultUpdated)
		writeActionJSON(w, http.StatusOK, &resp)
	default:
		w.Header().Set("Allow", "GET, PATCH, PUT")
		writeAPIError(w, http.StatusMethodNotAllowed, errcat.MethodNotAllowed)
	}
}

// validateWorkspaceRootPaths rejects runtime-config workspace roots that do
// not resolve to real directories on the server's filesystem, using the same
// resolution conventions as the workspace catalog and readiness surface
// (home expansion, symlinks followed by os.Stat). Every rejected path is
// reported with its reason so the client can fix the whole list in one
// round-trip; nothing is persisted when any root is invalid.
func validateWorkspaceRootPaths(w http.ResponseWriter, roots []string) bool {
	type rejectedRoot struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
	}
	var invalid []rejectedRoot
	for _, root := range roots {
		info, err := os.Stat(workspace.ExpandHome(root))
		switch {
		case err == nil && info.IsDir():
			// Valid root.
		case err == nil:
			invalid = append(invalid, rejectedRoot{Path: root, Reason: "path is not a directory"})
		case errors.Is(err, fs.ErrNotExist):
			invalid = append(invalid, rejectedRoot{Path: root, Reason: "path does not exist"})
		default:
			invalid = append(invalid, rejectedRoot{Path: root, Reason: "path could not be resolved"})
		}
	}
	if len(invalid) == 0 {
		return true
	}
	invalidParams := make([]errcat.InvalidPath, 0, len(invalid))
	reasons := make([]string, 0, len(invalid))
	for _, entry := range invalid {
		invalidParams = append(invalidParams, errcat.InvalidPath{Path: entry.Path, Reason: entry.Reason})
		reasons = append(reasons, fmt.Sprintf("%s (%s)", entry.Path, entry.Reason))
	}
	writeAPIError(w, http.StatusBadRequest, errcat.InvalidWorkspaceRoot,
		errcat.WithParams(errcat.WorkspaceRootParams{Paths: invalidParams}),
		errcat.WithDiagnostics("rejected workspace roots: "+strings.Join(reasons, "; ")))
	return false
}

func (h *apiHandler) handlePermissionMutationRoutes(w http.ResponseWriter, r *http.Request) {
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/api/v1/permissions/"))
	if r.Method != http.MethodPost || len(parts) != 1 || parts[0] != "answer" {
		w.Header().Set("Allow", "POST")
		writeAPIError(w, http.StatusNotFound, errcat.NotFound, errcat.WithParams(errcat.SubjectParams{Subject: "Endpoint"}))
		return
	}
	if !h.requireTrustedMutation(w, r) {
		return
	}
	var req PermissionAnswerRequest
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.RequestID) == "" {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("request_id is required"))
		return
	}
	switch req.Decision {
	case decisionAllowOnce, decisionAllowRemember, decisionDeny:
	default:
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics(errMessageInvalidDecision))
		return
	}
	if req.Decision == decisionAllowRemember && req.RememberScope == nil {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("remember_scope is required for allow_remember"))
		return
	}
	switch req.AutoApproveScope {
	case "", AutoApproveScopeFeature, AutoApproveScopeWorkspace:
	default:
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("auto_approve_scope must be feature or workspace"))
		return
	}
	if req.AutoApproveScope != "" && req.Decision == decisionDeny {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("auto_approve_scope cannot be combined with deny"))
		return
	}
	resp, err := h.mutations.AnswerPermission(req)
	if err != nil {
		writeMutationError(w, err)
		return
	}
	defaultActionFields(&resp, "", resultAnswered)
	writeActionJSON(w, http.StatusOK, &resp)
}

func (h *apiHandler) handlePromptMutationRoutes(w http.ResponseWriter, r *http.Request) {
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/api/v1/prompts/"))
	if r.Method != http.MethodPost || len(parts) != 2 {
		w.Header().Set("Allow", "POST")
		writeAPIError(w, http.StatusNotFound, errcat.NotFound, errcat.WithParams(errcat.SubjectParams{Subject: "Endpoint"}))
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
			writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("request_id is required"))
			return
		}
		if len(req.Answers) == 0 {
			writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("answers are required"))
			return
		}
		resp, err := h.mutations.AnswerAskUser(req)
		if err != nil {
			writeMutationError(w, err)
			return
		}
		defaultActionFields(&resp, "", resultAnswered)
		writeActionJSON(w, http.StatusOK, &resp)
	case "help/send":
		var req HelpAnswerRequest
		if !decodeMutationJSON(w, r, &req) {
			return
		}
		if strings.TrimSpace(req.Message) == "" {
			writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("message is required"))
			return
		}
		if strings.TrimSpace(req.SessionID) == "" && strings.TrimSpace(req.FeatureID) == "" {
			writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("session_id or feature_id is required"))
			return
		}
		resp, err := h.mutations.SendHelp(req)
		if err != nil {
			writeMutationError(w, err)
			return
		}
		defaultActionFields(&resp, "", "sent")
		writeActionJSON(w, http.StatusOK, &resp)
	case "chat/start":
		var req ChatStartRequest
		if !decodeMutationJSON(w, r, &req) {
			return
		}
		if strings.TrimSpace(req.Message) == "" {
			writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("message is required"))
			return
		}
		// A malformed context reference is rejected before uploads are
		// consumed, so nothing is staged for a turn that never starts.
		if field := validateChatContextReference(req.Context); field != "" {
			writeChatContextInvalid(w, req.Context, field)
			return
		}
		// The reference resolves against durable state before the mutation
		// target runs; a stale or mismatched home rejects the turn the same
		// way. The bundle crosses to the target separately from the visible
		// message and never enters the response body.
		hiddenContext := ""
		if !chatContextAbsent(req.Context) {
			bundle, rejection := h.resolveChatContext(req.Context)
			if rejection != nil {
				rejection.write(w, req.Context)
				return
			}
			hiddenContext = bundle
		}
		if !validateCombinedUploadCounts(w, len(req.Images), len(req.ImageUploads), 0, 0) {
			return
		}
		// Chat resolves staged image references by copying them into the chat
		// session directory; the copied paths are what the prompt embeds.
		chatDir := ""
		if h.uploads != nil {
			chatDir = filepath.Join(h.runtime.StateDir, uploadChatDirName)
		}
		consumed, err := h.consumeUploadRefs(req.ImageUploads, nil, chatDir)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics(err.Error()))
			return
		}
		if consumed != nil {
			req.Images = append(req.Images, consumed.imagePaths...)
		}
		resp, err := h.mutations.StartChat(req, hiddenContext)
		if err != nil {
			consumed.rollback() // nil-safe: nothing to roll back without refs
			writeMutationError(w, err)
			return
		}
		consumed.commit()
		defaultActionFields(&resp, "", resultStarted)
		writeActionJSON(w, http.StatusOK, &resp)
	case "chat/end":
		var req map[string]any
		if !decodeMutationJSON(w, r, &req) {
			return
		}
		resp, err := h.mutations.EndChat()
		if err != nil {
			writeMutationError(w, err)
			return
		}
		defaultActionFields(&resp, "", "ended")
		writeActionJSON(w, http.StatusOK, &resp)
	default:
		writeAPIError(w, http.StatusNotFound, errcat.NotFound, errcat.WithParams(errcat.SubjectParams{Subject: "Endpoint"}))
	}
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
	defaultActionFields(&resp, featureID, resultStarted)
	writeActionJSON(w, http.StatusOK, &resp)
}

func (h *apiHandler) handleResumeFeatureMutationTrusted(w http.ResponseWriter, r *http.Request, featureID string) {
	var req map[string]any
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	resp, err := h.mutations.ResumeFeature(featureID)
	if err != nil {
		writeMutationError(w, err)
		return
	}
	defaultActionFields(&resp, featureID, resultStarted)
	writeActionJSON(w, http.StatusOK, &resp)
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
	defaultActionFields(&resp, featureID, "stopped")
	writeActionJSON(w, http.StatusOK, &resp)
}

// handleRefactorFeatureMutationTrusted launches a refactor child under the
// given parent. Unlike the other actions it returns 201 with the new child;
// the mutation target runs child setup asynchronously after creation is
// durable.
func (h *apiHandler) handleRefactorFeatureMutationTrusted(w http.ResponseWriter, r *http.Request, featureID string) {
	var req RefactorFeatureRequest
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("name is required"))
		return
	}
	if !validatePipelineProfile(w, req.Pipeline) || !validateRiskLevel(w, req.RiskLevel) || !validateInquireness(w, req.Inquireness) {
		return
	}
	if !validateEffortConfig(w, req.Effort, req.Models, h.registry) {
		return
	}
	if !validateCombinedUploadCounts(w, len(req.Images), len(req.ImageUploads), len(req.Attachments), len(req.AttachmentUploads)) {
		return
	}
	consumed, err := h.consumeUploadRefs(req.ImageUploads, req.AttachmentUploads, "")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics(err.Error()))
		return
	}
	if consumed != nil {
		req.Images = append(req.Images, consumed.imagePaths...)
		req.Attachments = append(req.Attachments, consumed.attachmentPaths...)
	}
	resp, err := h.mutations.RefactorFeature(featureID, req)
	if err != nil {
		consumed.rollback() // nil-safe: nothing to roll back without refs
		writeMutationError(w, err)
		return
	}
	consumed.commit()
	defaultActionFields(&resp, "", resultCreated)
	writeActionJSON(w, http.StatusCreated, &resp)
}

func (h *apiHandler) handleReviewFeedbackFeatureMutationTrusted(w http.ResponseWriter, r *http.Request, featureID string) {
	var req ReviewFeedbackFeatureRequest
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	resp, err := h.mutations.ReviewFeedbackFeature(featureID, req)
	if err != nil {
		writeMutationError(w, err)
		return
	}
	defaultActionFields(&resp, "", resultCreated)
	writeActionJSON(w, http.StatusCreated, &resp)
}

func (h *apiHandler) writeRestartFeature(w http.ResponseWriter, featureID string, req RestartFeatureRequest) {
	resp, err := h.mutations.RestartFeature(featureID, req)
	if err != nil {
		writeMutationError(w, err)
		return
	}
	defaultActionFields(&resp, featureID, "restarted")
	writeActionJSON(w, http.StatusOK, &resp)
}

func validatePipelineProfile(w http.ResponseWriter, profile feature.PipelineProfile) bool {
	if profile == "" || profile.IsValid() {
		return true
	}
	writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("pipeline must be medium, large, or moonshot"))
	return false
}

func validateRiskLevel(w http.ResponseWriter, risk feature.RiskLevel) bool {
	switch risk {
	case "", feature.RiskLow, feature.RiskMedium, feature.RiskHigh:
		return true
	default:
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("risk_level must be low, medium, or high"))
		return false
	}
}

func validateInquireness(w http.ResponseWriter, inq feature.Inquireness) bool {
	switch inq {
	case "", feature.InquirenessNone, feature.InquirenessMedium, feature.InquirenessHigh:
		return true
	default:
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("inquireness must be none, medium, or high"))
		return false
	}
}

// RefactorChildSpecFromRequest maps a typed refactor launch request to the
// domain launch brief. This is the single request→spec mapping: the
// production mutation target and tests share it so no request field (Review
// configuration, copied inputs) can silently drift out of one path. Staged
// upload references (image_uploads/attachment_uploads) never reach this
// mapper: the route handler resolves them to durable local paths and merges
// them into Images/Attachments first.
func RefactorChildSpecFromRequest(req RefactorFeatureRequest) (feature.RefactorChildSpec, error) {
	spec := feature.RefactorChildSpec{
		Name:         req.Name,
		Description:  req.Description,
		Images:       req.Images,
		Attachments:  req.Attachments,
		Checkpoints:  req.Checkpoints,
		Effort:       req.Effort,
		Models:       req.Models,
		RiskLevel:    req.RiskLevel,
		ExitCriteria: req.ExitCriteria,
		Inquireness:  req.Inquireness,
	}
	if req.Pipeline != "" {
		pipeline, err := feature.ParsePipelineProfile(string(req.Pipeline))
		if err != nil {
			return spec, err
		}
		spec.Pipeline = pipeline
	}
	return spec, nil
}

// ReviewFeedbackGateFromRequest maps the optional gate presence of the
// constant-size launch request onto the pointer the domain launch expects.
// The comment payloads no longer travel with the request: launch reconciles
// the committed pending draft against current server-resolved GitHub data.
func ReviewFeedbackGateFromRequest(req ReviewFeedbackFeatureRequest) *bool {
	return req.Gate
}

// RebaseChildSpecFromRequest is the zero-input mapper for rebase child
// launches. The rebase action takes no user input; the orchestrator preflight
// resolves targets and computes behind-ness before child creation. The mapper
// exists for structural consistency with the other child-launch actions.
func RebaseChildSpecFromRequest(_ RebaseFeatureRequest) feature.RebaseChildSpec {
	return feature.RebaseChildSpec{}
}

func validateAutomaticReviewMode(w http.ResponseWriter, raw *string) bool {
	if raw == nil {
		return true
	}
	if _, err := feature.ParseAutomaticReviewMode(*raw); err == nil {
		return true
	}
	writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("automatic_review_mode must be default, enabled, or disabled"))
	return false
}

func validateEffortConfig(w http.ResponseWriter, effort config.EffortConfig, models config.ModelConfig, reg *llm.Registry) bool {
	roles := []struct {
		val   string
		model string
		label string
	}{
		{effort.Inquiry, models.Inquiry, "inquiry"},
		{effort.Research, models.Research, "research"},
		{effort.Planning, models.Planning, "planning"},
		{effort.Implementation, models.Implementation, "implementation"},
		{effort.Review, models.Review, "review"},
		{effort.Utilities, models.Utilities, "utilities"},
		{effort.KBBuild, models.KBBuild, "kb_build"},
	}
	for _, r := range roles {
		if r.val == "" {
			continue
		}
		if !llm.IsValidExplicitEffort(llm.EffortLevel(r.val)) {
			writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
				errcat.WithDiagnostics("effort."+r.label+" must be one of: auto, low, medium, high, xhigh, max"))
			return false
		}
		if r.val == "auto" {
			continue
		}
		if reg == nil || r.model == "" {
			continue
		}
		prov, resolvedModel, err := reg.ResolveModel(r.model)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
				errcat.WithDiagnostics("effort."+r.label+" value "+r.val+" cannot be verified: "+r.label+" model "+r.model+" not found in registry"))
			return false
		}
		caps := llm.EffortCapabilitiesForModel(prov, resolvedModel)
		if len(caps) == 0 || !llm.EffortCapabilitySupported(caps, llm.EffortLevel(r.val)) {
			writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
				errcat.WithDiagnostics("effort."+r.label+" value "+r.val+" is not supported by the selected "+r.label+" model"))
			return false
		}
	}
	return true
}

func validateRepoList(w http.ResponseWriter, repos []string, required bool) bool {
	if required && len(repos) == 0 {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("repos are required"))
		return false
	}
	if len(repos) > 50 {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("too many repos"))
		return false
	}
	for _, repo := range repos {
		if !validateRepoName(w, repo, true) {
			return false
		}
	}
	return true
}

func validateRepoName(w http.ResponseWriter, repo string, required bool) bool {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		if required {
			writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("repo is required"))
			return false
		}
		return true
	}
	if !safeActionToken(repo, false) {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("repo has invalid characters"))
		return false
	}
	return true
}

func validatePhaseName(w http.ResponseWriter, phase string) bool {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "knowledge-base", "knowledgebase", targetPhaseInquire, "research", "design", targetPhasePlan, targetPhaseImplement, "review", "final-review", actionPublish:
		return true
	default:
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("target_phase is invalid"))
		return false
	}
}

func validatePositiveOptionalInt(w http.ResponseWriter, field string, value int) bool {
	if value >= 0 {
		return true
	}
	writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics(field+" must be positive"))
	return false
}

func validateCleanupRequest(w http.ResponseWriter, req CleanupActionRequest) bool {
	switch strings.ToLower(strings.TrimSpace(req.Target)) {
	case "", "worktrees":
		return true
	default:
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("cleanup target is invalid"))
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
		writeAPIError(w, http.StatusServiceUnavailable, errcat.Unavailable)
		return false
	}
	if r.Header.Get("X-Agentico-Client") != trustedClientHeaderValue {
		writeAPIError(w, http.StatusForbidden, errcat.Forbidden, errcat.WithDiagnostics("trusted local client header is required"))
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" && !isLoopbackOrigin(origin) {
		writeAPIError(w, http.StatusForbidden, errcat.Forbidden, errcat.WithDiagnostics("browser origin is not trusted"))
		return false
	}
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	if !strings.HasPrefix(ct, "application/json") {
		writeAPIError(w, http.StatusUnsupportedMediaType, errcat.UnsupportedMediaType, errcat.WithDiagnostics("JSON body is required"))
		return false
	}
	if r.ContentLength > MaxMutationBodyBytes {
		writeAPIError(w, http.StatusRequestEntityTooLarge, errcat.RequestTooLarge, errcat.WithDiagnostics("mutation body is too large"))
		return false
	}
	return true
}

// classifyDecodeError maps a JSON decode error to the transport status,
// catalog code, and raw diagnostics used by decodeMutationJSON.
func classifyDecodeError(err error) (status int, code errcat.Code, diagnostics string) {
	status = http.StatusBadRequest
	code = errcat.BadRequest
	diagnostics = "invalid JSON request"
	if errors.Is(err, io.ErrUnexpectedEOF) {
		diagnostics = "truncated JSON request"
	}
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		status = http.StatusRequestEntityTooLarge
		code = errcat.RequestTooLarge
		diagnostics = "mutation body is too large"
	}
	return status, code, diagnostics
}

func decodeMutationJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	limited := http.MaxBytesReader(w, r.Body, MaxMutationBodyBytes)
	defer limited.Close()
	dec := json.NewDecoder(limited)
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		status, code, diagnostics := classifyDecodeError(err)
		writeAPIError(w, status, code, errcat.WithDiagnostics(diagnostics))
		return false
	}
	var extra struct{}
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("invalid JSON request"))
		return false
	}
	return true
}
