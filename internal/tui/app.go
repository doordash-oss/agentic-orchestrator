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

package tui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

// TickMsg fires periodically to check for missed session events.
type TickMsg time.Time

const (
	tickInterval            = 3 * time.Second
	waitingInputHelpMessage = "Agent is waiting for input — press 'a' to answer"
	questionHelpMessage     = "Agent has a question — press 'a' to answer"
)

type View int

const (
	ViewDashboard View = iota
	ViewDetail
	ViewWizard
	ViewAttach
	ViewPublish
	ViewRecovery
	ViewLogs
	ViewReviewComments
	ViewChat
	ViewArtifactReview
	ViewWelcome
)

// RefreshFeaturesMsg triggers a feature list refresh.
type RefreshFeaturesMsg struct{}

// HelpResolvedMsg is emitted by the attach view after a user action that may
// have resolved a session's waiting state (answer submitted, permission
// decided, freeform chat sent). The app model reconciles the feature's
// HelpQueue against current session statuses — so badges track session
// state accurately instead of being cleared en masse on detach.
type HelpResolvedMsg struct{ FeatureID string }

// ViewTransitionMsg switches to a new view.
type ViewTransitionMsg struct {
	View      View
	FeatureID string
}

// PublishResultMsg carries the result of a publish operation.
type PublishResultMsg struct {
	PRURL string
	Err   error
}

// SDKSessionEventMsg carries a structured SDK message from a session to the TUI.
type SDKSessionEventMsg struct {
	Event session.SDKEventMsg
}

// SessionDoneMsg carries a session completion event.
type SessionDoneTUIMsg struct {
	Done session.SessionDoneMsg
}

// PhaseCompletedMsg indicates a phase session completed and feature state should advance.
type PhaseCompletedMsg struct {
	FeatureID   string
	Phase       feature.Phase
	SessionID   string
	Success     bool
	ErrorDetail string // human-readable crash/error reason from the session
}

// ImplementLoopDoneMsg indicates the implementation loop completed.
type ImplementLoopDoneMsg struct {
	FeatureID string
	Result    *agent.LoopResult
}

// MultiRepoImplDoneMsg indicates the multi-repo orchestrator completed.
type MultiRepoImplDoneMsg struct {
	FeatureID string
	Result    *agent.OrchestratorResult
}

// RepoCycleLoopDoneMsg indicates a per-repo post-publish cycle's implementation completed.
type RepoCycleLoopDoneMsg struct {
	FeatureID string
	RepoName  string
	CycleType feature.RepoCycleType
	Result    *agent.LoopResult
}

// repoCycleStartResultMsg reports whether a repo-cycle implementation launch
// succeeded. Errors are surfaced back through Update so the dashboard does not
// silently bounce back to Published on a rejected cycle start.
type repoCycleStartResultMsg struct {
	FeatureID string
	RepoName  string
	CycleType feature.RepoCycleType
	Err       error
}

// restartRepoCycleMsg triggers a repo cycle restart after clearing stale state.
type restartRepoCycleMsg struct {
	FeatureID   string
	RepoName    string
	CycleType   feature.RepoCycleType
	PlanContent string
}

// showRefactorForRepoMsg triggers the refactor prompt textarea for a specific repo.
type showRefactorForRepoMsg struct {
	FeatureID string
	RepoName  string
}

// restartRefactorCycleMsg triggers a refactor cycle restart after clearing stale state.
type restartRefactorCycleMsg struct {
	FeatureID string
	RepoName  string
	Prompt    string
}

// RebaseRepoCycleResultMsg carries the result of a per-repo rebase attempt.
type RebaseRepoCycleResultMsg struct {
	FeatureID     string
	RepoName      string
	Success       bool
	HasConflict   bool
	Err           error
	RebaseTarget  string
	ConflictFiles []string // files with unmerged conflicts (only when HasConflict)
}

// PlanLoopDoneMsg is sent when the planning loop (with validation) completes.
type PlanLoopDoneMsg struct {
	FeatureID string
	Result    *agent.PlanLoopResult
}

// ArtifactReviewStartMsg triggers opening the artifact review editor.
type ArtifactReviewStartMsg struct {
	FeatureID    string
	ArtifactPath string
	ReviewMode   string        // "plan" | "rewind" | "gate"
	RewindPhase  feature.Phase // target phase for rewind/gate
	AutoAttach   bool          // true = auto-triggered (badge only), false = user-initiated (open editor)
	WorkDir      string        // feature worktree/repo path for repo-aware agent analysis
	Warnings     []string      // non-fatal context-resolution warnings surfaced in the status bar
}

// PlanReviewDecisionMsg carries the user's decision from the plan review menu.
type PlanReviewDecisionMsg struct {
	FeatureID string
	Decision  string // "iterate" or "proceed"
}

// RoadmapReviewDecisionMsg signals the user's decision from a roadmap approval menu.
type RoadmapReviewDecisionMsg struct {
	FeatureID string
	Decision  string // "approve" or "reject"
	Comment   string // rejection feedback from the user (chat history)
}

// StartPhaseMsg triggers execution of a phase after state has been reset.
type StartPhaseMsg struct {
	FeatureID string
	Phase     feature.Phase
}

// LogsContentMsg carries log content to display in the logs view.
type LogsContentMsg struct {
	Title     string
	Content   string
	FeatureID string // optional: set when viewing diff to enable publish shortcut
}

// tweakCommitDoneMsg carries the result of the async feature-level tweak
// commit step. hadChanges is true when at least one Feature.Repos worktree
// had uncommitted changes that the orchestrator committed.
type tweakCommitDoneMsg struct {
	featureID  string
	hadChanges bool
	err        error
}

// RebaseResultMsg carries the result of a rebase attempt.
type RebaseResultMsg struct {
	FeatureID     string
	Success       bool     // true = clean rebase + force push done
	HasConflict   bool     // true = conflicts detected, need implementation loop
	Err           error    // non-nil on unexpected failure
	RebaseTarget  string   // branch that was used as the rebase target
	ConflictFiles []string // files with unmerged conflicts (only when HasConflict)
}

// MergeLocalResultMsg carries the result of merging a feature branch into the local base branch.
type MergeLocalResultMsg struct {
	FeatureID string
	Err       error
}

// ReviewCommentsFetchedMsg carries fetched PR review comments.
type ReviewCommentsFetchedMsg struct {
	FeatureID   string
	FeatureSlug string
	Comments    []git.ReviewComment
	Err         error
}

// DeleteFeatureDoneMsg signals that a feature deletion has completed.
type DeleteFeatureDoneMsg struct {
	FeatureID string
	Err       error
}

// RewindDoneMsg signals that a feature rewind has completed.
type RewindDoneMsg struct {
	FeatureID   string
	TargetPhase feature.Phase
	Warnings    []string
	Err         error
}

// RestartFromBeginningDoneMsg signals that a feature restart-from-beginning has completed.
// Kept for backward compatibility with tests.
type RestartFromBeginningDoneMsg struct {
	FeatureID string
	Err       error
}

// restartBusyMsg signals that orchestrator.RestartPhase declined the request
// because the feature still has active sessions (typically a stop is mid-flight).
// The user-visible status surfaces as a hint to wait and retry; the second key
// press once the feature has settled then proceeds normally.
type restartBusyMsg struct {
	FeatureID string
}

// RewindReviewDecisionMsg signals the user's decision from the rewind review menu.
type RewindReviewDecisionMsg struct {
	FeatureID string
	Phase     feature.Phase
	Decision  string // "proceed"
}

type roadmapRewindRow struct {
	Number            int
	Total             int
	Title             string
	PhaseType         string
	Status            string
	Effect            string
	ResetBoundary     string
	AnchorAvailable   bool
	UnavailableReason string
	CurrentPhase      bool
}

// GateReviewDecisionMsg signals the user's decision from a checkpoint gate review.
type GateReviewDecisionMsg struct {
	FeatureID string
	Phase     feature.Phase
	Decision  string // "proceed"
}

// FeatureCreatedMsg carries the result of an async feature creation.
type FeatureCreatedMsg struct {
	FeatureIDs []string // IDs of created features (may be multiple for multi-repo)
	Err        error
}

// featureSummaryMsg carries a Claude-generated summary for a feature.
type featureSummaryMsg struct {
	featureID string
	summary   string
}

// ProgramRef holds a reference to the tea.Program that survives BubbleTea's value copying.
type ProgramRef struct {
	P *tea.Program
}

// AppOption configures optional AppModel fields.
type AppOption func(*AppModel)

// WithConfigPath sets the config file path for persisting auto-discovered repos.
func WithConfigPath(path string) AppOption {
	return func(a *AppModel) { a.configPath = path }
}

// WithWorkspaceDir sets the workspace directory for auto-discovering repos.
func WithWorkspaceDir(dir string) AppOption {
	return func(a *AppModel) { a.workspaceDir = dir }
}

// WithDangerouslySkipPermissions enables --dangerously-skip-permissions for all Claude sessions.
func WithDangerouslySkipPermissions() AppOption {
	return func(a *AppModel) { a.dangerouslySkipPerms = true }
}

// WithPhaseRunner injects a pre-configured PhaseRunner (used by tests).
func WithPhaseRunner(pr *agent.PhaseRunner) AppOption {
	return func(a *AppModel) { a.phaseRunner = pr }
}

// WithRegistry injects the LLM provider registry.
func WithRegistry(r *llm.Registry) AppOption {
	return func(a *AppModel) { a.registry = r }
}

// WithObserver injects the observability observer.
func WithObserver(o Observer) AppOption {
	return func(a *AppModel) { a.observer = o }
}

// notifyType distinguishes notification categories so they get independent cooldowns.
type notifyType int

const (
	notifyWaitingInput notifyType = iota
	notifyAPIError
)

// notifyKey identifies a specific notification bucket: one per (feature, type) pair.
type notifyKey struct {
	featureID  string
	notifyType notifyType
}

// minNotifyInterval is the dedup window — prevents double-notifications
// when both handleSDKEvent and handleTick detect the same waiting state
// within a short window.
const minNotifyInterval = 10 * time.Second

type AppModel struct {
	currentView    View
	dashboard      DashboardModel
	detail         DetailModel
	wizard         WizardModel
	publish        PublishModel
	attach         AttachModel
	chat           ChatModel
	welcome        WelcomeModel
	artifactReview ArtifactReviewModel
	chatReady      bool // true after first lazy init; persists across esc/reopen
	chatOpen       bool // true when chat panel is visible at bottom of dashboard
	recovery       RecoveryModel
	logs           LogsModel
	featureManager *feature.Manager
	sessionManager *session.Manager
	orchestrator   orchestratorAPI
	phaseRunner    *agent.PhaseRunner
	eventCh        chan interface{}
	programRef     *ProgramRef
	width, height  int

	// Config persistence for auto-discovery
	configPath   string
	workspaceDir string

	// LLM provider registry for model lookups
	registry *llm.Registry

	// Skip all Claude CLI permission prompts when true
	dangerouslySkipPerms bool

	// Observability
	observer Observer

	// Shared permission cache for auto-approving remembered tool requests
	permissionCache *permission.Cache

	// Help input state for human-in-the-loop
	helpInputActive bool
	helpInput       textinput.Model
	helpFeatureID   string
	helpQuestion    string

	// Interactive tweak session state
	tweakFinishingFeatureID  string // set by Ctrl+D to signal explicit finish intent
	tweakCompletingFeatureID string // set when tweak completion is in flight; guards against duplicate signals

	// Tweak Final Review modal state
	tweakReviewModalActive    bool
	tweakReviewModalFeatureID string
	tweakReviewModalRepoName  string

	// Edit config overlay state (feature-config editing on quiescent features).
	editConfigActive bool
	editConfig       EditConfigModel

	// Inline refactor input state
	refactorInputActive bool
	refactorInput       textarea.Model
	refactorFeatureID   string
	refactorFeatureName string

	// Delete confirmation state
	deleteConfirmActive bool
	deleteFeatureID     string
	deleteFeatureName   string

	// Rewind menu state
	rewindMenuActive         bool
	rewindMenuFeatureID      string
	rewindMenuChoices        []feature.RewindChoice
	rewindMenuCursor         int
	rewindMenuHasEscalation  bool
	rewindMenuUpgradeOptions []feature.PipelineProfile

	// Roadmap phase picker shown after choosing Implement rewind on multi-phase features.
	rewindPhasePickerActive    bool
	rewindPhasePickerFeatureID string
	rewindPhasePickerRows      []roadmapRewindRow
	rewindPhasePickerCursor    int

	// Partial Implement unavailable fallback prompt.
	rewindPartialUnavailableActive bool
	rewindPartialUnavailableRow    roadmapRewindRow
	rewindPartialUnavailableReason string

	// Rewind confirmation state
	rewindConfirmActive       bool
	rewindConfirmFeatureID    string
	rewindConfirmPhase        feature.Phase
	rewindConfirmPhaseName    string
	rewindConfirmEscalates    feature.PipelineProfile // non-empty if this rewind escalates
	rewindConfirmOverridesKB  bool                    // true when rewind will redirect to KB Build
	rewindConfirmUpgrade      feature.PipelineProfile // non-empty if this is a pipeline upgrade + KB rewind
	rewindConfirmRoadmapRow   roadmapRewindRow
	rewindConfirmRoadmapPhase int

	// Rewinding in-progress indicator (shown while sessions are stopping)
	rewindingFeatureID string

	// Manual publish confirmation state
	manualPublishConfirmActive bool
	manualPublishFeatureID     string
	manualPublishFeatureName   string

	// Resume all confirmation state
	resumeAllConfirmActive bool
	resumeAllCount         int

	// Review comments view
	reviewComments ReviewCommentsModel

	// Stop confirmation state
	stopConfirmActive      bool
	stopConfirmFeatureID   string
	stopConfirmFeatureName string

	// Refactor pipeline selector state — shown after refactor prompt submission
	refactorPipelineActive    bool
	refactorPipelineCursor    int
	refactorPipelineOptions   []feature.PipelineProfile
	refactorPipelineFeatureID string
	refactorPipelineRepoName  string // empty for single-repo refactor
	refactorPipelinePrompt    string

	// Cycle repo selector state (rebase/tweak/review on multi-repo features)
	cycleSelectActive    bool
	cycleSelectFeatureID string
	cycleSelectAction    feature.RepoCycleType
	cycleSelectRepos     []cycleRepoEntry
	cycleSelectCursor    int
	cycleSelectRefactor  string // repo name for refactor cycle (filled after textarea submit)

	// Quit confirmation state
	quitConfirmActive bool

	// Wizard async creation state
	wizardCreating     bool
	wizardCreatingName string

	// Single spinner for the entire app — shared by all detail views
	spinner spinner.Model

	// Track last notification time per feature+type to avoid notification spam.
	// Key: notifyKey{featureID, notifyType}, Value: last notify time.
	lastNotifyTime map[notifyKey]time.Time

	// Help overlay state
	helpOverlayActive bool
	helpOverlay       HelpOverlayModel

	// Workspace manager overlay state
	workspaceManagerActive bool
	workspaceManager       WorkspaceManagerModel

	// Transient status message (auto-cleared after timeout)
	statusMessage string
	statusTime    time.Time

	// Cached KB stale warnings per feature ID.
	// Computed lazily; cleared when KB phase completes.
	kbStaleWarnings map[string]string
}

func NewAppModel(fm *feature.Manager, sm *session.Manager, orch orchestratorAPI, permCache *permission.Cache, eventCh chan interface{}, opts ...AppOption) (AppModel, error) {
	stateDir := fm.Store.BaseDir

	// Scan for recovery items before any startup sweep. A second Agentic
	// process must surface the recovery screen without first interrupting the
	// live sessions it is meant to recover.
	//
	// Dead-process PID files contain SessionID and RepoName needed for --resume
	// recovery. ExecuteRecovery handles cleanup for all action paths.
	//
	// Plan invariant 2: app.go must not call session.ScanForRecovery directly —
	// recovery scanning routes exclusively through orchestrator.ScanRecovery so
	// the per-feature OnRecoveryScanned hook fires through the single chokepoint
	// and so the TUI is never responsible for the scan itself. A nil orchestrator
	// (e.g. tests that exercise non-recovery code paths) yields an empty items
	// slice, which is the correct default — no orphans visible → no recovery UI.
	var recoveryItems []session.RecoveryItem
	recoveryScanOK := orch == nil
	if orch != nil {
		portItems, err := orch.ScanRecovery(context.Background())
		if err != nil {
			log.Printf("startup recovery scan: %v", err)
		} else {
			recoveryScanOK = true
			recoveryItems = make([]session.RecoveryItem, len(portItems))
			for i, it := range portItems {
				recoveryItems[i] = it
			}
		}
	}

	// With no recoverable pidfiles, clean up stale running feature state left by
	// a prior hard exit. When recovery items exist, leave feature/session state
	// untouched so the user's recovery action owns the transition. If recovery
	// scanning fails, also leave state untouched; the safe default is to avoid
	// interrupting sessions we could not inspect.
	if orch != nil && recoveryScanOK && len(recoveryItems) == 0 {
		if err := orch.InterruptAllRunning(); err != nil {
			log.Printf("startup sweep: %v", err)
		}
	}

	features, _ := fm.List()
	if recoveryScanOK && len(recoveryItems) == 0 {
		for _, f := range features {
			if !featureCanRecoverQuestion(f) || !phaseNeedsUserInput(fm.Store.BaseDir, f, f.CurrentPhase, nil) {
				continue
			}
			_ = fm.Store.Modify(f.ID, func(f *feature.Feature) error {
				ensurePendingQuestionHelp(f)
				return nil
			})
		}
		features, _ = fm.List()
	}

	s := spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(lipgloss.NewStyle().Foreground(colorInfo)))

	app := AppModel{
		currentView:     ViewDashboard,
		dashboard:       NewDashboardModel(features, fm.Store.BaseDir),
		featureManager:  fm,
		sessionManager:  sm,
		orchestrator:    orch,
		permissionCache: permCache,
		eventCh:         eventCh,
		programRef:      &ProgramRef{},
		spinner:         s,
	}

	// Apply options first so dangerouslySkipPerms is set before PhaseRunner creation
	for _, opt := range opts {
		opt(&app)
	}

	// Discover repos from workspace roots at startup (in-memory only)
	if len(fm.Config.WorkspaceRoots) > 0 {
		config.DiscoverReposFromRoots(fm.Config)
	}

	// Detect welcome flow: empty workspace roots on a non-recovery startup
	if len(fm.Config.WorkspaceRoots) == 0 && app.currentView != ViewRecovery {
		app.welcome = NewWelcomeModel()
		app.currentView = ViewWelcome
	}

	// Propagate DSP flag to dashboard for visual theming
	app.dashboard.dangerouslySkipPerms = app.dangerouslySkipPerms

	// Restore collapsed sections from config
	if fm.Config != nil && len(fm.Config.UI.CollapsedSections) > 0 {
		app.dashboard.SetCollapsedSections(fm.Config.UI.CollapsedSections)
	}

	// Apply keyboard layout alternatives from config.
	ApplyKeyboardLayout(fm.Config.UI.KeyboardLayout)

	// Permission cache is now built by the permission.Module fx provider and
	// injected into NewAppModel. If a caller (e.g., test harness) passed nil,
	// fall back to a minimal in-memory store so the TUI can still boot.
	if app.permissionCache == nil {
		permDir := filepath.Join(filepath.Dir(stateDir), "permissions")
		permStore := permission.NewStore(permDir)
		if err := permStore.EnsureGlobalDefaults(); err != nil {
			return AppModel{}, fmt.Errorf("ensuring global permissions: %w", err)
		}
		app.permissionCache = permission.NewCache(permStore)
		app.permissionCache.LoadAndMerge("")
	}

	// Post-creation permission import is now wired through orchestrator.Hooks
	// (see internal/orchestrator/hooks.go) — no feature.Manager callback here.

	// Create PhaseRunner after options are applied so the flag is available
	if app.phaseRunner == nil {
		pr := agent.NewPhaseRunner(sm, fm.Store, stateDir)
		pr.CommandRunner = agent.NewExecCommandRunner()
		pr.Config = fm.Config
		pr.DangerouslySkipPermissions = app.dangerouslySkipPerms
		pr.PermissionCache = app.permissionCache
		app.phaseRunner = pr
	} else {
		// Injected runner (e.g. from WithPhaseRunner) — share the same cache
		// so the TUI "r" keybinding and the runner use one cache instance.
		app.phaseRunner.PermissionCache = app.permissionCache
		if app.phaseRunner.CommandRunner == nil {
			app.phaseRunner.CommandRunner = agent.NewExecCommandRunner()
		}
	}

	// Skills and guidelines reconcile is owned by cmd/agentico/main.go so the
	// launch spinner is the single source of "syncing..." UI; the resulting
	// PhaseRunner.SkillsDir / .GuidelinesDir fields are set there too.

	app.lastNotifyTime = make(map[notifyKey]time.Time)
	app.kbStaleWarnings = make(map[string]string)

	// If there are recovery items, show recovery view first
	if len(recoveryItems) > 0 {
		app.currentView = ViewRecovery
		app.recovery = NewRecoveryModel(recoveryItems)
	}

	return app, nil
}

// maybeNotifyUser fires notifyUserCmd only if enough time has passed since the
// last notification for this feature+type pair. Returns a tea.Cmd (possibly nil).
func (m *AppModel) maybeNotifyUser(featureID string, ntype notifyType, slug, reason string) tea.Cmd {
	if ntype == notifyWaitingInput && m.isInputNotificationMuted(featureID) {
		return nil
	}

	key := notifyKey{featureID: featureID, notifyType: ntype}
	now := time.Now()
	if last, ok := m.lastNotifyTime[key]; ok {
		if now.Sub(last) < minNotifyInterval {
			return nil
		}
	}
	m.lastNotifyTime[key] = now
	return notifyUserCmd(slug, reason)
}

func (m *AppModel) defaultInputNotificationMuted() bool {
	if m.featureManager != nil && m.featureManager.Config != nil {
		return m.featureManager.Config.Notifications.MuteFeatureInput
	}
	return false
}

func (m *AppModel) isInputNotificationMuted(featureID string) bool {
	defaultMuted := m.defaultInputNotificationMuted()
	if m.featureManager == nil {
		return defaultMuted
	}
	f, err := m.featureManager.Get(featureID)
	if err != nil || f == nil {
		return defaultMuted
	}
	if f.MuteInputNotifications != nil {
		return *f.MuteInputNotifications
	}
	return defaultMuted
}

func (m *AppModel) toggleFeatureInputNotifications(featureID string) (bool, error) {
	var muted bool
	defaultMuted := m.defaultInputNotificationMuted()
	if m.featureManager == nil {
		return false, fmt.Errorf("feature manager not initialized")
	}
	err := m.featureManager.Store.Modify(featureID, func(f *feature.Feature) error {
		currentMuted := defaultMuted
		if f.MuteInputNotifications != nil {
			currentMuted = *f.MuteInputNotifications
		}
		nextMuted := !currentMuted
		f.MuteInputNotifications = &nextMuted
		muted = nextMuted
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("toggling input notifications: %w", err)
	}
	return muted, nil
}

func (m AppModel) Init() tea.Cmd {
	var cmds []tea.Cmd
	if m.eventCh != nil {
		cmds = append(cmds, m.listenForEvents())
	}
	if m.orchestrator != nil {
		if cmd := m.listenForOrchestratorEvents(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	cmds = append(cmds, tea.Tick(tickInterval, func(t time.Time) tea.Msg {
		return TickMsg(t)
	}))
	// Start the app-level spinner
	cmds = append(cmds, m.spinner.Tick)

	// Resume auto-publish for any CodeReady features that were pending before a crash.
	// One publishCmd per feature — orchestrator.Publish handles per-repo fan-out,
	// and the TryCompletePublish short-circuit inside publishCmd handles the
	// all-repos-already-published case.
	if features, err := m.featureManager.List(); err == nil {
		for _, f := range features {
			if f.Status == feature.StatusCodeReady && f.IsPublishable() && f.Checkpoints.AutoPublish() {
				cmds = append(cmds, m.publishCmd(f.ID))
			}
		}
	}

	return tea.Batch(cmds...)
}

func (m AppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.dashboard.width = msg.Width
		m.dashboard.height = msg.Height
		m.dashboard.updateScrollState(m.dashboard.effectivePanelHeight())
		m.dashboard.preview.width = msg.Width
		m.dashboard.preview.height = msg.Height
		m.detail.width = msg.Width
		m.detail.height = msg.Height
		m.wizard.SetWidth(msg.Width)
		m.wizard.height = msg.Height
		m.welcome.width = msg.Width
		m.welcome.height = msg.Height
		m.logs = m.logs.WithSize(msg.Width, msg.Height)
		m.reviewComments = m.reviewComments.WithSize(msg.Width, msg.Height)
		if m.refactorInputActive {
			if m.currentView == ViewDashboard && m.dashboard.getLayoutMode() == layoutNarrow {
				// Narrow dashboard hides the right panel, so transition
				// to detail view to keep the refactor textarea visible.
				m.currentView = ViewDetail
				if sel := m.dashboard.SelectedFeature(); sel != nil {
					m.detail = NewDetailModel(sel, m.featureManager.Store.BaseDir)
					m.detail.width = msg.Width
					m.detail.height = msg.Height
				}
				boxWidth := min(msg.Width-4, 76)
				m.refactorInput.SetWidth(max(boxWidth-6, 20))
			} else if m.currentView == ViewDashboard {
				m.refactorInput.SetWidth(max(m.rightPanelContentWidth()-6, 20))
			} else {
				boxWidth := min(msg.Width-4, 76)
				m.refactorInput.SetWidth(max(boxWidth-6, 20))
			}
		}
		if m.chatOpen {
			chatH := chatPanelHeight(msg.Height)
			m.chat = m.chat.resize(msg.Width, chatH)
		} else if m.currentView == ViewChat {
			m.chat, _ = m.chat.Update(msg)
		}
		if m.helpOverlayActive {
			m.helpOverlay = NewHelpOverlayModel(m.helpOverlay.context, msg.Width, msg.Height)
		}
		if m.workspaceManagerActive {
			m.workspaceManager, _ = m.workspaceManager.Update(msg)
		}
		if m.currentView == ViewWizard && m.wizard.IsPickerActive() {
			m.wizard.dirPicker, _ = m.wizard.dirPicker.Update(msg)
		}
		if m.currentView == ViewAttach {
			m.attach, _ = m.attach.Update(msg)
		}
		if m.currentView == ViewArtifactReview {
			m.artifactReview, _ = m.artifactReview.Update(msg)
		}
		if m.currentView == ViewWelcome {
			m.welcome, _ = m.welcome.Update(msg)
		}
		return m, nil

	case RefreshFeaturesMsg:
		features, listErr := m.featureManager.List()
		if listErr != nil && !feature.IsPartialLoadError(listErr) {
			m.statusMessage = "Feature refresh failed: " + firstLine(listErr.Error())
			m.statusTime = time.Now()
			return m, nil
		}
		m.dashboard.SetFeatures(features)
		m.refreshKBStaleWarnings(features)
		// Also refresh detail if viewing one
		if m.currentView == ViewDetail && m.detail.feature != nil {
			f, err := m.featureManager.Get(m.detail.feature.ID)
			if err == nil {
				m.detail = NewDetailModel(f, m.featureManager.Store.BaseDir)
			}
		}
		// Tab churn — if attached to a feature, rebuild its tab list so
		// validator sessions that spawned after initial attach (or completed
		// since) are reflected in the tab bar. Preserves per-tab pasted media.
		if m.currentView == ViewAttach && m.attach.featureID != "" {
			if f, err := m.featureManager.Get(m.attach.featureID); err == nil && f != nil {
				next := m.buildRepoTabs(f)
				if len(next) > 0 {
					m.attach.rebuildTabs(next)
				}
			}
		}
		return m, nil

	case repoCycleStartResultMsg:
		if msg.Err != nil {
			cycleLabel := strings.ReplaceAll(string(msg.CycleType), "-", " ")
			if msg.RepoName != "" {
				m.statusMessage = fmt.Sprintf("✗ %s %s: %s", cycleLabel, msg.RepoName, firstLine(msg.Err.Error()))
			} else {
				m.statusMessage = fmt.Sprintf("✗ %s: %s", cycleLabel, firstLine(msg.Err.Error()))
			}
			m.statusTime = time.Now()
		}
		return m, func() tea.Msg { return RefreshFeaturesMsg{} }

	case HelpResolvedMsg:
		m.reconcileHelpQueue(msg.FeatureID)
		return m, func() tea.Msg { return RefreshFeaturesMsg{} }

	case attachTweakSessionMsg:
		sess := m.sessionManager.GetSession(msg.sessionID)
		if sess == nil {
			return m, nil
		}
		cmd := m.attachToSession(sess)
		m.attach.isTweakSession = true
		return m, tea.Batch(cmd, m.listenForEvents())

	case ViewTransitionMsg:
		if m.currentView == ViewAttach || m.currentView == ViewArtifactReview {
			return m, nil
		}
		return m.transitionTo(msg.View, msg.FeatureID), nil

	case PublishResultMsg:
		if msg.Err != nil {
			m.publish.errMsg = msg.Err.Error()
		} else {
			m.publish.prURL = msg.PRURL
		}
		m.publish.step = publishStepDone
		return m, nil

	case spinner.TickMsg:
		// Update the single app-level spinner
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		// Pass spinner frame to chat for its thinking indicator animation
		if m.chatReady && m.chat.responding {
			m.chat.spinnerView = m.spinner.View()
			m.chat.rebuildViewport()
		}
		// Pass spinner frame to attach for tool thinking animation. The attach
		// view renders this line directly in View(), so we avoid rebuilding the
		// entire transcript on every spinner tick.
		if m.currentView == ViewAttach && m.attach.thinkingLine != "" {
			m.attach.spinnerView = m.spinner.View()
		}
		return m, cmd

	case TickMsg:
		return m.handleTick()

	case StartPhaseMsg:
		if msg.Phase == feature.PhasePublish {
			return m, m.publishCmd(msg.FeatureID)
		}
		// Test harness may construct an AppModel without an orchestrator; skip
		// dispatch rather than panic with a nil dereference.
		if m.orchestrator == nil {
			return m, nil
		}
		// All other phases flow through orchestrator.StartFeature → startPhase →
		// PhaseRunner. The orchestrator already knows the current pipeline state,
		// so we don't need phase-specific dispatch on the TUI side.
		return m, func() tea.Msg {
			_ = m.orchestrator.StartFeature(msg.FeatureID)
			return RefreshFeaturesMsg{}
		}

	case SDKSessionEventMsg:
		return m.handleSDKEvent(msg)

	case SessionDoneTUIMsg:
		return m.handleSessionDone(msg)

	case OrchFeatureCreatedMsg,
		OrchFeatureStartedMsg,
		OrchFeatureAdvancedMsg,
		OrchFeatureCompletedMsg,
		OrchFeatureFailedMsg,
		OrchFeatureInterruptedMsg,
		OrchPhaseStartedMsg,
		OrchPhaseCompletedMsg,
		OrchReviewRequiredMsg,
		OrchPublishStartedMsg,
		OrchRepoStatusChangedMsg,
		OrchRecoveryScannedMsg,
		OrchRecoveryExecutedMsg:
		// Orchestrator events are informational for the TUI; the underlying
		// state is still owned by feature.Manager. Trigger a dashboard refresh
		// and re-arm the listener for the next event.
		return m, tea.Batch(
			func() tea.Msg { return RefreshFeaturesMsg{} },
			m.listenForOrchestratorEvents(),
		)

	case OrchTweakReviewApprovedMsg:
		// Final Review passed for the feature-level tweak cycle. The
		// orchestrator emits a single TweakReviewApproved event; the finish
		// command commits review fixes, rebases/pushes every Feature.Repos
		// worktree, routes conflicts to RebaseResultMsg, and updates per-repo
		// cycle status. msg.RepoName remains on the message surface for
		// legacy callers but is ignored by the finish command.
		return m, tea.Batch(
			m.completeTweakFinishCmd(msg.FeatureID, msg.RepoName, true),
			m.listenForOrchestratorEvents(),
		)

	case OrchFeatureConfigChangedMsg:
		// Secondary reload broadcast (non-blocking, droppable). The authoritative
		// close path is editConfigResultMsg; this case is idempotent if the
		// overlay is already closed and just triggers a dashboard refresh so
		// other consumers pick up the new values.
		return m, tea.Batch(
			func() tea.Msg { return RefreshFeaturesMsg{} },
			m.listenForOrchestratorEvents(),
		)

	case editConfigResultMsg:
		if msg.err != nil {
			m.editConfig.saving = false
			m.editConfig.saveErr = msg.err.Error()
			return m, nil
		}
		// Successful save — close the overlay IMMEDIATELY from this authoritative
		// result msg and reload the feature so the detail view reflects the new
		// value. Do NOT wait for OrchFeatureConfigChangedMsg: that event flows
		// through the non-blocking emitEvent channel which can drop on saturation.
		m.editConfigActive = false
		m.editConfig = EditConfigModel{}
		return m, func() tea.Msg { return RefreshFeaturesMsg{} }

	case OrchPublishCompletedMsg:
		// Conflict routes to rebase UX; non-conflict success/failure refreshes.
		if msg.Error != nil {
			var conflict *orchestrator.PublishConflictError
			if errors.As(msg.Error, &conflict) {
				m.publish.step = publishStepDone
				if m.currentView != ViewAttach && m.currentView != ViewArtifactReview {
					m.currentView = ViewDashboard
				}
				return m.handleRebaseRepoCycleResult(RebaseRepoCycleResultMsg{
					FeatureID:    msg.FeatureID,
					RepoName:     conflict.RepoName,
					HasConflict:  true,
					RebaseTarget: conflict.RebaseTarget,
				})
			}
		}
		return m, tea.Batch(
			func() tea.Msg { return RefreshFeaturesMsg{} },
			m.listenForOrchestratorEvents(),
		)

	case PhaseCompletedMsg:
		return m.handlePhaseCompleted(msg)

	case ImplementLoopDoneMsg:
		return m.handleImplementLoopDone(msg)

	case tweakCommitDoneMsg:
		return m.handleTweakCommitDone(msg)

	case MultiRepoImplDoneMsg:
		return m.handleMultiRepoImplDone(msg)

	case PlanLoopDoneMsg:
		return m.handlePlanLoopDone(msg)

	case ArtifactReviewStartMsg:
		return m.handleArtifactReviewStart(msg)

	case artifactReviewSessionStartedMsg:
		// Validate that the started session matches the currently active
		// review model using per-attempt generation token. A stale message
		// from a previous start attempt (e.g. after detach/reattach) must
		// never bind to the current review model.
		if m.currentView == ViewArtifactReview && msg.sess != nil &&
			msg.generation == m.artifactReview.sessionGeneration &&
			!m.artifactReview.sessionStarted {
			// Generation matches current attempt — bind it.
			var cmd tea.Cmd
			m.artifactReview, cmd = m.artifactReview.handleSessionStarted(msg.sess)
			return m, cmd
		}
		// Stale generation, wrong view, or duplicate (already bound) —
		// stop the *arriving* session object to prevent an orphaned
		// process. Use the concrete session's Stop method when available
		// to avoid ambiguity from shared static IDs.
		if msg.sess != nil {
			_ = msg.sess.Stop()
		}
		return m, nil

	case artifactReviewMsgsMsg:
		if m.currentView == ViewArtifactReview {
			var cmd tea.Cmd
			m.artifactReview, cmd = m.artifactReview.handleSDKMessages(msg.messages)
			return m, cmd
		}
		return m, nil

	case artifactReviewDoneMsg:
		if m.currentView == ViewArtifactReview {
			m.artifactReview.agentResponding = false
			m.artifactReview.sessionStarting = false
			m.artifactReview.sessionStarted = false
			m.artifactReview.sess = nil
			m.artifactReview.pendingMessages = nil
		}
		return m, nil

	case artifactReviewStartErrorMsg:
		if m.currentView == ViewArtifactReview {
			var cmd tea.Cmd
			m.artifactReview, cmd = m.artifactReview.Update(msg)
			return m, cmd
		}
		return m, nil

	case artifactReviewSendErrorMsg:
		if m.currentView == ViewArtifactReview {
			var cmd tea.Cmd
			m.artifactReview, cmd = m.artifactReview.Update(msg)
			return m, cmd
		}
		return m, nil

	case PlanReviewDecisionMsg:
		return m.handlePlanReviewDecision(msg)

	case RoadmapReviewDecisionMsg:
		return m.handleRoadmapReviewDecision(msg)

	case RewindReviewDecisionMsg:
		return m.handleRewindReviewDecision(msg)

	case GateReviewDecisionMsg:
		return m.handleGateReviewDecision(msg)

	case NeedUserInputDecisionMsg:
		return m.handleNeedUserInputDecision(msg)

	case openNeedUserInputMsg:
		f, err := m.featureManager.Get(msg.FeatureID)
		if err != nil || f == nil {
			return m, nil
		}
		return m.attachNeedUserInput(f, msg.RepoName)

	case restartBusyMsg:
		m.statusMessage = "Feature is running — press [s] to stop first, then [r] to restart"
		m.statusTime = time.Now()
		return m, func() tea.Msg { return RefreshFeaturesMsg{} }

	case RebaseResultMsg:
		return m.handleRebaseResult(msg)

	case MergeLocalResultMsg:
		if msg.Err != nil {
			m.statusMessage = "\u2717 Merge failed: " + msg.Err.Error()
			m.statusTime = time.Now()
			return m, func() tea.Msg { return RefreshFeaturesMsg{} }
		}
		m.statusMessage = "\u2713 Merged to base branch — feature done"
		m.statusTime = time.Now()
		return m, func() tea.Msg { return RefreshFeaturesMsg{} }

	case RebaseRepoCycleResultMsg:
		return m.handleRebaseRepoCycleResult(msg)

	case RepoCycleLoopDoneMsg:
		return m.handleRepoCycleLoopDone(msg)

	case restartRepoCycleMsg:
		if msg.CycleType == feature.CycleTweak {
			// Interactive tweak sessions have no plan file — cannot restart autonomously.
			// Remove the stale cycle entry so the user can re-initiate from cycle selector.
			_ = m.orchestrator.RemoveRepoCycle(msg.FeatureID, msg.RepoName)
			return m, func() tea.Msg { return RefreshFeaturesMsg{} }
		}
		return m, m.startRepoCycleImplementCmd(msg.FeatureID, msg.RepoName, msg.CycleType, msg.PlanContent)

	case showRefactorForRepoMsg:
		m.refactorInputActive = true
		m.refactorFeatureID = msg.FeatureID
		m.cycleSelectRefactor = msg.RepoName
		if f, err := m.featureManager.Get(msg.FeatureID); err == nil {
			m.refactorFeatureName = f.Name
		}
		ta := textarea.New()
		ta.Placeholder = "Describe the refactoring for " + msg.RepoName + "..."
		if m.dashboard.getLayoutMode() == layoutNarrow {
			m = m.transitionTo(ViewDetail, msg.FeatureID)
			boxWidth := min(m.width-4, 76)
			ta.SetWidth(max(boxWidth-6, 20))
		} else {
			ta.SetWidth(max(m.rightPanelContentWidth()-6, 20))
		}
		ta.SetHeight(5)
		ta.Focus()
		m.refactorInput = ta
		return m, textarea.Blink

	case openCycleSelectorMsg:
		m.openCycleSelector(msg.FeatureID, msg.Action)
		return m, nil

	case restartRefactorCycleMsg:
		return m, m.restartRepoCycleRefactorCmd(msg.FeatureID, msg.RepoName, msg.Prompt)

	case ReviewCommentsFetchedMsg:
		if msg.Err != nil {
			m.logPhaseError(msg.FeatureID, "review-comments", msg.Err.Error())
			return m, func() tea.Msg { return RefreshFeaturesMsg{} }
		}
		m.reviewComments = NewReviewCommentsModel(msg.FeatureID, msg.FeatureSlug, msg.Comments, m.width, m.height)
		if m.currentView != ViewAttach && m.currentView != ViewArtifactReview {
			m.currentView = ViewReviewComments
		}
		return m, nil

	case ChatExitMsg:
		m.chatOpen = false
		m.dashboard.height = m.height
		return m, nil

	case publishExecuteResultMsg:
		// Handle pull-rebase conflict — enter resolution loop instead of showing error
		if msg.conflictDetected {
			featureID := msg.featureID
			if featureID == "" {
				featureID = m.publish.featureID
			}
			// Exit publish UI before entering rebase resolution so the
			// view isn't stuck in publishStepExecute (which blocks input).
			m.publish.step = publishStepDone
			if m.currentView != ViewAttach && m.currentView != ViewArtifactReview {
				m.currentView = ViewDashboard
			}
			return m.handleRebaseRepoCycleResult(RebaseRepoCycleResultMsg{
				FeatureID:    featureID,
				RepoName:     msg.repoName,
				HasConflict:  true,
				RebaseTarget: msg.rebaseTarget,
			})
		}
		// Handle non-conflict publish errors (pull-rebase, push, PR creation)
		if msg.err != nil {
			featureID := msg.featureID
			if featureID == "" {
				featureID = m.publish.featureID
			}
			if featureID != "" {
				m.logPhaseError(featureID, "publish", msg.err.Error())
				if msg.repoName != "" {
					// Multi-repo: record repo-scoped error instead of failing the whole feature
					_ = m.orchestrator.SetRepoPublishError(featureID, msg.repoName, msg.err.Error())
				} else {
					// Single-repo publish UI error: orchestrator method owns the
					// FeatureFailed emission so the TUI does not touch MarkFailed.
					_ = m.orchestrator.RecordPublishUIFailure(featureID, msg.err.Error())
				}
			}
		}
		if m.currentView == ViewPublish {
			m.publish, _ = m.publish.Update(msg)
		}
		if msg.prURL != "" {
			// Multi-repo: update per-repo state if repoName is set
			if msg.repoName != "" {
				_ = m.orchestrator.SetRepoPublished(m.publish.featureID, msg.repoName, msg.prURL)
				// Try to complete the feature-level publish
				_, _ = m.orchestrator.TryCompletePublish(m.publish.featureID)
			} else {
				_ = m.orchestrator.MarkPublished(m.publish.featureID, msg.prURL)
			}
		}
		return m, func() tea.Msg { return RefreshFeaturesMsg{} }

	case publishDescGeneratedMsg:
		if msg.err != nil && msg.featureID != "" {
			m.logPhaseError(msg.featureID, "publish", "description generation: "+msg.err.Error())
		}
		if m.currentView == ViewPublish {
			var cmd tea.Cmd
			m.publish, cmd = m.publish.Update(msg)
			return m, cmd
		}
		return m, nil

	case LogsContentMsg:
		m.logs = NewLogsModel(msg.Title, msg.Content, m.width, m.height)
		m.logs.featureID = msg.FeatureID
		if msg.FeatureID != "" {
			if lf, err := m.featureManager.Get(msg.FeatureID); err == nil {
				m.logs.autoPublish = lf.Checkpoints.AutoPublish()
				m.logs.isPublishable = lf.IsPublishable()
			}
		}
		if m.currentView != ViewAttach && m.currentView != ViewArtifactReview {
			m.currentView = ViewLogs
		}
		return m, nil

	case FeatureCreatedMsg:
		m.wizardCreating = false
		m.wizardCreatingName = ""
		m.dashboard.creatingName = ""
		if msg.Err != nil {
			log.Printf("feature creation failed: %v", msg.Err)
			m.statusMessage = "✗ Feature creation failed: " + firstLine(msg.Err.Error())
			m.statusTime = time.Now()
			return m, func() tea.Msg { return RefreshFeaturesMsg{} }
		}

		// Load imported repo permissions into memory BEFORE starting any sessions.
		// The OnFeatureCreated callback already persisted rules to disk during
		// Manager.Create(). We now load them into the in-memory cache so the
		// first post-creation session (KB, research, etc.) has immediate access.
		if m.permissionCache != nil {
			for _, fid := range msg.FeatureIDs {
				if f, err := m.featureManager.Get(fid); err == nil {
					for _, repo := range f.Repos {
						m.permissionCache.LoadAndMerge(repo.Name)
					}
				}
			}
		}

		var cmds []tea.Cmd
		cmds = append(cmds, func() tea.Msg { return RefreshFeaturesMsg{} })
		for _, fid := range msg.FeatureIDs {
			cmds = append(cmds, m.startFirstPhaseCmd(fid))
		}
		for _, fid := range msg.FeatureIDs {
			cmds = append(cmds, m.generateSummaryCmd(fid))
		}
		return m, tea.Batch(cmds...)

	case featureSummaryMsg:
		if msg.summary != "" {
			_ = m.orchestrator.SaveFeatureSummary(msg.featureID, msg.summary)
			if f, err := m.featureManager.Get(msg.featureID); err == nil && f != nil {
				features, _ := m.featureManager.List()
				m.dashboard.SetFeatures(features)
				if m.currentView == ViewDetail && m.detail.FeatureID() == msg.featureID {
					m.detail = NewDetailModel(f, m.featureManager.Store.BaseDir)
					m.detail.width = m.width
					m.detail.height = m.height
				}
			}
		}
		return m, nil

	case DeleteFeatureDoneMsg:
		if msg.Err != nil {
			log.Printf("delete feature failed: %v", msg.Err)
			m.statusMessage = "✗ Delete failed: " + firstLine(msg.Err.Error())
			m.statusTime = time.Now()
		}
		features, _ := m.featureManager.List()
		m.dashboard.SetFeatures(features)
		if m.currentView == ViewDetail {
			m.currentView = ViewDashboard
		}
		return m, nil

	case RewindDoneMsg:
		m.rewindingFeatureID = ""
		if msg.Err != nil {
			log.Printf("rewind failed: %v", msg.Err)
			m.statusMessage = "✗ Rewind failed: " + firstLine(msg.Err.Error())
			m.statusTime = time.Now()
			return m, func() tea.Msg { return RefreshFeaturesMsg{} }
		}
		// Surface any non-fatal warnings from the rewind
		for _, w := range msg.Warnings {
			log.Printf("rewind warning: %s", w)
		}
		if len(msg.Warnings) > 0 {
			m.statusMessage = "⚠ Rewind completed with warnings: " + strings.Join(msg.Warnings, "; ")
			m.statusTime = time.Now()
		}
		// KB escalation: skip artifact review, dispatch via orchestrator which
		// routes to startKB based on the feature's CurrentPhase after rewind.
		if msg.TargetPhase == feature.PhaseKnowledgeBase {
			return m, tea.Batch(
				m.startFeatureCmd(msg.FeatureID),
				func() tea.Msg { return RefreshFeaturesMsg{} },
			)
		}
		// Normal rewind: start artifact review
		return m, tea.Batch(
			m.startRewindReviewSessionCmd(msg.FeatureID, msg.TargetPhase, true), // auto-triggered: badge only
			func() tea.Msg { return RefreshFeaturesMsg{} },
		)

	case RestartFromBeginningDoneMsg:
		if msg.Err != nil {
			log.Printf("restart from beginning failed: %v", msg.Err)
			m.statusMessage = "✗ Restart failed: " + firstLine(msg.Err.Error())
			m.statusTime = time.Now()
		}
		features, _ := m.featureManager.List()
		m.dashboard.SetFeatures(features)
		if m.currentView == ViewDetail {
			f, _ := m.featureManager.Get(msg.FeatureID)
			if f != nil {
				m.detail = NewDetailModel(f, m.featureManager.Store.BaseDir)
				m.detail.width = m.width
				m.detail.height = m.height
			}
		}
		if msg.Err != nil {
			return m, func() tea.Msg { return RefreshFeaturesMsg{} }
		}
		var cmds []tea.Cmd
		cmds = append(cmds, func() tea.Msg { return RefreshFeaturesMsg{} })
		cmds = append(cmds, m.startFeatureCmd(msg.FeatureID))
		return m, tea.Batch(cmds...)

	case HelpOverlayCloseMsg:
		m.helpOverlayActive = false
		return m, nil

	case tea.KeyPressMsg:
		// Help overlay intercept (highest priority — modal consumes all input)
		if m.helpOverlayActive {
			switch {
			case key.Matches(msg, keys.Back), key.Matches(msg, keys.HelpOverlay):
				m.helpOverlayActive = false
				return m, nil
			default:
				var cmd tea.Cmd
				m.helpOverlay, cmd = m.helpOverlay.Update(msg)
				return m, cmd
			}
		}
		// Quit confirmation intercept (highest priority)
		if m.quitConfirmActive {
			switch msg.String() {
			case "y", "Y":
				m.quitConfirmActive = false
				return m, tea.Quit
			default:
				m.quitConfirmActive = false
				return m, nil
			}
		}
		// Manual publish confirmation intercept
		if m.manualPublishConfirmActive {
			switch msg.String() {
			case "y", "Y":
				m.manualPublishConfirmActive = false
				return m, m.manualPublishCmd(m.manualPublishFeatureID)
			default:
				m.manualPublishConfirmActive = false
				return m, nil
			}
		}
		// Tweak Final Review modal intercept. The modal is feature-level:
		// "y" runs Final Review across every Feature.Repos cumulative diff,
		// "n" skips review and pushes, and "Esc" abandons. The repoName modal
		// field is preserved on the model for compatibility but ignored by
		// every dispatch.
		if m.tweakReviewModalActive {
			fid := m.tweakReviewModalFeatureID
			switch msg.String() {
			case "y":
				m.tweakReviewModalActive = false
				m.tweakReviewModalFeatureID = ""
				m.tweakReviewModalRepoName = ""
				m.tweakCompletingFeatureID = ""
				return m, m.startCycleFinalReviewCmd(fid)
			case "n":
				m.tweakReviewModalActive = false
				m.tweakReviewModalFeatureID = ""
				m.tweakReviewModalRepoName = ""
				m.tweakCompletingFeatureID = ""
				return m, m.completeTweakFinishCmd(fid, "", true)
			case "esc":
				m.tweakReviewModalActive = false
				m.tweakReviewModalFeatureID = ""
				m.tweakReviewModalRepoName = ""
				m.tweakCompletingFeatureID = ""
				return m, m.restoreTweakFromReviewCmd(fid, "")
			}
			// Consume all other keys while modal is active
			return m, nil
		}
		// Edit config overlay intercept (feature-config edit).
		if m.editConfigActive {
			if m.editConfig.discardConfirm {
				switch msg.String() {
				case "y", "Y":
					m.editConfigActive = false
					m.editConfig = EditConfigModel{}
					return m, nil
				default:
					// Any other key cancels the confirm and returns to editing.
					m.editConfig.discardConfirm = false
					return m, nil
				}
			}
			switch msg.String() {
			case "esc":
				if m.editConfig.editor.HasChanges() {
					m.editConfig.discardConfirm = true
					return m, nil
				}
				m.editConfigActive = false
				m.editConfig = EditConfigModel{}
				return m, nil
			case "enter":
				if m.editConfig.saving {
					return m, nil
				}
				snap := m.editConfig.editor.Snapshot()
				input := orchestrator.UpdateFeatureConfigInput{
					Models:      snap.Models,
					Inquireness: snap.Inquireness,
					Checkpoints: snap.Checkpoints,
				}
				m.editConfig.saving = true
				m.editConfig.saveErr = ""
				return m, m.saveConfigCmd(m.editConfig.featureID, input, m.editConfig.repos, m.editConfig.pipeline, m.editConfig.publishable)
			default:
				var cmd tea.Cmd
				m.editConfig, cmd = m.editConfig.Update(msg)
				return m, cmd
			}
		}
		// Delete confirmation intercept (takes priority over view-specific handling)
		if m.deleteConfirmActive {
			switch msg.String() {
			case "y", "Y":
				m.deleteConfirmActive = false
				return m, m.deleteFeatureCmd(m.deleteFeatureID)
			default:
				m.deleteConfirmActive = false
				return m, nil
			}
		}
		// Roadmap phase picker intercept
		if m.rewindPhasePickerActive {
			switch msg.String() {
			case "up", "k":
				if len(m.rewindPhasePickerRows) == 0 {
					m.rewindPhasePickerActive = false
					return m, nil
				}
				if m.rewindPhasePickerCursor > 0 {
					m.rewindPhasePickerCursor--
				} else {
					m.rewindPhasePickerCursor = len(m.rewindPhasePickerRows) - 1
				}
				return m, nil
			case "down", "j":
				if len(m.rewindPhasePickerRows) == 0 {
					m.rewindPhasePickerActive = false
					return m, nil
				}
				if m.rewindPhasePickerCursor < len(m.rewindPhasePickerRows)-1 {
					m.rewindPhasePickerCursor++
				} else {
					m.rewindPhasePickerCursor = 0
				}
				return m, nil
			case "enter":
				if len(m.rewindPhasePickerRows) == 0 {
					m.rewindPhasePickerActive = false
					return m, nil
				}
				row := m.rewindPhasePickerRows[m.rewindPhasePickerCursor]
				m.rewindPhasePickerActive = false
				if row.AnchorAvailable {
					m.openPartialRewindConfirmation(m.rewindPhasePickerFeatureID, row)
				} else {
					m.rewindPartialUnavailableActive = true
					m.rewindPartialUnavailableRow = row
					m.rewindPartialUnavailableReason = row.UnavailableReason
				}
				return m, nil
			case "esc":
				m.rewindPhasePickerActive = false
				m.rewindMenuActive = true
				return m, nil
			default:
				return m, nil
			}
		}
		// Partial roadmap rewind unavailable prompt intercept
		if m.rewindPartialUnavailableActive {
			switch msg.String() {
			case "f", "F":
				m.rewindPartialUnavailableActive = false
				m.openFullRewindConfirmation(m.rewindPhasePickerFeatureID, feature.PhaseImplement, "Implement", false, "")
				return m, nil
			case "esc", "c", "C":
				m.rewindPartialUnavailableActive = false
				m.rewindPhasePickerActive = true
				return m, nil
			default:
				return m, nil
			}
		}
		// Rewind menu intercept
		if m.rewindMenuActive {
			totalItems := len(m.rewindMenuChoices) + len(m.rewindMenuUpgradeOptions)
			switch msg.String() {
			case "up", "k":
				if m.rewindMenuCursor > 0 {
					m.rewindMenuCursor--
				} else {
					m.rewindMenuCursor = totalItems - 1
				}
				return m, nil
			case "down", "j":
				if m.rewindMenuCursor < totalItems-1 {
					m.rewindMenuCursor++
				} else {
					m.rewindMenuCursor = 0
				}
				return m, nil
			case "enter":
				m.rewindMenuActive = false
				if m.rewindMenuCursor < len(m.rewindMenuChoices) {
					// Rewind choice selected
					selected := m.rewindMenuChoices[m.rewindMenuCursor]
					if selected.Phase == feature.PhaseImplement && m.openRoadmapPhasePicker(m.rewindMenuFeatureID) {
						return m, nil
					}
					phaseName := selected.Phase.String()
					if selected.Phase == feature.PhaseInquire {
						phaseName = "Start (Inquiry)"
					}
					m.openFullRewindConfirmation(m.rewindMenuFeatureID, selected.Phase, phaseName, selected.OverridePhase == feature.PhaseKnowledgeBase, "")
				} else {
					// Pipeline upgrade selected — confirm then upgrade + rewind to KB Build
					upgradeIdx := m.rewindMenuCursor - len(m.rewindMenuChoices)
					newProfile := m.rewindMenuUpgradeOptions[upgradeIdx]
					// Use PhaseInquire as target — after UpgradePipeline sets PipelineUpgradedFrom,
					// RewindToPhase will redirect to PhaseKnowledgeBase automatically.
					m.openFullRewindConfirmation(m.rewindMenuFeatureID, feature.PhaseInquire, "KB Build", true, newProfile)
				}
				return m, nil
			case "esc":
				m.rewindMenuActive = false
				return m, nil
			default:
				return m, nil
			}
		}
		// Rewind confirmation intercept
		if m.rewindConfirmActive {
			switch msg.String() {
			case "y", "Y":
				m.rewindConfirmActive = false
				m.rewindingFeatureID = m.rewindConfirmFeatureID
				// If this is a pipeline upgrade, apply it before rewinding to KB
				if m.rewindConfirmUpgrade != "" {
					if err := m.orchestrator.UpgradePipeline(m.rewindConfirmFeatureID, m.rewindConfirmUpgrade); err != nil {
						m.statusMessage = "Pipeline upgrade failed: " + err.Error()
						m.statusTime = time.Now()
						m.rewindConfirmUpgrade = ""
						return m, func() tea.Msg { return RefreshFeaturesMsg{} }
					}
					m.rewindConfirmUpgrade = ""
				}
				return m, m.rewindCmd(m.rewindConfirmFeatureID, m.rewindConfirmPhase, m.rewindConfirmRoadmapPhase)
			default:
				m.rewindConfirmActive = false
				m.rewindConfirmUpgrade = ""
				m.rewindConfirmRoadmapPhase = 0
				m.rewindConfirmRoadmapRow = roadmapRewindRow{}
				return m, nil
			}
		}
		// Resume all confirmation intercept
		if m.resumeAllConfirmActive {
			switch msg.String() {
			case "y", "Y":
				m.resumeAllConfirmActive = false
				return m, m.resumeAllCmd()
			default:
				m.resumeAllConfirmActive = false
				return m, nil
			}
		}
		// Stop confirmation intercept
		if m.stopConfirmActive {
			switch msg.String() {
			case "y", "Y":
				m.stopConfirmActive = false
				return m, m.stopFeatureCmd(m.stopConfirmFeatureID)
			default:
				m.stopConfirmActive = false
				return m, nil
			}
		}
		// Cycle repo selector intercept
		if m.cycleSelectActive {
			switch msg.String() {
			case "up", "k":
				if m.cycleSelectCursor > 0 {
					m.cycleSelectCursor--
				}
				return m, nil
			case "down", "j":
				if m.cycleSelectCursor < len(m.cycleSelectRepos)-1 {
					m.cycleSelectCursor++
				}
				return m, nil
			case "enter":
				selected := m.cycleSelectRepos[m.cycleSelectCursor]
				if strings.HasPrefix(selected.CycleStatus, "running:") {
					m.statusMessage = selected.Name + " already has an active cycle"
					m.statusTime = time.Now()
					m.cycleSelectActive = false
					return m, nil
				}
				m.cycleSelectActive = false
				return m, m.dispatchRepoCycleCmd(m.cycleSelectFeatureID, selected.Name, m.cycleSelectAction)
			case "esc":
				m.cycleSelectActive = false
				return m, nil
			}
			return m, nil
		}
		// Workspace manager overlay intercept
		if m.workspaceManagerActive {
			return m.updateWorkspaceManager(msg)
		}
		// Global quit — skip when a text input is active so that "q" can
		// be typed normally in the wizard and help-answer prompts.
		if !m.hasActiveTextInput() && key.Matches(msg, keys.Quit) {
			if m.currentView == ViewDashboard {
				m.quitConfirmActive = true
				return m, nil
			}
			return m.transitionTo(ViewDashboard, ""), nil
		}

		// Global help overlay — skip when a text input is active so that "?"
		// can be typed normally in the wizard, chat, and help-answer prompts.
		if !m.hasActiveTextInput() && key.Matches(msg, keys.HelpOverlay) {
			return m.transitionToHelpOverlay()
		}

		switch m.currentView {
		case ViewDashboard:
			return m.updateDashboard(msg)
		case ViewDetail:
			return m.updateDetail(msg)
		case ViewWizard:
			return m.updateWizard(msg)
		case ViewPublish:
			return m.updatePublish(msg)
		case ViewRecovery:
			return m.updateRecovery(msg)
		case ViewLogs:
			return m.updateLogs(msg)
		case ViewReviewComments:
			return m.updateReviewComments(msg)
		case ViewAttach:
			return m.updateAttach(msg)
		case ViewArtifactReview:
			return m.updateArtifactReview(msg)
		case ViewChat:
			return m.updateChat(msg)
		case ViewWelcome:
			return m.updateWelcome(msg)
		}

	default:
		// Route non-key messages to workspace manager when active so that
		// the nested DirPickerModel receives its init/readDir/scan results.
		if m.workspaceManagerActive {
			return m.updateWorkspaceManager(msg)
		}
		return m.forwardToActiveInput(msg)
	}

	return m, nil
}

// contextPctForFeature returns the context window usage percentage for the
// given feature while its normal lifecycle or a post-publish cycle is running.
// When multiple sessions are active in parallel (e.g., several plan-validation
// gates running concurrently), returns the MAX across them so the displayed
// bar reflects the session closest to its limit — the one most likely to trip
// the handoff threshold first. Returns -1 if no active session has reported
// usage yet — callers render "Calculating…" rather than bleeding through a
// prior phase's last reading.
func (m AppModel) contextPctForFeature(f *feature.Feature) int {
	if f == nil || m.sessionManager == nil {
		return -1
	}
	if !f.Status.IsRunning() && !featureHasRunningCycle(f) {
		return -1
	}
	maxActivePct := -1
	for _, s := range m.sessionManager.FeatureSessions(f.ID) {
		if !isGenericFeatureSession(s) || !s.IsActive() {
			continue
		}
		if pct := s.ContextPercentage(); pct > maxActivePct {
			maxActivePct = pct
		}
	}
	return maxActivePct
}

func featureHasRunningCycle(f *feature.Feature) bool {
	if f.ActiveCycle != nil && f.ActiveCycle.Status == feature.RepoCycleRunning {
		return true
	}
	for _, rc := range f.RepoCycles {
		if rc != nil && rc.Status == feature.RepoCycleRunning {
			return true
		}
	}
	return false
}

func (m *AppModel) livePreviewSessionForFeature(f *feature.Feature) session.SessionView {
	if f == nil || m.sessionManager == nil {
		return nil
	}
	tabs := m.buildRepoTabs(f)
	if idx := resolveInitialAttentionTab(tabs, f.LastAttachedRepo); idx >= 0 && tabs[idx].sess != nil {
		return tabs[idx].sess
	}
	for _, tab := range tabs {
		if tab.sess != nil {
			return tab.sess
		}
	}
	for _, s := range m.sessionManager.FeatureSessions(f.ID) {
		if isGenericFeatureSession(s) && s.IsActive() {
			return s
		}
	}
	return nil
}

func isGenericFeatureSession(s session.SessionView) bool {
	if s == nil {
		return false
	}
	return !isChatSession(s.ID()) && !isArtifactReviewSession(s.ID())
}

func resolveInitialAttentionTab(tabs []repoTab, lastAttachedRepo string) int {
	if idx := firstTabWithPendingPermission(tabs); idx >= 0 {
		return idx
	}
	if idx := firstTabWithPendingAskUser(tabs); idx >= 0 {
		return idx
	}
	return resolveInitialTab(tabs, lastAttachedRepo)
}

func firstTabWithPendingPermission(tabs []repoTab) int {
	for i, tab := range tabs {
		if tab.sess != nil && firstPendingPermissionControlRequest(tab.sess) != nil {
			return i
		}
	}
	for i, tab := range tabs {
		if tab.sess != nil && tab.sess.Status() == session.SessionWaitingPermission {
			return i
		}
	}
	return -1
}

func firstTabWithPendingAskUser(tabs []repoTab) int {
	for i, tab := range tabs {
		if tab.sess != nil && firstPendingAskUserControlRequest(tab.sess) != nil {
			return i
		}
	}
	for i, tab := range tabs {
		if tab.sess != nil && (tab.sess.Status() == session.SessionWaitingHelp || tab.sess.HasPendingAskUserQuestion()) {
			return i
		}
	}
	return -1
}

// activeSessionContextPct returns the context window usage percentage for the
// active session of the currently viewed feature. Returns -1 if unavailable.
func (m AppModel) activeSessionContextPct() int {
	return m.contextPctForFeature(m.detail.feature)
}

// kbStaleWarningFor returns the cached KB stale warning for the given feature.
// Returns "" if the KB is fresh, the feature has no repos, or the KB phase is done.
func (m AppModel) kbStaleWarningFor(f *feature.Feature) string {
	if f == nil || len(f.Repos) == 0 {
		return ""
	}
	// Only show warning when KB phase hasn't completed yet
	if f.CurrentPhase.LogicalOrder() > feature.PhaseKnowledgeBase.LogicalOrder() {
		return ""
	}
	return m.kbStaleWarnings[f.ID]
}

// refreshKBStaleWarnings updates the KB stale warning cache for all features.
// Called from RefreshFeaturesMsg handler (every tick interval).
func (m *AppModel) refreshKBStaleWarnings(features []*feature.Feature) {
	for _, f := range features {
		if len(f.Repos) == 0 {
			continue
		}
		// Skip features past KB phase
		if f.CurrentPhase.LogicalOrder() > feature.PhaseKnowledgeBase.LogicalOrder() {
			delete(m.kbStaleWarnings, f.ID)
			continue
		}
		// Only compute if not already cached
		if _, ok := m.kbStaleWarnings[f.ID]; ok {
			continue
		}
		// Check all repos and use the worst-case (most stale) warning
		worstBehind := 0
		for _, repo := range f.Repos {
			kbDir := agent.KBStateDir(m.featureManager.Store.BaseDir, repo.Name)
			behind := agent.CountKBCommitsBehind(context.Background(), m.phaseRunner.CommandRunner, kbDir, repo.Path)
			if behind == -1 {
				worstBehind = -1
				break
			}
			if behind > worstBehind {
				worstBehind = behind
			}
		}
		var warning string
		switch {
		case worstBehind == -1:
			warning = "KB not built yet \u2014 may take up to 30min"
		case worstBehind > 100:
			warning = fmt.Sprintf("KB is %d commits behind \u2014 may take up to 30min", worstBehind)
		}
		m.kbStaleWarnings[f.ID] = warning
	}
}

func (m AppModel) View() tea.View {
	// Pass the app-level spinner view to detail models
	sv := m.spinner.View()
	m.dashboard.spinnerView = sv
	m.dashboard.preview.spinnerView = sv
	m.dashboard.livePreview.spinnerView = sv
	m.detail.spinnerView = sv
	m.detail.contextPct = m.activeSessionContextPct()
	m.detail.kbStaleWarning = m.kbStaleWarningFor(m.detail.feature)
	if sel := m.dashboard.SelectedFeature(); sel != nil {
		contextPct := m.contextPctForFeature(sel)
		m.dashboard.preview.contextPct = contextPct
		m.dashboard.preview.kbStaleWarning = m.kbStaleWarningFor(sel)
		m.dashboard.livePreview.contextPct = contextPct
		m.dashboard.livePreview.session = m.livePreviewSessionForFeature(sel)
	} else {
		m.dashboard.livePreview.contextPct = -1
		m.dashboard.livePreview.session = nil
	}
	m.publish.spinnerView = sv

	// Sync transient status message to dashboard
	m.dashboard.statusMessage = m.statusMessage
	m.dashboard.rewindingFeatureID = m.rewindingFeatureID

	var view string
	switch m.currentView {
	case ViewDashboard:
		// Sync refactor overlay state to the dashboard preview's detail model
		if m.refactorInputActive {
			contentW := m.rightPanelContentWidth()
			m.refactorInput.SetWidth(max(contentW-6, 20))
			m.dashboard.preview.refactorActive = true
			m.dashboard.preview.refactorInputView = m.refactorInput.View()
			m.dashboard.preview.refactorFeatureName = m.refactorFeatureName
		} else {
			m.dashboard.preview.refactorActive = false
		}

		// Sync refactor pipeline selector state to dashboard preview
		if m.refactorPipelineActive {
			m.dashboard.preview.refactorPipelineActive = true
			m.dashboard.preview.refactorPipelineView = m.renderRefactorPipelineSelector()
		} else {
			m.dashboard.preview.refactorPipelineActive = false
		}

		if m.chatOpen {
			chatH := chatPanelHeight(m.height)
			m.dashboard.height = m.height - chatH
			view = m.dashboard.View()
			view += m.chat.View()
		} else if m.chatReady && m.chat.responding {
			m.dashboard.height = m.height - 1
			view = m.dashboard.View()
			view += lipgloss.NewStyle().Foreground(colorBrand).Render("  ● Chat thinking... press / to view")
		} else {
			m.dashboard.height = m.height
			view = m.dashboard.View()
		}
		if m.helpInputActive {
			view += m.helpInputView()
		}
	case ViewDetail:
		// Sync refactor overlay state to the full-screen detail model
		if m.refactorInputActive {
			boxWidth := min(m.width-4, 76)
			m.refactorInput.SetWidth(max(boxWidth-6, 20))
			m.detail.refactorActive = true
			m.detail.refactorInputView = m.refactorInput.View()
			m.detail.refactorFeatureName = m.refactorFeatureName
		} else {
			m.detail.refactorActive = false
		}

		// Sync refactor pipeline selector state to detail model
		if m.refactorPipelineActive {
			m.detail.refactorPipelineActive = true
			m.detail.refactorPipelineView = m.renderRefactorPipelineSelector()
		} else {
			m.detail.refactorPipelineActive = false
		}

		view = m.detail.View()
		if m.helpInputActive {
			view += m.helpInputView()
		}
	case ViewWizard:
		bg := m.dashboard.View()
		fg := m.wizard.ViewModal()
		view = overlayModal(bg, fg, m.width, m.height)
		if m.wizard.IsPickerActive() {
			view = overlayModal(view, m.wizard.PickerView(), m.width, m.height)
		}
		if m.wizard.IsRootPickerActive() {
			view = overlayModal(view, m.wizard.RootPickerView(), m.width, m.height)
		}
	case ViewPublish:
		view = m.publish.View()
	case ViewRecovery:
		view = m.recovery.View()
	case ViewLogs:
		view = m.logs.View()
	case ViewReviewComments:
		view = m.reviewComments.View()
	case ViewAttach:
		view = m.attach.View()
	case ViewArtifactReview:
		view = m.artifactReview.View()
	case ViewChat:
		view = m.chat.View()
	case ViewWelcome:
		view = m.welcome.View()
	default:
		view = m.dashboard.View()
	}

	if m.quitConfirmActive {
		bg := view
		fg := m.quitConfirmModal()
		view = overlayModal(bg, fg, m.width, m.height)
	}

	if m.deleteConfirmActive {
		bg := view
		fg := m.deleteConfirmModal()
		view = overlayModal(bg, fg, m.width, m.height)
	}

	if m.resumeAllConfirmActive {
		bg := view
		fg := m.resumeAllConfirmModal()
		view = overlayModal(bg, fg, m.width, m.height)
	}

	if m.manualPublishConfirmActive {
		bg := view
		fg := m.manualPublishConfirmModal()
		view = overlayModal(bg, fg, m.width, m.height)
	}

	if m.rewindMenuActive {
		bg := view
		fg := m.rewindMenuModal()
		view = overlayModal(bg, fg, m.width, m.height)
	}

	if m.rewindPhasePickerActive {
		bg := view
		fg := m.rewindPhasePickerModal()
		view = overlayModal(bg, fg, m.width, m.height)
	}

	if m.rewindPartialUnavailableActive {
		bg := view
		fg := m.rewindPartialUnavailableModal()
		view = overlayModal(bg, fg, m.width, m.height)
	}

	if m.tweakReviewModalActive {
		bg := view
		fg := m.renderTweakReviewModal()
		view = overlayModal(bg, fg, m.width, m.height)
	}

	if m.editConfigActive {
		bg := view
		fg := m.editConfig.View()
		view = overlayModal(bg, fg, m.width, m.height)
	}

	if m.rewindConfirmActive {
		bg := view
		fg := m.rewindConfirmModal()
		view = overlayModal(bg, fg, m.width, m.height)
	}

	if m.stopConfirmActive {
		bg := view
		fg := m.stopConfirmModal()
		view = overlayModal(bg, fg, m.width, m.height)
	}

	if m.cycleSelectActive {
		bg := view
		fg := m.cycleSelectModal()
		view = overlayModal(bg, fg, m.width, m.height)
	}

	if m.workspaceManagerActive {
		bg := view
		fg := m.workspaceManager.View()
		view = overlayModal(bg, fg, m.width, m.height)

		// Stacked picker overlay
		if m.workspaceManager.IsPickerActive() {
			bg = view
			fg = m.workspaceManager.PickerView()
			view = overlayModal(bg, fg, m.width, m.height)
		}
	}

	if m.helpOverlayActive {
		bg := view
		fg := m.helpOverlay.View()
		view = overlayModal(bg, fg, m.width, m.height)
	}

	v := tea.NewView(view)
	v.AltScreen = true
	// Enable mouse capture only while an AskUserQuestion dialog is active, so
	// trackpad scrolling reaches the chat viewport instead of being translated
	// by the terminal into arrow keys (which would walk the option list).
	// Outside of questions, leaving mouse mode off preserves native text
	// selection in the terminal.
	if m.currentView == ViewAttach && m.attach.HasActiveQuestion() {
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
}

// listenForEvents returns a tea.Cmd that waits for the next event from the event channel.
func (m AppModel) listenForEvents() tea.Cmd {
	return func() tea.Msg {
		event, ok := <-m.eventCh
		if !ok {
			return nil
		}
		switch e := event.(type) {
		case session.SDKEventMsg:
			return SDKSessionEventMsg{Event: e}
		case session.SessionDoneMsg:
			return SessionDoneTUIMsg{Done: e}
		}
		return nil
	}
}

// listenForOrchestratorEvents returns a tea.Cmd that awaits the next event
// from Orchestrator.Events() and translates it into the corresponding typed
// TUI message. The Cmd re-arms itself via the Update loop (which appends a
// fresh listenForOrchestratorEvents command after handling each OrchXxxMsg).
// Selecting on Done() ensures the listener exits cleanly on shutdown even if
// the orchestrator never closes eventCh.
func (m AppModel) listenForOrchestratorEvents() tea.Cmd {
	if m.orchestrator == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case ev, ok := <-m.orchestrator.Events():
			if !ok {
				return nil
			}
			return orchEventToMsg(ev)
		case <-m.orchestrator.Done():
			return nil
		}
	}
}

// orchEventToMsg converts a ports.Event into the typed TUI message that the
// Update loop recognizes. Returns nil for event types the TUI does not bridge
// (currently ports.SessionOutput — that stream flows through eventCh).
func orchEventToMsg(ev ports.Event) tea.Msg {
	switch ev.Type {
	case ports.FeatureCreated:
		return OrchFeatureCreatedMsg{FeatureID: ev.FeatureID, Feature: ev.Feature}
	case ports.FeatureStarted:
		return OrchFeatureStartedMsg{FeatureID: ev.FeatureID}
	case ports.FeatureAdvanced:
		return OrchFeatureAdvancedMsg{FeatureID: ev.FeatureID, Phase: ev.Phase}
	case ports.FeatureCompleted:
		return OrchFeatureCompletedMsg{FeatureID: ev.FeatureID}
	case ports.FeatureFailed:
		return OrchFeatureFailedMsg{FeatureID: ev.FeatureID, Message: ev.Message, Error: ev.Error}
	case ports.FeatureInterrupted:
		return OrchFeatureInterruptedMsg{FeatureID: ev.FeatureID}
	case ports.PhaseStarted:
		return OrchPhaseStartedMsg{FeatureID: ev.FeatureID, Phase: ev.Phase}
	case ports.PhaseCompleted:
		return OrchPhaseCompletedMsg{FeatureID: ev.FeatureID, Phase: ev.Phase, Error: ev.Error}
	case ports.ReviewRequired:
		return OrchReviewRequiredMsg{FeatureID: ev.FeatureID, Phase: ev.Phase}
	case ports.PublishStarted:
		return OrchPublishStartedMsg{FeatureID: ev.FeatureID}
	case ports.PublishCompleted:
		return OrchPublishCompletedMsg{FeatureID: ev.FeatureID, Error: ev.Error}
	case ports.RepoStatusChanged:
		return OrchRepoStatusChangedMsg{FeatureID: ev.FeatureID, RepoName: ev.Message}
	case ports.RecoveryScanned:
		return OrchRecoveryScannedMsg{Message: ev.Message}
	case ports.RecoveryExecuted:
		return OrchRecoveryExecutedMsg{FeatureID: ev.FeatureID, Message: ev.Message}
	case ports.TweakReviewApproved:
		return OrchTweakReviewApprovedMsg{FeatureID: ev.FeatureID, RepoName: ev.Message}
	case ports.FeatureConfigChanged:
		return OrchFeatureConfigChangedMsg{FeatureID: ev.FeatureID}
	}
	return nil
}

// handleSDKEvent processes structured SDK messages from sessions.
func (m AppModel) handleSDKEvent(msg SDKSessionEventMsg) (tea.Model, tea.Cmd) {
	evt := msg.Event
	sdkMsg := evt.Message

	// Skip feature-based handling for chat sessions and artifact-review sessions
	if isChatSession(evt.SessionID) || isArtifactReviewSession(evt.SessionID) {
		return m, m.listenForEvents()
	}
	if staleSessionEvent(m.sessionManager.GetSession(evt.SessionID), evt.StartedAt) {
		return m, m.listenForEvents()
	}

	var notifyCmd tea.Cmd

	switch {
	case sdkMsg.Assistant != nil:
		fid := eventFID(evt.SessionID, evt.FeatureID)

		// For tweak sessions, an assistant message means the agent is actively
		// working (processing the user's message). Clear the "waiting input"
		// badge so the dashboard shows the feature as running, not idle.
		if isTweakSessionID(evt.SessionID) {
			_ = m.featureManager.Store.Modify(fid, func(f *feature.Feature) error {
				clearPendingHelpByMessage(f, waitingInputHelpMessage)
				clearPendingHelpByMessage(f, questionHelpMessage)
				return nil
			})
		}

		// AskUserQuestion tool_use blocks are handled directly in the attach
		// view (multi-choice UI). We only send a notification here so the
		// user knows to attach — the questions themselves are NOT added to
		// the dashboard HelpQueue to avoid clutter.
		for _, block := range sdkMsg.Assistant.Message.Content {
			if block.IsToolUse() && block.Name == "AskUserQuestion" {
				var slug string
				_ = m.featureManager.Store.Modify(fid, func(f *feature.Feature) error {
					slug = f.Slug
					return nil
				})
				if slug != "" {
					notifyCmd = m.maybeNotifyUser(fid, notifyWaitingInput, slug, "agent has a question")
				}
				break
			}
		}

	case sdkMsg.ControlRequest != nil:
		// Tool permission request (Bash, Edit, etc.) or AskUserQuestion.
		// AskUserQuestion also arrives as a control_request; we add it to
		// HelpQueue but NOT PermissionsQueue (it is not a tool permission).
		fid := eventFID(evt.SessionID, evt.FeatureID)
		cr := sdkMsg.ControlRequest
		var slug string
		_ = m.featureManager.Store.Modify(fid, func(f *feature.Feature) error {
			if cr.Request.ToolName == "AskUserQuestion" {
				if ensurePendingQuestionHelp(f) {
					slug = f.Slug
				}
			} else if ensurePendingWaitingInputHelp(f) {
				slug = f.Slug
			}
			// Populate PermissionsQueue only for real tool permissions (not AskUserQuestion).
			if cr.Request.ToolName != "AskUserQuestion" {
				f.PermissionsQueue = append(f.PermissionsQueue, feature.PermissionRequest{
					Tool:    cr.Request.ToolName,
					Args:    string(cr.Request.Input),
					Time:    time.Now(),
					Pending: true,
				})
			}
			return nil
		})
		if slug != "" {
			notifyCmd = m.maybeNotifyUser(fid, notifyWaitingInput, slug, "agent waiting for input")
		}

	case sdkMsg.Result != nil:
		if sdkMsg.Result.Subtype == "error" {
			// API error — include actual error details
			errorMsg := fmt.Sprintf("%s %s%s", apiErrorHelpPrefix, sdkMsg.Result.Result, apiErrorHelpSuffix)
			fid := eventFID(evt.SessionID, evt.FeatureID)
			var slug string
			_ = m.featureManager.Store.Modify(fid, func(f *feature.Feature) error {
				normalizeManagedHelpQueue(f)
				if !hasPendingHelpRequestMessage(f, errorMsg) {
					f.HelpQueue = append(f.HelpQueue, feature.HelpRequest{
						Question: errorMsg,
						Time:     time.Now(),
						Pending:  true,
					})
					slug = f.Slug
				}
				return nil
			})
			if slug != "" {
				notifyCmd = m.maybeNotifyUser(fid, notifyAPIError, slug, "API error: "+sdkMsg.Result.Result)
			}
		} else if sdkMsg.Result.IsSuccess() {
			// Successful result — handle phase completion.
			//
			// Plan and implement phases are intentionally NOT badged here.
			// The phase runner's waitForStatus (internal/agent/implement.go)
			// is the sole authority on whether the agent actually finished
			// vs. ended its turn without writing phase_complete, because
			// only it sees the definitive status after any auto-resume
			// retries. Badging on the Result message raced with waitForStatus
			// and flashed "waiting for input" between phases when both were
			// checking HasPhaseComplete concurrently. The periodic tick
			// (handleTick) observes SessionWaitingHelp when waitForStatus
			// actually escalates and adds the help badge with notification.
			phase := eventPhase(evt.SessionID, evt.FeatureID, evt.Phase)

			if phase == feature.PhaseResearch || phase == feature.PhaseKnowledgeBase ||
				phase == feature.PhaseInquire || phase == feature.PhaseDesign {
				fid := eventFID(evt.SessionID, evt.FeatureID)
				sess := m.sessionManager.GetSession(evt.SessionID)
				var artifactDir string
				if pf, err := m.featureManager.Get(fid); err == nil && pf != nil {
					artifactDir = filepath.Join(agent.ActiveRunDir(m.featureManager.Store.BaseDir, pf), phase.DirName())
				}

				if registryOwnedSingleShotPhase(phase) {
					if sess != nil {
						if artifactDir != "" {
							output := sess.MessageLog().Text()
							_ = os.MkdirAll(artifactDir, 0o755)
							_ = os.WriteFile(filepath.Join(artifactDir, "output.txt"), []byte(output), 0o644)
						}
						cost := agent.ExtractSessionCost(sess)
						if cost.TotalCostUSD > 0 {
							_ = m.featureManager.Store.Modify(fid, func(f *feature.Feature) error {
								f.AddPhaseCost(phase.DirName(), cost.TotalCostUSD)
								return nil
							})
						}
					}
					return m.completeRegistryOwnedSingleShotPhase(fid, phase, evt.SessionID, sess)
				}

				if loopOwnedArtifactPhase(phase) {
					if sess != nil {
						cost := agent.ExtractSessionCost(sess)
						if cost.TotalCostUSD > 0 {
							_ = m.featureManager.Store.Modify(fid, func(f *feature.Feature) error {
								f.AddPhaseCost(phase.DirName(), cost.TotalCostUSD)
								return nil
							})
						}
					}
					go func() {
						if sess != nil {
							_ = sess.Stop()
						}
					}()
					return m, tea.Batch(
						func() tea.Msg { return RefreshFeaturesMsg{} },
						m.listenForEvents(),
					)
				}
			}
		}
	}

	cmds := []tea.Cmd{
		func() tea.Msg { return RefreshFeaturesMsg{} },
		m.listenForEvents(),
	}
	if notifyCmd != nil {
		cmds = append(cmds, notifyCmd)
	}
	return m, tea.Batch(cmds...)
}

// handleTick checks for missed session events on a periodic timer.
func (m AppModel) handleTick() (tea.Model, tea.Cmd) {
	cmds := []tea.Cmd{
		// Re-dispatch the tick
		tea.Tick(tickInterval, func(t time.Time) tea.Msg {
			return TickMsg(t)
		}),
		// Always refresh features to pick up background state changes
		// (e.g. ReviewingGate, CurrentIteration) that don't emit TUI events.
		func() tea.Msg { return RefreshFeaturesMsg{} },
	}

	// Auto-clear transient status messages. Error messages (✗) persist
	// longer so the user has time to notice creation/operation failures.
	if m.statusMessage != "" {
		timeout := 8 * time.Second
		if strings.HasPrefix(m.statusMessage, "✗") || strings.HasPrefix(m.statusMessage, "\u2717") {
			timeout = 30 * time.Second
		}
		if time.Since(m.statusTime) > timeout {
			m.statusMessage = ""
		}
	}

	// Poll active sessions for missed status events
	for _, s := range m.sessionManager.ActiveSessions() {
		if !isGenericFeatureSession(s) {
			continue
		}
		f, err := m.featureManager.Get(s.FeatureID())
		if err != nil {
			continue
		}

		// Also rely on parser-detected states as a secondary signal.
		// Status now latches on waiting states (no oscillation), so using
		// hasPendingHelpRequestMessage is safe — it allows re-detection after
		// a previous prompt was resolved.
		isWaiting := s.Status() == session.SessionWaitingPermission ||
			s.Status() == session.SessionWaitingHelp

		if isWaiting {
			var added bool
			waitingReason := "agent waiting for input"
			_ = m.featureManager.Store.Modify(f.ID, func(f *feature.Feature) error {
				if s.HasPendingAskUserQuestion() {
					if ensurePendingQuestionHelp(f) {
						added = true
					}
					waitingReason = "agent has a question"
					return nil
				}
				if ensurePendingWaitingInputHelp(f) {
					added = true
				}
				// Populate PermissionsQueue for permission-waiting sessions (tick fallback)
				if s.Status() == session.SessionWaitingPermission && !hasPendingPerms(f) {
					lcr := s.LastControlRequest()
					if lcr != nil {
						f.PermissionsQueue = append(f.PermissionsQueue, feature.PermissionRequest{
							Tool:    lcr.Request.ToolName,
							Args:    string(lcr.Request.Input),
							Time:    time.Now(),
							Pending: true,
						})
					}
				}
				return nil
			})
			if added {
				if cmd := m.maybeNotifyUser(f.ID, notifyWaitingInput, f.Slug, waitingReason); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		} else if s.Status() == session.SessionRunning {
			isTweak := isTweakSessionID(s.ID())
			_ = m.featureManager.Store.Modify(f.ID, func(f *feature.Feature) error {
				// For tweak sessions, SessionRunning after a completed turn
				// means the agent is idle and waiting for the user's next
				// message. Don't clear the "waiting input" help badge — it's
				// cleared when the agent starts processing the next message
				// (see handleSDKEvent). Still clear stale permissions below.
				if !isTweak {
					clearPendingHelpByMessage(f, waitingInputHelpMessage)
					clearPendingHelpByMessage(f, questionHelpMessage)
				}
				// Clear stale PermissionsQueue entries when session returns to running
				for i := range f.PermissionsQueue {
					if f.PermissionsQueue[i].Pending {
						f.PermissionsQueue[i].Pending = false
					}
				}
				return nil
			})
		}
	}

	features, _ := m.featureManager.List()
	for _, f := range features {
		if !featureCanRecoverQuestion(f) {
			continue
		}
		if hasPendingHelpRequestMessage(f, questionHelpMessage) {
			continue
		}
		if !phaseNeedsUserInput(m.featureManager.Store.BaseDir, f, f.CurrentPhase, nil) {
			continue
		}
		var added bool
		_ = m.featureManager.Store.Modify(f.ID, func(f *feature.Feature) error {
			added = ensurePendingQuestionHelp(f)
			return nil
		})
		if added {
			if cmd := m.maybeNotifyUser(f.ID, notifyWaitingInput, f.Slug, "agent has a question"); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}

	// Clean up stale lastNotifyTime entries for features with no pending help.
	// This also serves as the cooldown-reset mechanism: once all pending help
	// requests for a feature are resolved, the next waiting cycle will notify
	// immediately because the lastNotifyTime entries have been removed.
	for key := range m.lastNotifyTime {
		f, err := m.featureManager.Get(key.featureID)
		if err != nil {
			delete(m.lastNotifyTime, key)
			continue
		}
		hasPending := false
		for _, h := range f.HelpQueue {
			if h.Pending {
				hasPending = true
				break
			}
		}
		if !hasPending {
			delete(m.lastNotifyTime, key)
		}
	}

	// Plan review no longer needs a background session — the ArtifactReviewModel
	// provides direct editing. The dashboard badge is sufficient; the user
	// attaches when ready.

	return m, tea.Batch(cmds...)
}

func phasePersistsQALog(phase feature.Phase) bool {
	switch phase {
	case feature.PhaseInquire, feature.PhaseResearch, feature.PhaseDesign:
		return true
	default:
		return false
	}
}

func writeSessionQAFile(phase feature.Phase, sess session.SessionView, artifactDir string) {
	if !phasePersistsQALog(phase) || sess == nil || len(sess.QALog()) == 0 {
		return
	}
	_, _ = agent.WriteQAFile(sess.QALog(), artifactDir)
}

func loopOwnedArtifactPhase(phase feature.Phase) bool {
	switch phase {
	case feature.PhaseKnowledgeBase, feature.PhaseInquire, feature.PhaseResearch, feature.PhaseDesign:
		return true
	default:
		return false
	}
}

// handleSessionDone processes session completion.
func (m AppModel) handleSessionDone(msg SessionDoneTUIMsg) (tea.Model, tea.Cmd) {
	done := msg.Done

	// Skip feature-based handling for chat sessions and artifact-review sessions
	if isChatSession(done.SessionID) || isArtifactReviewSession(done.SessionID) {
		return m, m.listenForEvents()
	}

	fid := eventFID(done.SessionID, done.FeatureID)
	sess := m.sessionManager.GetSession(done.SessionID)
	phase := eventPhase(done.SessionID, done.FeatureID, done.Phase)
	if staleSessionEvent(sess, done.StartedAt) {
		return m, m.listenForEvents()
	}

	// Derive success primarily from the SDK result status (StatusCh),
	// not the process exit code. A clean exit (SessionDone) does NOT
	// imply success — the SDK result may indicate failure (max_turns,
	// error, etc.). Only an explicit "SUCCESS" from the result message
	// is treated as success.
	success := false
	if sess != nil {
		select {
		case status := <-sess.StatusCh():
			success = (status == "SUCCESS")
		default:
			// No SDK result available — fall back to process exit code.
			// This handles edge cases like piped/print-mode sessions
			// that may not emit a result message.
			success = (done.Status == session.SessionDone)
		}
	} else {
		success = (done.Status == session.SessionDone)
	}
	registryOwnedSuccess := success && registryOwnedSingleShotPhase(phase)

	// Clear any stale "waiting for input" help requests — session is done.
	needsInput := false
	if !registryOwnedSuccess && !(success && loopOwnedArtifactPhase(phase)) {
		if f, err := m.featureManager.Get(fid); err == nil {
			needsInput = phaseNeedsUserInput(m.featureManager.Store.BaseDir, f, phase, sess)
		}
	}
	_ = m.featureManager.Store.Modify(fid, func(f *feature.Feature) error {
		clearPendingHelpByMessage(f, waitingInputHelpMessage)
		removeResolvedHelpByMessage(f, waitingInputHelpMessage)
		if needsInput {
			ensurePendingQuestionHelp(f)
		} else {
			clearPendingHelpByMessage(f, questionHelpMessage)
			removeResolvedHelpByMessage(f, questionHelpMessage)
		}
		clearPendingHelpByPrefix(f, apiErrorHelpPrefix)
		removeResolvedHelpByPrefix(f, apiErrorHelpPrefix)
		return nil
	})
	// Clear all notification cooldowns for this feature
	for key := range m.lastNotifyTime {
		if key.featureID == fid {
			delete(m.lastNotifyTime, key)
		}
	}

	// Capture cost from session's ResultMessage.
	if sess != nil && (phase == feature.PhaseResearch || phase == feature.PhaseKnowledgeBase ||
		phase == feature.PhaseInquire || phase == feature.PhaseDesign) {
		cost := agent.ExtractSessionCost(sess)
		if cost.TotalCostUSD > 0 {
			_ = m.featureManager.Store.Modify(fid, func(f *feature.Feature) error {
				f.AddPhaseCost(phase.DirName(), cost.TotalCostUSD)
				return nil
			})
		}
	}

	if loopOwnedArtifactPhase(phase) {
		return m, tea.Batch(
			func() tea.Msg { return RefreshFeaturesMsg{} },
			m.listenForEvents(),
		)
	}

	if registryOwnedSuccess {
		if sess != nil {
			if pf, err := m.featureManager.Get(fid); err == nil && pf != nil {
				output := sess.MessageLog().Text()
				artifactDir := filepath.Join(agent.ActiveRunDir(m.featureManager.Store.BaseDir, pf), phase.DirName())
				_ = os.MkdirAll(artifactDir, 0o755)
				_ = os.WriteFile(filepath.Join(artifactDir, "output.txt"), []byte(output), 0o644)
				writeSessionQAFile(phase, sess, artifactDir)
			}
		}
		return m.completeRegistryOwnedSingleShotPhase(fid, phase, done.SessionID, sess)
	}

	// Intercept tweak sessions before hasAdvancedPast (which returns true for
	// PhaseImplement unconditionally, swallowing the SessionDoneMsg).
	if isTweakSessionID(done.SessionID) {
		return m.handleTweakSessionDone(fid, done.SessionID, sess, success)
	}

	// Implementation iterations are managed by RunImplementationLoop which
	// maintains its own aggregate log and sends ImplementLoopDoneMsg.
	// Skip per-iteration output saving to avoid overwriting the aggregate.
	if f, err := m.featureManager.Get(fid); err == nil && hasAdvancedPast(f, phase) {
		return m, m.listenForEvents()
	}

	if needsInput {
		cmds := []tea.Cmd{
			func() tea.Msg { return RefreshFeaturesMsg{} },
			m.listenForEvents(),
		}
		if f, err := m.featureManager.Get(fid); err == nil {
			if notifyCmd := m.maybeNotifyUser(fid, notifyWaitingInput, f.Slug, "agent has a question"); notifyCmd != nil {
				cmds = append(cmds, notifyCmd)
			}
		}
		return m, tea.Batch(cmds...)
	}

	// Save session output as artifact — nest under the active run so the
	// fallback save path matches the orchestrator/phase writers and doesn't
	// resurrect flat <feature>/<phase>/output.txt at the feature root.
	if sess != nil {
		if pf, err := m.featureManager.Get(fid); err == nil && pf != nil {
			output := sess.MessageLog().Text()
			artifactDir := filepath.Join(agent.ActiveRunDir(m.featureManager.Store.BaseDir, pf), phase.DirName())
			_ = os.MkdirAll(artifactDir, 0o755)
			_ = os.WriteFile(filepath.Join(artifactDir, "output.txt"), []byte(output), 0o644)
			writeSessionQAFile(phase, sess, artifactDir)
		}
	}

	var errorDetail string
	if !success && sess != nil {
		errorDetail = sess.ErrorDetail()
		if errorDetail == "" {
			errorDetail = sess.ExitCodeDetail()
		}
	}

	cmd := tea.Batch(
		func() tea.Msg {
			return PhaseCompletedMsg{
				FeatureID:   fid,
				Phase:       phase,
				SessionID:   done.SessionID,
				Success:     success,
				ErrorDetail: errorDetail,
			}
		},
		m.listenForEvents(),
	)

	return m, cmd
}

func staleSessionEvent(sess session.SessionView, eventStartedAt time.Time) bool {
	if sess == nil || eventStartedAt.IsZero() {
		return false
	}
	currentStartedAt := sess.StartedAt()
	return !currentStartedAt.IsZero() && !currentStartedAt.Equal(eventStartedAt)
}

func (m AppModel) completeRegistryOwnedSingleShotPhase(
	featureID string,
	phase feature.Phase,
	sessionID string,
	sess session.SessionView,
) (tea.Model, tea.Cmd) {
	if phase == feature.PhaseKnowledgeBase {
		delete(m.kbStaleWarnings, featureID)
	}
	if sess != nil {
		go func() { _ = sess.Stop() }()
	}
	_ = m.orchestrator.HandlePhaseCompletion(featureID, orchestrator.PhaseCompletionInput{
		Phase:     phase,
		SessionID: sessionID,
		Success:   true,
	})
	return m, tea.Batch(
		func() tea.Msg { return RefreshFeaturesMsg{} },
		m.listenForEvents(),
	)
}

// handlePhaseCompleted delegates phase-completion business logic to the orchestrator.
// The orchestrator owns artifact persistence, Q&A file writing, lifecycle completion,
// phase advancement, event emission, and failure handling.
func (m AppModel) handlePhaseCompleted(msg PhaseCompletedMsg) (tea.Model, tea.Cmd) {
	if msg.Phase == feature.PhaseKnowledgeBase && msg.Success {
		// Clear KB stale warning cache — per-repo completions drive this TUI-only concern.
		delete(m.kbStaleWarnings, msg.FeatureID)
	}
	_ = m.orchestrator.HandlePhaseCompletion(msg.FeatureID, orchestrator.PhaseCompletionInput{
		Phase:       msg.Phase,
		SessionID:   msg.SessionID,
		Success:     msg.Success,
		ErrorDetail: msg.ErrorDetail,
	})
	return m, func() tea.Msg { return RefreshFeaturesMsg{} }
}

// handlePlanLoopDone delegates plan-loop completion to the orchestrator.
// The orchestrator owns roadmap parsing, TotalRoadmapPhases persistence,
// review-gate routing, execution-plan population, and next-phase dispatch.
// When the orchestrator raises a review gate (status=StatusPlanNeedsReview),
// the TUI opens the plan-review editor — a presentation concern that belongs
// in the TUI, not the orchestrator. The orchestrator's ReviewRequired event
// is emitted asynchronously via the event channel and is the observable
// source of truth for subscribers (JSONL stream, headless mode).
func (m AppModel) handlePlanLoopDone(msg PlanLoopDoneMsg) (tea.Model, tea.Cmd) {
	_ = m.orchestrator.HandlePhaseCompletion(msg.FeatureID, orchestrator.PhaseCompletionInput{
		Phase:      feature.PhasePlan,
		PlanResult: msg.Result,
	})
	// If the orchestrator raised a review gate, open the review editor. The
	// review-session UI is a presentation concern that stays TUI-resident. All
	// other transitions (auto-advance roadmap phase, dispatch implementation)
	// are dispatched internally by the orchestrator via startPhase.
	f, err := m.featureManager.Get(msg.FeatureID)
	if err == nil && f != nil && f.Status == feature.StatusPlanNeedsReview {
		return m, tea.Batch(
			func() tea.Msg { return RefreshFeaturesMsg{} },
			m.startPlanReviewSessionCmd(msg.FeatureID, true),
		)
	}
	return m, func() tea.Msg { return RefreshFeaturesMsg{} }
}

// startPlanReviewSessionCmd resolves the correct plan artifact (roadmap, phase-plan,
// or legacy plan) and returns an ArtifactReviewStartMsg with ReviewMode "plan" so the
// user can iterate. When autoAttach is true the TUI only refreshes (badge visible);
// when false, the editor opens immediately.
func (m AppModel) startPlanReviewSessionCmd(featureID string, autoAttach bool) tea.Cmd {
	return func() tea.Msg {
		f, err := m.featureManager.Get(featureID)
		if err != nil {
			return RefreshFeaturesMsg{}
		}

		// Resolve the correct artifact key based on roadmap state:
		// - Roadmap artifact exists and CurrentRoadmapPhase == 0: roadmap artifact
		//   (covers needs_human_review before TotalRoadmapPhases is populated)
		// - CurrentRoadmapPhase > 0: phase-specific plan artifact
		// - Otherwise (legacy): plain "plan" artifact
		artifactKey := "plan"
		if f.CurrentRoadmapPhase == 0 && f.Artifacts["roadmap"] != "" {
			artifactKey = "roadmap"
		} else if f.CurrentRoadmapPhase > 0 {
			artifactKey = fmt.Sprintf("phase-%d-plan", f.CurrentRoadmapPhase)
		}

		planPath := m.resolvePhaseArtifactPath(f, artifactKey)
		if planPath == "" {
			return RefreshFeaturesMsg{}
		}

		// Use the feature's worktree (or repo) path as working directory so the
		// agent chat session has repo-aware scope, not just the artifact directory.
		workDir := ""
		if len(f.Repos) > 0 {
			workDir = f.Repos[0].WorktreePath
			if workDir == "" {
				workDir = f.Repos[0].Path
			}
		}

		return ArtifactReviewStartMsg{
			FeatureID:    featureID,
			ArtifactPath: planPath,
			ReviewMode:   "plan",
			AutoAttach:   autoAttach,
			WorkDir:      workDir,
		}
	}
}

// startRoadmapReviewCmd resolves the roadmap artifact and returns an ArtifactReviewStartMsg.
// Uses ReviewMode "plan" so handlePlanReviewDecision routes proceed/iterate based on
// the roadmap vs phase-plan vs legacy-plan detection in that handler.
func (m AppModel) startRoadmapReviewCmd(featureID string) tea.Cmd {
	return func() tea.Msg {
		f, err := m.featureManager.Get(featureID)
		if err != nil {
			return RefreshFeaturesMsg{}
		}

		// Mark feature as needing plan review so the badge shows.
		// The ArtifactReviewModel is created in detached state by handleArtifactReviewStart
		// so the Reattach check in the attach handler routes correctly on [a].
		_ = m.orchestrator.NeedsPlanReview(featureID)

		roadmapPath := m.resolvePhaseArtifactPath(f, "roadmap")
		if roadmapPath == "" {
			return RefreshFeaturesMsg{}
		}

		workDir := ""
		if len(f.Repos) > 0 {
			workDir = f.Repos[0].WorktreePath
			if workDir == "" {
				workDir = f.Repos[0].Path
			}
		}

		return ArtifactReviewStartMsg{
			FeatureID:    featureID,
			ArtifactPath: roadmapPath,
			ReviewMode:   "plan",
			AutoAttach:   true, // auto-triggered: badge only (user attaches via 'a')
			WorkDir:      workDir,
		}
	}
}

// handleArtifactReviewStart opens the artifact review editor or just refreshes
// the dashboard so the review badge is visible. The user attaches manually via 'a'.
func (m AppModel) handleArtifactReviewStart(msg ArtifactReviewStartMsg) (tea.Model, tea.Cmd) {
	if len(msg.Warnings) > 0 {
		m.statusMessage = "⚠ " + strings.Join(msg.Warnings, "; ")
		m.statusTime = time.Now()
	}
	utilityModel := m.utilityModelForFeature(msg.FeatureID)
	if !msg.AutoAttach && m.currentView != ViewAttach {
		// Explicit attach (e.g. user pressed 'a'): open the editor immediately.
		// Guard against ViewAttach so a background review never kicks the user
		// out of an active session they are watching.
		m.artifactReview = NewArtifactReviewModel(msg.ArtifactPath, msg.FeatureID, msg.ReviewMode, msg.RewindPhase, m.width, m.height, m.sessionManager, msg.WorkDir, m.phaseRunner.BuildSession)
		m.artifactReview.utilityModel = utilityModel
		m.currentView = ViewArtifactReview
		// Focus the editor directly via pointer receiver so the mutation persists
		// on the stored model (Init() has a value receiver, so it would Focus a copy).
		cmd := m.artifactReview.editor.Focus()
		return m, cmd
	}

	// Badge-only: create the model in detached state so the Reattach check
	// in the attach handler picks it up with the correct ReviewMode.
	m.artifactReview = NewArtifactReviewModel(msg.ArtifactPath, msg.FeatureID, msg.ReviewMode, msg.RewindPhase, m.width, m.height, m.sessionManager, msg.WorkDir, m.phaseRunner.BuildSession)
	m.artifactReview.utilityModel = utilityModel
	m.artifactReview.detached = true
	return m, func() tea.Msg { return RefreshFeaturesMsg{} }
}

func (m AppModel) utilityModelForFeature(featureID string) string {
	if m.featureManager != nil {
		if f, err := m.featureManager.Get(featureID); err == nil && f != nil && f.Models.Utilities != "" {
			return f.Models.Utilities
		}
		if m.featureManager.Config != nil && m.featureManager.Config.Defaults.Models.Utilities != "" {
			return m.featureManager.Config.Defaults.Models.Utilities
		}
	}
	return ""
}

// handlePlanReviewDecision processes the user's choice from the plan review menu.
// orchestrator.HandleReviewDecision owns lifecycle mutation and next-phase
// dispatch. TUI only closes the review session and refreshes.
func (m AppModel) handlePlanReviewDecision(msg PlanReviewDecisionMsg) (tea.Model, tea.Cmd) {
	reviewSessionID := fmt.Sprintf("%s-artifact-review", msg.FeatureID)
	if sess := m.sessionManager.GetSession(reviewSessionID); sess != nil {
		_ = m.sessionManager.StopSession(reviewSessionID)
	}

	decision := orchestrator.ReviewDecision{Decision: msg.Decision}
	if f, err := m.featureManager.Get(msg.FeatureID); err == nil {
		decision.Roadmap = f.CurrentRoadmapPhase == 0 && f.Artifacts["roadmap"] != ""
		decision.PhasePlan = f.CurrentRoadmapPhase > 0
		if !decision.Roadmap && !decision.PhasePlan && msg.Decision == "proceed" {
			decision.TargetPhase = feature.PhaseImplement
		}
	}

	_ = m.orchestrator.HandleReviewDecision(msg.FeatureID, decision)
	return m, func() tea.Msg { return RefreshFeaturesMsg{} }
}

// handleRoadmapReviewDecision processes the user's choice from the roadmap approval menu.
// Approve becomes Decision="proceed" with Roadmap=true; reject becomes
// Decision="iterate" with Roadmap=true + Comment. The orchestrator handles
// lifecycle mutation and next-phase dispatch internally.
func (m AppModel) handleRoadmapReviewDecision(msg RoadmapReviewDecisionMsg) (tea.Model, tea.Cmd) {
	reviewSessionID := fmt.Sprintf("%s-artifact-review", msg.FeatureID)
	if sess := m.sessionManager.GetSession(reviewSessionID); sess != nil {
		_ = m.sessionManager.StopSession(reviewSessionID)
	}

	m.currentView = ViewDashboard

	decision := "proceed"
	if msg.Decision == "reject" {
		decision = "iterate"
	}
	_ = m.orchestrator.HandleReviewDecision(msg.FeatureID, orchestrator.ReviewDecision{
		Decision: decision,
		Roadmap:  true,
		Comment:  msg.Comment,
	})
	return m, func() tea.Msg { return RefreshFeaturesMsg{} }
}

// startRewindReviewSessionCmd launches a temporary interactive session for reviewing
// the previous phase's output before re-running the target phase.
// When autoAttach is true the TUI only refreshes (badge visible); when false, the editor opens immediately.
func (m AppModel) startRewindReviewSessionCmd(featureID string, targetPhase feature.Phase, autoAttach bool) tea.Cmd {
	return func() tea.Msg {
		if m.orchestrator == nil {
			return RefreshFeaturesMsg{}
		}
		ctx, err := m.orchestrator.ResolveRewindReviewContext(featureID, targetPhase)
		if err != nil || ctx.ArtifactPath == "" {
			return RewindDoneMsg{
				FeatureID:   featureID,
				TargetPhase: targetPhase,
				Err:         fmt.Errorf("cannot start rewind review: no artifact found for the previous phase"),
			}
		}

		return ArtifactReviewStartMsg{
			FeatureID:    featureID,
			ArtifactPath: ctx.ArtifactPath,
			ReviewMode:   "rewind",
			RewindPhase:  targetPhase,
			AutoAttach:   autoAttach,
			WorkDir:      ctx.WorkDir,
			Warnings:     ctx.Warnings,
		}
	}
}

// handleRewindReviewDecision confirms a rewind that was already performed by
// rewindCmd (which calls Lifecycle.RewindToPhase before opening this review).
// The orchestrator owns post-rewind state changes (clear PendingReviewPhase,
// read-back description-review.md, CompletePlanning for Implement) and
// next-phase dispatch via ProceedFromRewindReview. TUI cleans up the
// artifact-review session. msg.Phase is the effective target propagated from
// RewindDoneMsg, so no second RewindToPhase call is needed (or wanted — that
// regression produced phantom extra runs on every confirm).
func (m AppModel) handleRewindReviewDecision(msg RewindReviewDecisionMsg) (tea.Model, tea.Cmd) {
	reviewSessionID := fmt.Sprintf("%s-artifact-review", msg.FeatureID)
	if sess := m.sessionManager.GetSession(reviewSessionID); sess != nil {
		_ = m.sessionManager.StopSession(reviewSessionID)
	}
	_ = m.orchestrator.ProceedFromRewindReview(msg.FeatureID, msg.Phase)
	return m, func() tea.Msg { return RefreshFeaturesMsg{} }
}

// triggerReviewGateCmd pauses the workflow at a checkpoint by asking the
// orchestrator to enter the review-gate state and resolve the gate-review
// context (artifact + workdir). The orchestrator owns Store.Modify for the
// status transition, PendingReviewPhase bookkeeping, and the target-phase →
// artifact-key mapping. This helper is a thin delegate that forwards the
// resolved context into an ArtifactReviewStartMsg.
func (m AppModel) triggerReviewGateCmd(featureID string, targetPhase feature.Phase) tea.Cmd {
	return func() tea.Msg {
		_ = m.orchestrator.EnterReviewGate(featureID, targetPhase)
		return m.gateReviewStartMsg(featureID, targetPhase, true) // auto-triggered: badge only
	}
}

// gateReviewStartMsg translates orchestrator.ResolveGateReviewContext into a
// TUI message. Used by triggerReviewGateCmd and gateReviewStartCmd — both
// paths need the same translation. The orchestrator owns artifact resolution;
// this helper only handles the tea.Msg framing.
func (m AppModel) gateReviewStartMsg(featureID string, targetPhase feature.Phase, autoAttach bool) tea.Msg {
	ctx, err := m.orchestrator.ResolveGateReviewContext(featureID, targetPhase)
	if err != nil || ctx.ArtifactPath == "" {
		return RefreshFeaturesMsg{}
	}
	return ArtifactReviewStartMsg{
		FeatureID:    featureID,
		ArtifactPath: ctx.ArtifactPath,
		ReviewMode:   "gate",
		RewindPhase:  targetPhase,
		AutoAttach:   autoAttach,
		WorkDir:      ctx.WorkDir,
	}
}

// gateReviewStartCmd wraps gateReviewStartMsg as a tea.Cmd for use in attach
// handlers and key-press dispatches.
func (m AppModel) gateReviewStartCmd(featureID string, targetPhase feature.Phase, autoAttach bool) tea.Cmd {
	return func() tea.Msg {
		return m.gateReviewStartMsg(featureID, targetPhase, autoAttach)
	}
}

// handleGateReviewDecision delegates gate decisions to the orchestrator. Orchestrator
// clears gate state, handles roadmap-gate advancement, executes CompletePlanning,
// populates the execution plan, and dispatches the target phase. TUI only stops the
// artifact-review session.
func (m AppModel) handleGateReviewDecision(msg GateReviewDecisionMsg) (tea.Model, tea.Cmd) {
	reviewSessionID := fmt.Sprintf("%s-artifact-review", msg.FeatureID)
	if sess := m.sessionManager.GetSession(reviewSessionID); sess != nil {
		_ = m.sessionManager.StopSession(reviewSessionID)
	}
	_ = m.orchestrator.HandleReviewDecision(msg.FeatureID, orchestrator.ReviewDecision{
		Decision:    "proceed",
		TargetPhase: msg.Phase,
	})
	return m, func() tea.Msg { return RefreshFeaturesMsg{} }
}

// handleImplementLoopDone is a no-op. ImplementLoopDoneMsg is no longer
// produced in production: single-repo cycle paths route through the per-repo
// cycle FR pipeline, and main-path implementation uses the multi-repo engine.
// Kept for legacy test compatibility.
func (m AppModel) handleImplementLoopDone(_ ImplementLoopDoneMsg) (tea.Model, tea.Cmd) {
	return m, func() tea.Msg { return RefreshFeaturesMsg{} }
}

// attachNeedUserInput opens the artifact-review shell in need-user-input mode,
// pointed at the feature's pending gate artifact. When repoName is non-empty
// the routing prefers the cycle-scoped gate (RepoCycles[repoName]) over a
// stale repo-impl gate; both fall through to the feature-scoped gate.
func (m AppModel) attachNeedUserInput(f *feature.Feature, repoName string) (tea.Model, tea.Cmd) {
	if f == nil {
		return m, nil
	}
	gatePath := f.PendingNeedUserInputPath
	var cycleType feature.RepoCycleType
	if repoName != "" {
		if rc, ok := f.RepoCycles[repoName]; ok && rc != nil &&
			rc.Status == feature.RepoCycleNeedUserInput &&
			rc.PendingNeedUserInputPath != "" {
			gatePath = rc.PendingNeedUserInputPath
			cycleType = rc.Type
		} else if f.PendingNeedUserInputPath == "" {
			// No cycle-scoped gate and no feature-scoped gate — nothing
			// to attach to.
			return m, nil
		}
	}
	if gatePath == "" {
		return m, nil
	}
	// Reattach key: feature-scoped gates use empty RepoName so a re-entry
	// from a different repo-tab focus still finds the same review.
	expectedRepoName := ""
	if cycleType != "" {
		expectedRepoName = repoName
	}
	if m.artifactReview.FeatureID() == f.ID &&
		m.artifactReview.RepoName() == expectedRepoName &&
		m.artifactReview.CycleType() == cycleType &&
		m.artifactReview.ReviewMode() == reviewModeNeedUserInput &&
		m.artifactReview.Detached() && !m.artifactReview.Decided() {
		cmd := m.artifactReview.Reattach()
		m.currentView = ViewArtifactReview
		return m, cmd
	}
	m.artifactReview = NewArtifactReviewModel(
		gatePath, f.ID, reviewModeNeedUserInput,
		feature.PhaseImplement, m.width, m.height,
		m.sessionManager, "", nil,
	)
	// Only attach RepoName when the gate is genuinely cycle-scoped. A
	// phase-implement gate is feature-scoped (Feature.PendingNeedUserInputPath);
	// attaching a repoName from the TUI's repo-tab focus context would
	// mis-route the decision dispatch.
	if cycleType != "" {
		m.artifactReview = m.artifactReview.SetRepoName(repoName)
		m.artifactReview = m.artifactReview.SetCycleType(cycleType)
	}
	m.currentView = ViewArtifactReview
	return m, nil
}

// firstRepoNeedingInput returns the first paused cycle / repo for a feature.
// Empty string when no paused work is available. Works for any repo count: in
// the unified design N=1 is a degenerate multi-repo, so a single-repo
// published feature with a paused post-publish cycle still routes through the
// per-repo gate path.
func firstRepoNeedingInput(f *feature.Feature) string {
	if f == nil {
		return ""
	}
	if cycles := f.PendingUserInputCycles(); len(cycles) > 0 {
		return cycles[0].RepoName
	}
	return ""
}

func (m AppModel) contextualAttentionForFeature(f *feature.Feature) featureAttention {
	return computeFeatureAttention(f, m.livePreviewSessionForFeature(f))
}

func (m AppModel) openContextualFeatureAction(f *feature.Feature) (tea.Model, tea.Cmd) {
	if f == nil {
		return m, nil
	}
	att := m.contextualAttentionForFeature(f)
	switch att.Kind {
	case attentionPermission, attentionAskUser, attentionWatch:
		return m, m.attachToFeature(f)
	case attentionNeedUserInput:
		return m.attachNeedUserInput(f, att.RepoName)
	case attentionReview:
		return m.openReviewAttention(f)
	default:
		return m, nil
	}
}

func (m AppModel) openReviewAttention(f *feature.Feature) (tea.Model, tea.Cmd) {
	if f == nil || !f.Status.IsNeedsReview() {
		return m, nil
	}
	if m.artifactReview.FeatureID() == f.ID && m.artifactReview.Detached() && !m.artifactReview.Decided() {
		cmd := m.artifactReview.Reattach()
		m.currentView = ViewArtifactReview
		return m, cmd
	}
	if f.PendingReviewPhase != nil && f.IsRewind {
		return m, m.startRewindReviewSessionCmd(f.ID, *f.PendingReviewPhase, false)
	}
	if f.PendingReviewPhase != nil && !f.IsRewind {
		return m, m.gateReviewStartCmd(f.ID, *f.PendingReviewPhase, false)
	}
	if f.Status == feature.StatusPlanNeedsReview {
		return m, m.startPlanReviewSessionCmd(f.ID, false)
	}
	return m, nil
}

// handleNeedUserInputDecision routes the user's gate-menu choice through the
// orchestrator. On error the questionnaire stays open with the failure
// surfaced so the user can retry or abort instead of being stranded.
func (m AppModel) handleNeedUserInputDecision(msg NeedUserInputDecisionMsg) (tea.Model, tea.Cmd) {
	if err := m.orchestrator.HandleNeedUserInputDecision(msg.FeatureID, orchestrator.NeedUserInputDecision{
		Decision:  msg.Decision,
		RepoName:  msg.RepoName,
		CycleType: msg.CycleType,
	}); err != nil {
		m.artifactReview = m.artifactReview.WithDecisionError(err)
		m.currentView = ViewArtifactReview
		return m, func() tea.Msg { return RefreshFeaturesMsg{} }
	}
	m.currentView = ViewDashboard
	return m, func() tea.Msg { return RefreshFeaturesMsg{} }
}

// handleMultiRepoImplDone delegates multi-repo implement completion to the
// orchestrator. The orchestrator owns CompleteImplementation, cycle routing
// (review-comments / rebase / refactor), roadmap advancement, Final Review
// dispatch, MarkCodeReady, per-repo auto-publish, and failure attribution.
func (m AppModel) handleMultiRepoImplDone(msg MultiRepoImplDoneMsg) (tea.Model, tea.Cmd) {
	// Guard against stale messages from previous orchestrator runs. After a restart
	// ("r" pressed twice), the old orchestrator goroutine still sends a result.
	if f, err := m.featureManager.Get(msg.FeatureID); err == nil {
		if f.Status != feature.StatusImplementing {
			return m, func() tea.Msg { return RefreshFeaturesMsg{} }
		}
	}
	_ = m.orchestrator.HandlePhaseCompletion(msg.FeatureID, orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		Success:         msg.Result != nil && msg.Result.FinalStatus == "all_passed",
		MultiRepoResult: msg.Result,
	})
	return m, func() tea.Msg { return RefreshFeaturesMsg{} }
}

func (m AppModel) updateDashboard(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// When chat panel is open, route all key events to the chat model
	if m.chatOpen {
		var cmd tea.Cmd
		m.chat, cmd = m.chat.Update(msg)
		return m, cmd
	}

	// When right panel is focused, route detail actions to the selected feature
	if m.dashboard.focusPanel == 1 {
		return m.updateDashboardRightPanel(msg)
	}

	switch {
	case key.Matches(msg, keys.Enter):
		// Enter toggles section collapse or focuses right panel (handled by dashboard.Update)
		prevSection := m.dashboard.SelectedSection()
		m.dashboard, _ = m.dashboard.Update(msg)
		// Ghost CTA: Enter on empty-state CTA triggers wizard
		if m.dashboard.ConsumeWantNewFeature() {
			return m.transitionToWizard(), nil
		}
		// If a section header was toggled, persist collapsed state
		if prevSection != "" && m.configPath != "" {
			m.featureManager.Config.UI.CollapsedSections = m.dashboard.CollapsedSectionsList()
			_ = config.Save(m.configPath, m.featureManager.Config)
		}
	case key.Matches(msg, keys.New):
		return m.transitionToWizard(), nil
	case key.Matches(msg, keys.ViewDiff):
		f := m.dashboard.SelectedFeature()
		if f != nil && f.Status == feature.StatusCodeReady && len(f.Repos) > 0 && f.Repos[0].WorktreePath != "" {
			return m, m.viewDiffCmd(f.ID, f.Slug, f.Repos[0].WorktreePath, f.Repos[0].BaseBranch)
		}
	case key.Matches(msg, keys.Publish):
		f := m.dashboard.SelectedFeature()
		if f != nil && f.Status == feature.StatusCodeReady && !f.Checkpoints.AutoPublish() && f.IsPublishable() {
			return m.transitionToPublish(f.ID)
		}
	case key.Matches(msg, keys.Delete):
		f := m.dashboard.SelectedFeature()
		if f != nil {
			return m.confirmDelete(f.ID, f.Name), nil
		}
	case key.Matches(msg, keys.Rebase):
		f := m.dashboard.SelectedFeature()
		if f != nil && (f.Status == feature.StatusPublished || f.Status == feature.StatusCodeReady) {
			if len(f.Repos) <= 1 {
				m.statusMessage = "Rebasing " + f.Slug + "..."
				m.statusTime = time.Now()
			}
			return m, m.dispatchCycleKey(f.ID, feature.CycleRebase)
		}
	case key.Matches(msg, keys.ResumeAll):
		return m.confirmResumeAll(), nil
	case key.Matches(msg, keys.WorkspaceManager):
		return m.transitionToWorkspaceManager()
	case key.Matches(msg, keys.Chat):
		return m.transitionToChat()
	case key.Matches(msg, keys.Attach):
		f := m.dashboard.SelectedFeature()
		return m.openContextualFeatureAction(f)
	case key.Matches(msg, keys.Restart):
		f := m.dashboard.SelectedFeature()
		if f != nil {
			return m, m.restartPhaseCmd(f.ID)
		}
	case key.Matches(msg, keys.ApproveAndRemember):
		f := m.dashboard.SelectedFeature()
		if f != nil && hasPendingPerms(f) {
			return m, m.approveAndRememberCmd(f.ID)
		}
	default:
		m.dashboard, _ = m.dashboard.Update(msg)
	}
	return m, nil
}

// updateDashboardRightPanel handles keys when the right detail panel is focused.
func (m AppModel) updateDashboardRightPanel(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// If help input is active, forward events there
	if m.helpInputActive {
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
			answer := strings.TrimSpace(m.helpInput.Value())
			if answer == "" {
				return m, nil
			}
			m.helpInputActive = false
			m.helpInput.Blur()
			return m, m.answerHelpCmd(m.helpFeatureID, answer)

		case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
			m.helpInputActive = false
			m.helpInput.Blur()
			return m, nil

		default:
			var cmd tea.Cmd
			m.helpInput, cmd = m.helpInput.Update(msg)
			return m, cmd
		}
	}

	// If refactor pipeline selector is active, handle its keys
	if m.refactorPipelineActive {
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
			m.refactorPipelineActive = false
			return m, nil
		case key.Matches(msg, key.NewBinding(key.WithKeys("left", "h"))):
			if m.refactorPipelineCursor > 0 {
				m.refactorPipelineCursor--
			}
			return m, nil
		case key.Matches(msg, key.NewBinding(key.WithKeys("right", "l"))):
			if m.refactorPipelineCursor < len(m.refactorPipelineOptions)-1 {
				m.refactorPipelineCursor++
			}
			return m, nil
		case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
			selected := m.refactorPipelineOptions[m.refactorPipelineCursor]
			featureID := m.refactorPipelineFeatureID
			repoName := m.refactorPipelineRepoName
			prompt := m.refactorPipelinePrompt
			m.refactorPipelineActive = false
			cmd := m.applyRefactorPipelineAndStart(featureID, repoName, prompt, selected)
			return m, cmd
		}
		return m, nil
	}

	// If refactor input is active, forward events there
	if m.refactorInputActive {
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
			m.refactorInputActive = false
			m.refactorInput.Blur()
			m.cycleSelectRefactor = ""
			return m, nil
		case key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+s"))):
			prompt := strings.TrimSpace(m.refactorInput.Value())
			if prompt == "" {
				return m, nil
			}
			featureID := m.refactorFeatureID
			repoName := m.cycleSelectRefactor
			m.refactorInputActive = false
			m.refactorInput.Blur()
			m.cycleSelectRefactor = ""
			// Activate pipeline selector overlay with all profiles
			m.refactorPipelineActive = true
			m.refactorPipelineFeatureID = featureID
			m.refactorPipelineRepoName = repoName
			m.refactorPipelinePrompt = prompt
			m.refactorPipelineOptions = []feature.PipelineProfile{
				feature.PipelineMedium,
				feature.PipelineLarge,
				feature.PipelineMoonshot,
			}
			m.refactorPipelineCursor = 1 // default to large
			return m, nil
		default:
			var cmd tea.Cmd
			m.refactorInput, cmd = m.refactorInput.Update(msg)
			return m, cmd
		}
	}

	f := m.dashboard.SelectedFeature()

	switch {
	case key.Matches(msg, keys.Back), key.Matches(msg, keys.PanelLeft):
		// Return focus to left panel
		m.dashboard, _ = m.dashboard.Update(msg)
		return m, nil

	case key.Matches(msg, keys.Tab):
		// Tab also switches panels
		m.dashboard, _ = m.dashboard.Update(msg)
		return m, nil

	case key.Matches(msg, keys.Up):
		m.dashboard.MoveToAdjacentFeature(-1)
		return m, nil

	case key.Matches(msg, keys.Down):
		m.dashboard.MoveToAdjacentFeature(1)
		return m, nil

	case key.Matches(msg, keys.Attach):
		return m.openContextualFeatureAction(f)

	case key.Matches(msg, keys.Overview):
		if m.dashboard.ShowOverview() {
			return m, nil
		}

	case key.Matches(msg, keys.Approve):
		if f != nil {
			return m, m.approvePermissionsCmd(f.ID)
		}

	case key.Matches(msg, keys.ApproveAndRemember):
		if f != nil && hasPendingPerms(f) {
			return m, m.approveAndRememberCmd(f.ID)
		}

	case key.Matches(msg, keys.Help):
		if f != nil {
			for _, h := range f.HelpQueue {
				if h.Pending {
					m.helpInputActive = true
					m.helpFeatureID = f.ID
					m.helpQuestion = normalizeManagedHelpQuestion(h.Question)
					m.helpInput = textinput.New()
					m.helpInput.Placeholder = "Type your answer..."
					m.helpInput.Focus()
					return m, textinput.Blink
				}
			}
		}

	case key.Matches(msg, keys.ToggleInputNotify):
		if f != nil {
			muted, err := m.toggleFeatureInputNotifications(f.ID)
			if err != nil {
				m.statusMessage = "✗ " + err.Error()
			} else if muted {
				m.statusMessage = "✓ Input notifications muted for " + f.Slug
			} else {
				m.statusMessage = "✓ Input notifications enabled for " + f.Slug
			}
			m.statusTime = time.Now()
			return m, func() tea.Msg { return RefreshFeaturesMsg{} }
		}

	case key.Matches(msg, keys.Rewind):
		if f != nil {
			if hasActiveRepoCycles(f) {
				m.statusMessage = "Stop active repo cycles before rewinding"
				m.statusTime = time.Now()
				return m, nil
			}
			choices := feature.RewindChoicesForFeature(f)
			if len(choices) == 0 {
				return m, nil
			}
			m.rewindMenuActive = true
			m.rewindMenuFeatureID = f.ID
			m.rewindMenuChoices = choices
			m.rewindMenuCursor = 0
			// Compute upgrade options for non-moonshot features
			m.rewindMenuUpgradeOptions = nil
			profile := f.EffectivePipeline()
			if profile == feature.PipelineMedium {
				m.rewindMenuUpgradeOptions = []feature.PipelineProfile{feature.PipelineLarge, feature.PipelineMoonshot}
			} else if profile == feature.PipelineLarge {
				m.rewindMenuUpgradeOptions = []feature.PipelineProfile{feature.PipelineMoonshot}
			}
			return m, nil
		}

	case key.Matches(msg, keys.Restart):
		if f != nil {
			return m, m.restartPhaseCmd(f.ID)
		}

	case key.Matches(msg, keys.Stop):
		if f != nil && isRunningFeature(f) {
			return m.confirmStop(f.ID, f.Name), nil
		}

	case key.Matches(msg, keys.ViewLogs):
		if m.dashboard.showingOverviewForLiveFeature() {
			m.dashboard.ShowLivePreview()
			return m, nil
		}
		if f != nil {
			return m, m.viewLogsCmd(f.ID, f.CurrentPhase, f.CurrentRoadmapPhase)
		}

	case key.Matches(msg, keys.ViewDiff):
		if f != nil && f.Status == feature.StatusCodeReady && len(f.Repos) > 0 && f.Repos[0].WorktreePath != "" {
			return m, m.viewDiffCmd(f.ID, f.Slug, f.Repos[0].WorktreePath, f.Repos[0].BaseBranch)
		}

	case key.Matches(msg, keys.CleanWorktree):
		if f != nil && isCompletedFeature(f) && len(f.Repos) > 0 && f.Repos[0].WorktreePath != "" {
			return m, m.cleanWorktreeCmd(f.ID)
		}

	case key.Matches(msg, keys.Rebase):
		if f != nil && (f.Status == feature.StatusPublished || f.Status == feature.StatusCodeReady) {
			if len(f.Repos) <= 1 {
				m.statusMessage = "Rebasing " + f.Slug + "..."
				m.statusTime = time.Now()
			}
			return m, m.dispatchCycleKey(f.ID, feature.CycleRebase)
		}

	case key.Matches(msg, keys.ReviewComments):
		if f != nil && f.Status == feature.StatusPublished {
			hasPR := len(f.PRURLs()) > 0
			if hasPR {
				return m, m.dispatchCycleKey(f.ID, feature.CycleReviewComments)
			}
		}

	case key.Matches(msg, keys.Tweak):
		if f != nil && (f.Status == feature.StatusPublished || (f.Status == feature.StatusCodeReady && !f.Checkpoints.AutoPublish())) {
			if len(f.Repos) > 1 && f.Status != feature.StatusPublished {
				return m, nil
			}
			return m, m.dispatchCycleKey(f.ID, feature.CycleTweak)
		}

	case key.Matches(msg, keys.Refactor):
		if f != nil && (f.Status == feature.StatusPublished || (f.Status == feature.StatusCodeReady && !f.Checkpoints.AutoPublish())) {
			if hasRunningRefactorCycle(f) {
				m.statusMessage = "A refactor cycle is already running on this feature"
				m.statusTime = time.Now()
				return m, nil
			}
			if len(f.Repos) > 1 && f.Status != feature.StatusPublished {
				m.statusMessage = "Multi-repo refactor is only supported for Published features"
				m.statusTime = time.Now()
				return m, nil
			}
			return m, m.dispatchCycleKey(f.ID, feature.CycleRefactor)
		}

	case key.Matches(msg, keys.EditConfig):
		if f != nil && isFeatureQuiescent(f) {
			cat := BuildPhaseModelCatalog(m.registry, m.featureManager.Config.Defaults)
			m.editConfig = NewEditConfigModel(f, cat, f.IsPublishable())
			m.editConfigActive = true
			return m, nil
		}
		if f != nil {
			m.statusMessage = "Config can only be edited when the feature is idle"
			m.statusTime = time.Now()
			return m, nil
		}

	case key.Matches(msg, keys.MergeLocal):
		if f != nil && f.Status == feature.StatusCodeReady && !f.IsPublishable() {
			m.statusMessage = "Merging " + f.Slug + " to base branch..."
			m.statusTime = time.Now()
			return m, m.mergeLocalCmd(f.ID)
		}

	case key.Matches(msg, keys.MarkDone):
		if f != nil && (f.Status == feature.StatusPublished || (f.Status == feature.StatusCodeReady && !f.IsPublishable())) {
			if hasActiveRepoCycles(f) {
				m.statusMessage = "Cannot mark done while repo cycles are active"
				m.statusTime = time.Now()
				return m, nil
			}
			return m, m.markDoneCmd(f.ID)
		}

	case key.Matches(msg, keys.Publish):
		if f != nil && f.Status == feature.StatusCodeReady && !f.Checkpoints.AutoPublish() && f.IsPublishable() {
			return m.transitionToPublish(f.ID)
		}

	case key.Matches(msg, keys.ManualPublish):
		if f != nil && f.Status == feature.StatusCodeReady {
			return m.confirmManualPublish(f.ID, f.Name), nil
		}

	case key.Matches(msg, keys.Delete):
		if f != nil {
			if hasActiveRepoCycles(f) {
				m.statusMessage = "Stop active repo cycles before deleting"
				m.statusTime = time.Now()
				return m, nil
			}
			return m.confirmDelete(f.ID, f.Name), nil
		}

	case key.Matches(msg, keys.Chat):
		return m.transitionToChat()
	}
	return m, nil
}

func (m AppModel) updateDetail(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// If help input is active, forward events there
	if m.helpInputActive {
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
			answer := strings.TrimSpace(m.helpInput.Value())
			if answer == "" {
				return m, nil
			}
			m.helpInputActive = false
			m.helpInput.Blur()
			return m, m.answerHelpCmd(m.helpFeatureID, answer)

		case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
			m.helpInputActive = false
			m.helpInput.Blur()
			return m, nil

		default:
			var cmd tea.Cmd
			m.helpInput, cmd = m.helpInput.Update(msg)
			return m, cmd
		}
	}

	// If refactor pipeline selector is active, handle its keys
	if m.refactorPipelineActive {
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
			m.refactorPipelineActive = false
			return m, nil
		case key.Matches(msg, key.NewBinding(key.WithKeys("left", "h"))):
			if m.refactorPipelineCursor > 0 {
				m.refactorPipelineCursor--
			}
			return m, nil
		case key.Matches(msg, key.NewBinding(key.WithKeys("right", "l"))):
			if m.refactorPipelineCursor < len(m.refactorPipelineOptions)-1 {
				m.refactorPipelineCursor++
			}
			return m, nil
		case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
			selected := m.refactorPipelineOptions[m.refactorPipelineCursor]
			featureID := m.refactorPipelineFeatureID
			repoName := m.refactorPipelineRepoName
			prompt := m.refactorPipelinePrompt
			m.refactorPipelineActive = false
			cmd := m.applyRefactorPipelineAndStart(featureID, repoName, prompt, selected)
			return m, cmd
		}
		return m, nil
	}

	// If refactor input is active, forward events there
	if m.refactorInputActive {
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
			m.refactorInputActive = false
			m.refactorInput.Blur()
			m.cycleSelectRefactor = ""
			return m, nil
		case key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+s"))):
			prompt := strings.TrimSpace(m.refactorInput.Value())
			if prompt == "" {
				return m, nil
			}
			featureID := m.refactorFeatureID
			repoName := m.cycleSelectRefactor
			m.refactorInputActive = false
			m.refactorInput.Blur()
			m.cycleSelectRefactor = ""
			// Activate pipeline selector overlay with all profiles
			m.refactorPipelineActive = true
			m.refactorPipelineFeatureID = featureID
			m.refactorPipelineRepoName = repoName
			m.refactorPipelinePrompt = prompt
			m.refactorPipelineOptions = []feature.PipelineProfile{
				feature.PipelineMedium,
				feature.PipelineLarge,
				feature.PipelineMoonshot,
			}
			m.refactorPipelineCursor = 1 // default to large
			return m, nil
		default:
			var cmd tea.Cmd
			m.refactorInput, cmd = m.refactorInput.Update(msg)
			return m, cmd
		}
	}

	switch {
	case key.Matches(msg, keys.Back):
		return m.transitionTo(ViewDashboard, ""), nil

	case key.Matches(msg, keys.Attach):
		return m.openContextualFeatureAction(m.detail.feature)

	case key.Matches(msg, keys.Approve):
		// Approve all pending permissions for this feature
		if m.detail.feature != nil {
			return m, m.approvePermissionsCmd(m.detail.feature.ID)
		}
		return m, nil

	case key.Matches(msg, keys.ApproveAndRemember):
		if m.detail.feature != nil && hasPendingPerms(m.detail.feature) {
			return m, m.approveAndRememberCmd(m.detail.feature.ID)
		}
		return m, nil

	case key.Matches(msg, keys.Help):
		// Show input for answering the first pending help request
		if m.detail.feature != nil {
			for _, h := range m.detail.feature.HelpQueue {
				if h.Pending {
					m.helpInputActive = true
					m.helpFeatureID = m.detail.feature.ID
					m.helpQuestion = normalizeManagedHelpQuestion(h.Question)
					m.helpInput = textinput.New()
					m.helpInput.Placeholder = "Type your answer..."
					m.helpInput.Focus()
					return m, textinput.Blink
				}
			}
		}
		return m, nil

	case key.Matches(msg, keys.ToggleInputNotify):
		if m.detail.feature != nil {
			muted, err := m.toggleFeatureInputNotifications(m.detail.feature.ID)
			if err != nil {
				m.statusMessage = "✗ " + err.Error()
			} else if muted {
				m.statusMessage = "✓ Input notifications muted for " + m.detail.feature.Slug
			} else {
				m.statusMessage = "✓ Input notifications enabled for " + m.detail.feature.Slug
			}
			m.statusTime = time.Now()
			return m, func() tea.Msg { return RefreshFeaturesMsg{} }
		}
		return m, nil

	case key.Matches(msg, keys.Rewind):
		if m.detail.feature != nil {
			if hasActiveRepoCycles(m.detail.feature) {
				m.statusMessage = "Stop active repo cycles before rewinding"
				m.statusTime = time.Now()
				return m, nil
			}
			choices := feature.RewindChoicesForFeature(m.detail.feature)
			if len(choices) == 0 {
				return m, nil
			}
			m.rewindMenuActive = true
			m.rewindMenuFeatureID = m.detail.feature.ID
			m.rewindMenuChoices = choices
			m.rewindMenuCursor = 0
			m.rewindMenuUpgradeOptions = nil
			profile := m.detail.feature.EffectivePipeline()
			if profile == feature.PipelineMedium {
				m.rewindMenuUpgradeOptions = []feature.PipelineProfile{feature.PipelineLarge, feature.PipelineMoonshot}
			} else if profile == feature.PipelineLarge {
				m.rewindMenuUpgradeOptions = []feature.PipelineProfile{feature.PipelineMoonshot}
			}
			return m, nil
		}
		return m, nil

	case key.Matches(msg, keys.RetryPhase):
		// Retry the failed phase atomically across every phase-declared
		// repo — only when feature is quiescent (Failed/Interrupted) so
		// we don't kill unrelated in-flight sessions.
		if m.detail.feature != nil && featureHasFailedRepos(m.detail.feature) &&
			(m.detail.feature.Status == feature.StatusFailed || m.detail.feature.Status == feature.StatusInterrupted) {
			return m, m.retryPhaseCmd(m.detail.feature.ID)
		}
		return m, nil

	case key.Matches(msg, keys.Restart):
		if m.detail.feature != nil {
			return m, m.restartPhaseCmd(m.detail.feature.ID)
		}
		return m, nil

	case key.Matches(msg, keys.Stop):
		if m.detail.feature != nil && isRunningFeature(m.detail.feature) {
			return m.confirmStop(m.detail.feature.ID, m.detail.feature.Name), nil
		}
		return m, nil

	case key.Matches(msg, keys.ViewLogs):
		if m.detail.feature != nil {
			return m, m.viewLogsCmd(m.detail.feature.ID, m.detail.feature.CurrentPhase, m.detail.feature.CurrentRoadmapPhase)
		}
		return m, nil

	case key.Matches(msg, keys.ViewDiff):
		if m.detail.feature != nil && m.detail.feature.Status == feature.StatusCodeReady && len(m.detail.feature.Repos) > 0 && m.detail.feature.Repos[0].WorktreePath != "" {
			return m, m.viewDiffCmd(m.detail.feature.ID, m.detail.feature.Slug, m.detail.feature.Repos[0].WorktreePath, m.detail.feature.Repos[0].BaseBranch)
		}
		return m, nil

	case key.Matches(msg, keys.CleanWorktree):
		if m.detail.feature != nil && isCompletedFeature(m.detail.feature) && len(m.detail.feature.Repos) > 0 && m.detail.feature.Repos[0].WorktreePath != "" {
			return m, m.cleanWorktreeCmd(m.detail.feature.ID)
		}
		return m, nil

	case key.Matches(msg, keys.Rebase):
		if m.detail.feature != nil && (m.detail.feature.Status == feature.StatusPublished || m.detail.feature.Status == feature.StatusCodeReady) {
			if len(m.detail.feature.Repos) <= 1 {
				m.statusMessage = "Rebasing " + m.detail.feature.Slug + "..."
				m.statusTime = time.Now()
			}
			return m, m.dispatchCycleKey(m.detail.feature.ID, feature.CycleRebase)
		}
		return m, nil

	case key.Matches(msg, keys.ReviewComments):
		if m.detail.feature != nil && m.detail.feature.Status == feature.StatusPublished {
			f := m.detail.feature
			hasPR := len(f.PRURLs()) > 0
			if hasPR {
				return m, m.dispatchCycleKey(f.ID, feature.CycleReviewComments)
			}
		}
		return m, nil

	case key.Matches(msg, keys.Tweak):
		if m.detail.feature != nil && (m.detail.feature.Status == feature.StatusPublished || (m.detail.feature.Status == feature.StatusCodeReady && !m.detail.feature.Checkpoints.AutoPublish())) {
			if len(m.detail.feature.Repos) > 1 && m.detail.feature.Status != feature.StatusPublished {
				return m, nil
			}
			return m, m.dispatchCycleKey(m.detail.feature.ID, feature.CycleTweak)
		}
		return m, nil

	case key.Matches(msg, keys.Refactor):
		if m.detail.feature != nil && (m.detail.feature.Status == feature.StatusPublished || (m.detail.feature.Status == feature.StatusCodeReady && !m.detail.feature.Checkpoints.AutoPublish())) {
			if hasRunningRefactorCycle(m.detail.feature) {
				m.statusMessage = "A refactor cycle is already running on this feature"
				m.statusTime = time.Now()
				return m, nil
			}
			if len(m.detail.feature.Repos) > 1 && m.detail.feature.Status != feature.StatusPublished {
				m.statusMessage = "Multi-repo refactor is only supported for Published features"
				m.statusTime = time.Now()
				return m, nil
			}
			return m, m.dispatchCycleKey(m.detail.feature.ID, feature.CycleRefactor)
		}
		return m, nil

	case key.Matches(msg, keys.EditConfig):
		f := m.detail.feature
		if f != nil && isFeatureQuiescent(f) {
			cat := BuildPhaseModelCatalog(m.registry, m.featureManager.Config.Defaults)
			m.editConfig = NewEditConfigModel(f, cat, f.IsPublishable())
			m.editConfigActive = true
			return m, nil
		}
		if f != nil {
			m.statusMessage = "Config can only be edited when the feature is idle"
			m.statusTime = time.Now()
			return m, nil
		}
		return m, nil

	case key.Matches(msg, keys.MergeLocal):
		if m.detail.feature != nil && m.detail.feature.Status == feature.StatusCodeReady && !m.detail.feature.IsPublishable() {
			m.statusMessage = "Merging " + m.detail.feature.Slug + " to base branch..."
			m.statusTime = time.Now()
			return m, m.mergeLocalCmd(m.detail.feature.ID)
		}
		return m, nil

	case key.Matches(msg, keys.MarkDone):
		if m.detail.feature != nil && (m.detail.feature.Status == feature.StatusPublished || (m.detail.feature.Status == feature.StatusCodeReady && !m.detail.feature.IsPublishable())) {
			if hasActiveRepoCycles(m.detail.feature) {
				m.statusMessage = "Cannot mark done while repo cycles are active"
				m.statusTime = time.Now()
				return m, nil
			}
			return m, m.markDoneCmd(m.detail.feature.ID)
		}
		return m, nil

	case key.Matches(msg, keys.Publish):
		if m.detail.feature != nil && m.detail.feature.Status == feature.StatusCodeReady && !m.detail.feature.Checkpoints.AutoPublish() && m.detail.feature.IsPublishable() {
			return m.transitionToPublish(m.detail.feature.ID)
		}
		return m, nil

	case key.Matches(msg, keys.ManualPublish):
		if m.detail.feature != nil && m.detail.feature.Status == feature.StatusCodeReady {
			return m.confirmManualPublish(m.detail.feature.ID, m.detail.feature.Name), nil
		}
		return m, nil

	case key.Matches(msg, keys.Delete):
		if m.detail.feature != nil {
			if hasActiveRepoCycles(m.detail.feature) {
				m.statusMessage = "Stop active repo cycles before deleting"
				m.statusTime = time.Now()
				return m, nil
			}
			return m.confirmDelete(m.detail.feature.ID, m.detail.feature.Name), nil
		}
		return m, nil

	default:
		m.detail, _ = m.detail.Update(msg)
	}
	return m, nil
}

func (m AppModel) updateWizard(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.wizard, cmd = m.wizard.Update(msg)
	m = m.handleWizardBrowseResult()
	m = m.handleWizardCreateRepoResult()

	if m.wizard.IsCancelled() {
		return m.transitionTo(ViewDashboard, ""), nil
	}

	if m.wizard.IsDone() {
		result := m.wizard.Result()
		if result != nil {
			m.wizardCreating = true
			m.wizardCreatingName = result.Name
			m.dashboard.creatingName = result.Name
			return m.transitionTo(ViewDashboard, ""), m.createFeatureCmd(result)
		}
		return m.transitionTo(ViewDashboard, ""), nil
	}

	return m, cmd
}

func (m AppModel) updatePublish(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.publish, cmd = m.publish.Update(msg)

	if m.publish.IsDone() {
		// Only mark the whole feature as Published for single-repo / legacy flows.
		// Multi-repo flows manage per-repo state via publishExecuteResultMsg handler.
		if m.publish.prURL != "" && !m.publish.hasRepoSelect {
			_ = m.orchestrator.MarkPublished(m.publish.featureID, m.publish.prURL)
		}
		return m.transitionTo(ViewDashboard, ""), func() tea.Msg { return RefreshFeaturesMsg{} }
	}

	return m, cmd
}

func (m AppModel) updateRecovery(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.recovery, cmd = m.recovery.Update(msg)

	if m.recovery.IsDone() {
		// Execute against the exact items the user reviewed. The scan already
		// ran at view entry (NewAppModel) and RecoveryModel captured the
		// slice; rescanning here would apply the user's action map to a
		// possibly-changed set of orphans (iteration 12 reviewer finding #2).
		items := m.recovery.Items()
		portItems := make([]ports.RecoveryItem, len(items))
		for i, it := range items {
			portItems[i] = it
		}
		actions := m.recovery.Actions()
		_ = m.orchestrator.ExecuteRecovery(context.Background(), portItems, actions)

		return m.transitionTo(ViewDashboard, ""), func() tea.Msg { return RefreshFeaturesMsg{} }
	}

	return m, cmd
}

func (m AppModel) transitionTo(view View, featureID string) AppModel {
	m.currentView = view
	switch view {
	case ViewDashboard:
		features, _ := m.featureManager.List()
		m.dashboard.SetFeatures(features)
	case ViewDetail:
		if featureID != "" {
			f, err := m.featureManager.Get(featureID)
			if err == nil {
				m.detail = NewDetailModel(f, m.featureManager.Store.BaseDir)
				m.detail.width = m.width
				m.detail.height = m.height
			}
		}
	}
	return m
}

func (m AppModel) transitionToWizard() AppModel {
	config.DiscoverReposFromRoots(m.featureManager.Config)
	allRepos := config.AllRepos(m.featureManager.Config)

	var availRepos []string
	repoPaths := make(map[string]string)
	for name, rc := range allRepos {
		availRepos = append(availRepos, name)
		repoPaths[name] = rc.Path
	}
	sort.Strings(availRepos)
	cat := BuildPhaseModelCatalog(m.registry, m.featureManager.Config.Defaults)
	// Build existing slugs map for duplicate detection
	existingSlugs := make(map[string]string)
	if features, err := m.featureManager.List(); err == nil || feature.IsPartialLoadError(err) {
		for _, f := range features {
			existingSlugs[f.Slug] = f.Name
		}
	}
	m.wizard = NewWizardModel(availRepos, repoPaths, allRepos, m.featureManager.Config.Defaults, m.workspaceDir, cat.ProviderModels, cat.ProviderOrder, cat.PhaseDefaults, cat.PhaseProviderModels, existingSlugs, m.featureManager.Config.WorkspaceRoots)
	if m.width > 0 {
		m.wizard.SetWidth(m.width)
	}
	if m.height > 0 {
		m.wizard.height = m.height
	}
	m.currentView = ViewWizard
	return m
}

func (m AppModel) transitionToPublish(featureID string) (AppModel, tea.Cmd) {
	f, err := m.featureManager.Get(featureID)
	if err != nil {
		return m, nil
	}
	if !f.IsPublishable() {
		return m, nil
	}

	// Read plan text for PR description generation. Prefer roadmap (covers all
	// phases) over the per-phase plan artifact, which is overwritten each phase
	// and ends up pointing only at the last phase for multi-phase features.
	planText := m.resolvePhaseArtifact(f, "roadmap")
	if planText == "" {
		planText = m.resolvePhaseArtifact(f, "plan")
	}
	descModel := f.Models.Planning

	// Single-repo only: commit any uncommitted changes so they appear in
	// the publish diff. Multi-repo handles this in autoPublishRepoCmd /
	// executePublish (publish.go) so the per-repo flow already covers it.
	// This is a SEMANTIC decision (commit timing for the publish UI's diff
	// display), not a chrome decision — we route through the chrome
	// predicate only because the timing happens to align with the chrome
	// boundary (single-repo commits at construction time; multi-repo
	// commits per repo at execute time).
	if len(f.Repos) <= 1 {
		_ = m.orchestrator.CommitUncommittedForPublish(featureID)
		f, _ = m.featureManager.Get(featureID)
	}

	var entries []publishRepoEntry
	for _, repo := range f.Repos {
		entry := publishRepoEntry{
			Name:        repo.Name,
			Branch:      repo.Branch,
			WorktreeDir: repo.WorktreePath,
			RepoPath:    repo.Path,
			BaseBranch:  repo.BaseBranch,
			PRStatus:    "pending",
		}
		if entry.WorktreeDir == "" {
			entry.WorktreeDir = repo.Path
		}
		if entry.Branch == "" {
			entry.Branch = "feature/" + f.Slug
		}
		// Stacked-PR base branch: use the repo's BaseBranch when it differs
		// from the default. Multi-repo path leaves BaseBranch as-is (each
		// repo carries its own); the constructor copies it into m.baseBranch
		// during the auto-select branch.
		if repo.BaseBranch != "" {
			defaultBranch := git.DefaultBranch(repo.Path)
			if repo.BaseBranch == defaultBranch {
				entry.BaseBranch = ""
			}
		}
		if state, ok := f.RepoStates[repo.Name]; ok && state != nil {
			if state.PRURL != "" {
				entry.PRStatus = "published"
				entry.PRURL = state.PRURL
			}
			if state.LastError != "" {
				entry.PRStatus = "failed"
			}
		}
		entries = append(entries, entry)
	}

	m.publish = NewPublishModel(
		f, entries, planText, descModel,
		m.width, m.height,
	)
	m.publish.runDesc = m.phaseRunner.RunDescriptionGeneration

	// PR context shape differs by chrome — single-repo populates CommitBodies
	// + DiffStat from the worktree (the constructor's auto-select branch
	// loaded the diff already, so the worktree dir is on m.publish.worktreeDir);
	// multi-repo leaves them empty because per-repo execute fills them at
	// publish time.
	if len(f.Repos) > 1 {
		m.publish.prCtx = agent.PRContext{
			FeatureName:        f.Name,
			FeatureDescription: f.Description,
			Roadmap:            planText,
		}
	} else {
		m.publish.prCtx = buildPublishPRContext(f, m.publish.worktreeDir, planText)
	}

	m.publish.publishable = f.IsPublishable()
	m.currentView = ViewPublish
	return m, nil
}

// buildPublishPRContext assembles the lean PRContext passed to the
// description-generation agent for the interactive single-repo publish path.
// Commit bodies + diff stat are fetched from the worktree; git errors are
// tolerated (empty strings still produce a usable prompt and fallback body).
func buildPublishPRContext(f *feature.Feature, worktreeDir, planText string) agent.PRContext {
	prCtx := agent.PRContext{
		Roadmap: planText,
	}
	if f != nil {
		prCtx.FeatureName = f.Name
		prCtx.FeatureDescription = f.Description
	}
	if worktreeDir == "" {
		return prCtx
	}
	baseBranch := ""
	if f != nil && len(f.Repos) > 0 {
		baseBranch = f.Repos[0].BaseBranch
	}
	if bodies, err := git.CommitBodies(worktreeDir, baseBranch); err == nil {
		prCtx.CommitBodies = bodies
	}
	if stat, err := git.DiffStat(worktreeDir, baseBranch); err == nil {
		prCtx.DiffStat = stat
	}
	return prCtx
}

// buildCrossRefEntries builds CrossRefEntry slice from the feature's current state,
// incorporating the just-published repo's URL.
func buildCrossRefEntries(f *feature.Feature, justPublishedRepo, justPublishedURL string) []git.CrossRefEntry {
	var entries []git.CrossRefEntry
	for _, repo := range f.Repos {
		entry := git.CrossRefEntry{
			RepoName: repo.Name,
			Branch:   repo.Branch,
		}
		if repo.Name == justPublishedRepo {
			entry.PRURL = justPublishedURL
		} else if state, ok := f.RepoStates[repo.Name]; ok && state != nil {
			entry.PRURL = state.PRURL
			if state.LastError != "" {
				entry.PRURL = "(failed)"
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

// Feature creation commands

func (m AppModel) createFeatureCmd(result *WizardResult) tea.Cmd {
	return func() tea.Msg {
		var featureIDs []string
		riskLevel := feature.RiskLevel(result.RiskLevel)
		projection := result.Pipeline.ProjectGates(result.Checkpoints, true)
		opts := feature.CreateOptions{
			UseCurrentBranch:        result.UseCurrentBranch,
			UseCurrentBranchPerRepo: result.UseCurrentBranchPerRepo,
			Checkpoints:             projection.Checkpoints,
			Attachments:             result.Attachments,
			RiskLevel:               riskLevel,
			Pipeline:                result.Pipeline,
		}

		// Route creation through the orchestrator so its post-creation hook
		// (e.g., permission.ImportRepoSettings) fires and the FeatureCreated
		// event is emitted on the orchestrator bus.
		f, err := m.orchestrator.CreateFeature(
			result.Name, result.Description, result.Repos,
			result.Models, result.ExitCriteria, result.Inquireness, result.Images, opts,
		)
		if err != nil {
			return FeatureCreatedMsg{Err: err}
		}
		featureIDs = append(featureIDs, f.ID)

		m.persistPipelinePreferences(result.Repos, result.Pipeline, result.Models, feature.Inquireness(result.Inquireness), projection.Checkpoints, true)

		return FeatureCreatedMsg{FeatureIDs: featureIDs}
	}
}

func (m AppModel) persistPipelinePreferences(repos []string, pipeline feature.PipelineProfile, models config.ModelConfig, inquireness feature.Inquireness, checkpoints feature.Checkpoints, publishable bool) {
	if m.featureManager.Config == nil {
		return
	}
	if m.featureManager.Config.Defaults.PipelinePreferences == nil {
		m.featureManager.Config.Defaults.PipelinePreferences = make(map[string]config.PipelinePreference)
	}
	projection := pipeline.ProjectGates(checkpoints, publishable)
	configGates := feature.FeatureCheckpointsToConfig(projection.Checkpoints)
	profileKey := string(pipeline)
	pref := config.PipelinePreference{
		Models:      models,
		Inquireness: string(inquireness),
	}
	m.featureManager.Config.Defaults.PipelinePreferences[profileKey] = pref
	for _, repoName := range repos {
		rc := m.featureManager.Config.Repos[repoName]
		if rc.PipelineGates == nil {
			rc.PipelineGates = make(map[string]config.Checkpoints)
		}
		rc.PipelineGates[profileKey] = configGates
		m.featureManager.Config.Repos[repoName] = rc
	}
	_ = config.Save(m.configPath, m.featureManager.Config)
}

func (m AppModel) generateSummaryCmd(featureID string) tea.Cmd {
	mgr := m.featureManager
	return func() tea.Msg {
		f, err := mgr.Get(featureID)
		if err != nil || f == nil || f.Description == "" {
			return featureSummaryMsg{}
		}
		summary, err := m.phaseRunner.RunSummaryGeneration(context.Background(), f.Name, f.Description)
		if err != nil {
			return featureSummaryMsg{}
		}
		return featureSummaryMsg{featureID: featureID, summary: summary}
	}
}

// Phase execution commands

// computeKBInfos returns KB info for all repos in a feature.
func (m AppModel) computeKBInfos(f *feature.Feature) []agent.KBInfo {
	var infos []agent.KBInfo
	for _, repo := range f.Repos {
		kbDir := agent.KBStateDir(m.featureManager.Store.BaseDir, repo.Name)
		indexPath := agent.KBPath(kbDir)
		if _, err := os.Stat(indexPath); err == nil {
			infos = append(infos, agent.KBInfo{Name: repo.Name, IndexPath: indexPath, RootDir: kbDir})
		}
	}
	return infos
}

// startFirstPhaseCmd returns the tea.Cmd to start the first phase for a newly
// created feature. Thin delegate to orchestrator.StartFeature, which reads the
// effective pipeline, applies any pre-transitions (e.g. Medium Created →
// PlanReady), fires OnFeatureStarted, and dispatches to the correct internal
// starter. The Update-dispatched handler stays free of Store.Modify.
func (m AppModel) startFirstPhaseCmd(featureID string) tea.Cmd {
	return func() tea.Msg {
		_ = m.orchestrator.StartFeature(featureID)
		return RefreshFeaturesMsg{}
	}
}

// Delete commands

func (m AppModel) confirmManualPublish(featureID, featureName string) AppModel {
	m.manualPublishConfirmActive = true
	m.manualPublishFeatureID = featureID
	m.manualPublishFeatureName = featureName
	return m
}

// publishCmd dispatches the publish pipeline via orchestrator.Publish. The
// orchestrator owns per-repo fan-out, pull-rebase, push, PR creation, and
// conflict-to-PublishConflictError conversion; the TUI only forwards the
// result back to the Update loop as OrchPublishCompletedMsg so conflicts can
// be routed into the rebase-resolution UX.
//
// The up-front publishability / AutoPublish guards are evaluated inside the
// tea.Cmd closure so the Update dispatch stays a single delegate call; the
// TryCompletePublish fallback handles the case where every repo was already
// published (e.g. Init-time crash resume).
func (m AppModel) publishCmd(featureID string) tea.Cmd {
	return func() tea.Msg {
		f, err := m.featureManager.Get(featureID)
		if err != nil || !f.IsPublishable() || !f.Checkpoints.AutoPublish() {
			return RefreshFeaturesMsg{}
		}
		if f.AllReposPublished() {
			_, _ = m.orchestrator.TryCompletePublish(featureID)
			return RefreshFeaturesMsg{}
		}
		// Publish owns PublishCompleted event emission. Returning another
		// OrchPublishCompletedMsg here would process conflicts twice.
		_ = m.orchestrator.Publish(featureID)
		return RefreshFeaturesMsg{}
	}
}

// startFeatureCmd is a thin delegate: delegates to orchestrator.StartFeature,
// which dispatches to the correct internal starter using CurrentPhase.
func (m AppModel) startFeatureCmd(featureID string) tea.Cmd {
	return func() tea.Msg {
		_ = m.orchestrator.StartFeature(featureID)
		return RefreshFeaturesMsg{}
	}
}

func (m AppModel) manualPublishCmd(featureID string) tea.Cmd {
	return func() tea.Msg {
		_ = m.orchestrator.MarkDone(featureID)
		return RefreshFeaturesMsg{}
	}
}

func (m AppModel) confirmDelete(featureID, featureName string) AppModel {
	m.deleteConfirmActive = true
	m.deleteFeatureID = featureID
	m.deleteFeatureName = featureName
	return m
}

func (m AppModel) deleteFeatureCmd(featureID string) tea.Cmd {
	return func() tea.Msg {
		err := m.orchestrator.Delete(featureID)
		return DeleteFeatureDoneMsg{FeatureID: featureID, Err: err}
	}
}

func (m AppModel) confirmStop(featureID, featureName string) AppModel {
	m.stopConfirmActive = true
	m.stopConfirmFeatureID = featureID
	m.stopConfirmFeatureName = featureName
	return m
}

func (m AppModel) stopFeatureCmd(featureID string) tea.Cmd {
	return func() tea.Msg {
		// Published features with interactive tweak-only cycles cannot be
		// resumed autonomously, so Stop clears the cycle and returns to the
		// published baseline. Non-interactive cycles such as rebase go through
		// InterruptFeature so the feature lands in Interrupted and [r] can
		// relaunch the saved cycle plan.
		if f, err := m.featureManager.Get(featureID); err == nil &&
			f.Status == feature.StatusPublished &&
			hasActiveRepoCycles(f) &&
			!hasInterruptibleRepoCycles(f) {
			if m.orchestrator != nil {
				m.orchestrator.StopFeatureSessions(featureID)
				_ = m.orchestrator.ClearRepoCycles(featureID)
			}
			return RefreshFeaturesMsg{}
		}

		// Normal path: delegate to the orchestrator. It owns session stop,
		// pending help/permission cleanup, and whether the feature-level
		// status should become Interrupted for this kind of work.
		if m.orchestrator != nil {
			_ = m.orchestrator.InterruptFeature(featureID)
		}
		return RefreshFeaturesMsg{}
	}
}

func (m AppModel) rewindCmd(featureID string, targetPhase feature.Phase, roadmapPhase int) tea.Cmd {
	return func() tea.Msg {
		// Stop any active sessions before the rewind so orphaned agents cannot
		// race the store reset. The orchestrator owns session walk semantics.
		if m.orchestrator != nil {
			m.orchestrator.StopFeatureSessions(featureID)
		}

		var (
			warns           []string
			effectiveTarget feature.Phase
			err             error
		)
		if roadmapPhase > 0 {
			warns, effectiveTarget, err = m.orchestrator.RewindWithRequest(featureID, feature.RewindRequest{
				TargetPhase:  targetPhase,
				RoadmapPhase: roadmapPhase,
			})
		} else {
			warns, effectiveTarget, err = m.orchestrator.RewindToPhase(featureID, targetPhase)
		}
		return RewindDoneMsg{FeatureID: featureID, TargetPhase: effectiveTarget, Warnings: warns, Err: err}
	}
}

func (m AppModel) confirmResumeAll() AppModel {
	count := 0
	features, _ := m.featureManager.List()
	for _, f := range features {
		if f.Status == feature.StatusInterrupted || f.Status == feature.StatusFailed {
			count++
		}
	}
	m.resumeAllConfirmActive = true
	m.resumeAllCount = count
	return m
}

func (m AppModel) resumeAllCmd() tea.Cmd {
	return func() tea.Msg {
		features, err := m.featureManager.List()
		if err != nil && !feature.IsPartialLoadError(err) {
			return RefreshFeaturesMsg{}
		}

		maxIterationsDelta, maxPlanIterationsDelta := m.restartBudgetDeltas()
		for _, f := range features {
			if f.Status != feature.StatusInterrupted && f.Status != feature.StatusFailed {
				continue
			}

			outcome, err := m.orchestrator.RestartPhase(f.ID, maxIterationsDelta, maxPlanIterationsDelta)
			if err != nil {
				continue
			}
			m.sendRestartOutcome(f.ID, outcome)
		}

		return RefreshFeaturesMsg{}
	}
}

func (m AppModel) resumeAllConfirmModal() string {
	var c strings.Builder
	c.WriteString("\n")
	if m.resumeAllCount > 0 {
		c.WriteString(fmt.Sprintf("  %d interrupted/failed feature(s) will be resumed.\n", m.resumeAllCount))
	} else {
		c.WriteString(MutedStyle.Render("  No interrupted or failed features to resume."))
		c.WriteString("\n")
	}

	panelWidth := 56
	contentBox := panelStyle(true).
		Width(panelWidth).
		BorderForeground(colorInfo).
		Render(c.String())
	contentBox = renderBorderTitle(contentBox, "Resume All", lipgloss.NewStyle().Foreground(colorInfo))

	footer := KeyHelpStyle.Render(" [y] Confirm   [any key] Cancel")
	return contentBox + "\n" + footer
}

// mergeLocalCmd merges the feature branch into the local base branch for
// non-publishable features. Thin delegate to orchestrator.MergeFeatureLocal so
// the TUI does not call git mutators or featureManager directly.
func (m AppModel) mergeLocalCmd(featureID string) tea.Cmd {
	return func() tea.Msg {
		err := m.orchestrator.MergeFeatureLocal(featureID)
		return MergeLocalResultMsg{FeatureID: featureID, Err: err}
	}
}

// handleRebaseResult processes the result of a rebase attempt.
func (m AppModel) handleRebaseResult(msg RebaseResultMsg) (tea.Model, tea.Cmd) {
	if msg.Err != nil {
		m.statusMessage = "\u2717 Rebase failed: " + msg.Err.Error()
		m.statusTime = time.Now()
		m.logPhaseError(msg.FeatureID, "rebase", msg.Err.Error())
		return m, func() tea.Msg { return RefreshFeaturesMsg{} }
	}

	if msg.Success {
		f, err := m.featureManager.Get(msg.FeatureID)
		if err == nil && f.IsPublishable() {
			m.statusMessage = "\u2713 Rebase complete — force pushed to origin"
		} else {
			m.statusMessage = "\u2713 Rebase complete"
		}
		m.statusTime = time.Now()
		return m, func() tea.Msg { return RefreshFeaturesMsg{} }
	}

	return m, func() tea.Msg { return RefreshFeaturesMsg{} }
}

// rebaseCmd is a thin delegate to orchestrator.StartRebase.
// The feature stays StatusPublished; orchestrator owns git operations and
// returns a conflict sentinel for the TUI to route to conflict resolution.
func (m AppModel) rebaseCmd(featureID, repoName string) tea.Cmd {
	return func() tea.Msg {
		err := m.orchestrator.StartRebase(featureID, repoName)
		if err == nil {
			return RebaseRepoCycleResultMsg{FeatureID: featureID, RepoName: repoName, Success: true}
		}
		var conflict *orchestrator.RebaseConflictError
		if errors.As(err, &conflict) {
			return RebaseRepoCycleResultMsg{
				FeatureID:     featureID,
				RepoName:      repoName,
				HasConflict:   true,
				RebaseTarget:  conflict.RebaseTarget,
				ConflictFiles: conflict.ConflictFiles,
			}
		}
		return RebaseRepoCycleResultMsg{FeatureID: featureID, RepoName: repoName, Err: err}
	}
}

// handleRebaseRepoCycleResult processes the result of a per-repo rebase.
func (m AppModel) handleRebaseRepoCycleResult(msg RebaseRepoCycleResultMsg) (tea.Model, tea.Cmd) {
	if msg.Err != nil {
		m.statusMessage = "\u2717 Rebase " + msg.RepoName + ": " + msg.Err.Error()
		m.statusTime = time.Now()
		return m, func() tea.Msg { return RefreshFeaturesMsg{} }
	}
	if msg.Success {
		m.statusMessage = "\u2713 Rebase " + msg.RepoName + " complete — force pushed"
		m.statusTime = time.Now()
		return m, func() tea.Msg { return RefreshFeaturesMsg{} }
	}
	if msg.HasConflict {
		m.statusMessage = "\u26a0 Rebase " + msg.RepoName + " conflicts — starting resolution..."
		m.statusTime = time.Now()
		return m, m.startRepoCycleImplementCmd(msg.FeatureID, msg.RepoName, feature.CycleRebase, msg.RebaseTarget, msg.ConflictFiles...)
	}
	return m, func() tea.Msg { return RefreshFeaturesMsg{} }
}

// startRepoCycleImplementCmd is a thin delegate to orchestrator.StartRepoCycleImplement.
// The orchestrator owns cycle-start, plan write, implement loop spawn, and the
// HandleRepoCycleLoopDone → final-review chain. conflictFiles is forwarded to
// the orchestrator so the rebase-conflict path can emit the
// "rebase-already-in-progress" plan template instead of restarting from
// scratch.
func (m AppModel) startRepoCycleImplementCmd(featureID, repoName string, cycleType feature.RepoCycleType, extra string, conflictFiles ...string) tea.Cmd {
	return func() tea.Msg {
		_, err := m.orchestrator.StartRepoCycleImplement(featureID, repoName, cycleType, extra, conflictFiles...)
		if err != nil {
			return repoCycleStartResultMsg{
				FeatureID: featureID,
				RepoName:  repoName,
				CycleType: cycleType,
				Err:       err,
			}
		}
		return RefreshFeaturesMsg{}
	}
}

// handleRepoCycleLoopDone is a thin delegate that forwards the loop result to
// the orchestrator. The orchestrator dispatches to Final Review or FailRepoCycle.
func (m AppModel) handleRepoCycleLoopDone(msg RepoCycleLoopDoneMsg) (tea.Model, tea.Cmd) {
	_ = m.orchestrator.HandleRepoCycleLoopDone(msg.FeatureID, orchestrator.RepoCycleLoopResultInput{
		RepoName: msg.RepoName,
		Result:   msg.Result,
	})
	return m, func() tea.Msg { return RefreshFeaturesMsg{} }
}

// startCycleFinalReviewCmd is a thin delegate to orchestrator.StartCycleFinalReview.
// Post-publish cycle Final Review is feature-level: every Feature.Repos
// worktree is reviewed atomically. The orchestrator owns the review loop
// dispatch, per-repo CompleteRepoCycle / FailRepoCycle completion, and the
// TweakReviewApproved event emission for tweak cycles.
func (m AppModel) startCycleFinalReviewCmd(featureID string) tea.Cmd {
	return func() tea.Msg {
		_ = m.orchestrator.StartCycleFinalReview(featureID)
		return RefreshFeaturesMsg{}
	}
}

// Repo-cycle completion (rebase/tweak/review-comments) is owned by
// orchestrator.CompleteRepoCycle, which is invoked from the post-cycle FR
// done handler. No per-type TUI wrappers are needed.

// startRefactorCmd is a thin delegate to orchestrator.StartRefactorCycle.
// The orchestrator owns refactor-count bump, cycle start, RefactorLoop spawn,
// and HandleRefactorCycleLoopDone → CompleteRefactorRepoCycle dispatch.
func (m AppModel) startRefactorCmd(featureID, repoName, prompt string) tea.Cmd {
	return func() tea.Msg {
		_, _ = m.orchestrator.StartRefactorCycle(featureID, repoName, prompt)
		return RefreshFeaturesMsg{}
	}
}

// restartRepoCycleRefactorCmd is a thin delegate to orchestrator.RestartRefactorCycle.
// The orchestrator reuses the existing refactor directory and count from the
// prior attempt.
func (m AppModel) restartRepoCycleRefactorCmd(featureID, repoName, prompt string) tea.Cmd {
	return func() tea.Msg {
		_, _ = m.orchestrator.RestartRefactorCycle(featureID, repoName, prompt)
		return RefreshFeaturesMsg{}
	}
}

// startReviewCommentsRepoCycleFromView starts a per-repo review comments cycle
// from the review comments view (after the user has seen the comments and chosen address/auto).
func (m AppModel) startReviewCommentsRepoCycleFromView(featureID, repoName string, comments []git.ReviewComment, mode string) tea.Cmd {
	return func() tea.Msg {
		f, err := m.featureManager.Get(featureID)
		if err != nil {
			return RefreshFeaturesMsg{}
		}

		baseDir := m.featureManager.Store.BaseDir

		if err := agent.SaveReviewCommentsForRepo(baseDir, f, repoName, agent.ReviewCommentsData{
			Mode:     mode,
			Comments: comments,
		}); err != nil {
			return repoCycleStartResultMsg{
				FeatureID: featureID,
				RepoName:  repoName,
				CycleType: feature.CycleReviewComments,
				Err:       fmt.Errorf("save review comments: %w", err),
			}
		}

		commentsDir := filepath.Join(agent.ActiveRunDir(baseDir, f), "review-comments", repoName)

		// Build the review plan
		prURL := ""
		if state, ok := f.RepoStates[repoName]; ok && state != nil {
			prURL = state.PRURL
		}
		planContent := agent.BuildReviewCommentsPlan(comments, prURL, mode, filepath.Join(commentsDir, "review-resolutions.json"))

		// Start the cycle via the shared implementation command
		return m.startRepoCycleImplementCmd(featureID, repoName, feature.CycleReviewComments, planContent)()
	}
}

// fetchReviewCommentsCmd fetches review comments for a specific repo's PR.
func (m AppModel) fetchReviewCommentsCmd(featureID, repoName string) tea.Cmd {
	return func() tea.Msg {
		f, err := m.featureManager.Get(featureID)
		if err != nil {
			return ReviewCommentsFetchedMsg{FeatureID: featureID, Err: err}
		}

		state, ok := f.RepoStates[repoName]
		if !ok || state == nil || state.PRURL == "" {
			return ReviewCommentsFetchedMsg{FeatureID: featureID, Err: fmt.Errorf("no PR URL for %s", repoName)}
		}

		// Find repo path
		repoPath := ""
		for _, r := range f.Repos {
			if r.Name == repoName {
				repoPath = r.Path
				break
			}
		}

		comments, err := git.FetchPRComments(repoPath, state.PRURL)
		if err != nil {
			return ReviewCommentsFetchedMsg{FeatureID: featureID, Err: fmt.Errorf("fetching comments for %s: %w", repoName, err)}
		}

		// Tag comments with repo name
		for i := range comments {
			comments[i].RepoName = repoName
		}

		addressed, _ := agent.LoadAddressedIDsForRepo(m.featureManager.Store.BaseDir, f, repoName)
		if len(addressed) > 0 {
			filtered := comments[:0]
			for _, c := range comments {
				if !addressed[c.ID] {
					filtered = append(filtered, c)
				}
			}
			comments = filtered
		}

		return ReviewCommentsFetchedMsg{
			FeatureID:   featureID,
			FeatureSlug: f.Slug,
			Comments:    comments,
		}
	}
}

// updateReviewComments handles key events in the review comments view.
func (m AppModel) updateReviewComments(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, keys.Back) || key.Matches(msg, keys.Quit):
		return m.transitionTo(ViewDashboard, ""), nil

	case key.Matches(msg, keys.AutoAddressReview):
		// Auto — agent decides
		if len(m.reviewComments.comments) > 0 {
			if f, err := m.featureManager.Get(m.reviewComments.featureID); err == nil {
				repoName := m.reviewComments.comments[0].RepoName
				if repoName == "" && len(f.Repos) > 0 {
					repoName = f.Repos[0].Name
				}
				if repoName == "" {
					return m, func() tea.Msg { return RefreshFeaturesMsg{} }
				}
				m.currentView = ViewDashboard
				m.statusMessage = "Auto-addressing review comments for " + repoName + "..."
				m.statusTime = time.Now()
				return m, m.startReviewCommentsRepoCycleFromView(m.reviewComments.featureID, repoName, m.reviewComments.comments, "auto")
			}
			return m, func() tea.Msg { return RefreshFeaturesMsg{} }
		}

	default:
		var cmd tea.Cmd
		m.reviewComments, cmd = m.reviewComments.Update(msg)
		return m, cmd
	}
	return m, nil
}

// isTweakSessionID returns true when the session ID belongs to an interactive tweak session.
func isTweakSessionID(sessionID string) bool {
	return strings.HasSuffix(sessionID, "-impl-tweak") ||
		(strings.Contains(sessionID, "-impl-") && strings.HasSuffix(sessionID, "-tweak"))
}

// startInteractiveTweakCmd delegates to orchestrator.StartTweak and
// attaches on success. Tweak is feature-level; repoName is ignored at the
// orchestrator boundary and kept on this signature for compatibility with the
// existing TUI dispatch shape, which hands a per-repo selection through.
func (m AppModel) startInteractiveTweakCmd(featureID, _ string) tea.Cmd {
	return func() tea.Msg {
		sessionID, err := m.orchestrator.StartTweak(featureID)
		if err != nil {
			return RefreshFeaturesMsg{}
		}
		if m.programRef != nil && m.programRef.P != nil {
			m.programRef.P.Send(attachTweakSessionMsg{featureID: featureID, sessionID: sessionID})
		}
		return RefreshFeaturesMsg{}
	}
}

// attachTweakSessionMsg is sent to the TUI to attach to a newly-created tweak session.
type attachTweakSessionMsg struct {
	featureID string
	sessionID string
}

// handleTweakSessionDone processes the completion of an interactive tweak session.
// Every return path must batch listenForEvents() to preserve the event pump.
//
// The tweak cycle is feature-level. The session's RepoName, if any, is
// diagnostic; the completion flow runs against Feature.ActiveCycle and then
// walks every Feature.Repos entry. The per-repo RepoCycles guard is preserved
// as a defensive check that some tweak cycle is open before completion.
func (m AppModel) handleTweakSessionDone(featureID, _ string, sess session.SessionView, success bool) (tea.Model, tea.Cmd) {
	_ = sess // diagnostic only under the unified flow.
	f, err := m.featureManager.Get(featureID)
	if err != nil {
		return m, m.listenForEvents()
	}
	if !hasTweakCycle(f) {
		return m, m.listenForEvents()
	}

	// Guard: a prior completion signal is already being processed for this
	// feature (async commit in flight). Ignore duplicate signals so that
	// rapid Ctrl+D doesn't interrupt the in-flight completion path.
	guardKey := featureID
	if m.tweakCompletingFeatureID == guardKey {
		return m, m.listenForEvents()
	}

	// Explicit finish intent (Ctrl+D) overrides process exit failure
	effectiveSuccess := success || m.tweakFinishingFeatureID == featureID
	m.tweakFinishingFeatureID = ""

	if !effectiveSuccess {
		_ = m.orchestrator.FailTweakSession(featureID)
		return m, tea.Batch(
			func() tea.Msg { return RefreshFeaturesMsg{} },
			m.listenForEvents(),
		)
	}

	// Mark completion in flight so duplicate signals are ignored until the
	// flow resolves (commit → modal → finish or error).
	m.tweakCompletingFeatureID = guardKey

	return m, tea.Batch(
		m.completeTweakCommitCmd(featureID),
		m.listenForEvents(),
	)
}

// hasTweakCycle returns true when the feature has a tweak cycle open
// (either via Feature.ActiveCycle under the unified flow, or via the
// legacy RepoCycles map for backward compatibility).
func hasTweakCycle(f *feature.Feature) bool {
	if f == nil {
		return false
	}
	if f.ActiveCycle != nil && f.ActiveCycle.Type == feature.CycleTweak {
		return true
	}
	for _, rc := range f.RepoCycles {
		if rc != nil && rc.Type == feature.CycleTweak {
			return true
		}
	}
	return false
}

// completeTweakCommitCmd delegates to orchestrator.CompleteTweakCommit.
func (m AppModel) completeTweakCommitCmd(featureID string) tea.Cmd {
	return func() tea.Msg {
		hadChanges, err := m.orchestrator.CompleteTweakCommit(featureID)
		return tweakCommitDoneMsg{featureID: featureID, hadChanges: hadChanges, err: err}
	}
}

// handleTweakCommitDone processes the result of the async feature-level
// tweak commit. CompleteTweakCommit handles MarkFailed internally on
// failure so the TUI body only routes between modal display and finish.
func (m AppModel) handleTweakCommitDone(msg tweakCommitDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		// CompleteTweakCommit already fired MarkFailed; the TUI only
		// needs to clear the in-flight guard and refresh.
		m.tweakCompletingFeatureID = ""
		return m, func() tea.Msg { return RefreshFeaturesMsg{} }
	}

	if !msg.hadChanges {
		// No changes — skip review, proceed directly to feature-level finish.
		m.tweakCompletingFeatureID = ""
		return m, m.completeTweakFinishCmd(msg.featureID, "", false)
	}

	// Changes committed — show Final Review modal. The modal body shows
	// only feature-scope text under the unified flow; the repoName field
	// is preserved on the model for the rare legacy fixture that still
	// has a per-repo cycle entry but no ActiveCycle.
	m.tweakReviewModalActive = true
	m.tweakReviewModalFeatureID = msg.featureID
	m.tweakReviewModalRepoName = ""
	// Keep tweakCompletingFeatureID set until modal resolves.
	return m, func() tea.Msg { return RefreshFeaturesMsg{} }
}

// completeTweakFinishCmd delegates to orchestrator.CompleteTweakFinish.
// Pull-rebase conflicts surface as *orchestrator.PublishConflictError
// (carrying the affected repo's name + RebaseTarget) and are routed
// into the per-repo rebase-resolution UX via RebaseRepoCycleResultMsg
// (handleRebaseRepoCycleResult spawns a fresh CycleRebase against the
// conflicted repo). The follow-up rebase plan rebases the feature
// branch ONTO the PR base, so we forward conflict.RebaseTarget. All
// other paths fall through to a dashboard refresh.
//
// repoName is ignored at the orchestrator boundary but kept on this signature
// so modal handlers and existing per-repo dispatch points can share the same
// command. Surfaced conflicts still carry a RepoName because rebase
// resolution is per-repo.
func (m AppModel) completeTweakFinishCmd(featureID, _ string, hadChanges bool) tea.Cmd {
	return func() tea.Msg {
		err := m.orchestrator.CompleteTweakFinish(featureID, hadChanges)
		if err != nil {
			var conflict *orchestrator.PublishConflictError
			if errors.As(err, &conflict) {
				return RebaseRepoCycleResultMsg{
					FeatureID:    featureID,
					RepoName:     conflict.RepoName,
					HasConflict:  true,
					RebaseTarget: conflict.RebaseTarget,
				}
			}
		}
		return RefreshFeaturesMsg{}
	}
}

// restoreTweakFromReviewCmd delegates to orchestrator.RestoreTweakFromReview.
// repoName is ignored at the orchestrator boundary; the parameter remains for
// modal keybinding handlers that still pass a per-repo hint.
func (m AppModel) restoreTweakFromReviewCmd(featureID, _ string) tea.Cmd {
	return func() tea.Msg {
		_ = m.orchestrator.RestoreTweakFromReview(featureID)
		return RefreshFeaturesMsg{}
	}
}

// renderTweakReviewModal renders the Final Review decision modal for tweaks.
func (m AppModel) renderTweakReviewModal() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorBrand)
	normalStyle := lipgloss.NewStyle()

	skipLabel := "skip review and complete"
	if f, err := m.featureManager.Get(m.tweakReviewModalFeatureID); err == nil && len(f.PRURLs()) > 0 {
		skipLabel = "skip review, push changes"
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render("Final Review"))
	b.WriteString("\n\n")
	b.WriteString(normalStyle.Render("Changes have been committed. Run a Final Review?"))
	b.WriteString("\n\n")
	b.WriteString(normalStyle.Render("  [y] Yes — review and fix issues"))
	b.WriteByte('\n')
	b.WriteString(normalStyle.Render("  [n] No  — " + skipLabel))
	b.WriteByte('\n')
	b.WriteByte('\n')
	b.WriteString(MutedStyle.Render("y to review · n to skip · Esc to cancel"))

	menuBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorBrand).
		Padding(1, 2).
		Width(min(m.width-4, 50)).
		Render(b.String())

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, menuBox)
}

// applyRefactorPipelineAndStart sets the pipeline profile for a refactor cycle
// via orchestrator.ApplyRefactorPipeline (which owns the Store.Modify) and
// dispatches to the correct refactor command. Unlike UpgradePipeline, the
// orchestrator method permits downgrade profiles because the refactor flow
// resets the cycle.
func (m AppModel) applyRefactorPipelineAndStart(featureID, repoName, prompt string, profile feature.PipelineProfile) tea.Cmd {
	_ = m.orchestrator.ApplyRefactorPipeline(featureID, profile)
	return m.startRefactorCmd(featureID, repoName, prompt)
}

// renderRefactorPipelineSelector renders a horizontal pipeline selector overlay.
func (m AppModel) renderRefactorPipelineSelector() string {
	var b strings.Builder
	b.WriteString(TitleStyle.Render("Select Pipeline for Refactor"))
	b.WriteString("\n\n")

	var parts []string
	for i, opt := range m.refactorPipelineOptions {
		label := string(opt)
		if i == m.refactorPipelineCursor {
			parts = append(parts, SelectedRowStyle.Render("▸ "+label))
		} else {
			parts = append(parts, "  "+label)
		}
	}
	b.WriteString(strings.Join(parts, "    "))
	b.WriteString("\n\n")

	// Description for the selected profile
	selected := m.refactorPipelineOptions[m.refactorPipelineCursor]
	switch selected {
	case feature.PipelineMedium:
		b.WriteString(MutedStyle.Render("Skip research — go straight to planning"))
	case feature.PipelineLarge:
		b.WriteString(MutedStyle.Render("Inquiry + research + planning"))
	case feature.PipelineMoonshot:
		b.WriteString(MutedStyle.Render("Full pipeline with all gates enabled"))
	}
	b.WriteString("\n\n")
	b.WriteString(KeyHelpStyle.Render(" [←/→] Navigate   [enter] Confirm   [esc] Cancel"))

	return b.String()
}

// markDoneCmd transitions a Published feature to Done.
func (m AppModel) markDoneCmd(featureID string) tea.Cmd {
	return func() tea.Msg {
		_ = m.orchestrator.MarkDone(featureID)
		return RefreshFeaturesMsg{}
	}
}

// Human-in-the-loop commands

func (m AppModel) approvePermissionsCmd(featureID string) tea.Cmd {
	return func() tea.Msg {
		sessions := m.sessionManager.ActiveSessions()
		for _, s := range sessions {
			if s.FeatureID() == featureID &&
				(s.Status() == session.SessionWaitingPermission || s.Status() == session.SessionWaitingHelp) {
				// Use structured control response if a pending request exists
				if s.LastControlRequest() != nil {
					_ = s.RespondToControl(s.LastControlRequest().RequestID, true, "")
				}
				s.ResetWaitingStatus()
			}
		}

		_ = m.featureManager.Store.Modify(featureID, func(f *feature.Feature) error {
			clearPendingHelpByMessage(f, waitingInputHelpMessage)
			for i := range f.PermissionsQueue {
				if f.PermissionsQueue[i].Pending {
					f.PermissionsQueue[i].Pending = false
				}
			}
			return nil
		})
		return RefreshFeaturesMsg{}
	}
}

func (m AppModel) approveAndRememberCmd(featureID string) tea.Cmd {
	return func() tea.Msg {
		sessions := m.sessionManager.ActiveSessions()
		for _, s := range sessions {
			if s.FeatureID() == featureID && s.Status() == session.SessionWaitingPermission {
				lcr := s.LastControlRequest()
				if lcr != nil {
					// Remember before approving
					if m.permissionCache != nil {
						toolName := lcr.Request.ToolName
						toolInput := string(lcr.Request.Input)
						repoName := s.PermCacheScope()
						m.permissionCache.RememberAllow(toolName, toolInput, repoName)
					}
					_ = s.RespondToControl(lcr.RequestID, true, "")
				}
				s.ResetWaitingStatus()
			}
		}

		// Only clear the shared help-queue entry if no sessions are still
		// waiting for help (e.g. AskUserQuestion). Without this guard, a
		// mixed permission+help state would lose the help attention marker.
		hasRemainingHelp := false
		for _, s := range sessions {
			if s.FeatureID() == featureID && s.Status() == session.SessionWaitingHelp {
				hasRemainingHelp = true
				break
			}
		}

		_ = m.featureManager.Store.Modify(featureID, func(f *feature.Feature) error {
			if !hasRemainingHelp {
				clearPendingHelpByMessage(f, waitingInputHelpMessage)
			}
			for i := range f.PermissionsQueue {
				if f.PermissionsQueue[i].Pending {
					f.PermissionsQueue[i].Pending = false
				}
			}
			return nil
		})
		return RefreshFeaturesMsg{}
	}
}

func (m AppModel) answerHelpCmd(featureID, answer string) tea.Cmd {
	return func() tea.Msg {
		sessions := m.sessionManager.ActiveSessions()
		for _, s := range sessions {
			if s.FeatureID() == featureID &&
				(s.Status() == session.SessionWaitingHelp || s.Status() == session.SessionWaitingPermission) {
				_ = s.SendUserMessage(answer)
				s.ResetWaitingStatus()
			}
		}

		_ = m.featureManager.Store.Modify(featureID, func(f *feature.Feature) error {
			// Mark first pending help as answered with user's response
			for i := range f.HelpQueue {
				if f.HelpQueue[i].Pending {
					f.HelpQueue[i].Pending = false
					f.HelpQueue[i].Answer = answer
					break
				}
			}
			clearPendingHelpByMessage(f, waitingInputHelpMessage)
			return nil
		})
		return RefreshFeaturesMsg{}
	}
}

func (m AppModel) viewDiffCmd(featureID, slug, worktreePath, baseBranch string) tea.Cmd {
	return func() tea.Msg {
		diff, err := git.DiffSummary(worktreePath, baseBranch)
		if err != nil {
			diff = MutedStyle.Render("No changes found")
		} else {
			diff = colorizeDiff(diff)
		}
		return LogsContentMsg{
			Title:     fmt.Sprintf("Diff: %s", slug),
			Content:   diff,
			FeatureID: featureID,
		}
	}
}

func (m AppModel) cleanWorktreeCmd(featureID string) tea.Cmd {
	return func() tea.Msg {
		_ = m.orchestrator.CleanWorktree(featureID)
		return RefreshFeaturesMsg{}
	}
}

func (m AppModel) viewLogsCmd(featureID string, phase feature.Phase, roadmapPhase int) tea.Cmd {
	return func() tea.Msg {
		f, loadErr := m.featureManager.Get(featureID)
		if loadErr != nil {
			return RefreshFeaturesMsg{}
		}
		var phaseDir string
		if roadmapPhase > 0 {
			// Roadmap features store logs under phase-NN/<phase>/
			phaseDir = filepath.Join(agent.PhaseDir(m.featureManager.Store.BaseDir, f, roadmapPhase), phase.DirName())
		} else {
			phaseDir = filepath.Join(agent.ActiveRunDir(m.featureManager.Store.BaseDir, f), phase.DirName())
		}
		logPath := filepath.Join(phaseDir, "output.txt")
		data, err := os.ReadFile(logPath)
		if err != nil {
			// Fallback: find the most recently modified .txt file in
			// iteration subdirectories (e.g. review/iteration-02/fix-output.txt).
			data, logPath = findLatestLogFile(phaseDir)
		}
		if data == nil {
			return RefreshFeaturesMsg{}
		}
		// With JSON protocol, output is already clean text
		lines := splitLastN(string(data), 100)
		title := fmt.Sprintf("Logs: %s (%s)", featureID, phase)
		if base := filepath.Base(logPath); base != "output.txt" {
			title = fmt.Sprintf("Logs: %s (%s — %s)", featureID, phase, base)
		}
		return LogsContentMsg{
			Title:   title,
			Content: strings.Join(lines, "\n"),
		}
	}
}

// findLatestLogFile searches phaseDir for the most recently modified .txt
// log file inside iteration subdirectories. Returns nil data if nothing found.
func findLatestLogFile(phaseDir string) ([]byte, string) {
	entries, err := os.ReadDir(phaseDir)
	if err != nil {
		return nil, ""
	}

	var bestPath string
	var bestTime time.Time

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		iterDir := filepath.Join(phaseDir, e.Name())
		files, err := os.ReadDir(iterDir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".txt") {
				continue
			}
			info, err := f.Info()
			if err != nil {
				continue
			}
			if info.ModTime().After(bestTime) {
				bestTime = info.ModTime()
				bestPath = filepath.Join(iterDir, f.Name())
			}
		}
	}

	if bestPath == "" {
		return nil, ""
	}
	data, err := os.ReadFile(bestPath)
	if err != nil {
		return nil, ""
	}
	return data, bestPath
}

func (m AppModel) updateLogs(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, keys.Back) || key.Matches(msg, keys.Quit) {
		if m.detail.feature != nil {
			return m.transitionTo(ViewDetail, m.detail.feature.ID), nil
		}
		return m.transitionTo(ViewDashboard, ""), nil
	}
	// Allow publishing directly from diff view
	if key.Matches(msg, keys.Publish) && m.logs.featureID != "" {
		f, err := m.featureManager.Get(m.logs.featureID)
		if err == nil && f.Status == feature.StatusCodeReady && !f.Checkpoints.AutoPublish() && f.IsPublishable() {
			return m.transitionToPublish(f.ID)
		}
	}
	var cmd tea.Cmd
	m.logs, cmd = m.logs.Update(msg)
	return m, cmd
}

func (m AppModel) updateAttach(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.attach, cmd = m.attach.Update(msg)
	if m.attach.Detached() || m.attach.Done() {
		// Handle tweak session Ctrl+D: record finish intent and stop session.
		if m.attach.isTweakSession && m.attach.TweakFinishing() {
			if sess := m.attach.sess; sess != nil {
				m.tweakFinishingFeatureID = sess.FeatureID()
				sess.Stop()
			}
		}
		// Badge clearing is NOT done here. Help badges are reconciled at the
		// moment of user response via HelpResolvedMsg → reconcileHelpQueue, so
		// detaching without answering leaves a pending badge intact.
		//
		// Save LastAttachedRepo for multi-repo features
		if repoName := m.attach.ActiveRepoName(); repoName != "" && m.attach.featureID != "" {
			_ = m.featureManager.Store.Modify(m.attach.featureID, func(f *feature.Feature) error {
				f.LastAttachedRepo = repoName
				return nil
			})
		}
		m.currentView = ViewDashboard
		// Preserve the command from attach (e.g. PlanReviewDecisionMsg) alongside refresh.
		return m, tea.Batch(cmd, func() tea.Msg { return RefreshFeaturesMsg{} })
	}
	return m, cmd
}

func (m AppModel) updateArtifactReview(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.artifactReview, cmd = m.artifactReview.Update(msg)
	if m.artifactReview.Detached() {
		if m.artifactReview.Decided() {
			m.artifactReview.StopSession()
		}
		m.clearReviewGateDashboardAttention(m.artifactReview.FeatureID())
		m.currentView = ViewDashboard
		return m, tea.Batch(cmd, func() tea.Msg { return RefreshFeaturesMsg{} })
	}
	return m, cmd
}

func (m AppModel) clearReviewGateDashboardAttention(featureID string) {
	if featureID == "" || m.featureManager == nil || m.featureManager.Store == nil {
		return
	}
	_ = m.featureManager.Store.Modify(featureID, func(f *feature.Feature) error {
		if !f.Status.IsNeedsReview() {
			return nil
		}
		for i := range f.HelpQueue {
			if f.HelpQueue[i].Pending {
				f.HelpQueue[i].Pending = false
			}
		}
		for i := range f.PermissionsQueue {
			if f.PermissionsQueue[i].Pending {
				f.PermissionsQueue[i].Pending = false
			}
		}
		return nil
	})
}

func (m AppModel) updateChat(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.chat, cmd = m.chat.Update(msg)
	return m, cmd
}

func (m AppModel) updateWelcome(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.welcome, cmd = m.welcome.Update(msg)

	// Check if a new root was just selected (happens on each picker completion)
	if root := m.welcome.ConsumePendingRoot(); root != "" {
		m.featureManager.Config.WorkspaceRoots = append(
			m.featureManager.Config.WorkspaceRoots,
			root,
		)
		// Persist config immediately — survives crashes between selections
		if m.configPath != "" {
			_ = config.Save(m.configPath, m.featureManager.Config)
		}
		// Rebuild in-memory discovered repos from the updated root set
		config.DiscoverReposFromRoots(m.featureManager.Config)
	}

	if m.welcome.IsDone() {
		if m.welcome.IsCancelled() {
			// User skipped — show guidance on dashboard (renders in footer
			// regardless of feature count via the transient status message)
			m.dashboard.SetWelcomeSkipped()
			m.statusMessage = "You can add workspace roots later by pressing W"
			m.statusTime = time.Now()
			features, _ := m.featureManager.List()
			m.dashboard.SetFeatures(features)
			return m.transitionTo(ViewDashboard, ""), cmd
		}
		// All roots already saved incrementally; just transition
		features, _ := m.featureManager.List()
		m.dashboard.SetFeatures(features)
		return m.transitionTo(ViewDashboard, ""), cmd
	}
	return m, cmd
}

// transitionToHelpOverlay opens the context-sensitive help overlay.
func (m AppModel) transitionToHelpOverlay() (tea.Model, tea.Cmd) {
	contexts := AllHelpContexts()

	var ctxName string
	switch m.currentView {
	case ViewDashboard:
		if m.dashboard.focusPanel == 1 {
			ctxName = "Detail Panel"
		} else {
			ctxName = "Dashboard"
		}
	case ViewDetail:
		ctxName = "Detail"
	case ViewWizard:
		ctxName = "Wizard"
	case ViewPublish:
		ctxName = "Publish"
	case ViewRecovery:
		ctxName = "Recovery"
	case ViewLogs:
		ctxName = "Logs"
	case ViewReviewComments:
		ctxName = "Review Comments"
	default:
		ctxName = "Dashboard"
	}

	ctx, ok := contexts[ctxName]
	if !ok {
		ctx = contexts["Dashboard"]
	}

	m.helpOverlay = NewHelpOverlayModel(ctx, m.width, m.height)
	m.helpOverlayActive = true
	return m, nil
}

// transitionToWorkspaceManager opens the workspace manager overlay.
func (m AppModel) transitionToWorkspaceManager() (tea.Model, tea.Cmd) {
	roots := buildWorkspaceRoots(m.featureManager.Config)
	m.workspaceManager = NewWorkspaceManagerModel(roots, m.width, m.height)
	m.workspaceManagerActive = true
	return m, m.workspaceManager.Init()
}

// buildWorkspaceRoots creates the display model from config.
func buildWorkspaceRoots(cfg *config.Config) []workspaceRoot {
	var roots []workspaceRoot
	for _, path := range cfg.WorkspaceRoots {
		expanded := config.ExpandHome(path)
		selfIsRepo := isGitRepo(expanded)
		count := countGitReposInDir(expanded)
		roots = append(roots, workspaceRoot{Path: expanded, RepoCount: count, IsRepo: selfIsRepo})
	}
	return roots
}

// updateWorkspaceManager delegates input to the workspace manager and handles state changes.
func (m AppModel) updateWorkspaceManager(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.workspaceManager, cmd = m.workspaceManager.Update(msg)

	// Handle root added
	if root := m.workspaceManager.ConsumeAddedRoot(); root != "" {
		// Prevent duplicates: compare by expanded path (~ resolved to $HOME)
		if !containsRootExpanded(m.featureManager.Config.WorkspaceRoots, root) {
			m.featureManager.Config.WorkspaceRoots = append(
				m.featureManager.Config.WorkspaceRoots,
				root,
			)
		}
		if m.configPath != "" {
			_ = config.Save(m.configPath, m.featureManager.Config)
		}
		config.DiscoverReposFromRoots(m.featureManager.Config)
		// Refresh the manager's root list with updated counts
		m.workspaceManager.SetRoots(buildWorkspaceRoots(m.featureManager.Config))
	}

	// Handle root removed
	if root := m.workspaceManager.ConsumeRemovedRoot(); root != "" {
		m.featureManager.Config.WorkspaceRoots = removeRoot(
			m.featureManager.Config.WorkspaceRoots, root,
		)
		if m.configPath != "" {
			_ = config.Save(m.configPath, m.featureManager.Config)
		}
		config.DiscoverReposFromRoots(m.featureManager.Config)
		// Refresh the manager's root list
		m.workspaceManager.SetRoots(buildWorkspaceRoots(m.featureManager.Config))
	}

	// Handle close
	if m.workspaceManager.IsClosed() {
		m.workspaceManagerActive = false
		// Refresh dashboard features after any changes
		features, _ := m.featureManager.List()
		m.dashboard.SetFeatures(features)
	}

	return m, cmd
}

// handleWizardBrowseResult checks for a pending browse root from the wizard's
// DirPickerModel, persists it to config, re-discovers repos, and refreshes
// the wizard's repo list.
func (m AppModel) handleWizardBrowseResult() AppModel {
	root := m.wizard.ConsumeBrowseRoot()
	if root == "" {
		return m
	}
	if !containsRootExpanded(m.featureManager.Config.WorkspaceRoots, root) {
		m.featureManager.Config.WorkspaceRoots = append(
			m.featureManager.Config.WorkspaceRoots,
			root,
		)
	}
	if m.configPath != "" {
		_ = config.Save(m.configPath, m.featureManager.Config)
	}
	config.DiscoverReposFromRoots(m.featureManager.Config)

	allRepos := config.AllRepos(m.featureManager.Config)
	var availRepos []string
	repoPaths := make(map[string]string)
	for name, rc := range allRepos {
		availRepos = append(availRepos, name)
		repoPaths[name] = rc.Path
	}
	sort.Strings(availRepos)
	m.wizard.RefreshRepos(availRepos, repoPaths, allRepos)
	m.wizard.SetWorkspaceRoots(m.featureManager.Config.WorkspaceRoots)
	return m
}

// handleWizardCreateRepoResult processes a newly created repo from the wizard,
// registering its parent as a workspace root and auto-selecting it.
func (m AppModel) handleWizardCreateRepoResult() AppModel {
	createdPath := m.wizard.ConsumeCreateRepoPath()
	if createdPath == "" {
		return m
	}
	parentDir := filepath.Dir(createdPath)
	if !containsRootExpanded(m.featureManager.Config.WorkspaceRoots, parentDir) {
		m.featureManager.Config.WorkspaceRoots = append(
			m.featureManager.Config.WorkspaceRoots,
			parentDir,
		)
	}
	if m.configPath != "" {
		_ = config.Save(m.configPath, m.featureManager.Config)
	}
	config.DiscoverReposFromRoots(m.featureManager.Config)

	allRepos := config.AllRepos(m.featureManager.Config)
	var availRepos []string
	repoPaths := make(map[string]string)
	for name, rc := range allRepos {
		availRepos = append(availRepos, name)
		repoPaths[name] = rc.Path
	}
	sort.Strings(availRepos)
	m.wizard.RefreshRepos(availRepos, repoPaths, allRepos)
	m.wizard.SetWorkspaceRoots(m.featureManager.Config.WorkspaceRoots)
	m.wizard.AutoSelectCreatedRepo(createdPath, repoPaths)
	m.wizard.Advance()
	return m
}

// containsRootExpanded checks if a root path is already in the list.
// Comparison is by expanded path: both candidate and each existing root
// are passed through config.ExpandHome() to resolve ~ to $HOME, then
// compared as strings. This prevents duplicates like "~/Projects" and
// "/home/user/Projects" from coexisting.
func containsRootExpanded(roots []string, candidate string) bool {
	expandedCandidate := config.ExpandHome(candidate)
	for _, r := range roots {
		if config.ExpandHome(r) == expandedCandidate {
			return true
		}
	}
	return false
}

// removeRoot removes a root path from the list by expanded path comparison.
func removeRoot(roots []string, path string) []string {
	expandedPath := config.ExpandHome(path)
	result := make([]string, 0, len(roots))
	for _, r := range roots {
		if config.ExpandHome(r) != expandedPath {
			result = append(result, r)
		}
	}
	return result
}

// transitionToChat opens the Ask me Anything chat panel at the bottom of the dashboard.
// The chat model is initialized lazily on first open and reused across
// esc/reopen transitions within the same TUI session. Conversation
// history is discarded only on next TUI launch.
func (m AppModel) transitionToChat() (tea.Model, tea.Cmd) {
	chatH := chatPanelHeight(m.height)
	if !m.chatReady {
		systemPrompt := m.buildChatContext()
		chatModelName := "sonnet"
		if m.featureManager != nil && m.featureManager.Config != nil && m.featureManager.Config.Defaults.Models.Utilities != "" {
			chatModelName = m.featureManager.Config.Defaults.Models.Utilities
		}
		var buildSession agent.BuildSessionFunc
		var skillsDir string
		if m.phaseRunner != nil {
			buildSession = m.phaseRunner.BuildSession
			skillsDir = m.phaseRunner.SkillsDir
		}
		m.chat = NewChatModel(m.width, chatH, m.sessionManager, m.workspaceDir, systemPrompt, buildSession, chatModelName, skillsDir)
		m.chatReady = true
	} else {
		// Reuse existing chat, but update dimensions in case of resize
		m.chat = m.chat.resize(m.width, chatH)
		m.chat.input.Focus()
	}
	m.chatOpen = true
	return m, textarea.Blink
}

// chatPanelHeight returns the height allocated to the chat bottom panel.
func chatPanelHeight(totalHeight int) int {
	h := totalHeight * 35 / 100
	if h < 10 {
		h = 10
	}
	if h > 18 {
		h = 18
	}
	return h
}

// buildChatContext returns a summary of current features for chat context.
func (m AppModel) buildChatContext() string {
	features, _ := m.featureManager.List()
	if len(features) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Current Features\n\n")
	for _, f := range features {
		fmt.Fprintf(&b, "- **%s** (ID: %s): %s — Status: %s\n",
			f.Name, f.ID, f.Description, f.Status)
		if len(f.Repos) > 0 {
			fmt.Fprintf(&b, "  Repo: %s", f.Repos[0].Path)
			if f.Repos[0].WorktreePath != "" {
				fmt.Fprintf(&b, ", Worktree: %s", f.Repos[0].WorktreePath)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

func (m AppModel) restartBudgetDeltas() (int, int) {
	// Config defaults drive the iteration budget bump for failed phases. The
	// orchestrator's ports.FeatureLifecycle surface does not carry config, so
	// the TUI reads Defaults and passes them through.
	maxIterationsDelta := 10
	maxPlanIterationsDelta := 2
	if cfg := m.featureManager.Config; cfg != nil {
		if cfg.Defaults.MaxIterations > 0 {
			maxIterationsDelta = cfg.Defaults.MaxIterations
		}
		if cfg.Defaults.MaxPhasePlanIterations > 0 {
			maxPlanIterationsDelta = cfg.Defaults.MaxPhasePlanIterations
		}
	}
	return maxIterationsDelta, maxPlanIterationsDelta
}

func (m AppModel) sendRestartOutcome(featureID string, outcome orchestrator.RestartOutcome) {
	if m.programRef == nil || m.programRef.P == nil {
		return
	}

	switch outcome.Action {
	case orchestrator.RestartDispatchPhase:
		m.programRef.P.Send(StartPhaseMsg{FeatureID: featureID, Phase: outcome.Phase})
	case orchestrator.RestartDispatchRepoCycles:
		for _, r := range outcome.RepoCycleRestarts {
			m.programRef.P.Send(restartRepoCycleMsg{
				FeatureID:   featureID,
				RepoName:    r.RepoName,
				CycleType:   r.CycleType,
				PlanContent: r.PlanContent,
			})
		}
		if outcome.RefactorRestart != nil {
			m.programRef.P.Send(restartRefactorCycleMsg{
				FeatureID: featureID,
				RepoName:  outcome.RefactorRestart.RepoName,
				Prompt:    outcome.RefactorRestart.Prompt,
			})
		}
	}
}

// restartPhaseCmd is a thin delegate over orchestrator.RestartPhase. The
// orchestrator owns the full restart decision tree: session-stop, tweak-cycle
// reset, failed-phase budget extension, Published + repo-cycles fan-out, and
// the phase + status transition switch. This helper just reads the config
// deltas (TUI-local config lookup) and forwards the resulting RestartOutcome
// to the program event loop.
func (m AppModel) restartPhaseCmd(featureID string) tea.Cmd {
	return func() tea.Msg {
		maxIterationsDelta, maxPlanIterationsDelta := m.restartBudgetDeltas()

		outcome, err := m.orchestrator.RestartPhase(featureID, maxIterationsDelta, maxPlanIterationsDelta)
		if err != nil {
			if errors.Is(err, orchestrator.ErrFeatureBusy) {
				return restartBusyMsg{FeatureID: featureID}
			}
			return RefreshFeaturesMsg{}
		}

		// Dispatch the follow-up work the orchestrator asked for.
		m.sendRestartOutcome(featureID, outcome)
		return RefreshFeaturesMsg{}
	}
}

// retryPhaseCmd retries the failed phase atomically across every
// phase-declared repo. Under the unified flow phase atomicity means
// retry clears feature-level error state so the unified phase-implement
// loop can re-run from iteration 1. Bound to [Shift+R] in the detail view.
func (m AppModel) retryPhaseCmd(featureID string) tea.Cmd {
	return func() tea.Msg {
		// Stop any active sessions for this feature so the retry does not race
		// with an orphaned agent writing to the store.
		if m.orchestrator != nil {
			m.orchestrator.StopFeatureSessions(featureID)
		}

		if err := m.orchestrator.RetryPhase(featureID); err != nil {
			return RefreshFeaturesMsg{}
		}

		// Transition to implementing state
		_ = m.orchestrator.TransitionTo(featureID, feature.StatusImplementReady)

		// Schedule phase execution
		if m.programRef != nil && m.programRef.P != nil {
			m.programRef.P.Send(StartPhaseMsg{FeatureID: featureID, Phase: feature.PhaseImplement})
		}

		return RefreshFeaturesMsg{}
	}
}

// attachToSession transitions to the structured attach view for the given session.
func (m *AppModel) attachToSession(sess session.SessionView) tea.Cmd {
	m.attach = attachModelFromSession(sess, m.width, m.height)
	if m.phaseRunner != nil {
		m.attach.skillsDir = m.phaseRunner.SkillsDir
	}
	m.attach.permCache = m.permissionCache
	m.attach.permRepoName = sess.PermCacheScope()
	m.attach.observer = m.observer
	m.attach.isTweakSession = isTweakSessionID(sess.ID())
	if f, err := m.featureManager.Get(sess.FeatureID()); err == nil {
		m.attach.traceID = f.TraceID
		m.attach.featureName = f.Name
		m.attach.featureSpanID = f.FeatureSpanID
		m.attach.activeRun = f.ActiveRun
	}
	m.attach.emitRestoredObservability()
	m.currentView = ViewAttach
	return m.attach.Init()
}

// ProgramRef returns the shared program reference. Tests use this to wire
// the teatest program into the model's background goroutines.
func (m AppModel) ProgramRef() *ProgramRef {
	return m.programRef
}

// SetProgram stores the tea.Program reference for attach/detach operations.
func (m *AppModel) SetProgram(p *tea.Program) {
	if m.programRef == nil {
		m.programRef = &ProgramRef{}
	}
	m.programRef.P = p
}

// Close cleans up background resources (fsnotify watcher).
// Call after the tea.Program has exited.
func (m *AppModel) Close() {
	// No-op: signal file watcher removed with JSON protocol migration
}

// helpInputView renders the inline help answer input.
func (m AppModel) helpInputView() string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(WarningStyle.Render(" Agent needs help:"))
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("   %s\n\n", m.helpQuestion))
	b.WriteString(" Your answer:\n")
	b.WriteString(" " + m.helpInput.View() + "\n")
	b.WriteString(MutedStyle.Render(" [enter] Send   [esc] Cancel"))
	b.WriteString("\n")
	return b.String()
}

// deleteConfirmModal renders the delete confirmation as a bordered modal panel.
func (m AppModel) deleteConfirmModal() string {
	var c strings.Builder
	c.WriteString("\n")
	c.WriteString(fmt.Sprintf("  %s\n\n", m.deleteFeatureName))
	c.WriteString(WarningStyle.Render("  This will remove all artifacts and worktrees."))
	c.WriteString("\n")
	c.WriteString(WarningStyle.Render("  This cannot be undone."))
	c.WriteString("\n")

	panelWidth := 52
	contentBox := panelStyle(true).
		Width(panelWidth).
		BorderForeground(colorError).
		Render(c.String())
	contentBox = renderBorderTitle(contentBox, "Delete Feature", ErrorStyle)

	footer := KeyHelpStyle.Render(" [y] Confirm   [any key] Cancel")
	return contentBox + "\n" + footer
}

func (m AppModel) rewindMenuModal() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorBrand)
	selectedStyle := lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	normalStyle := lipgloss.NewStyle()
	hintStyle := MutedStyle

	var b strings.Builder
	b.WriteString(titleStyle.Render("Rewind to Phase"))
	b.WriteString("\n\n")

	for i, choice := range m.rewindMenuChoices {
		cursor := "  "
		style := normalStyle
		if i == m.rewindMenuCursor {
			cursor = "> "
			style = selectedStyle
		}
		label := m.rewindMenuChoiceLabel(choice)
		if choice.OverridePhase == feature.PhaseKnowledgeBase {
			label += " " + hintStyle.Render("(restarts from KB Build)")
		}
		b.WriteString(style.Render(cursor + label))
		b.WriteByte('\n')
	}

	// Pipeline Upgrade section
	if len(m.rewindMenuUpgradeOptions) > 0 {
		b.WriteByte('\n')
		b.WriteString(titleStyle.Render("Pipeline Upgrade"))
		b.WriteByte('\n')
		for i, profile := range m.rewindMenuUpgradeOptions {
			idx := len(m.rewindMenuChoices) + i
			cursor := "  "
			style := normalStyle
			if idx == m.rewindMenuCursor {
				cursor = "> "
				style = selectedStyle
			}
			label := "Upgrade to " + string(profile) + " " + hintStyle.Render("(rewinds to KB Build)")
			b.WriteString(style.Render(cursor + label))
			b.WriteByte('\n')
		}
	}

	b.WriteByte('\n')
	b.WriteString(hintStyle.Render("Enter to select · Esc to cancel"))

	menuBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorBrand).
		Padding(1, 2).
		Width(min(m.width-4, 56)).
		Render(b.String())

	return lipgloss.PlaceHorizontal(m.width, lipgloss.Center, menuBox)
}

func (m AppModel) rewindMenuChoiceLabel(choice feature.RewindChoice) string {
	if choice.Phase == feature.PhaseImplement && m.rewindMenuFeatureHasRoadmapPicker() {
		return "Choose Implement roadmap phase"
	}
	if choice.Phase == feature.PhaseInquire {
		return "Rewind to Start (Inquiry)"
	}
	return "Rewind to " + choice.Phase.String()
}

func (m AppModel) rewindMenuFeatureHasRoadmapPicker() bool {
	if m.featureManager == nil || m.rewindMenuFeatureID == "" {
		return false
	}
	f, err := m.featureManager.Get(m.rewindMenuFeatureID)
	if err != nil {
		return false
	}
	return len(m.buildRoadmapRewindRows(f)) > 1
}

func (m AppModel) buildRoadmapRewindRows(f *feature.Feature) []roadmapRewindRow {
	if f == nil {
		return nil
	}
	metadata := m.loadRoadmapPhaseMetadata(f)
	total := f.TotalRoadmapPhases
	for phase := range metadata {
		if phase > total {
			total = phase
		}
	}
	if total <= 1 {
		return nil
	}

	rows := make([]roadmapRewindRow, 0, total)
	for phase := 1; phase <= total; phase++ {
		meta := metadata[phase]
		title := meta.Name
		if title == "" {
			title = fmt.Sprintf("Phase %d", phase)
		}
		phaseType := meta.Type
		if phaseType == "" {
			phaseType = fallbackRoadmapPhaseType(phase, total)
		}
		anchorAvailable, unavailableReason := roadmapResetBoundaryAvailable(f, phase)
		row := roadmapRewindRow{
			Number:            phase,
			Total:             total,
			Title:             title,
			PhaseType:         phaseType,
			Status:            roadmapPhaseStatus(f, phase),
			Effect:            roadmapPhaseEffect(phase, total),
			ResetBoundary:     roadmapResetBoundaryLabel(phase),
			AnchorAvailable:   anchorAvailable,
			UnavailableReason: unavailableReason,
			CurrentPhase:      f.CurrentRoadmapPhase == phase,
		}
		rows = append(rows, row)
	}
	return rows
}

func (m AppModel) loadRoadmapPhaseMetadata(f *feature.Feature) map[int]agent.RoadmapPhase {
	path := m.resolvePhaseArtifactPath(f, "roadmap")
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	phases, err := agent.ParseRoadmap(string(data))
	if err != nil {
		return nil
	}
	metadata := make(map[int]agent.RoadmapPhase, len(phases))
	for _, phase := range phases {
		metadata[phase.Number] = phase
	}
	return metadata
}

func fallbackRoadmapPhaseType(phase, total int) string {
	if total == 1 {
		return "collapsed"
	}
	if phase == 1 {
		return "tracer-bullet"
	}
	return "tdd-fill-in"
}

func roadmapPhaseStatus(f *feature.Feature, phase int) string {
	switch {
	case f.CurrentRoadmapPhase == phase:
		return "current"
	case f.CurrentRoadmapPhase > phase:
		return "completed"
	default:
		return "pending"
	}
}

func roadmapResetBoundaryAvailable(f *feature.Feature, phase int) (bool, string) {
	if phase <= 1 {
		return true, ""
	}
	previousPhase := phase - 1
	var anchors map[string]string
	if f.Run() != nil {
		anchors = f.Run().RoadmapPhaseCommitAnchors[previousPhase]
	}
	for _, repo := range f.Repos {
		if repo.WorktreePath == "" {
			continue
		}
		if anchors == nil || anchors[repo.Name] == "" {
			return false, fmt.Sprintf("reset boundary unavailable: missing commit anchor for Phase %d", previousPhase)
		}
	}
	return true, ""
}

func roadmapResetBoundaryLabel(phase int) string {
	if phase <= 1 {
		return "base branch"
	}
	return fmt.Sprintf("end of roadmap Phase %d", phase-1)
}

func roadmapPhaseEffect(phase, total int) string {
	return fmt.Sprintf("Preserve: %s; redo: Phase %d; discard: %s",
		roadmapPhaseRangeLabel(1, phase-1),
		phase,
		roadmapPhaseRangeLabel(phase+1, total),
	)
}

func roadmapPhaseRangeLabel(start, end int) string {
	if start > end {
		return "none"
	}
	if start == end {
		return fmt.Sprintf("Phase %d", start)
	}
	return fmt.Sprintf("Phases %d-%d", start, end)
}

func (m *AppModel) openRoadmapPhasePicker(featureID string) bool {
	if m.featureManager == nil {
		return false
	}
	f, err := m.featureManager.Get(featureID)
	if err != nil {
		return false
	}
	rows := m.buildRoadmapRewindRows(f)
	if len(rows) <= 1 {
		return false
	}
	m.rewindPhasePickerActive = true
	m.rewindPhasePickerFeatureID = featureID
	m.rewindPhasePickerRows = rows
	m.rewindPhasePickerCursor = 0
	for i, row := range rows {
		if row.CurrentPhase {
			m.rewindPhasePickerCursor = i
			break
		}
	}
	return true
}

func (m *AppModel) openFullRewindConfirmation(featureID string, phase feature.Phase, phaseName string, overridesKB bool, upgrade feature.PipelineProfile) {
	m.rewindConfirmActive = true
	m.rewindConfirmFeatureID = featureID
	m.rewindConfirmPhase = phase
	m.rewindConfirmPhaseName = phaseName
	m.rewindConfirmEscalates = upgrade
	m.rewindConfirmOverridesKB = overridesKB
	m.rewindConfirmUpgrade = upgrade
	m.rewindConfirmRoadmapPhase = 0
	m.rewindConfirmRoadmapRow = roadmapRewindRow{}
}

func (m *AppModel) openPartialRewindConfirmation(featureID string, row roadmapRewindRow) {
	m.rewindConfirmActive = true
	m.rewindConfirmFeatureID = featureID
	m.rewindConfirmPhase = feature.PhaseImplement
	m.rewindConfirmPhaseName = fmt.Sprintf("roadmap Phase %d", row.Number)
	m.rewindConfirmEscalates = ""
	m.rewindConfirmOverridesKB = false
	m.rewindConfirmUpgrade = ""
	m.rewindConfirmRoadmapPhase = row.Number
	m.rewindConfirmRoadmapRow = row
}

func (m AppModel) rewindPhasePickerModal() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorBrand)
	selectedStyle := lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	normalStyle := lipgloss.NewStyle()
	hintStyle := MutedStyle
	warningStyle := WarningStyle

	var b strings.Builder
	b.WriteString(titleStyle.Render("Choose Roadmap Phase"))
	b.WriteString("\n\n")
	for i, row := range m.rewindPhasePickerRows {
		cursor := "  "
		style := normalStyle
		if i == m.rewindPhasePickerCursor {
			cursor = "> "
			style = selectedStyle
		}
		header := fmt.Sprintf("%sPhase %d/%d: %s  [%s]  %s", cursor, row.Number, row.Total, row.Title, row.PhaseType, row.Status)
		if row.CurrentPhase {
			header += "  (current phase)"
		}
		b.WriteString(style.Render(header))
		b.WriteByte('\n')
		b.WriteString(hintStyle.Render("    " + row.Effect))
		b.WriteByte('\n')
		b.WriteString(hintStyle.Render("    Reset boundary: " + row.ResetBoundary))
		b.WriteByte('\n')
		if !row.AnchorAvailable {
			b.WriteString(warningStyle.Render("    " + row.UnavailableReason))
			b.WriteByte('\n')
		}
	}
	b.WriteByte('\n')
	b.WriteString(hintStyle.Render("Enter to continue · Esc to return"))

	menuBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorBrand).
		Padding(1, 2).
		Width(min(m.width-4, 84)).
		Render(b.String())
	return lipgloss.PlaceHorizontal(m.width, lipgloss.Center, menuBox)
}

func (m AppModel) rewindPartialUnavailableModal() string {
	var c strings.Builder
	row := m.rewindPartialUnavailableRow
	reason := m.rewindPartialUnavailableReason
	if reason == "" {
		reason = "reset boundary unavailable"
	}
	fmt.Fprintf(&c, "\n  Partial rewind to roadmap Phase %d is unavailable.\n\n", row.Number)
	c.WriteString(WarningStyle.Render("  " + reason))
	c.WriteString("\n")
	c.WriteString(WarningStyle.Render("  You can cancel or deliberately continue with a full Implement rewind."))
	c.WriteString("\n")

	contentBox := panelStyle(true).
		Width(68).
		BorderForeground(colorWarning).
		Render(c.String())
	contentBox = renderBorderTitle(contentBox, "Partial Rewind Unavailable", WarningStyle)
	footer := KeyHelpStyle.Render(" [f] Full Implement rewind   [esc] Cancel")
	return contentBox + "\n" + footer
}

func (m AppModel) rewindConfirmModal() string {
	var c strings.Builder
	c.WriteString("\n")
	if m.rewindConfirmUpgrade != "" {
		fmt.Fprintf(&c, "  ⚠ Upgrade to %s\n\n", m.rewindConfirmUpgrade)
		c.WriteString(WarningStyle.Render("  Pipeline will be upgraded and feature will restart from KB Build."))
		c.WriteString("\n")
		c.WriteString(WarningStyle.Render("  All progress will be lost."))
		c.WriteString("\n")
	} else if m.rewindConfirmRoadmapPhase > 0 {
		row := m.rewindConfirmRoadmapRow
		fmt.Fprintf(&c, "  ⚠ Rewind Implement to roadmap Phase %d\n\n", row.Number)
		c.WriteString(WarningStyle.Render("  Keep: " + roadmapPhaseRangeLabel(1, row.Number-1)))
		c.WriteString("\n")
		redo := fmt.Sprintf("Phase %d", row.Number)
		if row.Title != "" && row.Title != fmt.Sprintf("Phase %d", row.Number) {
			redo = fmt.Sprintf("%s (%s)", redo, row.Title)
		}
		c.WriteString(WarningStyle.Render("  Redo: " + redo))
		c.WriteString("\n")
		c.WriteString(WarningStyle.Render("  Discard: " + roadmapPhaseRangeLabel(row.Number+1, row.Total)))
		c.WriteString("\n")
		c.WriteString(WarningStyle.Render("  Reset boundary: " + row.ResetBoundary))
		c.WriteString("\n")
	} else {
		fmt.Fprintf(&c, "  ⚠ Rewind to %s\n\n", m.rewindConfirmPhaseName)

		// List phases that will be re-run
		phasesToDelete := feature.PhasesFromOnwards(m.rewindConfirmPhase)
		c.WriteString(WarningStyle.Render("  All progress from this phase onwards will be lost:"))
		c.WriteString("\n")
		for _, p := range phasesToDelete {
			c.WriteString(WarningStyle.Render("  - " + p.String()))
			c.WriteString("\n")
		}

		if m.rewindConfirmOverridesKB {
			c.WriteString(WarningStyle.Render("  Feature will restart from KB Build"))
			c.WriteString("\n")
		}
	}

	// Check if PR will be closed or backup branch will be created
	if f, err := m.featureManager.Get(m.rewindConfirmFeatureID); err == nil {
		prURLs := f.PRURLs()
		if len(prURLs) > 0 {
			repoNames := make([]string, 0, len(prURLs))
			for repoName := range prURLs {
				repoNames = append(repoNames, repoName)
			}
			sort.Strings(repoNames)
			c.WriteString(WarningStyle.Render("  PRs that will be closed:"))
			c.WriteString("\n")
			for _, repoName := range repoNames {
				c.WriteString(WarningStyle.Render(fmt.Sprintf("  - %s: %s", repoName, prURLs[repoName])))
				c.WriteString("\n")
			}
		}
		// Show backup branch warning if worktree has implementation work
		hasWorktree := false
		for _, repo := range f.Repos {
			if repo.WorktreePath != "" {
				hasWorktree = true
				break
			}
		}
		if hasWorktree {
			c.WriteString(WarningStyle.Render("  - A backup branch will be created"))
			c.WriteString("\n")
		}
	}

	panelWidth := 60
	contentBox := panelStyle(true).
		Width(panelWidth).
		BorderForeground(colorWarning).
		Render(c.String())
	contentBox = renderBorderTitle(contentBox, "Rewind Confirmation", WarningStyle)

	footer := KeyHelpStyle.Render(" [y] Confirm   [any key] Cancel")
	return contentBox + "\n" + footer
}

func (m AppModel) stopConfirmModal() string {
	var c strings.Builder
	c.WriteString("\n")
	c.WriteString(fmt.Sprintf("  %s\n\n", m.stopConfirmFeatureName))
	c.WriteString(WarningStyle.Render("  This will interrupt the current phase."))
	c.WriteString("\n")
	c.WriteString(WarningStyle.Render("  You can restart it later with [r]."))
	c.WriteString("\n")

	panelWidth := 52
	contentBox := panelStyle(true).
		Width(panelWidth).
		BorderForeground(colorWarning).
		Render(c.String())
	contentBox = renderBorderTitle(contentBox, "Stop Feature", WarningStyle)

	footer := KeyHelpStyle.Render(" [y] Confirm   [any key] Cancel")
	return contentBox + "\n" + footer
}

func (m AppModel) cycleSelectModal() string {
	actionLabel := string(m.cycleSelectAction)
	switch m.cycleSelectAction {
	case feature.CycleRebase:
		actionLabel = "Rebase"
	case feature.CycleTweak:
		actionLabel = "Tweak"
	case feature.CycleReviewComments:
		actionLabel = "Review Comments"
	case feature.CycleRefactor:
		actionLabel = "Refactor"
	}

	var c strings.Builder
	c.WriteString("\n")
	for i, entry := range m.cycleSelectRepos {
		cursor := "  "
		if i == m.cycleSelectCursor {
			cursor = lipgloss.NewStyle().Foreground(colorBrand).Render("> ")
		}

		name := entry.Name
		status := MutedStyle.Render("idle")
		if strings.HasPrefix(entry.CycleStatus, "running:") {
			runType := strings.TrimPrefix(entry.CycleStatus, "running:")
			status = lipgloss.NewStyle().Foreground(colorInfo).Render(runType)
			name = MutedStyle.Render(entry.Name) // dim busy repos
		} else if entry.CycleStatus == "failed" {
			status = ErrorStyle.Render("failed")
		}

		prInfo := ""
		if entry.PRURL != "" {
			// Extract PR number
			parts := strings.Split(entry.PRURL, "/")
			if len(parts) > 0 {
				prInfo = MutedStyle.Render(" PR #" + parts[len(parts)-1])
			}
		}

		c.WriteString(fmt.Sprintf("  %s%s  %s%s\n", cursor, name, status, prInfo))
	}

	panelWidth := 57
	contentBox := panelStyle(true).
		Width(panelWidth).
		BorderForeground(colorBrand).
		Render(c.String())
	contentBox = renderBorderTitle(contentBox, "Select repo — "+actionLabel, lipgloss.NewStyle().Foreground(colorBrand))

	footer := KeyHelpStyle.Render(" [\u2191/\u2193] Select   [enter] Confirm   [esc] Cancel")
	return contentBox + "\n" + footer
}

func (m AppModel) manualPublishConfirmModal() string {
	var c strings.Builder
	c.WriteString("\n")
	c.WriteString(fmt.Sprintf("  %s\n\n", m.manualPublishFeatureName))
	c.WriteString(MutedStyle.Render("  Mark this feature as manually published?"))
	c.WriteString("\n")
	c.WriteString(MutedStyle.Render("  The feature will move to Done without creating a PR."))
	c.WriteString("\n")

	panelWidth := 58
	contentBox := panelStyle(true).
		Width(panelWidth).
		BorderForeground(colorInfo).
		Render(c.String())
	contentBox = renderBorderTitle(contentBox, "Manual Publish", lipgloss.NewStyle().Foreground(colorInfo))

	footer := KeyHelpStyle.Render(" [y] Confirm   [any key] Cancel")
	return contentBox + "\n" + footer
}

func (m AppModel) quitConfirmModal() string {
	features, _ := m.featureManager.List()
	running := 0
	for _, f := range features {
		if isRunningFeature(f) {
			running++
		}
	}

	var c strings.Builder
	c.WriteString("\n")
	if running > 0 {
		c.WriteString(WarningStyle.Render(fmt.Sprintf("  %d running session(s) will be interrupted.", running)))
	} else {
		c.WriteString(MutedStyle.Render("  No sessions are currently running."))
	}
	c.WriteString("\n")

	panelWidth := 52
	contentBox := panelStyle(true).
		Width(panelWidth).
		BorderForeground(colorWarning).
		Render(c.String())
	contentBox = renderBorderTitle(contentBox, "Quit Agentic Orchestrator?", WarningStyle)

	footer := KeyHelpStyle.Render(" [y] Confirm   [any key] Cancel")
	return contentBox + "\n" + footer
}

// forwardToActiveInput routes non-key messages (e.g. clipboard paste results,
// cursor blink) to the view that owns the currently focused text input so that
// internal component messages are not silently dropped.
func (m AppModel) forwardToActiveInput(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Chat-owned messages must reach the chat model regardless of which
	// view is currently active. Otherwise a tick fired while the user is
	// in the attach/artifact-review view gets eaten by that view's
	// Update and the chat's recovery ticker dies permanently — turning a
	// transient dropped-Result into a forever "Thinking…".
	switch msg.(type) {
	case chatMsgsMsg, chatDoneMsg, chatSessionStartedMsg, chatSendErrorMsg, chatRecoveryTickMsg:
		if m.chatReady {
			var cmd tea.Cmd
			m.chat, cmd = m.chat.Update(msg)
			return m, cmd
		}
	}
	// Help input can be active from dashboard or detail
	if m.helpInputActive {
		var cmd tea.Cmd
		m.helpInput, cmd = m.helpInput.Update(msg)
		return m, cmd
	}
	if m.refactorInputActive {
		var cmd tea.Cmd
		m.refactorInput, cmd = m.refactorInput.Update(msg)
		return m, cmd
	}
	// Forward all messages to attach model when in attach view.
	// This ensures cursor blink, viewport scroll, attach data, and
	// other internal messages reach the attach model's sub-components.
	if m.currentView == ViewAttach {
		var cmd tea.Cmd
		m.attach, cmd = m.attach.Update(msg)
		if m.attach.Detached() {
			// Handle tweak session Ctrl+D: record finish intent and stop session.
			if m.attach.isTweakSession && m.attach.TweakFinishing() {
				if sess := m.attach.sess; sess != nil {
					m.tweakFinishingFeatureID = sess.FeatureID()
					sess.Stop()
				}
			}
			// Badge clearing is not done on detach — it happens at response
			// time via HelpResolvedMsg → reconcileHelpQueue.
			// Save LastAttachedRepo for multi-repo features
			if repoName := m.attach.ActiveRepoName(); repoName != "" && m.attach.featureID != "" {
				_ = m.featureManager.Store.Modify(m.attach.featureID, func(f *feature.Feature) error {
					f.LastAttachedRepo = repoName
					return nil
				})
			}
			m.currentView = ViewDashboard
			return m, func() tea.Msg { return RefreshFeaturesMsg{} }
		}
		return m, cmd
	}
	// Forward all messages to artifact review model when in that view.
	if m.currentView == ViewArtifactReview {
		var cmd tea.Cmd
		m.artifactReview, cmd = m.artifactReview.Update(msg)
		if m.artifactReview.Detached() {
			if m.artifactReview.Decided() {
				m.artifactReview.StopSession()
			}
			m.clearReviewGateDashboardAttention(m.artifactReview.FeatureID())
			m.currentView = ViewDashboard
			return m, tea.Batch(cmd, func() tea.Msg { return RefreshFeaturesMsg{} })
		}
		return m, cmd
	}
	// Forward to chat panel when open
	if m.chatOpen {
		var cmd tea.Cmd
		m.chat, cmd = m.chat.Update(msg)
		return m, cmd
	}
	switch m.currentView {
	case ViewWizard:
		var cmd tea.Cmd
		m.wizard, cmd = m.wizard.Update(msg)
		m = m.handleWizardBrowseResult()
		m = m.handleWizardCreateRepoResult()
		return m, cmd
	case ViewPublish:
		var cmd tea.Cmd
		m.publish, cmd = m.publish.Update(msg)
		return m, cmd
	case ViewChat:
		var cmd tea.Cmd
		m.chat, cmd = m.chat.Update(msg)
		return m, cmd
	case ViewWelcome:
		var cmd tea.Cmd
		m.welcome, cmd = m.welcome.Update(msg)
		return m, cmd
	}
	return m, nil
}

// hasActiveTextInput returns true when the user is typing into a text field.
// Used to suppress single-key shortcuts (like "q" for quit) that would
// otherwise swallow printable characters.
func (m AppModel) hasActiveTextInput() bool {
	if m.helpOverlayActive {
		return true
	}
	if m.workspaceManagerActive {
		return true
	}
	if m.helpInputActive {
		return true
	}
	if m.currentView == ViewWizard && m.wizard.isTextInputStep() {
		return true
	}
	if m.refactorInputActive {
		return true
	}
	if m.chatOpen {
		return true
	}
	if m.currentView == ViewChat {
		return true
	}
	if m.currentView == ViewAttach {
		return true
	}
	if m.currentView == ViewArtifactReview {
		return true
	}
	if m.currentView == ViewPublish && m.publish.step == publishStepPRDesc {
		return true
	}
	if m.currentView == ViewWelcome && m.welcome.step == welcomeStepPicker {
		return true
	}
	return false
}

// rightPanelContentWidth returns the approximate content width inside the
// dashboard's right panel (after subtracting borders and padding).
func (m AppModel) rightPanelContentWidth() int {
	w := m.width
	if w < 80 {
		return max(w-6, 20) // narrow: single panel
	}
	var leftPct int
	if w > 120 {
		leftPct = 30
	} else {
		leftPct = 35
	}
	leftWidth := w*leftPct/100 - 2
	rightWidth := w - leftWidth - 4
	// Content area inside the right panel (subtract border+padding)
	return max(rightWidth-4, 20)
}

// hasAdvancedPast returns true if the feature has already moved beyond the given phase,
// indicating that phase completion was already handled (e.g., via EventStatusSuccess).
// For implementation and planning, individual iteration sessions are managed by their
// respective loops (RunImplementationLoop / RunRoadmapPlanningLoop / RunPhasePlanningLoop) which send dedicated
// done messages (ImplementLoopDoneMsg / PlanLoopDoneMsg) when the full loop completes.
func hasAdvancedPast(f *feature.Feature, phase feature.Phase) bool {
	switch phase {
	case feature.PhaseKnowledgeBase:
		return f.Status != feature.StatusCreated && f.Status != feature.StatusBuildingKB
	case feature.PhaseInquire:
		return f.Status != feature.StatusCreated && f.Status != feature.StatusInquiring
	case feature.PhaseResearch:
		return f.Status != feature.StatusCreated && f.Status != feature.StatusResearching &&
			f.Status != feature.StatusInquireReady
	case feature.PhaseDesign:
		return f.Status != feature.StatusDesignReady && f.Status != feature.StatusDesigning
	case feature.PhasePlan:
		// Planning iterations are managed by the planning loops; skip per-session
		// artifact writing and PhaseCompletedMsg to avoid clobbering loop-owned
		// output files and emitting conflicting phase transitions.
		return true
	case feature.PhaseImplement:
		return true
	case feature.PhaseReview:
		// Review sessions are loop-internal (plan-validation and implementation
		// review gates). Their outcomes are governed by the parent loop
		// (PlanLoopDoneMsg / ImplementLoopDoneMsg), not phase-transition logic.
		// A transient review failure must not mark the feature as Failed.
		return true
	default:
		return false
	}
}

// resolvePhaseArtifact reads the artifact file written by the agent for a given phase.
// Returns the file contents if an artifact path was recorded and the file exists,
// or empty string otherwise.
func (m AppModel) resolvePhaseArtifact(f *feature.Feature, phase string) string {
	artifactPath, ok := f.Artifacts[phase]
	if !ok || artifactPath == "" {
		return ""
	}

	if filepath.IsAbs(artifactPath) {
		data, err := os.ReadFile(artifactPath)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(data))
	}

	if len(f.Repos) == 0 {
		return ""
	}
	repo := f.Repos[0]

	// Try worktree first, then fall back to main repo path.
	// Artifacts may live in the main repo when the planning session wrote
	// to a shared thoughts/ directory that isn't part of the worktree.
	candidates := []string{}
	if repo.WorktreePath != "" {
		candidates = append(candidates, filepath.Join(repo.WorktreePath, artifactPath))
	}
	if repo.Path != "" {
		candidates = append(candidates, filepath.Join(repo.Path, artifactPath))
	}

	for _, fullPath := range candidates {
		data, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		return strings.TrimSpace(string(data))
	}
	return ""
}

// globPhaseArtifact finds the best artifact file in a phase directory,
// excluding non-artifact files and selecting the most recently modified.
// globCyclePlan finds the plan file inside a cycle directory (e.g. rebase-plan.md,
// tweak-plan.md, review-plan.md). Returns empty string if none found.
func globCyclePlan(cycleDir string) string {
	matches, _ := filepath.Glob(filepath.Join(cycleDir, "*-plan.md"))
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

func globPhaseArtifact(phaseDir string) string {
	var bestPath string
	var bestModTime int64

	matches, _ := filepath.Glob(filepath.Join(phaseDir, "*.md"))
	for _, m := range matches {
		if agent.IsArtifactExcluded(filepath.Base(m)) {
			continue
		}
		info, err := os.Stat(m)
		if err != nil {
			continue
		}
		if mt := info.ModTime().UnixNano(); bestPath == "" || mt > bestModTime {
			bestPath = m
			bestModTime = mt
		}
	}
	return bestPath
}

// resolvePhaseArtifactPath returns the absolute filesystem path to a phase
// artifact without reading its content. Downstream phases pass this path to
// the agent so it can read the document via tool use, keeping prompts small.
func (m AppModel) resolvePhaseArtifactPath(f *feature.Feature, phase string) string {
	artifactPath, ok := f.Artifacts[phase]
	if !ok || artifactPath == "" {
		// No stored artifact path — try the well-known phase directory.
		// Phase plan artifacts live in phase-NN/plan/ (not phase-N-plan/).
		phaseDir := m.resolvePhaseDirForKey(f, phase)
		return globPhaseArtifact(phaseDir)
	}

	if filepath.IsAbs(artifactPath) {
		if _, err := os.Stat(artifactPath); err == nil {
			return artifactPath
		}
		// Absolute path didn't resolve — fall through to phase-dir glob fallback.
	}

	if !filepath.IsAbs(artifactPath) {
		activeRunDir := agent.ActiveRunDir(m.featureManager.Store.BaseDir, f)

		// Run-relative form: values are relative to the active run directory,
		// e.g. `phase-01/plan/plan.md` for a roadmap pipeline's carried
		// "plan" key. Resolve by joining to ActiveRunDir directly, not to
		// `<ActiveRunDir>/<phase>/`, which would double-prefix the first path
		// segment.
		if activeRunDir != "" {
			if candidate := filepath.Join(activeRunDir, artifactPath); candidate != "" {
				if _, err := os.Stat(candidate); err == nil {
					return candidate
				}
			}
		}

		// Legacy basename form: values are relative to the phase's output
		// directory (e.g. `inquire.md` inside `<ActiveRunDir>/inquire/`).
		phaseDir := filepath.Join(activeRunDir, phase)
		candidate := filepath.Join(phaseDir, artifactPath)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}

		// Try repo worktree / repo path.
		if len(f.Repos) > 0 {
			repo := f.Repos[0]
			if repo.WorktreePath != "" {
				if _, err := os.Stat(filepath.Join(repo.WorktreePath, artifactPath)); err == nil {
					return filepath.Join(repo.WorktreePath, artifactPath)
				}
			}
			if repo.Path != "" {
				if _, err := os.Stat(filepath.Join(repo.Path, artifactPath)); err == nil {
					return filepath.Join(repo.Path, artifactPath)
				}
			}
		}
	}

	// Last resort: glob for artifact files in the phase directory, excluding
	// debug/infrastructure files (system-prompt, user-prompt, validation-*, etc.).
	phaseDir := m.resolvePhaseDirForKey(f, phase)
	return globPhaseArtifact(phaseDir)
}

// resolvePhaseDirForKey maps an artifact key to the correct filesystem directory.
// Phase plan keys (e.g. "phase-2-plan") map to phase-NN/plan/ directories.
func (m AppModel) resolvePhaseDirForKey(f *feature.Feature, key string) string {
	var phaseNum int
	if _, err := fmt.Sscanf(key, "phase-%d-plan", &phaseNum); err == nil && phaseNum > 0 {
		return agent.PhasePlanDir(m.featureManager.Store.BaseDir, f, phaseNum)
	}
	return filepath.Join(agent.ActiveRunDir(m.featureManager.Store.BaseDir, f), key)
}

// logPhaseError writes an error message to a debug log file for diagnosing
// phase start failures. Routes through agent.LogPhaseError so the log nests
// under the feature's active run (runs/run-NNN/<phase>/error.log). When the
// feature can't be resolved, the error is silently dropped — best-effort
// diagnostics, not authoritative state. Looking up the feature here (rather
// than taking a *feature.Feature parameter) keeps the helper ergonomic for
// the many call sites that only have a feature ID in scope.
func (m AppModel) logPhaseError(featureID, phase, msg string) {
	if m.featureManager == nil || featureID == "" || phase == "" {
		return
	}
	f, err := m.featureManager.Get(featureID)
	if err != nil || f == nil {
		return
	}
	agent.LogPhaseError(m.featureManager.Store.BaseDir, f, phase, msg)
}

// extractRefactorPrompt extracts the user's prompt from a refactor plan file.
// Plan format: "# Refactor: <repoName>\n\n<prompt>\n"
func extractRefactorPrompt(content string) string {
	lines := strings.SplitN(content, "\n\n", 2)
	if len(lines) >= 2 {
		return strings.TrimSpace(lines[1])
	}
	return strings.TrimSpace(content)
}

// Helper functions

// identityFallbackWarned tracks sessionIDs for which the string-parse
// fallback has already emitted a warning, so we don't flood the log on
// every event from the same session. Keyed by sessionID; value is an
// empty struct.
var identityFallbackWarned sync.Map

// warnIdentityFallback logs a warn-once message for a sessionID when the
// string-parse fallback is taken because a caller did not populate
// FeatureID on the event. Deduped per sessionID.
func warnIdentityFallback(sessionID string) {
	if _, loaded := identityFallbackWarned.LoadOrStore(sessionID, struct{}{}); loaded {
		return
	}
	log.Printf("tui: identity fallback used — sessionID=%q featureID-empty=true", sessionID)
}

// FALLBACK ONLY. All production paths set FeatureID/Phase on the event
// struct at emission time (see internal/session/manager.go). This helper
// exists for legacy test fixtures that construct events without those
// fields. The fallback emits a warn-once log so a production regression
// surfaces in the TUI log instead of silently misrouting events.
func eventFID(sessionID, featureID string) string {
	if featureID != "" {
		return featureID
	}
	warnIdentityFallback(sessionID)
	return featureIDFromSession(sessionID)
}

// FALLBACK ONLY. Mirrors eventFID for the phase carried on a session
// event. A non-empty FeatureID on the event proves the phase was also
// populated (they are written together at the manager), so we gate on
// FeatureID. See eventFID for rationale — this helper exists only for
// legacy test fixtures.
func eventPhase(sessionID, featureID string, phase feature.Phase) feature.Phase {
	if featureID != "" {
		return phase
	}
	warnIdentityFallback(sessionID)
	return phaseFromSessionID(sessionID)
}

// parserFallbackWarned tracks sessionIDs for which a raw string parser
// (featureIDFromSession / phaseFromSessionID / phaseFromPhaseSessionID)
// has already warned, deduped per parser+sessionID pair.
var parserFallbackWarned sync.Map

func warnParserFallback(parser, sessionID string) {
	key := parser + "|" + sessionID
	if _, loaded := parserFallbackWarned.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	log.Printf("tui: identity string-parse fallback used — parser=%s sessionID=%q", parser, sessionID)
}

// FALLBACK ONLY. featureIDFromSession extracts the feature ID from a
// session ID. Session IDs follow the format
// "<featureID>-<phase>[-<suffix>]". Implementation sessions use
// "<featureID>-impl[-<repoName>]-<NN>" where the repo name may contain
// substrings like "-plan" or "-research", so we match the "-impl-"
// boundary first to avoid false positives.
//
// This helper is FALLBACK ONLY. Production events carry FeatureID
// directly on SDKEventMsg / SessionDoneMsg (see
// internal/session/manager.go). The helper is retained for legacy test
// fixtures and the TUI fallback path; it emits a warn-once log so a
// regression that drops the structured field surfaces in the logs.
func featureIDFromSession(sessionID string) string {
	warnParserFallback("featureIDFromSession", sessionID)
	// Implementation sessions: match first "-impl-" to avoid repo names
	// containing phase keywords (e.g., "deploy-plan" matching "-plan").
	if idx := strings.Index(sessionID, "-impl-"); idx > 0 {
		return stripPhaseSuffix(sessionID[:idx])
	}

	// Per-repo KB sessions: "<featureID>-kb-<repoName>" — the repo name
	// may contain phase-like substrings (e.g., "data-research", "code-review-tool"),
	// so we must anchor on the first "-kb-" before scanning for other patterns.
	if idx := strings.Index(sessionID, "-kb-"); idx > 0 {
		return stripPhaseSuffix(sessionID[:idx])
	}

	// Fix-agent sessions: "<featureID>-fix[-<repoName>]-<NN>" emitted from the
	// final-review loop. Without this anchor, the trailing "-NN" would fall
	// through to the suffix loop and match nothing, leaving the whole session
	// ID mis-classified as a feature ID. See internal/agent/final_review.go
	// (sessionID construction) for the emitter.
	if idx := strings.Index(sessionID, "-fix-"); idx > 0 {
		return stripPhaseSuffix(sessionID[:idx])
	}

	// Non-implementation sessions: "<featureID>-<phase>[-<suffix>]"
	// Includes roadmap/phase patterns from two-tier roadmap system.
	suffixes := []string{"-artifact-review", "-inquire", "-design", "-design", "-research", "-roadmap-", "-phase-", "-plan", "-review-", "-kb"}
	for _, suffix := range suffixes {
		idx := findLastIndex(sessionID, suffix)
		if idx > 0 {
			return sessionID[:idx]
		}
	}
	return sessionID
}

func findLastIndex(s, sub string) int {
	for i := len(s) - len(sub); i >= 0; i-- {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// stripPhaseSuffix removes a trailing "-phase-NN" from s, if present.
// This handles phase-scoped session IDs like "abc123-phase-01-impl-01"
// where the prefix before "-impl-" is "abc123-phase-01".
func stripPhaseSuffix(s string) string {
	idx := findLastIndex(s, "-phase-")
	if idx <= 0 {
		return s
	}
	// Verify everything after "-phase-" is digits (the phase number).
	suffix := s[idx+len("-phase-"):]
	for _, c := range suffix {
		if c < '0' || c > '9' {
			return s
		}
	}
	return s[:idx]
}

// repoNameFromKBSession extracts the repo name from a per-repo KB session ID.
// Formats: "<featureID>-kb-<repoName>" and
// "<featureID>-kb-<repoName>-NN" → "<repoName>".
// Returns "" if not a per-repo KB session (legacy "<featureID>-kb" format).
func repoNameFromKBSession(sessionID string) string {
	return agent.RepoNameFromKBSession(sessionID)
}

// FALLBACK ONLY. phaseFromSessionID determines the feature phase from a
// session ID. Implementation sessions use the format
// <featureID>-impl-<repoName>-<iter>, where repoName may contain
// phase-like substrings (e.g. "data-research", "code-review-tool"). We
// must match -impl- first to avoid mis-classification.
//
// Production events carry Phase directly on SDKEventMsg /
// SessionDoneMsg — see internal/session/manager.go. Only retained for
// legacy test fixtures and the TUI fallback path. Emits a warn-once log
// so regressions surface.
func phaseFromSessionID(sessionID string) feature.Phase {
	warnParserFallback("phaseFromSessionID", sessionID)
	// Check -impl- first: repo names in multi-repo sessions can contain
	// substrings like "-research", "-review-", "-plan", "-kb" that would
	// otherwise match a different phase.
	if strings.Index(sessionID, "-impl-") > 0 {
		return feature.PhaseImplement
	}

	// Check -kb- early: per-repo KB sessions use "<featureID>-kb-<repoName>"
	// where repoName may contain phase-like substrings (e.g., "data-research",
	// "code-review-tool"). Anchoring on the first "-kb-" prevents misrouting.
	if strings.Index(sessionID, "-kb-") > 0 {
		return feature.PhaseKnowledgeBase
	}

	// Fix-agent sessions run inside the final-review loop; anchor on "-fix-"
	// so they resolve to PhaseReview rather than falling through to the
	// PhaseResearch default. See internal/agent/final_review.go.
	if strings.Index(sessionID, "-fix-") > 0 {
		return feature.PhaseReview
	}

	type entry struct {
		pattern string
		phase   feature.Phase
	}
	// Check roadmap/phase-scoped patterns first (most specific)
	if findLastIndex(sessionID, "-roadmap-validate-") > 0 {
		return feature.PhaseReview
	}
	if findLastIndex(sessionID, "-phase-") > 0 {
		return phaseFromPhaseSessionID(sessionID)
	}
	if findLastIndex(sessionID, "-roadmap-") > 0 {
		return feature.PhasePlan
	}

	// Ordered from most specific to least specific.
	patterns := []entry{
		{"-planreview-", feature.PhaseReview},
		{"-inquire", feature.PhaseInquire},
		{"-design", feature.PhaseDesign},
		{"-design", feature.PhaseDesign},
		{"-research", feature.PhaseResearch},
		{"-review-", feature.PhaseReview},
		{"-kb", feature.PhaseKnowledgeBase},
		{"-plan", feature.PhasePlan},
	}
	for _, e := range patterns {
		if findLastIndex(sessionID, e.pattern) > 0 {
			return e.phase
		}
	}
	return feature.PhaseResearch
}

// FALLBACK ONLY. phaseFromPhaseSessionID handles
// "featureID-phase-NN-plan-NN", "featureID-phase-NN-planreview-NN",
// "featureID-phase-NN-impl-NN", "featureID-phase-NN-review-NN" patterns.
// Retained only for legacy test fixtures; emits a warn-once log.
func phaseFromPhaseSessionID(sessionID string) feature.Phase {
	warnParserFallback("phaseFromPhaseSessionID", sessionID)
	if strings.Contains(sessionID, "-planreview-") {
		return feature.PhaseReview
	}
	if strings.Contains(sessionID, "-review-") {
		return feature.PhaseReview
	}
	if strings.Contains(sessionID, "-impl-") {
		return feature.PhaseImplement
	}
	if strings.Contains(sessionID, "-plan-") {
		return feature.PhasePlan
	}
	return feature.PhasePlan
}

func splitLastN(s string, n int) []string {
	lines := make([]string, 0)
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

func featureCanRecoverQuestion(f *feature.Feature) bool {
	if f == nil {
		return false
	}
	// Interrupted means the prior live session is gone. Do not resurrect
	// stale "agent has a question" badges from persisted transcripts on restart.
	switch f.Status {
	case feature.StatusInquiring, feature.StatusResearching, feature.StatusDesigning:
	default:
		return false
	}
	switch f.CurrentPhase {
	case feature.PhaseInquire, feature.PhaseResearch, feature.PhaseDesign:
		return true
	default:
		return false
	}
}

func phaseArtifactDir(baseDir string, f *feature.Feature, phase feature.Phase, sess session.SessionView) string {
	if f == nil {
		return ""
	}
	dir := filepath.Join(agent.ActiveRunDir(baseDir, f), phase.DirName())
	if phase != feature.PhaseImplement {
		return dir
	}

	iteration := f.CurrentIteration
	if sess != nil && sess.Iteration() > 0 {
		iteration = sess.Iteration()
	}
	if iteration <= 0 {
		return dir
	}
	return filepath.Join(dir, fmt.Sprintf("iteration-%02d", iteration))
}

func registryOwnedSingleShotPhase(phase feature.Phase) bool {
	return false
}

func phaseOutputSuggestsQuestion(output string) bool {
	blocks := parseTranscriptBlocks(output)
	for i := len(blocks) - 1; i >= 0; i-- {
		block := blocks[i]
		switch block.kind {
		case "tool_use_ask":
			return true
		case "assistant":
			if strings.Contains(block.text, "?") {
				return true
			}
			continue
		case "ignored":
			continue
		case "user", "tool_result":
			return false
		default:
			return false
		}
	}
	return false
}

type transcriptBlock struct {
	kind string
	text string
}

func parseTranscriptBlocks(output string) []transcriptBlock {
	lines := splitLastN(output, 200)
	blocks := make([]transcriptBlock, 0, len(lines))
	var current *transcriptBlock

	flush := func() {
		if current == nil {
			return
		}
		blocks = append(blocks, *current)
		current = nil
	}

	appendContinuation := func(line string) {
		if current == nil {
			blocks = append(blocks, transcriptBlock{kind: "other", text: line})
			return
		}
		if current.text != "" {
			current.text += "\n"
		}
		current.text += line
	}

	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		switch {
		case strings.HasPrefix(trimmed, "[assistant] "):
			flush()
			current = &transcriptBlock{
				kind: "assistant",
				text: strings.TrimSpace(strings.TrimPrefix(trimmed, "[assistant] ")),
			}
		case strings.HasPrefix(trimmed, "[user] "):
			flush()
			current = &transcriptBlock{
				kind: "user",
				text: strings.TrimSpace(strings.TrimPrefix(trimmed, "[user] ")),
			}
		case strings.HasPrefix(trimmed, "[tool_result] "):
			flush()
			blocks = append(blocks, transcriptBlock{kind: "tool_result", text: trimmed})
		case strings.HasPrefix(trimmed, "[tool_use] AskUserQuestion"):
			flush()
			blocks = append(blocks, transcriptBlock{kind: "tool_use_ask", text: trimmed})
		case strings.HasPrefix(trimmed, "[result] "),
			strings.HasPrefix(trimmed, "[rate_limit] "),
			strings.HasPrefix(trimmed, "[progress] "),
			strings.HasPrefix(trimmed, "[status] "),
			strings.HasPrefix(trimmed, "[init] "),
			strings.HasPrefix(trimmed, "[compact] "),
			strings.HasPrefix(trimmed, "[thinking] "):
			flush()
			blocks = append(blocks, transcriptBlock{kind: "ignored", text: trimmed})
		case strings.HasPrefix(trimmed, "["):
			flush()
			blocks = append(blocks, transcriptBlock{kind: "other", text: trimmed})
		default:
			appendContinuation(trimmed)
		}
	}

	flush()
	return blocks
}

func phaseNeedsUserInput(baseDir string, f *feature.Feature, phase feature.Phase, sess session.SessionView) bool {
	if !featureCanRecoverQuestion(f) || f.CurrentPhase != phase {
		return false
	}
	if sess != nil && sess.HasPendingAskUserQuestion() {
		return true
	}

	artifactDir := phaseArtifactDir(baseDir, f, phase, sess)
	if artifactDir == "" || agent.HasPhaseComplete(artifactDir) {
		return false
	}

	output := ""
	if sess != nil {
		output = sess.MessageLog().Text()
	}
	if strings.TrimSpace(output) == "" {
		data, err := os.ReadFile(filepath.Join(artifactDir, "output.txt"))
		if err != nil {
			return false
		}
		output = string(data)
	}
	return phaseOutputSuggestsQuestion(output)
}

// hasHelpRequestMessage checks if ANY help request (pending or resolved) exists
// with the given message. Not used for dedup (see hasPendingHelpRequestMessage).
func hasHelpRequestMessage(f *feature.Feature, question string) bool {
	for _, h := range f.HelpQueue {
		if sameManagedHelpMessage(h.Question, question) {
			return true
		}
	}
	return false
}

// hasPendingHelpRequestMessage checks if a PENDING help request exists with the
// given message. Unlike hasHelpRequestMessage, this ignores resolved entries,
// which is appropriate for signal-file-based detection that doesn't oscillate.
func hasPendingHelpRequestMessage(f *feature.Feature, question string) bool {
	for _, h := range f.HelpQueue {
		if sameManagedHelpMessage(h.Question, question) && h.Pending {
			return true
		}
	}
	return false
}

func ensurePendingQuestionHelp(f *feature.Feature) bool {
	normalizeManagedHelpQueue(f)
	clearPendingHelpByMessage(f, waitingInputHelpMessage)
	removeResolvedHelpByMessage(f, waitingInputHelpMessage)
	if hasPendingHelpRequestMessage(f, questionHelpMessage) {
		return false
	}
	f.HelpQueue = append(f.HelpQueue, feature.HelpRequest{
		Question: questionHelpMessage,
		Time:     time.Now(),
		Pending:  true,
	})
	return true
}

func ensurePendingWaitingInputHelp(f *feature.Feature) bool {
	normalizeManagedHelpQueue(f)
	if hasPendingHelpRequestMessage(f, questionHelpMessage) {
		clearPendingHelpByMessage(f, waitingInputHelpMessage)
		removeResolvedHelpByMessage(f, waitingInputHelpMessage)
		return false
	}
	if hasPendingHelpRequestMessage(f, waitingInputHelpMessage) {
		return false
	}
	f.HelpQueue = append(f.HelpQueue, feature.HelpRequest{
		Question: waitingInputHelpMessage,
		Time:     time.Now(),
		Pending:  true,
	})
	return true
}

// clearPendingHelpByMessage marks all pending help requests matching the given
// message as resolved (Pending = false). Keeping resolved entries prevents the
// tick handler from re-adding a duplicate when the session status hasn't been
// updated yet.
func clearPendingHelpByMessage(f *feature.Feature, message string) bool {
	cleared := false
	for i := range f.HelpQueue {
		if sameManagedHelpMessage(f.HelpQueue[i].Question, message) && f.HelpQueue[i].Pending {
			f.HelpQueue[i].Pending = false
			cleared = true
		}
	}
	return cleared
}

// reconcileHelpQueue aligns the feature's HelpQueue Pending flags with the
// current waiting status of all active sessions for the feature. It is the
// single authoritative post-action clearing primitive: after a user action
// that may change a session's waiting state (answer submitted, permission
// decided, freeform chat sent), emit HelpResolvedMsg and this runs.
//
// Only the generic question/waiting-input badges are managed here. API error
// help (prefix-based) has its own lifecycle tied to session done / attach and
// is intentionally untouched.
func (m *AppModel) reconcileHelpQueue(featureID string) {
	var hasWaitingHelp, hasWaitingPerm bool
	for _, s := range m.sessionManager.ActiveSessions() {
		if s.FeatureID() != featureID {
			continue
		}
		switch s.Status() {
		case session.SessionWaitingHelp:
			hasWaitingHelp = true
		case session.SessionWaitingPermission:
			hasWaitingPerm = true
		}
	}
	_ = m.featureManager.Store.Modify(featureID, func(f *feature.Feature) error {
		normalizeManagedHelpQueue(f)
		if !hasWaitingHelp {
			clearPendingHelpByMessage(f, questionHelpMessage)
		}
		if !hasWaitingHelp && !hasWaitingPerm {
			clearPendingHelpByMessage(f, waitingInputHelpMessage)
		}
		return nil
	})
}

// isRunningFeature returns true if the feature is actively running a phase
// or has active post-publish repo cycles (rebase/tweak/review-comments).
//
// Active cycles also run in StatusCodeReady (manual_publish: true keeps the
// feature there until the user presses [p]); every other key handler that
// dispatches a cycle (Rebase/Tweak/Refactor) already treats CodeReady and
// Published as equivalent, so the attach gate must too.
func isRunningFeature(f *feature.Feature) bool {
	if f.Status == feature.StatusBuildingKB ||
		f.Status == feature.StatusResearching ||
		f.Status == feature.StatusInquiring ||
		f.Status == feature.StatusDesigning ||
		f.Status == feature.StatusPlanning ||
		f.Status == feature.StatusImplementing ||
		f.IsReviewing() {
		return true
	}
	if f.Status == feature.StatusPublished || f.Status == feature.StatusCodeReady {
		return hasActiveRepoCycles(f)
	}
	return false
}

// isCompletedFeature returns true if the feature has finished (Published or Done).
func isCompletedFeature(f *feature.Feature) bool {
	return f.Status == feature.StatusPublished || f.Status == feature.StatusDone
}

// cycleRepoEntry describes a repo in the cycle repo selector overlay.
type cycleRepoEntry struct {
	Name        string
	Branch      string
	PRURL       string
	CycleStatus string // "idle", "running:<type>", "failed"
}

// buildCycleRepoEntries creates selector entries for a multi-repo Published feature.
func buildCycleRepoEntries(f *feature.Feature) []cycleRepoEntry {
	var entries []cycleRepoEntry
	for _, repo := range f.Repos {
		entry := cycleRepoEntry{
			Name:        repo.Name,
			Branch:      repo.Branch,
			CycleStatus: "idle",
		}
		if state, ok := f.RepoStates[repo.Name]; ok && state != nil && state.PRURL != "" {
			entry.PRURL = state.PRURL
		}
		if rc, ok := f.RepoCycles[repo.Name]; ok {
			if rc.Status == "running" {
				entry.CycleStatus = "running:" + string(rc.Type)
			} else if rc.Status == "reviewing" {
				entry.CycleStatus = "reviewing:" + string(rc.Type)
			} else if rc.LastError != "" {
				entry.CycleStatus = "failed"
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

// dispatchRepoCycleCmd routes a per-repo cycle action to the appropriate command.
func (m AppModel) dispatchRepoCycleCmd(featureID, repoName string, action feature.RepoCycleType) tea.Cmd {
	switch action {
	case feature.CycleRebase:
		return m.rebaseCmd(featureID, repoName)
	case feature.CycleTweak:
		return m.startInteractiveTweakCmd(featureID, repoName)
	case feature.CycleReviewComments:
		return m.fetchReviewCommentsCmd(featureID, repoName)
	case feature.CycleRefactor:
		return func() tea.Msg {
			return showRefactorForRepoMsg{FeatureID: featureID, RepoName: repoName}
		}
	}
	return nil
}

// openCycleSelector activates the cycle repo selector overlay.
func (m *AppModel) openCycleSelector(featureID string, action feature.RepoCycleType) {
	f, err := m.featureManager.Get(featureID)
	if err != nil {
		return
	}
	m.cycleSelectActive = true
	m.cycleSelectFeatureID = featureID
	m.cycleSelectAction = action
	m.cycleSelectRepos = buildCycleRepoEntries(f)
	m.cycleSelectCursor = 0
}

// hasRunningRefactorCycle returns true if any repo on the feature has a running CycleRefactor cycle.
func hasRunningRefactorCycle(f *feature.Feature) bool {
	for _, rc := range f.RepoCycles {
		if rc.Type == feature.CycleRefactor && (rc.Status == "running" || rc.Status == "reviewing") {
			return true
		}
	}
	return false
}

// hasActiveRepoCycles returns true if the feature has any running per-repo cycles.
func hasActiveRepoCycles(f *feature.Feature) bool {
	return f.HasActiveRepoCycles()
}

func hasInterruptibleRepoCycles(f *feature.Feature) bool {
	if f == nil {
		return false
	}
	for _, rc := range f.RepoCycles {
		if rc == nil {
			continue
		}
		switch rc.Type {
		case feature.CycleRebase, feature.CycleReviewComments, feature.CycleRefactor:
		default:
			continue
		}
		if rc.Status == feature.RepoCycleRunning || rc.Status == feature.RepoCycleReviewing {
			return true
		}
	}
	return false
}

// isFeatureQuiescent reports whether the `e` key should offer the edit-
// config overlay for this feature. Mirrors the quiescence predicate used
// inside Orchestrator.UpdateFeatureConfig's Store.Modify closure.
func isFeatureQuiescent(f *feature.Feature) bool {
	if f == nil {
		return false
	}
	return !f.Status.IsRunning() && !f.HasActiveRepoCycles() && !f.Status.IsNeedsReview()
}

// editConfigResultMsg signals completion of a UpdateFeatureConfig call from
// saveConfigCmd. This is the authoritative close signal for the overlay: on
// success the handler clears editConfigActive immediately and issues a
// feature refresh. The orchestrator also emits FeatureConfigChanged →
// OrchFeatureConfigChangedMsg via the droppable non-blocking Events()
// channel; that is a secondary broadcast. On save error, the overlay stays
// open with an in-modal banner.
type editConfigResultMsg struct {
	featureID string
	err       error
}

// saveConfigCmd dispatches UpdateFeatureConfig through the orchestratorAPI
// seam and returns an editConfigResultMsg.
func (m AppModel) saveConfigCmd(featureID string, input orchestrator.UpdateFeatureConfigInput, repos []string, pipeline feature.PipelineProfile, publishable bool) tea.Cmd {
	return func() tea.Msg {
		err := m.orchestrator.UpdateFeatureConfig(featureID, input)
		if err == nil {
			m.persistPipelinePreferences(repos, pipeline, input.Models, input.Inquireness, input.Checkpoints, publishable)
		}
		return editConfigResultMsg{featureID: featureID, err: err}
	}
}

// removeResolvedHelpByMessage removes all resolved (non-pending) help requests
// matching the given message. Called on session completion to prevent
// accumulation of stale resolved entries.
func removeResolvedHelpByMessage(f *feature.Feature, message string) {
	filtered := f.HelpQueue[:0]
	for _, h := range f.HelpQueue {
		if sameManagedHelpMessage(h.Question, message) && !h.Pending {
			continue
		}
		filtered = append(filtered, h)
	}
	f.HelpQueue = filtered
}

// clearPendingHelpByPrefix marks all pending help requests whose Question
// starts with the given prefix as resolved (Pending = false).
func clearPendingHelpByPrefix(f *feature.Feature, prefix string) bool {
	cleared := false
	for i := range f.HelpQueue {
		if strings.HasPrefix(f.HelpQueue[i].Question, prefix) && f.HelpQueue[i].Pending {
			f.HelpQueue[i].Pending = false
			cleared = true
		}
	}
	return cleared
}

// removeResolvedHelpByPrefix removes all resolved (non-pending) help requests
// whose Question starts with the given prefix.
func removeResolvedHelpByPrefix(f *feature.Feature, prefix string) {
	filtered := f.HelpQueue[:0]
	for _, h := range f.HelpQueue {
		if strings.HasPrefix(h.Question, prefix) && !h.Pending {
			continue
		}
		filtered = append(filtered, h)
	}
	f.HelpQueue = filtered
}

// extractRepoNameFromSessionID extracts the repo name from a per-repo session ID.
// Session ID format: <featureID>-impl-<repoName>-<iteration> or
//
//	<featureID>-review-<repoName>-<iteration>
//	<featureID>-kb-<repoName> (no iteration suffix)
//
// Returns empty string if the format doesn't match.
func extractRepoNameFromSessionID(sessionID, featureID string) string {
	// KB sessions: <featureID>-kb-<repoName> (no iteration suffix)
	kbPrefix := featureID + "-kb-"
	if rest := strings.TrimPrefix(sessionID, kbPrefix); rest != sessionID {
		if rest != "" {
			return rest
		}
	}

	for _, phase := range []string{"-impl-", "-review-"} {
		prefix := featureID + phase
		rest := strings.TrimPrefix(sessionID, prefix)
		if rest == sessionID {
			continue
		}
		// rest is "<repoName>-<iteration>" e.g. "auth-service-01"
		lastDash := strings.LastIndex(rest, "-")
		if lastDash <= 0 {
			return "" // no iteration suffix found
		}
		// Verify the part after the last dash looks like an iteration (digits)
		suffix := rest[lastDash+1:]
		isDigits := len(suffix) > 0
		for _, c := range suffix {
			if c < '0' || c > '9' {
				isDigits = false
				break
			}
		}
		if !isDigits {
			return "" // not a valid per-repo session ID
		}
		return rest[:lastDash]
	}
	return ""
}

// resolveInitialTab returns the tab index to start on when attaching.
func resolveInitialTab(tabs []repoTab, lastAttachedRepo string) int {
	if lastAttachedRepo != "" {
		for i, t := range tabs {
			if t.repoName == lastAttachedRepo && t.sess != nil {
				return i
			}
		}
	}
	for i, t := range tabs {
		if t.sess != nil {
			return i
		}
	}
	return -1
}

// buildRepoTabs constructs a repoTab slice from the feature's active sessions.
//
// The tab list is unified: for multi-repo features the per-repo implementation
// tabs are emitted first (one per repo, active or not — inactive tabs render
// disabled), followed by validator-critic tabs, review-helper tabs, tweak tabs,
// and any remaining active phase sessions.
//
// A feature with a single active session returns a single-element slice; the
// tab bar is hidden in that case (renderTabBar short-circuits when len<=1).
func (m *AppModel) buildRepoTabs(f *feature.Feature) []repoTab {
	allSessions := m.sessionManager.FeatureSessions(f.ID)

	// Bucket active sessions by kind for ordered emission.
	var (
		validatorSess    = make(map[string]session.SessionView) // label -> session
		reviewHelperSess []session.SessionView
		tweakSess        []session.SessionView
		phaseSess        []session.SessionView
	)
	sessionByRepo := make(map[string]session.SessionView)
	for _, s := range allSessions {
		if !isGenericFeatureSession(s) || !s.IsActive() {
			continue
		}
		switch s.Kind() {
		case ports.KindValidator:
			if lbl := s.Label(); lbl != "" {
				validatorSess[lbl] = s
			}
		case ports.KindReviewHelper:
			reviewHelperSess = append(reviewHelperSess, s)
		case ports.KindTweak:
			tweakSess = append(tweakSess, s)
		case ports.KindRepoImpl:
			repoName := s.RepoName()
			if repoName == "" {
				repoName = extractRepoNameFromSessionID(s.ID(), f.ID)
			}
			if repoName == "" {
				continue
			}
			if existing, ok := sessionByRepo[repoName]; !ok || s.StartedAt().After(existing.StartedAt()) {
				sessionByRepo[repoName] = s
			}
		default: // KindPhase
			repoName := s.RepoName()
			if repoName == "" {
				repoName = extractRepoNameFromSessionID(s.ID(), f.ID)
			}
			if repoName != "" {
				// Phase session scoped to a repo (older multi-repo code paths);
				// treat as a repo tab for backward-compat.
				if existing, ok := sessionByRepo[repoName]; !ok || s.StartedAt().After(existing.StartedAt()) {
					sessionByRepo[repoName] = s
				}
			} else {
				phaseSess = append(phaseSess, s)
			}
		}
	}

	var tabs []repoTab

	// Group 1: repo tabs (one per declared repo for multi-repo features, or
	// any repo-scoped sessions for other features).
	if len(f.Repos) > 0 {
		orderedRepos := repoDisplayOrder(f)
		for _, repoName := range orderedRepos {
			status := statusPending
			if f.Status == feature.StatusBuildingKB {
				if kbs, ok := f.KBStatus[repoName]; ok {
					switch {
					case kbs == "completed":
						status = statusReviewPassed
					case strings.HasPrefix(kbs, "failed"):
						status = statusFailed
					default:
						status = statusImplementing
					}
				}
			} else if f.Status == feature.StatusPublished && len(f.RepoCycles) > 0 {
				if rc, ok := f.RepoCycles[repoName]; ok {
					switch rc.Status {
					case "running":
						status = statusImplementing
					case "failed":
						status = statusFailed
					default:
						status = statusPending
					}
				}
			} else if state, ok := f.RepoStates[repoName]; ok && state != nil {
				status = repoStateToPresentationStatus(state)
			}

			sess := sessionByRepo[repoName]
			// Only include the repo tab at all if the feature is truly
			// multi-repo, OR if there's an active session for it. Otherwise a
			// single-repo feature with only a phase session would get a
			// confusing "pending" placeholder tab.
			if len(f.Repos) > 1 || sess != nil {
				tabs = append(tabs, repoTab{
					repoName: repoName,
					kind:     ports.KindRepoImpl,
					sess:     sess,
					status:   status,
				})
			}
			delete(sessionByRepo, repoName)
		}
	}
	// Any leftover repo-scoped sessions whose repo wasn't in f.Repos.
	// Sort alphabetically so the tab order is stable across poll ticks.
	if len(sessionByRepo) > 0 {
		leftover := make([]string, 0, len(sessionByRepo))
		for repoName := range sessionByRepo {
			leftover = append(leftover, repoName)
		}
		sort.Strings(leftover)
		for _, repoName := range leftover {
			tabs = append(tabs, repoTab{
				repoName: repoName,
				kind:     ports.KindRepoImpl,
				sess:     sessionByRepo[repoName],
				status:   statusImplementing,
			})
		}
	}

	// Group 2: validator tabs (one per active validator, always alphabetical
	// by domain for a stable tab order across refresh ticks — Go map
	// iteration is randomized, so anything that pulls directly from a map
	// would shuffle the tab bar on every poll).
	if len(validatorSess) > 0 {
		domainOrder := make([]string, 0, len(validatorSess))
		for d := range validatorSess {
			domainOrder = append(domainOrder, d)
		}
		sort.Strings(domainOrder)
		for _, domain := range domainOrder {
			sess := validatorSess[domain]
			status := statusImplementing
			if vs, ok := f.ValidatorStatuses[domain]; ok {
				status = validatorTabStatus(vs)
			}
			tabs = append(tabs, repoTab{
				// Stable identity across session rotation — using the domain
				// lets rebuildTabs recognize the same validator even if the
				// underlying session is restarted mid-validation.
				repoName: "validator:" + domain,
				label:    domain,
				kind:     ports.KindValidator,
				sess:     sess,
				status:   status,
			})
		}
	}

	// Group 3: review helpers.
	sort.Slice(reviewHelperSess, func(i, j int) bool {
		return reviewHelperSess[i].StartedAt().Before(reviewHelperSess[j].StartedAt())
	})
	for _, s := range reviewHelperSess {
		lbl := "Review"
		if s.Label() != "" {
			lbl = "Review · " + s.Label()
		}
		tabs = append(tabs, repoTab{
			repoName: "review:" + lbl,
			label:    lbl,
			kind:     ports.KindReviewHelper,
			sess:     s,
			status:   statusImplementing,
		})
	}

	// Group 4: tweak sessions.
	sort.Slice(tweakSess, func(i, j int) bool {
		return tweakSess[i].StartedAt().Before(tweakSess[j].StartedAt())
	})
	for _, s := range tweakSess {
		lbl := "Tweak"
		if s.Label() != "" {
			lbl = "Tweak · " + s.Label()
		}
		tabs = append(tabs, repoTab{
			repoName: "tweak:" + lbl,
			label:    lbl,
			kind:     ports.KindTweak,
			sess:     s,
			status:   statusImplementing,
		})
	}

	// Group 5: bare phase sessions (not scoped to a repo). Rare for multi-tab
	// scenarios — normally a feature has one active phase session at a time.
	sort.Slice(phaseSess, func(i, j int) bool {
		return phaseSess[i].StartedAt().Before(phaseSess[j].StartedAt())
	})
	for _, s := range phaseSess {
		lbl := phaseTabLabel(s.Phase())
		tabs = append(tabs, repoTab{
			repoName: "phase:" + lbl,
			label:    lbl,
			kind:     ports.KindPhase,
			sess:     s,
			status:   statusImplementing,
		})
	}

	return tabs
}

// phaseTabLabel returns a short human-readable label for a phase session tab.
func phaseTabLabel(p feature.Phase) string {
	switch p {
	case feature.PhaseResearch:
		return "Research"
	case feature.PhasePlan:
		return "Plan"
	case feature.PhaseImplement:
		return "Implement"
	case feature.PhaseReview:
		return "Review"
	default:
		return p.String()
	}
}

// attachToMultiRepoFeature gathers per-session tabs and opens the tabbed attach model.
// Retained under its original name for callers that explicitly want the tabbed
// view regardless of session count; attachToFeature is the preferred entry
// point and will fall back to a single-session view when only one tab exists.
func (m *AppModel) attachToMultiRepoFeature(f *feature.Feature) tea.Cmd {
	tabs := m.buildRepoTabs(f)
	initialIdx := resolveInitialAttentionTab(tabs, f.LastAttachedRepo)
	if initialIdx < 0 || tabs[initialIdx].sess == nil {
		// Fall back to single-session attach if no managed sessions found
		// (e.g., session from a previous TUI instance not yet recovered)
		sessions := m.sessionManager.ActiveSessions()
		for _, s := range sessions {
			if isGenericFeatureSession(s) && s.FeatureID() == f.ID {
				return m.attachToSession(s)
			}
		}
		return nil
	}
	m.attach = NewAttachModel(tabs, initialIdx, f.ID, m.width, m.height)
	if m.phaseRunner != nil {
		m.attach.skillsDir = m.phaseRunner.SkillsDir
	}
	m.attach.permCache = m.permissionCache
	m.attach.permRepoName = tabs[initialIdx].sess.PermCacheScope()
	m.attach.observer = m.observer
	m.attach.traceID = f.TraceID
	m.attach.featureName = f.Name
	m.attach.featureSpanID = f.FeatureSpanID
	m.attach.activeRun = f.ActiveRun
	for _, tab := range tabs {
		if tab.sess != nil && isTweakSessionID(tab.sess.ID()) {
			m.attach.isTweakSession = true
			break
		}
	}
	m.attach.emitRestoredObservability()
	m.currentView = ViewAttach
	return m.attach.Init()
}

// attachToFeature opens the tabbed multi-session attach view over every active
// session for the feature (phase sessions, per-repo implementation, validator
// critics, review helpers, tweak sessions). When only a single session is
// active the tab bar is hidden so the view degrades gracefully to the classic
// single-session attach look.
func (m *AppModel) attachToFeature(f *feature.Feature) tea.Cmd {
	tabs := m.buildRepoTabs(f)
	// Count tabs with live sessions — disabled (sess==nil) tabs don't count.
	liveTabs := 0
	for _, t := range tabs {
		if t.sess != nil {
			liveTabs++
		}
	}
	if liveTabs >= 2 || (liveTabs == 1 && len(tabs) >= 2) {
		// Multiple tabs to render: use the tabbed attach model.
		return m.attachToMultiRepoFeature(f)
	}
	// Exactly one live tab and no disabled siblings → attach directly.
	if liveTabs == 1 {
		for _, t := range tabs {
			if t.sess != nil {
				return m.attachToSession(t.sess)
			}
		}
	}
	// No tabs from the unified builder — fall back to the legacy discovery
	// path for sessions that predate the session-kind plumbing or for features
	// recovered from disk before a kind was stamped.
	sessions := m.sessionManager.ActiveSessions()
	for _, s := range sessions {
		if isGenericFeatureSession(s) && s.FeatureID() == f.ID {
			return m.attachToSession(s)
		}
	}
	allSessions := m.sessionManager.FeatureSessions(f.ID)
	for _, s := range allSessions {
		if isGenericFeatureSession(s) {
			return m.attachToSession(s)
		}
	}
	// Nothing to attach to. Surface a reason rather than silently no-op'ing
	// — the [a] hint stays visible during BuildingKB but a feature waiting on
	// another feature's kb.lock has zero live sessions.
	if f.Status == feature.StatusBuildingKB && f.KBWaitMessage != "" {
		m.statusMessage = "Watch unavailable: " + f.KBWaitMessage
	} else {
		m.statusMessage = "No active sessions to watch."
	}
	return nil
}
