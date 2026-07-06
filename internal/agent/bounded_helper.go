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
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const (
	BoundedHelperStatusCompleted          = "completed"
	BoundedHelperStatusTimedOut           = "timed_out"
	BoundedHelperStatusAskedUser          = "asked_user"
	BoundedHelperStatusPermissionRequired = "permission_required"
	BoundedHelperStatusFailed             = "failed"
	BoundedHelperStatusEmptyOutput        = "empty_output"
	BoundedHelperStatusProtocolViolation  = "protocol_violation"
)

// BoundedHelperConfig configures a single-turn interactive helper run.
type BoundedHelperConfig struct {
	SessionID      string
	FeatureID      string
	Phase          feature.Phase
	Label          string
	ObserverPhase  string
	Model          string
	Prompt         string
	SystemPrompt   string
	ResponsePath   string
	WorkDir        string
	RepoName       string
	AdditionalDirs []string
	WritableRoots  []string
	LogPath        string
	Timeout        time.Duration
	EffortLevel    llm.EffortLevel
	PermHandler    ports.PermissionHandler
	RequireOutput  bool
	// PhaseCompleteDir opts this helper into local phase_complete semantics.
	// Empty preserves markerless bounded-helper behavior.
	PhaseCompleteDir string
	ContractPhase    feature.Phase
	ContractRole     Role
	ParentSpanCtx    observe.SpanContext
}

// BoundedHelperResult captures the output and terminal state of a bounded helper run.
type BoundedHelperResult struct {
	Output string
	Status string
	Result *llm.ResultMessage
	Usage  llm.Usage
}

// RunBoundedHelper runs a single-turn interactive helper session without phase_complete semantics.
func (pr *PhaseRunner) RunBoundedHelper(ctx context.Context, cfg BoundedHelperConfig) (*BoundedHelperResult, error) {
	if cfg.SessionID == "" {
		return nil, fmt.Errorf("running bounded helper: missing session id")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("running bounded helper: missing model")
	}
	if cfg.WorkDir == "" {
		return nil, fmt.Errorf("running bounded helper: missing work dir")
	}

	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}

	pidDir := pr.StateDir
	if cfg.FeatureID != "" {
		pidDir = filepath.Join(pr.StateDir, cfg.FeatureID)
	}

	markerPath := ""
	if cfg.PhaseCompleteDir != "" {
		markerPath = filepath.Join(cfg.PhaseCompleteDir, PhaseCompleteFile)
	}
	cmd, env, sessOpts, err := pr.BuildSession(BuildSessionOpts{
		Model:          cfg.Model,
		Prompt:         cfg.Prompt,
		SystemPrompt:   cfg.SystemPrompt,
		AdditionalDirs: cfg.AdditionalDirs,
		WritableRoots:  cfg.WritableRoots,
		PIDDir:         pidDir,
		PermHandler:    cfg.PermHandler,
		RepoName:       cfg.RepoName,
		WorkDir:        cfg.WorkDir,
		LogPath:        cfg.LogPath,
		EffortLevel:    cfg.EffortLevel,
		AgentNames:     []string{},
		Phase:          cfg.Phase,
		MarkerPath:     markerPath,
	})
	if err != nil {
		return nil, fmt.Errorf("running bounded helper: building session: %w", err)
	}

	return pr.runBoundedHelperSession(ctx, boundedHelperRunConfig{
		sessionID:        cfg.SessionID,
		featureID:        cfg.FeatureID,
		phase:            cfg.Phase,
		label:            cfg.Label,
		observerPhase:    cfg.ObserverPhase,
		model:            cfg.Model,
		responsePath:     cfg.ResponsePath,
		repoName:         cfg.RepoName,
		workDir:          cfg.WorkDir,
		command:          cmd,
		env:              env,
		sessOpts:         sessOpts,
		requireOutput:    cfg.RequireOutput,
		phaseCompleteDir: cfg.PhaseCompleteDir,
		contractPhase:    cfg.ContractPhase,
		contractRole:     cfg.ContractRole,
		parentSpanCtx:    cfg.ParentSpanCtx,
	})
}

type boundedHelperRunConfig struct {
	sessionID        string
	featureID        string
	phase            feature.Phase
	label            string
	observerPhase    string
	model            string
	responsePath     string
	repoName         string
	workDir          string
	command          []string
	env              []string
	sessOpts         *ports.SessionOpts
	requireOutput    bool
	phaseCompleteDir string
	contractPhase    feature.Phase
	contractRole     Role
	parentSpanCtx    observe.SpanContext
	// finishOrViolateNudge arms the finish-or-violate auto-continuation retry
	// in the statusCh arm: when the helper ends its turn without its required
	// completion artifacts, it is nudged on the same live session before the
	// run is finalized as a protocol violation. Resolved per-model from the
	// provider capability.
	finishOrViolateNudge bool
}

func (pr *PhaseRunner) runBoundedHelperSession(ctx context.Context, cfg boundedHelperRunConfig) (*BoundedHelperResult, error) {
	label := cfg.label
	if label == "" {
		label = "bounded helper"
	}

	sess, err := pr.SessionManager.StartSession(cfg.sessionID, cfg.featureID, cfg.phase, cfg.command, cfg.workDir, cfg.env, cfg.sessOpts)
	if err != nil {
		return nil, fmt.Errorf("running %s: starting session: %w", label, err)
	}

	sessionCtx := observe.SpanContext{}
	sessionStart := time.Now()
	observerPhase := cfg.observerPhase
	if observerPhase == "" {
		observerPhase = label
	}
	if pr.Observer != nil {
		if cfg.parentSpanCtx.TraceID != "" || cfg.parentSpanCtx.SpanID != "" || cfg.parentSpanCtx.FeatureID != "" {
			sessionCtx = cfg.parentSpanCtx.Child()
		} else {
			featureCtx := observe.SpanContextForFeature(cfg.featureID, "", "", "")
			sessionCtx = featureCtx.Child()
		}
		providerName := ""
		if cfg.sessOpts != nil {
			providerName = cfg.sessOpts.ProviderName
		}
		pr.Observer.SessionStarted(sessionCtx, observerPhase, cfg.sessionID, providerName, cfg.model, cfg.repoName)
		pr.installContextReadTracker(sess, sessionCtx, observerPhase, cfg.sessionID, pr.StateDir)
		pr.installSubagentProgressTracker(sess, sessionCtx, observerPhase, cfg.sessionID)
	}

	defer func() {
		cost := ExtractSessionCost(sess)
		_ = accumulateSessionCostToFeature(pr.FeatureStore, cfg.featureID, observerPhase, cost, SessionCostMetadata{
			SessionID:     cfg.sessionID,
			ObserverPhase: observerPhase,
			RepoName:      cfg.repoName,
		})
		if pr.Observer != nil {
			pr.Observer.SessionEnded(sessionCtx, observerPhase, cfg.sessionID, cfg.repoName, toSessionUsage(cost), time.Since(sessionStart), sessionErrFromStatus(sess))
		}
		_ = sess.Stop()
		sess.Wait()
	}()

	attachCh := sess.AttachCh()
	statusCh := sess.StatusCh()
	if registrar, ok := sess.(ports.AttachConsumerRegistrar); ok {
		unregister := registrar.RegisterAttachConsumer()
		defer unregister()
	}

	// Counts finish-or-violate nudges for this invocation; bounded by
	// maxFinishOrViolateNudges and armed only when cfg.finishOrViolateNudge is
	// set (capability-positive providers).
	finishOrViolateNudges := 0

	for {
		select {
		case <-ctx.Done():
			result := boundedHelperSnapshot(cfg.responsePath, sess, BoundedHelperStatusTimedOut)
			return result, fmt.Errorf("running %s: %w", label, ctx.Err())

		case msg, ok := <-attachCh:
			if !ok {
				attachCh = nil
				continue
			}
			if msg.ControlRequest == nil {
				continue
			}
			if msg.ControlRequest.Request.ToolName == "AskUserQuestion" {
				result := boundedHelperSnapshot(cfg.responsePath, sess, BoundedHelperStatusAskedUser)
				return result, fmt.Errorf("running %s: helper asked for user input", label)
			}
			result := boundedHelperSnapshot(cfg.responsePath, sess, BoundedHelperStatusPermissionRequired)
			return result, fmt.Errorf("running %s: helper requested tool permission for %s", label, msg.ControlRequest.Request.ToolName)

		case <-statusCh:
			// Peek at why the turn ended (mirroring finalizeBoundedHelperResult's
			// classification). For capability-positive providers, nudge the same
			// live session to finish when it ended its turn without writing the
			// completion artifacts, before finalizing as a protocol violation.
			if cfg.finishOrViolateNudge && boundedHelperShouldNudge(sess, cfg.phaseCompleteDir) {
				if decideFinishOrViolate(sess, llm.TermEndedAfterText, &finishOrViolateNudges, []string{PhaseCompleteFile}) {
					continue
				}
			}
			return finalizeBoundedHelperResult(cfg.responsePath, sess, label, cfg.requireOutput, cfg.phaseCompleteDir, cfg.contractPhase, cfg.contractRole)

		case <-sess.Done():
			// The process already exited; there is nothing to nudge. Never unify
			// this arm with the statusCh arm above.
			return finalizeBoundedHelperResult(cfg.responsePath, sess, label, cfg.requireOutput, cfg.phaseCompleteDir, cfg.contractPhase, cfg.contractRole)
		}
	}
}

// boundedHelperShouldNudge reports whether the just-ended turn is a deliberate
// end_turn without the completion marker — the only case the finish-or-violate
// nudge fires for. It mirrors finalizeBoundedHelperResult's inputs so the nudge
// and the finalize verdict classify the turn identically.
func boundedHelperShouldNudge(sess ports.SessionHandle, phaseCompleteDir string) bool {
	if sess.HasPendingAskUserQuestion() || sess.LastControlRequest() != nil {
		return false
	}
	result := sess.Cost()
	if result == nil {
		return false
	}
	phaseCompleteExists := false
	if phaseCompleteDir != "" {
		phaseCompleteExists = HasPhaseComplete(phaseCompleteDir)
	}
	class := llm.ClassifyTermination(llm.TerminationInputs{
		Result:                 result,
		PhaseCompleteExists:    phaseCompleteExists,
		AskUserQuestionPending: false,
	})
	return class == llm.TermEndedAfterText && !phaseCompleteExists
}

func finalizeBoundedHelperResult(responsePath string, sess ports.SessionHandle, label string, requireOutput bool, phaseCompleteDir string, contractPhase feature.Phase, contractRole Role) (*BoundedHelperResult, error) {
	if sess.HasPendingAskUserQuestion() {
		result := boundedHelperSnapshot(responsePath, sess, BoundedHelperStatusAskedUser)
		return result, fmt.Errorf("running %s: helper asked for user input", label)
	}
	if req := sess.LastControlRequest(); req != nil {
		status := BoundedHelperStatusPermissionRequired
		errText := fmt.Sprintf("running %s: helper requested tool permission for %s", label, req.Request.ToolName)
		if req.Request.ToolName == "AskUserQuestion" {
			status = BoundedHelperStatusAskedUser
			errText = fmt.Sprintf("running %s: helper asked for user input", label)
		}
		result := boundedHelperSnapshot(responsePath, sess, status)
		return result, fmt.Errorf("%s", errText)
	}

	result := boundedHelperSnapshot(responsePath, sess, BoundedHelperStatusCompleted)
	if result.Result == nil {
		result.Status = BoundedHelperStatusFailed
		return result, fmt.Errorf("running %s: helper ended without a result", label)
	}

	phaseCompleteExists := false
	if phaseCompleteDir != "" {
		phaseCompleteExists = HasPhaseComplete(phaseCompleteDir)
	}
	class := llm.ClassifyTermination(llm.TerminationInputs{
		Result:                 result.Result,
		PhaseCompleteExists:    phaseCompleteExists,
		AskUserQuestionPending: false,
	})

	switch class {
	case llm.TermCompleted:
		if requireOutput && strings.TrimSpace(result.Output) == "" {
			result.Status = BoundedHelperStatusEmptyOutput
			return result, fmt.Errorf("running %s: helper completed without output", label)
		}
		if contractRole != "" {
			outcome, violations, err := Validate(contractPhase, contractRole, phaseCompleteDir)
			if err != nil {
				result.Status = BoundedHelperStatusFailed
				return result, fmt.Errorf("running %s: validating helper contract: %w", label, err)
			}
			if !outcome.OK {
				result.Status = BoundedHelperStatusProtocolViolation
				return result, newProtocolViolationError(contractRole, phaseCompleteDir, violations)
			}
		}
		return result, nil
	case llm.TermEndedAfterText:
		if phaseCompleteDir != "" {
			result.Status = BoundedHelperStatusProtocolViolation
			return result, newProtocolViolationError(contractRole, phaseCompleteDir, []ProtocolViolation{{
				Artifact: PhaseCompleteFile,
				Reason:   fmt.Sprintf("%s is missing from helper output directory", PhaseCompleteFile),
			}})
		}
		if requireOutput && strings.TrimSpace(result.Output) == "" {
			result.Status = BoundedHelperStatusEmptyOutput
			return result, fmt.Errorf("running %s: helper completed without output", label)
		}
		return result, nil
	case llm.TermAskedFormal:
		result.Status = BoundedHelperStatusAskedUser
		return result, fmt.Errorf("running %s: helper asked for user input", label)
	case llm.TermErrored:
		result.Status = BoundedHelperStatusFailed
		return result, fmt.Errorf("running %s: helper returned an error result", label)
	case llm.TermRefused:
		result.Status = BoundedHelperStatusFailed
		return result, fmt.Errorf("running %s: helper refused the request", label)
	case llm.TermTurnTruncated:
		result.Status = BoundedHelperStatusFailed
		return result, fmt.Errorf("running %s: helper turn ended before completion", label)
	default:
		result.Status = BoundedHelperStatusFailed
		return result, fmt.Errorf("running %s: helper ended in an unknown state", label)
	}
}

func boundedHelperSnapshot(responsePath string, sess ports.SessionHandle, status string) *BoundedHelperResult {
	return &BoundedHelperResult{
		Output: strings.TrimSpace(readSessionOutput(responsePath, sess)),
		Status: status,
		Result: sess.Cost(),
		Usage:  sess.AccumulatedUsage(),
	}
}
