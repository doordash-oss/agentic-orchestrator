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
	// EnableSmartZoneHandoff opts this helper into helper-level Smart Zone
	// continuations. The shared bounded-helper path remains unarmed unless a
	// call-site sets this field.
	EnableSmartZoneHandoff  bool
	HandoffPath             string
	MaxConsecNoProgress     int
	MaxConsecHandoffFails   int
	ContextHandoffIteration int
	ContinuationPaths       []string
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

	spec, ok := lookupRoleSpec(cfg.Phase, cfg.Role)
	if !ok {
		return nil, fmt.Errorf("running review helper: missing RoleSpec for phase %s role %s", cfg.Phase, cfg.Role)
	}
	var boundedResult *BoundedHelperResult
	var runErr error
	if cfg.EnableSmartZoneHandoff {
		if cfg.HandoffPath == "" {
			cfg.HandoffPath = filepath.Join(cfg.HelperIterDir, ReviewProgressHandoffFilename)
		}
		paths := append([]string(nil), cfg.ContinuationPaths...)
		for _, path := range []string{cfg.PromptPath, cfg.FeedbackPath, cfg.HandoffPath} {
			if strings.TrimSpace(path) != "" && !sliceContainsString(paths, path) {
				paths = append(paths, path)
			}
		}
		_, runErr = runHelperWithContinuations(ctx, helperContinuationConfig{
			Label:                "review helper",
			SessionIDBase:        cfg.SessionID,
			HandoffPath:          cfg.HandoffPath,
			CanonicalPaths:       paths,
			ParseHandoff:         ParseReviewProgressHandoffMd,
			Fingerprint:          ReviewProgressHandoffFingerprint,
			MaxConsecNoProgress:  cfg.MaxConsecNoProgress,
			MaxConsecMalformed:   cfg.MaxConsecHandoffFails,
			ContinuationSkill:    spec.SkillName,
			ContinuationArtifact: ReviewProgressHandoffFilename,
			ForbiddenOnContinue:  []string{cfg.FeedbackPath},
			RunSession: func(ctx context.Context, in helperContinuationRunInput) (helperContinuationRunResult, error) {
				runCfg := cfg
				prompt := cfg.Prompt
				if in.Prompt != "" {
					prompt = in.Prompt
					runCfg.PromptPath = reviewHelperContinuationPromptPath(cfg.PromptPath, in.Continuation)
				}
				result, err := pr.runReadOnlyReviewHelperOnce(ctx, runCfg, spec, prompt, in.SessionID, cfg.Role, true, true)
				boundedResult = result
				if result == nil {
					return helperContinuationRunResult{}, err
				}
				status := agentStatusSuccess
				if result.Status != BoundedHelperStatusCompleted {
					status = result.Status
				}
				return helperContinuationRunResult{Status: status}, err
			},
		})
		if runErr == nil {
			outcome, violations, validateErr := Validate(cfg.Phase, cfg.Role, cfg.HelperIterDir)
			if validateErr != nil {
				runErr = fmt.Errorf("running review helper: validating helper contract: %w", validateErr)
			} else if !outcome.OK {
				if outcome.ReviewFeedback != nil && !outcome.ReviewFeedback.OK() {
					synth := FormatReviewProtocolViolationFeedback(outcome.ReviewFeedback)
					_ = os.WriteFile(cfg.FeedbackPath, []byte(synth), 0o644)
				}
				runErr = newProtocolViolationError(cfg.Role, cfg.HelperIterDir, violations)
			}
		}
	} else {
		boundedResult, runErr = pr.runReadOnlyReviewHelperOnce(ctx, cfg, spec, cfg.Prompt, cfg.SessionID, cfg.Role, false, false)
	}
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

func (pr *PhaseRunner) runReadOnlyReviewHelperOnce(ctx context.Context, cfg ReviewHelperConfig, spec RoleSpec, prompt, sessionID string, contractRole Role, enableHandoff, skipValidation bool) (*BoundedHelperResult, error) {
	if cfg.PromptPath != "" {
		if err := os.WriteFile(cfg.PromptPath, []byte(prompt), 0o644); err != nil {
			return nil, fmt.Errorf("running review helper: writing prompt: %w", err)
		}
	}

	pidDir := pr.StateDir
	if cfg.FeatureID != "" {
		pidDir = filepath.Join(pr.StateDir, cfg.FeatureID)
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
		Prompt:                         prompt,
		SystemPrompt:                   systemPrompt,
		AdditionalDirs:                 cfg.AdditionalDirs,
		WritableRoots:                  allowedPaths,
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
	return pr.runBoundedHelperSession(ctx, boundedHelperRunConfig{
		sessionID:        sessionID,
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
		contractRole:     contractRole,
		skipValidation:   skipValidation,
		parentSpanCtx:    cfg.ParentSpanCtx,
		enableHandoff:    enableHandoff,
		handoffRole:      spec.SkillName,
		handoffIteration: cfg.ContextHandoffIteration,
	})
}

func reviewHelperContinuationPromptPath(promptPath string, continuation int) string {
	if strings.TrimSpace(promptPath) == "" || continuation <= 0 {
		return promptPath
	}
	ext := filepath.Ext(promptPath)
	stem := strings.TrimSuffix(filepath.Base(promptPath), ext)
	if stem == "" {
		stem = "continuation-prompt"
	}
	return filepath.Join(filepath.Dir(promptPath), fmt.Sprintf("%s-c%02d%s", stem, continuation+1, ext))
}

func sliceContainsString(values []string, want string) bool {
	for _, got := range values {
		if got == want {
			return true
		}
	}
	return false
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
	if cfg.HandoffPath != "" {
		allowed = append(allowed, cfg.HandoffPath)
	}
	allowed = append(allowed, cfg.AllowedPaths...)
	return allowed
}
