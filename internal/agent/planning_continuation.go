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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const (
	planningAgentStatusInterrupted              = "INTERRUPTED"
	planningAgentStatusSafetyRail               = "SAFETY_RAIL"
	planningAgentStatusHandoffProtocolViolation = "HANDOFF_PROTOCOL_VIOLATION"
	defaultPlanningMaxConsecutiveFailures       = 3
	defaultPlanningMaxConsecutiveNoProgress     = 3
)

type planningContinuationInput struct {
	Config          PlanLoopConfig
	SessionManager  ports.SessionManager
	Attempt         int
	AttemptDir      string
	ArtifactDir     string
	Prompt          string
	SystemPrompt    string
	PlannerSpec     RoleSpec
	SessionIDBase   string
	Model           string
	CostKey         string
	AutoPickPurpose ports.AskUserAutoPickPurpose
	CanonicalPath   string
}

type planningContinuationOutcome struct {
	AgentStatus       string
	Iterations        int
	LastError         string
	HandoffViolations []string
	QALog             []ports.QAPair
}

type planningSafetyRailError struct {
	message string
}

func (e planningSafetyRailError) Error() string {
	return e.message
}

func isPlanningSafetyRailError(err error) bool {
	var target planningSafetyRailError
	return errors.As(err, &target)
}

func handoffProtocolViolations(violations []string) []ProtocolViolation {
	if len(violations) == 0 {
		return []ProtocolViolation{{
			Artifact: PlanningHandoffFilename,
			Reason:   "planning handoff did not satisfy the continuation contract",
		}}
	}
	out := make([]ProtocolViolation, 0, len(violations))
	for _, violation := range violations {
		out = append(out, ProtocolViolation{
			Artifact: PlanningHandoffFilename,
			Reason:   violation,
		})
	}
	return out
}

func runPlanningSessionWithContinuations(in planningContinuationInput) (planningContinuationOutcome, error) {
	cfg := in.Config
	handoffPath := filepath.Join(in.AttemptDir, PlanningHandoffFilename)
	_ = os.Remove(handoffPath)
	tracker := NewProgressTracker()
	maxNoProgress := cfg.MaxConsecNoProgress
	if maxNoProgress <= 0 {
		maxNoProgress = defaultPlanningMaxConsecutiveNoProgress
	}
	maxFailures := cfg.MaxConsecFails
	if maxFailures <= 0 {
		maxFailures = defaultPlanningMaxConsecutiveFailures
	}

	var allQA []ports.QAPair
	var nextPrompt string
	consecutiveHandoffFailures := 0
	for continuation := 0; ; continuation++ {
		prompt := in.Prompt
		if nextPrompt != "" {
			prompt = nextPrompt
			nextPrompt = ""
		} else if continuation > 0 {
			prompt = buildPlanningContinuationPrompt(handoffPath, in.CanonicalPath)
		}

		RemovePhaseComplete(in.AttemptDir)
		cmd, env, sessOpts, err := cfg.BuildSession(BuildSessionOpts{
			Model:                          in.Model,
			Prompt:                         prompt,
			SystemPrompt:                   in.SystemPrompt,
			AdditionalDirs:                 additionalDirsOrState(cfg),
			AgentNames:                     []string{},
			PIDDir:                         filepath.Join(cfg.StateDir, cfg.Feature.ID),
			PermHandler:                    permHandlerFor(cfg.DangerouslySkipPermissions, cfg.PermissionCache, cfg.RepoName),
			WorkDir:                        cfg.WorkDir,
			EffortLevel:                    cfg.EffortLevel,
			Phase:                          feature.PhasePlan,
			SystemPromptHasUsefulResources: true,
			MarkerPath:                     filepath.Join(in.AttemptDir, PhaseCompleteFile),
		})
		if err != nil {
			return planningContinuationOutcome{}, fmt.Errorf("building planning session (attempt %d): %w", in.Attempt, err)
		}
		sessOpts = enableTruncatedTurnAutoResume(sessOpts)
		WriteDebugPrompts(in.AttemptDir, sessOpts.DebugSystemPrompt, prompt)
		sessOpts.PermCacheScope = cfg.RepoName

		sessionID := in.SessionIDBase
		if continuation > 0 {
			sessionID = fmt.Sprintf("%s-c%02d", in.SessionIDBase, continuation+1)
		}
		planSessionCtx := cfg.PhaseSpanCtx.Child()
		if in.AutoPickPurpose != ports.AskUserAutoPickPurposeNone {
			sessOpts.AskUserAutoPick = askUserAutoPickConfig(
				cfg.FeatureStore,
				cfg.Observer,
				cfg.Feature,
				in.AutoPickPurpose,
				planSessionCtx,
				sessionID,
				cfg.RepoName,
				0,
			)
		}

		startSession := cfg.SessionStartFunc
		if startSession == nil {
			if in.SessionManager == nil {
				return planningContinuationOutcome{}, fmt.Errorf("starting planning session (attempt %d): session manager is nil", in.Attempt)
			}
			startSession = in.SessionManager.StartSession
		}
		sess, err := startSession(sessionID, cfg.Feature.ID, feature.PhasePlan, cmd, cfg.WorkDir, env, sessOpts)
		if err != nil {
			if errors.Is(err, ports.ErrSessionShuttingDown) {
				return planningContinuationOutcome{AgentStatus: planningAgentStatusInterrupted, Iterations: in.Attempt - 1}, nil
			}
			return planningContinuationOutcome{}, fmt.Errorf("starting planning session (attempt %d): %w", in.Attempt, err)
		}

		providerName := ""
		if sessOpts != nil {
			providerName = sessOpts.ProviderName
		}
		cfg.Observer.SessionStarted(planSessionCtx, "plan", sessionID, providerName, in.Model, cfg.RepoName)
		(&ContextReadTracker{
			KBBaseDir:     filepath.Join(filepath.Dir(cfg.StateDir), "knowledge-base"),
			SkillsDir:     cfg.SkillsDir,
			GuidelinesDir: cfg.GuidelinesDir,
			Observer:      cfg.Observer,
		}).Install(sess, planSessionCtx, "plan", sessionID)
		sessionStart := time.Now()

		logPath := filepath.Join(in.AttemptDir, "output.txt")
		logFile, err := os.Create(logPath)
		if err == nil {
			sess.SetLogFile(logFile)
		}

		waitResult := waitForStatusDetailed(sess, in.SessionManager, sessionID, waitForStatusOptions{
			ReadyCheck: func() bool {
				if HasPhaseComplete(in.AttemptDir) {
					sess.SetHasUnansweredQuestion(false)
					return true
				}
				return false
			},
			EnableContextHandoff:          true,
			ContextHandoffDisabled:        sessOpts.ContextHandoffDisabled,
			ContextHandoffThresholdTokens: sessOpts.ContextHandoffThresholdTokens,
			ContextHandoffRole:            in.PlannerSpec.SkillName,
			OnContextHandoff: func(snap contextSnapshot) {
				cfg.Observer.ContextHandoffTriggered(
					planSessionCtx,
					"plan",
					sessionID,
					cfg.RepoName,
					providerName,
					in.Attempt,
					snap.Pct,
					snap.ThresholdPct,
					snap.ThresholdTokens,
					snap.TotalTokens,
					snap.WindowTokens,
					snap.BaselineTokens,
				)
			},
		})
		agentStatus := waitResult.Status

		cost := ExtractSessionCost(sess)
		if cfg.FeatureStore != nil && cost.TotalCostUSD > 0 {
			costKey := in.CostKey
			_ = cfg.FeatureStore.Modify(cfg.Feature.ID, func(f *feature.Feature) error {
				f.AddPhaseCost(costKey, cost.TotalCostUSD)
				return nil
			})
		}

		cfg.Observer.SessionEnded(planSessionCtx, "plan", sessionID, cfg.RepoName,
			toSessionUsage(cost), time.Since(sessionStart), sessionErrFromAgentStatus(agentStatus))

		output := sess.MessageLog().Text()
		_ = os.WriteFile(logPath, []byte(output), 0o644)
		allQA = append(allQA, sess.QALog()...)

		if agentStatus != agentStatusSuccess {
			return planningContinuationOutcome{AgentStatus: agentStatus, QALog: allQA}, nil
		}

		if _, err := os.Stat(handoffPath); err != nil {
			if os.IsNotExist(err) {
				return planningContinuationOutcome{AgentStatus: agentStatusSuccess, QALog: allQA}, nil
			}
			return planningContinuationOutcome{}, fmt.Errorf("stat planning handoff: %w", err)
		}
		parsed, err := ParsePlanningHandoffMd(handoffPath)
		if err != nil {
			return planningContinuationOutcome{}, err
		}
		if !parsed.OK() {
			consecutiveHandoffFailures++
			violations := append([]string(nil), parsed.ProtocolViolations...)
			if consecutiveHandoffFailures >= maxFailures {
				return planningContinuationOutcome{
					AgentStatus:       planningAgentStatusSafetyRail,
					Iterations:        in.Attempt,
					LastError:         fmt.Sprintf("planning handoff protocol violation repeated %d consecutive times: %s", consecutiveHandoffFailures, strings.Join(violations, "; ")),
					HandoffViolations: violations,
					QALog:             allQA,
				}, nil
			}
			nextPrompt = buildPlanningHandoffRepairPrompt(handoffPath, in.CanonicalPath, violations)
			continue
		}
		consecutiveHandoffFailures = 0
		if parsed.State == PlanningHandoffComplete {
			return planningContinuationOutcome{
				AgentStatus: agentStatusSuccess,
				QALog:       allQA,
			}, nil
		}

		progressMade, err := tracker.CheckWithFingerprint(handoffPath, PlanningHandoffFingerprint)
		if err != nil {
			return planningContinuationOutcome{}, err
		}
		if !progressMade && tracker.NoProgressCount() >= maxNoProgress {
			return planningContinuationOutcome{
				AgentStatus: planningAgentStatusSafetyRail,
				Iterations:  in.Attempt,
				LastError:   fmt.Sprintf("planning handoff made no progress for %d consecutive continuations", tracker.NoProgressCount()),
				QALog:       allQA,
			}, nil
		}
	}
}

func additionalDirsOrState(cfg PlanLoopConfig) []string {
	addDirs := cfg.AdditionalDirs
	if len(addDirs) == 0 {
		return []string{cfg.StateDir}
	}
	return addDirs
}

func buildPlanningContinuationPrompt(handoffPath, canonicalPath string) string {
	var b strings.Builder
	b.WriteString("# Planning Smart Zone Continuation\n\n")
	b.WriteString("A previous planning agent wound down inside this same planning unit. Continue the same role inside the same attempt; do not restart validation, advance the attempt counter, or discard the existing canonical artifact.\n\n")
	b.WriteString("Read the rolling handoff scratch first:\n")
	fmt.Fprintf(&b, "- `%s`\n\n", handoffPath)
	if canonicalPath != "" {
		b.WriteString("Then read the canonical plan artifact so far and continue editing it in place:\n")
		fmt.Fprintf(&b, "- `%s`\n\n", canonicalPath)
	}
	b.WriteString("Resume from `### Where I stopped`. When you need another Smart Zone continuation, overwrite `planning-handoff.md` with `CONTINUE`; when the canonical plan is ready for validation, overwrite `planning-handoff.md` with `COMPLETE`. Touch `phase_complete` last.")
	return b.String()
}

func buildPlanningHandoffRepairPrompt(handoffPath, canonicalPath string, violations []string) string {
	var b strings.Builder
	b.WriteString("# Planning Smart Zone Continuation Repair\n\n")
	b.WriteString("The previous planning handoff did not satisfy the continuation contract. Stay inside the same planning attempt; do not run validation, advance the attempt counter, or discard the canonical artifact.\n\n")
	b.WriteString("Fix these handoff contract violations:\n")
	if len(violations) == 0 {
		b.WriteString("- planning-handoff.md did not satisfy the continuation contract\n")
	} else {
		for _, violation := range violations {
			fmt.Fprintf(&b, "- %s\n", violation)
		}
	}
	b.WriteString("\nRead the current rolling handoff scratch:\n")
	fmt.Fprintf(&b, "- `%s`\n\n", handoffPath)
	if canonicalPath != "" {
		b.WriteString("Then read the canonical plan artifact so far and continue editing it in place:\n")
		fmt.Fprintf(&b, "- `%s`\n\n", canonicalPath)
	}
	b.WriteString("Overwrite `planning-handoff.md` with the required sections and a `CONTINUE` or `COMPLETE` state. Touch `phase_complete` last.")
	return b.String()
}
