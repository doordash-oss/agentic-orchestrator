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
	SessionID string
	FeatureID string
	Phase     feature.Phase
	// ContractPhase selects the RoleSpec and artifact contract when it differs
	// from the lifecycle phase shown for the session. It defaults to Phase.
	ContractPhase          feature.Phase
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

	contractPhase := cfg.ContractPhase
	if contractPhase == 0 {
		contractPhase = cfg.Phase
	}
	spec, ok := lookupRoleSpec(contractPhase, cfg.Role)
	if !ok {
		return nil, fmt.Errorf("running review helper: missing RoleSpec for phase %s role %s", contractPhase, cfg.Role)
	}
	systemPrompt := BuildRoleSystemPrompt(BuildRoleSystemPromptInput{
		Spec:          spec,
		IterationDir:  cfg.HelperIterDir,
		SkillsDir:     pr.SkillsDir,
		GuidelinesDir: pr.GuidelinesDir,
		AskingClause:  cfg.CompletionAskingClause,
	})
	allowedPaths := boundedReviewHelperAllowedPaths(cfg)
	boundedHandler := &permission.BoundedHelperArtifactHandler{AllowedPaths: allowedPaths}
	command, env, sessOpts, err := pr.BuildSession(BuildSessionOpts{
		Model:                          cfg.Model,
		Prompt:                         cfg.Prompt,
		SystemPrompt:                   systemPrompt,
		AdditionalDirs:                 cfg.AdditionalDirs,
		WritableRoots:                  allowedPaths,
		PIDDir:                         pidDir,
		PermHandler:                    permission.Guarded(boundedHandler),
		RepoName:                       cfg.RepoName,
		WorkDir:                        cfg.WorkDir,
		LogPath:                        cfg.LogPath,
		EffortLevel:                    cfg.EffortLevel,
		AgentNames:                     explorationAgentNames(),
		Phase:                          cfg.Phase,
		SystemPromptHasUsefulResources: true,
		MarkerPath:                     filepath.Join(cfg.HelperIterDir, PhaseCompleteFile),
	})
	if err != nil {
		return nil, fmt.Errorf("running review helper: building session: %w", err)
	}
	// The provider's bounded-helper capabilities are resolved by BuildSession
	// (which holds the registry) and surfaced on sessOpts, so they hold even
	// when this helper runner carries no registry of its own.
	nudgeSupported := sessOpts != nil && sessOpts.SupportsFinishOrViolateNudge
	sandboxRequested := sessOpts != nil && sessOpts.UsesBoundedHelperSandbox
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
		if nudgeSupported {
			sessOpts.TurnMode = ports.TurnModeInteractive
		}
	}

	// Some helper adapters need an OS sandbox so read-only analysis can run
	// without tool-denial control flow while worktree mutation is blocked below
	// the process.
	var sandboxCleanup func()
	if sessOpts != nil {
		var sandboxed bool
		command, sandboxed, sandboxCleanup = maybeWrapHelperSandbox(command, sandboxRequested, pr.StateDir)
		boundedHandler.Sandboxed = sandboxed
	}
	if sandboxCleanup != nil {
		defer sandboxCleanup()
	}

	// requireOutput stays false: the verdict lives in FeedbackPath, not in
	// chat output, so an empty stdout body is a perfectly valid run.
	boundedResult, runErr := pr.runBoundedHelperSession(ctx, boundedHelperRunConfig{
		sessionID:            cfg.SessionID,
		featureID:            cfg.FeatureID,
		phase:                cfg.Phase,
		label:                "review helper",
		observerPhase:        "review",
		model:                cfg.Model,
		responsePath:         cfg.ResponsePath,
		repoName:             cfg.RepoName,
		workDir:              cfg.WorkDir,
		command:              command,
		env:                  env,
		sessOpts:             sessOpts,
		requireOutput:        false,
		phaseCompleteDir:     cfg.HelperIterDir,
		contractPhase:        contractPhase,
		contractRole:         cfg.Role,
		parentSpanCtx:        cfg.ParentSpanCtx,
		finishOrViolateNudge: nudgeSupported,
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

// RunLiveRunReviewHelper runs a review helper with the live-run posture: broad
// shell access plus harness-owned writable scratch roots, while file tools stay
// scoped away from the reviewed source tree.
func (pr *PhaseRunner) RunLiveRunReviewHelper(ctx context.Context, cfg ReviewHelperConfig) (*ReviewHelperResult, error) {
	if cfg.SessionID == "" {
		return nil, fmt.Errorf("running live-run review helper: missing session id")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("running live-run review helper: missing model")
	}
	if cfg.Prompt == "" {
		return nil, fmt.Errorf("running live-run review helper: missing prompt")
	}
	if cfg.WorkDir == "" {
		return nil, fmt.Errorf("running live-run review helper: missing work dir")
	}
	if cfg.FeedbackPath == "" {
		return nil, fmt.Errorf("running live-run review helper: missing feedback path (file-based handoff is required)")
	}
	if cfg.HelperIterDir == "" {
		return nil, fmt.Errorf("running live-run review helper: missing helper iteration directory")
	}
	if cfg.Role == "" {
		return nil, fmt.Errorf("running live-run review helper: missing helper role")
	}

	scratch, err := prepareLiveRunReviewScratch(cfg.HelperIterDir)
	if err != nil {
		return nil, err
	}
	prompt := liveRunReviewPrompt(cfg.Prompt, scratch)
	if cfg.PromptPath != "" {
		if err := os.WriteFile(cfg.PromptPath, []byte(prompt), 0o644); err != nil {
			return nil, fmt.Errorf("running live-run review helper: writing prompt: %w", err)
		}
	}

	pidDir := pr.StateDir
	if cfg.FeatureID != "" {
		pidDir = filepath.Join(pr.StateDir, cfg.FeatureID)
	}

	contractPhase := cfg.ContractPhase
	if contractPhase == 0 {
		contractPhase = cfg.Phase
	}
	spec, ok := lookupRoleSpec(contractPhase, cfg.Role)
	if !ok {
		return nil, fmt.Errorf("running live-run review helper: missing RoleSpec for phase %s role %s", contractPhase, cfg.Role)
	}
	systemPrompt := BuildRoleSystemPrompt(BuildRoleSystemPromptInput{
		Spec:          spec,
		IterationDir:  cfg.HelperIterDir,
		SkillsDir:     pr.SkillsDir,
		GuidelinesDir: pr.GuidelinesDir,
		AskingClause:  cfg.CompletionAskingClause,
	})
	allowedPaths := boundedReviewHelperAllowedPaths(cfg)
	writableRoots := append([]string(nil), allowedPaths...)
	writableRoots = append(writableRoots, scratch.roots()...)
	liveHandler := &permission.LiveRunReviewHandler{
		AllowedPaths:  allowedPaths,
		ScratchRoots:  scratch.roots(),
		DenyWriteHint: "live-run review may write only review-feedback.md, phase_complete, and files under its scratch roots",
	}
	command, env, sessOpts, err := pr.BuildSession(BuildSessionOpts{
		Model:                          cfg.Model,
		Prompt:                         prompt,
		SystemPrompt:                   systemPrompt,
		AdditionalDirs:                 cfg.AdditionalDirs,
		WritableRoots:                  writableRoots,
		PIDDir:                         pidDir,
		PermHandler:                    permission.Guarded(liveHandler),
		RepoName:                       cfg.RepoName,
		WorkDir:                        cfg.WorkDir,
		LogPath:                        cfg.LogPath,
		EffortLevel:                    cfg.EffortLevel,
		AgentNames:                     explorationAgentNames(),
		Phase:                          cfg.Phase,
		SystemPromptHasUsefulResources: true,
		MarkerPath:                     filepath.Join(cfg.HelperIterDir, PhaseCompleteFile),
	})
	if err != nil {
		return nil, fmt.Errorf("running live-run review helper: building session: %w", err)
	}
	env = mergeSessionEnv(env, scratch.env()...)

	nudgeSupported := sessOpts != nil && sessOpts.SupportsFinishOrViolateNudge
	sandboxRequested := sessOpts != nil && sessOpts.UsesBoundedHelperSandbox
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
		if nudgeSupported {
			sessOpts.TurnMode = ports.TurnModeInteractive
		}
	}

	var sandboxCleanup func()
	if sessOpts != nil {
		command, _, sandboxCleanup = maybeWrapHelperSandbox(command, sandboxRequested, pr.StateDir)
	}
	if sandboxCleanup != nil {
		defer sandboxCleanup()
	}

	boundedResult, runErr := pr.runBoundedHelperSession(ctx, boundedHelperRunConfig{
		sessionID:            cfg.SessionID,
		featureID:            cfg.FeatureID,
		phase:                cfg.Phase,
		label:                "live-run review helper",
		observerPhase:        "review",
		model:                cfg.Model,
		responsePath:         cfg.ResponsePath,
		repoName:             cfg.RepoName,
		workDir:              cfg.WorkDir,
		command:              command,
		env:                  env,
		sessOpts:             sessOpts,
		requireOutput:        false,
		phaseCompleteDir:     cfg.HelperIterDir,
		contractPhase:        contractPhase,
		contractRole:         cfg.Role,
		parentSpanCtx:        cfg.ParentSpanCtx,
		finishOrViolateNudge: nudgeSupported,
	})
	if boundedResult == nil {
		return nil, runErr
	}

	result := &ReviewHelperResult{
		Output: boundedResult.Output,
		Usage:  boundedResult.Usage,
	}

	parsed, parseErr := ParseReviewFeedback(cfg.FeedbackPath)
	if parseErr != nil {
		if runErr != nil {
			return result, runErr
		}
		return result, fmt.Errorf("running live-run review helper: parsing review-feedback.md: %w", parseErr)
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

type liveRunReviewScratch struct {
	EvidenceRoot   string
	BuildCacheRoot string
	TempRoot       string
}

func prepareLiveRunReviewScratch(helperDir string) (liveRunReviewScratch, error) {
	scratch := liveRunReviewScratch{
		EvidenceRoot:   filepath.Join(helperDir, "evidence"),
		BuildCacheRoot: filepath.Join(helperDir, "build-cache"),
		TempRoot:       filepath.Join(helperDir, "tmp"),
	}
	for _, dir := range append(scratch.roots(), scratch.cacheSubdirs()...) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return liveRunReviewScratch{}, fmt.Errorf("preparing live-run review scratch root %s: %w", dir, err)
		}
	}
	return scratch, nil
}

func (s liveRunReviewScratch) roots() []string {
	return []string{s.EvidenceRoot, s.BuildCacheRoot, s.TempRoot}
}

func (s liveRunReviewScratch) cacheSubdirs() []string {
	return []string{
		filepath.Join(s.BuildCacheRoot, "go-build"),
		filepath.Join(s.BuildCacheRoot, "go-mod"),
		filepath.Join(s.BuildCacheRoot, "xdg"),
		filepath.Join(s.BuildCacheRoot, "npm"),
		filepath.Join(s.BuildCacheRoot, "yarn"),
		filepath.Join(s.BuildCacheRoot, "pnpm"),
		filepath.Join(s.BuildCacheRoot, "pip"),
		filepath.Join(s.BuildCacheRoot, "cargo"),
		filepath.Join(s.BuildCacheRoot, "rustup"),
	}
}

func (s liveRunReviewScratch) env() []string {
	return []string{
		"TMPDIR=" + s.TempRoot,
		"TMP=" + s.TempRoot,
		"TEMP=" + s.TempRoot,
		"XDG_CACHE_HOME=" + filepath.Join(s.BuildCacheRoot, "xdg"),
		"GOCACHE=" + filepath.Join(s.BuildCacheRoot, "go-build"),
		"GOMODCACHE=" + filepath.Join(s.BuildCacheRoot, "go-mod"),
		"npm_config_cache=" + filepath.Join(s.BuildCacheRoot, "npm"),
		"YARN_CACHE_FOLDER=" + filepath.Join(s.BuildCacheRoot, "yarn"),
		"PNPM_HOME=" + filepath.Join(s.BuildCacheRoot, "pnpm"),
		"PIP_CACHE_DIR=" + filepath.Join(s.BuildCacheRoot, "pip"),
		"CARGO_HOME=" + filepath.Join(s.BuildCacheRoot, "cargo"),
		"RUSTUP_HOME=" + filepath.Join(s.BuildCacheRoot, "rustup"),
	}
}

func liveRunReviewPrompt(prompt string, scratch liveRunReviewScratch) string {
	var b strings.Builder
	b.WriteString("## Live-Run Scratch Roots\n\n")
	fmt.Fprintf(&b, "Evidence root: %s\n", scratch.EvidenceRoot)
	fmt.Fprintf(&b, "Build cache root: %s\n", scratch.BuildCacheRoot)
	fmt.Fprintf(&b, "Temp root: %s\n\n", scratch.TempRoot)
	b.WriteString("Cache and temp environment variables are already pointed at these roots. ")
	b.WriteString("Write screenshots, recordings, command logs, and other QA evidence under the evidence root. ")
	b.WriteString("Do not write into the reviewed source tree.\n\n")
	b.WriteString(prompt)
	return b.String()
}

func mergeSessionEnv(env []string, overrides ...string) []string {
	out := append([]string(nil), env...)
	index := make(map[string]int, len(out))
	for i, entry := range out {
		key, _, ok := strings.Cut(entry, "=")
		if ok {
			index[key] = i
		}
	}
	for _, entry := range overrides {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		if i, exists := index[key]; exists {
			out[i] = entry
			continue
		}
		index[key] = len(out)
		out = append(out, entry)
	}
	return out
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
