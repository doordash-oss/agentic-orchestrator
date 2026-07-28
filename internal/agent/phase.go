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
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/autoreview"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/guidelinedef"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// PhaseRunner handles launching agent sessions for each feature phase.
type PhaseRunner struct {
	SessionManager             ports.SessionManager
	FeatureStore               ports.FeatureStore
	CommandRunner              ports.CommandRunner
	Config                     *config.Config
	StateDir                   string
	SkillsDir                  string // path to reconciled skills dir; empty = no skills
	GuidelinesDir              string // path to reconciled guidelines dir; empty = no guidelines
	DangerouslySkipPermissions bool
	PermissionCache            *permission.Cache // shared permission cache (nil = no caching)

	// Registry is the LLM provider registry for looking up providers by model.
	Registry *llm.Registry

	// Observer is the observability facade. May be nil (acts as NopObserver).
	Observer *observe.Observer

	// OnVerificationProgress is called after a harness verification status
	// transition has been persisted. The runtime uses it to invalidate API
	// clients while verification runs without an active agent session.
	OnVerificationProgress func(featureID string)

	// BuildSessionFn, if non-nil, overrides the production BuildSession logic.
	// Used exclusively for test injection.
	BuildSessionFn func(BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error)

	// RunImplementFn, if non-nil, is forwarded into RunMultiRepoOrchestrator's
	// OrchestratorConfig.RunImplementFn so tests can stub the per-repo
	// implementation loop without bypassing the orchestrator.
	RunImplementFn func(ImplementConfig, ports.SessionManager) (*LoopResult, error)

	// RunFinalReviewFn, if non-nil, is forwarded into
	// RunMultiRepoFinalReview's OrchestratorConfig.RunFinalReviewFn so tests
	// can stub the unified feature-level Final Review loop driven by the
	// engine without launching real sessions.
	RunFinalReviewFn func(OrchestratorConfig, ports.SessionManager) (*FeatureFinalReviewResult, error)
}

// askingQuestionsClauseForModel returns the PromptAdapter.AskingQuestionsClause()
// for the given model via the registry. Falls back to an empty string if the
// registry is nil or the provider doesn't implement PromptAdapter.
func (pr *PhaseRunner) askingQuestionsClauseForModel(model string) string {
	if pr.Registry == nil {
		return ""
	}
	pa, err := pr.Registry.PromptAdapterForModel(model)
	if err != nil {
		return ""
	}
	return pa.AskingQuestionsClause()
}

func (pr *PhaseRunner) defaultModelForRole(role llm.PhaseRole) string {
	if pr.Registry == nil {
		return ""
	}
	defaults := pr.Registry.CatalogDefaultModels()
	switch role {
	case llm.PhaseInquiry:
		return defaults.Inquiry
	case llm.PhaseResearch:
		return defaults.Research
	case llm.PhasePlanning:
		return defaults.Planning
	case llm.PhaseImplementation:
		return defaults.Implementation
	case llm.PhaseReview:
		return defaults.Review
	case llm.PhaseChat:
		return defaults.Utilities
	case llm.PhaseKBBuild:
		return defaults.KBBuild
	default:
		return ""
	}
}

func (pr *PhaseRunner) modelForRole(configured string, role llm.PhaseRole) string {
	if configured != "" {
		return configured
	}
	return pr.defaultModelForRole(role)
}

// EffortCapabilitiesForModel resolves the effort capabilities for a model
// through the provider registry. Returns nil when the model is unknown or the
// provider has no catalog (Auto-only).
func (pr *PhaseRunner) EffortCapabilitiesForModel(model string) []llm.EffortLevel {
	if pr.Registry == nil || model == "" {
		return nil
	}
	prov, _, err := pr.Registry.ResolveModel(model)
	if err != nil {
		return nil
	}
	return llm.EffortCapabilitiesForModel(prov, model)
}

// resolveEffortForRole resolves the effective effort level for a primary
// session launch from the configured per-role effort value, the selected
// model's capabilities, and the active pipeline. The role determines which
// EffortConfig field is read; Utilities (PhaseChat) Auto always resolves to
// low regardless of pipeline, while all other roles use the pipeline level
// for Auto. Capability drift (explicit value no longer supported by the
// model) falls back to the pipeline level (or low for Utilities) with a
// runtime warning and does not mutate persisted state.
func (pr *PhaseRunner) resolveEffortForRole(f *feature.Feature, role llm.PhaseRole, model string) (llm.EffortLevel, llm.EffortSource) {
	return pr.resolveEffortForRoleWithPipeline(f, role, model, "")
}

// resolveEffortForRoleWithPipeline is the shared resolver for both primary and
// secondary session launches. When pipelineOverride is a valid PipelineProfile,
// it supplies the Auto baseline for this launch only — used by refactor
// requests whose temporary pipeline differs from the feature's configured
// pipeline. An empty pipelineOverride falls back to f.EffectivePipeline().
// Explicit role values are always preserved regardless of the Auto baseline.
func (pr *PhaseRunner) resolveEffortForRoleWithPipeline(f *feature.Feature, role llm.PhaseRole, model string, pipelineOverride feature.PipelineProfile) (llm.EffortLevel, llm.EffortSource) {
	field := llm.ConfigFieldForRole(role)
	configured := config.EffortConfigFieldByName(f.Effort, field)
	pipelineEffort := f.EffectivePipeline().EffortLevel()
	if pipelineOverride.IsValid() {
		pipelineEffort = pipelineOverride.EffortLevel()
	}

	if role == llm.PhaseChat && (configured == "" || configured == "auto") {
		return llm.EffortLow, llm.EffortSourceAuto
	}

	caps := pr.EffortCapabilitiesForModel(model)
	effectiveEffort, effortSource := llm.ResolveEffortFromString(configured, caps, pipelineEffort)
	if llm.EffortDrifted(llm.EffortLevel(configured), caps) {
		pr.logRuntimeWarning(f, "%s effort %q is not supported by model %q; falling back to Auto (%s)",
			strings.ToLower(field), configured, model, string(pipelineEffort))
	}
	return effectiveEffort, effortSource
}

// ResolveSecondaryEffort is the exported entry point for secondary session
// launches (validators, review helpers, fix agents, cycle workers, utility
// helpers). It resolves the effective effort and source from the same
// configured role as the selected model: Planning for planning agents, Review
// for validators and review axes, Implementation for implementation and fix
// workers, and Utilities for utility helpers. When pipelineOverride is a
// valid PipelineProfile, it supplies the Auto baseline for this launch only
// (used by refactor requests); an empty value falls back to the feature's
// effective pipeline. Capability drift projects the affected role as Auto,
// omits unsupported provider arguments, and emits a runtime warning through
// the standard log channel without mutating persisted state.
func (pr *PhaseRunner) ResolveSecondaryEffort(f *feature.Feature, role llm.PhaseRole, model string, pipelineOverride feature.PipelineProfile) (llm.EffortLevel, llm.EffortSource) {
	return pr.resolveEffortForRoleWithPipeline(f, role, model, pipelineOverride)
}

// logRuntimeWarning emits a warning through the standard logger channel.
// Used for non-fatal issues like capability drift that should be visible to
// the user without blocking execution.
func (pr *PhaseRunner) logRuntimeWarning(f *feature.Feature, format string, args ...any) {
	log.Printf("feature %s: "+format, append([]any{f.ID}, args...)...)
}

// NewPhaseRunner creates a PhaseRunner with a default execCommandRunner.
func NewPhaseRunner(sm ports.SessionManager, store ports.FeatureStore, stateDir string) *PhaseRunner {
	return &PhaseRunner{SessionManager: sm, FeatureStore: store, StateDir: stateDir, CommandRunner: &execCommandRunner{}}
}

func (pr *PhaseRunner) buildSessionForFeature(f *feature.Feature) BuildSessionFunc {
	return func(opts BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
		if f != nil {
			opts.FeatureID = f.ID
			opts.AutomaticReviewMode = feature.NormalizeAutomaticReviewMode(f.AutomaticReviewMode)
		}
		return pr.BuildSession(opts)
	}
}

// resolvePhaseArtifactDir returns the artifact directory for a pipeline phase,
// accounting for active refactor cycles. Paths route through the active run
// directory so sealed runs are not overwritten.
func (pr *PhaseRunner) resolvePhaseArtifactDir(f *feature.Feature, phaseName string) string {
	return filepath.Join(ActiveRunDir(pr.StateDir, f), f.RefactorPrefix(), phaseName)
}

// interactivePhaseConfig holds the values that differ across the async
// interactive phase methods (RunInquire, RunResearchFromQuestions,
// RunDesign). Prompt is the lean user prompt; Spec drives the generic
// RoleSpec system prompt that owns skill discovery, useful resources, output
// roots, and completion.
type interactivePhaseConfig struct {
	Prompt          string
	Spec            RoleSpec
	DirName         string        // artifact subdirectory: "inquire", "research", "design"
	SkillName       string        // skill name (used for error messages and session naming)
	SessionSuffix   string        // appended to feature ID: "-inquire", "-research", "-design"
	Phase           feature.Phase // feature.PhaseInquire, etc.
	ConfiguredModel string
	ModelRole       llm.PhaseRole
	AgentNames      []string
	GuidelinesDir   string
	KBInfos         []KBInfo
}

// runInteractivePhase contains the shared boilerplate for launching an async
// interactive agent session. Each public Run* method builds its prompt, then
// delegates here.
func (pr *PhaseRunner) runInteractivePhase(f *feature.Feature, cfg interactivePhaseConfig) (string, error) {
	artifactDir := pr.resolvePhaseArtifactDir(f, cfg.DirName)
	_ = os.MkdirAll(artifactDir, 0o755)
	RemovePhaseComplete(artifactDir)
	phaseModel := pr.modelForRole(cfg.ConfiguredModel, cfg.ModelRole)

	effectiveEffort, effortSource := pr.resolveEffortForRole(f, cfg.ModelRole, phaseModel)

	systemPrompt := BuildRoleSystemPrompt(BuildRoleSystemPromptInput{
		Spec:          cfg.Spec,
		IterationDir:  artifactDir,
		SkillsDir:     pr.SkillsDir,
		GuidelinesDir: cfg.GuidelinesDir,
		KBInfos:       cfg.KBInfos,
		AskingClause:  pr.askingQuestionsClauseForModel(phaseModel),
	})
	sessionID := fmt.Sprintf("%s%s", f.ID, cfg.SessionSuffix)

	workDir, additionalDirs := resolveUnifiedWorkDir(f, pr.StateDir)

	pidDir := filepath.Join(pr.StateDir, f.ID)
	featureCtx := observe.SpanContextForFeature(f.ID, f.TraceID, f.Name, f.FeatureSpanID).WithRun(f.ActiveRun)
	phaseCtx := featureCtx.Child()
	pr.Observer.PhaseStarted(phaseCtx, cfg.Phase.String())

	effortLevel := f.EffectivePipeline().EffortLevel()
	if effectiveEffort != "" {
		effortLevel = effectiveEffort
	}
	cmd, env, sessOpts, err := pr.BuildSession(BuildSessionOpts{
		FeatureID:                      f.ID,
		AutomaticReviewMode:            feature.NormalizeAutomaticReviewMode(f.AutomaticReviewMode),
		Model:                          phaseModel,
		Prompt:                         cfg.Prompt,
		SystemPrompt:                   systemPrompt,
		AdditionalDirs:                 additionalDirs,
		AgentNames:                     cfg.AgentNames,
		PIDDir:                         pidDir,
		PermHandler:                    permHandlerFor(pr.DangerouslySkipPermissions, pr.PermissionCache, ""),
		WorkDir:                        workDir,
		EffortLevel:                    effortLevel,
		Phase:                          cfg.Phase,
		SystemPromptHasUsefulResources: true,
		MarkerPath:                     filepath.Join(artifactDir, PhaseCompleteFile),
	})
	if err != nil {
		return "", fmt.Errorf("building inquire session: %w", err)
	}
	if sessOpts == nil {
		sessOpts = &ports.SessionOpts{}
	}
	if effectiveEffort != "" {
		sessOpts.EffectiveEffort = effectiveEffort
		sessOpts.EffortSource = effortSource
	}
	WriteDebugPrompts(artifactDir, sessOpts.DebugSystemPrompt, cfg.Prompt)
	sessOpts.PermCacheScope = ""
	sessionCtx := phaseCtx.Child()
	sessOpts.AskUserAutoPick = askUserAutoPickConfig(
		pr.FeatureStore,
		pr.Observer,
		f,
		interactiveAutoPickPurpose(cfg.Phase),
		sessionCtx,
		sessionID,
		"",
		0,
	)

	sess, err := pr.SessionManager.StartSession(sessionID, f.ID, cfg.Phase, cmd, workDir, env, sessOpts)
	if err != nil {
		return "", fmt.Errorf("starting %s session: %w", cfg.SkillName, err)
	}

	providerName := ""
	if sessOpts != nil {
		providerName = sessOpts.ProviderName
	}
	pr.Observer.SessionStarted(sessionCtx, cfg.Phase.String(), sessionID, providerName, phaseModel, "", string(effectiveEffort), string(effortSource))

	// Track reads of KB/skill/guideline files for observability.
	pr.installContextReadTracker(sess, sessionCtx, cfg.Phase.String(), sessionID, pr.StateDir)
	pr.installSubagentProgressTracker(sess, sessionCtx, cfg.Phase.String(), sessionID)

	sessionStart := time.Now()
	sess.AddCleanupFunc(func() {
		cost := ExtractSessionCost(sess)
		usage := toSessionUsage(cost)
		duration := time.Since(sessionStart)
		_ = accumulateSessionCostToFeatureKey(pr.FeatureStore, f.ID, cfg.Phase.DirName(), cost, SessionCostMetadata{
			SessionID:     sessionID,
			ObserverPhase: cfg.Phase.String(),
		})
		pr.Observer.SessionEnded(sessionCtx, cfg.Phase.String(), sessionID, "", usage, duration, sessionErrFromStatus(sess))
	})

	// Set up log file
	logPath := filepath.Join(artifactDir, "output.txt")
	logFile, err := os.Create(logPath)
	if err == nil {
		sess.SetLogFile(logFile)
	}

	return sessionID, nil
}

// RunInquire starts an inquire session for a feature.
// Returns the session ID. The session runs asynchronously.
func (pr *PhaseRunner) RunInquire(f *feature.Feature, kbInfos ...KBInfo) (string, error) {
	inquiryModel := f.Models.Inquiry
	if inquiryModel == "" {
		inquiryModel = f.Models.Research
	}
	return pr.runInteractivePhase(f, interactivePhaseConfig{
		Prompt:          BuildInquirePrompt(f, pr.SkillsDir, kbInfos...),
		Spec:            InquirerRoleSpec(),
		DirName:         "inquire",
		SkillName:       "inquire",
		SessionSuffix:   "-inquire",
		Phase:           feature.PhaseInquire,
		ConfiguredModel: inquiryModel,
		ModelRole:       llm.PhaseInquiry,
		AgentNames:      []string{},
		KBInfos:         kbInfos,
	})
}

// RunResearchFromQuestions starts a research session using questions from the Inquire phase.
// Returns the session ID. The session runs asynchronously.
func (pr *PhaseRunner) RunResearchFromQuestions(f *feature.Feature, questionsPath string, kbInfos ...KBInfo) (string, error) {
	return pr.runInteractivePhase(f, interactivePhaseConfig{
		Prompt:          BuildResearchFromQuestionsPrompt(f, pr.SkillsDir, questionsPath, kbInfos...),
		Spec:            ResearcherRoleSpec(),
		DirName:         "research",
		SkillName:       "research-codebase",
		SessionSuffix:   "-research",
		Phase:           feature.PhaseResearch,
		ConfiguredModel: f.Models.Research,
		ModelRole:       llm.PhaseResearch,
		AgentNames:      explorationAgentNames(),
		KBInfos:         kbInfos,
	})
}

// explorationAgentNames returns the codebase- and web-exploration subagents
// shared by research and the planning, review, and refactor phases.
func explorationAgentNames() []string {
	return []string{
		"codebase-locator",
		"codebase-analyzer",
		"codebase-pattern-finder",
		"web-search-researcher",
	}
}

// RunDesign starts the canonical Design session for a feature. Returns the
// session ID. The session runs asynchronously. The on-disk artifact
// subdirectory remains "design" so legacy run state stays readable in
// place; the session identity, skill, and prompt template are Design-facing.
func (pr *PhaseRunner) RunDesign(f *feature.Feature, researchOutput string, qaFilePaths []string, kbInfos ...KBInfo) (string, error) {
	return pr.runInteractivePhase(f, interactivePhaseConfig{
		Prompt:          BuildDesignPrompt(f, pr.SkillsDir, pr.GuidelinesDir, researchOutput, qaFilePaths, kbInfos...),
		Spec:            DesignerRoleSpec(),
		DirName:         feature.PhaseDesign.DirName(),
		SkillName:       "design",
		SessionSuffix:   "-design",
		Phase:           feature.PhaseDesign,
		ConfiguredModel: f.Models.Planning,
		ModelRole:       llm.PhasePlanning,
		AgentNames:      []string{},
		GuidelinesDir:   pr.GuidelinesDir,
		KBInfos:         kbInfos,
	})
}

// RunDesignWithValidation starts the iterative Design author/critic loop.
func (pr *PhaseRunner) RunDesignWithValidation(f *feature.Feature, researchOutput string, qaFilePaths []string, kbInfos ...KBInfo) (chan *DesignLoopResult, error) {
	workDir, additionalDirs := resolveUnifiedWorkDir(f, pr.StateDir)
	var repoName string
	if len(f.Repos) > 0 {
		repoName = f.Repos[0].Name
	}

	designModel := pr.modelForRole(f.Models.Planning, llm.PhasePlanning)
	designEffort, designEffortSource := pr.resolveEffortForRole(f, llm.PhasePlanning, designModel)
	reviewModel := pr.modelForRole(f.Models.Review, llm.PhaseReview)
	validatorEffort, validatorEffortSource := pr.resolveEffortForRole(f, llm.PhaseReview, reviewModel)

	maxAttempts := f.MaxDesignIterations
	if maxAttempts <= 0 && pr.Config != nil {
		maxAttempts = pr.Config.Defaults.MaxDesignIterations
	}
	if maxAttempts <= 0 {
		maxAttempts = DefaultMaxDesignAttempts
	}
	cfg := DesignLoopConfig{PlanLoopConfig: PlanLoopConfig{
		Feature:                    f,
		FeatureStore:               pr.FeatureStore,
		StateDir:                   pr.StateDir,
		ResearchArtifactPath:       researchOutput,
		QAFilePaths:                qaFilePaths,
		KBInfos:                    kbInfos,
		WorkDir:                    workDir,
		AdditionalDirs:             additionalDirs,
		MaxAttempts:                maxAttempts,
		DangerouslySkipPermissions: pr.DangerouslySkipPermissions,
		PermissionCache:            pr.PermissionCache,
		RepoName:                   repoName,
		BuildSession:               pr.buildSessionForFeature(f),
		AskingClause:               pr.askingQuestionsClauseForModel(designModel),
		EffortLevel:                f.EffectivePipeline().EffortLevel(),
		EffectiveEffort:            designEffort,
		EffortSource:               designEffortSource,
		ValidatorEffectiveEffort:   validatorEffort,
		ValidatorEffortSource:      validatorEffortSource,
		SkillsDir:                  pr.SkillsDir,
		GuidelinesDir:              pr.GuidelinesDir,
		FinishOrViolateNudge:       pr.finishOrViolateNudgeForModel(designModel),
		Observer:                   pr.Observer,
	}}

	resultCh := make(chan *DesignLoopResult, 1)
	go func() {
		result, err := RunDesignValidationLoop(cfg, pr.SessionManager)
		if err != nil {
			resultCh <- &DesignLoopResult{FinalStatus: "failed", LastError: err.Error()}
			return
		}
		resultCh <- result
	}()
	return resultCh, nil
}

// RunCodebaseIndex builds structural codebase indexes for all repos in a feature.
// This is pure Go (no LLM) and runs quickly. It checks freshness first and
// skips repos whose indexes are already up-to-date with HEAD.
func (pr *PhaseRunner) RunCodebaseIndex(f *feature.Feature) error {
	if len(f.Repos) == 0 {
		return nil
	}
	for _, repo := range f.Repos {
		if err := pr.RunCodebaseIndexForRepo(repo); err != nil {
			log.Printf("codebase index for %s: %v", repo.Name, err)
			// Non-fatal — continue with other repos
		}
	}
	return nil
}

// RunCodebaseIndexForRepo builds a codebase index for a single repo.
func (pr *PhaseRunner) RunCodebaseIndexForRepo(repo feature.FeatureRepo) error {
	kbDir := KBStateDir(pr.StateDir, repo.Name)

	if IsCodebaseIndexFresh(context.Background(), pr.CommandRunner, kbDir, repo.Path) {
		return nil
	}

	index, err := BuildCodebaseIndex(repo.Path, 10*time.Second)
	if err != nil {
		return fmt.Errorf("building codebase index for %s: %w", repo.Name, err)
	}

	if err := SaveCodebaseIndex(kbDir, index); err != nil {
		return fmt.Errorf("saving codebase index for %s: %w", repo.Name, err)
	}

	return MarkCodebaseIndexFresh(context.Background(), pr.CommandRunner, kbDir, repo.Path, len(index.Symbols), len(index.Summaries))
}

// RunKnowledgeBase starts a knowledge base build session for a feature's primary repo.
// Returns the session ID. The session runs asynchronously.
// Returns ("", nil) if the KB is already fresh and no rebuild is needed.
// This is a backward-compatible wrapper around RunKnowledgeBaseForRepo.
func (pr *PhaseRunner) RunKnowledgeBase(f *feature.Feature) (string, error) {
	if len(f.Repos) == 0 {
		return "", fmt.Errorf("feature has no repos")
	}
	return pr.RunKnowledgeBaseForRepo(f, f.Repos[0])
}

// RunKnowledgeBaseForRepo starts a KB build for a single repo within a feature.
// Session ID format: "<featureID>-kb-<repoName>"
// Returns (sessionID, error). Returns ("", nil) if KB is already fresh.
func (pr *PhaseRunner) RunKnowledgeBaseForRepo(f *feature.Feature, repo feature.FeatureRepo) (string, error) {
	// Resolve effective repo path: prefer worktree, fall back to original path.
	repoPath := repo.Path
	if repo.WorktreePath != "" {
		repoPath = repo.WorktreePath
	}

	kbDir := KBStateDir(pr.StateDir, repo.Name)
	_ = os.MkdirAll(kbDir, 0o755)

	// Pre-create standard KB category subdirectories so the agent doesn't
	// need Bash mkdir (which would trigger a permission prompt).
	for _, cat := range kbStandardCategories {
		_ = os.MkdirAll(filepath.Join(kbDir, cat), 0o755)
	}

	// Skip rebuild if KB is already up-to-date with HEAD
	if IsKBFresh(context.Background(), pr.CommandRunner, kbDir, repoPath) {
		return "", nil
	}

	locked, err := AcquireKBLock(kbDir, f.ID)
	if err != nil {
		return "", fmt.Errorf("acquiring KB lock for %s: %w", repo.Name, err)
	}
	if !locked {
		return "", fmt.Errorf("%w for repo %s", ErrKBLocked, repo.Name)
	}

	// Re-check after acquiring the lock in case another process refreshed the KB
	// immediately before we obtained ownership.
	if IsKBFresh(context.Background(), pr.CommandRunner, kbDir, repoPath) {
		_ = ReleaseKBLock(kbDir, f.ID)
		return "", nil
	}
	RemovePhaseComplete(kbDir)

	// Determine full vs incremental build
	var existingKBPath, lastCommit string
	state, _ := LoadKBState(kbDir)
	if state != nil {
		kbPath := KBPath(kbDir)
		if _, err := os.Stat(kbPath); err == nil {
			existingKBPath = kbPath
			lastCommit = state.HeadCommit
		}
	}

	prompt := BuildKBPrompt(repo.Name, repoPath, kbDir, existingKBPath, lastCommit)

	kbModel := pr.modelForRole(f.Models.KBBuild, llm.PhaseKBBuild)
	kbEffectiveEffort, kbEffortSource := pr.resolveEffortForRole(f, llm.PhaseKBBuild, kbModel)

	systemPrompt := BuildRoleSystemPrompt(BuildRoleSystemPromptInput{
		Spec:          KnowledgeBaseBuilderRoleSpec(),
		IterationDir:  kbDir,
		SkillsDir:     pr.SkillsDir,
		GuidelinesDir: pr.GuidelinesDir,
		AskingClause:  pr.askingQuestionsClauseForModel(kbModel),
	})
	sessionID := fmt.Sprintf("%s-kb-%s", f.ID, repo.Name)

	featureCtx := observe.SpanContextForFeature(f.ID, f.TraceID, f.Name, f.FeatureSpanID).WithRun(f.ActiveRun)
	phaseCtx := featureCtx.Child()
	pr.Observer.PhaseStarted(phaseCtx, feature.PhaseKnowledgeBase.String())

	workDir := repoPath

	pidDir := filepath.Join(pr.StateDir, f.ID)
	logPath := filepath.Join(kbDir, "output.txt")
	kbEffortLevel := f.EffectivePipeline().EffortLevel()
	if kbEffectiveEffort != "" {
		kbEffortLevel = kbEffectiveEffort
	}
	cmd, env, sessOpts, err := pr.BuildSession(BuildSessionOpts{
		FeatureID:           f.ID,
		AutomaticReviewMode: feature.NormalizeAutomaticReviewMode(f.AutomaticReviewMode),
		Model:               kbModel,
		Prompt:              prompt,
		SystemPrompt:        systemPrompt,
		// kbDir is a sibling of pr.StateDir (under knowledge-base/<repo>), so it
		// must be mounted explicitly. The global feature-state root is not KB
		// context and must not be exposed to this agent.
		AdditionalDirs:                 []string{kbDir},
		AgentNames:                     knowledgeBaseAgentNames(),
		PIDDir:                         pidDir,
		PermHandler:                    permHandlerFor(pr.DangerouslySkipPermissions, pr.PermissionCache, repo.Name),
		RepoName:                       repo.Name,
		WorkDir:                        workDir,
		EffortLevel:                    kbEffortLevel,
		Phase:                          feature.PhaseKnowledgeBase,
		SystemPromptHasUsefulResources: true,
		MarkerPath:                     filepath.Join(kbDir, PhaseCompleteFile),
		LogPath:                        logPath,
	})
	if err != nil {
		return "", fmt.Errorf("building KB session: %w", err)
	}
	if sessOpts == nil {
		sessOpts = &ports.SessionOpts{}
	}
	if kbEffectiveEffort != "" {
		sessOpts.EffectiveEffort = kbEffectiveEffort
		sessOpts.EffortSource = kbEffortSource
	}
	if sessOpts.LogPath == "" {
		sessOpts.LogPath = logPath
	}
	WriteDebugPrompts(kbDir, sessOpts.DebugSystemPrompt, prompt)

	sess, err := pr.SessionManager.StartSession(sessionID, f.ID, feature.PhaseKnowledgeBase, cmd, workDir, env, sessOpts)
	if err != nil {
		_ = ReleaseKBLock(kbDir, f.ID)
		return "", fmt.Errorf("starting KB session: %w", err)
	}

	sessionCtx := phaseCtx.Child()
	pr.Observer.SessionStarted(sessionCtx, feature.PhaseKnowledgeBase.String(), sessionID, sessOpts.ProviderName, kbModel, repo.Name, string(kbEffectiveEffort), string(kbEffortSource))
	pr.installContextReadTracker(sess, sessionCtx, feature.PhaseKnowledgeBase.String(), sessionID, pr.StateDir)
	pr.installSubagentProgressTracker(sess, sessionCtx, feature.PhaseKnowledgeBase.String(), sessionID)
	sessionStart := time.Now()
	sess.AddCleanupFunc(func() {
		cost := ExtractSessionCost(sess)
		_ = accumulateSessionCostToFeatureKey(pr.FeatureStore, f.ID, feature.PhaseKnowledgeBase.DirName(), cost, SessionCostMetadata{
			SessionID:     sessionID,
			ObserverPhase: feature.PhaseKnowledgeBase.String(),
			RepoName:      repo.Name,
		})
		pr.Observer.SessionEnded(sessionCtx, feature.PhaseKnowledgeBase.String(), sessionID, repo.Name, toSessionUsage(cost), time.Since(sessionStart), sessionErrFromStatus(sess))
	})

	// Register cleanup to release lock when session ends.
	// KB state is only saved on confirmed success (see handlePhaseCompleted).
	featureID := f.ID
	sess.AddCleanupFunc(func() {
		_ = ReleaseKBLock(kbDir, featureID)
	})

	// Fallback for custom SessionManager implementations that do not honor
	// SessionOpts.LogPath. The production manager opens it before Start().
	if sess.LogFilePath() == "" {
		logFile, err := os.Create(logPath)
		if err == nil {
			sess.SetLogFile(logFile)
		}
	}

	return sessionID, nil
}

// RunAllKnowledgeBuilds starts KB builds for all repos in a feature.
// Returns a map of repoName -> sessionID for repos that started sessions.
// Repos with fresh KBs are skipped (not in the returned map).
func (pr *PhaseRunner) RunAllKnowledgeBuilds(f *feature.Feature) (map[string]string, error) {
	if len(f.Repos) == 0 {
		return nil, fmt.Errorf("feature has no repos")
	}
	sessions := make(map[string]string)
	for _, repo := range f.Repos {
		sessionID, err := pr.RunKnowledgeBaseForRepo(f, repo)
		if err != nil {
			return sessions, fmt.Errorf("KB build for repo %s: %w", repo.Name, err)
		}
		if sessionID != "" {
			sessions[repo.Name] = sessionID
		}
	}
	return sessions, nil
}

// knowledgeBaseAgentNames returns the subagents allowed in Knowledge Base sessions.
func knowledgeBaseAgentNames() []string {
	return []string{
		"codebase-locator",
		"architecture-researcher",
		"conventions-researcher",
		"api-surface-researcher",
		"dependencies-researcher",
		"verification-researcher",
	}
}

// RunPlanningWithValidation starts the planning loop with validation gate.
// This runs asynchronously and returns a channel that receives the result.
// qaFilePaths are paths to Q&A files from earlier phases (inquire, research, design).
func (pr *PhaseRunner) RunPlanningWithValidation(f *feature.Feature, researchArtifactPath string, qaFilePaths []string, kbInfos ...KBInfo) (chan *PlanLoopResult, error) {
	workDir, additionalDirs := resolveUnifiedWorkDir(f, pr.StateDir)

	// Derive repo name for permission scoping so planning sessions persist
	// rules per-repo rather than globally (mirrors implement.go fallback).
	var repoName string
	if len(f.Repos) > 0 {
		repoName = f.Repos[0].Name
	}

	planningModel := pr.modelForRole(f.Models.Planning, llm.PhasePlanning)
	planEffort, planEffortSource := pr.resolveEffortForRole(f, llm.PhasePlanning, planningModel)
	reviewModel := pr.modelForRole(f.Models.Review, llm.PhaseReview)
	validatorEffort, validatorEffortSource := pr.resolveEffortForRole(f, llm.PhaseReview, reviewModel)
	cfg := PlanLoopConfig{
		Feature:                      f,
		FeatureStore:                 pr.FeatureStore,
		StateDir:                     pr.StateDir,
		ResearchArtifactPath:         researchArtifactPath,
		PlanningResearchArtifactPath: f.ResearchArtifactPath(),
		DesignArtifactPath:           f.DesignArtifactPath(),
		QAFilePaths:                  qaFilePaths,
		KBInfos:                      kbInfos,
		WorkDir:                      workDir,
		AdditionalDirs:               additionalDirs,
		MaxAttempts:                  f.MaxPlanIterations,
		DangerouslySkipPermissions:   pr.DangerouslySkipPermissions,
		PermissionCache:              pr.PermissionCache,
		RepoName:                     repoName,
		BuildSession:                 pr.buildSessionForFeature(f),
		AskingClause:                 pr.askingQuestionsClauseForModel(planningModel),
		EffortLevel:                  f.EffectivePipeline().EffortLevel(),
		EffectiveEffort:              planEffort,
		EffortSource:                 planEffortSource,
		ValidatorEffectiveEffort:     validatorEffort,
		ValidatorEffortSource:        validatorEffortSource,
		SkillsDir:                    pr.SkillsDir,
		GuidelinesDir:                pr.GuidelinesDir,
		FinishOrViolateNudge:         pr.finishOrViolateNudgeForModel(planningModel),
		Observer:                     pr.Observer,
	}

	resultCh := make(chan *PlanLoopResult, 1)
	go func() {
		result, err := RunRoadmapPlanningLoop(cfg, pr.SessionManager)
		if err != nil {
			resultCh <- &PlanLoopResult{FinalStatus: "failed", LastError: err.Error()}
		} else {
			resultCh <- result
		}
	}()

	return resultCh, nil
}

// RunPhasePlanning runs per-phase plan creation + critic loop.
// qaFilePaths are paths to Q&A files from earlier phases (inquire, research, design).
// priorPhasePlanPaths are approved plan paths from completed phases (phase-01, phase-02, etc.).
func (pr *PhaseRunner) RunPhasePlanning(f *feature.Feature, roadmapPath string, phase RoadmapPhase, qaFilePaths []string, priorPhasePlanPaths []string, kbInfos ...KBInfo) (chan *PlanLoopResult, error) {
	// Use the shared resolver so per-phase planning runs in the feature worktree
	// (same as roadmap planning and implementation). Previously this function
	// used f.Repos[0].Path unconditionally, which pointed the Grounding critic
	// at the base repo clone on master and made every prior-phase symbol look
	// missing.
	workDir, additionalDirs := resolveUnifiedWorkDir(f, pr.StateDir)

	maxAttempts := 10
	if pr.Config != nil && pr.Config.Defaults.MaxPhasePlanIterations > 0 {
		maxAttempts = pr.Config.Defaults.MaxPhasePlanIterations
	}
	// Honor per-feature budget override (set by "iterate more" in the TUI).
	if f.MaxPlanIterations > maxAttempts {
		maxAttempts = f.MaxPlanIterations
	}

	// Derive repo name for permission scoping (same fallback as roadmap planning).
	var planRepoName string
	if len(f.Repos) > 0 {
		planRepoName = f.Repos[0].Name
	}

	phasePlanModel := pr.modelForRole(f.Models.Planning, llm.PhasePlanning)
	phasePlanEffort, phasePlanEffortSource := pr.resolveEffortForRole(f, llm.PhasePlanning, phasePlanModel)
	phaseValidatorModel := pr.modelForRole(f.Models.Review, llm.PhaseReview)
	phaseValidatorEffort, phaseValidatorEffortSource := pr.resolveEffortForRole(f, llm.PhaseReview, phaseValidatorModel)
	cfg := PhasePlanLoopConfig{
		PlanLoopConfig: PlanLoopConfig{
			Feature:                      f,
			FeatureStore:                 pr.FeatureStore,
			StateDir:                     pr.StateDir,
			PlanningResearchArtifactPath: f.ResearchArtifactPath(),
			DesignArtifactPath:           f.DesignArtifactPath(),
			QAFilePaths:                  qaFilePaths,
			KBInfos:                      kbInfos,
			WorkDir:                      workDir,
			AdditionalDirs:               additionalDirs,
			MaxAttempts:                  maxAttempts,
			DangerouslySkipPermissions:   pr.DangerouslySkipPermissions,
			PermissionCache:              pr.PermissionCache,
			RepoName:                     planRepoName,
			BuildSession:                 pr.buildSessionForFeature(f),
			AskingClause:                 pr.askingQuestionsClauseForModel(phasePlanModel),
			EffortLevel:                  f.EffectivePipeline().EffortLevel(),
			EffectiveEffort:              phasePlanEffort,
			EffortSource:                 phasePlanEffortSource,
			ValidatorEffectiveEffort:     phaseValidatorEffort,
			ValidatorEffortSource:        phaseValidatorEffortSource,
			SkillsDir:                    pr.SkillsDir,
			GuidelinesDir:                pr.GuidelinesDir,
			FinishOrViolateNudge:         pr.finishOrViolateNudgeForModel(phasePlanModel),
			Observer:                     pr.Observer,
		},
		RoadmapPath:         roadmapPath,
		Phase:               phase,
		PriorPhasePlanPaths: priorPhasePlanPaths,
	}

	resultCh := make(chan *PlanLoopResult, 1)
	go func() {
		result, err := RunPhasePlanningLoop(cfg, pr.SessionManager)
		if err != nil {
			resultCh <- &PlanLoopResult{FinalStatus: "failed", LastError: err.Error()}
		} else {
			resultCh <- result
		}
	}()

	return resultCh, nil
}

// RunImplementation starts the implementation loop for a feature.
// This runs asynchronously and returns a channel that receives the result.
func (pr *PhaseRunner) RunImplementation(f *feature.Feature, planPath string, kbInfos ...KBInfo) (chan *LoopResult, error) {
	workDir := pr.StateDir
	if len(f.Repos) > 0 {
		r := f.Repos[0]
		if r.WorktreePath != "" {
			workDir = r.WorktreePath
		} else {
			workDir = r.Path
		}
	}

	maxIter, maxFails, maxNoProg := pr.resolveLoopLimits(f)
	implementationModel := pr.modelForRole(f.Models.Implementation, llm.PhaseImplementation)
	reviewModel := pr.modelForRole(f.Models.Review, llm.PhaseReview)

	// Resolve Implementation effort from the configured value, the selected
	// model's capabilities, and the active pipeline. Auto (or empty) preserves
	// current pipeline behavior; a supported explicit value is used unchanged;
	// capability drift falls back to the pipeline level with a runtime warning.
	pipelineEffort := f.EffectivePipeline().EffortLevel()
	effortCaps := pr.EffortCapabilitiesForModel(implementationModel)
	effectiveEffort, effortSource := llm.ResolveEffortFromString(f.Effort.Implementation, effortCaps, pipelineEffort)
	if llm.EffortDrifted(llm.EffortLevel(f.Effort.Implementation), effortCaps) {
		pr.logRuntimeWarning(f, "implementation effort %q is not supported by model %q; falling back to Auto (%s)",
			f.Effort.Implementation, implementationModel, string(pipelineEffort))
	}
	reviewEffort, reviewEffortSource := pr.resolveEffortForRole(f, llm.PhaseReview, reviewModel)

	// Cycles (rebase, review-comments) operate on the whole branch,
	// not a specific roadmap phase — omit roadmap/phase-type context so the
	// review prompt stays focused on the cycle's objectives.
	phaseType := f.RoadmapPhaseType
	roadmapPath := f.Artifacts["roadmap"]
	if f.CyclePrefix() != "" {
		phaseType = ""
		roadmapPath = ""
	}

	cfg := ImplementConfig{
		Feature:                    f,
		FeatureStore:               pr.FeatureStore,
		WorkDir:                    workDir,
		PlanPath:                   planPath,
		KBInfos:                    kbInfos,
		MaxIterations:              maxIter,
		MaxConsecFails:             maxFails,
		MaxConsecNoProgress:        maxNoProg,
		ExitCriteria:               f.ExitCriteria,
		Model:                      implementationModel,
		ReviewModel:                reviewModel,
		ArtifactDir:                pr.resolveImplementArtifactDir(f),
		StateDir:                   filepath.Join(pr.StateDir, f.ID),
		RunDir:                     ActiveRunDir(pr.StateDir, f),
		PhaseType:                  phaseType,
		RoadmapPath:                roadmapPath,
		DesignArtifactPath:         f.DesignArtifactPath(),
		DangerouslySkipPermissions: pr.DangerouslySkipPermissions,
		PermissionCache:            pr.PermissionCache,
		CommandRunner:              pr.CommandRunner,
		BuildSession:               pr.buildSessionForFeature(f),
		AskingClause:               pr.askingQuestionsClauseForModel(implementationModel),
		EffortLevel:                pipelineEffort,
		EffectiveEffort:            effectiveEffort,
		EffortSource:               effortSource,
		ReviewEffectiveEffort:      reviewEffort,
		ReviewEffortSource:         reviewEffortSource,
		SkillsDir:                  pr.SkillsDir,
		GuidelinesDir:              pr.GuidelinesDir,
		SkipIterationReview:        f.EffectivePipeline().ShouldSkipIterationReview(),
		FinishOrViolateNudge:       pr.finishOrViolateNudgeForModel(implementationModel),
		Observer:                   pr.Observer,
		OnVerificationProgress:     pr.OnVerificationProgress,
	}

	resultCh := make(chan *LoopResult, 1)
	go func() {
		result, err := RunImplementationLoop(cfg, pr.SessionManager)
		if err != nil {
			resultCh <- &LoopResult{FinalStatus: "failed", LastError: err.Error()}
		} else {
			resultCh <- result
		}
	}()

	return resultCh, nil
}

// RunFeatureCycleFinalReview spawns the unified feature-level Final Review
// loop for a post-publish cycle. Cwd at the active run dir; --add-dir for every
// Feature.Repos worktree. The reviewer reads the cumulative diff across
// all repos. Unlike the engine FR, the post-cycle FR does NOT atomic-stamp
// per-repo state on completion — the surrounding cycle owns the post-FR
// transitions. Returns a channel that receives the FR result.
func (pr *PhaseRunner) RunFeatureCycleFinalReview(
	f *feature.Feature,
) (chan *FeatureFinalReviewResult, error) {
	if f == nil {
		return nil, fmt.Errorf("nil feature")
	}
	if len(f.Repos) == 0 {
		return nil, fmt.Errorf("feature %s has no repos", f.ID)
	}

	maxIter, maxFails, maxNoProg := pr.resolveLoopLimits(f)
	implementationModel := pr.modelForRole(f.Models.Implementation, llm.PhaseImplementation)
	reviewModel := pr.modelForRole(f.Models.Review, llm.PhaseReview)
	cycleReviewEffort, cycleReviewEffortSource := pr.resolveEffortForRole(f, llm.PhaseReview, reviewModel)
	cycleImplEffort, cycleImplEffortSource := pr.resolveEffortForRole(f, llm.PhaseImplementation, implementationModel)

	cfg := OrchestratorConfig{
		Feature:                    f,
		FeatureStore:               pr.FeatureStore,
		StateDir:                   pr.StateDir,
		Config:                     pr.Config,
		Model:                      implementationModel,
		ReviewModel:                reviewModel,
		MaxIterations:              maxIter,
		MaxConsecFails:             maxFails,
		MaxConsecNoProgress:        maxNoProg,
		DangerouslySkipPermissions: pr.DangerouslySkipPermissions,
		PermissionCache:            pr.PermissionCache,
		CommandRunner:              pr.CommandRunner,
		BuildSession:               pr.buildSessionForFeature(f),
		AskingClause:               pr.askingQuestionsClauseForModel(implementationModel),
		EffortLevel:                f.EffectivePipeline().EffortLevel(),
		EffectiveEffort:            cycleReviewEffort,
		EffortSource:               cycleReviewEffortSource,
		ImplEffectiveEffort:        cycleImplEffort,
		ImplEffortSource:           cycleImplEffortSource,
		ReviewEffectiveEffort:      cycleReviewEffort,
		ReviewEffortSource:         cycleReviewEffortSource,
		SkillsDir:                  pr.SkillsDir,
		GuidelinesDir:              pr.GuidelinesDir,
		FinishOrViolateNudge:       pr.finishOrViolateNudgeForModel(reviewModel) && pr.finishOrViolateNudgeForModel(implementationModel),
		Observer:                   pr.Observer,
		OnVerificationProgress:     pr.OnVerificationProgress,
		RunFinalReviewFn:           pr.RunFinalReviewFn,
	}

	resultCh := make(chan *FeatureFinalReviewResult, 1)
	go func() {
		runFn := RunFeatureCycleFinalReviewLoop
		if cfg.RunFinalReviewFn != nil {
			runFn = cfg.RunFinalReviewFn
		}
		result, err := runFn(cfg, pr.SessionManager)
		if err != nil {
			if result == nil {
				result = &FeatureFinalReviewResult{}
			}
			result.FinalStatus = "failed"
			result.LastError = err.Error()
		}
		resultCh <- result
	}()
	return resultCh, nil
}

// RunMultiRepoImplementation starts the unified phase-implement loop for a
// feature. Returns a channel that receives the aggregate result.
//
// Under SchemaVersionCurrent = 4 the per-stage ExecutionPlan and per-repo
// resume token are gone. The loop owns the entire phase across every
// Feature.Repos repo; crash recovery re-runs the interrupted unit from
// scratch. Single-repo features use the same code path (degenerate
// len(Feature.Repos) == 1).
func (pr *PhaseRunner) RunMultiRepoImplementation(
	f *feature.Feature,
	planPath string,
	kbInfos ...KBInfo,
) (chan *OrchestratorResult, error) {
	maxIter, maxFails, maxNoProg := pr.resolveLoopLimits(f)

	model := pr.modelForRole(f.Models.Implementation, llm.PhaseImplementation)
	reviewModel := pr.modelForRole(f.Models.Review, llm.PhaseReview)
	implEffort, implEffortSource := pr.resolveEffortForRole(f, llm.PhaseImplementation, model)
	reviewEffort, reviewEffortSource := pr.resolveEffortForRole(f, llm.PhaseReview, reviewModel)

	cfg := OrchestratorConfig{
		Feature:                    f,
		FeatureStore:               pr.FeatureStore,
		PlanPath:                   planPath,
		StateDir:                   pr.StateDir,
		Config:                     pr.Config,
		Model:                      model,
		ReviewModel:                reviewModel,
		MaxIterations:              maxIter,
		MaxConsecFails:             maxFails,
		MaxConsecNoProgress:        maxNoProg,
		KBInfos:                    kbInfos,
		DangerouslySkipPermissions: pr.DangerouslySkipPermissions,
		PermissionCache:            pr.PermissionCache,
		CommandRunner:              pr.CommandRunner,
		BuildSession:               pr.buildSessionForFeature(f),
		AskingClause:               pr.askingQuestionsClauseForModel(model),
		EffortLevel:                f.EffectivePipeline().EffortLevel(),
		EffectiveEffort:            implEffort,
		EffortSource:               implEffortSource,
		ImplEffectiveEffort:        implEffort,
		ImplEffortSource:           implEffortSource,
		ReviewEffectiveEffort:      reviewEffort,
		ReviewEffortSource:         reviewEffortSource,
		SkillsDir:                  pr.SkillsDir,
		GuidelinesDir:              pr.GuidelinesDir,
		FinishOrViolateNudge:       pr.finishOrViolateNudgeForModel(reviewModel) && pr.finishOrViolateNudgeForModel(model),
		Observer:                   pr.Observer,
		OnVerificationProgress:     pr.OnVerificationProgress,
		RunImplementFn:             pr.RunImplementFn,
		RunFinalReviewFn:           pr.RunFinalReviewFn,
	}

	resultCh := make(chan *OrchestratorResult, 1)
	go func() {
		result, err := RunMultiRepoImplementation(cfg, pr.SessionManager)
		if err != nil {
			resultCh <- &OrchestratorResult{FinalStatus: "failed", LastError: err.Error()}
		} else {
			resultCh <- result
		}
	}()
	return resultCh, nil
}

// RunMultiRepoFinalReview spawns the deferred end-of-feature Final Review pass
// for a feature whose implementation pass has completed across all phases. The
// returned channel receives the aggregate FR-pass result. Called by the
// orchestrator's completion handler after the last roadmap-phase implement
// returns "awaiting_final_review".
//
// Plan and PlanPath are intentionally unused by the FR pass; pass nil for
// plan and "" for planPath.
func (pr *PhaseRunner) RunMultiRepoFinalReview(
	f *feature.Feature,
	kbInfos ...KBInfo,
) (chan *OrchestratorResult, error) {
	maxIter, maxFails, maxNoProg := pr.resolveLoopLimits(f)
	model := pr.modelForRole(f.Models.Implementation, llm.PhaseImplementation)
	reviewModel := pr.modelForRole(f.Models.Review, llm.PhaseReview)
	reviewEffort, reviewEffortSource := pr.resolveEffortForRole(f, llm.PhaseReview, reviewModel)
	implEffort, implEffortSource := pr.resolveEffortForRole(f, llm.PhaseImplementation, model)

	cfg := OrchestratorConfig{
		Feature:                    f,
		FeatureStore:               pr.FeatureStore,
		StateDir:                   pr.StateDir,
		Config:                     pr.Config,
		Model:                      model,
		ReviewModel:                reviewModel,
		MaxIterations:              maxIter,
		MaxConsecFails:             maxFails,
		MaxConsecNoProgress:        maxNoProg,
		KBInfos:                    kbInfos,
		DangerouslySkipPermissions: pr.DangerouslySkipPermissions,
		PermissionCache:            pr.PermissionCache,
		CommandRunner:              pr.CommandRunner,
		BuildSession:               pr.buildSessionForFeature(f),
		AskingClause:               pr.askingQuestionsClauseForModel(model),
		EffortLevel:                f.EffectivePipeline().EffortLevel(),
		EffectiveEffort:            reviewEffort,
		EffortSource:               reviewEffortSource,
		ImplEffectiveEffort:        implEffort,
		ImplEffortSource:           implEffortSource,
		ReviewEffectiveEffort:      reviewEffort,
		ReviewEffortSource:         reviewEffortSource,
		SkillsDir:                  pr.SkillsDir,
		GuidelinesDir:              pr.GuidelinesDir,
		FinishOrViolateNudge:       pr.finishOrViolateNudgeForModel(reviewModel) && pr.finishOrViolateNudgeForModel(model),
		Observer:                   pr.Observer,
		OnVerificationProgress:     pr.OnVerificationProgress,
		RunFinalReviewFn:           pr.RunFinalReviewFn,
	}

	resultCh := make(chan *OrchestratorResult, 1)
	go func() {
		result, err := RunMultiRepoFinalReview(cfg, pr.SessionManager)
		if err != nil {
			resultCh <- &OrchestratorResult{FinalStatus: "failed", LastError: err.Error()}
		} else {
			resultCh <- result
		}
	}()
	return resultCh, nil
}

// GetPhaseOutput reads the output from a completed session. Paths route
// through the feature's active run directory so sealed runs are not read.
func (pr *PhaseRunner) GetPhaseOutput(f *feature.Feature, phase string) string {
	if f == nil {
		return ""
	}
	logPath := filepath.Join(ActiveRunDir(pr.StateDir, f), phase, "output.txt")
	data, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// resolveLoopLimits returns (maxIterations, maxConsecFails, maxConsecNoProgress)
// using the feature-level override first, then config defaults, then hardcoded fallbacks.
func (pr *PhaseRunner) resolveLoopLimits(f *feature.Feature) (int, int, int) {
	const fallbackMaxIter = 10
	const fallbackMaxFails = 3
	const fallbackMaxNoProg = 3

	maxIter := f.MaxIterations
	if maxIter == 0 && pr.Config != nil {
		maxIter = pr.Config.Defaults.MaxIterations
	}
	if maxIter == 0 {
		maxIter = fallbackMaxIter
	}

	maxFails := fallbackMaxFails
	if pr.Config != nil && pr.Config.Defaults.MaxConsecutiveFailures > 0 {
		maxFails = pr.Config.Defaults.MaxConsecutiveFailures
	}

	maxNoProg := fallbackMaxNoProg
	if pr.Config != nil && pr.Config.Defaults.MaxConsecutiveNoProgress > 0 {
		maxNoProg = pr.Config.Defaults.MaxConsecutiveNoProgress
	}

	return maxIter, maxFails, maxNoProg
}

// permHandlerFor returns the appropriate PermissionHandler based on whether
// permissions are being skipped.
//
// When skip is true (-dsp mode): auto-approve all tool requests.
// AskUserQuestion is still carved out in session.handleControlRequest.
//
// When skip is false (normal mode): auto-approve reads and file edits;
// leave Bash and other tools for the TUI to prompt.
//
// The returned handler is always wrapped in a SizeGuardHandler that denies
// oversized Claude Write calls — see permission.SizeGuardHandler for the
// failure mode (~20KB Write payloads have hung the tool call for minutes
// and then dropped the turn with nothing written).
func permHandlerFor(skip bool, cache *permission.Cache, repoName string) ports.PermissionHandler {
	var inner ports.PermissionHandler
	if skip {
		inner = &permission.AutoApproveHandler{}
	} else {
		inner = &permission.AcceptEditsHandler{}
		if cache != nil {
			cache.LoadAndMerge(repoName) // Load rules from disk for this repo
			inner = &permission.CachingHandler{
				Inner:    inner,
				Cache:    cache,
				RepoName: repoName,
			}
		}
	}
	return permission.Guarded(inner)
}

// decorateHandlerWithAutoReview wraps the fully composed permission handler
// with the automatic Bash-review decorator when enabled and the handler is the
// general-phase policy. Disabled sessions get the composed handler unchanged
// (byte-identical to the pre-feature path). The reviewer is already resolved
// by the caller, so the enabled flag and reviewer identity are snapshotted as
// values and a running session is unaffected by later workspace edits or
// provider/catalog changes.
func decorateHandlerWithAutoReview(composed, original ports.PermissionHandler, enabled bool, reviewer autoreview.Reviewer, workDir string, writableRoots []string) ports.PermissionHandler {
	if !enabled {
		return composed
	}
	if !permission.IsAutomaticReviewHandler(original) {
		return composed
	}
	return &autoReviewPermissionDecorator{
		inner:         composed,
		reviewer:      reviewer,
		workDir:       workDir,
		writableRoots: append([]string(nil), writableRoots...),
	}
}

// decorateWithAutoReview snapshots the workspace automatic-review defaults,
// resolves the reviewer, and delegates to decorateHandlerWithAutoReview. When
// opts.AutoReview.Enabled is non-nil (crash-resume), the reviewer is restored
// from the snapshotted identity instead of re-resolved, so the resumed session
// retains the original session's reviewer even if the provider/catalog state
// changed. Otherwise the current workspace defaults are read, the reviewer is
// resolved, and the full snapshot is returned for the caller to store. The
// hidden reviewer itself is launched via autoreview.Classify (never
// BuildSession), so it is never decorated and cannot recurse.
func (pr *PhaseRunner) decorateWithAutoReview(composed, original ports.PermissionHandler, opts *BuildSessionOpts, workDir string, writableRoots []string) (ports.PermissionHandler, ports.AutoReviewSnapshot) {
	if opts.AutoReview.Enabled != nil {
		reviewer := autoreview.RestoreReviewer(pr.Registry, opts.AutoReview.ReviewerProvider, opts.AutoReview.ReviewerModel)
		snap := opts.AutoReview
		if *snap.Enabled && reviewer.Provider == nil && strings.TrimSpace(snap.UnavailableReason) == "" {
			snap.UnavailableReason = "snapshotted reviewer provider is no longer available"
		}
		handler := decorateHandlerWithAutoReview(composed, original, *snap.Enabled, reviewer, workDir, writableRoots)
		installAutoReviewObserver(handler, pr.Observer)
		return handler, snap
	}
	globalEnabled := pr != nil && pr.Config != nil && pr.Config.Defaults.AutomaticReviewEnabled
	enabled, _ := feature.ResolveAutomaticReview(opts.AutomaticReviewMode, globalEnabled)
	model := ""
	if pr != nil && pr.Config != nil {
		model = pr.Config.Defaults.Models.AutomaticReview
	}
	reviewer, _, unavailableReason := autoreview.ResolveReviewer(pr.Registry, model)
	provName, revModel := reviewer.Identity()
	snap := ports.AutoReviewSnapshot{
		Enabled:           &enabled,
		Model:             model,
		ReviewerProvider:  provName,
		ReviewerModel:     revModel,
		UnavailableReason: unavailableReason,
	}
	handler := decorateHandlerWithAutoReview(composed, original, enabled, reviewer, workDir, writableRoots)
	installAutoReviewObserver(handler, pr.Observer)
	return handler, snap
}

func automaticReviewSessionBuildNotices(observer *observe.Observer, snap ports.AutoReviewSnapshot) []ports.SessionBuildNotice {
	if snap.Enabled == nil || !*snap.Enabled || snap.ReviewerProvider != "" {
		return nil
	}
	reason := strings.TrimSpace(snap.UnavailableReason)
	if reason == "" {
		reason = "reviewer resolution failed"
	}
	status := "Automatic review enabled but no reviewer available: " + reason
	return []ports.SessionBuildNotice{{
		Status: status,
		Emit: func(ctx ports.SessionBuildNoticeContext) {
			if observer == nil {
				return
			}
			sc, ok := observer.ActivePhaseSpanContext(ctx.FeatureID)
			if !ok {
				sc = observe.SpanContextForFeature(ctx.FeatureID, "", "", "")
			}
			observer.AutomaticReviewUnavailable(sc, observe.AutomaticReviewUnavailableEventInput{
				Phase:     strings.ToLower(ctx.Phase.String()),
				SessionID: ctx.SessionID,
				RepoName:  ctx.RepoName,
				Iteration: ctx.Iteration,
				Scope:     "session_build",
				Reason:    reason,
			})
		},
	}}
}

func installAutoReviewObserver(handler ports.PermissionHandler, observer *observe.Observer) {
	if decorator, ok := handler.(*autoReviewPermissionDecorator); ok {
		decorator.observer = observer
	}
}

// resolveImplementArtifactDir returns the artifact directory for implementation
// within the feature's active run. When in a roadmap phase, uses phase-scoped
// directories. Includes refactor prefix when an active refactor cycle is in
// progress.
func (pr *PhaseRunner) resolveImplementArtifactDir(f *feature.Feature) string {
	runDir := ActiveRunDir(pr.StateDir, f)
	// Cycle prefix takes precedence — when an active cycle is running,
	// artifacts go into the cycle subtree (e.g. rebase-1/).
	// Cycles operate on the whole branch, so roadmap phase scoping is skipped.
	if prefix := f.CyclePrefix(); prefix != "" {
		return filepath.Join(runDir, prefix, "implement")
	}
	base := filepath.Join(runDir, f.RefactorPrefix())
	if f.CurrentRoadmapPhase > 0 {
		return filepath.Join(base, fmt.Sprintf("phase-%02d", f.CurrentRoadmapPhase), "implement")
	}
	return filepath.Join(base, "implement")
}

// resolveUnifiedWorkDir computes the working directory and additional directories
// for unified phases (Inquire, Research, Design, Plan).
//
// With worktrees: workDir = worktree parent, additionalDirs includes
// each repo's worktree path plus the active run directory.
// Without worktrees: workDir = repos[0].Path, additionalDirs includes
// each other repo's path plus the active run directory.
// No repos: workDir = active run dir, additionalDirs = [active run dir].
func resolveUnifiedWorkDir(f *feature.Feature, stateDir string) (workDir string, additionalDirs []string) {
	activeRunDir := ActiveRunDir(stateDir, f)
	if len(f.Repos) == 0 {
		return activeRunDir, []string{activeRunDir}
	}
	allWorktrees := true
	for _, r := range f.Repos {
		if r.WorktreePath == "" {
			allWorktrees = false
			break
		}
	}
	if allWorktrees {
		workDir = filepath.Dir(f.Repos[0].WorktreePath)
		additionalDirs = []string{activeRunDir}
		for _, r := range f.Repos {
			additionalDirs = append(additionalDirs, r.WorktreePath)
		}
		return workDir, additionalDirs
	}
	workDir = f.Repos[0].Path
	additionalDirs = []string{activeRunDir}
	for _, r := range f.Repos[1:] {
		additionalDirs = append(additionalDirs, r.Path)
	}
	return workDir, additionalDirs
}

// BuildSessionOpts holds parameters for BuildSession.
type BuildSessionOpts struct {
	FeatureID       string
	Model           string
	Prompt          string
	SystemPrompt    string
	AllowedTools    []string
	DisallowedTools []string
	AdditionalDirs  []string
	// WritableRoots overrides the provider write surface. Nil preserves the
	// default WorkDir + AdditionalDirs behavior, except that read-only context
	// mounts are not passed as command-level writable roots. The orchestrator's
	// global state root is never granted implicitly.
	WritableRoots []string
	PIDDir        string
	PermHandler   ports.PermissionHandler
	RepoName      string
	WorkDir       string
	LogPath       string          // session output log path
	EffortLevel   llm.EffortLevel // pipeline-driven effort level; providers map to their own naming
	AgentNames    []string
	TurnMode      ports.SessionTurnMode
	Phase         feature.Phase // current phase, used to filter utility skill preamble
	// SystemPromptHasUsefulResources indicates the system prompt already
	// includes the soft KB/guideline/skill discovery catalog. Bounded helpers
	// that still pass ad-hoc prompts leave this false so BuildSession can
	// append the guideline preamble when needed.
	SystemPromptHasUsefulResources bool
	// MarkerPath, when set, is the absolute path to the role's
	// `phase_complete` marker file. Forwarded to llm.ProtocolOpts so providers
	// that synthesize completion-vs-question signals from end-of-turn text
	// (Codex) can use marker existence as the authoritative completion signal.
	// Leave empty for paths that don't have a marker contract; the provider
	// falls back to its legacy heuristic.
	MarkerPath string
	// ResumeSessionID, when set, asks the provider to resume that prior session
	// identity rather than start a fresh one. It is forwarded to both command and
	// protocol setup so each provider resumes via its own supported path.
	ResumeSessionID string
	// Interactive marks a session where a human answers every AskUserQuestion
	// turn in real time (e.g. AMA chat). Forwarded to llm.ProtocolOpts.Interactive;
	// see its doc comment for why this changes text-parsed AskUserQuestion
	// providers' behavior.
	Interactive bool
	// AutoReview carries the snapshotted automatic-review settings for this
	// session. When AutoReview.Enabled is non-nil (crash-resume), the
	// snapshotted values are used; otherwise BuildSession reads the current
	// workspace defaults. The snapshot is copied as a single value across the
	// BuildSessionOpts → SessionOpts → BuildSessionOpts crash-resume boundary
	// so future reviewer-selection fields cannot be copied inconsistently.
	AutoReview ports.AutoReviewSnapshot
	// AutomaticReviewMode is resolved from FeatureID for fresh sessions. It is
	// ignored when AutoReview already contains a crash-resume snapshot.
	AutomaticReviewMode feature.AutomaticReviewMode
}

// BuildSessionFunc is the callback signature for session creation via the registry.
// Used by TUI components and loop configs that need to create sessions.
type BuildSessionFunc func(BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error)

var sessionWatchdogConfig = ports.SessionWatchdogConfig{
	PendingToolIdleTimeout:    5 * time.Minute,
	TurnCompletionIdleTimeout: 5 * time.Minute,
	PollInterval:              time.Second,
	SubagentHeartbeatInterval: 5 * time.Minute,
}

type sessionWatchdogProvider interface {
	EnablesPendingToolWatchdog() bool
}

func watchdogConfigForProvider(provider llm.LLMProvider) *ports.SessionWatchdogConfig {
	p, ok := provider.(sessionWatchdogProvider)
	if !ok || !p.EnablesPendingToolWatchdog() {
		return nil
	}
	cfg := sessionWatchdogConfig
	return &cfg
}

// grillingPhasePermissionMode returns the provider permission mode to pin for
// phases whose prompts rely on the [grill-me] directive. Returns "default" for
// grilling phases so the user's Claude Code "auto" defaultMode setting does
// not inject a "work without stopping for clarifying questions" system
// reminder that suppresses grilling. Returns "" for other phases (inherit
// user defaults).
func grillingPhasePermissionMode(phase feature.Phase) string {
	if phase.RequiresGrilling() {
		return "default"
	}
	return ""
}

// subtractRoots returns roots with every entry in remove omitted, preserving
// order. Used to keep read-only context mounts (skills, guidelines) out of a
// provider's writable set while leaving them in the read set.
func subtractRoots(roots, remove []string) []string {
	if len(remove) == 0 {
		return roots
	}
	skip := make(map[string]struct{}, len(remove))
	for _, r := range remove {
		skip[r] = struct{}{}
	}
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		if _, ok := skip[r]; ok {
			continue
		}
		out = append(out, r)
	}
	return out
}

func appendUniqueRoots(roots []string, additions ...string) []string {
	seen := make(map[string]struct{}, len(roots)+len(additions))
	for _, root := range roots {
		if root != "" {
			seen[root] = struct{}{}
		}
	}
	for _, root := range additions {
		if root == "" {
			continue
		}
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}
	return roots
}

// BuildSession creates CLI command args, env vars, and session opts by
// routing through the provider registry. This is the primary entry point
// for all session creation (research, KB, implementation, review, planning).
//
// If BuildSessionFn is set (test injection), it is used directly
// and no protocol is set.
func (pr *PhaseRunner) BuildSession(opts BuildSessionOpts) (cmd []string, env []string, sessOpts *ports.SessionOpts, err error) {
	// Test injection path
	if pr.BuildSessionFn != nil {
		return pr.BuildSessionFn(opts)
	}

	if opts.AutoReview.Enabled == nil && opts.FeatureID != "" {
		if pr.FeatureStore != nil {
			f, loadErr := pr.FeatureStore.Load(opts.FeatureID)
			if loadErr != nil {
				return nil, nil, nil, fmt.Errorf("loading feature %q for automatic review: %w", opts.FeatureID, loadErr)
			}
			opts.AutomaticReviewMode = feature.NormalizeAutomaticReviewMode(f.AutomaticReviewMode)
		}
	}

	// Production path — registry-based routing.
	// ResolveModel strips any "provider:" prefix and canonicalizes aliases so
	// the provider CLI receives its canonical model identifier.
	prov, bareModel, err := pr.Registry.ResolveModel(opts.Model)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolving provider for model %q: %w", opts.Model, err)
	}

	// Skills and guidelines are read-only context mounts: agents must be able to
	// read them, but they are global shared resources that must never become
	// writable by default.
	var readOnlyContextDirs []string

	// Skills injection: grant agents filesystem access to reconciled skill files.
	if pr.SkillsDir != "" {
		opts.AdditionalDirs = append(opts.AdditionalDirs, pr.SkillsDir)
		readOnlyContextDirs = append(readOnlyContextDirs, pr.SkillsDir)
	}

	// Guidelines injection: append discovery table for code-touching phases.
	if pr.GuidelinesDir != "" && opts.SystemPrompt != "" && !opts.SystemPromptHasUsefulResources {
		switch opts.Phase {
		case feature.PhaseDesign, feature.PhasePlan, feature.PhaseImplement, feature.PhaseReview:
			preamble := guidelinedef.BuildPreamble(pr.GuidelinesDir)
			if preamble != "" {
				opts.SystemPrompt = opts.SystemPrompt + "\n\n" + preamble
			}
		}
	}
	// Guidelines injection: grant agents filesystem access to guideline files.
	if pr.GuidelinesDir != "" {
		opts.AdditionalDirs = append(opts.AdditionalDirs, pr.GuidelinesDir)
		readOnlyContextDirs = append(readOnlyContextDirs, pr.GuidelinesDir)
	}

	writableRoots := append([]string(nil), opts.WritableRoots...)
	if opts.WritableRoots == nil {
		writableRoots = appendUniqueRoots(nil, opts.AdditionalDirs...)
		writableRoots = appendUniqueRoots(writableRoots, opts.WorkDir)
	}

	// Read roots are everything a provider may read without per-call mediation:
	// the working directory and every additional mounted dir (active-run state,
	// skills, guidelines, worktrees, knowledge base, images, attachments). The
	// global state root is provider bookkeeping, not agent context.
	readRoots := appendUniqueRoots(nil, opts.AdditionalDirs...)
	readRoots = appendUniqueRoots(readRoots, opts.WorkDir)

	// Command-level writable roots omit read-only context mounts unless the
	// caller supplied an explicit writable-root list.
	commandWritableRoots := writableRoots
	if opts.WritableRoots == nil {
		commandWritableRoots = subtractRoots(writableRoots, readOnlyContextDirs)
	}

	agentsJSON, err := AgentsJSONForNames(opts.AgentNames)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("building selected agents JSON: %w", err)
	}

	// Always auto-approve web tools so sub-agents can use them without
	// permission prompts inside the Claude CLI.
	opts.AllowedTools = append(opts.AllowedTools, "WebSearch", "WebFetch")

	buildOpts := llm.CommandBuildOpts{
		Model:                bareModel,
		Prompt:               opts.Prompt,
		SystemPrompt:         opts.SystemPrompt,
		AllowedTools:         opts.AllowedTools,
		DisallowedTools:      opts.DisallowedTools,
		DangerouslySkipPerms: pr.DangerouslySkipPermissions,
		AdditionalDirs:       opts.AdditionalDirs,
		StateDir:             pr.StateDir,
		AgentsJSON:           agentsJSON,
		AgentNames:           opts.AgentNames,
		EffortLevel:          opts.EffortLevel,
		PermissionMode:       grillingPhasePermissionMode(opts.Phase),
		ResumeSessionID:      opts.ResumeSessionID,
		WritableRoots:        commandWritableRoots,
		ReadRoots:            readRoots,
		WorkDir:              opts.WorkDir,
	}

	cmd, env, err = prov.BuildCommand(buildOpts)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("building command for %s: %w", prov.Name(), err)
	}
	env = appendAgenticoBinEnv(env)

	contextWindow := 0
	if cc, ok := prov.(llm.CostCalculator); ok {
		contextWindow = cc.ContextWindowForModel(bareModel)
	}

	protocol := prov.NewProtocol(llm.ProtocolOpts{
		Model:           bareModel,
		ContextWindow:   contextWindow,
		WorkDir:         opts.WorkDir,
		SystemPrompt:    opts.SystemPrompt,
		InitialPrompt:   opts.Prompt,
		WritableRoots:   writableRoots,
		DSP:             pr.DangerouslySkipPermissions,
		StateDir:        pr.StateDir,
		MarkerPath:      opts.MarkerPath,
		ResumeSessionID: opts.ResumeSessionID,
		Interactive:     opts.Interactive,
	})

	// Snapshot the automatic-review settings and decorate the permission
	// handler. The snapshot is returned so it can be stored in sessOpts for
	// the implement loop to copy back into BuildSessionOpts on crash-resume.
	permHandler, arSnap := pr.decorateWithAutoReview(
		permission.WrapGeneralPhaseHandlerWithSafeCreate(opts.PermHandler, commandWritableRoots),
		opts.PermHandler,
		&opts,
		opts.WorkDir,
		commandWritableRoots,
	)

	sessOpts = &ports.SessionOpts{
		PIDDir: opts.PIDDir,
		// commandWritableRoots is the same boundary just computed for the
		// provider's own writable-root config, so a plain `touch` or
		// `mkdir -p` inside it (e.g. the phase_complete marker, or a run
		// directory the harness already created) can be trusted the same way
		// AcceptEditsHandler already trusts an equivalent empty Write —
		// see permission.WrapGeneralPhaseHandlerWithSafeCreate.
		PermHandler:         permHandler,
		InitialPrompt:       opts.Prompt,
		ContextWindow:       contextWindow,
		RepoName:            opts.RepoName,
		LogPath:             opts.LogPath,
		ProviderName:        prov.Name(),
		Protocol:            protocol,
		DebugSystemPrompt:   opts.SystemPrompt,
		TurnMode:            opts.TurnMode,
		Watchdog:            watchdogConfigForProvider(prov),
		AutoReview:          arSnap,
		SessionBuildNotices: automaticReviewSessionBuildNotices(pr.Observer, arSnap),
	}
	if c, ok := prov.(finishOrViolateNudgeProvider); ok {
		sessOpts.SupportsFinishOrViolateNudge = c.SupportsFinishOrViolateNudge()
	}
	if c, ok := prov.(boundedHelperSandboxProvider); ok {
		sessOpts.UsesBoundedHelperSandbox = c.UsesBoundedHelperSandbox()
	}
	if c, ok := prov.(sessionResumeProvider); ok {
		sessOpts.SupportsSessionResume = c.SupportsSessionResume()
	}
	return cmd, env, sessOpts, nil
}

func appendAgenticoBinEnv(env []string) []string {
	path := currentAgenticoBinPath()
	if path == "" {
		return env
	}
	entry := "AGENTICO_BIN=" + path
	out := append([]string(nil), env...)
	for i, existing := range out {
		if strings.HasPrefix(existing, "AGENTICO_BIN=") {
			out[i] = entry
			return out
		}
	}
	return append(out, entry)
}

func currentAgenticoBinPath() string {
	path, err := os.Executable()
	if err != nil || strings.TrimSpace(path) == "" {
		path = os.Args[0]
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return strings.TrimSpace(path)
}

// AskingClauseForModel returns the asking-questions clause for a given model.
// Exported wrapper around the private askingQuestionsClauseForModel for use
// by external callers (e.g. TUI).
func (pr *PhaseRunner) AskingClauseForModel(model string) string {
	return pr.askingQuestionsClauseForModel(model)
}

// ModelForRole resolves the effective model for a phase role. If configured is
// non-empty it is returned as-is; otherwise the catalog default for the role is
// returned. Exported so external callers (e.g. refactor loop) can perform the
// same resolution that PhaseRunner uses internally.
func (pr *PhaseRunner) ModelForRole(configured string, role llm.PhaseRole) string {
	return pr.modelForRole(configured, role)
}
