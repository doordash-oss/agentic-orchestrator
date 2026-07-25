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

// autoReviewPermissionDecorator wraps the fully composed session permission
// policy. It asks the existing handler first and returns every non-empty
// decision or existing error unchanged. Only an empty decision for canonical
// Bash, when the session's snapshotted flag is enabled and reviewer is usable,
// permits a hidden classification. Successful ALLOW and DEFER classifications
// are memoized by the byte-exact extracted command for this decorator's
// session, and concurrent identical classifications share one in-flight
// attempt. The guardrail classifier determines eligibility: it parses the
// command structurally and checks it against the curated development-command
// policy. A successful ALLOW is the only new automatic decision; DEFER and
// every failure return the same empty human-deferral decision. The decorator
// sits outside the CachingHandler, so its allow bypasses the cache and creates
// no remembered rule, cache entry, or audit event. The hidden reviewer is
// launched via autoreview.Classify (not BuildSession), so it is never decorated
// and cannot recurse.
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
	mu       sync.Mutex
	cached   map[string]autoreview.Decision
	inFlight map[string]*autoReviewFlight
	disposed bool
}

type autoReviewFlight struct {
	done     chan struct{}
	decision autoreview.Decision
	ok       bool
}

func newAutoReviewSessionState() *autoReviewSessionState {
	return &autoReviewSessionState{
		cached:   make(map[string]autoreview.Decision),
		inFlight: make(map[string]*autoReviewFlight),
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
	for command, flight := range s.inFlight {
		flight.decision = ""
		flight.ok = false
		delete(s.inFlight, command)
		close(flight.done)
	}
}

func (s *autoReviewSessionState) review(ctx context.Context, command string, classify func(context.Context) (autoreview.Decision, bool)) (autoreview.Decision, bool) {
	if ctx.Err() != nil {
		return "", false
	}
	s.mu.Lock()
	if s.disposed {
		s.mu.Unlock()
		return "", false
	}
	if decision, ok := s.cached[command]; ok {
		s.mu.Unlock()
		if ctx.Err() != nil {
			return "", false
		}
		return decision, true
	}
	if flight, ok := s.inFlight[command]; ok {
		s.mu.Unlock()
		if ctx.Err() != nil {
			return "", false
		}
		select {
		case <-ctx.Done():
			return "", false
		case <-flight.done:
			if ctx.Err() != nil {
				return "", false
			}
			return flight.decision, flight.ok
		}
	}
	flight := &autoReviewFlight{done: make(chan struct{})}
	s.inFlight[command] = flight
	s.mu.Unlock()

	decision, ok := classify(ctx)

	s.mu.Lock()
	if s.disposed || s.inFlight[command] != flight {
		s.mu.Unlock()
		return "", false
	}
	flight.decision = decision
	flight.ok = ok
	if ok && (decision == autoreview.Allow || decision == autoreview.Defer) {
		s.cached[command] = decision
	}
	delete(s.inFlight, command)
	close(flight.done)
	s.mu.Unlock()
	return decision, ok
}

func (d *autoReviewPermissionDecorator) CanUseTool(req ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	decision, err := d.inner.CanUseTool(req)
	if err != nil {
		return decision, err
	}
	if decision.Behavior != "" {
		return decision, nil
	}
	if d.reviewer.Provider == nil || req.ToolName != autoReviewBashToolName {
		return decision, nil
	}
	command := permission.ExtractBashCommand(req.Input)
	if !permission.GuardrailClassify(command, d.workDir, d.writableRoots) {
		return decision, nil
	}
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	result, ok := d.sessionState().review(ctx, command, func(classifyCtx context.Context) (autoreview.Decision, bool) {
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
		statusFailureClass := ""
		statusFailureReason := ""
		if detailed.Outcome == autoreview.OutcomeAllow && classifyCtx.Err() == nil {
			persisted := false
			statusPersisted = &persisted
			appendStatus := d.appendStatus
			if appendStatus == nil {
				appendStatus = req.AppendStatus
			}
			switch {
			case appendStatus == nil:
				statusFailureClass = "unavailable"
				statusFailureReason = "session status sink unavailable"
			default:
				if err := appendStatus(permission.AutomaticReviewStatusLine(command)); err != nil {
					statusFailureClass = "append_error"
					statusFailureReason = permission.AutomaticReviewBoundReason(err.Error())
				} else {
					persisted = true
				}
			}
		}
		d.emitAutomaticReview(req, detailed, time.Since(startedAt), statusPersisted, statusFailureClass, statusFailureReason)
		return detailed.Decision, detailed.Outcome == autoreview.OutcomeAllow || detailed.Outcome == autoreview.OutcomeDefer
	})
	if !ok || result != autoreview.Allow {
		return decision, nil
	}
	return ports.PermissionDecision{Behavior: permission.DecisionAllow}, nil
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
	if d.observer == nil {
		return
	}
	sc, ok := d.observer.ActivePhaseSpanContext(req.FeatureID)
	if !ok {
		sc = observe.SpanContextForFeature(req.FeatureID, "", "", "")
	}
	provider, model := d.reviewer.Identity()
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
