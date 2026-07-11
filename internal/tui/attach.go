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
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
	"github.com/charmbracelet/x/ansi"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

// attachMsgsMsg carries new SDK messages from the attach channel to the model.
type attachMsgsMsg struct {
	generation int
	messages   []llm.SDKMessage
}

// attachDoneMsg signals the session has exited while attached.
type attachDoneMsg struct {
	generation int
}

// agentInterruptedMsg is the result of a Stop action in the tweak-session
// finish prompt. err is nil on success; non-nil when the interrupt could
// not be delivered.
type agentInterruptedMsg struct {
	err error
}

// agentToastClearMsg is dispatched after interruptToastDuration to clear
// the transient toast shown after an interrupt.
type agentToastClearMsg struct{}

// skillsLoadedMsg carries the loaded skills from async discovery.
type skillsLoadedMsg struct {
	items   []AutocompleteItem
	workDir string // originating work dir for stale-result detection
}

// attachFilter controls which message types are shown in the attach view.
type attachFilter int

const (
	filterAll      attachFilter = iota // show everything
	filterNoTools                      // hide tool use, tool progress, thinking
	filterTextOnly                     // only assistant text and user messages
)

const (
	attachMessagePlaceholder = "Type a message... (Enter to send)"
	attachAnswerPlaceholder  = "Type your answer..."

	minInputLines = 1 // textarea starts at 1 line
	maxInputLines = 6 // textarea grows to at most 6 lines

	questionPanelBaseMaxLines           = 20 // default AskUser panel cap for normal/small terminals
	questionPanelTallMaxLines           = 32 // upper bound so the transcript remains visible
	expandedQuestionPromptReservedLines = 4  // room for scroll indicators and the return hint

	interruptToastDuration = 10 * time.Second
)

// thinkingLineText is the transient status line shown while the agent is
// generating without a specific tool/task activity to report.
const thinkingLineText = "Thinking..."

func (f attachFilter) String() string {
	switch f {
	case filterNoTools:
		return "No Tools"
	case filterTextOnly:
		return "Text Only"
	default:
		return "All"
	}
}

func (f attachFilter) next() attachFilter {
	return (f + 1) % 3
}

// controlRequestStillPending reports whether the given requestID is still
// waiting on the session for a response. Returns true when the session
// view does not expose the multi-request API (older mocks). The TUI uses
// this before activating an AUQ or permission prompt to skip
// control_requests that have already been resolved upstream — e.g. by
// the session's forwarder synthesising a structured deny when the TUI
// was detached for too long.
func controlRequestStillPending(sess session.SessionView, requestID string) bool {
	if sess == nil || requestID == "" {
		return true
	}
	for _, p := range sess.PendingControlRequests() {
		if p != nil && p.RequestID == requestID {
			return true
		}
	}
	return false
}

func askUserQuestionsAlreadyAutoPicked(sess session.SessionView, questions []askUserQuestion) bool {
	if sess == nil || len(questions) == 0 {
		return false
	}
	answered := make(map[string]bool, len(questions))
	for _, pair := range sess.QALog() {
		if pair.AutoPicked {
			answered[pair.Question] = true
		}
	}
	if len(answered) == 0 {
		return false
	}
	for _, q := range questions {
		if !answered[q.Question] {
			return false
		}
	}
	return true
}

func appendMissingAutoPickedMessages(sess session.SessionView) {
	if sess == nil || sess.MessageLog() == nil {
		return
	}
	existing := map[string]int{}
	for _, msg := range sess.MessageLog().Messages() {
		if !msg.AutoPicked || msg.User == nil {
			continue
		}
		for _, block := range msg.User.Message.Content {
			if block.IsText() && block.Text != "" {
				existing[autoPickedMessageKey(msg.AutoPickQuestion, block.Text, msg.AutoPickConfidence)]++
			}
		}
	}
	for _, pair := range sess.QALog() {
		if !pair.AutoPicked || pair.Answer == "" {
			continue
		}
		key := autoPickedMessageKey(pair.Question, pair.Answer, pair.Confidence)
		if existing[key] > 0 {
			existing[key]--
			continue
		}
		sess.MessageLog().Append(llm.SDKMessage{
			Type:               "user",
			LocallyAppended:    true,
			AutoPicked:         true,
			AutoPickQuestion:   pair.Question,
			AutoPickConfidence: pair.Confidence,
			User: &llm.UserMessage{
				Message: llm.ConversationMsg{
					Role:    "user",
					Content: []llm.ContentBlock{{Type: "text", Text: pair.Answer}},
				},
			},
		})
	}
}

func autoPickedMessageKey(question, answer string, confidence float64) string {
	return fmt.Sprintf("%s\x00%s\x00%.6f", question, answer, confidence)
}

func (m *AttachModel) parseAskUserQuestionsForDisplay(input json.RawMessage) []askUserQuestion {
	questions := parseAskUserQuestions(input)
	if len(questions) == 0 {
		return nil
	}
	return m.enrichAskUserQuestionConfidence(questions)
}

func (m *AttachModel) enrichAskUserQuestionConfidence(questions []askUserQuestion) []askUserQuestion {
	if !askUserQuestionsNeedConfidence(questions) || m == nil || m.sess == nil || m.sess.MessageLog() == nil {
		return questions
	}
	blocks := m.sess.MessageLog().ToolUseBlocks()
	for i := len(blocks) - 1; i >= 0; i-- {
		block := blocks[i]
		if block.Name != toolNameAskUserQuestion || len(block.Input) == 0 {
			continue
		}
		source := parseAskUserQuestions(block.Input)
		if !askUserQuestionBundlesMatch(questions, source) {
			continue
		}
		return copyAskUserQuestionConfidence(questions, source)
	}
	return questions
}

func askUserQuestionsNeedConfidence(questions []askUserQuestion) bool {
	for _, q := range questions {
		for _, opt := range q.Options {
			if opt.Confidence == nil {
				return true
			}
		}
	}
	return false
}

func askUserQuestionBundlesMatch(a, b []askUserQuestion) bool {
	if len(a) == 0 || len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.TrimSpace(a[i].Question) != strings.TrimSpace(b[i].Question) ||
			strings.TrimSpace(a[i].Header) != strings.TrimSpace(b[i].Header) ||
			a[i].MultiSelect != b[i].MultiSelect ||
			len(a[i].Options) != len(b[i].Options) {
			return false
		}
		for j := range a[i].Options {
			if strings.TrimSpace(a[i].Options[j].Label) != strings.TrimSpace(b[i].Options[j].Label) ||
				strings.TrimSpace(a[i].Options[j].Description) != strings.TrimSpace(b[i].Options[j].Description) {
				return false
			}
		}
	}
	return true
}

func copyAskUserQuestionConfidence(questions, source []askUserQuestion) []askUserQuestion {
	enriched := make([]askUserQuestion, len(questions))
	for i := range questions {
		enriched[i] = questions[i]
		enriched[i].Options = make([]askUserOption, len(questions[i].Options))
		copy(enriched[i].Options, questions[i].Options)
		for j := range enriched[i].Options {
			if enriched[i].Options[j].Confidence == nil {
				enriched[i].Options[j].Confidence = source[i].Options[j].Confidence
			}
		}
	}
	return enriched
}

// presentationStatus is the attach tab bar's per-tab rendering token. It
// carries durable end-of-phase outcomes plus mid-flight presentation-only
// values (Implementing, Reviewing, FinalReviewing, Blocked, Waiting) used to
// drive tab spinners and active-state coloring.
type presentationStatus string

// Durable values are derived from RepoState (Touched + PRURL + LastError)
// via repoStateToPresentationStatus.
const (
	statusPending             presentationStatus = "pending"
	statusReviewPassed        presentationStatus = "review_passed"
	statusCodeReady           presentationStatus = "pr_ready"
	statusFailed              presentationStatus = "failed"
	statusAwaitingFinalReview presentationStatus = "awaiting_final_review"
	statusNeedUserInput       presentationStatus = "need_user_input"
	statusSkipped             presentationStatus = "skipped"
)

func repoStateToPresentationStatus(s *feature.RepoState) presentationStatus {
	if s == nil {
		return statusPending
	}
	switch {
	case s.LastError != "":
		return statusFailed
	case s.PRURL != "":
		return statusCodeReady
	case s.Touched:
		return statusAwaitingFinalReview
	default:
		return statusPending
	}
}

// Mid-flight presentation-only tokens. The TUI sets these on its own
// repoTab values to drive spinner / coloring; they are never persisted
// to RepoImpl[*].Status.
const (
	statusImplementing   presentationStatus = "implementing"
	statusReviewing      presentationStatus = "reviewing"
	statusFinalReviewing presentationStatus = "final_reviewing"
	statusBlocked        presentationStatus = "blocked"
	statusWaiting        presentationStatus = "waiting"
)

// abbreviateRepoName shortens a repo name for tab-bar display.
func abbreviateRepoName(name string) string {
	suffixes := []string{"-service", "-runner", "-platform", "-extract"}
	result := name
	for _, s := range suffixes {
		if strings.HasSuffix(result, s) {
			result = strings.TrimSuffix(result, s)
			break
		}
	}
	prefixes := []string{"services-"}
	for _, p := range prefixes {
		if strings.HasPrefix(result, p) {
			result = strings.TrimPrefix(result, p)
			break
		}
	}
	if len(result) > 5 {
		result = result[:5]
	}
	return result
}

// repoTab represents one tab in the multi-session attach tab bar.
// Originally used exclusively for per-repo implementation sessions, it now
// backs any attach view that cycles across multiple live sessions for a
// feature (phase sessions, validator critics, review helpers, tweak sessions).
//
// Identity fields:
//   - repoName: the tab's identity key. For repo-impl tabs this is the repo
//     name; for non-repo tabs (validators, helpers, etc.) this is the
//     session-identifier string so diffing during tab churn works on sessions,
//     not just repos.
//   - label: optional display override. When empty, the tab bar abbreviates
//     repoName. When set, label is rendered verbatim (e.g. "Architecture").
//   - kind: the session kind this tab represents. Zero value (KindPhase) is
//     the pre-existing behavior.
type repoTab struct {
	repoName string
	label    string
	kind     ports.SessionKind
	sess     session.SessionView
	status   presentationStatus

	// Per-tab pasted media state — preserved across tab switches so that
	// viewport placeholders survive round-trips (tab A → B → A).
	pastedImages    []string
	pastedFiles     []string
	pastedFileNames []string
}

type attachFileChange struct {
	Path         string
	OldPath      string
	Operation    string
	Detail       string
	AddedLines   int
	RemovedLines int
	HasDiffPatch bool // true when Detail is a compactPatch from git diff
}

type attachFileEvent struct {
	afterMessageCount int
	change            attachFileChange
}

type permissionAnswerFailedMsg struct {
	requestID string
	toolName  string
	toolInput json.RawMessage
	pattern   string
	choice    int
	err       error
}

// AttachModel is a Bubbletea model for the structured attach view.
// It subscribes to session.AttachCh() and renders messages inline
// using a viewport. No raw terminal mode is used.
//
// The attach model can render in read-only mode for specialized workflows
// that intentionally suppress chat input, but normal agent sessions now all
// share the same interactive transport path.
type AttachModel struct {
	viewport      viewport.Model
	input         textarea.Model
	sess          session.SessionView
	width         int
	height        int
	done          bool // session has exited
	detached      bool // user chose to detach
	readOnly      bool
	filter        attachFilter
	awaitingInput bool   // agent finished a turn, waiting for user response
	inputHeight   int    // current textarea height in lines (1-6), for chatPanelHeight()
	logPath       string // path to session log file, shown in footer

	// Permission prompt state (for control_request-based tools like Bash)
	pendingPermRequestID string
	pendingPermToolName  string
	pendingPermToolInput json.RawMessage // raw input JSON for display

	// AskUserQuestion multi-choice state — extracted from control_request
	// messages. Shows a numbered option list for each question.
	//
	// currentQuestionIdx ranges over [0, len(pendingQuestions)]. The sentinel
	// value len(pendingQuestions) means the user is on the "Review & Submit"
	// recap slot after answering the last question.
	pendingQuestions       []askUserQuestion // parsed questions with options
	questionStates         []questionUIState // parallel to pendingQuestions; per-question UI snapshots for back/forward nav
	currentQuestionIdx     int               // which question is being shown; == len(pendingQuestions) on recap
	selectedOption         int               // 0-based cursor; len(options) = "Type something" freeform
	selectedMulti          map[int]bool      // ticked option indices for the current multiSelect question
	questionScrollOffset   int               // first visible option index in windowed question list
	questionPromptExpanded bool              // true when the panel shows full question text instead of choices
	questionPromptScroll   int               // first visible visual line in expanded question view
	typingCustom           bool              // user selected "Type something" and is typing
	typingNotes            bool              // user pressed `n` and is typing notes for the current question
	pendingAskRequestID    string            // control_request ID for AskUserQuestion
	pendingAskQuestionsRaw json.RawMessage   // original questions JSON for control_response
	collectedAnswers       map[string]string // accumulated answers for multi-question flows
	collectedNotes         map[string]string // accumulated per-question notes (Claude annotations.notes)

	// pendingAskQueue holds AskUserQuestion bundles that arrived while the
	// user was still answering a previous one. The LLM can issue multiple
	// AskUserQuestion tool calls in parallel inside a single turn; this
	// queue makes the TUI sequence them — show the active bundle, then
	// pop the next when submitAllAnswers resolves the current. Without
	// this, a second control_request would overwrite the first's request
	// ID and the SDK would never receive a response for the orphaned one,
	// surfacing as a synthesised tool error to the LLM.
	pendingAskQueue []pendingAskBundle

	// Plan review mode state
	planReviewMode      bool   // true when attached to a plan review session
	showPlanReviewMenu  bool   // true when Ctrl+D menu is visible
	planReviewChoice    int    // 0 = Iterate more, 1 = Proceed
	planReviewFeatureID string // feature ID for the review

	// Rewind review mode state
	rewindReviewMode      bool          // true when attached to a rewind review session
	rewindReviewFeatureID string        // feature ID for the review
	rewindReviewPhase     feature.Phase // target phase to rewind to
	showRewindReviewMenu  bool          // true when Ctrl+D menu is visible

	// Multi-repo tab bar state
	repoTabs      []repoTab // nil/empty for single-session mode (single-repo or non-impl phases)
	activeTabIdx  int       // currently visible tab
	featureID     string    // for saving LastAttachedRepo on detach
	tabGeneration int       // incremented on tab switch; used to discard stale poll messages

	// Permission menu state (visual 3-option menu overlay)
	showPermMenu    bool   // true when permission menu is visible
	permMenuChoice  int    // 0=Allow, 1=Allow & Remember, 2=Deny
	permMenuPattern string // inferred pattern preview for "Allow & Remember" option

	// Shared permission cache for "Allow & Remember" (r key)
	permCache    *permission.Cache
	permRepoName string // repo name for permission scoping (non-empty even for single-repo features)

	// Observability
	observer      Observer
	traceID       string
	featureName   string
	featureSpanID string
	activeRun     int // mirrors f.ActiveRun at attach time

	// Tweak session state
	isTweakSession   bool // true when attached to a tweak session
	tweakFinishing   bool // set by Ctrl+D or finish prompt, read by the parent model during detach
	showFinishPrompt bool // true when Esc finish/detach prompt is visible

	// Transient toast shown after the Stop option in the finish prompt.
	// Cleared by agentToastClearMsg after interruptToastDuration.
	interruptToast   string
	interruptToastAt time.Time

	// Tool spinner state (single-line animated indicator, like chat model)
	thinkingLine   string    // current tool name/progress (overwritten on each update)
	spinnerView    string    // animated spinner frame, set by app-level spinner
	lastActivityAt time.Time // last time any message arrived from session
	// turnActive is true from the moment the user submits input until the
	// session emits a Result (or the session ends). While true, the spinner
	// badge unconditionally animates — silence on stdout is not treated as
	// idleness because the agent is known to be working on a turn. Reset on
	// Result arrival and on attach-done.
	turnActive bool

	// Image paste state
	canPasteImages bool   // true if clipboard image pasting is supported (macOS)
	imageTempDir   string // lazy-created temp dir for pasted images
	imageCounter   int    // running counter for numbering pasted images

	// Pasted media tracking (for display placeholders in viewport)
	pastedImages    []string // ordered pasted image temp paths
	pastedFiles     []string // ordered pasted file temp paths
	pastedFileNames []string // display names parallel to pastedFiles

	// Message queue indicator — tracks user messages sent while agent is processing
	queuedMessages []string // messages sent since last agent response (max ~3 displayed)

	// Cached git diff previews for file change cards, keyed by relative path.
	// Invalidated when message count changes (new tool activity).
	diffCache           map[string]*git.DiffPreview
	diffCacheGeneration int

	// Autocomplete dropdown state
	autocomplete AutocompleteModel

	// Skill autocomplete cache (loaded lazily on first "/" trigger)
	skillItems       []AutocompleteItem // nil = not loaded yet
	skillItemsLoaded bool               // true after first successful load
	skillsLoading    bool               // true while async load in flight
	skillsWorkDir    string             // work dir the load was started for (stale detection)
	skillsDir        string             // reconciled agentic skills directory on disk
	acceptedSkills   []AutocompleteItem // skills inserted via autocomplete (consumed on send)

	// File index state (@ autocomplete data source)
	fileIndex        *FileIndex // nil until first @ trigger; rebuilt on tab switch
	fileIndexLoading bool       // true while Build() is in progress
	fileIndexWorkDir string     // work dir the index was built for (stale detection)
}

type explicitPermissionResponder interface {
	RespondToPermissionDecision(requestID, decision, rememberPattern, rememberScope string) error
}

const (
	attachViewportMessageLimit = 1000
	attachViewportFileLimit    = 200
)

// IsScrollEvent reports whether a message is a viewport-scroll event we want
// to rate-limit: mouse wheel up/down, or ↑/↓ arrow keys (which terminals
// synthesize from trackpad wheel when mouse mode is off). Horizontal wheel
// and page keys are not rate-limited — horizontal is ignored by the viewport
// and page keys aren't subject to kinetic burst.
func IsScrollEvent(msg tea.Msg) bool {
	switch m := msg.(type) {
	case tea.MouseWheelMsg:
		b := m.Mouse().Button
		return b == tea.MouseWheelUp || b == tea.MouseWheelDown
	case tea.KeyPressMsg:
		s := m.String()
		return s == "up" || s == "down"
	}
	return false
}

// ScrollRateLimit is the minimum gap between scroll events that Bubble Tea
// will process. macOS trackpad kinetic scrolling fires dozens of wheel ticks
// (or synthesized arrow keys) per gesture; without throttling they pile up
// in the message queue, each paying full Update+View cost, and block higher-
// priority messages like Esc and typed characters behind them.
const ScrollRateLimit = 33 * time.Millisecond

// NewScrollRateLimiter returns a bubbletea message filter that drops scroll
// events (mouse wheel + ↑/↓) arriving faster than ScrollRateLimit. Dropped
// events are discarded before Update/View run, so Esc / typing / other
// control messages queued behind a kinetic burst reach the model quickly.
//
// Non-scroll events always pass through unchanged. The filter is stateful
// (closure over lastScrollAt) but the bubbletea event loop invokes it
// single-threaded, so no synchronization is needed.
func NewScrollRateLimiter() func(tea.Model, tea.Msg) tea.Msg {
	var lastScrollAt time.Time
	return func(_ tea.Model, msg tea.Msg) tea.Msg {
		if !IsScrollEvent(msg) {
			return msg
		}
		now := time.Now()
		if now.Sub(lastScrollAt) < ScrollRateLimit {
			return nil
		}
		lastScrollAt = now
		return msg
	}
}

// NewAttachModel creates an attach model with optional tab bar; renderTabBar
// collapses to empty when len(tabs) <= 1, so a single-entry tabs slice produces
// a model that renders byte-for-byte identical to the classic single-session
// attach view.
func NewAttachModel(tabs []repoTab, initialIdx int, featureID string, width, height int) AttachModel {
	activeSess := tabs[initialIdx].sess

	vpW, vpH, inputW := attachLayout(width, height, false, true)

	vp := viewport.New(viewport.WithWidth(vpW), viewport.WithHeight(vpH))
	vp.Style = lipgloss.NewStyle()
	vp.SoftWrap = true
	vp.KeyMap = viewport.KeyMap{
		Up:       key.NewBinding(key.WithKeys("up")),
		Down:     key.NewBinding(key.WithKeys("down")),
		PageUp:   key.NewBinding(key.WithKeys("pgup")),
		PageDown: key.NewBinding(key.WithKeys("pgdown")),
	}

	ti := newStyledTextarea()
	ti.Placeholder = attachMessagePlaceholder
	ti.CharLimit = 4096
	ti.SetWidth(inputW)
	ti.SetHeight(minInputLines)
	ti.ShowLineNumbers = false
	ti.Focus()

	logPath := activeSess.LogFilePath()

	m := AttachModel{
		viewport:      vp,
		input:         ti,
		sess:          activeSess,
		width:         width,
		height:        height,
		readOnly:      false,
		inputHeight:   minInputLines,
		logPath:       logPath,
		repoTabs:      tabs,
		activeTabIdx:  initialIdx,
		featureID:     featureID,
		tabGeneration: 0,
	}

	appendMissingAutoPickedMessages(activeSess)

	// Check for pending control requests on the active session.
	m.restorePendingAskUserQuestions(activeSess)
	if !m.hasActiveQuestion() {
		m.restorePendingPermission(activeSess)
	}
	// Fallback: scan message log for AskUserQuestion tool_use blocks.
	// This handles cases where the question appeared as a tool_use in an
	// assistant message (e.g., before the control protocol was activated,
	// or when the CLI auto-denied AskUserQuestion in old sessions).
	if !m.hasActiveQuestion() {
		allMsgs := activeSess.MessageLog().LastN(50)
		for i, scanMsg := range allMsgs {
			if scanMsg.Assistant != nil {
				for _, block := range scanMsg.Assistant.Message.Content {
					if block.IsToolUse() && block.Name == toolNameAskUserQuestion {
						// Check if a subsequent message answered this question:
						// look for a non-error tool_result with matching
						// tool_use_id, or a plain user message (text response).
						// Error tool_results indicate the CLI auto-denied the
						// question, so the user still needs to answer it.
						answered := false
						for j := i + 1; j < len(allMsgs); j++ {
							laterMsg := allMsgs[j]
							if laterMsg.User != nil {
								// Check if this user message has a tool_result
								// for our specific question.
								hasToolResult := false
								isErrorResult := false
								for _, lb := range laterMsg.User.Message.Content {
									if lb.Type == "tool_result" && lb.ToolUseID == block.ID {
										hasToolResult = true
										isErrorResult = lb.IsError
										break
									}
								}
								if hasToolResult && isErrorResult {
									// CLI auto-denied; keep scanning
									continue
								}
								if hasToolResult && !isErrorResult {
									// Successfully answered via tool_result
									answered = true
									break
								}
								// Plain user message counts as an answer
								answered = true
								break
							}
						}
						if answered {
							continue
						}
						if questions := m.parseAskUserQuestionsForDisplay(block.Input); len(questions) > 0 && !askUserQuestionsAlreadyAutoPicked(activeSess, questions) {
							m.activateAskUserQuestions(questions, "", block.Input)
						}
					}
				}
			}
		}
	}

	m.restoreThinkingLine()
	m.updateViewport()
	return m
}

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

// attachLayout computes viewport width, viewport height, and input width
// from the overall terminal dimensions, accounting for header, borders,
// the chat panel, and footer. When readOnly is true, the chat panel is
// omitted, giving the viewport more vertical space.
func attachLayout(width, height int, readOnly bool, hasTabBar bool) (vpWidth, vpHeight, inputWidth int) {
	const headerH = 3
	const footerH = 1
	const boxBorders = 2 // each panel's lipgloss Height includes +2 for border

	tabBarH := 0
	if hasTabBar {
		tabBarH = 1
	}

	panelW := max(width-2, 20)
	contentW := max(panelW-4, 10)

	// Chat panel height uses baseline (1-line input). The viewport is sized
	// for this baseline so it stays stable as the user types multi-line input.
	chatBoxH := 0
	if !readOnly {
		chatPanelH := minInputLines + 2
		chatBoxH = chatPanelH + boxBorders + 1 // content + border + "\n" gap
	}

	// Matches View() formula: height - headerH - tabBarH - chatBoxH - footerH - boxBorders - 1
	msgPanelH := max(height-headerH-tabBarH-chatBoxH-footerH-boxBorders-1, 6)
	vpH := max(msgPanelH-2, 4)

	return contentW, vpH, contentW
}

// questionContentWidth returns the inner content width of the chat panel,
// used for measuring wrapped line heights in question rendering.
// Must match attachLayout's contentW: panelW - border(2) - padding(2).
func (m AttachModel) questionContentWidth() int {
	panelW := max(m.width-2, 22)
	return max(panelW-4, 10) // subtract border(2) + padding(0,1)(2)
}

func (m AttachModel) questionPanelMaxHeight() int {
	if m.height <= 0 {
		return questionPanelBaseMaxLines
	}
	return min(max(questionPanelBaseMaxLines, m.height/2), questionPanelTallMaxLines)
}

func (m AttachModel) expandedQuestionPromptLineBudget() int {
	return max(m.questionPanelMaxHeight()-expandedQuestionPromptReservedLines, 1)
}

// chatPanelHeight returns the height needed for the chat panel.
func (m AttachModel) chatPanelHeight() int {
	if m.showFinishPrompt {
		// title (1) + blank (1) + options (2) + blank (1) + hint (1) = 6
		return 6
	}
	if m.showPermMenu {
		contentW := m.questionContentWidth()
		detail := formatPermissionDetail(m.pendingPermToolName, m.pendingPermToolInput)
		detailLines := permDetailLineCount(detail, contentW)
		// title (1) + detail (detailLines) + blank (1) + 3 options (3)
		// + separator (1) + hint (1) = detailLines + 7
		h := detailLines + 7
		if m.permMenuPattern != "" {
			h++ // pattern description line under "Allow & Remember"
		}
		return h
	}
	if m.onRecapSlot() {
		// Title (1) + blank (1) + N rows (each may wrap) + blank (1) + hint (1).
		contentW := m.questionContentWidth()
		rows := 0
		for _, q := range m.pendingQuestions {
			rows += recapRowLineCount(q, m.collectedAnswers, m.collectedNotes, contentW)
		}
		h := rows + 4 // title + blank + blank + hint
		if h < 5 {
			h = 5
		}
		if h > m.questionPanelMaxHeight() {
			h = m.questionPanelMaxHeight()
		}
		return h
	}
	if len(m.pendingQuestions) > 0 && m.currentQuestionIdx < len(m.pendingQuestions) {
		q := m.pendingQuestions[m.currentQuestionIdx]
		if m.questionPromptExpanded {
			return m.questionPanelMaxHeight()
		}
		if questionUsesDirectFreeform(q) {
			contentW := m.questionContentWidth()
			qLines := questionPromptLineCount(q.Question, contentW)
			// question text (qLines) + blank (1) + border (2) + hint (1) + blank-after-textarea (1)
			h := qLines + 5 + m.inputHeight
			if len(m.pendingQuestions) > 1 {
				h++ // progress indicator
			}
			if h > m.questionPanelMaxHeight() {
				h = m.questionPanelMaxHeight()
			}
			return h
		}
		// Width-aware overhead: question text (wrapped) + blank(1) + separator(1) +
		// "Type something"(1) + blank(1) + notes line(1) + blank(1) + hint(1).
		// Option lines use the visible window size (not total count).
		contentW := m.questionContentWidth()
		qLines := questionPromptLineCount(q.Question, contentW)
		overhead := qLines + 7 // blank(1) + separator(1) + "Type something"(1) + blank(1) + notes(1) + blank(1) + hint(1)
		if m.typingCustom {
			overhead += m.inputHeight + 1 // answer textarea + hint (1)
		}
		if m.typingNotes {
			overhead += m.inputHeight + 1 // notes textarea + its hint (1)
		}
		if len(m.pendingQuestions) > 1 {
			overhead++ // progress indicator
		}
		h := overhead + m.questionVisibleOptions()
		if h < 5 {
			h = 5
		}
		if h > m.questionPanelMaxHeight() {
			h = m.questionPanelMaxHeight()
		}
		return h
	}
	h := m.inputHeight + 2
	if len(m.queuedMessages) > 0 {
		h++ // queue indicator line
	}
	return h
}

// questionVisibleOptions returns the number of option lines (including scroll
// indicator lines, if overflow occurs) that fit in the chat panel for the
// current question. This is the total line budget for the option section within
// the question panel cap. Called by both chatPanelHeight and renderQuestion.
func (m AttachModel) questionVisibleOptions() int {
	if len(m.pendingQuestions) == 0 || m.currentQuestionIdx >= len(m.pendingQuestions) {
		return 0
	}
	q := m.pendingQuestions[m.currentQuestionIdx]

	// Width-aware overhead: question text (wrapped) + blank(1) + separator(1) +
	// "Type something"(1) + blank(1) + notes line(1) + blank(1) + hint(1).
	contentW := m.questionContentWidth()
	qLines := questionPromptLineCount(q.Question, contentW)
	overhead := qLines + 7
	if m.typingCustom {
		overhead += m.inputHeight + 1
	}
	if m.typingNotes {
		overhead += m.inputHeight + 1
	}
	if len(m.pendingQuestions) > 1 {
		overhead++ // progress indicator
	}

	maxOptionArea := max(m.questionPanelMaxHeight()-overhead, 1)

	totalLines := 0
	for i, o := range q.Options {
		totalLines += questionOptionLineCount(o, i, contentW)
	}

	if totalLines <= maxOptionArea {
		return totalLines
	}
	return maxOptionArea
}

// questionVisibleWindow computes the visible option range given the current
// scroll offset. Returns start (inclusive) and end (exclusive) option indices,
// plus whether above/below scroll indicators are needed.
func (m AttachModel) questionVisibleWindow() (start, end int, needAbove, needBelow bool) {
	q := m.pendingQuestions[m.currentQuestionIdx]
	return questionVisibleWindowPure(q.Options, m.selectedOption, m.questionScrollOffset, m.questionVisibleOptions(), m.questionContentWidth())
}

// updateQuestionScrollOffset adjusts questionScrollOffset so that
// selectedOption is visible within the windowed option list.
func (m *AttachModel) updateQuestionScrollOffset() {
	if len(m.pendingQuestions) == 0 || m.currentQuestionIdx >= len(m.pendingQuestions) {
		return
	}
	q := m.pendingQuestions[m.currentQuestionIdx]
	totalOptions := len(q.Options)

	// "Type something" (index == totalOptions) is always visible below the separator;
	// no scroll adjustment needed for it.
	if m.selectedOption >= totalOptions {
		return
	}

	contentW := m.questionContentWidth()
	totalLines := 0
	for i, o := range q.Options {
		totalLines += questionOptionLineCount(o, i, contentW)
	}

	optionArea := m.questionVisibleOptions()

	// No scrolling needed.
	if totalLines <= optionArea {
		m.questionScrollOffset = 0
		return
	}

	// If selected is above the window, scroll up.
	if m.selectedOption < m.questionScrollOffset {
		m.questionScrollOffset = m.selectedOption
		return
	}

	// Check if selected is visible from current offset.
	_, end, _, _ := m.questionVisibleWindow()
	if m.selectedOption < end {
		return // visible, no change needed
	}

	// Scroll down: find minimum offset so selectedOption is the last visible option.
	// Work backwards from selectedOption, counting lines until budget is exhausted.
	needAbove := true        // scrolling down means there are options above
	budget := optionArea - 1 // reserve for "above" indicator

	// Check if below indicator is needed (options exist after selectedOption).
	if m.selectedOption < totalOptions-1 {
		budget-- // reserve for "below" indicator
	}

	usedLines := 0
	newOffset := m.selectedOption
	for i := m.selectedOption; i >= 0; i-- {
		ol := questionOptionLineCount(q.Options[i], i, contentW)
		if usedLines+ol > budget {
			newOffset = i + 1
			break
		}
		usedLines += ol
		newOffset = i
	}

	// Ensure needAbove is actually true; if newOffset is 0, no indicator needed.
	if newOffset == 0 {
		_ = needAbove // already handled by questionVisibleWindow
	}

	m.questionScrollOffset = newOffset
}

// restoreThinkingLine scans recent messages to reconstruct the spinner state
// on re-attach. If the last assistant message contains a tool_use that hasn't
// been followed by a Result, show the tool name in the spinner.
func (m *AttachModel) restoreThinkingLine() {
	if m.sess == nil {
		return
	}
	msgs := m.sess.MessageLog().LastN(50)
	// Walk backwards to find the most recent meaningful state.
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if msg.Result != nil {
			// A Result means the turn completed — no active tool.
			return
		}
		if msg.Assistant != nil {
			for _, block := range msg.Assistant.Message.Content {
				if block.IsToolUse() {
					m.thinkingLine = fmt.Sprintf("Using %s...", block.Name)
					m.lastActivityAt = time.Now()
					m.turnActive = true
					return
				}
			}
			for _, block := range msg.Assistant.Message.Content {
				if block.IsThinking() {
					m.thinkingLine = thinkingLineText
					m.lastActivityAt = time.Now()
					m.turnActive = true
					return
				}
			}
		}
		if msg.ToolProgress != nil {
			m.thinkingLine = fmt.Sprintf("Using %s...", msg.ToolProgress.ToolName)
			m.lastActivityAt = time.Now()
			m.turnActive = true
			return
		}
	}
}

// preExpandInput grows the textarea by one line (up to maxInputLines) before
// a newline is inserted. This prevents the textarea viewport from scrolling
// prior lines out of view at the old (smaller) height.
func (m *AttachModel) preExpandInput() {
	h := growTextareaHeight(m.inputHeight)
	if h != m.inputHeight {
		m.inputHeight = h
		m.input.SetHeight(h)
	}
}

// syncInputHeight recalculates the textarea height based on content line count.
// Must be called after any operation that changes textarea content.
func (m *AttachModel) syncInputHeight() {
	h := syncTextareaHeight(m.input.Value(), minInputLines, maxInputLines)
	if h != m.inputHeight {
		m.inputHeight = h
		m.input.SetHeight(h)
	}
}

// Init starts the message polling and session done monitor.
func (m AttachModel) Init() tea.Cmd {
	gen := m.tabGeneration
	sess := m.sess
	return tea.Batch(
		textarea.Blink,
		drainAndPollAttachChCmd(sess, gen),
		waitForDoneCmd(sess, gen),
	)
}

// hasActiveQuestion returns true if the AskUserQuestion panel is visible —
// either on a specific question or on the "Review & Submit" recap slot that
// sits at index len(pendingQuestions).
func (m AttachModel) hasActiveQuestion() bool {
	return len(m.pendingQuestions) > 0 && m.currentQuestionIdx <= len(m.pendingQuestions)
}

// onRecapSlot reports whether the panel is currently showing the
// "Review & Submit" summary (one slot past the final question).
func (m AttachModel) onRecapSlot() bool {
	return len(m.pendingQuestions) > 0 && m.currentQuestionIdx == len(m.pendingQuestions)
}

// onQuestionSlot reports whether the panel is currently showing a real
// question (i.e., hasActiveQuestion but not the recap slot).
func (m AttachModel) onQuestionSlot() bool {
	return len(m.pendingQuestions) > 0 && m.currentQuestionIdx < len(m.pendingQuestions)
}

// forwardToViewport forwards a message to the viewport, short-circuiting
// scroll events that have already hit the top/bottom. This prevents kinetic
// wheel bursts from paying viewport-update cost per event once we can't
// scroll any further. Actual rate limiting across a burst is handled by
// scrollRateFilter at the program level (see cmd/agentic/main.go), which
// drops excess events before they ever reach Update.
func (m *AttachModel) forwardToViewport(msg tea.Msg) tea.Cmd {
	if IsScrollEvent(msg) {
		switch v := msg.(type) {
		case tea.MouseWheelMsg:
			if v.Mouse().Button == tea.MouseWheelUp && m.viewport.AtTop() {
				return nil
			}
			if v.Mouse().Button == tea.MouseWheelDown && m.viewport.AtBottom() {
				return nil
			}
		case tea.KeyPressMsg:
			if v.String() == "up" && m.viewport.AtTop() {
				return nil
			}
			if v.String() == "down" && m.viewport.AtBottom() {
				return nil
			}
		}
	}
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return cmd
}

// HasActiveQuestion is the exported form of hasActiveQuestion, used by the
// parent model to toggle bubbletea mouse capture on only while a question
// is pending — so the wheel scrolls the viewport instead of being synthesized
// into arrow keys by the terminal's alt-screen-scroll fallback.
func (m AttachModel) HasActiveQuestion() bool {
	return m.hasActiveQuestion()
}

func (m *AttachModel) syncInputMode() {
	if m.readOnly {
		return
	}
	switch {
	case m.hasActiveQuestion() && m.typingNotes:
		m.input.Placeholder = "Type notes…"
		m.input.Focus()
	case m.hasActiveQuestion() && m.typingCustom:
		m.input.Placeholder = attachAnswerPlaceholder
		m.input.Focus()
	case m.hasActiveQuestion():
		m.input.Placeholder = attachAnswerPlaceholder
		m.input.Blur()
	default:
		m.input.Placeholder = attachMessagePlaceholder
		m.input.Focus()
	}
}

func (m *AttachModel) markAgentProgress() {
	m.turnActive = true
	if !m.hasActiveQuestion() && !m.showPermMenu {
		m.awaitingInput = false
	}
}

func (m *AttachModel) restorePendingAskUserQuestions(sess session.SessionView) {
	if sess == nil {
		return
	}
	for _, cr := range sess.PendingControlRequests() {
		if cr == nil || cr.Request.ToolName != toolNameAskUserQuestion {
			continue
		}
		if !controlRequestStillPending(sess, cr.RequestID) {
			continue
		}
		questions := m.parseAskUserQuestionsForDisplay(cr.Request.Input)
		if len(questions) == 0 {
			continue
		}
		m.activateAskUserQuestions(questions, cr.RequestID, cr.Request.Input)
	}
}

func (m *AttachModel) restorePendingPermission(sess session.SessionView) bool {
	if sess == nil || sess.Status() != session.SessionWaitingPermission {
		return false
	}
	for _, cr := range sess.PendingControlRequests() {
		if cr == nil || cr.Request.ToolName == toolNameAskUserQuestion {
			continue
		}
		if !controlRequestStillPending(sess, cr.RequestID) {
			continue
		}
		m.activatePermissionRequest(cr)
		return true
	}
	return false
}

func (m *AttachModel) activatePermissionRequest(cr *llm.ControlRequestMessage) {
	if cr == nil {
		return
	}
	m.pendingPermRequestID = cr.RequestID
	m.pendingPermToolName = cr.Request.ToolName
	m.pendingPermToolInput = cr.Request.Input
	m.showPermMenu = true
	m.permMenuChoice = 0
	m.permMenuPattern = permission.InferBashPattern(cr.Request.ToolName, string(cr.Request.Input))
	m.emitPermissionRequested()
}

func (m *AttachModel) activateAskUserQuestions(questions []askUserQuestion, requestID string, raw json.RawMessage) {
	if len(questions) == 0 {
		return
	}
	// If the user is already in the middle of answering an AUQ bundle,
	// queue the new one instead of overwriting state. The current bundle
	// will pop the next from the queue on submitAllAnswers. Identical
	// requestIDs are treated as a duplicate delivery (e.g., during
	// re-attach) and ignored to avoid double-prompting.
	if m.hasActiveQuestion() {
		if requestID != "" && requestID == m.pendingAskRequestID {
			return
		}
		for _, b := range m.pendingAskQueue {
			if requestID != "" && b.requestID == requestID {
				return
			}
		}
		m.pendingAskQueue = append(m.pendingAskQueue, pendingAskBundle{
			questions: questions,
			requestID: requestID,
			raw:       raw,
		})
		return
	}
	m.pendingQuestions = questions
	m.questionStates = make([]questionUIState, len(questions))
	m.pendingAskRequestID = requestID
	m.pendingAskQuestionsRaw = raw
	m.collectedAnswers = make(map[string]string)
	m.collectedNotes = make(map[string]string)
	m.currentQuestionIdx = 0
	m.selectedOption = 0
	m.selectedMulti = nil
	m.questionScrollOffset = 0
	m.questionPromptExpanded = false
	m.questionPromptScroll = 0
	m.typingCustom = questionUsesDirectFreeform(questions[0])
	m.typingNotes = false
	m.awaitingInput = true
	m.input.Reset()
	m.syncInputHeight()
	m.syncInputMode()
	if len(m.pendingQuestions) > 0 {
		m.emitQuestionAsked(m.pendingQuestions[0])
		m.questionStates[0].askedEmitted = true
	}
}

// promoteNextPendingAsk activates the oldest queued AskUserQuestion bundle,
// if any. Called from submitAllAnswers after the active bundle is dispatched
// so concurrent requests are answered FIFO. Returns true if a bundle was
// promoted.
func (m *AttachModel) promoteNextPendingAsk() bool {
	for len(m.pendingAskQueue) > 0 {
		next := m.pendingAskQueue[0]
		m.pendingAskQueue = m.pendingAskQueue[1:]
		if !controlRequestStillPending(m.sess, next.requestID) {
			continue
		}
		// activateAskUserQuestions early-returns when hasActiveQuestion() is
		// true; submitAllAnswers has already cleared pendingQuestions before
		// reaching here, so the call falls into the activation path.
		m.activateAskUserQuestions(next.questions, next.requestID, next.raw)
		return true
	}
	return false
}

// sendChatInput sends the current textarea content as a user message.
// Returns nil cmd if the textarea is empty (no-op).
func (m *AttachModel) sendChatInput() tea.Cmd {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return nil
	}

	// Expand [Image #N] / [filename] placeholders back to real file paths
	// so the agent receives usable paths. Keep the placeholder form for
	// the queued-message display.
	var media *pastedMediaMap
	if len(m.pastedImages) > 0 || len(m.pastedFiles) > 0 {
		media = &pastedMediaMap{
			images:    m.pastedImages,
			files:     m.pastedFiles,
			fileNames: m.pastedFileNames,
		}
	}
	agentText := expandMediaPlaceholders(text, media)

	// Expand skill placeholders inserted by autocomplete: replace
	// "/name (source)" tokens with the real on-disk paths, just like
	// expandMediaPlaceholders does for [Image #N].
	agentText = expandSkillPlaceholders(agentText, m.acceptedSkills)

	// When the message leads with a skill path, wrap it in the standard
	// "read the skill instructions" preamble so the agent knows to
	// execute it as a methodology rather than a bare file reference.
	agentText = wrapLeadingSkillInstruction(agentText, m.acceptedSkills)

	m.input.Reset()
	m.syncInputHeight()
	m.awaitingInput = false
	// Track queued message for the visual indicator (placeholder form)
	m.queuedMessages = append(m.queuedMessages, text)
	m.pendingQuestions = nil
	// Mark the turn as active immediately so the spinner animates while the
	// agent processes the message — no matter how long it takes before the
	// first stream event arrives.
	m.turnActive = true
	m.lastActivityAt = time.Now()
	if m.thinkingLine == "" {
		m.thinkingLine = thinkingLineText
	}
	// Sending a new message is the user's answer to the interrupt toast —
	// clear it so it doesn't linger after they've moved on.
	m.interruptToast = ""
	m.interruptToastAt = time.Time{}
	m.sess.MessageLog().Append(llm.SDKMessage{
		Type:            "user",
		LocallyAppended: true,
		User: &llm.UserMessage{
			Message: llm.ConversationMsg{
				Role:    "user",
				Content: []llm.ContentBlock{{Type: "text", Text: agentText}},
			},
		},
	})
	featureID := m.sess.FeatureID()
	m.updateViewport()
	return func() tea.Msg {
		_ = m.sess.SendUserMessage(agentText)
		// SendUserMessage synchronously transitions SessionWaitingHelp →
		// SessionRunning, so reconcileHelpQueue will see the new state and
		// clear any pending question/waiting-input badges for this feature.
		return HelpResolvedMsg{FeatureID: featureID}
	}
}

// isInterruptResult reports whether a result message was produced by a
// user-initiated turn interrupt. Claude emits subtype
// "error_during_execution"; Codex emits subtype "error" with result
// "Turn interrupted". Both should render as a neutral "interrupted"
// marker rather than a scary "error" line.
func isInterruptResult(r *llm.ResultMessage) bool {
	if r == nil {
		return false
	}
	if r.Subtype == "error_during_execution" {
		return true
	}
	if r.Subtype == "error" && strings.EqualFold(strings.TrimSpace(r.Result), "turn interrupted") {
		return true
	}
	return false
}

// interruptAgentCmd returns a command that asks the session to cancel the
// current agent turn. Used by the tweak finish prompt's Stop option; the
// result is delivered as an agentInterruptedMsg which sets the toast.
func (m *AttachModel) interruptAgentCmd() tea.Cmd {
	sess := m.sess
	return func() tea.Msg {
		if sess == nil {
			return agentInterruptedMsg{err: fmt.Errorf("no active session")}
		}
		return agentInterruptedMsg{err: sess.Interrupt()}
	}
}

// skillPlaceholder returns the human-readable placeholder inserted into the
// textarea when a skill is autocompleted, e.g. "/research-codebase (built-in)".
func skillPlaceholder(item AutocompleteItem) string {
	return fmt.Sprintf("/%s (%s)", item.Name, sourceDisplayLabel(item.Source))
}

// expandSkillPlaceholders replaces "/name (source)" placeholders with the
// real on-disk skill paths. Mirrors expandMediaPlaceholders.
func expandSkillPlaceholders(text string, skills []AutocompleteItem) string {
	for _, sk := range skills {
		text = strings.ReplaceAll(text, skillPlaceholder(sk), sk.Path)
	}
	return text
}

// wrapLeadingSkillInstruction checks if the message starts with a skill path
// and wraps it in the standard preamble so the agent reads and follows the
// skill methodology. Mid-sentence skill paths are left as bare file references.
func wrapLeadingSkillInstruction(text string, skills []AutocompleteItem) string {
	for _, sk := range skills {
		if strings.HasPrefix(text, sk.Path) {
			rest := strings.TrimSpace(strings.TrimPrefix(text, sk.Path))
			return fmt.Sprintf(
				"Before starting your task, read the skill instructions at: %s\n\nRead the file completely, then follow its instructions as you work on the task below.\n\n%s",
				sk.Path, rest,
			)
		}
	}
	return text
}

// snapshotCurrentQuestion writes the live UI state into
// questionStates[currentQuestionIdx] so it can be restored if the user
// navigates back. No-op when not on a real question slot.
func (m *AttachModel) snapshotCurrentQuestion() {
	if !m.onQuestionSlot() || m.currentQuestionIdx >= len(m.questionStates) {
		return
	}
	st := &m.questionStates[m.currentQuestionIdx]
	st.selectedOption = m.selectedOption
	st.selectedMulti = cloneIntBoolMap(m.selectedMulti)
	st.scrollOffset = m.questionScrollOffset
	st.typingCustom = m.typingCustom
	if m.typingCustom {
		st.customText = m.input.Value()
	}
}

// restoreQuestion primes the live UI state for the question at idx. For a
// question the user has visited before, selection/ticks/scroll are rehydrated
// from the snapshot. Direct-freeform questions always land in typing mode; for
// option questions, we deliberately land on the option list (not typing mode)
// so left/right arrows can navigate between questions. If the prior answer
// came from "Type something" the cursor sits on that row with the stored text
// available for re-edit via Enter.
func (m *AttachModel) restoreQuestion(idx int) {
	if idx < 0 || idx >= len(m.pendingQuestions) {
		return
	}
	q := m.pendingQuestions[idx]
	st := m.questionStates[idx]
	m.typingNotes = false
	m.input.Reset()

	visited := st.askedEmitted
	if questionUsesDirectFreeform(q) {
		m.selectedOption = 0
		m.selectedMulti = nil
		m.questionScrollOffset = 0
		m.typingCustom = true
		if visited {
			m.input.SetValue(st.customText)
		}
		return
	}

	if visited {
		m.selectedOption = st.selectedOption
		m.selectedMulti = cloneIntBoolMap(st.selectedMulti)
		m.questionScrollOffset = st.scrollOffset
	} else {
		m.selectedOption = 0
		m.selectedMulti = nil
		m.questionScrollOffset = 0
	}
	// Always land on option-selection (not typing) so ←/→ navigate questions.
	// Freeform re-edit is reached by Enter on the "Type something" row.
	m.typingCustom = false
}

// commitCurrentAnswer records answer for the currently focused question and
// updates the question's UI snapshot so that back-nav rehydrates with the
// final picked state. The answer is NOT yet echoed into the chat log — that
// happens once per question at recap-submit time so repeated back-nav commits
// don't litter the log with stale picks.
func (m *AttachModel) commitCurrentAnswer(answer string) {
	if !m.onQuestionSlot() || m.collectedAnswers == nil {
		return
	}
	idx := m.currentQuestionIdx
	q := m.pendingQuestions[idx]
	m.collectedAnswers[q.Question] = answer
	m.emitQuestionAnswered(q.Question, answer)

	// Persist the final selection into the snapshot so back-nav reflects it.
	if idx < len(m.questionStates) {
		st := &m.questionStates[idx]
		st.scrollOffset = m.questionScrollOffset
		if m.typingCustom {
			st.typingCustom = true
			st.customText = answer
			st.selectedOption = len(q.Options) // "Type something" row
			st.selectedMulti = nil
		} else {
			st.typingCustom = false
			st.customText = ""
			st.selectedOption = m.selectedOption
			if q.MultiSelect {
				// Enter with no ticks implicitly picks the focused row — mirror
				// that so re-visiting shows the correct checkbox state.
				if len(m.selectedMulti) == 0 {
					st.selectedMulti = map[int]bool{m.selectedOption: true}
				} else {
					st.selectedMulti = cloneIntBoolMap(m.selectedMulti)
				}
			} else {
				st.selectedMulti = nil
			}
		}
	}
}

// advanceQuestion moves currentQuestionIdx by delta (typically +1 or -1),
// snapshotting the current question before moving and restoring the target
// question's state afterward. Clamps to [0, len(pendingQuestions)] — the upper
// bound is the "Review & Submit" recap slot. Returns true when navigation
// actually happened.
func (m *AttachModel) advanceQuestion(delta int) bool {
	return m.advanceQuestionOpts(delta, true)
}

// advanceQuestionOpts is the low-level nav helper. Pass snapshot=false when
// the caller has already written authoritative state (e.g., commitCurrentAnswer)
// and the live UI scalars may have been reset for the next render.
func (m *AttachModel) advanceQuestionOpts(delta int, snapshot bool) bool {
	if len(m.pendingQuestions) == 0 {
		return false
	}
	newIdx := m.currentQuestionIdx + delta
	maxIdx := len(m.pendingQuestions) // recap slot lives here
	if newIdx < 0 || newIdx > maxIdx || newIdx == m.currentQuestionIdx {
		return false
	}
	if snapshot {
		m.snapshotCurrentQuestion()
	}
	m.currentQuestionIdx = newIdx
	if newIdx == maxIdx {
		// Recap slot — no textarea needed, no ticks, no typing.
		m.selectedOption = 0
		m.selectedMulti = nil
		m.questionScrollOffset = 0
		m.questionPromptExpanded = false
		m.questionPromptScroll = 0
		m.typingCustom = false
		m.typingNotes = false
		m.input.Reset()
		m.syncInputHeight()
		m.syncInputMode()
		return true
	}
	m.restoreQuestion(newIdx)
	m.questionPromptExpanded = false
	m.questionPromptScroll = 0
	if !m.questionStates[newIdx].askedEmitted {
		m.emitQuestionAsked(m.pendingQuestions[newIdx])
		m.questionStates[newIdx].askedEmitted = true
	}
	m.syncInputHeight()
	m.syncInputMode()
	return true
}

// submitAllAnswers dispatches the accumulated answers to the agent via the
// control_response protocol (or the legacy user-message path if no request ID
// was attached), clears AskUserQuestion state, and resumes the turn. Called
// from Enter on the "Review & Submit" recap slot.
func (m *AttachModel) submitAllAnswers() tea.Cmd {
	requestID := m.pendingAskRequestID
	questionsRaw := m.pendingAskQuestionsRaw
	answers := m.collectedAnswers
	annotations := buildAskUserAnnotations(m.collectedNotes)

	// Legacy tool_use path (no control_request ID) needs a text message for
	// SendUserMessage. Join answers in question order for a readable echo.
	var fallback string
	if requestID == "" {
		parts := make([]string, 0, len(m.pendingQuestions))
		for _, q := range m.pendingQuestions {
			if a, ok := answers[q.Question]; ok {
				parts = append(parts, a)
			}
		}
		fallback = strings.Join(parts, " / ")
	}

	// Echo each final answer into the chat log as "[you] <answer>" in
	// question order. Only done for the control_response path — the legacy
	// SendUserMessage fallback appends its own message via the session.
	if requestID != "" {
		for _, q := range m.pendingQuestions {
			a, ok := answers[q.Question]
			if !ok || a == "" {
				continue
			}
			m.sess.MessageLog().Append(llm.SDKMessage{
				Type:            "user",
				LocallyAppended: true,
				User: &llm.UserMessage{
					Message: llm.ConversationMsg{
						Role:    "user",
						Content: []llm.ContentBlock{{Type: "text", Text: a}},
					},
				},
			})
		}
	}

	m.pendingQuestions = nil
	m.questionStates = nil
	m.currentQuestionIdx = 0
	m.selectedOption = 0
	m.selectedMulti = nil
	m.questionScrollOffset = 0
	m.typingCustom = false
	m.typingNotes = false
	m.awaitingInput = false
	m.pendingAskRequestID = ""
	m.pendingAskQuestionsRaw = nil
	m.collectedAnswers = nil
	m.collectedNotes = nil
	// Dispatching resumes the turn — mark it active so the spinner animates
	// until the agent emits the next Result.
	m.turnActive = true
	m.lastActivityAt = time.Now()
	if m.thinkingLine == "" {
		m.thinkingLine = thinkingLineText
	}

	// Clear session state synchronously so re-attaching before the async
	// write completes does not re-show the question via LastControlRequest.
	if requestID != "" {
		m.sess.ClearPendingQuestion(requestID)
	}

	// If another AskUserQuestion arrived while this one was being answered,
	// promote it now so the user sees it immediately. Each queued bundle
	// keeps its own requestID so the SDK gets a control_response for every
	// in-flight tool use — preventing the silent-cancellation behaviour
	// that surfaces as "AskUserQuestion errored" when the LLM issued
	// parallel AUQ calls.
	if !m.promoteNextPendingAsk() {
		m.restorePendingPermission(m.sess)
	}

	featureID := m.sess.FeatureID()
	m.syncInputMode()
	m.updateViewport()
	return func() tea.Msg {
		if requestID != "" {
			_ = m.sess.RespondToAskUser(requestID, questionsRaw, answers, annotations)
		} else if fallback != "" {
			_ = m.sess.SendUserMessage(fallback)
		}
		return HelpResolvedMsg{FeatureID: featureID, RequestID: requestID}
	}
}

// submitAnswer commits the answer for the focused question and advances to
// the next slot (next question or recap). The agent is only notified from the
// recap slot via submitAllAnswers — pressing Enter on the last real question
// lands the user on the recap first.
func (m *AttachModel) submitAnswer(answer string) tea.Cmd {
	m.commitCurrentAnswer(answer)
	// Skip the pre-nav snapshot: commit already wrote the authoritative UI
	// state for the question, and the live scalars (textarea, typingCustom)
	// were reset in preparation for the next slot.
	m.advanceQuestionOpts(+1, false)
	m.updateViewport()
	return nil
}

// Update handles messages for the attach view.
func (m AttachModel) Update(msg tea.Msg) (AttachModel, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case permissionAnswerFailedMsg:
		m.pendingPermRequestID = msg.requestID
		m.pendingPermToolName = msg.toolName
		m.pendingPermToolInput = append(json.RawMessage(nil), msg.toolInput...)
		m.permMenuPattern = msg.pattern
		m.permMenuChoice = msg.choice
		m.showPermMenu = true
		if msg.err != nil {
			m.interruptToast = "Permission answer failed: " + firstLine(msg.err.Error())
			m.interruptToastAt = time.Now()
		}
		m.updateViewport()
		return m, nil
	case tea.KeyPressMsg:
		// Multi-repo tab navigation (tab/shift+tab)
		if len(m.repoTabs) > 1 {
			switch {
			case key.Matches(msg, key.NewBinding(key.WithKeys("shift+tab"))):
				nextIdx := m.findNextActiveTab(-1)
				if nextIdx >= 0 {
					return m.switchToTab(nextIdx)
				}
				return m, nil
			case key.Matches(msg, key.NewBinding(key.WithKeys("tab"))):
				nextIdx := m.findNextActiveTab(1)
				if nextIdx >= 0 {
					return m.switchToTab(nextIdx)
				}
				return m, nil
			}
		}

		// In read-only mode, only allow detach and filter toggle.
		if m.readOnly {
			switch {
			case key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+]", "esc"))):
				m.detached = true
				return m, nil
			case key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+f"))):
				m.filter = m.filter.next()
				m.updateViewport()
				return m, nil
			}
			// Forward scroll events to viewport (page up/down, arrow keys).
			// Wheel + ↑/↓ are coalesced to avoid kinetic-burst queue buildup.
			return m, m.forwardToViewport(msg)
		}

		if m.done {
			switch {
			case key.Matches(msg, keys.Detach):
				m.detached = true
				return m, nil
			case key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+f"))):
				m.filter = m.filter.next()
				m.updateViewport()
				return m, nil
			}
			return m, m.forwardToViewport(msg)
		}

		// Handle plan review menu navigation
		if m.showPlanReviewMenu {
			switch msg.String() {
			case "up", "k":
				if m.planReviewChoice > 0 {
					m.planReviewChoice--
				}
				return m, nil
			case "down", "j":
				if m.planReviewChoice < 1 {
					m.planReviewChoice++
				}
				return m, nil
			case "enter":
				m.detached = true
				decision := "iterate"
				if m.planReviewChoice == 1 {
					decision = "proceed"
				}
				featureID := m.planReviewFeatureID
				return m, func() tea.Msg {
					return PlanReviewDecisionMsg{FeatureID: featureID, Decision: decision}
				}
			case "esc":
				m.showPlanReviewMenu = false
				return m, nil
			}
			return m, nil
		}

		// Handle Ctrl+D for plan review mode
		if m.planReviewMode && key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+d"))) {
			m.showPlanReviewMenu = true
			m.planReviewChoice = 0
			return m, nil
		}

		// Handle rewind review menu navigation
		if m.showRewindReviewMenu {
			switch msg.String() {
			case "enter":
				m.detached = true
				featureID := m.rewindReviewFeatureID
				phase := m.rewindReviewPhase
				return m, func() tea.Msg {
					return RewindReviewDecisionMsg{FeatureID: featureID, Phase: phase, Decision: "proceed"}
				}
			case "esc":
				m.showRewindReviewMenu = false
				return m, nil
			}
			return m, nil
		}

		// Handle Ctrl+D for rewind review mode
		if m.rewindReviewMode && key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+d"))) {
			m.showRewindReviewMenu = true
			return m, nil
		}

		// Handle Ctrl+D for tweak session: explicit finish (bypasses Esc prompt)
		if m.isTweakSession && key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+d"))) {
			m.tweakFinishing = true
			m.detached = true
			return m, nil
		}

		// Handle permission menu navigation
		if m.showPermMenu {
			if key.Matches(msg, keys.Detach) {
				m.detached = true
				return m, nil
			}
			switch msg.String() {
			case "up", "k":
				if m.permMenuChoice > 0 {
					m.permMenuChoice--
				}
				return m, nil
			case "down", "j":
				if m.permMenuChoice < 2 {
					m.permMenuChoice++
				}
				return m, nil
			case "y", "Y":
				m.permMenuChoice = 0
				return m, m.executePermChoice()
			case "r", "R":
				m.permMenuChoice = 1
				return m, m.executePermChoice()
			case "n", "N":
				m.permMenuChoice = 2
				return m, m.executePermChoice()
			case "enter":
				return m, m.executePermChoice()
			}
			return m, nil
		}

		// Handle "Review & Submit" recap slot navigation. Lives on top of the
		// question slot handler since onRecapSlot() also returns true from
		// hasActiveQuestion().
		if m.onRecapSlot() && !m.typingCustom && !m.typingNotes {
			switch msg.String() {
			case "left", "h":
				m.advanceQuestion(-1)
				m.updateViewport()
				return m, nil
			case "enter":
				return m, m.submitAllAnswers()
			case "ctrl+]", "esc":
				m.detached = true
				return m, nil
			case "pgup", "pgdown":
				return m, m.forwardToViewport(msg)
			}
			return m, nil
		}

		// Handle multi-choice question navigation
		if m.hasActiveQuestion() && !m.typingCustom && !m.typingNotes {
			q := m.pendingQuestions[m.currentQuestionIdx]
			if questionUsesDirectFreeform(q) {
				m.typingCustom = true
				m.syncInputMode()
				return m, nil
			}
			if m.questionPromptExpanded {
				contentW := m.questionContentWidth()
				lineCount := len(questionPromptVisualLines(q.Question, contentW))
				switch msg.String() {
				case "?", "shift+/":
					m.questionPromptExpanded = false
					m.questionPromptScroll = 0
					return m, nil
				case "up", "k":
					if m.questionPromptScroll > 0 {
						m.questionPromptScroll--
					}
					return m, nil
				case "down", "j":
					if m.questionPromptScroll < lineCount-1 {
						m.questionPromptScroll++
					}
					return m, nil
				case "pgup":
					m.questionPromptScroll = max(m.questionPromptScroll-m.expandedQuestionPromptLineBudget(), 0)
					return m, nil
				case "pgdown":
					m.questionPromptScroll = min(m.questionPromptScroll+m.expandedQuestionPromptLineBudget(), max(lineCount-1, 0))
					return m, nil
				case "ctrl+]", "esc":
					m.detached = true
					return m, nil
				}
				return m, nil
			}
			numOptions := len(q.Options) // extra slot for "Type something"
			switch msg.String() {
			case "?", "shift+/":
				m.questionPromptExpanded = true
				m.questionPromptScroll = 0
				return m, nil
			case "up", "k":
				if m.selectedOption > 0 {
					m.selectedOption--
					m.updateQuestionScrollOffset()
				}
				return m, nil
			case "down", "j":
				if m.selectedOption < numOptions {
					m.selectedOption++
					m.updateQuestionScrollOffset()
				}
				return m, nil
			case "left", "h":
				// Back-nav to prior question (if any). No-op at idx 0.
				if m.currentQuestionIdx > 0 {
					m.advanceQuestion(-1)
					m.updateViewport()
				}
				return m, nil
			case "right", "l":
				// Forward-nav to the next slot. Only allowed for questions
				// that already have a committed answer — keeps right-arrow
				// non-destructive; Enter is the path for committing.
				if _, answered := m.collectedAnswers[q.Question]; answered {
					m.advanceQuestion(+1)
					m.updateViewport()
				}
				return m, nil
			case " ", "space":
				if q.MultiSelect && m.selectedOption < numOptions {
					if m.selectedMulti == nil {
						m.selectedMulti = make(map[int]bool)
					}
					if m.selectedMulti[m.selectedOption] {
						delete(m.selectedMulti, m.selectedOption)
					} else {
						m.selectedMulti[m.selectedOption] = true
					}
				}
				return m, nil
			case "enter":
				if m.selectedOption < numOptions {
					if q.MultiSelect {
						// Collect ticked labels in option order. If nothing is
						// ticked, Enter implicitly submits just the focused row.
						var labels []string
						for i := range q.Options {
							if m.selectedMulti[i] {
								labels = append(labels, q.Options[i].Label)
							}
						}
						if len(labels) == 0 {
							labels = []string{q.Options[m.selectedOption].Label}
						}
						return m, m.submitAnswer(strings.Join(labels, ", "))
					}
					// Single-select: submit the focused option.
					answer := q.Options[m.selectedOption].Label
					return m, m.submitAnswer(answer)
				}
				// Selected "Type something" — switch to freeform input. If the
				// user had previously answered via freeform, rehydrate the
				// textarea with their prior text so they can edit in place.
				m.selectedMulti = nil
				m.typingCustom = true
				if m.currentQuestionIdx < len(m.questionStates) {
					if prior := m.questionStates[m.currentQuestionIdx].customText; prior != "" {
						m.input.SetValue(prior)
					}
				}
				m.syncInputMode()
				m.syncInputHeight()
				return m, nil
			case "n", "N":
				// Open the notes editor for the current question. Only valid
				// when a real option is focused — not on the "Type something"
				// row, since notes attach to the selected answer.
				if m.selectedOption < numOptions {
					m.typingNotes = true
					m.input.Reset()
					if existing := m.collectedNotes[q.Question]; existing != "" {
						m.input.SetValue(existing)
					}
					m.syncInputMode()
					m.syncInputHeight()
				}
				return m, nil
			case "ctrl+]", "esc":
				m.detached = true
				return m, nil
			case "pgup", "pgdown":
				// Let page keys scroll the chat viewport so the user can
				// read back the context that prompted the question.
				return m, m.forwardToViewport(msg)
			}
			// Swallow all other keys during multi-choice
			return m, nil
		}

		// Handle notes editor mode (user pressed `n` to attach notes to the
		// current question). Enter saves, Esc cancels; notes do not submit the
		// answer — the user returns to option selection and picks normally.
		if m.hasActiveQuestion() && m.typingNotes {
			switch {
			case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
				m.typingNotes = false
				m.input.Reset()
				m.syncInputMode()
				m.syncInputHeight()
				return m, nil
			case key.Matches(msg, keys.Detach):
				m.detached = true
				return m, nil
			case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
				questionText := m.pendingQuestions[m.currentQuestionIdx].Question
				text := strings.TrimSpace(m.input.Value())
				if text == "" {
					delete(m.collectedNotes, questionText)
				} else {
					m.collectedNotes[questionText] = text
				}
				m.typingNotes = false
				m.input.Reset()
				m.syncInputMode()
				m.syncInputHeight()
				return m, nil
			case key.Matches(msg, shiftEnterKey):
				m.preExpandInput()
				var cmd tea.Cmd
				m.input, cmd = m.input.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				m.syncInputHeight()
				return m, cmd
			default:
				// Forward to textarea
				var cmd tea.Cmd
				m.input, cmd = m.input.Update(msg)
				m.syncInputHeight()
				return m, cmd
			}
		}

		// Handle freeform typing mode (user chose "Type something")
		if m.hasActiveQuestion() && m.typingCustom {
			switch {
			case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
				if questionUsesDirectFreeform(m.pendingQuestions[m.currentQuestionIdx]) {
					m.input.Reset()
					m.syncInputHeight()
					return m, nil
				}
				// Go back to option selection
				m.typingCustom = false
				m.syncInputMode()
				return m, nil
			case key.Matches(msg, keys.Detach):
				m.detached = true
				return m, nil
			case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
				text := strings.TrimSpace(m.input.Value())
				if text != "" {
					m.input.Reset()
					m.syncInputHeight()
					return m, m.submitAnswer(text)
				}
				return m, nil
			case key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+s"))):
				text := strings.TrimSpace(m.input.Value())
				if text != "" {
					m.input.Reset()
					m.syncInputHeight()
					return m, m.submitAnswer(text)
				}
				return m, nil
			case key.Matches(msg, shiftEnterKey):
				m.preExpandInput()
				var cmd tea.Cmd
				m.input, cmd = m.input.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				m.syncInputHeight()
				return m, cmd
			default:
				// Forward to textarea
				var cmd tea.Cmd
				m.input, cmd = m.input.Update(msg)
				m.syncInputHeight()
				return m, cmd
			}
		}

		// Handle tweak finish prompt navigation
		if m.showFinishPrompt {
			switch msg.String() {
			case "f", "enter":
				m.tweakFinishing = true
				m.detached = true
				m.showFinishPrompt = false
				return m, nil
			case "s":
				m.showFinishPrompt = false
				return m, m.interruptAgentCmd()
			case "d":
				m.detached = true
				m.showFinishPrompt = false
				return m, nil
			case "esc":
				m.showFinishPrompt = false
				return m, nil
			default:
				m.showFinishPrompt = false
				return m, nil
			}
		}

		// Autocomplete navigation — intercepts specific keys when dropdown is active.
		// Non-navigation keys are NOT intercepted; they fall through to the existing
		// cascade below so that detach, filter toggle, send, paste, and all other
		// handlers continue to work while the dropdown is visible.
		if m.autocomplete.active {
			switch msg.String() {
			case "up":
				m.autocomplete = m.autocomplete.MoveUp()
				return m, nil
			case "down":
				m.autocomplete = m.autocomplete.MoveDown()
				return m, nil
			case "enter", "tab":
				if item := m.autocomplete.Selected(); item != nil {
					m = m.applyAutocompleteSelection(*item)
				}
				m.autocomplete = m.autocomplete.Dismiss()
				return m, nil
			case "esc":
				m.autocomplete = m.autocomplete.Dismiss()
				return m, nil
			}
		}

		switch {
		case key.Matches(msg, keys.Detach):
			// In tweak sessions, Esc shows the finish prompt; Ctrl+] detaches directly
			if m.isTweakSession && msg.String() == "esc" {
				m.showFinishPrompt = true
				return m, nil
			}
			m.detached = true
			return m, nil
		case key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+f"))):
			m.filter = m.filter.next()
			m.updateViewport()
			return m, nil
		case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
			return m, m.sendChatInput()
		case key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+s"))):
			return m, m.sendChatInput()
		case key.Matches(msg, shiftEnterKey):
			// Pre-expand before inserting newline so prior lines stay visible.
			m.preExpandInput()
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			m.syncInputHeight()
			return m, cmd
		case key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+v"))):
			if m.canPasteImages {
				return m, m.tryPasteImageCmd()
			}
		}

		// Forward to text input
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.syncInputHeight()
		var recheckCmd tea.Cmd
		m, recheckCmd = m.recheckAutocompleteTrigger()
		cmds = append(cmds, cmd)
		if recheckCmd != nil {
			cmds = append(cmds, recheckCmd)
		}

	case ImagePastedMsg:
		// Track pasted image path; insert a human-readable placeholder into
		// the textarea. The real path is restored at send time via
		// expandMediaPlaceholders so the agent receives a usable file path.
		m.pastedImages = append(m.pastedImages, msg.Path)
		m.input.InsertString(fmt.Sprintf("[Image #%d]", len(m.pastedImages)))
		m.syncInputHeight()
		return m, nil

	case ImagePasteFailedMsg:
		// No image on clipboard — try text fallback
		return m, m.textPasteFallbackCmd()

	case TextPastedMsg:
		m.input.InsertString(msg.Text)
		m.syncInputHeight()
		return m, nil

	case FilesPastedMsg:
		// Track pasted files; insert human-readable placeholders into the
		// textarea. Real paths are restored at send time via
		// expandMediaPlaceholders so the agent receives usable file paths.
		m.pastedFiles = append(m.pastedFiles, msg.Paths...)
		m.pastedFileNames = append(m.pastedFileNames, msg.Names...)
		for i := range msg.Paths {
			m.input.InsertString(fmt.Sprintf("[%s] ", msg.Names[i]))
		}
		m.syncInputHeight()
		return m, nil

	case tea.PasteMsg:
		// Forward paste events to textarea so Cmd+V text paste works
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.syncInputHeight()
		cmds = append(cmds, cmd)

	case attachMsgsMsg:
		// Discard stale messages from a previous tab's session
		if len(m.repoTabs) > 0 && msg.generation != m.tabGeneration {
			return m, nil
		}
		if len(msg.messages) > 0 {
			m.lastActivityAt = time.Now()
		}
		for _, sdkMsg := range msg.messages {
			if sdkMsg.ControlRequest != nil {
				// A control request — including permission prompts and
				// AskUserQuestion — is evidence the turn is still in progress.
				m.turnActive = true
				// Skip control_requests that have already been resolved
				// upstream — e.g., the session's forwarder synthesised a
				// deny on timeout while no TUI was attached. The message
				// is still in attachCh because we drain it lazily, but
				// re-prompting would cause a double-response: the SDK has
				// already moved past this requestID and a second answer
				// would either be ignored or mismatch its tool_use_id.
				if !controlRequestStillPending(m.sess, sdkMsg.ControlRequest.RequestID) {
					continue
				}
				if sdkMsg.ControlRequest.Request.ToolName == toolNameAskUserQuestion {
					// Route AskUserQuestion control_requests to multi-choice UI
					if questions := m.parseAskUserQuestionsForDisplay(sdkMsg.ControlRequest.Request.Input); len(questions) > 0 {
						m.activateAskUserQuestions(questions, sdkMsg.ControlRequest.RequestID, sdkMsg.ControlRequest.Request.Input)
					}
				} else {
					// Generic tool permission prompt (Bash, etc.)
					m.activatePermissionRequest(sdkMsg.ControlRequest)
				}
			}
			// Track tool use / thinking for spinner display.
			// Unlike the chat model, text does NOT clear the spinner here.
			// The CLI splits content blocks into separate messages, so text
			// arrives before tool_use — clearing on text would blank the
			// spinner between every text+tool pair. Result clears it instead.
			if sdkMsg.Assistant != nil {
				m.markAgentProgress()
				for _, block := range sdkMsg.Assistant.Message.Content {
					if block.IsThinking() {
						m.thinkingLine = thinkingLineText
					}
					if block.IsToolUse() {
						m.thinkingLine = fmt.Sprintf("Using %s...", block.Name)
					}
				}
			}
			// Stream delta events drive the spinner during streaming.
			if sdkMsg.StreamDeltaType != "" {
				m.markAgentProgress()
				if sdkMsg.StreamDeltaType == "thinking" {
					m.thinkingLine = thinkingLineText
				}
			}
			if sdkMsg.ToolProgress != nil {
				m.markAgentProgress()
				m.thinkingLine = fmt.Sprintf("Using %s...", sdkMsg.ToolProgress.ToolName)
			}
			// Subagent (Task tool) lifecycle messages arrive while the main
			// agent is blocked — treat them as definitive progress.
			if sdkMsg.TaskStarted != nil || sdkMsg.TaskProgress != nil || sdkMsg.TaskNotification != nil {
				m.markAgentProgress()
			}
			if sdkMsg.Result != nil {
				m.thinkingLine = ""
				m.turnActive = false
				m.queuedMessages = nil // Turn complete — queued messages have been processed
			}
			if sdkMsg.Result != nil && sdkMsg.Result.IsSuccess() {
				m.awaitingInput = true
			}
		}
		m.updateViewport()
		cmds = append(cmds, pollAttachChCmd(m.sess, m.tabGeneration))

	case skillsLoadedMsg:
		// Discard stale results (e.g., tab switched before discovery completed).
		if msg.workDir != m.skillsWorkDir {
			return m, nil
		}
		m.skillItems = msg.items
		m.skillItemsLoaded = true
		m.skillsLoading = false
		if m.autocomplete.active && m.autocomplete.mode == AutocompleteSkill {
			m.autocomplete = m.autocomplete.Activate(
				AutocompleteSkill, m.autocomplete.triggerOffset,
				m.autocomplete.query, m.skillItems,
			)
		}
		return m, nil

	case fileIndexReadyMsg:
		// Discard stale results (e.g., tab switched before Build() completed).
		if msg.workDir != m.fileIndexWorkDir {
			return m, nil
		}
		m.fileIndex = msg.index
		m.fileIndexLoading = false

		// If autocomplete is active in file mode, refresh with search results.
		if m.autocomplete.Active() && m.autocomplete.Mode() == AutocompleteFile {
			results := m.fileIndex.Search(m.autocomplete.Query(), autocompleteMaxVisible)
			items := make([]AutocompleteItem, len(results))
			for i, path := range results {
				items[i] = AutocompleteItem{
					Name:   path,
					Source: "file",
				}
			}
			m.autocomplete = m.autocomplete.Activate(
				AutocompleteFile, m.autocomplete.TriggerOffset(),
				m.autocomplete.Query(), items,
			)
			m.autocomplete = m.autocomplete.SetLoading(false)
		}
		return m, nil

	case attachDoneMsg:
		// Discard stale done signals
		if len(m.repoTabs) > 0 && msg.generation != m.tabGeneration {
			return m, nil
		}
		m.done = true
		m.thinkingLine = ""
		m.turnActive = false
		m.updateViewport()
		return m, nil

	case agentInterruptedMsg:
		if msg.err != nil {
			m.interruptToast = "✗ Interrupt failed: " + msg.err.Error()
		} else {
			m.interruptToast = "✓ Agent interrupted: what should I do instead?"
		}
		m.interruptToastAt = time.Now()
		return m, tea.Tick(interruptToastDuration, func(time.Time) tea.Msg {
			return agentToastClearMsg{}
		})

	case agentToastClearMsg:
		if !m.interruptToastAt.IsZero() && time.Since(m.interruptToastAt) >= interruptToastDuration {
			m.interruptToast = ""
			m.interruptToastAt = time.Time{}
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		vpW, vpH, inputW := attachLayout(m.width, m.height, m.readOnly, len(m.repoTabs) > 0)
		m.viewport.SetWidth(vpW)
		m.viewport.SetHeight(vpH)
		m.input.SetWidth(inputW)
		m.syncInputHeight()
		m.updateViewport()
	}

	cmds = append(cmds, m.forwardToViewport(msg))

	return m, tea.Batch(cmds...)
}

// View renders the attach view.
func (m AttachModel) View() string {
	if m.detached {
		return ""
	}

	w := m.width
	if w < 40 {
		w = 80
	}

	header := m.renderAttachHeader(w)

	var tabBar string
	tabBarH := 0
	if len(m.repoTabs) > 0 {
		tabBar = m.renderTabBar(w)
		tabBarH = 1
	}

	panelW := max(w-2, 22) // must match attachLayout's panelW so content width is consistent

	// Compute chat panel height (dynamic for questions).
	// In read-only mode, no chat panel is rendered.
	chatH := 0
	if !m.readOnly {
		chatH = m.chatPanelHeight()
	}

	// Use the actual chatH so the message panel shrinks as the chat input
	// grows, keeping the total view within the terminal height.
	const headerH = 3
	const footerH = 1
	const boxBorders = 2 // each panel's Height() includes +2 for border
	chatBoxH := 0
	if !m.readOnly {
		chatBoxH = chatH + boxBorders + 1 // chat box + "\n" before it
	}
	msgPanelH := max(m.height-headerH-tabBarH-chatBoxH-footerH-boxBorders-1, 6) // -1 for "\n" before footer
	vpH := max(msgPanelH-2, 4)
	// Preserve bottom-anchoring across height changes. When a question opens
	// (chat panel grows → viewport shrinks), a viewport that was at bottom is
	// no longer at bottom with the new smaller height, cutting off the last
	// few lines. Re-anchor before rendering.
	wasAtBottom := m.viewport.AtBottom() || m.viewport.TotalLineCount() <= m.viewport.Height()
	m.viewport.SetHeight(vpH)
	if wasAtBottom {
		m.viewport.GotoBottom()
	}

	msgContent := m.viewport.View()
	if spinnerLine := m.renderSpinnerLine(); spinnerLine != "" {
		msgContent += "\n" + spinnerLine
	}

	msgBox := panelStyle(true).
		Width(panelW).
		Height(msgPanelH + 2).
		Render(msgContent)

	statusText := "reviewing"
	if m.readOnly {
		if m.done {
			statusText = "completed"
		}
	} else {
		statusText = "running"
		if m.awaitingInput {
			statusText = "waiting for your response"
		}
		if m.done {
			statusText = "completed"
		}
	}
	titleName := m.featureName
	if titleName == "" {
		titleName = m.sess.ID()
	}
	panelTitle := fmt.Sprintf("Watch · %s · %s (%s)", titleName, m.sess.Phase(), statusText)
	msgBox = renderBorderTitle(msgBox, panelTitle, lipgloss.NewStyle().Foreground(colorBrand))

	var result strings.Builder
	result.WriteString(header)
	if tabBar != "" {
		result.WriteString(tabBar)
	}
	result.WriteString(msgBox)

	// --- Chat panel (interactive sessions only) ---
	if !m.readOnly {
		var chatContent string
		var chatTitle string
		var chatTitleStyle lipgloss.Style
		if m.showFinishPrompt {
			chatContent = m.renderFinishPromptInline()
			chatTitle = "Tweak Session"
			chatTitleStyle = lipgloss.NewStyle().Foreground(colorBrand)
		} else if m.showPermMenu {
			chatContent = m.renderPermMenu()
			chatTitle = "Permission"
			chatTitleStyle = lipgloss.NewStyle().Foreground(colorWarning)
		} else if m.hasActiveQuestion() {
			chatContent = m.renderQuestion()
			chatTitle = "Chat"
			chatTitleStyle = lipgloss.NewStyle().Foreground(colorBrand)
		} else if !m.done {
			chatContent = m.renderQueueIndicator() + m.input.View()
			chatTitle = "Chat"
			chatTitleStyle = lipgloss.NewStyle().Foreground(colorBrand)
		} else {
			chatTitle = "Chat"
			chatTitleStyle = lipgloss.NewStyle().Foreground(colorBrand)
		}
		borderColor := colorSurface
		if m.showPermMenu {
			borderColor = colorWarning
		}
		chatBox := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(borderColor).
			Padding(0, 1).
			Width(panelW).
			Height(chatH + 2).
			Render(chatContent)
		chatBox = renderBorderTitle(chatBox, chatTitle, chatTitleStyle)
		result.WriteString("\n")
		result.WriteString(chatBox)
	}

	// --- Plan review menu overlay ---
	if m.showPlanReviewMenu {
		result.WriteString("\n")
		result.WriteString(m.renderPlanReviewMenu(panelW))
	}

	// --- Rewind review menu overlay ---
	if m.showRewindReviewMenu {
		result.WriteString("\n")
		result.WriteString(m.renderRewindReviewMenu(panelW))
	}

	// --- Footer ---
	var tabHint string
	if len(m.repoTabs) > 1 {
		tabHint = " [Tab/S-Tab] Switch repo  "
	}
	var hints string
	stopWatchingHint := "[" + DetachKeyHint() + "] Stop watching"
	if m.done && !m.readOnly {
		hints = tabHint + " Session ended — " + stopWatchingHint + " to return   [Ctrl+f] Filter: " + m.filter.String()
	} else if m.readOnly {
		hints = tabHint + " " + stopWatchingHint + "   [Ctrl+f] Filter: " + m.filter.String()
	} else if m.planReviewMode && m.showPlanReviewMenu {
		hints = tabHint + " [↑↓] Navigate   [Enter] Select   [Esc] Cancel"
	} else if m.planReviewMode {
		hints = tabHint + " " + stopWatchingHint + "   [Ctrl+D] Done reviewing   [Ctrl+S] Send   [Ctrl+f] Filter: " + m.filter.String()
	} else if m.rewindReviewMode && m.showRewindReviewMenu {
		hints = tabHint + " [Enter] Proceed   [Esc] Cancel"
	} else if m.rewindReviewMode {
		hints = tabHint + " " + stopWatchingHint + "   [Ctrl+D] Done reviewing   [Ctrl+S] Send   [Ctrl+f] Filter: " + m.filter.String()
	} else if m.isTweakSession && m.showFinishPrompt {
		hints = tabHint + " [f/Enter] Finish   [s] Stop   [d] Stop watching   [Esc] Cancel"
	} else if m.isTweakSession {
		hints = tabHint + " [Esc] Finish/Stop/Stop watching   [Ctrl+D] Finish   [Enter] Send   [Shift+Enter] Newline   [Ctrl+f] Filter: " + m.filter.String()
	} else {
		hints = tabHint + " " + stopWatchingHint + "   [Enter] Send   [Shift+Enter] Newline   [Ctrl+f] Filter: " + m.filter.String()
	}
	// While the interrupt toast is active, it replaces the key-hints in the
	// footer so the confirmation is visible without changing layout height
	// (attachLayout reserves exactly one footer line).
	var leftPart string
	if m.interruptToast != "" {
		toastStyle := lipgloss.NewStyle().Foreground(colorSuccess).Bold(true)
		if strings.HasPrefix(m.interruptToast, "✗") {
			toastStyle = lipgloss.NewStyle().Foreground(colorError).Bold(true)
		}
		leftPart = toastStyle.Render(m.interruptToast)
	} else {
		leftPart = KeyHelpStyle.Render(hints)
	}

	var rightPart string
	if m.logPath != "" {
		rightPart = MutedStyle.Render(m.logPath)
	}

	gap := max(w-lipgloss.Width(leftPart)-lipgloss.Width(rightPart)-1, 2)
	footer := leftPart + strings.Repeat(" ", gap) + rightPart

	result.WriteString("\n")
	result.WriteString(footer)

	rendered := result.String()

	// Overlay autocomplete dropdown when active (including empty-state "No results").
	if m.autocomplete.active && !m.readOnly {
		dropdownStr := m.autocomplete.View(panelW - 4)
		dropdownH := lipgloss.Height(dropdownStr)

		// Position dropdown just above the chat box.
		dropdownY := max(headerH+tabBarH+msgPanelH+boxBorders-dropdownH, headerH+tabBarH)
		dropdownX := 2

		bg := lipgloss.NewLayer(rendered)
		fg := lipgloss.NewLayer(dropdownStr).X(dropdownX).Y(dropdownY).Z(1)
		comp := lipgloss.NewCompositor(bg, fg)
		return comp.Render()
	}

	return rendered
}

// recapRowLineCount returns the number of wrapped terminal lines one recap
// row ("N. question → answer") occupies at the given content width.
func recapRowLineCount(q askUserQuestion, answers map[string]string, notes map[string]string, width int) int {
	answer := answers[q.Question]
	if answer == "" {
		answer = "(no answer)"
	}
	row := recapRowText(q, answer, notes[q.Question])
	return wrappedLineCount(row, width)
}

// recapRowText formats a single recap row. Leading index prefix is added by
// the caller since it depends on the 1-based position in the batch.
func recapRowText(q askUserQuestion, answer, note string) string {
	answerPart := answer
	if answerPart == "" {
		answerPart = "(no answer)"
	}
	if note != "" {
		answerPart += "  (notes)"
	}
	return fmt.Sprintf("%s → %s", q.Question, answerPart)
}

// renderRecap renders the "Review & Submit" summary shown after the last
// question in an AskUserQuestion batch.
func (m AttachModel) renderRecap() string {
	titleStyle := lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	hintStyle := MutedStyle
	arrowStyle := MutedStyle
	numStyle := MutedStyle

	var b strings.Builder
	b.WriteString(titleStyle.Render("Review & Submit"))
	b.WriteByte('\n')
	b.WriteByte('\n')

	for i, q := range m.pendingQuestions {
		answer := ""
		if m.collectedAnswers != nil {
			answer = m.collectedAnswers[q.Question]
		}
		if answer == "" {
			answer = "(no answer)"
		}
		note := ""
		if m.collectedNotes != nil {
			note = m.collectedNotes[q.Question]
		}
		suffix := ""
		if note != "" {
			suffix = "  (notes)"
		}
		b.WriteString(numStyle.Render(fmt.Sprintf(" %d. ", i+1)))
		b.WriteString(q.Question)
		b.WriteString(arrowStyle.Render(" → "))
		b.WriteString(answer)
		if suffix != "" {
			b.WriteString(hintStyle.Render(suffix))
		}
		b.WriteByte('\n')
	}

	b.WriteByte('\n')
	b.WriteString(hintStyle.Render("Enter to submit · ← to edit previous"))
	return b.String()
}

// renderQuestion renders the multi-choice question UI for the chat panel,
// styled after the Claude Code TUI selection dialog.
func (m AttachModel) renderQuestion() string {
	if m.onRecapSlot() {
		return m.renderRecap()
	}
	q := m.pendingQuestions[m.currentQuestionIdx]
	if m.questionPromptExpanded {
		return m.renderExpandedQuestion(q)
	}

	questionStyle := lipgloss.NewStyle().Bold(true)
	selectedLabel := lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	normalLabel := lipgloss.NewStyle()
	hintStyle := MutedStyle
	notesLabelStyle := lipgloss.NewStyle().Foreground(colorBrand)
	separatorStyle := lipgloss.NewStyle().Foreground(colorSurface)

	if questionUsesDirectFreeform(q) {
		var b strings.Builder
		contentW := m.questionContentWidth()
		b.WriteString(questionStyle.Render(questionPromptText(q.Question, contentW)))
		b.WriteString("\n\n")
		b.WriteString(m.input.View())
		b.WriteByte('\n')
		b.WriteByte('\n')
		hint := "Enter to send · Shift+Enter for newline · Esc to clear"
		if len(m.pendingQuestions) > 1 {
			hint += fmt.Sprintf(" · Question %d of %d", m.currentQuestionIdx+1, len(m.pendingQuestions))
		}
		b.WriteString(hintStyle.Render(hint))
		return b.String()
	}

	numOptions := len(q.Options)
	hasAnyPreview := false
	for _, o := range q.Options {
		if o.Preview != "" {
			hasAnyPreview = true
			break
		}
	}

	var top strings.Builder
	contentW := m.questionContentWidth()
	top.WriteString(questionStyle.Render(questionPromptText(q.Question, contentW)))
	top.WriteString("\n\n")

	start, end, needAbove, needBelow := m.questionVisibleWindow()
	top.WriteString(renderQuestionOptionsBlock(q, m.selectedOption, m.selectedMulti, start, end, needAbove, needBelow))

	top.WriteString(separatorStyle.Render("  ────────────────────────────"))
	top.WriteByte('\n')
	typeIdx := numOptions
	var priorPreview string
	if m.currentQuestionIdx < len(m.questionStates) {
		if prior := m.questionStates[m.currentQuestionIdx].customText; prior != "" {
			preview := strings.ReplaceAll(prior, "\n", " ")
			if len([]rune(preview)) > 40 {
				preview = string([]rune(preview)[:40]) + "…"
			}
			priorPreview = preview
		}
	}
	if m.typingCustom || m.selectedOption == typeIdx {
		top.WriteString(selectedLabel.Render(fmt.Sprintf("> %d. Type something.", typeIdx+1)))
	} else {
		top.WriteString(normalLabel.Render(fmt.Sprintf("  %d. Type something.", typeIdx+1)))
	}
	if priorPreview != "" {
		top.WriteString(MutedStyle.Render("  (" + priorPreview + ")"))
	}

	topBlock := top.String()
	if hasAnyPreview && m.selectedOption < numOptions {
		if preview := q.Options[m.selectedOption].Preview; preview != "" {
			previewW := 0
			for _, line := range strings.Split(preview, "\n") {
				if w := lipgloss.Width(line); w > previewW {
					previewW = w
				}
			}
			leftNaturalW := 0
			for _, line := range strings.Split(topBlock, "\n") {
				if w := lipgloss.Width(line); w > leftNaturalW {
					leftNaturalW = w
				}
			}
			const gap = 2
			if leftNaturalW+gap+previewW <= contentW {
				topBlock = lipgloss.JoinHorizontal(lipgloss.Top, topBlock, strings.Repeat(" ", gap), preview)
			}
		}
	}

	var bottom strings.Builder
	bottom.WriteByte('\n')
	switch {
	case m.typingNotes:
		bottom.WriteString(notesLabelStyle.Render("Notes:"))
		bottom.WriteByte('\n')
		bottom.WriteString(m.input.View())
		bottom.WriteByte('\n')
		bottom.WriteString(hintStyle.Render("Enter to save · Shift+Enter for newline · Esc to cancel"))
	case m.typingCustom:
		bottom.WriteString(m.input.View())
		bottom.WriteByte('\n')
		bottom.WriteString(hintStyle.Render("Enter to send · Shift+Enter for newline · Esc to go back"))
	default:
		notesText := m.collectedNotes[q.Question]
		bottom.WriteString(notesLabelStyle.Render("Notes: "))
		if notesText != "" {
			bottom.WriteString(notesText)
		} else {
			bottom.WriteString(hintStyle.Render("press n to add notes"))
		}
		bottom.WriteByte('\n')
		canBack := m.currentQuestionIdx > 0
		_, canForward := m.collectedAnswers[q.Question]
		bottom.WriteString(renderQuestionFooterHint(q, m.currentQuestionIdx, len(m.pendingQuestions), canBack, canForward, questionPromptIsTruncated(q.Question, contentW), "n for notes"))
	}

	return topBlock + "\n" + bottom.String()
}

func (m AttachModel) renderExpandedQuestion(q askUserQuestion) string {
	contentW := m.questionContentWidth()
	lines := questionPromptVisualLines(q.Question, contentW)
	if len(lines) == 0 {
		lines = []string{""}
	}

	start := m.questionPromptScroll
	if start < 0 {
		start = 0
	}
	if start >= len(lines) {
		start = len(lines) - 1
	}

	budget := m.expandedQuestionPromptLineBudget()
	needAbove := start > 0
	if needAbove {
		budget--
	}
	end := min(start+budget, len(lines))
	needBelow := end < len(lines)
	if needBelow && end > start+1 {
		end--
	}

	var b strings.Builder
	if needAbove {
		b.WriteString(MutedStyle.Render(fmt.Sprintf("↑ %d more above", start)))
		b.WriteByte('\n')
	}
	b.WriteString(lipgloss.NewStyle().Bold(true).Render(strings.Join(renderExpandedQuestionBody(q, contentW, start, end-start), "\n")))
	b.WriteByte('\n')
	if needBelow {
		b.WriteString(MutedStyle.Render(fmt.Sprintf("↓ %d more below", len(lines)-end)))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(MutedStyle.Render("↑/↓ to scroll · ? back to choices"))
	return b.String()
}

// renderPlanReviewMenu renders the Ctrl+D plan review decision overlay.
func (m AttachModel) renderPlanReviewMenu(panelW int) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorBrand)
	selectedStyle := lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	normalStyle := lipgloss.NewStyle()
	hintStyle := MutedStyle

	options := []string{"Iterate more (+3 rounds)", "Proceed with current plan"}

	var b strings.Builder
	b.WriteString(titleStyle.Render("Plan Review"))
	b.WriteString("\n\n")

	for i, opt := range options {
		cursor := "  "
		style := normalStyle
		if i == m.planReviewChoice {
			cursor = "> "
			style = selectedStyle
		}
		b.WriteString(style.Render(cursor + opt))
		b.WriteByte('\n')
	}

	b.WriteByte('\n')
	b.WriteString(hintStyle.Render("Enter to select · Esc to cancel"))

	menuBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorBrand).
		Padding(1, 2).
		Width(min(panelW-2, 42)).
		Render(b.String())

	return lipgloss.PlaceHorizontal(panelW, lipgloss.Center, menuBox)
}

// renderPermMenu renders the permission decision inline in the chat panel,
// styled like renderQuestion but with colorWarning accents.
func (m AttachModel) renderPermMenu() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorWarning)
	selectedStyle := lipgloss.NewStyle().Foreground(colorWarning).Bold(true)
	normalStyle := lipgloss.NewStyle()
	descStyle := MutedStyle
	hintStyle := MutedStyle
	separatorStyle := lipgloss.NewStyle().Foreground(colorSurface)

	contentW := m.questionContentWidth()
	detail := formatPermissionDetail(m.pendingPermToolName, m.pendingPermToolInput)
	options := []struct {
		shortcut string
		label    string
		desc     string
	}{
		{"y", "Allow", ""},
		{"r", "Allow & Remember", m.permMenuPattern},
		{"n", "Deny", ""},
	}

	var b strings.Builder
	b.WriteString(titleStyle.MaxWidth(contentW).Render(fmt.Sprintf("Allow %s?", m.pendingPermToolName)))
	b.WriteByte('\n')
	b.WriteString(descStyle.Width(contentW).Render(truncatePermDetail(detail, contentW)))
	b.WriteString("\n\n")

	for i, opt := range options {
		cursor := "  "
		labelStyle := normalStyle
		if i == m.permMenuChoice {
			cursor = "> "
			labelStyle = selectedStyle
		}
		b.WriteString(labelStyle.MaxWidth(contentW).Render(fmt.Sprintf("%s%d. [%s] %s", cursor, i+1, opt.shortcut, opt.label)))
		b.WriteByte('\n')
		if opt.desc != "" {
			b.WriteString(descStyle.Width(contentW).Render(fmt.Sprintf("     %s", opt.desc)))
			b.WriteByte('\n')
		}
	}

	sepW := min(contentW, 30)
	b.WriteString(separatorStyle.Render("  " + strings.Repeat("─", max(sepW-2, 4))))
	b.WriteByte('\n')
	b.WriteString(hintStyle.MaxWidth(contentW).Render("y/r/n to select · ↑/↓ navigate · Enter to confirm"))

	return b.String()
}

// renderRewindReviewMenu renders the Ctrl+D rewind review decision overlay.
func (m AttachModel) renderRewindReviewMenu(panelW int) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorBrand)
	selectedStyle := lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	hintStyle := MutedStyle

	var b strings.Builder
	b.WriteString(titleStyle.Render("Rewind Review"))
	b.WriteString("\n\n")
	b.WriteString(selectedStyle.Render("> Proceed with rewind"))
	b.WriteByte('\n')
	b.WriteByte('\n')
	b.WriteString(hintStyle.Render("Enter to proceed · Esc to cancel"))

	menuBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorBrand).
		Padding(1, 2).
		Width(min(panelW-2, 42)).
		Render(b.String())

	return lipgloss.PlaceHorizontal(panelW, lipgloss.Center, menuBox)
}

// renderFinishPromptInline renders the finish/detach prompt as plain text for the chat panel.
func (m AttachModel) renderFinishPromptInline() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorBrand)
	normalStyle := lipgloss.NewStyle()
	hintStyle := MutedStyle

	var b strings.Builder
	b.WriteString(titleStyle.Render("Finish, Stop, or Stop watching?"))
	b.WriteString("\n\n")
	b.WriteString(normalStyle.Render("  [f/Enter] Finish — commit changes and complete"))
	b.WriteByte('\n')
	b.WriteString(normalStyle.Render("  [s]       Stop   — interrupt the agent's current task"))
	b.WriteByte('\n')
	b.WriteString(normalStyle.Render("  [d]       Stop watching — leave session running"))
	b.WriteByte('\n')
	b.WriteByte('\n')
	b.WriteString(hintStyle.Render("Esc to cancel"))
	return b.String()
}

// renderAttachHeader renders the AGENTICO brand header for the attach view.
func (m AttachModel) renderAttachHeader(w int) string {
	artLines := []string{
		" \u2584\u2580\u2588 \u2588\u2580\u2580 \u2588\u2580\u2580 \u2588\u2584\u2591\u2588 \u2580\u2588\u2580 \u2588 \u2588\u2580\u2580 \u2588\u2580\u2588",
		" \u2588\u2580\u2588 \u2588\u2584\u2588 \u2588\u2588\u2584 \u2588\u2591\u2580\u2588 \u2591\u2588\u2591 \u2588 \u2588\u2584\u2584 \u2588\u2584\u2588",
	}

	brandStyle := lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(colorSurface)

	var header strings.Builder
	for _, line := range artLines {
		header.WriteString(brandStyle.Render(line))
		header.WriteByte('\n')
	}
	header.WriteString(dimStyle.Render(strings.Repeat("\u2500", w)))
	header.WriteByte('\n')

	return header.String()
}

// tabStatusIcon returns a styled icon for the attach tab bar driven by the
// per-tab presentationStatus token.
func tabStatusIcon(status presentationStatus) string {
	switch status {
	case statusReviewPassed, statusCodeReady:
		return SuccessStyle.Render("✓")
	case statusImplementing, statusReviewing, statusAwaitingFinalReview, statusFinalReviewing:
		return lipgloss.NewStyle().Foreground(colorInfo).Render("▸")
	case statusNeedUserInput:
		return WarningStyle.Render("⚠")
	case statusFailed:
		return ErrorStyle.Render("✗")
	case statusBlocked:
		return WarningStyle.Render("⊘")
	case statusSkipped:
		return MutedStyle.Render("—")
	default:
		return MutedStyle.Render("○")
	}
}

// renderTabBar renders the tab bar for multi-session attach mode. The bar is
// hidden when a feature has only one active session (preserves the classic
// single-session attach look).
func (m AttachModel) renderTabBar(w int) string {
	if len(m.repoTabs) <= 1 {
		return ""
	}

	activeStyle := lipgloss.NewStyle().Bold(true).Foreground(colorBrand)
	inactiveStyle := lipgloss.NewStyle()
	disabledStyle := MutedStyle
	separatorStyle := MutedStyle

	var b strings.Builder
	for i, tab := range m.repoTabs {
		var display string
		if tab.label != "" {
			display = tab.label
		} else {
			display = abbreviateRepoName(tab.repoName)
		}
		icon := tabStatusIcon(tab.status)

		label := fmt.Sprintf(" %s%s ", display, icon)

		if i == m.activeTabIdx {
			b.WriteString(activeStyle.Render(label))
		} else if tab.sess == nil {
			b.WriteString(disabledStyle.Render(label))
		} else {
			b.WriteString(inactiveStyle.Render(label))
		}

		if i < len(m.repoTabs)-1 {
			b.WriteString(separatorStyle.Render(" │ "))
		}
	}
	b.WriteString("\n")
	return b.String()
}

// Detached returns true if the user chose to detach.
func (m AttachModel) Detached() bool {
	return m.detached
}

// TweakFinishing returns true when the user pressed Ctrl+D to finish a tweak session.
func (m AttachModel) TweakFinishing() bool {
	return m.tweakFinishing
}

// Done returns true if the session has exited.
func (m AttachModel) Done() bool {
	if len(m.repoTabs) > 1 {
		return false
	}
	return m.done
}

// executePermChoice handles the selected permission menu choice and clears all menu state.
func (m *AttachModel) executePermChoice() tea.Cmd {
	reqID := m.pendingPermRequestID
	toolName := m.pendingPermToolName
	toolInputRaw := append(json.RawMessage(nil), m.pendingPermToolInput...)
	toolInput := string(toolInputRaw)
	repoName := m.permRepoName
	choice := m.permMenuChoice
	pattern := m.permMenuPattern
	featureID := m.sess.FeatureID()
	fail := func(err error) tea.Msg {
		return permissionAnswerFailedMsg{
			requestID: reqID,
			toolName:  toolName,
			toolInput: toolInputRaw,
			pattern:   pattern,
			choice:    choice,
			err:       err,
		}
	}

	// Emit permission resolution before clearing state
	if sc, ok := m.sessionSpanContext(); ok && m.observer != nil {
		decisionStr := permission.DecisionAllowOnce
		if choice == 1 {
			decisionStr = permission.DecisionAllowRemember
		} else if choice == 2 {
			decisionStr = "deny"
		}
		m.observer.PermissionResolved(sc, m.sess.ID(), m.sess.PermCacheScope(), m.sess.Iteration(), toolName, decisionStr)
	}

	// Clear all permission + menu state
	m.pendingPermRequestID = ""
	m.pendingPermToolName = ""
	m.pendingPermToolInput = nil
	m.showPermMenu = false
	m.permMenuChoice = 0
	m.permMenuPattern = ""

	switch choice {
	case 0: // Allow
		return m.resolvePermChoiceCmd(reqID, toolName, toolInput, permission.DecisionAllowOnce, "", "", featureID, fail)
	case 1: // Allow & Remember
		return m.resolvePermChoiceCmd(reqID, toolName, toolInput, permission.DecisionAllowRemember, pattern, repoName, featureID, fail)
	case 2: // Deny
		return m.resolvePermChoiceCmd(reqID, toolName, toolInput, permission.DecisionDeny, "", "", featureID, fail)
	}
	return nil
}

// resolvePermChoiceCmd dispatches a single permission decision through the
// session's explicit responder when available, otherwise through
// permission.NewAnswerService. pattern and repoName are only meaningful for
// DecisionAllowRemember; they are ignored (and safe to pass empty) for the
// other decisions. Shared by all three executePermChoice cases.
func (m *AttachModel) resolvePermChoiceCmd(reqID, toolName, toolInput, decision, pattern, repoName, featureID string, fail func(error) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		var err error
		if responder, ok := m.sess.(explicitPermissionResponder); ok {
			err = responder.RespondToPermissionDecision(reqID, decision, pattern, repoName)
		} else {
			var audit *permission.AuditSink
			if decision == permission.DecisionAllowRemember && m.permCache != nil && m.permCache.StoreRef() != nil {
				audit = permission.NewAuditSink(m.permCache.StoreRef().BaseDir)
			}
			_, err = permission.NewAnswerService(m.permCache, audit).Answer(permission.AnswerRequest{
				RequestID:        reqID,
				SessionID:        m.sess.ID(),
				FeatureID:        featureID,
				ToolName:         toolName,
				ToolInput:        toolInput,
				Decision:         decision,
				RememberPattern:  pattern,
				RememberScope:    repoName,
				RememberScopeSet: decision == permission.DecisionAllowRemember,
			}, func(requestID string, allow bool, reason string) error {
				return m.sess.RespondToControl(requestID, allow, reason)
			})
		}
		if err != nil {
			return fail(err)
		}
		m.sess.ResetWaitingStatus()
		return HelpResolvedMsg{FeatureID: featureID, RequestID: reqID}
	}
}

// findNextActiveTab returns the index of the next tab with an active session
// in the given direction (+1 for right, -1 for left). Returns -1 if no other
// active tab exists.
func (m AttachModel) findNextActiveTab(direction int) int {
	n := len(m.repoTabs)
	for offset := 1; offset < n; offset++ {
		idx := (m.activeTabIdx + direction*offset%n + n) % n
		if m.repoTabs[idx].sess != nil {
			return idx
		}
	}
	return -1
}

// recheckAutocompleteTrigger re-evaluates the trigger after a textarea update.
// Activates autocomplete if a new trigger is detected, updates the query if
// already active, or dismisses if the trigger is no longer valid.
// Returns a tea.Cmd when an async skill load is needed.
func (m AttachModel) recheckAutocompleteTrigger() (AttachModel, tea.Cmd) {
	value := m.input.Value()
	row := m.input.Line()
	col := m.input.Column()
	offset := cursorByteOffset(value, row, col)

	mode, trigOff, query, ok := detectTrigger(value, offset)
	if !ok {
		if m.autocomplete.active {
			m.autocomplete = m.autocomplete.Dismiss()
		}
		return m, nil
	}

	// File mode — always re-search on each keystroke (index pre-filters results).
	if mode == AutocompleteFile {
		return m.recheckFileAutocomplete(trigOff, query)
	}

	// Skill mode — original logic.
	if !m.autocomplete.active {
		if m.skillItemsLoaded {
			m.autocomplete = m.autocomplete.Activate(mode, trigOff, query, m.skillItems)
		} else if !m.skillsLoading {
			m.skillsLoading = true
			m.skillsWorkDir = m.sess.WorkDir()
			m.autocomplete = m.autocomplete.ActivateLoading(mode, trigOff, query)
			return m, loadSkillsCmd(m.sess.WorkDir(), m.skillsDir)
		} else {
			m.autocomplete = m.autocomplete.ActivateLoading(mode, trigOff, query)
		}
	} else {
		m.autocomplete = m.autocomplete.UpdateQuery(query)
	}
	return m, nil
}

// recheckFileAutocomplete handles @ file autocomplete: lazily builds the index,
// then searches it on each keystroke.
func (m AttachModel) recheckFileAutocomplete(trigOff int, query string) (AttachModel, tea.Cmd) {
	workDir := m.sess.WorkDir()

	// Index not ready yet — start building or show loading state.
	if m.fileIndex == nil || !m.fileIndex.Ready() {
		if !m.fileIndexLoading {
			m.fileIndexLoading = true
			m.fileIndexWorkDir = workDir
			m.autocomplete = m.autocomplete.Activate(AutocompleteFile, trigOff, query, nil)
			m.autocomplete = m.autocomplete.SetLoading(true)
			return m, buildFileIndexCmd(workDir)
		}
		// Already loading — keep loading state, update query for when index arrives.
		if !m.autocomplete.active {
			m.autocomplete = m.autocomplete.Activate(AutocompleteFile, trigOff, query, nil)
			m.autocomplete = m.autocomplete.SetLoading(true)
		} else {
			m.autocomplete = m.autocomplete.UpdateQuery(query)
		}
		return m, nil
	}

	// Index ready — search and show results.
	results := m.fileIndex.Search(query, autocompleteMaxVisible)
	items := make([]AutocompleteItem, len(results))
	for i, path := range results {
		items[i] = AutocompleteItem{
			Name:   path,
			Source: "file",
		}
	}
	m.autocomplete = m.autocomplete.Activate(AutocompleteFile, trigOff, query, items)
	m.autocomplete = m.autocomplete.SetLoading(false)
	return m, nil
}

// loadSkillsCmd returns a tea.Cmd that discovers all skills asynchronously.
func loadSkillsCmd(repoPath, skillsDir string) tea.Cmd {
	return func() tea.Msg {
		return skillsLoadedMsg{items: discoverAllSkills(repoPath, skillsDir), workDir: repoPath}
	}
}

// applyAutocompleteSelection replaces the trigger+query range in the textarea
// with the selected item's text.
func (m AttachModel) applyAutocompleteSelection(item AutocompleteItem) AttachModel {
	value := m.input.Value()
	triggerOffset := m.autocomplete.triggerOffset

	var triggerChar byte = '/'
	if m.autocomplete.mode == AutocompleteFile {
		triggerChar = '@'
	}

	cursorOffset := triggerOffset + 1 + len(m.autocomplete.query)
	if cursorOffset > len(value) {
		cursorOffset = len(value)
	}

	var replacement string
	if m.autocomplete.mode == AutocompleteSkill && item.Path != "" {
		// Insert a human-readable placeholder; sendChatInput will expand
		// it to the real path at send time (like [Image #N] for pastes).
		replacement = skillPlaceholder(item) + " "
		m.acceptedSkills = append(m.acceptedSkills, item)
	} else {
		replacement = string(triggerChar) + item.Name + " "
	}
	newValue := value[:triggerOffset] + replacement + value[cursorOffset:]
	m.input.SetValue(newValue)
	m.syncInputHeight()
	return m
}

// switchToTab switches the active session to a different repo tab.
func (m AttachModel) switchToTab(idx int) (AttachModel, tea.Cmd) {
	// Save current tab's pasted media state so it survives round-trips.
	if len(m.repoTabs) > 0 && m.activeTabIdx < len(m.repoTabs) {
		m.repoTabs[m.activeTabIdx].pastedImages = m.pastedImages
		m.repoTabs[m.activeTabIdx].pastedFiles = m.pastedFiles
		m.repoTabs[m.activeTabIdx].pastedFileNames = m.pastedFileNames
	}

	m.activeTabIdx = idx
	m.sess = m.repoTabs[idx].sess
	m.permRepoName = m.sess.PermCacheScope()
	m.tabGeneration++
	m.thinkingLine = "" // Reset before restoring from new tab's messages

	// Adapt layout to the target session.
	m.readOnly = false
	vpW, vpH, inputW := attachLayout(m.width, m.height, m.readOnly, len(m.repoTabs) > 0)
	m.viewport.SetWidth(vpW)
	m.viewport.SetHeight(vpH)
	m.input.SetWidth(inputW)

	// Reset interaction state
	m.pendingPermRequestID = ""
	m.pendingPermToolName = ""
	m.pendingPermToolInput = nil
	m.showPermMenu = false
	m.permMenuChoice = 0
	m.permMenuPattern = ""
	m.pendingQuestions = nil
	m.pendingAskRequestID = ""
	m.pendingAskQuestionsRaw = nil
	m.pendingAskQueue = nil
	m.collectedAnswers = nil
	m.currentQuestionIdx = 0
	m.selectedOption = 0
	m.typingCustom = false
	m.awaitingInput = false
	m.done = false
	m.autocomplete = m.autocomplete.Dismiss()
	m.skillItems = nil
	m.skillItemsLoaded = false
	m.skillsLoading = false
	m.skillsWorkDir = ""
	m.fileIndex = nil
	m.fileIndexLoading = false
	m.fileIndexWorkDir = ""
	// Restore target tab's pasted media state (imageCounter stays global
	// to avoid filename collisions in the shared imageTempDir).
	m.pastedImages = m.repoTabs[idx].pastedImages
	m.pastedFiles = m.repoTabs[idx].pastedFiles
	m.pastedFileNames = m.repoTabs[idx].pastedFileNames
	m.input.Reset()
	m.syncInputHeight()
	m.syncInputMode()

	// Check new session for pending control requests
	if !m.readOnly && m.sess != nil {
		appendMissingAutoPickedMessages(m.sess)
		m.restorePendingAskUserQuestions(m.sess)
		if !m.hasActiveQuestion() {
			m.restorePendingPermission(m.sess)
		}
		// Fallback: scan message log for unanswered AskUserQuestion tool_use blocks.
		// Same logic as NewAttachModel constructor.
		if !m.hasActiveQuestion() {
			allMsgs := m.sess.MessageLog().LastN(50)
			for i, scanMsg := range allMsgs {
				if scanMsg.Assistant != nil {
					for _, block := range scanMsg.Assistant.Message.Content {
						if block.IsToolUse() && block.Name == toolNameAskUserQuestion {
							answered := false
							for j := i + 1; j < len(allMsgs); j++ {
								laterMsg := allMsgs[j]
								if laterMsg.User != nil {
									hasToolResult := false
									isErrorResult := false
									for _, lb := range laterMsg.User.Message.Content {
										if lb.Type == "tool_result" && lb.ToolUseID == block.ID {
											hasToolResult = true
											isErrorResult = lb.IsError
											break
										}
									}
									if hasToolResult && isErrorResult {
										continue
									}
									if hasToolResult && !isErrorResult {
										answered = true
										break
									}
									answered = true
									break
								}
							}
							if answered {
								continue
							}
							if questions := m.parseAskUserQuestionsForDisplay(block.Input); len(questions) > 0 && !askUserQuestionsAlreadyAutoPicked(m.sess, questions) {
								m.activateAskUserQuestions(questions, "", block.Input)
							}
						}
					}
				}
			}
		}
	}

	// Update log path for footer
	if m.sess != nil {
		m.logPath = m.sess.LogFilePath()
	}

	m.restoreThinkingLine()
	m.emitRestoredObservability()
	m.updateViewport()

	gen := m.tabGeneration
	sess := m.sess
	return m, tea.Batch(
		drainAndPollAttachChCmd(sess, gen),
		waitForDoneCmd(sess, gen),
	)
}

// updateTabStatus updates a repo tab's status.
func (m *AttachModel) updateTabStatus(repoName string, status presentationStatus) {
	for i := range m.repoTabs {
		if m.repoTabs[i].repoName == repoName {
			m.repoTabs[i].status = status
			return
		}
	}
}

// rebuildTabs swaps the tab list for a freshly-built one, preserving per-tab
// pasted media and active-tab focus where the underlying session survives.
// Used during tab churn when sessions start (validators spawning at the start
// of plan validation) or end (validators completing) while the user is
// attached.
//
// Returns true if the active tab's session changed as a side effect (the
// previously active session is no longer in the list), so the caller can
// trigger the same re-subscription work switchToTab does.
func (m *AttachModel) rebuildTabs(next []repoTab) bool {
	if len(next) == 0 {
		return false
	}

	// Index existing tabs by a stable key so we can preserve per-tab state
	// across the rebuild. repoTab.repoName is the identity key: for repo-impl
	// tabs it's the repo name; for validator/helper tabs buildRepoTabs sets it
	// to the session ID.
	prevByKey := make(map[string]int, len(m.repoTabs))
	for i, t := range m.repoTabs {
		prevByKey[t.repoName] = i
	}

	for i := range next {
		if pi, ok := prevByKey[next[i].repoName]; ok {
			next[i].pastedImages = m.repoTabs[pi].pastedImages
			next[i].pastedFiles = m.repoTabs[pi].pastedFiles
			next[i].pastedFileNames = m.repoTabs[pi].pastedFileNames
		}
	}

	// Preserve the active tab if its identity still exists in the new list;
	// otherwise fall back to the first live tab.
	activeKey := ""
	var activeSess session.SessionView
	if m.activeTabIdx >= 0 && m.activeTabIdx < len(m.repoTabs) {
		activeKey = m.repoTabs[m.activeTabIdx].repoName
		activeSess = m.repoTabs[m.activeTabIdx].sess
	}
	newIdx := -1
	for i, t := range next {
		if t.repoName == activeKey && t.sess != nil {
			newIdx = i
			break
		}
	}
	sessionSwapped := false
	if newIdx < 0 {
		for i, t := range next {
			if t.sess != nil {
				newIdx = i
				break
			}
		}
		sessionSwapped = true
	} else if !sameAttachSession(activeSess, next[newIdx].sess) {
		sessionSwapped = true
	}
	if newIdx < 0 {
		for i, t := range next {
			if t.repoName == activeKey {
				newIdx = i
				break
			}
		}
	}

	m.repoTabs = next
	if newIdx >= 0 {
		m.activeTabIdx = newIdx
	}
	return sessionSwapped
}

func sameAttachSession(a, b session.SessionView) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if a.ID() != b.ID() {
		return false
	}
	aStarted := a.StartedAt()
	bStarted := b.StartedAt()
	if aStarted.IsZero() || bStarted.IsZero() {
		return true
	}
	return aStarted.Equal(bStarted)
}

// ActiveRepoName returns the name of the currently active repo tab.
// Returns empty string for single-session mode.
func (m AttachModel) ActiveRepoName() string {
	if len(m.repoTabs) == 0 {
		return ""
	}
	return m.repoTabs[m.activeTabIdx].repoName
}

func registerAttachConsumer(sess session.SessionView) func() {
	if registrar, ok := sess.(ports.AttachConsumerRegistrar); ok {
		return registrar.RegisterAttachConsumer()
	}
	return func() {}
}

// drainAndPollAttachChCmd discards stale messages from the attach channel
// before blocking for the first fresh message. Use this on initial attach —
// restoreThinkingLine() already established the correct spinner state from
// the message log, so stale buffered messages can only corrupt it.
func drainAndPollAttachChCmd(sess session.SessionView, gen int) tea.Cmd {
	return func() tea.Msg {
		unregister := registerAttachConsumer(sess)
		defer unregister()
		ch := sess.AttachCh()
		// Drain stale buffered messages.
		for len(ch) > 0 {
			if _, ok := <-ch; !ok {
				return attachDoneMsg{generation: gen}
			}
		}
		// Block for the first fresh message, then batch-drain.
		return pollAttachCh(ch, gen)
	}
}

func pollAttachChCmd(sess session.SessionView, gen int) tea.Cmd {
	return func() tea.Msg {
		unregister := registerAttachConsumer(sess)
		defer unregister()
		return pollAttachCh(sess.AttachCh(), gen)
	}
}

func pollAttachCh(ch <-chan llm.SDKMessage, gen int) tea.Msg {
	msg, ok := <-ch
	if !ok {
		return attachDoneMsg{generation: gen}
	}
	msgs := []llm.SDKMessage{msg}
	for {
		select {
		case m, ok := <-ch:
			if !ok {
				return attachMsgsMsg{generation: gen, messages: msgs}
			}
			msgs = append(msgs, m)
		default:
			return attachMsgsMsg{generation: gen, messages: msgs}
		}
	}
}

func waitForDoneCmd(sess session.SessionView, gen int) tea.Cmd {
	return func() tea.Msg {
		<-sess.Done()
		return attachDoneMsg{generation: gen}
	}
}

// pastedMediaMap holds pasted image/file paths and autocompleted skill items
// for rendering placeholders in the chat viewport. Nil means no replacements.
type pastedMediaMap struct {
	images    []string           // ordered image temp paths
	files     []string           // ordered file temp paths
	fileNames []string           // display names parallel to files
	skills    []AutocompleteItem // autocompleted skill items (path → styled label)
}

// placeholderStyle is the lipgloss style for pasted media placeholders.
var placeholderStyle = lipgloss.NewStyle().Foreground(colorBrand).Italic(true)

// replacePastedPaths replaces raw pasted image/file paths and skill paths in
// text with styled placeholders. Returns the original text unchanged if media
// is nil or empty.
func replacePastedPaths(text string, media *pastedMediaMap) string {
	if media == nil {
		return text
	}
	for i, path := range media.images {
		placeholder := placeholderStyle.Render(fmt.Sprintf("[Image #%d]", i+1))
		text = strings.ReplaceAll(text, path, placeholder)
	}
	for i, path := range media.files {
		name := path
		if i < len(media.fileNames) {
			name = media.fileNames[i]
		}
		placeholder := placeholderStyle.Render(fmt.Sprintf("[%s]", name))
		text = strings.ReplaceAll(text, path, placeholder)
	}
	for _, sk := range media.skills {
		styled := placeholderStyle.Render(skillPlaceholder(sk))
		text = strings.ReplaceAll(text, sk.Path, styled)
	}
	return text
}

// expandMediaPlaceholders is the inverse of replacePastedPaths: it replaces
// human-readable placeholders ([Image #N], [filename]) with the actual file
// paths so the agent receives usable paths. Returns text unchanged if media is
// nil or empty.
func expandMediaPlaceholders(text string, media *pastedMediaMap) string {
	if media == nil {
		return text
	}
	for i, path := range media.images {
		placeholder := fmt.Sprintf("[Image #%d]", i+1)
		text = strings.ReplaceAll(text, placeholder, path)
	}
	for i, path := range media.files {
		name := path
		if i < len(media.fileNames) {
			name = media.fileNames[i]
		}
		placeholder := fmt.Sprintf("[%s]", name)
		text = strings.ReplaceAll(text, placeholder, path)
	}
	return text
}

// updateViewport re-renders messages into the viewport content.
// Auto-scroll to the bottom only if the user was already scrolled to the
// bottom (or near it). This lets users scroll up freely to review earlier
// output without being yanked back by incoming messages.
func (m *AttachModel) updateViewport() {
	// Check scroll position before updating content.
	// AtBottom() returns true when the viewport cannot scroll further down.
	wasAtBottom := m.viewport.AtBottom() || m.viewport.TotalLineCount() <= m.viewport.Height()

	msgs := m.sess.MessageLog().LastN(attachViewportMessageLimit)
	content := m.renderViewportContent(msgs)
	m.viewport.SetContent(content)

	if wasAtBottom {
		m.viewport.GotoBottom()
	}
}

// renderViewportContent builds the full viewport content: the initial prompt
// header (gray background) followed by rendered messages.
func (m *AttachModel) renderViewportContent(msgs []llm.SDKMessage) string {
	var b strings.Builder

	// Render the initial prompt at the top with a gray background, so the
	// user always has context about what the agent was asked to do.
	if prompt := m.sess.InitialPrompt(); prompt != "" {
		promptBg := lipgloss.NewStyle().
			Background(compat.AdaptiveColor{Light: lipgloss.Color("#dce0e8"), Dark: lipgloss.Color("#313244")}).
			Foreground(compat.AdaptiveColor{Light: lipgloss.Color("#4c4f69"), Dark: lipgloss.Color("#cdd6f4")}).
			Width(m.viewport.Width()).
			Padding(0, 1)
		b.WriteString(promptBg.Render(prompt))
		b.WriteString("\n\n")
	}

	baseMessageCount := m.sess.MessageLog().Len() - len(msgs)
	if baseMessageCount < 0 {
		baseMessageCount = 0
	}
	fileEvents := buildAttachFileEvents(msgs, baseMessageCount)
	m.enrichFileEventsWithDiffs(fileEvents)

	var media *pastedMediaMap
	if len(m.pastedImages) > 0 || len(m.pastedFiles) > 0 || len(m.acceptedSkills) > 0 {
		media = &pastedMediaMap{
			images:    m.pastedImages,
			files:     m.pastedFiles,
			fileNames: m.pastedFileNames,
			skills:    m.acceptedSkills,
		}
	}
	b.WriteString(renderAttachTranscript(msgs, fileEvents, baseMessageCount, m.filter, m.viewport.Width(), media))

	return b.String()
}

func (m AttachModel) renderSpinnerLine() string {
	if m.thinkingLine == "" || m.filter >= filterNoTools {
		return ""
	}
	line := strings.Join(strings.Fields(strings.ReplaceAll(m.thinkingLine, "\r", "\n")), " ")
	if line == "" {
		line = thinkingLineText
	}

	// A turn is considered in progress from the moment the user sends
	// input until the session emits a Result. While the turn is active,
	// silence on stdout is not evidence of a stall — the agent may be
	// running a long local tool, waiting for API latency, or doing
	// extended thinking without streaming deltas. Always animate the
	// spinner in this state.
	if m.turnActive {
		rendered := "  " + m.spinnerView + " " + chatThinkingStyle.Render(line)
		if w := m.viewport.Width(); w > 0 {
			rendered = ansi.Truncate(rendered, w, "…")
		}
		return rendered
	}

	// Defensive fallback for !turnActive: the thinkingLine should have
	// been cleared when the turn ended, so this path is mostly unreachable.
	// If we somehow hold a stale thinkingLine and the session process has
	// ended, show a clear "not responding" indicator; otherwise render the
	// last-known-active spinner so the UI never silently goes blank.
	if m.sess != nil && !m.sess.IsActive() && !m.done {
		rendered := "  " + WarningStyle.Render(fmt.Sprintf("⚠ %s — session not responding", line))
		if w := m.viewport.Width(); w > 0 {
			rendered = ansi.Truncate(rendered, w, "…")
		}
		return rendered
	}
	rendered := "  " + m.spinnerView + " " + chatThinkingStyle.Render(line)
	if w := m.viewport.Width(); w > 0 {
		rendered = ansi.Truncate(rendered, w, "…")
	}
	return rendered
}

func renderAttachTranscript(msgs []llm.SDKMessage, fileEvents []attachFileEvent, baseMessageCount int, filter attachFilter, viewportWidth int, media *pastedMediaMap) string {
	if len(fileEvents) == 0 {
		return renderAttachMessages(msgs, filter, viewportWidth, media)
	}

	grouped := make(map[int][]attachFileEvent, len(fileEvents))
	for _, event := range fileEvents {
		relativePos := event.afterMessageCount - baseMessageCount
		if relativePos < 1 || relativePos > len(msgs) {
			continue
		}
		grouped[relativePos] = append(grouped[relativePos], event)
	}

	var b strings.Builder
	start := 0
	for pos := 1; pos <= len(msgs); pos++ {
		events := grouped[pos]
		if len(events) == 0 {
			continue
		}
		if pos > start {
			b.WriteString(renderAttachMessages(msgs[start:pos], filter, viewportWidth, media))
			start = pos
		}
		if filter == filterTextOnly {
			continue
		}
		for _, event := range events {
			b.WriteString(renderFileEvent(event.change, viewportWidth))
		}
	}
	if start < len(msgs) {
		b.WriteString(renderAttachMessages(msgs[start:], filter, viewportWidth, media))
	}
	return b.String()
}

// renderAttachMessages renders messages for the attach view with styling.
// When media is non-nil, raw pasted paths in locally-appended user messages
// are replaced with styled placeholders.
func renderAttachMessages(msgs []llm.SDKMessage, filter attachFilter, viewportWidth int, media *pastedMediaMap) string {
	wrapWidth := max(viewportWidth-2, 20)
	var b strings.Builder
	lastAssistantText := ""
	for _, msg := range msgs {
		switch {
		case msg.Init != nil:
			// Always hide raw init — it's noisy metadata, not useful in attach view.
			lastAssistantText = ""
			continue

		case msg.Assistant != nil:
			for _, block := range msg.Assistant.Message.Content {
				switch {
				case block.IsText():
					if shouldSkipDuplicateAttachAssistantText(lastAssistantText, block.Text) {
						continue
					}
					var rendered string
					if msg.Subtype == "partial" {
						// Streaming deltas routinely contain half-written
						// fenced code blocks; markdown-parsing them can produce
						// incomplete output. Keep partials as plain wrapped text
						// and let the final message snap into styled markdown
						// once the turn completes.
						rendered = lipgloss.NewStyle().Width(wrapWidth).Render(block.Text)
					} else {
						rendered = renderMarkdown(block.Text, wrapWidth)
					}
					for _, line := range strings.Split(rendered, "\n") {
						b.WriteString("  ")
						b.WriteString(line)
						b.WriteByte('\n')
					}
					lastAssistantText = strings.TrimSpace(block.Text)
				case block.IsToolUse():
					if filter < filterNoTools {
						if rendered := renderAttachDelegationToolUse(block, viewportWidth); rendered != "" {
							b.WriteString(rendered)
							lastAssistantText = ""
							continue
						}
					}
					// Other tool use blocks are shown as a single spinner
					// line at the bottom of the viewport (like chat model).
					lastAssistantText = ""
					continue
				case block.IsThinking():
					// Thinking blocks are collapsed into the spinner line.
					lastAssistantText = ""
					continue
				}
			}

		case msg.User != nil:
			lastAssistantText = ""
			for _, block := range msg.User.Message.Content {
				if block.IsText() {
					if msg.LocallyAppended {
						// Messages typed by the human in the attach view.
						// Rendered with a left accent bar and surrounding
						// blank lines to visually separate them from the
						// assistant's response above and below.
						label, tagStyle := autoPickedTag(msg.AutoPicked, msg.AutoPickConfidence)
						barStyle := tagStyle
						labelStyle := tagStyle
						displayText := replacePastedPaths(block.Text, media)
						bar := barStyle.Render("▍") + " "
						prefix := labelStyle.Render(label) + " "
						wrapped := lipgloss.NewStyle().Width(wrapWidth).Render(displayText)
						b.WriteByte('\n')
						first := true
						for _, line := range strings.Split(wrapped, "\n") {
							b.WriteString(bar)
							if first {
								b.WriteString(prefix)
								first = false
							}
							b.WriteString(line)
							b.WriteByte('\n')
						}
						b.WriteByte('\n')
					} else {
						// Protocol-stream user messages are orchestrator
						// prompts (initial prompt, sub-agent input, etc.).
						// Render in a collapsed box so they don't overwhelm
						// the assistant text.
						if filter >= filterNoTools {
							continue
						}
						b.WriteByte('\n')
						b.WriteString(renderAttachPromptBox(block.Text, "prompt", viewportWidth))
						b.WriteString("\n\n")
					}
				}
			}

		case msg.Result != nil:
			lastAssistantText = ""
			if filter == filterTextOnly {
				continue
			}
			if msg.Result.IsClientSideResult() {
				// CLI-side errors (e.g. "Unknown skill: X") arrive as
				// success with zero output tokens. Show them in red.
				errStyle := lipgloss.NewStyle().Foreground(colorError)
				b.WriteString(errStyle.Render(fmt.Sprintf("  [error] %s", msg.Result.Result)))
				b.WriteByte('\n')
				continue
			}
			if msg.Result.IsSuccess() {
				continue
			}
			// Render interrupt-driven terminations (Claude:
			// "error_during_execution", Codex: "Turn interrupted" via
			// "error") as a friendly confirmation instead of the raw
			// subtype, which otherwise reads like a genuine failure.
			if isInterruptResult(msg.Result) {
				interruptStyle := MutedStyle
				b.WriteString(interruptStyle.Render(fmt.Sprintf("  [interrupted] cost=$%.4f", msg.Result.TotalCostUSD)))
				b.WriteByte('\n')
				continue
			}
			resultStyle := lipgloss.NewStyle().Foreground(colorSuccess)
			if msg.Result.Subtype == "error" {
				resultStyle = lipgloss.NewStyle().Foreground(colorError)
			}
			b.WriteString(resultStyle.Render(fmt.Sprintf("  [result] %s cost=$%.4f", msg.Result.Subtype, msg.Result.TotalCostUSD)))
			b.WriteByte('\n')

		case msg.ControlRequest != nil:
			lastAssistantText = ""
			// Skip hook_callback messages — they are auto-handled internally
			if msg.ControlRequest.Request.Subtype == "hook_callback" {
				continue
			}
			if msg.ControlRequest.Request.Subtype == controlRequestSubtypeCanUseTool && msg.ControlRequest.Request.ToolName != "" {
				if filter >= filterNoTools {
					continue
				}
				toolStyle := lipgloss.NewStyle().Foreground(colorWarning).Bold(true)
				b.WriteString(toolStyle.Render(fmt.Sprintf("  [tool_use] %s", msg.ControlRequest.Request.ToolName)))
				b.WriteByte('\n')
				continue
			}
			if filter == filterTextOnly {
				continue
			}
			permStyle := lipgloss.NewStyle().Foreground(colorWarning).Bold(true)
			b.WriteString(permStyle.Render(fmt.Sprintf("  [permission] %s: %s", msg.ControlRequest.Request.Subtype, msg.ControlRequest.Request.ToolName)))
			b.WriteByte('\n')

		case msg.Status != nil:
			lastAssistantText = ""
			if filter == filterTextOnly {
				continue
			}
			b.WriteString(MutedStyle.Render(fmt.Sprintf("  [status] %s", msg.Status.Message)))
			b.WriteByte('\n')

		case msg.TaskStarted != nil:
			lastAssistantText = ""
			if filter >= filterNoTools {
				continue
			}
			b.WriteString(renderAttachTaskStarted(msg.TaskStarted, viewportWidth))

		case msg.TaskProgress != nil:
			lastAssistantText = ""
			if filter >= filterNoTools {
				continue
			}
			b.WriteString(renderAttachTaskProgress(msg.TaskProgress))

		case msg.TaskNotification != nil:
			lastAssistantText = ""
			if filter >= filterNoTools {
				continue
			}
			b.WriteString(renderAttachTaskNotification(msg.TaskNotification))

		case msg.ToolProgress != nil:
			// Tool progress is shown via the spinner line, not as permanent entries.
			lastAssistantText = ""
			continue

		case msg.RateLimit != nil:
			lastAssistantText = ""
			// Rate limit notifications are noisy (fire multiple times per turn)
			// and not actionable — usage limit exhaustion surfaces as a turn
			// failure instead. Suppress at all filter levels.
			continue

		case msg.Compact != nil:
			lastAssistantText = ""
			if filter == filterTextOnly {
				continue
			}
			b.WriteString(MutedStyle.Render("  [compact] context compacted"))
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func renderAttachDelegationToolUse(block llm.ContentBlock, viewportWidth int) string {
	if !isAttachDelegationTool(block.Name) {
		return ""
	}
	fields := jsonObjectFields(block.Input)
	summary := firstNonEmptyString(fields, "description", "summary", "title")
	prompt := firstNonEmptyString(fields, "prompt", "instructions")
	if summary == "" {
		summary = prompt
	}

	line := block.Name
	if detail := singleLine(summary); detail != "" {
		line += ": " + detail
	}
	toolStyle := lipgloss.NewStyle().Foreground(colorInfo).Bold(true)
	var b strings.Builder
	b.WriteString(toolStyle.Render("  [tool_use] " + line))
	b.WriteByte('\n')
	if strings.TrimSpace(prompt) != "" {
		b.WriteByte('\n')
		b.WriteString(renderAttachPromptBox(prompt, "sub-agent prompt", viewportWidth))
		b.WriteString("\n\n")
	}
	return b.String()
}

func isAttachDelegationTool(name string) bool {
	switch name {
	case toolNameAgent, "Task", toolNameTaskCreate:
		return true
	default:
		return false
	}
}

func renderAttachTaskStarted(msg *llm.TaskStartedMessage, viewportWidth int) string {
	if msg == nil {
		return ""
	}
	line := "Task started"
	if detail := firstNonEmpty(msg.Description, msg.TaskType, msg.TaskID); detail != "" {
		line += ": " + detail
	}
	var b strings.Builder
	b.WriteString(attachTaskStyle(false).Render("  [task] " + line))
	b.WriteByte('\n')
	if strings.TrimSpace(msg.Prompt) != "" {
		b.WriteByte('\n')
		b.WriteString(renderAttachPromptBox(msg.Prompt, "sub-agent prompt", viewportWidth))
		b.WriteString("\n\n")
	}
	return b.String()
}

func renderAttachTaskProgress(msg *llm.TaskProgressMessage) string {
	if msg == nil {
		return ""
	}
	line := "Task progress"
	if detail := firstNonEmpty(msg.Description, msg.TaskID); detail != "" {
		line += ": " + detail
	}
	if msg.LastToolName != "" {
		line += " via " + msg.LastToolName
	}
	return attachTaskStyle(false).Render("  [task] "+line) + "\n"
}

func renderAttachTaskNotification(msg *llm.TaskNotificationMessage) string {
	if msg == nil {
		return ""
	}
	status := firstNonEmpty(msg.Status, "notification")
	line := "Task " + status
	if detail := firstNonEmpty(msg.Summary, msg.OutputFile, msg.TaskID); detail != "" {
		line += ": " + detail
	}
	return attachTaskStyle(status == string(statusFailed) || status == "error").Render("  [task] "+line) + "\n"
}

func attachTaskStyle(warning bool) lipgloss.Style {
	if warning {
		return lipgloss.NewStyle().Foreground(colorWarning).Bold(true)
	}
	return lipgloss.NewStyle().Foreground(colorInfo).Bold(true)
}

func renderAttachPromptBox(text, title string, viewportWidth int) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if strings.TrimSpace(text) == "" {
		return ""
	}
	boxWidth := max(viewportWidth-4, 20)
	wrapped := lipgloss.NewStyle().Width(boxWidth - 4).Render(text)
	lines := strings.Split(wrapped, "\n")
	var content string
	if len(lines) > promptMaxLines {
		content = strings.Join(lines[:promptMaxLines-1], "\n") + "\n" + MutedStyle.Render("...")
	} else {
		content = strings.Join(lines, "\n")
	}
	box := panelStyle(false).Width(boxWidth).Render(content)
	titleStyle := lipgloss.NewStyle().Foreground(compat.AdaptiveColor{Light: lipgloss.Color("#6c6f85"), Dark: lipgloss.Color("#6c7086")}).Bold(true)
	return renderBorderTitle(box, title, titleStyle)
}

func renderFileEvent(change attachFileChange, viewportWidth int) string {
	boxWidth := max(viewportWidth-4, 20)
	op := change.Operation
	if op != "" {
		op = strings.ToUpper(op[:1]) + op[1:]
	}
	path := change.Path
	if change.Operation == "rename" && change.OldPath != "" {
		path = change.OldPath + " -> " + change.Path
	}

	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Foreground(colorSuccess).Render("•"))
	b.WriteString(" ")
	b.WriteString(lipgloss.NewStyle().Bold(true).Render(fmt.Sprintf("%s(%s)", op, path)))
	b.WriteByte('\n')

	if change.HasDiffPatch {
		// Summary line with colored counts.
		summary := "└ "
		if change.AddedLines > 0 {
			summary += SuccessStyle.Render(fmt.Sprintf("Added %d", change.AddedLines))
		}
		if change.AddedLines > 0 && change.RemovedLines > 0 {
			summary += MutedStyle.Render(", ")
		}
		if change.RemovedLines > 0 {
			summary += DiffRemoveStyle.Render(fmt.Sprintf("removed %d", change.RemovedLines))
		}
		if change.AddedLines > 0 || change.RemovedLines > 0 {
			lines := "lines"
			if change.AddedLines+change.RemovedLines == 1 {
				lines = "line"
			}
			summary += MutedStyle.Render(" " + lines)
		}
		b.WriteString(summary)
		b.WriteByte('\n')
		b.WriteString(renderFileEventDiff(change.Detail, boxWidth))
	} else {
		detail := strings.TrimSpace(change.Detail)
		if detail == "" {
			detail = "Captured from tool activity."
		}
		b.WriteString(renderFileEventDetail(detail))
	}
	b.WriteByte('\n')

	box := panelStyle(false).Width(boxWidth).Render(strings.TrimRight(b.String(), "\n"))
	return "\n" + box + "\n\n"
}

// renderFileEventDiff renders a compact unified diff patch with line numbers
// and colored +/- lines using the existing diff style palette.
func renderFileEventDiff(patch string, viewportWidth int) string {
	lines := strings.Split(patch, "\n")
	contentWidth := max(viewportWidth-6, 20) // account for box padding + line number gutter

	var b strings.Builder
	var oldLine, newLine int
	maxLineNum := 0

	// Pre-scan for max line number to determine gutter width.
	for _, line := range lines {
		if strings.HasPrefix(line, "@@") {
			var oStart, nStart int
			if _, err := fmt.Sscanf(line, "@@ -%d", &oStart); err == nil && oStart > maxLineNum {
				maxLineNum = oStart
			}
			if idx := strings.Index(line, "+"); idx >= 0 {
				fmt.Sscanf(line[idx:], "+%d", &nStart)
				if nStart > maxLineNum {
					maxLineNum = nStart
				}
			}
		}
	}
	// Estimate max reachable line number.
	maxLineNum += len(lines)
	gutterWidth := len(fmt.Sprintf("%d", maxLineNum))
	if gutterWidth < 3 {
		gutterWidth = 3
	}

	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"):
			continue
		case strings.HasPrefix(line, "@@"):
			// Parse hunk header.
			fmt.Sscanf(line, "@@ -%d", &oldLine)
			if idx := strings.Index(line, "+"); idx >= 0 {
				fmt.Sscanf(line[idx:], "+%d", &newLine)
			}
			b.WriteString(DiffHunkStyle.Render(line))
			b.WriteByte('\n')
		case line == "...":
			b.WriteString(MutedStyle.Render("  ..."))
			b.WriteByte('\n')
		case strings.HasPrefix(line, "+"):
			numStr := fmt.Sprintf("%*d", gutterWidth, newLine)
			content := ansi.Truncate(line, contentWidth, "…")
			b.WriteString(DiffAddStyle.Render(numStr + " " + content))
			b.WriteByte('\n')
			newLine++
		case strings.HasPrefix(line, "-"):
			numStr := fmt.Sprintf("%*d", gutterWidth, oldLine)
			content := ansi.Truncate(line, contentWidth, "…")
			b.WriteString(DiffRemoveStyle.Render(numStr + " " + content))
			b.WriteByte('\n')
			oldLine++
		default:
			// Context line.
			numStr := fmt.Sprintf("%*d", gutterWidth, newLine)
			content := ansi.Truncate("  "+strings.TrimPrefix(line, " "), contentWidth, "…")
			b.WriteString(MutedStyle.Render(numStr) + " " + content)
			b.WriteByte('\n')
			oldLine++
			newLine++
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func buildAttachFileEvents(msgs []llm.SDKMessage, baseMessageCount int) []attachFileEvent {
	if len(msgs) == 0 {
		return nil
	}

	events := make([]attachFileEvent, 0, 8)
	seen := make(map[string]struct{})
	for i, msg := range msgs {
		anchor := baseMessageCount + i + 1
		appendChange := func(change attachFileChange) {
			if strings.TrimSpace(change.Path) == "" {
				return
			}
			key := fmt.Sprintf("%d|%s|%s|%s|%s", anchor, change.Operation, change.OldPath, change.Path, change.Detail)
			if _, ok := seen[key]; ok {
				return
			}
			seen[key] = struct{}{}
			events = append(events, attachFileEvent{
				afterMessageCount: anchor,
				change:            change,
			})
		}

		if msg.Assistant != nil {
			for _, block := range msg.Assistant.Message.Content {
				for _, change := range fileChangesFromToolUse(block) {
					appendChange(change)
				}
			}
		}
		if msg.ToolProgress != nil {
			for _, change := range fileChangesFromToolProgress(*msg.ToolProgress) {
				appendChange(change)
			}
		}
		for _, change := range fileChangesFromSDKFileChanges(msg.FileChanges) {
			appendChange(change)
		}
	}

	if len(events) > attachViewportFileLimit {
		events = events[len(events)-attachViewportFileLimit:]
	}
	return events
}

func (m *AttachModel) enrichFileEventsWithDiffs(events []attachFileEvent) {
	currentGen := m.sess.MessageLog().Len()
	if currentGen != m.diffCacheGeneration {
		m.diffCache = make(map[string]*git.DiffPreview)
		m.diffCacheGeneration = currentGen
	}

	workDir := m.sess.WorkDir()
	if workDir == "" || len(events) == 0 {
		return
	}

	for i := range events {
		relPath := makeRelativePath(workDir, events[i].change.Path)
		preview, cached := m.diffCache[relPath]
		if !cached {
			preview, _ = git.SingleFileDiffPreview(workDir, relPath)
			m.diffCache[relPath] = preview
		}
		applyDiffPreview(&events[i].change, preview, relPath)
	}
}

func applyDiffPreview(change *attachFileChange, preview *git.DiffPreview, relPath string) {
	change.Path = relPath
	if preview == nil || preview.Patch == "" {
		return
	}
	change.Detail = preview.Patch
	change.AddedLines = preview.AddedLines
	change.RemovedLines = preview.RemovedLines
	change.HasDiffPatch = true
	if preview.Operation != "" {
		change.Operation = preview.Operation
	}
}

func makeRelativePath(workDir, filePath string) string {
	if filePath == "" || workDir == "" {
		return filePath
	}
	if !strings.HasPrefix(filePath, "/") {
		return filePath
	}
	dir := workDir
	if !strings.HasSuffix(dir, "/") {
		dir += "/"
	}
	if strings.HasPrefix(filePath, dir) {
		return filePath[len(dir):]
	}
	return filePath
}

func fileChangesFromToolUse(block llm.ContentBlock) []attachFileChange {
	if !block.IsToolUse() {
		return nil
	}
	input := jsonObjectFields(block.Input)
	switch block.Name {
	case toolNameEdit, toolNameMultiEdit, toolNameWrite:
		path := firstNonEmptyString(input, "file_path", "path", "target_file")
		if path == "" {
			return nil
		}
		op := "update"
		if block.Name == toolNameWrite && strings.TrimSpace(firstNonEmptyString(input, "old_string")) == "" {
			op = "write"
		}
		detail := buildToolUseFileChangeDetail(block.Name, input)
		if detail == "" {
			detail = "Captured from tool usage."
		}
		return []attachFileChange{{Path: path, Operation: op, Detail: detail}}
	case "Delete":
		path := firstNonEmptyString(input, "file_path", "path")
		if path == "" {
			return nil
		}
		return []attachFileChange{{Path: path, Operation: "delete", Detail: "Captured from tool usage."}}
	case "Move", "Rename":
		oldPath := firstNonEmptyString(input, "old_path", "source_path", "from")
		newPath := firstNonEmptyString(input, "new_path", "destination_path", "to", "path")
		if newPath == "" {
			return nil
		}
		return []attachFileChange{{Path: newPath, OldPath: oldPath, Operation: "rename", Detail: "Captured from tool usage."}}
	default:
		return nil
	}
}

func fileChangesFromToolProgress(progress llm.ToolProgressMessage) []attachFileChange {
	if toolProgressFailed(progress.Data) {
		return nil
	}
	var path string
	switch progress.ToolName {
	case toolNameWrite, toolNameEdit:
		path = extractFilePathFromProgress(progress.Data)
	case toolNameBash:
		path = extractExplicitProgressFile(progress.Data)
	default:
		return nil
	}
	if path == "" {
		return nil
	}
	detail := truncateFileChangeDetail(progress.Data)
	return []attachFileChange{{
		Path:      path,
		Operation: "update",
		Detail:    detail,
	}}
}

var attachExplicitProgressFileRe = regexp.MustCompile(`(?m)^File:\s*(\S+)`)

func extractExplicitProgressFile(data string) string {
	matches := attachExplicitProgressFileRe.FindStringSubmatch(data)
	if len(matches) < 2 {
		return ""
	}
	return strings.Trim(matches[1], "\"'.,:;)")
}

func toolProgressFailed(data string) bool {
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(strings.ToLower(line))
		if line == "" {
			continue
		}
		return line == "failed" || strings.HasPrefix(line, "status: failed")
	}
	return false
}

func fileChangesFromSDKFileChanges(changes []llm.FileChangeEvent) []attachFileChange {
	if len(changes) == 0 {
		return nil
	}
	out := make([]attachFileChange, 0, len(changes))
	for _, change := range changes {
		if strings.TrimSpace(change.Path) == "" {
			continue
		}
		op := strings.TrimSpace(change.Operation)
		if op == "" {
			op = "update"
		}
		detail := strings.TrimSpace(change.Detail)
		if detail == "" {
			detail = "Captured from tool activity."
		}
		out = append(out, attachFileChange{
			Path:         change.Path,
			OldPath:      change.OldPath,
			Operation:    op,
			Detail:       detail,
			AddedLines:   change.AddedLines,
			RemovedLines: change.RemovedLines,
			HasDiffPatch: change.HasDiffPatch,
		})
	}
	return out
}

func renderFileEventDetail(detail string) string {
	lines := strings.Split(strings.TrimSpace(detail), "\n")
	if len(lines) == 0 {
		return MutedStyle.Render("└ Captured from tool activity.")
	}

	var b strings.Builder
	for i, line := range lines {
		line = strings.TrimRight(line, " ")
		prefix := "  "
		if i == 0 {
			prefix = "└ "
		}
		styled := prefix + line
		switch {
		case strings.HasPrefix(line, "+"):
			b.WriteString(DiffAddStyle.Render(styled))
		case strings.HasPrefix(line, "-"):
			b.WriteString(DiffRemoveStyle.Render(styled))
		default:
			b.WriteString(MutedStyle.Render(styled))
		}
		if i < len(lines)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func buildToolUseFileChangeDetail(toolName string, fields map[string]interface{}) string {
	oldString := firstNonEmptyString(fields, "old_string", "oldText")
	newString := firstNonEmptyString(fields, "new_string", "newText", "content")

	switch toolName {
	case toolNameEdit, toolNameMultiEdit:
		if oldString == "" && newString == "" {
			return ""
		}
		return truncateFileChangeDetail(formatSimpleReplacement(oldString, newString))
	case toolNameWrite:
		if newString == "" {
			return ""
		}
		return truncateFileChangeDetail("+ " + newString)
	default:
		return ""
	}
}

func formatSimpleReplacement(oldString, newString string) string {
	var lines []string
	if strings.TrimSpace(oldString) != "" {
		for _, line := range strings.Split(strings.TrimSuffix(oldString, "\n"), "\n") {
			lines = append(lines, "- "+line)
		}
	}
	if strings.TrimSpace(newString) != "" {
		for _, line := range strings.Split(strings.TrimSuffix(newString, "\n"), "\n") {
			lines = append(lines, "+ "+line)
		}
	}
	return strings.Join(lines, "\n")
}

func truncateFileChangeDetail(detail string) string {
	detail = strings.TrimSpace(strings.ReplaceAll(detail, "\r\n", "\n"))
	if detail == "" {
		return ""
	}

	const (
		maxChars = 2000
		maxLines = 24
	)

	if len(detail) > maxChars {
		detail = strings.TrimSpace(detail[:maxChars]) + "\n..."
	}

	lines := strings.Split(detail, "\n")
	if len(lines) > maxLines {
		lines = append(lines[:maxLines], "...")
	}

	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func jsonObjectFields(raw json.RawMessage) map[string]interface{} {
	if len(raw) == 0 {
		return nil
	}
	var fields map[string]interface{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil
	}
	return fields
}

func firstNonEmptyString(fields map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		value, ok := fields[key]
		if !ok {
			continue
		}
		if s, ok := value.(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

var attachProgressPathRe = regexp.MustCompile(`(?:^|[\s('"])((?:/|\.?/)?[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)+|(?:[A-Za-z0-9_.-]+/)+[A-Za-z0-9_.-]+)`)

func extractFilePathFromProgress(data string) string {
	matches := attachProgressPathRe.FindStringSubmatch(data)
	if len(matches) < 2 {
		return ""
	}
	return strings.Trim(matches[1], "\"'.,:;)")
}

func shouldSkipDuplicateAttachAssistantText(previous, current string) bool {
	previous = strings.TrimSpace(previous)
	current = strings.TrimSpace(current)
	return previous != "" && previous == current
}

// formatPermissionDetail extracts a human-readable detail from a tool's input
// for display in the permission prompt. For Bash it shows the command; for
// Write/Edit it shows the file path; for others it shows the raw input summary.
// renderQueueIndicator shows truncated queued messages above the input.
// Returns empty string when no messages are queued.
func (m AttachModel) renderQueueIndicator() string {
	if len(m.queuedMessages) == 0 {
		return ""
	}
	queueStyle := lipgloss.NewStyle().
		Foreground(compat.AdaptiveColor{Light: lipgloss.Color("#6c6f85"), Dark: lipgloss.Color("#6c7086")}).
		Italic(true)
	const maxVisible = 3
	const maxMsgLen = 40
	var parts []string
	shown := min(len(m.queuedMessages), maxVisible)
	for i := range shown {
		msg := m.queuedMessages[i]
		msg = strings.ReplaceAll(msg, "\n", " ")
		if len(msg) > maxMsgLen {
			msg = msg[:maxMsgLen] + "…"
		}
		parts = append(parts, fmt.Sprintf("\"%s\"", msg))
	}
	label := fmt.Sprintf("queued: %s", strings.Join(parts, " | "))
	if len(m.queuedMessages) > maxVisible {
		label += fmt.Sprintf(" +%d more", len(m.queuedMessages)-maxVisible)
	}
	return queueStyle.Render(label) + "\n"
}

// tryPasteImageCmd tries to paste a clipboard image, falling back to files then text.
func (m *AttachModel) tryPasteImageCmd() tea.Cmd {
	return func() tea.Msg {
		if m.imageTempDir == "" {
			dir, err := os.MkdirTemp("", "agentic-attach-images-")
			if err != nil {
				return ImagePasteFailedMsg{}
			}
			m.imageTempDir = dir
		}
		m.imageCounter++
		path, err := saveClipboardImage(m.imageTempDir, m.imageCounter)
		if err == nil {
			return ImagePastedMsg{Path: path}
		}
		// Try file paste
		paths, names, err := saveClipboardFiles(m.imageTempDir)
		if err == nil && len(paths) > 0 {
			return FilesPastedMsg{Paths: paths, Names: names}
		}
		// Try text paste
		text, err := getClipboardText()
		if err == nil && text != "" {
			return TextPastedMsg{Text: text}
		}
		return ImagePasteFailedMsg{}
	}
}

// textPasteFallbackCmd reads text from the clipboard as a fallback.
func (m *AttachModel) textPasteFallbackCmd() tea.Cmd {
	return func() tea.Msg {
		text, err := getClipboardText()
		if err == nil && text != "" {
			return TextPastedMsg{Text: text}
		}
		return nil
	}
}

func formatPermissionDetail(toolName string, input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal(input, &parsed); err != nil {
		return summarizeToolInput(string(input))
	}

	switch toolName {
	case toolNameBash:
		if cmd, ok := parsed["command"].(string); ok {
			return cmd
		}
	case toolNameWrite:
		if fp, ok := parsed["file_path"].(string); ok {
			return fp
		}
	case toolNameEdit:
		if fp, ok := parsed["file_path"].(string); ok {
			return fp
		}
	}
	return summarizeToolInput(string(input))
}

// summarizeToolInput returns a short summary of tool input JSON.
func summarizeToolInput(inputJSON string) string {
	if inputJSON == "" {
		return ""
	}
	s := strings.ReplaceAll(inputJSON, "\n", " ")
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}

// promptMaxLines is the maximum number of visible lines for a [prompt]
// box (orchestrator / sub-agent prompts). Longer content is truncated.
const promptMaxLines = 15

// permDetailMaxLines is the maximum number of visible lines for the
// permission detail text. Longer content is truncated with an ellipsis.
const permDetailMaxLines = 6

// truncatePermDetail truncates the detail string so that when rendered at
// the given width it occupies at most permDetailMaxLines terminal lines.
// It accounts for both raw newlines and long lines that wrap at width.
func truncatePermDetail(detail string, width int) string {
	if detail == "" || width <= 0 {
		return detail
	}
	// Render at width to get visual lines (accounts for wrapping)
	rendered := lipgloss.NewStyle().Width(width).Render(detail)
	visualLines := strings.Split(rendered, "\n")
	if len(visualLines) <= permDetailMaxLines {
		return detail
	}
	// Take first permDetailMaxLines-1 visual lines + "..." indicator
	truncated := strings.Join(visualLines[:permDetailMaxLines-1], "\n")
	return truncated + "\n..."
}

// permDetailLineCount returns how many terminal lines the permission
// detail text will occupy when rendered at the given width, capped at
// permDetailMaxLines.
func permDetailLineCount(detail string, width int) int {
	if detail == "" {
		return 1
	}
	truncated := truncatePermDetail(detail, width)
	return wrappedLineCount(truncated, width)
}

// ── Observability helpers ──

// sessionSpanContext builds a child observability context from the current
// session's feature context.
func (m *AttachModel) sessionSpanContext() (ObservabilityContext, bool) {
	if m.sess == nil {
		return ObservabilityContext{}, false
	}
	featureID := m.sess.FeatureID()
	if featureID == "" {
		return ObservabilityContext{}, false
	}
	return spanContextForFeature(featureID, m.traceID, m.featureName, m.featureSpanID).
		WithRun(m.activeRun).
		Child(), true
}

// emitPermissionRequested emits a permission.requested event for the current pending permission.
func (m *AttachModel) emitPermissionRequested() {
	if m.observer == nil {
		return
	}
	sc, ok := m.sessionSpanContext()
	if !ok {
		return
	}
	m.observer.PermissionRequested(sc, m.sess.ID(), m.sess.PermCacheScope(), m.sess.Iteration(), m.pendingPermToolName, string(m.pendingPermToolInput))
}

// emitQuestionAsked emits a question.asked event.
func (m *AttachModel) emitQuestionAsked(q askUserQuestion) {
	if m.observer == nil {
		return
	}
	sc, ok := m.sessionSpanContext()
	if !ok {
		return
	}
	repoName := m.sess.RepoName()
	if repoName == "" {
		repoName = m.sess.PermCacheScope()
	}
	m.observer.QuestionAsked(sc, m.sess.ID(), repoName, m.sess.Iteration(), q.Question)
}

// emitQuestionAnswered emits a question.answered event.
func (m *AttachModel) emitQuestionAnswered(question string, answer string) {
	if m.observer == nil {
		return
	}
	sc, ok := m.sessionSpanContext()
	if !ok {
		return
	}
	repoName := m.sess.RepoName()
	if repoName == "" {
		repoName = m.sess.PermCacheScope()
	}
	m.observer.QuestionAnswered(sc, m.sess.ID(), repoName, m.sess.Iteration(), question, answer)
}

// emitRestoredObservability emits events for prompts that were already active when
// the attach model was constructed or when a tab switch occurred.
func (m *AttachModel) emitRestoredObservability() {
	if m.showPermMenu {
		m.emitPermissionRequested()
	} else if m.hasActiveQuestion() {
		if m.currentQuestionIdx < len(m.pendingQuestions) {
			m.emitQuestionAsked(m.pendingQuestions[m.currentQuestionIdx])
		}
	}
}
