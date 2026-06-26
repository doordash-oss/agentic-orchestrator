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

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/roles"
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

// NewPhaseRunner creates a PhaseRunner with a default execCommandRunner.
func NewPhaseRunner(sm ports.SessionManager, store ports.FeatureStore, stateDir string) *PhaseRunner {
	return &PhaseRunner{SessionManager: sm, FeatureStore: store, StateDir: stateDir, CommandRunner: &execCommandRunner{}}
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

	cmd, env, sessOpts, err := pr.BuildSession(BuildSessionOpts{
		Model:                          phaseModel,
		Prompt:                         cfg.Prompt,
		SystemPrompt:                   systemPrompt,
		AdditionalDirs:                 additionalDirs,
		AgentNames:                     cfg.AgentNames,
		PIDDir:                         pidDir,
		PermHandler:                    permHandlerFor(pr.DangerouslySkipPermissions, pr.PermissionCache, ""),
		WorkDir:                        workDir,
		EffortLevel:                    f.EffectivePipeline().EffortLevel(),
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
	pr.Observer.SessionStarted(sessionCtx, cfg.Phase.String(), sessionID, providerName, phaseModel, "")

	// Track reads of KB/skill/guideline files for observability.
	pr.installContextReadTracker(sess, sessionCtx, cfg.Phase.String(), sessionID, pr.StateDir)
	pr.installSubagentProgressTracker(sess, sessionCtx, cfg.Phase.String(), sessionID)

	sessionStart := time.Now()
	sess.AddCleanupFunc(func() {
		cost := ExtractSessionCost(sess)
		usage := toSessionUsage(cost)
		duration := time.Since(sessionStart)
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

// TweakSessionConfig holds optional overrides for RunTweakSession.
type TweakSessionConfig struct {
	WorkDir        string   // working directory override (typically the feature state dir)
	AdditionalDirs []string // other paths to mount via --add-dir for cross-repo context
}

// BuildTweakPrompt builds the initial user prompt for an interactive tweak session.
// It invokes the tweak-session skill and passes only the feature context needed
// to orient the session before the user provides the first requested change.
//
// The prose lives in internal/agent/prompts/templates/tweak.user.tmpl.
//
// When repoName is supplied (multi-repo tweak), the PR URL is resolved
// from the per-repo PRURLs() map so the prompt always shows the PR for
// the repo the user selected. Falling back to "first map value" would
// pick an arbitrary PR because Go map iteration order is undefined.
// When repoName is empty (legacy single-repo tweak), fall back to the
// feature-level f.PRURL with FirstRepoPRURL() as a deterministic
// secondary fallback.
func BuildTweakPrompt(f *feature.Feature, stateDir, skillsDir, repoName string) string {
	var prURL string
	if repoName != "" {
		prURL = f.PRURLs()[repoName]
	} else {
		prURL = f.PRURL()
		if prURL == "" {
			prURL = f.FirstRepoPRURL()
		}
	}
	return roles.BuildTweakPrompt(roles.TweakUserInput{
		SkillPath: tweakSkillPath(skillsDir),
		Name:      f.Name,
		PlanPath:  resolvePhaseArtifactPath(stateDir, f, "roadmap"),
		PRURL:     prURL,
	})
}

func tweakSkillPath(skillsDir string) string {
	if skillsDir == "" {
		return ""
	}
	return filepath.Join(skillsDir, "tweak-session", "SKILL.md")
}

// RunTweakSession starts an interactive tweak session for a feature.
// Tweak is a fully interactive PTY session: the user drives the agent
// directly via the attach view and ends the session with Ctrl+D. It does
// not participate in the autonomous Implement loop's progress.md /
// NEED_USER_INPUT handoff contract — there is no agent-emitted gate
// because the user is already on the keyboard. Returns the session ID;
// the session runs asynchronously.
func (pr *PhaseRunner) RunTweakSession(f *feature.Feature, cfgs ...TweakSessionConfig) (string, error) {
	prefix := f.CyclePrefix()
	if prefix == "" {
		prefix = "tweak" // fallback for safety
	}
	artifactDir := filepath.Join(ActiveRunDir(pr.StateDir, f), prefix)
	_ = os.MkdirAll(artifactDir, 0o755)

	var cfg TweakSessionConfig
	if len(cfgs) > 0 {
		cfg = cfgs[0]
	}

	implModel := pr.modelForRole(f.Models.Implementation, llm.PhaseImplementation)

	// Tweak is a fully interactive PTY session — the user drives the agent
	// directly, so no system prompt is injected (no completion protocol, no
	// AskUserQuestion contract). The session relies entirely on the user
	// prompt and the user's ongoing input.
	prompt := BuildTweakPrompt(f, pr.StateDir, pr.SkillsDir, "")

	sessionID := fmt.Sprintf("%s-impl-tweak", f.ID)

	var workDir string
	var additionalDirs []string
	if cfg.WorkDir != "" {
		workDir = cfg.WorkDir
		additionalDirs = cfg.AdditionalDirs
	} else {
		workDir, additionalDirs = resolveUnifiedWorkDir(f, pr.StateDir)
	}

	pidDir := filepath.Join(pr.StateDir, f.ID)

	featureCtx := observe.SpanContextForFeature(f.ID, f.TraceID, f.Name, f.FeatureSpanID).WithRun(f.ActiveRun)
	phaseCtx := featureCtx.Child()
	pr.Observer.PhaseStarted(phaseCtx, feature.PhaseImplement.String())

	cmd, env, sessOpts, err := pr.BuildSession(BuildSessionOpts{
		Model:          implModel,
		Prompt:         prompt,
		SystemPrompt:   "",
		AdditionalDirs: additionalDirs,
		AgentNames:     []string{},
		PIDDir:         pidDir,
		PermHandler:    permHandlerFor(pr.DangerouslySkipPermissions, pr.PermissionCache, ""),
		WorkDir:        workDir,
		EffortLevel:    f.EffectivePipeline().EffortLevel(),
		TurnMode:       ports.TurnModeInteractive,
		Phase:          feature.PhaseImplement,
	})
	if err != nil {
		return "", fmt.Errorf("building tweak session: %w", err)
	}
	if sessOpts == nil {
		sessOpts = &ports.SessionOpts{}
	}
	WriteDebugPrompts(artifactDir, sessOpts.DebugSystemPrompt, prompt)
	sessOpts.PermCacheScope = ""
	sessOpts.Kind = ports.KindTweak

	sess, err := pr.SessionManager.StartSession(sessionID, f.ID, feature.PhaseImplement, cmd, workDir, env, sessOpts)
	if err != nil {
		return "", fmt.Errorf("starting tweak session: %w", err)
	}

	sessionCtx := phaseCtx.Child()
	providerName := ""
	if sessOpts != nil {
		providerName = sessOpts.ProviderName
	}
	pr.Observer.SessionStarted(sessionCtx, feature.PhaseImplement.String(), sessionID, providerName, f.Models.Implementation, "")

	// Track reads of KB/skill/guideline files for observability.
	pr.installContextReadTracker(sess, sessionCtx, feature.PhaseImplement.String(), sessionID, pr.StateDir)
	pr.installSubagentProgressTracker(sess, sessionCtx, feature.PhaseImplement.String(), sessionID)

	costKey := f.ActiveTimingKey
	featureID := f.ID
	sessionStart := time.Now()
	sess.AddCleanupFunc(func() {
		cost := ExtractSessionCost(sess)
		usage := toSessionUsage(cost)
		duration := time.Since(sessionStart)
		pr.Observer.SessionEnded(sessionCtx, feature.PhaseImplement.String(), sessionID, "", usage, duration, sessionErrFromStatus(sess))
		if pr.FeatureStore != nil && cost.TotalCostUSD > 0 {
			_ = pr.FeatureStore.Modify(featureID, func(feat *feature.Feature) error {
				key := costKey
				if key == "" {
					key = feat.ActiveTimingKey
				}
				if key == "" {
					key = "implement"
				}
				feat.AddPhaseCost(key, cost.TotalCostUSD)
				return nil
			})
		}
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
	cmd, env, sessOpts, err := pr.BuildSession(BuildSessionOpts{
		Model:        kbModel,
		Prompt:       prompt,
		SystemPrompt: systemPrompt,
		// kbDir is a sibling of pr.StateDir (under knowledge-base/<repo>), not a
		// descendant, so it must be mounted explicitly: this makes it readable and,
		// via the default writable-root derivation, writable for managed providers.
		AdditionalDirs:                 []string{pr.StateDir, kbDir},
		AgentNames:                     knowledgeBaseAgentNames(),
		PIDDir:                         pidDir,
		PermHandler:                    permHandlerFor(pr.DangerouslySkipPermissions, pr.PermissionCache, repo.Name),
		RepoName:                       repo.Name,
		WorkDir:                        workDir,
		EffortLevel:                    f.EffectivePipeline().EffortLevel(),
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
	pr.Observer.SessionStarted(sessionCtx, feature.PhaseKnowledgeBase.String(), sessionID, sessOpts.ProviderName, kbModel, repo.Name)
	pr.installContextReadTracker(sess, sessionCtx, feature.PhaseKnowledgeBase.String(), sessionID, pr.StateDir)
	pr.installSubagentProgressTracker(sess, sessionCtx, feature.PhaseKnowledgeBase.String(), sessionID)
	sessionStart := time.Now()
	sess.AddCleanupFunc(func() {
		cost := ExtractSessionCost(sess)
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
	cfg := PlanLoopConfig{
		Feature:                    f,
		FeatureStore:               pr.FeatureStore,
		StateDir:                   pr.StateDir,
		ResearchArtifactPath:       researchArtifactPath,
		DesignArtifactPath:         f.DesignArtifactPath(),
		QAFilePaths:                qaFilePaths,
		KBInfos:                    kbInfos,
		WorkDir:                    workDir,
		AdditionalDirs:             additionalDirs,
		MaxAttempts:                f.MaxPlanIterations,
		DangerouslySkipPermissions: pr.DangerouslySkipPermissions,
		PermissionCache:            pr.PermissionCache,
		RepoName:                   repoName,
		BuildSession:               pr.BuildSession,
		AskingClause:               pr.askingQuestionsClauseForModel(planningModel),
		EffortLevel:                f.EffectivePipeline().EffortLevel(),
		SkillsDir:                  pr.SkillsDir,
		GuidelinesDir:              pr.GuidelinesDir,
		FinishOrViolateNudge:       pr.finishOrViolateNudgeForModel(planningModel),
		Observer:                   pr.Observer,
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
	cfg := PhasePlanLoopConfig{
		PlanLoopConfig: PlanLoopConfig{
			Feature:                    f,
			FeatureStore:               pr.FeatureStore,
			StateDir:                   pr.StateDir,
			DesignArtifactPath:         f.DesignArtifactPath(),
			QAFilePaths:                qaFilePaths,
			KBInfos:                    kbInfos,
			WorkDir:                    workDir,
			AdditionalDirs:             additionalDirs,
			MaxAttempts:                maxAttempts,
			DangerouslySkipPermissions: pr.DangerouslySkipPermissions,
			PermissionCache:            pr.PermissionCache,
			RepoName:                   planRepoName,
			BuildSession:               pr.BuildSession,
			AskingClause:               pr.askingQuestionsClauseForModel(phasePlanModel),
			EffortLevel:                f.EffectivePipeline().EffortLevel(),
			SkillsDir:                  pr.SkillsDir,
			GuidelinesDir:              pr.GuidelinesDir,
			FinishOrViolateNudge:       pr.finishOrViolateNudgeForModel(phasePlanModel),
			Observer:                   pr.Observer,
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

	// Cycles (rebase, tweak, review-comments) operate on the whole branch,
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
		PhaseType:                  phaseType,
		RoadmapPath:                roadmapPath,
		DesignArtifactPath:         f.DesignArtifactPath(),
		DangerouslySkipPermissions: pr.DangerouslySkipPermissions,
		PermissionCache:            pr.PermissionCache,
		BuildSession:               pr.BuildSession,
		AskingClause:               pr.askingQuestionsClauseForModel(implementationModel),
		EffortLevel:                f.EffectivePipeline().EffortLevel(),
		SkillsDir:                  pr.SkillsDir,
		GuidelinesDir:              pr.GuidelinesDir,
		SkipIterationReview:        f.EffectivePipeline().ShouldSkipIterationReview(),
		FinishOrViolateNudge:       pr.finishOrViolateNudgeForModel(implementationModel),
		Observer:                   pr.Observer,
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
// loop for a post-publish cycle (e.g., post-tweak "review changes? y/n"
// modal "y" path). Cwd at the feature state dir; --add-dir for every
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
		BuildSession:               pr.BuildSession,
		AskingClause:               pr.askingQuestionsClauseForModel(implementationModel),
		EffortLevel:                f.EffectivePipeline().EffortLevel(),
		SkillsDir:                  pr.SkillsDir,
		GuidelinesDir:              pr.GuidelinesDir,
		FinishOrViolateNudge:       pr.finishOrViolateNudgeForModel(reviewModel) && pr.finishOrViolateNudgeForModel(implementationModel),
		Observer:                   pr.Observer,
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
		BuildSession:               pr.BuildSession,
		AskingClause:               pr.askingQuestionsClauseForModel(model),
		EffortLevel:                f.EffectivePipeline().EffortLevel(),
		SkillsDir:                  pr.SkillsDir,
		GuidelinesDir:              pr.GuidelinesDir,
		FinishOrViolateNudge:       pr.finishOrViolateNudgeForModel(reviewModel) && pr.finishOrViolateNudgeForModel(model),
		Observer:                   pr.Observer,
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
		BuildSession:               pr.BuildSession,
		AskingClause:               pr.askingQuestionsClauseForModel(model),
		EffortLevel:                f.EffectivePipeline().EffortLevel(),
		SkillsDir:                  pr.SkillsDir,
		GuidelinesDir:              pr.GuidelinesDir,
		FinishOrViolateNudge:       pr.finishOrViolateNudgeForModel(reviewModel) && pr.finishOrViolateNudgeForModel(model),
		Observer:                   pr.Observer,
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

// resolveImplementArtifactDir returns the artifact directory for implementation
// within the feature's active run. When in a roadmap phase, uses phase-scoped
// directories. Includes refactor prefix when an active refactor cycle is in
// progress.
func (pr *PhaseRunner) resolveImplementArtifactDir(f *feature.Feature) string {
	runDir := ActiveRunDir(pr.StateDir, f)
	// Cycle prefix takes precedence — when an active cycle is running,
	// artifacts go into the cycle subtree (e.g. rebase-1/, tweak-2/).
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
// each repo's worktree path plus stateDir.
// Without worktrees: workDir = repos[0].Path, additionalDirs includes
// each other repo's path plus stateDir.
// No repos: workDir = stateDir, additionalDirs = [stateDir].
func resolveUnifiedWorkDir(f *feature.Feature, stateDir string) (workDir string, additionalDirs []string) {
	if len(f.Repos) == 0 {
		return stateDir, []string{stateDir}
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
		additionalDirs = []string{stateDir}
		for _, r := range f.Repos {
			additionalDirs = append(additionalDirs, r.WorktreePath)
		}
		return workDir, additionalDirs
	}
	workDir = f.Repos[0].Path
	additionalDirs = []string{stateDir}
	for _, r := range f.Repos[1:] {
		additionalDirs = append(additionalDirs, r.Path)
	}
	return workDir, additionalDirs
}

// BuildSessionOpts holds parameters for BuildSession.
type BuildSessionOpts struct {
	Model           string
	Prompt          string
	SystemPrompt    string
	AllowedTools    []string
	DisallowedTools []string
	AdditionalDirs  []string
	// WritableRoots overrides the provider write surface. Nil preserves the
	// default StateDir + AdditionalDirs behavior, except that read-only context
	// mounts are not passed as command-level writable roots.
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
	// Leave empty for paths that don't have a marker contract (e.g. tweak
	// PTY sessions); the provider falls back to its legacy heuristic.
	MarkerPath string
	// ResumeSessionID, when set, asks the provider to resume that prior session
	// identity rather than start a fresh one. It is forwarded to both command and
	// protocol setup so each provider resumes via its own supported path.
	ResumeSessionID string
}

// BuildSessionFunc is the callback signature for session creation via the registry.
// Used by TUI components and loop configs that need to create sessions.
type BuildSessionFunc func(BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error)

var pendingToolWatchdogConfig = ports.SessionWatchdogConfig{
	PendingToolIdleTimeout: 5 * time.Minute,
	PollInterval:           time.Second,
}

type pendingToolWatchdogProvider interface {
	EnablesPendingToolWatchdog() bool
}

func watchdogConfigForProvider(provider llm.LLMProvider) *ports.SessionWatchdogConfig {
	p, ok := provider.(pendingToolWatchdogProvider)
	if !ok || !p.EnablesPendingToolWatchdog() {
		return nil
	}
	cfg := pendingToolWatchdogConfig
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
		writableRoots = []string{pr.StateDir}
		writableRoots = append(writableRoots, opts.AdditionalDirs...)
	}

	// Read roots are everything a provider may read without per-call mediation:
	// the feature state, the working directory, and every additional mounted dir
	// (skills, guidelines, worktrees, knowledge base, images, attachments).
	readRoots := append([]string{pr.StateDir}, opts.AdditionalDirs...)
	if opts.WorkDir != "" {
		readRoots = append(readRoots, opts.WorkDir)
	}

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
	})

	sessOpts = &ports.SessionOpts{
		PIDDir:            opts.PIDDir,
		PermHandler:       opts.PermHandler,
		InitialPrompt:     opts.Prompt,
		ContextWindow:     contextWindow,
		RepoName:          opts.RepoName,
		LogPath:           opts.LogPath,
		ProviderName:      prov.Name(),
		Protocol:          protocol,
		DebugSystemPrompt: opts.SystemPrompt,
		TurnMode:          opts.TurnMode,
		Watchdog:          watchdogConfigForProvider(prov),
	}
	if c, ok := prov.(finishOrViolateNudgeProvider); ok {
		sessOpts.SupportsFinishOrViolateNudge = c.SupportsFinishOrViolateNudge()
	}
	if c, ok := prov.(boundedHelperSandboxProvider); ok {
		sessOpts.UsesBoundedHelperSandbox = c.UsesBoundedHelperSandbox()
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
