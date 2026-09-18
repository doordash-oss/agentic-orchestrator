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
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
)

// ConflictResolutionTimeout bounds one conflict-resolution attempt.
const ConflictResolutionTimeout = 20 * time.Minute

// ConflictResolutionRequest describes one conflict-resolution attempt for a
// cherry-pick that stopped with conflicts while a PR stack was replayed onto
// a new target. The session runs inside the detached temporary restack
// worktree; AttemptDir is its prompt, output, and receipt root.
type ConflictResolutionRequest struct {
	FeatureID    string
	SessionID    string
	RunNumber    int // stamps the session into the run's session list
	RepoName     string
	WorkDir      string // absolute temporary restack worktree; session working directory
	AttemptDir   string // absolute; prompt, output, and receipt root
	Prompt       string // rendered user prompt
	SystemPrompt string // rendered role system prompt
	Model        string

	EffortLevel     llm.EffortLevel
	EffectiveEffort llm.EffortLevel
	EffortSource    llm.EffortSource

	// ConflictPaths are the repo-relative conflicted paths; they scope both
	// the permission handler and the session's writable roots.
	ConflictPaths []string
	// Timeout bounds the attempt; <= 0 means ConflictResolutionTimeout.
	Timeout time.Duration
}

// ConflictResolutionStatus is the terminal state of one attempt.
type ConflictResolutionStatus string

const (
	// ConflictResolutionCompleted means the session emitted a valid success
	// outcome and the harness committed the completion receipt.
	ConflictResolutionCompleted ConflictResolutionStatus = "completed"
	// ConflictResolutionFailed means the attempt ended without a committed
	// success outcome; Reason names what happened.
	ConflictResolutionFailed ConflictResolutionStatus = "failed"
)

// ConflictResolutionResult is the outcome the rebase loop routes on. A
// failed attempt is data, not a Go error: the orchestrator retries with the
// accumulated feedback.
type ConflictResolutionResult struct {
	Status ConflictResolutionStatus
	// Reason is the human-readable failure reason for failed attempts;
	// empty for completed ones.
	Reason string
}

// RunConflictResolution runs one short-lived conflict-resolution session
// inside the temporary restack worktree and maps its terminal state onto the
// two-value conflict-resolution result. A nil bounded-helper result (session
// build or harness error) returns the error unchanged; every other terminal
// state is reported through the result so the rebase loop can retry.
//
// Model, effort, and system prompt resolve from the PhaseRunner's
// configuration for the implementation role when the request leaves them
// unset: the session uses the implementation role's model with the feature's
// effort resolution, and the role's system prompt names the attempt directory
// as the only output root.
func (pr *PhaseRunner) RunConflictResolution(ctx context.Context, req ConflictResolutionRequest) (*ConflictResolutionResult, error) {
	if req.SessionID == "" {
		return nil, fmt.Errorf("running conflict resolution: missing session id")
	}
	if req.WorkDir == "" {
		return nil, fmt.Errorf("running conflict resolution: missing work dir")
	}
	if req.AttemptDir == "" {
		return nil, fmt.Errorf("running conflict resolution: missing attempt dir")
	}

	model, effort, effortSource := pr.conflictResolutionRuntime(req)
	if model == "" {
		return nil, fmt.Errorf("running conflict resolution: no model resolved for the implementation role")
	}

	if err := os.MkdirAll(req.AttemptDir, 0o755); err != nil {
		return nil, fmt.Errorf("running conflict resolution: preparing attempt dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(req.AttemptDir, "user-prompt.md"), []byte(req.Prompt), 0o644); err != nil {
		return nil, fmt.Errorf("running conflict resolution: writing user prompt: %w", err)
	}
	systemPrompt := req.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = BuildRoleSystemPrompt(BuildRoleSystemPromptInput{
			Spec:           ConflictResolverRoleSpec(),
			IterationDir:   req.AttemptDir,
			SkillsDir:      pr.SkillsDir,
			Model:          model,
			AskingClause:   pr.askingQuestionsClauseForModel(model),
			CompletionTool: pr.completionToolForModel(model),
		})
	}
	if err := os.WriteFile(filepath.Join(req.AttemptDir, "system-prompt.md"), []byte(systemPrompt), 0o644); err != nil {
		return nil, fmt.Errorf("running conflict resolution: writing system prompt: %w", err)
	}
	outputPath := filepath.Join(req.AttemptDir, "output.txt")

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = ConflictResolutionTimeout
	}

	// The session's writable surface is exactly the conflicted files; the
	// permission handler re-derives the same scope from the repo-relative
	// paths so shell and file tools cannot drift apart.
	writableRoots := make([]string, 0, len(req.ConflictPaths))
	for _, path := range req.ConflictPaths {
		writableRoots = append(writableRoots, filepath.Join(req.WorkDir, path))
	}

	result, err := pr.RunBoundedHelper(ctx, BoundedHelperConfig{
		SessionID:       req.SessionID,
		FeatureID:       req.FeatureID,
		Phase:           feature.PhaseImplement,
		Label:           "rebase-conflict-resolution",
		ObserverPhase:   feature.PhaseImplement.String(),
		Model:           model,
		Prompt:          req.Prompt,
		SystemPrompt:    systemPrompt,
		ResponsePath:    outputPath,
		LogPath:         outputPath,
		WorkDir:         req.WorkDir,
		RepoName:        req.RepoName,
		WritableRoots:   writableRoots,
		PermHandler:     permission.Guarded(&permission.ConflictResolverHandler{WorkDir: req.WorkDir, ConflictPaths: req.ConflictPaths}),
		Timeout:         timeout,
		EffortLevel:     effort,
		EffectiveEffort: effort,
		EffortSource:    effortSource,
		// CompletionDir opts into the semantic completion protocol. The
		// no-artifact implement-phase contract commits on the success
		// outcome alone and writes the harness receipt into AttemptDir.
		CompletionDir: req.AttemptDir,
		ContractPhase: feature.PhaseImplement,
		ContractRole:  RoleResolveRebaseConflict,
		// The bounded helper stamps RunNumber onto the session from the
		// parent span context, placing it in the run's session list.
		ParentSpanCtx: observe.SpanContext{FeatureID: req.FeatureID, RunNumber: req.RunNumber},
	})
	if result == nil {
		return nil, err
	}
	return conflictResolutionOutcome(result, err), nil
}

// conflictResolutionRuntime resolves the model and effort a
// conflict-resolution session runs with. Explicit request values win; the
// defaults are the implementation role's model with the feature's effort
// resolution, matching how every other implementation worker launches.
func (pr *PhaseRunner) conflictResolutionRuntime(req ConflictResolutionRequest) (string, llm.EffortLevel, llm.EffortSource) {
	if req.Model != "" && req.EffortLevel != "" {
		return req.Model, req.EffortLevel, req.EffortSource
	}
	model := req.Model
	effort, source := req.EffortLevel, req.EffortSource
	if pr.FeatureStore != nil && req.FeatureID != "" {
		if f, err := pr.FeatureStore.Load(req.FeatureID); err == nil {
			if model == "" {
				model = pr.modelForRole(config.ModelConfigFieldByName(f.Models, llm.ConfigFieldForRole(llm.PhaseImplementation)), llm.PhaseImplementation)
			}
			if effort == "" {
				effort, source = pr.resolveEffortForRole(f, llm.PhaseImplementation, model)
			}
			return model, effort, source
		}
	}
	if model == "" {
		model = pr.modelForRole("", llm.PhaseImplementation)
	}
	return model, effort, source
}

// conflictResolutionOutcome maps a bounded-helper terminal state onto the
// conflict-resolution result. Only a completed, receipt-backed session
// counts as success; every other terminal state is a failed attempt whose
// reason names the state for the orchestrator's retry loop.
func conflictResolutionOutcome(result *BoundedHelperResult, err error) *ConflictResolutionResult {
	if result.Status == BoundedHelperStatusCompleted {
		return &ConflictResolutionResult{Status: ConflictResolutionCompleted}
	}
	switch result.Status {
	case BoundedHelperStatusTimedOut:
		return &ConflictResolutionResult{Status: ConflictResolutionFailed, Reason: "session timed out"}
	case BoundedHelperStatusAskedUser:
		return &ConflictResolutionResult{Status: ConflictResolutionFailed, Reason: "session asked the user for input"}
	case BoundedHelperStatusPermissionRequired:
		return &ConflictResolutionResult{Status: ConflictResolutionFailed, Reason: "session requested a denied tool permission"}
	case BoundedHelperStatusFailed:
		detail := strings.TrimSpace(result.Output)
		if detail == "" && err != nil {
			detail = err.Error()
		}
		if detail != "" {
			return &ConflictResolutionResult{Status: ConflictResolutionFailed, Reason: "session failed: " + detail}
		}
		return &ConflictResolutionResult{Status: ConflictResolutionFailed, Reason: "session failed"}
	case BoundedHelperStatusEmptyOutput, BoundedHelperStatusProtocolViolation:
		return &ConflictResolutionResult{Status: ConflictResolutionFailed, Reason: "session ended without completing"}
	default:
		return &ConflictResolutionResult{Status: ConflictResolutionFailed, Reason: fmt.Sprintf("session ended in an unknown state (%s)", result.Status)}
	}
}
