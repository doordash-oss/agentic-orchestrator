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
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const MaxMutationBodyBytes = 64 * 1024
const MaxActionTextBytes = 4000

type MutationLimits struct {
	FeatureQueue   int
	RuntimeQueue   int
	AggregateQueue int
}

type MutationTarget interface {
	CreateFeature(CreateFeatureRequest) (OperationResult, error)
	StartFeature(featureID string) (OperationResult, error)
	StopFeature(featureID string) (OperationResult, error)
	RestartFeature(featureID string, req RestartFeatureRequest) (OperationResult, error)
	ReviewDecision(featureID string, req ReviewDecisionRequest) (OperationResult, error)
	UpdateFeatureConfig(featureID string, req FeatureConfigMutationRequest) (OperationResult, error)
	NeedUserInputDecision(featureID string, req NeedUserInputDecisionRequest) (OperationResult, error)
	DraftNeedUserInputAnswers(featureID string, req NeedUserInputDraftRequest) (OperationResult, error)
	AnswerPermission(req PermissionAnswerRequest) (OperationResult, error)
	AnswerAskUser(req AskUserAnswerRequest) (OperationResult, error)
	SendHelp(req HelpAnswerRequest) (OperationResult, error)
	RuntimeConfig(req RuntimeConfigMutationRequest) (OperationResult, error)
	PublishFeature(featureID string, req PublishFeatureRequest) (OperationResult, error)
	MergeFeature(featureID string) (OperationResult, error)
	RewindFeature(featureID string, req RewindFeatureRequest) (OperationResult, error)
	RetryFeature(featureID string) (OperationResult, error)
	StartRebase(featureID string, req RebaseActionRequest) (OperationResult, error)
	FetchReviewComments(featureID string, req ReviewCommentsFetchRequest) (ReviewCommentsFetchResponse, error)
	StartReviewComments(featureID string, req ReviewCommentsActionRequest) (OperationResult, error)
	StartTweak(featureID string, req TweakActionRequest) (OperationResult, error)
	FinishTweak(featureID string, req TweakFinishRequest) (OperationResult, error)
	StartRefactor(featureID string, req RefactorActionRequest) (OperationResult, error)
	RestartRefactor(featureID string, req RefactorActionRequest) (OperationResult, error)
	MarkDone(featureID string) (OperationResult, error)
	CleanupFeature(featureID string, req CleanupActionRequest) (OperationResult, error)
	DeleteFeature(featureID string) (OperationResult, error)
	ScanRecovery(ctx context.Context) ([]ports.RecoveryItem, error)
	ExecuteRecovery(ctx context.Context, items []ports.RecoveryItem, actions map[string]ports.RecoveryAction) (OperationResult, error)
}

type OperationResult struct {
	Metadata map[string]string
}

type OperationFailureError struct {
	Err     error
	Failure *OperationError
}

func (e OperationFailureError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	if e.Failure != nil {
		return e.Failure.Message
	}
	return "operation failed"
}

func (e OperationFailureError) Unwrap() error {
	return e.Err
}

func (e OperationFailureError) OperationFailure() *OperationError {
	return e.Failure
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

type RuntimeConfigMutationRequest struct {
	Defaults config.DefaultsConfig `json:"defaults,omitempty"`
}

type PublishFeatureRequest struct {
	Repos []string `json:"repos,omitempty"`
}

type RewindFeatureRequest struct {
	TargetPhase  string `json:"target_phase"`
	RoadmapPhase int    `json:"roadmap_phase,omitempty"`
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
	Repo string `json:"repo"`
	Mode string `json:"mode"`
}

type TweakActionRequest struct{}

type TweakFinishRequest struct {
	Decision   string `json:"decision"`
	HadChanges bool   `json:"had_changes,omitempty"`
}

type RefactorActionRequest struct {
	Repo     string                  `json:"repo,omitempty"`
	Prompt   string                  `json:"prompt"`
	Pipeline feature.PipelineProfile `json:"pipeline,omitempty"`
}

type CleanupActionRequest struct {
	Target string `json:"target,omitempty"`
	Repo   string `json:"repo,omitempty"`
}

type mutationExecutor struct {
	mu        sync.Mutex
	registry  *OperationRegistry
	target    MutationTarget
	limits    MutationLimits
	publish   func(OperationRecord)
	active    map[string]string
	activeAll int
	queued    map[string][]queuedMutation
	queuedAll int
	closing   bool
}

type queuedMutation struct {
	record OperationRecord
	work   func() (OperationResult, error)
}

type mutationAdmission struct {
	record OperationRecord
	status int
	err    *OperationError
}

func newMutationExecutor(registry *OperationRegistry, target MutationTarget, limits MutationLimits, publish func(OperationRecord)) *mutationExecutor {
	if limits.AggregateQueue <= 0 {
		limits.AggregateQueue = 100
	}
	return &mutationExecutor{
		registry: registry,
		target:   target,
		limits:   limits,
		publish:  publish,
		active:   map[string]string{},
		queued:   map[string][]queuedMutation{},
	}
}

func (e *mutationExecutor) admit(kind string, target OperationTarget, work func() (OperationResult, error)) (mutationAdmission, bool) {
	if e == nil || e.registry == nil || e.target == nil {
		return mutationAdmission{status: http.StatusServiceUnavailable, err: &OperationError{Code: "unavailable", Message: "mutation service unavailable"}}, false
	}
	lane := mutationLane(kind, target)
	e.mu.Lock()
	if e.closing {
		e.mu.Unlock()
		return mutationAdmission{status: http.StatusServiceUnavailable, err: &OperationError{Code: "shutdown", Message: "server is shutting down"}}, false
	}
	if activeID := e.active[lane]; activeID != "" {
		laneLimit := e.laneQueueLimit(lane)
		if laneLimit <= 0 {
			rec, err := e.registry.Create(kind, target)
			if err != nil {
				e.mu.Unlock()
				return mutationAdmission{status: http.StatusInternalServerError, err: &OperationError{Code: "failed", Message: "operation failed"}}, false
			}
			_ = activeID
			opErr := &OperationError{Code: "conflict", Message: "feature operation already active"}
			_ = e.registry.Complete(rec.ID, OperationStatusRejected, nil, opErr)
			page, _ := e.registry.List(OperationListOptions{Limit: 1})
			if len(page.Operations) == 1 {
				rec.Status = page.Operations[0].Status
				rec.Error = page.Operations[0].Error
				rec.CompletedAt = page.Operations[0].CompletedAt
			}
			e.mu.Unlock()
			e.publishRecordID(rec.ID)
			return mutationAdmission{record: rec, status: http.StatusConflict, err: opErr}, false
		}
		if len(e.queued[lane]) >= laneLimit || e.activeAll+e.queuedAll >= e.limits.AggregateQueue {
			e.mu.Unlock()
			return mutationAdmission{status: http.StatusTooManyRequests, err: &OperationError{Code: "backpressure", Message: "operation queue is full"}}, false
		}
		rec, err := e.registry.Create(kind, target)
		if err != nil {
			e.mu.Unlock()
			return mutationAdmission{status: http.StatusInternalServerError, err: &OperationError{Code: "failed", Message: "operation failed"}}, false
		}
		e.queued[lane] = append(e.queued[lane], queuedMutation{record: rec, work: work})
		e.queuedAll++
		e.mu.Unlock()
		return mutationAdmission{record: rec, status: http.StatusAccepted}, true
	}
	if e.activeAll+e.queuedAll >= e.limits.AggregateQueue {
		e.mu.Unlock()
		return mutationAdmission{status: http.StatusTooManyRequests, err: &OperationError{Code: "backpressure", Message: "operation queue is full"}}, false
	}
	rec, err := e.registry.Create(kind, target)
	if err != nil {
		e.mu.Unlock()
		return mutationAdmission{status: http.StatusInternalServerError, err: &OperationError{Code: "failed", Message: "operation failed"}}, false
	}
	e.active[lane] = rec.ID
	e.activeAll++
	e.mu.Unlock()

	go e.run(rec, lane, work)
	return mutationAdmission{record: rec, status: http.StatusAccepted}, true
}

func (e *mutationExecutor) run(rec OperationRecord, lane string, work func() (OperationResult, error)) {
	if !e.beginRun(lane, rec.ID) {
		return
	}
	_ = e.registry.UpdateStatus(rec.ID, OperationStatusRunning)
	e.publishRecordID(rec.ID)
	result, err := work()
	status := OperationStatusSucceeded
	var opErr *OperationError
	if err != nil {
		status = OperationStatusFailed
		opErr = operationErrorFromError(err)
	}
	next, shouldComplete := e.finishLane(lane, rec.ID)
	if !shouldComplete {
		return
	}
	_ = e.registry.Complete(rec.ID, status, result.Metadata, opErr)
	e.publishRecordID(rec.ID)
	e.runQueued(lane, next)
}

func (e *mutationExecutor) beginRun(lane, id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.active[lane] == id
}

func (e *mutationExecutor) finishLane(lane, id string) (queuedMutation, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.active[lane] != id {
		return queuedMutation{}, false
	}
	delete(e.active, lane)
	if e.activeAll > 0 {
		e.activeAll--
	}
	if e.closing {
		return queuedMutation{}, true
	}
	var next queuedMutation
	if queue := e.queued[lane]; len(queue) > 0 {
		next = queue[0]
		if len(queue) == 1 {
			delete(e.queued, lane)
		} else {
			e.queued[lane] = queue[1:]
		}
		e.queuedAll--
		e.active[lane] = next.record.ID
		e.activeAll++
	}
	return next, true
}

func (e *mutationExecutor) runQueued(lane string, next queuedMutation) {
	if next.record.ID == "" {
		return
	}
	go e.run(next.record, lane, next.work)
}

func (e *mutationExecutor) publishRecordID(id string) {
	if e == nil || e.publish == nil {
		return
	}
	page, err := e.registry.List(OperationListOptions{Limit: e.limits.AggregateQueue})
	if err != nil {
		return
	}
	for _, dto := range page.Operations {
		if dto.ID == id {
			e.publish(OperationRecord{
				ID:          dto.ID,
				Kind:        dto.Kind,
				Target:      dto.Target,
				RequestedAt: dto.RequestedAt,
				UpdatedAt:   dto.UpdatedAt,
				CompletedAt: dto.CompletedAt,
				Status:      dto.Status,
				Result:      dto.Result,
				Error:       dto.Error,
			})
			return
		}
	}
}

func (e *mutationExecutor) runSynchronous(kind string, target OperationTarget, work func() (OperationResult, error)) (mutationAdmission, bool) {
	admission, accepted := e.reserve(kind, target)
	if !accepted {
		return admission, false
	}
	lane := mutationLane(kind, target)
	e.runReserved(admission.record, lane, work)
	return mutationAdmission{record: admission.record, status: http.StatusAccepted}, true
}

func (e *mutationExecutor) reserve(kind string, target OperationTarget) (mutationAdmission, bool) {
	if e == nil || e.registry == nil || e.target == nil {
		return mutationAdmission{status: http.StatusServiceUnavailable, err: &OperationError{Code: "unavailable", Message: "mutation service unavailable"}}, false
	}
	lane := mutationLane(kind, target)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closing {
		return mutationAdmission{status: http.StatusServiceUnavailable, err: &OperationError{Code: "shutdown", Message: "server is shutting down"}}, false
	}
	if e.active[lane] != "" {
		rec, err := e.registry.Create(kind, target)
		if err != nil {
			return mutationAdmission{status: http.StatusInternalServerError, err: &OperationError{Code: "failed", Message: "operation failed"}}, false
		}
		opErr := &OperationError{Code: "conflict", Message: "feature operation already active"}
		_ = e.registry.Complete(rec.ID, OperationStatusRejected, nil, opErr)
		rec.Status = OperationStatusRejected
		rec.Error = safeOperationError(opErr)
		return mutationAdmission{record: rec, status: http.StatusConflict, err: opErr}, false
	}
	if e.activeAll+e.queuedAll >= e.limits.AggregateQueue {
		return mutationAdmission{status: http.StatusTooManyRequests, err: &OperationError{Code: "backpressure", Message: "operation queue is full"}}, false
	}
	rec, err := e.registry.Create(kind, target)
	if err != nil {
		return mutationAdmission{status: http.StatusInternalServerError, err: &OperationError{Code: "failed", Message: "operation failed"}}, false
	}
	e.active[lane] = rec.ID
	e.activeAll++
	return mutationAdmission{record: rec, status: http.StatusAccepted}, true
}

func (e *mutationExecutor) runReserved(rec OperationRecord, lane string, work func() (OperationResult, error)) {
	if !e.beginRun(lane, rec.ID) {
		return
	}
	_ = e.registry.UpdateStatus(rec.ID, OperationStatusRunning)
	e.publishRecordID(rec.ID)
	result, err := work()
	status := OperationStatusSucceeded
	var opErr *OperationError
	if err != nil {
		status = OperationStatusFailed
		opErr = operationErrorFromError(err)
	}
	next, shouldComplete := e.finishLane(lane, rec.ID)
	if !shouldComplete {
		return
	}
	_ = e.registry.Complete(rec.ID, status, result.Metadata, opErr)
	e.publishRecordID(rec.ID)
	e.runQueued(lane, next)
}

func (e *mutationExecutor) admitShutdown(kind string, target OperationTarget) (mutationAdmission, []string, bool) {
	if e == nil || e.registry == nil || e.target == nil {
		return mutationAdmission{status: http.StatusServiceUnavailable, err: &OperationError{Code: "unavailable", Message: "mutation service unavailable"}}, nil, false
	}
	e.mu.Lock()
	if e.closing {
		e.mu.Unlock()
		return mutationAdmission{status: http.StatusServiceUnavailable, err: &OperationError{Code: "shutdown", Message: "server is shutting down"}}, nil, false
	}
	rec, err := e.registry.Create(kind, target)
	if err != nil {
		e.mu.Unlock()
		return mutationAdmission{status: http.StatusInternalServerError, err: &OperationError{Code: "failed", Message: "operation failed"}}, nil, false
	}
	e.closing = true
	interrupted := e.interruptedOperationIDsLocked()
	e.active = map[string]string{}
	e.activeAll = 0
	e.queued = map[string][]queuedMutation{}
	e.queuedAll = 0
	e.mu.Unlock()
	return mutationAdmission{record: rec, status: http.StatusAccepted}, interrupted, true
}

func (e *mutationExecutor) interruptedOperationIDsLocked() []string {
	interrupted := make([]string, 0, e.activeAll+e.queuedAll)
	for _, id := range e.active {
		if id != "" {
			interrupted = append(interrupted, id)
		}
	}
	for _, queue := range e.queued {
		for _, queued := range queue {
			if queued.record.ID != "" {
				interrupted = append(interrupted, queued.record.ID)
			}
		}
	}
	return interrupted
}

func (e *mutationExecutor) completeShutdown(rec OperationRecord, interrupted []string) {
	if e == nil || e.registry == nil || rec.ID == "" {
		return
	}
	_ = e.registry.UpdateStatus(rec.ID, OperationStatusRunning)
	e.publishRecordID(rec.ID)
	_ = e.registry.Complete(rec.ID, OperationStatusSucceeded, map[string]string{"status": "accepted"}, nil)
	e.publishRecordID(rec.ID)
	opErr := &OperationError{Code: "interrupted", Message: "operation interrupted by server shutdown"}
	for _, id := range interrupted {
		_ = e.registry.Complete(id, OperationStatusInterrupted, nil, opErr)
		e.publishRecordID(id)
	}
}

func operationErrorFromError(err error) *OperationError {
	if err == nil {
		return nil
	}
	var failure interface {
		OperationFailure() *OperationError
	}
	if errors.As(err, &failure) && failure.OperationFailure() != nil {
		return failure.OperationFailure()
	}
	return &OperationError{Code: "failed", Message: err.Error()}
}

func (e *mutationExecutor) shutdown() {
	if e == nil || e.registry == nil {
		return
	}
	e.mu.Lock()
	if e.closing {
		e.mu.Unlock()
		return
	}
	e.closing = true
	ids := e.interruptedOperationIDsLocked()
	e.active = map[string]string{}
	e.queued = map[string][]queuedMutation{}
	e.activeAll = 0
	e.queuedAll = 0
	e.mu.Unlock()

	opErr := &OperationError{Code: "interrupted", Message: "operation interrupted by server shutdown"}
	for _, id := range ids {
		_ = e.registry.Complete(id, OperationStatusInterrupted, nil, opErr)
		e.publishRecordID(id)
	}
}

func (e *mutationExecutor) laneQueueLimit(lane string) int {
	if lane == "runtime" {
		return e.limits.RuntimeQueue
	}
	if strings.HasPrefix(lane, "feature:") {
		return e.limits.FeatureQueue
	}
	return 0
}

func mutationLane(kind string, target OperationTarget) string {
	if kind == "feature.config.update" {
		return "runtime"
	}
	if target.Type == "feature" && target.FeatureID != "" {
		return "feature:" + target.FeatureID
	}
	if target.Type == "session" {
		if target.FeatureID != "" {
			return "feature:" + target.FeatureID
		}
		if target.SessionID != "" {
			return "session:" + target.SessionID
		}
		if target.RequestID != "" {
			return "session-request:" + target.RequestID
		}
		return "session"
	}
	if strings.HasPrefix(kind, "runtime.") || target.Type == "runtime" {
		return "runtime"
	}
	return "runtime"
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
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Agentico-Client")
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
	case "/api/v1/prompts/ask-user/answer", "/api/v1/prompts/help/send":
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
	case "start", "resume", "stop", "interrupt", "restart", "review-decision", "config", "need-user-input", "need-user-input-draft":
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
			if (parts[2] == "review-comments" && parts[3] == "fetch") ||
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
		case "content-type", "x-agentico-client":
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
	admission, accepted := h.mutations.admit("feature.create", OperationTarget{Type: "runtime"}, func() (OperationResult, error) {
		return h.mutations.target.CreateFeature(req)
	})
	h.writeMutationAdmission(w, admission, accepted)
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
		h.handleSimpleFeatureMutation(w, r, "feature.stop", featureID, func() (OperationResult, error) {
			return h.mutations.target.StopFeature(featureID)
		})
	case "restart":
		var req RestartFeatureRequest
		if !h.requireTrustedMutation(w, r) || !decodeMutationJSON(w, r, &req) {
			return true
		}
		h.handleSimpleFeatureMutationWithDecoded(w, "feature.restart", featureID, func() (OperationResult, error) {
			return h.mutations.target.RestartFeature(featureID, req)
		})
	case "review-decision":
		var req ReviewDecisionRequest
		if !h.requireTrustedMutation(w, r) || !decodeMutationJSON(w, r, &req) {
			return true
		}
		h.handleSimpleFeatureMutationWithDecoded(w, "feature.review_decision", featureID, func() (OperationResult, error) {
			return h.mutations.target.ReviewDecision(featureID, req)
		})
	case "config":
		var req FeatureConfigMutationRequest
		if !h.requireTrustedMutation(w, r) || !decodeMutationJSON(w, r, &req) {
			return true
		}
		if !validatePipelineProfile(w, req.Pipeline) {
			return true
		}
		h.handleFeatureConfigMutationWithDecoded(w, featureID, func() (OperationResult, error) {
			return h.mutations.target.UpdateFeatureConfig(featureID, req)
		})
	case "need-user-input":
		var req NeedUserInputDecisionRequest
		if !h.requireTrustedMutation(w, r) || !decodeMutationJSON(w, r, &req) {
			return true
		}
		if req.Decision != "resume" && req.Decision != "abort" {
			writeAPIError(w, http.StatusBadRequest, "bad_request", "decision must be resume or abort", nil)
			return true
		}
		h.handleSimpleFeatureMutationWithDecoded(w, "feature.need_user_input.decision", featureID, func() (OperationResult, error) {
			return h.mutations.target.NeedUserInputDecision(featureID, req)
		})
	case "need-user-input-draft":
		var req NeedUserInputDraftRequest
		if !h.requireTrustedMutation(w, r) || !decodeMutationJSON(w, r, &req) {
			return true
		}
		if len(req.Answers) == 0 {
			writeAPIError(w, http.StatusBadRequest, "bad_request", "answers are required", nil)
			return true
		}
		h.handleSimpleFeatureMutationWithDecoded(w, "feature.need_user_input.draft", featureID, func() (OperationResult, error) {
			return h.mutations.target.DraftNeedUserInputAnswers(featureID, req)
		})
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
	case "start":
		if subaction != "" {
			return false
		}
		h.decodeAndAdmitEmptyFeatureAction(w, r, "feature.start", featureID, func() (OperationResult, error) {
			return h.mutations.target.StartFeature(featureID)
		})
	case "pause-stop":
		if subaction != "" {
			return false
		}
		h.decodeAndAdmitEmptyFeatureAction(w, r, "feature.stop", featureID, func() (OperationResult, error) {
			return h.mutations.target.StopFeature(featureID)
		})
	case "resume":
		if subaction != "" {
			return false
		}
		h.decodeAndAdmitEmptyFeatureAction(w, r, "feature.resume", featureID, func() (OperationResult, error) {
			return h.mutations.target.StartFeature(featureID)
		})
	case "restart":
		if subaction != "" {
			return false
		}
		var req RestartFeatureRequest
		if !decodeMutationJSON(w, r, &req) {
			return true
		}
		h.handleSimpleFeatureMutationWithDecoded(w, "feature.restart", featureID, func() (OperationResult, error) {
			return h.mutations.target.RestartFeature(featureID, req)
		})
	case "publish":
		if subaction != "" {
			return false
		}
		var req PublishFeatureRequest
		if !decodeMutationJSON(w, r, &req) || !validateRepoList(w, req.Repos, false) {
			return true
		}
		h.handleSimpleFeatureMutationWithDecoded(w, "feature.publish", featureID, func() (OperationResult, error) {
			return h.mutations.target.PublishFeature(featureID, req)
		})
	case "merge":
		if subaction != "" {
			return false
		}
		h.decodeAndAdmitEmptyFeatureAction(w, r, "feature.merge", featureID, func() (OperationResult, error) {
			return h.mutations.target.MergeFeature(featureID)
		})
	case "rewind":
		if subaction != "" {
			return false
		}
		var req RewindFeatureRequest
		if !decodeMutationJSON(w, r, &req) || !validatePhaseName(w, req.TargetPhase) || !validatePositiveOptionalInt(w, "roadmap_phase", req.RoadmapPhase) {
			return true
		}
		h.handleSimpleFeatureMutationWithDecoded(w, "feature.rewind", featureID, func() (OperationResult, error) {
			return h.mutations.target.RewindFeature(featureID, req)
		})
	case "retry":
		if subaction != "" {
			return false
		}
		h.decodeAndAdmitEmptyFeatureAction(w, r, "feature.retry", featureID, func() (OperationResult, error) {
			return h.mutations.target.RetryFeature(featureID)
		})
	case "rebase":
		if subaction != "" {
			return false
		}
		var req RebaseActionRequest
		if !decodeMutationJSON(w, r, &req) || !validateRepoName(w, req.Repo, false) || !validateSafeOptionalToken(w, "rebase_target", req.RebaseTarget) || !validateConflictFiles(w, req.ConflictFiles) {
			return true
		}
		h.handleSimpleFeatureMutationWithDecoded(w, "feature.rebase", featureID, func() (OperationResult, error) {
			return h.mutations.target.StartRebase(featureID, req)
		})
	case "review-comments":
		if subaction == "fetch" {
			var req ReviewCommentsFetchRequest
			if !decodeMutationJSON(w, r, &req) || !validateRepoName(w, req.Repo, true) {
				return true
			}
			resp, err := h.mutations.target.FetchReviewComments(featureID, req)
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
		h.handleSimpleFeatureMutationWithDecoded(w, "feature.review_comments", featureID, func() (OperationResult, error) {
			return h.mutations.target.StartReviewComments(featureID, req)
		})
	case "tweak":
		if subaction == "finish" {
			var req TweakFinishRequest
			if !decodeMutationJSON(w, r, &req) || !validateTweakDecision(w, req.Decision) {
				return true
			}
			h.handleSimpleFeatureMutationWithDecoded(w, "feature.tweak.finish", featureID, func() (OperationResult, error) {
				return h.mutations.target.FinishTweak(featureID, req)
			})
			return true
		}
		if subaction != "" {
			return false
		}
		var req TweakActionRequest
		if !decodeMutationJSON(w, r, &req) {
			return true
		}
		h.handleSimpleFeatureMutationWithDecoded(w, "feature.tweak.start", featureID, func() (OperationResult, error) {
			return h.mutations.target.StartTweak(featureID, req)
		})
	case "refactor":
		var req RefactorActionRequest
		if !decodeMutationJSON(w, r, &req) || !validateRefactorRequest(w, req) {
			return true
		}
		if subaction == "restart" {
			h.handleSimpleFeatureMutationWithDecoded(w, "feature.refactor.restart", featureID, func() (OperationResult, error) {
				return h.mutations.target.RestartRefactor(featureID, req)
			})
			return true
		}
		if subaction != "" {
			return false
		}
		h.handleSimpleFeatureMutationWithDecoded(w, "feature.refactor.start", featureID, func() (OperationResult, error) {
			return h.mutations.target.StartRefactor(featureID, req)
		})
	case "mark-done":
		if subaction != "" {
			return false
		}
		h.decodeAndAdmitEmptyFeatureAction(w, r, "feature.mark_done", featureID, func() (OperationResult, error) {
			return h.mutations.target.MarkDone(featureID)
		})
	case "cleanup":
		if subaction != "" {
			return false
		}
		var req CleanupActionRequest
		if !decodeMutationJSON(w, r, &req) || !validateCleanupRequest(w, req) {
			return true
		}
		h.handleSimpleFeatureMutationWithDecoded(w, "feature.cleanup", featureID, func() (OperationResult, error) {
			return h.mutations.target.CleanupFeature(featureID, req)
		})
	case "delete":
		if subaction != "" {
			return false
		}
		h.decodeAndAdmitEmptyFeatureAction(w, r, "feature.delete", featureID, func() (OperationResult, error) {
			return h.mutations.target.DeleteFeature(featureID)
		})
	default:
		return false
	}
	return true
}

func (h *apiHandler) decodeAndAdmitEmptyFeatureAction(w http.ResponseWriter, r *http.Request, kind, featureID string, work func() (OperationResult, error)) {
	var req map[string]any
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	h.handleSimpleFeatureMutationWithDecoded(w, kind, featureID, work)
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
		admission, accepted := h.mutations.admit("runtime.config.update", OperationTarget{Type: "runtime"}, func() (OperationResult, error) {
			return h.mutations.target.RuntimeConfig(req)
		})
		h.writeMutationAdmission(w, admission, accepted)
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
	if h.mutations == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "unavailable", "mutation service unavailable", nil)
		return
	}
	admission, interrupted, accepted := h.mutations.admitShutdown("runtime.shutdown", OperationTarget{Type: "runtime"})
	h.writeMutationAdmission(w, admission, accepted)
	if accepted {
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		go func() {
			h.mutations.completeShutdown(admission.record, interrupted)
			if h.requestShutdown != nil {
				h.requestShutdown()
			}
		}()
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
	target := OperationTarget{Type: "session", SessionID: req.SessionID, RequestID: req.RequestID}
	admission, accepted := h.mutations.admit("permission.answer", target, func() (OperationResult, error) {
		return h.mutations.target.AnswerPermission(req)
	})
	h.writeMutationAdmission(w, admission, accepted)
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
		target := OperationTarget{Type: "session", SessionID: req.SessionID, RequestID: req.RequestID}
		admission, accepted := h.mutations.admit("ask_user.answer", target, func() (OperationResult, error) {
			return h.mutations.target.AnswerAskUser(req)
		})
		h.writeMutationAdmission(w, admission, accepted)
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
		target := OperationTarget{Type: "session", FeatureID: req.FeatureID, SessionID: req.SessionID}
		admission, accepted := h.mutations.admit("help.send", target, func() (OperationResult, error) {
			return h.mutations.target.SendHelp(req)
		})
		h.writeMutationAdmission(w, admission, accepted)
	default:
		writeAPIError(w, http.StatusNotFound, "not_found", "endpoint not found", nil)
	}
}

func (h *apiHandler) handleStartFeatureMutation(w http.ResponseWriter, r *http.Request, featureID string) {
	if !h.requireTrustedMutation(w, r) {
		return
	}
	var req map[string]any
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	h.handleSimpleFeatureMutationWithDecoded(w, "feature.start", featureID, func() (OperationResult, error) {
		return h.mutations.target.StartFeature(featureID)
	})
}

func (h *apiHandler) handleSimpleFeatureMutation(w http.ResponseWriter, r *http.Request, kind, featureID string, work func() (OperationResult, error)) {
	if !h.requireTrustedMutation(w, r) {
		return
	}
	var req map[string]any
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	h.handleSimpleFeatureMutationWithDecoded(w, kind, featureID, work)
}

func (h *apiHandler) handleSimpleFeatureMutationWithDecoded(w http.ResponseWriter, kind, featureID string, work func() (OperationResult, error)) {
	admission, accepted := h.mutations.admit(kind, OperationTarget{Type: "feature", FeatureID: featureID}, work)
	h.writeMutationAdmission(w, admission, accepted)
}

func (h *apiHandler) handleFeatureConfigMutationWithDecoded(w http.ResponseWriter, featureID string, work func() (OperationResult, error)) {
	admission, accepted := h.mutations.admit("feature.config.update", OperationTarget{Type: "feature", FeatureID: featureID}, work)
	h.writeMutationAdmission(w, admission, accepted)
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

func validateSafeOptionalToken(w http.ResponseWriter, field, value string) bool {
	if strings.TrimSpace(value) == "" {
		return true
	}
	if !safeActionToken(value, true) {
		writeAPIError(w, http.StatusBadRequest, "bad_request", field+" has invalid characters", nil)
		return false
	}
	return true
}

func validateConflictFiles(w http.ResponseWriter, files []string) bool {
	if len(files) > 100 {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "too many conflict files", nil)
		return false
	}
	for _, file := range files {
		if !safeRelativePathToken(file) {
			writeAPIError(w, http.StatusBadRequest, "bad_request", "conflict_files contains an invalid path", nil)
			return false
		}
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

func safeRelativePathToken(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 512 || strings.Contains(value, "..") || strings.ContainsAny(value, "\\\x00\r\n") {
		return false
	}
	return !strings.HasPrefix(value, "/")
}

func (h *apiHandler) writeMutationAdmission(w http.ResponseWriter, admission mutationAdmission, accepted bool) {
	if accepted {
		writeJSON(w, http.StatusAccepted, OperationAcceptedResponse{APIVersion: APIVersion, OperationID: admission.record.ID, Status: OperationStatusQueued})
		return
	}
	if admission.record.ID != "" && admission.err != nil && admission.err.Code == "conflict" {
		writeJSON(w, admission.status, OperationAcceptedResponse{APIVersion: APIVersion, OperationID: admission.record.ID, Status: OperationStatusRejected})
		return
	}
	status := admission.status
	if status == 0 {
		status = http.StatusInternalServerError
	}
	code := "internal_error"
	message := "mutation failed"
	if admission.err != nil {
		code = admission.err.Code
		message = admission.err.Message
	}
	writeAPIError(w, status, code, message, nil)
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
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "request body must contain one JSON object", nil)
		return false
	}
	return true
}

func isLoopbackOrigin(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func operationSSEEvent(id string, rec OperationRecord) SSEEventDTO {
	resource := ResourceDTO{Type: "operation", ID: rec.ID, FeatureID: rec.Target.FeatureID}
	return SSEEventDTO{
		APIVersion:       APIVersion,
		ID:               id,
		Kind:             "operation.updated",
		At:               rec.UpdatedAt,
		Resource:         resource,
		Revision:         revisionForAny(resource),
		SnapshotRequired: true,
		Summary:          fmt.Sprintf("%s %s", rec.Kind, rec.Status),
	}
}
