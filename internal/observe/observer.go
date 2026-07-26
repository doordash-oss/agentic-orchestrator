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

package observe

import (
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/autoreview"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
)

// Observer is the central observability facade. It coordinates JSONL event
// emission and (when enabled) OTel span management. All methods are safe to
// call on a nil receiver — this is the NopObserver pattern.
type Observer struct {
	emitter          *Emitter
	otel             *otelBridge
	enabled          bool
	activePhaseSpans sync.Map
}

// RewindEventInput carries the durable context for a successful rewind audit
// event. RoadmapPhase is zero for full phase rewinds and positive for partial
// Implement roadmap-phase rewinds.
type RewindEventInput struct {
	TargetPhase        feature.Phase
	EffectiveTarget    feature.Phase
	RoadmapPhase       int
	TotalRoadmapPhases int
	SourceRun          int
	NewRun             int
	CarriedPhases      []string
	BackupBranches     map[string]string
}

// ContextFileReadMeta carries optional provider provenance for context.file_read
// events. Category and path remain top-level event data; these fields explain
// how the read was observed.
type ContextFileReadMeta struct {
	Source         string
	ProviderItemID string
	ExitCode       *int
}

// AutomaticReviewEventInput carries one bounded actual-review outcome.
type AutomaticReviewEventInput struct {
	Phase               string
	SessionID           string
	RepoName            string
	Iteration           int
	Provider            string
	Model               string
	Outcome             autoreview.Outcome
	Duration            time.Duration
	CommandSummary      string
	FailureReason       string
	StatusPersisted     *bool
	StatusFailureClass  string
	StatusFailureReason string
}

// AutomaticReviewUnavailableEventInput carries one session-scoped reviewer
// unavailability notice, either from session-build resolution or the runtime
// failure circuit breaker.
type AutomaticReviewUnavailableEventInput struct {
	Phase     string
	SessionID string
	RepoName  string
	Iteration int
	Scope     string
	Reason    string
}

// New creates an Observer. When enabled is false, all methods return immediately.
func New(enabled bool, stateDir string, otelEnabled bool, otelEndpoint string, otelInsecure bool, otelServiceName string) *Observer {
	if !enabled {
		return &Observer{enabled: false}
	}
	return &Observer{
		emitter: NewEmitter(stateDir),
		otel:    newOtelBridge(otelEnabled, otelEndpoint, otelInsecure, otelServiceName),
		enabled: true,
	}
}

// ensureFeatureSpan re-materializes the feature-level OTel span in the bridge
// if it is not already tracked. This handles the case where the process was
// restarted after FeatureStarted was called in a previous session, or when
// the feature span hasn't been flushed yet by the batcher.
func (o *Observer) ensureFeatureSpan(sc SpanContext) {
	if sc.ParentSpanID == "" {
		return
	}
	parentSC := SpanContext{
		TraceID:     sc.TraceID,
		SpanID:      sc.ParentSpanID,
		FeatureID:   sc.FeatureID,
		FeatureName: sc.FeatureName,
		RunNumber:   sc.RunNumber,
	}
	attrs := map[string]string{}
	if sc.FeatureName != "" {
		attrs["feature.name"] = sc.FeatureName
	}
	o.otel.ensureSpan(parentSC, "feature", attrs)
}

// emit stamps RunNumber onto the event from sc and forwards to the underlying
// Emitter. Centralises Event population so every typed emit method inherits
// the run-number propagation without per-site duplication. Enabled / nil-safe
// behaviour is enforced at the call-site (each typed method short-circuits
// when o == nil || !o.enabled before reaching this helper).
func (o *Observer) emit(sc SpanContext, evt Event) error {
	evt.RunNumber = sc.RunNumber
	return o.emitter.Emit(evt)
}

// addRunNumber inserts the current run number into an attrs map when nonzero.
// Helper so every StartSpan / AddSpanEvent caller in observer.go gets
// run_number without per-site duplication. Returns attrs unchanged when
// sc.RunNumber == 0 so pre-Phase-4 callers / tests do not see a spurious
// "run_number":"0" attribute on OTel spans.
func addRunNumber(sc SpanContext, attrs map[string]string) map[string]string {
	if sc.RunNumber == 0 {
		return attrs
	}
	if attrs == nil {
		attrs = make(map[string]string, 1)
	}
	attrs["run_number"] = strconv.Itoa(sc.RunNumber)
	return attrs
}

// PhaseStarted emits a phase.started event.
func (o *Observer) PhaseStarted(sc SpanContext, phase string) {
	if o == nil || !o.enabled {
		return
	}
	// Ensure the parent feature span exists in the OTel bridge.
	// It may be missing if the process restarted since FeatureStarted was called.
	o.ensureFeatureSpan(sc)
	evt := Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "phase.started",
		Phase:        phase,
		Status:       "started",
		FeatureID:    sc.FeatureID,
	}
	o.emit(sc, evt)
	o.activePhaseSpans.Store(sc.FeatureID, sc)
	o.otel.StartSpan(sc, "phase."+phase, addRunNumber(sc, map[string]string{"phase": phase}))
}

// PhaseCompleted emits a phase.completed event.
func (o *Observer) PhaseCompleted(sc SpanContext, phase string, duration time.Duration, err error) {
	if o == nil || !o.enabled {
		return
	}
	status := "completed"
	errStr := ""
	if err != nil {
		status = "failed"
		errStr = err.Error()
	}
	evt := Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "phase." + status,
		Phase:        phase,
		Status:       status,
		FeatureID:    sc.FeatureID,
		DurationMs:   duration.Milliseconds(),
		Error:        errStr,
	}
	o.emit(sc, evt)
	o.activePhaseSpans.Delete(sc.FeatureID)
	o.otel.EndSpan(sc.SpanID, status, nil)
}

// SessionStarted emits a session.started event.
func (o *Observer) SessionStarted(sc SpanContext, phase, sessionID, provider, model, repoName string, effortLevel string, effortSource string) {
	if o == nil || !o.enabled {
		return
	}
	data := map[string]any{
		"provider": provider,
		"model":    model,
	}
	if effortLevel != "" {
		data["effort"] = effortLevel
	}
	if effortSource != "" {
		data["effort_source"] = effortSource
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "session.started",
		Phase:        phase,
		FeatureID:    sc.FeatureID,
		SessionID:    sessionID,
		RepoName:     repoName,
		Data:         data,
	})
	attrs := map[string]string{"phase": phase, "session_id": sessionID, "provider": provider, "model": model}
	if repoName != "" {
		attrs["repo"] = repoName
	}
	if effortLevel != "" {
		attrs["effort"] = effortLevel
	}
	if effortSource != "" {
		attrs["effort_source"] = effortSource
	}
	o.otel.StartSpan(sc, "session."+phase, addRunNumber(sc, attrs))
}

// SessionUsage carries cost and token data for a completed session.
// Defined in observe to avoid importing llm — callers convert from llm.Usage.
type SessionUsage struct {
	TotalCostUSD             float64
	InputTokens              int
	OutputTokens             int
	CacheReadInputTokens     int
	CacheCreationInputTokens int
}

// SessionEnded emits a session.ended event.
func (o *Observer) SessionEnded(sc SpanContext, phase, sessionID, repoName string, usage SessionUsage, duration time.Duration, err error) {
	if o == nil || !o.enabled {
		return
	}
	evt := Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "session.ended",
		Phase:        phase,
		FeatureID:    sc.FeatureID,
		SessionID:    sessionID,
		RepoName:     repoName,
		DurationMs:   duration.Milliseconds(),
		Status:       "ok",
		Data: map[string]any{
			"total_cost_usd":              usage.TotalCostUSD,
			"input_tokens":                usage.InputTokens,
			"output_tokens":               usage.OutputTokens,
			"cache_read_input_tokens":     usage.CacheReadInputTokens,
			"cache_creation_input_tokens": usage.CacheCreationInputTokens,
		},
	}
	if err != nil {
		evt.Status = "error"
		evt.Error = err.Error()
	}
	o.emit(sc, evt)
	o.otel.EndSpan(sc.SpanID, evt.Status, map[string]string{
		"session_id": sessionID,
		"phase":      phase,
	})
}

// IterationStarted emits an iteration.started event.
func (o *Observer) IterationStarted(sc SpanContext, iteration int) {
	if o == nil || !o.enabled {
		return
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "iteration.started",
		FeatureID:    sc.FeatureID,
		Iteration:    iteration,
	})
	o.otel.StartSpan(sc, "iteration", addRunNumber(sc, map[string]string{
		"iteration": fmt.Sprintf("%d", iteration),
	}))
}

// IterationEnded emits an iteration.ended event.
func (o *Observer) IterationEnded(sc SpanContext, iteration int, usage SessionUsage, duration time.Duration, status string) {
	if o == nil || !o.enabled {
		return
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "iteration.ended",
		FeatureID:    sc.FeatureID,
		Iteration:    iteration,
		DurationMs:   duration.Milliseconds(),
		Status:       status,
		Data: map[string]any{
			"total_cost_usd":              usage.TotalCostUSD,
			"input_tokens":                usage.InputTokens,
			"output_tokens":               usage.OutputTokens,
			"cache_read_input_tokens":     usage.CacheReadInputTokens,
			"cache_creation_input_tokens": usage.CacheCreationInputTokens,
		},
	})
	o.otel.EndSpan(sc.SpanID, status, map[string]string{
		"iteration": fmt.Sprintf("%d", iteration),
	})
}

// ReviewStarted emits a review.started event.
func (o *Observer) ReviewStarted(sc SpanContext, iteration int) {
	if o == nil || !o.enabled {
		return
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "review.started",
		FeatureID:    sc.FeatureID,
		Iteration:    iteration,
	})
	o.otel.StartSpan(sc, "review", addRunNumber(sc, map[string]string{
		"iteration": fmt.Sprintf("%d", iteration),
	}))
}

// ReviewCompleted emits a review.completed event.
func (o *Observer) ReviewCompleted(sc SpanContext, iteration int, status string, duration time.Duration) {
	if o == nil || !o.enabled {
		return
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "review.completed",
		FeatureID:    sc.FeatureID,
		Iteration:    iteration,
		DurationMs:   duration.Milliseconds(),
		Status:       status,
	})
	o.otel.EndSpan(sc.SpanID, status, map[string]string{
		"iteration": fmt.Sprintf("%d", iteration),
	})
}

// ValidationStarted emits a validation.started event.
func (o *Observer) ValidationStarted(sc SpanContext, phase string, validatorCount int) {
	if o == nil || !o.enabled {
		return
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "validation.started",
		Phase:        phase,
		FeatureID:    sc.FeatureID,
		Data: map[string]any{
			"validator_count": validatorCount,
		},
	})
	o.otel.StartSpan(sc, "validation."+phase, addRunNumber(sc, map[string]string{"phase": phase}))
}

// ValidationCompleted emits a validation.completed event.
func (o *Observer) ValidationCompleted(sc SpanContext, phase string, verdict string, duration time.Duration, validatorCount int) {
	if o == nil || !o.enabled {
		return
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "validation.completed",
		Phase:        phase,
		Status:       verdict,
		FeatureID:    sc.FeatureID,
		DurationMs:   duration.Milliseconds(),
		Data: map[string]any{
			"validator_count": validatorCount,
		},
	})
	o.otel.EndSpan(sc.SpanID, verdict, nil)
}

// ValidatorStarted emits a validator.started event.
func (o *Observer) ValidatorStarted(sc SpanContext, validatorName string) {
	if o == nil || !o.enabled {
		return
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "validator.started",
		FeatureID:    sc.FeatureID,
		Data: map[string]any{
			"validator_name": validatorName,
		},
	})
	o.otel.StartSpan(sc, "validator."+validatorName, addRunNumber(sc, map[string]string{"validator": validatorName}))
}

// ValidatorCompleted emits a validator.completed event.
func (o *Observer) ValidatorCompleted(sc SpanContext, validatorName, verdict string, duration time.Duration) {
	if o == nil || !o.enabled {
		return
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "validator.completed",
		Status:       verdict,
		FeatureID:    sc.FeatureID,
		DurationMs:   duration.Milliseconds(),
		Data: map[string]any{
			"validator_name": validatorName,
		},
	})
	o.otel.EndSpan(sc.SpanID, verdict, nil)
}

// FeatureStarted emits a feature.started event.
func (o *Observer) FeatureStarted(sc SpanContext, name string, repos []string, pipeline string) {
	if o == nil || !o.enabled {
		return
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "feature.started",
		FeatureID:    sc.FeatureID,
		Data: map[string]any{
			"name":     name,
			"repos":    repos,
			"pipeline": pipeline,
		},
	})
	o.otel.StartSpan(sc, "feature", addRunNumber(sc, map[string]string{
		"feature.name": name,
		"pipeline":     pipeline,
	}))
}

// FeatureCompleted emits a feature.completed event.
func (o *Observer) FeatureCompleted(sc SpanContext, totalCost float64, totalDuration time.Duration) {
	if o == nil || !o.enabled {
		return
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "feature.completed",
		FeatureID:    sc.FeatureID,
		DurationMs:   totalDuration.Milliseconds(),
		Data: map[string]any{
			"total_cost_usd": totalCost,
		},
	})
	o.otel.EndSpan(sc.SpanID, "completed", map[string]string{
		"total_cost_usd": fmt.Sprintf("%.4f", totalCost),
	})
}

// FeatureFailed emits a feature.failed event.
func (o *Observer) FeatureFailed(sc SpanContext, failureType, errorMsg string) {
	if o == nil || !o.enabled {
		return
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "feature.failed",
		FeatureID:    sc.FeatureID,
		Error:        errorMsg,
		Data: map[string]any{
			"failure_type": failureType,
		},
	})
	o.otel.EndSpan(sc.SpanID, "failed", map[string]string{
		"failure_type": failureType,
		"error":        errorMsg,
	})
}

// SetupLifecycle emits a pre-phase setup lifecycle event.
func (o *Observer) SetupLifecycle(sc SpanContext, ev feature.SetupEvent) {
	if o == nil || !o.enabled {
		return
	}
	eventType := "setup.progress"
	switch ev.Kind {
	case feature.SetupEventStarted:
		eventType = "setup.started"
	case feature.SetupEventCompleted:
		eventType = "setup.completed"
	case feature.SetupEventFailed:
		eventType = "setup.failed"
	}
	data := map[string]any{
		"attempt": ev.Attempt,
	}
	if ev.TaskKey != "" {
		data["setup_task"] = ev.TaskKey
	}
	if ev.TaskKind != "" {
		data["setup_kind"] = string(ev.TaskKind)
	}
	if ev.TaskStatus != "" {
		data["setup_status"] = string(ev.TaskStatus)
	}
	if ev.LogPath != "" {
		data["setup_log"] = ev.LogPath
	}
	if ev.Repo != "" {
		data["repo_name"] = ev.Repo
	}
	if ev.Path != "" {
		data["path"] = ev.Path
	}
	if ev.Branch != "" {
		data["branch"] = ev.Branch
	}
	if ev.Error != "" {
		data["error"] = ev.Error
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    eventType,
		FeatureID:    sc.FeatureID,
		RepoName:     ev.Repo,
		Status:       string(ev.TaskStatus),
		Error:        ev.Error,
		Data:         data,
	})
}

// FeatureInterrupted emits a feature.interrupted event.
func (o *Observer) FeatureInterrupted(sc SpanContext, phase string) {
	if o == nil || !o.enabled {
		return
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "feature.interrupted",
		FeatureID:    sc.FeatureID,
		Phase:        phase,
	})
	o.otel.EndSpan(sc.SpanID, "interrupted", map[string]string{
		"phase": phase,
	})
}

// PermissionRequested emits a permission.requested event.
// The toolInput field is truncated to 200 characters.
func (o *Observer) PermissionRequested(sc SpanContext, sessionID string, repoName string, iteration int, toolName string, toolInput string) {
	if o == nil || !o.enabled {
		return
	}
	if len(toolInput) > 200 {
		toolInput = toolInput[:200]
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "permission.requested",
		FeatureID:    sc.FeatureID,
		SessionID:    sessionID,
		RepoName:     repoName,
		Iteration:    iteration,
		Data: map[string]any{
			"tool_name":  toolName,
			"tool_input": toolInput,
		},
	})
	o.otel.AddSpanEvent(sc.ParentSpanID, "permission.requested", addRunNumber(sc, map[string]string{
		"tool_name": toolName,
	}))
}

// PermissionResolved emits a permission.resolved event.
func (o *Observer) PermissionResolved(sc SpanContext, sessionID string, repoName string, iteration int, toolName string, decision string) {
	if o == nil || !o.enabled {
		return
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "permission.resolved",
		FeatureID:    sc.FeatureID,
		SessionID:    sessionID,
		RepoName:     repoName,
		Iteration:    iteration,
		Data: map[string]any{
			"tool_name": toolName,
			"decision":  decision,
		},
	})
	o.otel.AddSpanEvent(sc.ParentSpanID, "permission.resolved", addRunNumber(sc, map[string]string{
		"tool_name": toolName,
		"decision":  decision,
	}))
}

// AutomaticReviewCompleted emits exactly one best-effort event for an actual
// automatic-review attempt. Nil and disabled observers are no-ops.
func (o *Observer) AutomaticReviewCompleted(sc SpanContext, in AutomaticReviewEventInput) {
	if o == nil || !o.enabled {
		return
	}
	duration := in.Duration
	if duration < 0 {
		duration = 0
	}
	data := map[string]any{
		"provider":        in.Provider,
		"model":           in.Model,
		"outcome":         string(in.Outcome),
		"command_summary": permission.AutomaticReviewCommandSummary(in.CommandSummary),
	}
	if in.FailureReason != "" {
		data["failure_reason"] = permission.AutomaticReviewBoundReason(in.FailureReason)
	}
	if in.StatusPersisted != nil {
		data["status_persisted"] = *in.StatusPersisted
		if !*in.StatusPersisted {
			data["status_failure_class"] = permission.AutomaticReviewBoundReason(in.StatusFailureClass)
			data["status_failure_reason"] = permission.AutomaticReviewBoundReason(in.StatusFailureReason)
		}
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "automatic_review.completed",
		Phase:        in.Phase,
		Status:       string(in.Outcome),
		FeatureID:    sc.FeatureID,
		SessionID:    in.SessionID,
		RepoName:     in.RepoName,
		Iteration:    in.Iteration,
		DurationMs:   duration.Milliseconds(),
		Data:         data,
	})
	attrs := map[string]string{
		"provider": in.Provider,
		"model":    in.Model,
		"outcome":  string(in.Outcome),
	}
	o.otel.AddSpanEvent(sc.ParentSpanID, "automatic_review.completed", addRunNumber(sc, attrs))
}

// AutomaticReviewUnavailable emits exactly one bounded operator notice for a
// session-scoped reviewer outage. Nil and disabled observers are no-ops.
func (o *Observer) AutomaticReviewUnavailable(sc SpanContext, in AutomaticReviewUnavailableEventInput) {
	if o == nil || !o.enabled {
		return
	}
	scope := permission.AutomaticReviewBoundReason(in.Scope)
	reason := permission.AutomaticReviewBoundReason(in.Reason)
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "automatic_review.unavailable",
		Phase:        in.Phase,
		Status:       "unavailable",
		FeatureID:    sc.FeatureID,
		SessionID:    in.SessionID,
		RepoName:     in.RepoName,
		Iteration:    in.Iteration,
		Data: map[string]any{
			"scope":  scope,
			"reason": reason,
		},
	})
	o.otel.AddSpanEvent(sc.ParentSpanID, "automatic_review.unavailable", addRunNumber(sc, map[string]string{
		"scope": scope,
	}))
}

// QuestionAsked emits a question.asked event.
func (o *Observer) QuestionAsked(sc SpanContext, sessionID string, repoName string, iteration int, question string) {
	if o == nil || !o.enabled {
		return
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "question.asked",
		FeatureID:    sc.FeatureID,
		SessionID:    sessionID,
		RepoName:     repoName,
		Iteration:    iteration,
		Data: map[string]any{
			"question": question,
		},
	})
	o.otel.AddSpanEvent(sc.ParentSpanID, "question.asked", addRunNumber(sc, map[string]string{
		"question": question,
	}))
}

// QuestionAnsweredMetadata carries optional audit fields for session-layer
// generated answers. It is omitted from provider-visible AskUserQuestion
// responses; this struct only affects observability events.
type QuestionAnsweredMetadata struct {
	AutoPicked bool
	Confidence float64
}

// QuestionAnswered emits a question.answered event.
func (o *Observer) QuestionAnswered(sc SpanContext, sessionID string, repoName string, iteration int, question string, answer string, metadata ...QuestionAnsweredMetadata) {
	if o == nil || !o.enabled {
		return
	}
	data := map[string]any{
		"question": question,
		"answer":   answer,
	}
	attrs := map[string]string{
		"question": question,
		"answer":   answer,
	}
	if len(metadata) > 0 && metadata[0].AutoPicked {
		data["auto_picked"] = true
		data["confidence"] = metadata[0].Confidence
		attrs["auto_picked"] = "true"
		attrs["confidence"] = fmt.Sprintf("%.2f", metadata[0].Confidence)
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "question.answered",
		FeatureID:    sc.FeatureID,
		SessionID:    sessionID,
		RepoName:     repoName,
		Iteration:    iteration,
		Data:         data,
	})
	o.otel.AddSpanEvent(sc.ParentSpanID, "question.answered", addRunNumber(sc, attrs))
}

// RecoveryScanned emits a recovery.scanned event.
func (o *Observer) RecoveryScanned(sc SpanContext, totalItems int, aliveItems int) {
	if o == nil || !o.enabled {
		return
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "recovery.scanned",
		FeatureID:    sc.FeatureID,
		Data: map[string]any{
			"total_items": totalItems,
			"alive_items": aliveItems,
		},
	})
	o.otel.AddSpanEvent(sc.SpanID, "recovery.scanned", addRunNumber(sc, map[string]string{
		"total_items": fmt.Sprintf("%d", totalItems),
		"alive_items": fmt.Sprintf("%d", aliveItems),
	}))
}

// RecoveryAction emits a recovery.action event.
func (o *Observer) RecoveryAction(sc SpanContext, action string, phase string, processAlive bool) {
	if o == nil || !o.enabled {
		return
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "recovery.action",
		FeatureID:    sc.FeatureID,
		Phase:        phase,
		Data: map[string]any{
			"action":        action,
			"process_alive": processAlive,
		},
	})
	o.otel.AddSpanEvent(sc.SpanID, "recovery.action", addRunNumber(sc, map[string]string{
		"action": action,
		"phase":  phase,
	}))
}

// ContextFileRead emits a context.file_read event when an agent reads a
// KB leaf file, skill SKILL.md, or guideline file during its session.
// category is one of "kb", "skill", or "guideline".
func (o *Observer) ContextFileRead(sc SpanContext, phase, sessionID, category, filePath string, metas ...ContextFileReadMeta) {
	if o == nil || !o.enabled {
		return
	}
	data := map[string]any{
		"category":  category,
		"file_path": filePath,
	}
	otelAttrs := map[string]string{
		"category":  category,
		"file_path": filePath,
	}
	if len(metas) > 0 {
		meta := metas[0]
		if meta.Source != "" {
			data["source"] = meta.Source
			otelAttrs["source"] = meta.Source
		}
		if meta.ProviderItemID != "" {
			data["provider_item_id"] = meta.ProviderItemID
			otelAttrs["provider_item_id"] = meta.ProviderItemID
		}
		if meta.ExitCode != nil {
			data["exit_code"] = *meta.ExitCode
			otelAttrs["exit_code"] = strconv.Itoa(*meta.ExitCode)
		}
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "context.file_read",
		Phase:        phase,
		FeatureID:    sc.FeatureID,
		SessionID:    sessionID,
		Data:         data,
	})
	o.otel.AddSpanEvent(sc.SpanID, "context.file_read", addRunNumber(sc, otelAttrs))
}

// ContextHandoffTriggered emits a context.handoff_triggered event when the
// autonomous loop asks an agent to stop early and write a handoff because the
// provider-reported context window crossed Agentic's threshold.
func (o *Observer) ContextHandoffTriggered(sc SpanContext, phase, sessionID, repoName, provider string, iteration int, pct, thresholdPct, totalTokens, windowTokens, baselineTokens int) {
	if o == nil || !o.enabled {
		return
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "context.handoff_triggered",
		Phase:        phase,
		Status:       "triggered",
		FeatureID:    sc.FeatureID,
		SessionID:    sessionID,
		RepoName:     repoName,
		Iteration:    iteration,
		Data: map[string]any{
			"provider":        provider,
			"context_pct":     pct,
			"threshold_pct":   thresholdPct,
			"total_tokens":    totalTokens,
			"window_tokens":   windowTokens,
			"baseline_tokens": baselineTokens,
		},
	})
	o.otel.AddSpanEvent(sc.SpanID, "context.handoff_triggered", addRunNumber(sc, map[string]string{
		"phase":         phase,
		"session_id":    sessionID,
		"provider":      provider,
		"context_pct":   strconv.Itoa(pct),
		"threshold_pct": strconv.Itoa(thresholdPct),
	}))
}

// ContextLargeOutput emits a context.large_output event when a Codex command
// execution records a large aggregated output in the session transcript.
func (o *Observer) ContextLargeOutput(sc SpanContext, phase, sessionID, repoName, provider string, iteration int, command string, outputChars, thresholdChars int, exitCode *int, durationMs *int64) {
	if o == nil || !o.enabled {
		return
	}
	data := map[string]any{
		"provider":        provider,
		"command":         command,
		"output_chars":    outputChars,
		"threshold_chars": thresholdChars,
	}
	if exitCode != nil {
		data["exit_code"] = *exitCode
	}
	if durationMs != nil {
		data["duration_ms"] = *durationMs
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "context.large_output",
		Phase:        phase,
		Status:       "observed",
		FeatureID:    sc.FeatureID,
		SessionID:    sessionID,
		RepoName:     repoName,
		Iteration:    iteration,
		Data:         data,
	})
	o.otel.AddSpanEvent(sc.SpanID, "context.large_output", addRunNumber(sc, map[string]string{
		"phase":        phase,
		"session_id":   sessionID,
		"provider":     provider,
		"output_chars": strconv.Itoa(outputChars),
		"threshold":    strconv.Itoa(thresholdChars),
	}))
}

// AgentTaskStarted emits an agent.task_started event when a subagent
// (Task tool) invocation begins. This is the canonical stamp of when a
// subagent's wall-clock started and is used by downstream consumers
// (dashboards, per-subagent watchdogs) to key on the subagent ID.
// The task prompt is intentionally NOT written to events.jsonl — it can
// be very large and lives in the session transcript already.
func (o *Observer) AgentTaskStarted(sc SpanContext, phase, sessionID, taskID, toolUseID, description, taskType string) {
	if o == nil || !o.enabled {
		return
	}
	data := map[string]any{}
	if taskID != "" {
		data["task_id"] = taskID
	}
	if toolUseID != "" {
		data["tool_use_id"] = toolUseID
	}
	if description != "" {
		data["description"] = description
	}
	if taskType != "" {
		data["task_type"] = taskType
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "agent.task_started",
		Phase:        phase,
		FeatureID:    sc.FeatureID,
		SessionID:    sessionID,
		Data:         data,
	})
}

// AgentTaskProgress emits an agent.task_progress event for a subagent
// (Task tool) invocation still in flight. durationMs/totalTokens/toolUses
// are cumulative since the subagent started, as reported by the SDK.
func (o *Observer) AgentTaskProgress(sc SpanContext, phase, sessionID, taskID, toolUseID, description, lastTool string,
	totalTokens, toolUses int, durationMs int64) {
	if o == nil || !o.enabled {
		return
	}
	data := map[string]any{}
	if taskID != "" {
		data["task_id"] = taskID
	}
	if toolUseID != "" {
		data["tool_use_id"] = toolUseID
	}
	if description != "" {
		data["description"] = description
	}
	if lastTool != "" {
		data["last_tool"] = lastTool
	}
	if totalTokens != 0 {
		data["total_tokens"] = totalTokens
	}
	if toolUses != 0 {
		data["tool_uses"] = toolUses
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "agent.task_progress",
		Phase:        phase,
		FeatureID:    sc.FeatureID,
		SessionID:    sessionID,
		DurationMs:   durationMs,
		Data:         data,
	})
}

// AgentTaskEnded emits an agent.task_ended event when a subagent returns.
// status is the SDK-reported status string (e.g., "completed"); summary is
// the subagent's final summary text.
func (o *Observer) AgentTaskEnded(sc SpanContext, phase, sessionID, taskID, toolUseID, status, summary string,
	totalTokens, toolUses int, durationMs int64) {
	if o == nil || !o.enabled {
		return
	}
	data := map[string]any{}
	if taskID != "" {
		data["task_id"] = taskID
	}
	if toolUseID != "" {
		data["tool_use_id"] = toolUseID
	}
	if summary != "" {
		data["summary"] = summary
	}
	if totalTokens != 0 {
		data["total_tokens"] = totalTokens
	}
	if toolUses != 0 {
		data["tool_uses"] = toolUses
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "agent.task_ended",
		Phase:        phase,
		Status:       status,
		FeatureID:    sc.FeatureID,
		SessionID:    sessionID,
		DurationMs:   durationMs,
		Data:         data,
	})
}

// WriteFeatureSummary produces observe-summary.yaml from feature metadata and events.
func (o *Observer) WriteFeatureSummary(input FeatureSummaryInput) error {
	if o == nil || !o.enabled {
		return nil
	}
	return writeFeatureSummaryImpl(input)
}

// FeatureRewound emits a dedicated audit event for a successful rewind.
// Full rewinds omit roadmap_phase; partial Implement rewinds include roadmap
// phase context plus preserved/redone/discarded labels.
func (o *Observer) FeatureRewound(sc SpanContext, input RewindEventInput) {
	if o == nil || !o.enabled {
		return
	}
	data := map[string]any{
		"rewind_scope":           "full_phase",
		"target_phase":           input.TargetPhase.DirName(),
		"effective_target_phase": input.EffectiveTarget.DirName(),
		"source_run":             input.SourceRun,
		"new_run":                input.NewRun,
	}
	if len(input.CarriedPhases) > 0 {
		data["carried_phases"] = append([]string(nil), input.CarriedPhases...)
	}
	if len(input.BackupBranches) > 0 {
		data["backup_branches"] = copyStringMap(input.BackupBranches)
	}
	if input.RoadmapPhase > 0 {
		data["rewind_scope"] = "partial_roadmap_phase"
		data["roadmap_phase"] = input.RoadmapPhase
		if input.TotalRoadmapPhases > 0 {
			data["total_roadmap_phases"] = input.TotalRoadmapPhases
			data["preserved_roadmap_phases"] = roadmapPhaseRangeLabel(1, input.RoadmapPhase-1)
			data["redone_roadmap_phase"] = roadmapPhaseRangeLabel(input.RoadmapPhase, input.RoadmapPhase)
			data["discarded_roadmap_phases"] = roadmapPhaseRangeLabel(input.RoadmapPhase+1, input.TotalRoadmapPhases)
		}
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "feature.rewound",
		FeatureID:    sc.FeatureID,
		Phase:        input.TargetPhase.String(),
		Data:         data,
	})
}

func copyStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func roadmapPhaseRangeLabel(start, end int) string {
	if start > end {
		return "none"
	}
	if start == end {
		return fmt.Sprintf("Phase %d", start)
	}
	return fmt.Sprintf("Phases %d-%d", start, end)
}

// Shutdown flushes any pending state.
func (o *Observer) Shutdown() error {
	if o == nil || !o.enabled {
		return nil
	}
	return o.otel.Shutdown()
}

// Emit is the generic escape hatch for ad-hoc events that do not have a
// first-class typed helper (e.g. PhaseStarted). It writes the supplied
// Event directly to the per-feature events.jsonl. Returns nil on a nil
// or disabled Observer so callers can treat Emit as always-safe.
//
// Prefer the typed helpers for canonical lifecycle events — Emit is for
// instrumentation metrics (e.g. dropped-message counters) that are
// expected to be rare and don't warrant a new method.
//
// Unlike the typed helpers, Emit does NOT auto-stamp RunNumber from a
// SpanContext — callers are expected to populate evt.RunNumber directly
// when they want the event associated with a specific run. Zero is
// serialised as missing (omitempty), so unstamped events match the
// pre-Phase-4 shape.
func (o *Observer) Emit(evt Event) error {
	if o == nil || !o.enabled {
		return nil
	}
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now()
	}
	return o.emitter.Emit(evt)
}

// ActivePhaseSpanContext returns the SpanContext stored when PhaseStarted was
// called for the given feature. This allows the TUI to emit phase.completed
// on the same span that phase.started used for async interactive phases.
func (o *Observer) ActivePhaseSpanContext(featureID string) (SpanContext, bool) {
	if o == nil || !o.enabled {
		return SpanContext{}, false
	}
	v, ok := o.activePhaseSpans.Load(featureID)
	if !ok {
		return SpanContext{}, false
	}
	return v.(SpanContext), true
}

// ConfigChanged emits a feature.config_changed audit event with before/after
// snapshots of the three editable per-feature config axes (Models,
// Inquireness, Checkpoints). Safe on nil receiver / disabled observer.
// Called from the orchestrator's OnFeatureConfigChanged hook after a
// successful UpdateFeatureConfig mutation.
func (o *Observer) ConfigChanged(sc SpanContext, before, after feature.ConfigSnapshot) {
	if o == nil || !o.enabled {
		return
	}
	o.emit(sc, Event{
		Timestamp:    time.Now(),
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		EventType:    "feature.config_changed",
		FeatureID:    sc.FeatureID,
		Data: map[string]any{
			"before": configSnapshotAttrs(before),
			"after":  configSnapshotAttrs(after),
		},
	})
}

// configSnapshotAttrs flattens a ConfigSnapshot into a plain map so the
// JSONL encoder renders stable keys rather than Go struct tags.
func configSnapshotAttrs(s feature.ConfigSnapshot) map[string]any {
	return map[string]any{
		"models": map[string]any{
			"inquiry":        s.Models.Inquiry,
			"research":       s.Models.Research,
			"planning":       s.Models.Planning,
			"implementation": s.Models.Implementation,
			"review":         s.Models.Review,
			"utilities":      s.Models.Utilities,
			"kb_build":       s.Models.KBBuild,
		},
		"inquireness":         string(s.Inquireness),
		"input_notifications": string(feature.NormalizeInputNotificationsMode(s.InputNotifications)),
		"checkpoints": map[string]any{
			"inquiry_review":    s.Checkpoints.InquiryReview,
			"research_review":   s.Checkpoints.ResearchReview,
			"design_review":     s.Checkpoints.DesignReview,
			"roadmap_review":    s.Checkpoints.RoadmapReview,
			"phase_plan_review": s.Checkpoints.PhasePlanReview,
			"manual_publish":    s.Checkpoints.ManualPublish,
		},
	}
}
