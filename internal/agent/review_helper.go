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

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// ReviewHelperConfig configures a bounded review or validation run that uses
// the bounded-helper artifact protocol: the helper is granted scoped write
// permission only for declared helper-owned artifacts, and the harness routes
// on ParseReviewFeedback(FeedbackPath) after the turn ends. FeedbackPath,
// HelperIterDir, and Role are required.
type ReviewHelperConfig struct {
	SessionID              string
	FeatureID              string
	Phase                  feature.Phase
	ParentSpanCtx          observe.SpanContext
	Model                  string
	Prompt                 string
	PromptPath             string
	ResponsePath           string
	FeedbackPath           string
	HelperIterDir          string
	Role                   Role
	AllowedPaths           []string
	WorkDir                string
	RepoName               string
	AdditionalDirs         []string
	LogPath                string
	SystemPromptPrefix     string
	CompletionAskingClause string
	Timeout                time.Duration
	EffortLevel            llm.EffortLevel
	// Kind classifies the helper session for TUI/observer purposes. Defaults to
	// KindReviewHelper when unset.
	Kind ports.SessionKind
	// Label is a short context-specific sub-label (validator domain, review
	// target, …) surfaced in attach-view tabs.
	Label string
}

// ReviewHelperResult captures the parsed verdict from a bounded helper run.
//
// Feedback holds the canonical body of the structured review-feedback.md
// (verbatim if the LLM produced a clean file and satisfied completion;
// deterministic CHANGES_REQUESTED protocol feedback otherwise). Output retains
// the helper's stdout for log surfaces (TUI, debug) but is not part of the
// wire protocol — the file is.
type ReviewHelperResult struct {
	Output   string
	Status   ReviewStatus
	Feedback string
	Markers  ValidatorMarkers
	Usage    llm.Usage
}

// RunReadOnlyReviewHelper runs a bounded review helper under the file-based
// handoff protocol and parses ParseReviewFeedback(FeedbackPath). The helper
// is read-only with respect to the worktree; it may only write to
// FeedbackPath, and the harness deterministically routes on the structured
// `## Verdict` section of that file.
func (pr *PhaseRunner) RunReadOnlyReviewHelper(ctx context.Context, cfg ReviewHelperConfig) (*ReviewHelperResult, error) {
	if cfg.SessionID == "" {
		return nil, fmt.Errorf("running review helper: missing session id")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("running review helper: missing model")
	}
	if cfg.Prompt == "" {
		return nil, fmt.Errorf("running review helper: missing prompt")
	}
	if cfg.WorkDir == "" {
		return nil, fmt.Errorf("running review helper: missing work dir")
	}
	if cfg.FeedbackPath == "" {
		return nil, fmt.Errorf("running review helper: missing feedback path (file-based handoff is required)")
	}
	if cfg.HelperIterDir == "" {
		return nil, fmt.Errorf("running review helper: missing helper iteration directory")
	}
	if cfg.Role == "" {
		return nil, fmt.Errorf("running review helper: missing helper role")
	}

	if cfg.PromptPath != "" {
		if err := os.WriteFile(cfg.PromptPath, []byte(cfg.Prompt), 0o644); err != nil {
			return nil, fmt.Errorf("running review helper: writing prompt: %w", err)
		}
	}

	pidDir := pr.StateDir
	if cfg.FeatureID != "" {
		pidDir = filepath.Join(pr.StateDir, cfg.FeatureID)
	}

	spec, ok := lookupRoleSpec(cfg.Phase, cfg.Role)
	if !ok {
		return nil, fmt.Errorf("running review helper: missing RoleSpec for phase %s role %s", cfg.Phase, cfg.Role)
	}
	systemPrompt := BuildRoleSystemPrompt(BuildRoleSystemPromptInput{
		Spec:          spec,
		IterationDir:  cfg.HelperIterDir,
		SkillsDir:     pr.SkillsDir,
		GuidelinesDir: pr.GuidelinesDir,
		AskingClause:  cfg.CompletionAskingClause,
	})
	allowedPaths := boundedReviewHelperAllowedPaths(cfg)
	command, env, sessOpts, err := pr.BuildSession(BuildSessionOpts{
		Model:                          cfg.Model,
		Prompt:                         cfg.Prompt,
		SystemPrompt:                   systemPrompt,
		AdditionalDirs:                 cfg.AdditionalDirs,
		WritableRoots:                  allowedPaths,
		DelegateEditsToClient:          true,
		PIDDir:                         pidDir,
		PermHandler:                    permission.Guarded(&permission.BoundedHelperArtifactHandler{AllowedPaths: allowedPaths}),
		RepoName:                       cfg.RepoName,
		WorkDir:                        cfg.WorkDir,
		LogPath:                        cfg.LogPath,
		EffortLevel:                    cfg.EffortLevel,
		AgentNames:                     []string{},
		Phase:                          cfg.Phase,
		SystemPromptHasUsefulResources: true,
		MarkerPath:                     filepath.Join(cfg.HelperIterDir, PhaseCompleteFile),
	})
	if err != nil {
		return nil, fmt.Errorf("running review helper: building session: %w", err)
	}
	if sessOpts != nil && cfg.SystemPromptPrefix != "" && cfg.PromptPath != "" {
		WriteValidatorSystemPrompt(filepath.Dir(cfg.PromptPath), cfg.SystemPromptPrefix, sessOpts.DebugSystemPrompt)
	}
	if sessOpts != nil {
		if cfg.Kind != 0 {
			sessOpts.Kind = cfg.Kind
		} else {
			sessOpts.Kind = ports.KindReviewHelper
		}
		if cfg.Label != "" {
			sessOpts.Label = cfg.Label
		}
	}

	// requireOutput stays false: the verdict lives in FeedbackPath, not in
	// chat output, so an empty stdout body is a perfectly valid run.
	boundedResult, runErr := pr.runBoundedHelperSession(ctx, boundedHelperRunConfig{
		sessionID:        cfg.SessionID,
		featureID:        cfg.FeatureID,
		phase:            cfg.Phase,
		label:            "review helper",
		observerPhase:    "review",
		model:            cfg.Model,
		responsePath:     cfg.ResponsePath,
		repoName:         cfg.RepoName,
		workDir:          cfg.WorkDir,
		command:          command,
		env:              env,
		sessOpts:         sessOpts,
		requireOutput:    false,
		phaseCompleteDir: cfg.HelperIterDir,
		contractPhase:    cfg.Phase,
		contractRole:     cfg.Role,
		parentSpanCtx:    cfg.ParentSpanCtx,
	})
	if boundedResult == nil {
		return nil, runErr
	}

	result := &ReviewHelperResult{
		Output: boundedResult.Output,
		Usage:  boundedResult.Usage,
	}

	// Parse the structured handoff file; on protocol violation, overwrite
	// it with the synthesized CHANGES_REQUESTED feedback so the on-disk
	// artifact matches the verdict the harness routed on.
	parsed, parseErr := ParseReviewFeedback(cfg.FeedbackPath)
	if parseErr != nil {
		if runErr != nil {
			return result, runErr
		}
		return result, fmt.Errorf("running review helper: parsing review-feedback.md: %w", parseErr)
	}
	if parsed.OK() {
		result.Status = parsed.Verdict
		result.Feedback = strings.TrimSpace(parsed.Body)
		result.Markers = parsed.Markers
	} else {
		synth := reviewHelperProtocolViolationFeedback(parsed, runErr)
		_ = os.WriteFile(cfg.FeedbackPath, []byte(synth), 0o644)
		result.Status = ReviewChangesRequested
		result.Feedback = strings.TrimSpace(synth)
	}

	if runErr != nil {
		if isProtocolViolationError(runErr) && parsed.OK() {
			synth := reviewHelperProtocolViolationFeedback(parsed, runErr)
			_ = os.WriteFile(cfg.FeedbackPath, []byte(synth), 0o644)
			result.Status = ReviewChangesRequested
			result.Feedback = strings.TrimSpace(synth)
			result.Markers = ValidatorMarkers{}
		}
		return result, runErr
	}
	return result, nil
}

func reviewHelperProtocolViolationFeedback(parsed *ParsedReviewFeedback, runErr error) string {
	var findings []string
	if parsed != nil {
		for _, violation := range parsed.ProtocolViolations {
			findings = append(findings, fmt.Sprintf("- **Critical**: %s", violation))
		}
	}
	if runErr != nil && isProtocolViolationError(runErr) {
		findings = append(findings, fmt.Sprintf("- **Critical**: review helper completion protocol violation: %v", runErr))
	}
	return FormatStructuredReviewFeedback(
		"Review Helper — Handoff Protocol Violation",
		strings.Join(findings, "\n"),
		"",
		ReviewChangesRequested,
	)
}

func boundedReviewHelperAllowedPaths(cfg ReviewHelperConfig) []string {
	allowed := []string{
		cfg.FeedbackPath,
		filepath.Join(cfg.HelperIterDir, "phase_complete"),
	}
	allowed = append(allowed, cfg.AllowedPaths...)
	return allowed
}
