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

package agent

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/autoreview"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// autoReviewBashToolName is the canonical Bash tool name the decorator matches.
const autoReviewBashToolName = "Bash"

const automaticReviewCircuitBreakerReason = "automatic reviewer unavailable after 2 consecutive failures"

// autoReviewPermissionDecorator wraps the fully composed session permission
// policy. It asks the existing handler first and returns every non-empty
// decision or existing error unchanged. Only an empty decision for canonical
// Bash first checks the deterministic guardrail fast path. Fast-path approvals
// need no reviewer, mutex, cache, or circuit-breaker state. Every other
// reviewable command reaches the hidden reviewer when one is available.
// Successful model ALLOW and DEFER classifications are memoized by the
// byte-exact extracted command for this decorator's session. A mutex serializes
// model review while leaving deterministic approvals lock-free. A successful
// ALLOW is the only new model decision; DEFER and every failure return the same
// empty human-deferral decision. The decorator never denies and sits outside
// the CachingHandler, so its allows create no remembered permission rule. The
// hidden reviewer is launched via autoreview.Classify (not BuildSession), so it
// is never decorated and cannot recurse.
type autoReviewPermissionDecorator struct {
	inner            ports.PermissionHandler
	reviewer         autoreview.Reviewer
	workDir          string
	writableRoots    []string
	classify         autoReviewClassifyFunc
	classifyDetailed autoReviewClassifyDetailedFunc
	appendStatus     func(string) error
	observer         *observe.Observer
	stateOnce        sync.Once
	state            *autoReviewSessionState
}

type autoReviewClassifyFunc func(context.Context, autoreview.Reviewer, autoreview.ClassifyRequest) (autoreview.Decision, bool)
type autoReviewClassifyDetailedFunc func(context.Context, autoreview.Reviewer, autoreview.ClassifyRequest) autoreview.Result

// autoReviewSessionState is owned by one decorator and therefore by one
// original provider session. Its exact-command maps never enter the shared
// permission cache or any durable store.
type autoReviewSessionState struct {
	mu                  sync.Mutex
	cached              map[string]autoreview.Decision
	consecutiveFailures int
	unavailable         bool
	disposed            bool
}

func newAutoReviewSessionState() *autoReviewSessionState {
	return &autoReviewSessionState{
		cached: make(map[string]autoreview.Decision),
	}
}

func (d *autoReviewPermissionDecorator) sessionState() *autoReviewSessionState {
	d.stateOnce.Do(func() {
		d.state = newAutoReviewSessionState()
	})
	return d.state
}

// Dispose discards all automatic-review state when the owning session ends.
func (d *autoReviewPermissionDecorator) Dispose() {
	d.sessionState().dispose()
}

func (s *autoReviewSessionState) dispose() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.disposed {
		return
	}
	s.disposed = true
	clear(s.cached)
	s.unavailable = true
}

func (s *autoReviewSessionState) isDisposed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.disposed
}

func (s *autoReviewSessionState) review(ctx context.Context, command string, classify func(context.Context) (autoreview.Decision, bool)) (autoreview.Decision, bool, bool) {
	if ctx.Err() != nil {
		return "", false, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return "", false, false
	}
	if s.disposed || s.unavailable {
		return "", false, false
	}
	if decision, ok := s.cached[command]; ok {
		if ctx.Err() != nil {
			return "", false, false
		}
		return decision, true, false
	}

	decision, ok := classify(ctx)
	if ctx.Err() != nil {
		decision, ok = "", false
	}
	if ok && (decision == autoreview.Allow || decision == autoreview.Defer) {
		s.cached[command] = decision
		s.consecutiveFailures = 0
		return decision, true, false
	}
	s.consecutiveFailures++
	if s.consecutiveFailures >= 2 {
		s.unavailable = true
		return "", false, true
	}
	return "", false, false
}

func (d *autoReviewPermissionDecorator) CanUseTool(req ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	decision, err := d.inner.CanUseTool(req)
	if err != nil {
		return decision, err
	}
	if decision.Behavior != "" {
		return decision, nil
	}
	if req.ToolName != autoReviewBashToolName {
		return decision, nil
	}
	if d.sessionState().isDisposed() {
		return decision, nil
	}
	command := permission.ExtractBashCommand(req.Input)
	if permission.GuardrailFastPath(command, d.workDir, d.writableRoots) {
		d.recordFastPathAllow(req, command)
		return ports.PermissionDecision{Behavior: permission.DecisionAllow}, nil
	}
	if !permission.ReviewableBashCommand(command) {
		return decision, nil
	}
	if d.reviewer.Provider == nil {
		d.persistAutomaticReviewStatus(req, permission.AutomaticReviewFailureStatusLine(command, "unavailable"))
		return decision, nil
	}
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	classificationRan := false
	result, ok, breakerTripped := d.sessionState().review(ctx, command, func(classifyCtx context.Context) (autoreview.Decision, bool) {
		classificationRan = true
		startedAt := time.Now()
		classifyReq := autoreview.ClassifyRequest{
			ToolName:      req.ToolName,
			Command:       command,
			WorkDir:       d.workDir,
			WritableRoots: d.writableRoots,
		}
		var detailed autoreview.Result
		switch {
		case d.classifyDetailed != nil:
			detailed = d.classifyDetailed(classifyCtx, d.reviewer, classifyReq)
		case d.classify != nil:
			decision, classified := d.classify(classifyCtx, d.reviewer, classifyReq)
			detailed = compatibilityAutoReviewResult(decision, classified)
		default:
			detailed = autoreview.ClassifyDetailed(classifyCtx, d.reviewer, classifyReq)
		}
		if err := classifyCtx.Err(); err != nil {
			if err == context.DeadlineExceeded {
				detailed = autoreview.Result{Outcome: autoreview.OutcomeTimeout, FailureReason: "review attempt timed out"}
			} else {
				detailed = autoreview.Result{Outcome: autoreview.OutcomeCanceled, FailureReason: "review attempt canceled"}
			}
		}

		var statusPersisted *bool
		statusFailureClass, statusFailureReason := "", ""
		if classifyCtx.Err() == nil {
			var status string
			switch detailed.Outcome {
			case autoreview.OutcomeAllow:
				status = permission.AutomaticReviewStatusLine(command)
			case autoreview.OutcomeDefer:
				status = permission.AutomaticReviewDeferStatusLine(command)
			default:
				status = permission.AutomaticReviewFailureStatusLine(command, string(detailed.Outcome))
			}
			statusPersisted, statusFailureClass, statusFailureReason = d.persistAutomaticReviewStatus(req, status)
		}
		d.emitAutomaticReview(req, detailed, time.Since(startedAt), statusPersisted, statusFailureClass, statusFailureReason)
		return detailed.Decision, detailed.Outcome == autoreview.OutcomeAllow || detailed.Outcome == autoreview.OutcomeDefer
	})
	if breakerTripped {
		d.emitReviewerUnavailable(req, "circuit_breaker", automaticReviewCircuitBreakerReason)
	}
	if ok && result == autoreview.Defer {
		// Fresh classifications already append this status inside the callback.
		// Cached DEFER decisions still need one explanation for each human prompt.
		if !classificationRan {
			d.persistAutomaticReviewStatus(req, permission.AutomaticReviewDeferStatusLine(command))
		}
	}
	if !ok && !classificationRan && ctx.Err() == nil {
		d.persistAutomaticReviewStatus(req, permission.AutomaticReviewFailureStatusLine(command, "unavailable"))
	}
	if !ok || result != autoreview.Allow {
		return decision, nil
	}
	return ports.PermissionDecision{Behavior: permission.DecisionAllow}, nil
}

func (d *autoReviewPermissionDecorator) recordFastPathAllow(req ports.ToolPermissionRequest, command string) {
	statusPersisted, statusFailureClass, statusFailureReason := d.persistAutomaticReviewStatus(
		req,
		permission.AutomaticReviewFastPathStatusLine(command),
	)
	d.emitAutomaticReviewEvent(
		req,
		autoreview.Result{Outcome: autoreview.OutcomeFastPath},
		0,
		statusPersisted,
		statusFailureClass,
		statusFailureReason,
		"",
		"",
	)
}

func (d *autoReviewPermissionDecorator) persistAutomaticReviewStatus(
	req ports.ToolPermissionRequest,
	status string,
) (*bool, string, string) {
	persisted := false
	appendStatus := d.appendStatus
	if appendStatus == nil {
		appendStatus = req.AppendStatus
	}
	if appendStatus == nil {
		return &persisted, "unavailable", "session status sink unavailable"
	}
	if err := appendStatus(status); err != nil {
		return &persisted, "append_error", permission.AutomaticReviewBoundReason(err.Error())
	}
	persisted = true
	return &persisted, "", ""
}

func (d *autoReviewPermissionDecorator) emitReviewerUnavailable(req ports.ToolPermissionRequest, scope, reason string) {
	if d.observer == nil {
		return
	}
	sc, ok := d.observer.ActivePhaseSpanContext(req.FeatureID)
	if !ok {
		sc = observe.SpanContextForFeature(req.FeatureID, "", "", "")
	}
	sessionID := req.LogicalSessionID
	if sessionID == "" {
		sessionID = req.SessionID
	}
	d.observer.AutomaticReviewUnavailable(sc, observe.AutomaticReviewUnavailableEventInput{
		Phase:     strings.ToLower(req.Phase.String()),
		SessionID: sessionID,
		RepoName:  req.RepoName,
		Iteration: req.Iteration,
		Scope:     scope,
		Reason:    reason,
	})
}

func compatibilityAutoReviewResult(decision autoreview.Decision, ok bool) autoreview.Result {
	if !ok {
		return autoreview.Result{Outcome: autoreview.OutcomeMalformedResponse, FailureReason: "review classification failed"}
	}
	if decision == autoreview.Allow {
		return autoreview.Result{Decision: decision, Outcome: autoreview.OutcomeAllow}
	}
	return autoreview.Result{Decision: decision, Outcome: autoreview.OutcomeDefer}
}

func (d *autoReviewPermissionDecorator) emitAutomaticReview(
	req ports.ToolPermissionRequest,
	result autoreview.Result,
	duration time.Duration,
	statusPersisted *bool,
	statusFailureClass string,
	statusFailureReason string,
) {
	provider, model := d.reviewer.Identity()
	d.emitAutomaticReviewEvent(
		req,
		result,
		duration,
		statusPersisted,
		statusFailureClass,
		statusFailureReason,
		provider,
		model,
	)
}

func (d *autoReviewPermissionDecorator) emitAutomaticReviewEvent(
	req ports.ToolPermissionRequest,
	result autoreview.Result,
	duration time.Duration,
	statusPersisted *bool,
	statusFailureClass string,
	statusFailureReason string,
	provider string,
	model string,
) {
	if d.observer == nil {
		return
	}
	sc, ok := d.observer.ActivePhaseSpanContext(req.FeatureID)
	if !ok {
		sc = observe.SpanContextForFeature(req.FeatureID, "", "", "")
	}
	sessionID := req.LogicalSessionID
	if sessionID == "" {
		sessionID = req.SessionID
	}
	d.observer.AutomaticReviewCompleted(sc, observe.AutomaticReviewEventInput{
		Phase:               strings.ToLower(req.Phase.String()),
		SessionID:           sessionID,
		RepoName:            req.RepoName,
		Iteration:           req.Iteration,
		Provider:            provider,
		Model:               model,
		Outcome:             result.Outcome,
		Duration:            duration,
		CommandSummary:      permission.AutomaticReviewCommandSummary(permission.ExtractBashCommand(req.Input)),
		FailureReason:       permission.AutomaticReviewBoundReason(result.FailureReason),
		StatusPersisted:     statusPersisted,
		StatusFailureClass:  statusFailureClass,
		StatusFailureReason: statusFailureReason,
	})
}
