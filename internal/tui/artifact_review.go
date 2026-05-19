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
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
	"github.com/charmbracelet/x/ansi"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

// isArtifactReviewSession returns true if the session ID belongs to an artifact
// review chat session (suffix "-artifact-review"). These sessions are managed
// entirely by ArtifactReviewModel and must be excluded from generic phase
// lifecycle handlers (handleSDKEvent, handleSessionDone).
func isArtifactReviewSession(sessionID string) bool {
	return strings.HasSuffix(sessionID, "-artifact-review")
}

// ArtifactReviewModel provides a split-panel review UX with a markdown editor
// (top panel) and an optional AI chat panel (bottom panel). Users can directly
// edit artifact files and optionally chat with an AI agent that can also edit
// the file, with green-highlighted changes and batch accept/reject.
type ArtifactReviewModel struct {
	editor          MarkdownEditor
	previewViewport viewport.Model
	documentMode    artifactDocumentMode
	artifactPath    string

	// Review mode
	reviewMode string // "plan" | "rewind" | "gate" | "need_user_input"
	featureID  string
	// repoName is non-empty when this review is repo-scoped — used for
	// need_user_input gates in multi-repo runs (mainline implement and
	// post-publish cycles) so the orchestrator decision message can target
	// the right repo.
	repoName string
	// cycleType is the post-publish cycle this gate belongs to, when the
	// review targets a cycle-scoped need-user-input pause. Empty for
	// feature- or repo-scoped (mainline) gates and for non-need-user-input
	// review modes.
	cycleType   feature.RepoCycleType
	rewindPhase feature.Phase // only for rewind review

	// Working directory for agent session (feature worktree or repo path).
	workDir string

	width, height int

	showMenu   bool
	menuChoice int

	chatViewport viewport.Model
	chatInput    SimpleTextarea
	chatHistory  string
	// partialAgentText holds the accumulated text of the in-flight
	// assistant message (Subtype == "partial"), rendered as plain wrapped
	// text. It is displayed alongside chatHistory but never appended to
	// it — once the message completes, the final (non-partial) text is
	// markdown-rendered and appended to chatHistory, and partialAgentText
	// is cleared.
	partialAgentText string
	chatFocused      bool

	sessionMgr        *session.Manager
	sess              session.SessionView
	sessionID         string
	sessionStarting   bool // true while startSessionCmd is in-flight
	sessionStarted    bool
	sessionGeneration uint64 // incremented on each start attempt; prevents stale binding
	agentResponding   bool
	thinkingLine      string // current tool/thinking status shown in the chat border title

	pendingPermRequestID string
	pendingPermToolName  string
	pendingPermToolInput string

	pendingMessages []string

	pendingAgentEdit bool
	preEditContent   string

	buildSession agent.BuildSessionFunc
	utilityModel string

	// criticApproved is true when the last completed plan attempt was APPROVED
	// by the critic. In that case "Iterate more" is dropped from the menu —
	// nothing would iterate on since the critic is already satisfied.
	criticApproved bool

	detached bool
	decided  bool // true after a menu decision has been emitted

	// nuiForm is the questionnaire body rendered when reviewMode ==
	// reviewModeNeedUserInput. It replaces the editor + chat panels
	// for the need-user-input gate while keeping the same attach /
	// detach / Ctrl+D-menu shell.
	nuiForm *needUserInputForm
}

const (
	artifactReviewHeaderH  = 3  // ASCII art header
	artifactReviewFooterH  = 1  // keybindings hint
	artifactReviewSpacingH = 2  // border top+bottom of editor panel
	chatPanelCollapsedH    = 5  // chat panel height when unfocused
	chatPanelExpandedH     = 12 // chat panel height when focused
	chatSpacingH           = 2  // border top+bottom of chat panel
)

type artifactDocumentMode int

const (
	artifactDocumentRaw artifactDocumentMode = iota
	artifactDocumentPreview
)

// artifactReviewMsgsMsg carries SDK messages from the agent session.
type artifactReviewMsgsMsg struct {
	messages []llm.SDKMessage
}

// artifactReviewDoneMsg indicates the agent session has ended.
type artifactReviewDoneMsg struct{}

// artifactReviewSendErrorMsg indicates a send failure on the session.
type artifactReviewSendErrorMsg struct{ err error }

// artifactReviewStartErrorMsg indicates the session failed to start.
type artifactReviewStartErrorMsg struct {
	err        error
	generation uint64 // matches sessionGeneration at the time of the start attempt
}

// NewArtifactReviewModel creates a new review model and loads the artifact file.
func NewArtifactReviewModel(artifactPath, featureID, reviewMode string, rewindPhase feature.Phase, width, height int, sessionMgr *session.Manager, workDir string, buildSession agent.BuildSessionFunc) ArtifactReviewModel {
	// Fall back to artifact directory if no worktree/repo path was provided.
	if workDir == "" {
		workDir = filepath.Dir(artifactPath)
	}
	m := ArtifactReviewModel{
		artifactPath: artifactPath,
		reviewMode:   reviewMode,
		featureID:    featureID,
		rewindPhase:  rewindPhase,
		workDir:      workDir,
		width:        width,
		height:       height,
		sessionMgr:   sessionMgr,
		sessionID:    featureID + "-artifact-review",
		buildSession: buildSession,
	}

	// Need-user-input mode swaps the editor + chat body for a structured
	// questionnaire. The shell (header / footer / Ctrl+D menu / detach)
	// is identical to review-gate modes so attach reattach behavior
	// matches what the user already knows.
	if reviewMode == reviewModeNeedUserInput {
		m.nuiForm = newNeedUserInputForm(artifactPath, width)
		return m
	}

	m.chatInput = NewSimpleTextarea()
	m.chatInput.Placeholder = "Chat with AI about this document... (Enter to send, Shift+Enter for newline)"
	m.chatInput.SetHeight(3)
	m.chatInput.BackgroundColor = compat.AdaptiveColor{Light: lipgloss.Color("#dce0e8"), Dark: lipgloss.Color("#181825")}

	// Initialize chat viewport. Restrict the viewport key map to arrow and
	// page keys so typing in the chat input doesn't scroll the history via
	// default vim-style bindings (j/k/u/d/f/b/space), matching the AMA
	// dashboard chat pattern.
	m.chatViewport = viewport.New(viewport.WithWidth(max(width-6, 20)), viewport.WithHeight(chatPanelCollapsedH-chatSpacingH))
	m.chatViewport.KeyMap = viewport.KeyMap{
		Up:       key.NewBinding(key.WithKeys("up")),
		Down:     key.NewBinding(key.WithKeys("down")),
		PageUp:   key.NewBinding(key.WithKeys("pgup")),
		PageDown: key.NewBinding(key.WithKeys("pgdown")),
	}
	m.previewViewport = newArtifactPreviewViewport(m.editorContentWidth(), m.editorHeight())

	m.recalcLayout()
	m.editor = NewMarkdownEditor(m.editorContentWidth(), m.editorHeight())
	_ = m.editor.Load(artifactPath)
	if isMarkdownReviewArtifact(artifactPath) {
		m.documentMode = artifactDocumentPreview
		m.refreshPreviewContent()
	}

	// For plan-mode reviews, check whether the critic already approved the last
	// attempt. If so, "Iterate more" is dropped from the menu (it would just
	// short-circuit because the planning loop treats approval as terminal).
	if reviewMode == "plan" {
		switch agent.LastAttemptReviewStatus(filepath.Dir(artifactPath)) {
		case "APPROVED":
			m.criticApproved = true
		}
	}

	return m
}

func newArtifactPreviewViewport(width, height int) viewport.Model {
	vp := viewport.New(viewport.WithWidth(max(width, 1)), viewport.WithHeight(max(height, 1)))
	vp.SoftWrap = true
	vp.FillHeight = true
	vp.KeyMap = viewport.KeyMap{
		Up:       key.NewBinding(key.WithKeys("up", "k")),
		Down:     key.NewBinding(key.WithKeys("down", "j")),
		PageUp:   key.NewBinding(key.WithKeys("pgup")),
		PageDown: key.NewBinding(key.WithKeys("pgdown")),
	}
	return vp
}

func isMarkdownReviewArtifact(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown":
		return true
	default:
		return false
	}
}

func (m ArtifactReviewModel) supportsRenderedPreview() bool {
	return m.nuiForm == nil && isMarkdownReviewArtifact(m.artifactPath)
}

func (m *ArtifactReviewModel) refreshPreviewContent() {
	if !m.supportsRenderedPreview() {
		return
	}
	width := max(m.previewViewport.Width(), m.editorContentWidth())
	m.previewViewport.SetContent(renderMarkdownPreview(m.editor.Content(), width))
}

// Init returns the initial command. Note: this is a value receiver (per
// bubbletea convention), so it cannot mutate m. The caller must focus the
// editor directly via m.editor.Focus() on the stored pointer/value.
func (m ArtifactReviewModel) Init() tea.Cmd {
	return nil
}

// Update handles messages for the artifact review model.
func (m ArtifactReviewModel) Update(msg tea.Msg) (ArtifactReviewModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.nuiForm != nil {
			m.nuiForm.SetWidth(msg.Width)
			return m, nil
		}
		m.recalcLayout()
		m.refreshPreviewContent()
		return m, nil

	case tea.PasteMsg:
		if m.nuiForm != nil {
			cmd := m.nuiForm.Forward(msg)
			return m, cmd
		}
		if m.chatFocused {
			var cmd tea.Cmd
			m.chatInput, cmd = m.chatInput.Update(msg)
			return m, cmd
		}
		if m.documentMode == artifactDocumentPreview {
			return m, nil
		}
		var cmd tea.Cmd
		m.editor, cmd = m.editor.Update(msg)
		return m, cmd

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case artifactReviewMsgsMsg:
		return m.handleSDKMessages(msg.messages)

	case artifactReviewDoneMsg:
		m.agentResponding = false
		m.thinkingLine = ""
		// Reset full session lifecycle so the chat is not stuck in a
		// non-recoverable state. A new session will start lazily on the
		// next Ctrl+S.
		m.sessionStarting = false
		m.sessionStarted = false
		m.sess = nil
		m.pendingMessages = nil
		return m, nil

	case artifactReviewSendErrorMsg:
		m.agentResponding = false
		m.thinkingLine = ""
		m.sessionStarting = false
		m.sessionStarted = false
		m.sess = nil
		m.pendingMessages = nil
		errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
		m.chatHistory += "\n\n" + errStyle.Render("Send failed: "+msg.err.Error()+". Try again with Ctrl+S.")
		m.chatViewport.SetContent(m.chatHistory)
		m.chatViewport.GotoBottom()
		return m, nil

	case artifactReviewStartErrorMsg:
		// Ignore stale start errors from previous generation attempts
		if msg.generation != m.sessionGeneration {
			return m, nil
		}
		m.agentResponding = false
		m.sessionStarting = false
		m.sessionStarted = false
		m.sess = nil
		m.pendingMessages = nil
		errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
		m.chatHistory += "\n\n" + errStyle.Render("Session start failed: "+msg.err.Error()+". Try again with Ctrl+S.")
		m.chatViewport.SetContent(m.chatHistory)
		m.chatViewport.GotoBottom()
		return m, nil

	default:
		// Forward non-key messages to the questionnaire form when in
		// need-user-input mode (cursor blink etc. for textinputs).
		if m.nuiForm != nil {
			cmd := m.nuiForm.Forward(msg)
			return m, cmd
		}
		var cmds []tea.Cmd
		var cmd tea.Cmd
		m.editor, cmd = m.editor.Update(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		if m.chatFocused {
			m.chatInput, cmd = m.chatInput.Update(msg)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		return m, tea.Batch(cmds...)
	}
}

func (m ArtifactReviewModel) handleKey(msg tea.KeyPressMsg) (ArtifactReviewModel, tea.Cmd) {
	if m.showMenu {
		return m.handleMenuKey(msg)
	}

	// Need-user-input mode short-circuits the editor / chat key routing
	// because there is no editor or chat — only the questionnaire form
	// and the same Ctrl+D action menu the artifact-review shell uses.
	if m.nuiForm != nil {
		switch msg.String() {
		case "ctrl+d", "ctrl+]", "esc":
			_ = m.nuiForm.Persist()
			m.showMenu = true
			m.menuChoice = 0
			return m, nil
		}
		cmd := m.nuiForm.HandleKey(msg)
		return m, cmd
	}

	if m.pendingPermRequestID != "" {
		return m.handlePermKey(msg)
	}

	switch msg.String() {
	case "ctrl+p":
		return m.toggleDocumentMode()

	case "ctrl+d":
		if m.editor.Dirty() {
			_ = m.editor.Save()
		}
		m.showMenu = true
		m.menuChoice = 0
		return m, nil

	case "ctrl+]":
		if m.editor.Dirty() {
			_ = m.editor.Save()
		}
		m.showMenu = true
		m.menuChoice = 0
		return m, nil

	case "tab":
		return m.toggleFocus()

	case "ctrl+y":
		if len(m.editor.highlightedLines) > 0 {
			m.editor.ClearHighlights()
			m.preEditContent = ""
			return m, nil
		}

	case "ctrl+z":
		if len(m.editor.highlightedLines) > 0 && m.preEditContent != "" {
			m.editor.SetContent(m.preEditContent, false)
			m.editor.ClearHighlights()
			m.preEditContent = ""
			_ = m.editor.Save()
			return m, nil
		}
	}

	if m.chatFocused {
		return m.handleChatKey(msg)
	}

	if msg.String() == "esc" && (m.documentMode == artifactDocumentPreview || m.editor.Mode() == NormalMode) {
		if m.editor.Dirty() {
			_ = m.editor.Save()
		}
		m.showMenu = true
		m.menuChoice = 0
		return m, nil
	}

	if m.documentMode == artifactDocumentPreview {
		return m.handlePreviewKey(msg)
	}

	var cmd tea.Cmd
	m.editor, cmd = m.editor.Update(msg)
	return m, cmd
}

func (m ArtifactReviewModel) toggleDocumentMode() (ArtifactReviewModel, tea.Cmd) {
	if !m.supportsRenderedPreview() {
		return m, nil
	}
	if m.documentMode == artifactDocumentPreview {
		m.documentMode = artifactDocumentRaw
		return m, nil
	}
	m.documentMode = artifactDocumentPreview
	m.refreshPreviewContent()
	return m, nil
}

func (m ArtifactReviewModel) handlePreviewKey(msg tea.KeyPressMsg) (ArtifactReviewModel, tea.Cmd) {
	m.refreshPreviewContent()
	switch msg.String() {
	case "ctrl+u":
		m.previewViewport.HalfPageUp()
		return m, nil
	case "ctrl+f":
		m.previewViewport.HalfPageDown()
		return m, nil
	}
	var cmd tea.Cmd
	m.previewViewport, cmd = m.previewViewport.Update(msg)
	return m, cmd
}

func (m ArtifactReviewModel) handleChatKey(msg tea.KeyPressMsg) (ArtifactReviewModel, tea.Cmd) {
	switch msg.String() {
	case "enter":
		// Plain Enter sends. Shift+Enter (msg.String() == "shift+enter")
		// falls through to the textarea, which inserts a newline via its
		// KeyEnter branch. Terminals without kitty keyboard protocol
		// cannot distinguish Shift+Enter from Enter — in those terminals
		// multi-line composition isn't possible, matching the AMA chat.
		return m.sendChatMessage()
	case "ctrl+s":
		// Backward-compat alias (also kept for tests).
		return m.sendChatMessage()
	case "esc":
		return m.toggleFocus()
	case "ctrl+u":
		m.chatViewport.HalfPageUp()
		return m, nil
	case "ctrl+f":
		m.chatViewport.HalfPageDown()
		return m, nil
	}

	// Forward to viewport (up/down/pgup/pgdown scroll history) and to chat
	// input (typing, cursor navigation). Mirrors the AMA dashboard chat in
	// chat.go — both receive the key press.
	var cmds []tea.Cmd
	var vpCmd tea.Cmd
	m.chatViewport, vpCmd = m.chatViewport.Update(msg)
	if vpCmd != nil {
		cmds = append(cmds, vpCmd)
	}
	var inputCmd tea.Cmd
	m.chatInput, inputCmd = m.chatInput.Update(msg)
	if inputCmd != nil {
		cmds = append(cmds, inputCmd)
	}
	return m, tea.Batch(cmds...)
}

func (m ArtifactReviewModel) toggleFocus() (ArtifactReviewModel, tea.Cmd) {
	if m.chatFocused {
		m.chatFocused = false
		m.chatInput.Blur()
		cmd := m.editor.Focus()
		m.recalcLayout()
		return m, cmd
	}
	if m.editor.Dirty() {
		_ = m.editor.Save()
	}
	m.chatFocused = true
	m.editor.Blur()
	cmd := m.chatInput.Focus()
	m.recalcLayout()
	return m, cmd
}

func (m ArtifactReviewModel) sendChatMessage() (ArtifactReviewModel, tea.Cmd) {
	text := strings.TrimSpace(m.chatInput.Value())
	if text == "" {
		return m, nil
	}
	m.chatInput.Reset()

	m.chatHistory += "\n\n" + lipgloss.NewStyle().Bold(true).Foreground(colorBrand).Render("You") + "\n" + text
	m.chatViewport.SetContent(m.chatHistory)
	m.chatViewport.GotoBottom()
	m.agentResponding = true

	if m.sessionStarting {
		// A startSessionCmd is already in-flight — queue the message so it
		// will be delivered once the session is established, instead of
		// launching a duplicate session.
		m.pendingMessages = append(m.pendingMessages, text)
		return m, nil
	}

	if !m.sessionStarted {
		m.sessionStarting = true
		m.sessionGeneration++
		return m, m.startSessionCmd(text, m.sessionGeneration)
	}

	if m.sess != nil {
		userMsg := text
		sess := m.sess
		return m, func() tea.Msg {
			if err := sess.SendUserMessage(userMsg); err != nil {
				return artifactReviewSendErrorMsg{err: err}
			}
			return nil
		}
	}
	return m, nil
}

func (m ArtifactReviewModel) startSessionCmd(initialMessage string, generation uint64) tea.Cmd {
	sessionMgr := m.sessionMgr
	sessionID := m.sessionID
	artifactPath := m.artifactPath
	reviewMode := m.reviewMode
	featureID := m.featureID
	workDir := m.workDir
	rewindPhase := m.rewindPhase
	buildSession := m.buildSession
	msg := initialMessage
	gen := generation

	return func() tea.Msg {
		if sessionMgr == nil {
			return artifactReviewStartErrorMsg{err: fmt.Errorf("no session manager"), generation: gen}
		}

		systemPrompt := fmt.Sprintf(
			"You are reviewing a %s artifact file at: %s\n"+
				"The user can directly edit this file in their editor. They may ask you questions about it or request changes.\n"+
				"When you need to modify the artifact, use the Edit tool targeting ONLY the file at the path above.\n"+
				"IMPORTANT: Whenever the user asks you to make a change, update, or edit — ALWAYS assume they are referring to "+
				"the artifact file above, even if they don't explicitly mention it. For example, if the user says "+
				"\"update the version to 2.0\" or \"fix the typo in section 3\", immediately edit the artifact file. "+
				"Do NOT ask for clarification about which file to edit — it is always the artifact.\n"+
				"CRITICAL: You may ONLY edit the artifact file at %s — you MUST NOT edit, write, or modify any other file. "+
				"Any attempt to edit a different file will be denied. If the user asks you to change other files, explain that "+
				"you can only modify the artifact under review and suggest they make those changes themselves.\n"+
				"Focus on being helpful and making precise, targeted edits when requested.",
			reviewMode, artifactPath, artifactPath,
		)

		// Prepend artifact-only edit restriction to the initial user prompt.
		// The permission handler is a secondary defense-in-depth layer.
		msg = fmt.Sprintf(
			"IMPORTANT: You may ONLY use the Edit tool on the artifact file at: %s\n"+
				"You MUST NOT edit, write, or modify any other file under any circumstances.\n"+
				"If asked to change other files, explain that you can only modify the artifact under review.\n\n%s",
			artifactPath, msg,
		)

		var permHandler session.PermissionHandler
		var allowedTools []string
		if reviewMode == "plan" {
			permHandler = &session.PlanReviewHandler{AllowedPath: artifactPath}
			allowedTools = []string{
				"Read", "Glob", "Grep", "LS", "LSP",
				"WebSearch", "WebFetch",
				"TodoWrite", "TaskCreate", "TaskGet", "TaskList", "TaskUpdate",
				"Agent", "AskUserQuestion", "Edit",
			}
		} else {
			permHandler = &session.RewindReviewHandler{AllowedPath: artifactPath}
			allowedTools = []string{
				"Read", "Glob", "Grep", "LS", "LSP",
				"WebSearch", "WebFetch",
				"TodoWrite", "TaskCreate", "TaskGet", "TaskList", "TaskUpdate",
				"Agent", "AskUserQuestion", "Edit",
			}
		}
		model := strings.TrimSpace(m.utilityModel)
		if model == "" {
			return artifactReviewStartErrorMsg{err: fmt.Errorf("artifact review utility model is not configured"), generation: gen}
		}

		additionalDirs := []string{}
		artifactDir := filepath.Dir(artifactPath)
		if artifactDir != workDir {
			additionalDirs = append(additionalDirs, artifactDir)
		}

		cmd, env, sessOpts, err := buildSession(agent.BuildSessionOpts{
			Model:          model,
			Prompt:         msg,
			SystemPrompt:   systemPrompt,
			AllowedTools:   allowedTools,
			WorkDir:        workDir,
			AdditionalDirs: additionalDirs,
			PermHandler:    permHandler,
			TurnMode:       ports.TurnModeInteractive,
		})
		if err != nil {
			return artifactReviewStartErrorMsg{err: fmt.Errorf("build session: %w", err), generation: gen}
		}
		sessOpts.InitialPrompt = msg

		// Use the correct phase metadata: PhasePlan for plan reviews,
		// the actual rewind/gate target phase for rewind/gate reviews.
		phase := feature.PhasePlan
		if reviewMode == "rewind" || reviewMode == "gate" {
			phase = rewindPhase
		}

		sess, err := sessionMgr.StartSession(sessionID, featureID, phase, cmd, workDir, env, sessOpts)
		if err != nil {
			return artifactReviewStartErrorMsg{err: fmt.Errorf("session start: %w", err), generation: gen}
		}

		return artifactReviewSessionStartedMsg{sess: sess, generation: gen}
	}
}

type artifactReviewSessionStartedMsg struct {
	sess       session.SessionView
	generation uint64 // matches sessionGeneration at the time of the start attempt
}

func (m ArtifactReviewModel) handleSessionStarted(sess session.SessionView) (ArtifactReviewModel, tea.Cmd) {
	m.sess = sess
	m.sessionStarting = false
	m.sessionStarted = true

	cmds := []tea.Cmd{
		pollArtifactReviewChCmd(sess),
		waitForArtifactReviewDoneCmd(sess),
	}

	// Drain any messages queued while the session was starting (double-send
	// protection). These were user Ctrl+S presses that arrived between the
	// first startSessionCmd and this callback.
	for _, queued := range m.pendingMessages {
		msg := queued // capture
		cmds = append(cmds, func() tea.Msg {
			_ = sess.SendUserMessage(msg)
			return nil
		})
	}
	m.pendingMessages = nil

	return m, tea.Batch(cmds...)
}

func pollArtifactReviewChCmd(sess session.SessionView) tea.Cmd {
	return func() tea.Msg {
		ch := sess.AttachCh()
		msg, ok := <-ch
		if !ok {
			return artifactReviewDoneMsg{}
		}
		msgs := []llm.SDKMessage{msg}
		for {
			select {
			case m, ok := <-ch:
				if !ok {
					return artifactReviewMsgsMsg{messages: msgs}
				}
				msgs = append(msgs, m)
			default:
				return artifactReviewMsgsMsg{messages: msgs}
			}
		}
	}
}

func waitForArtifactReviewDoneCmd(sess session.SessionView) tea.Cmd {
	return func() tea.Msg {
		<-sess.Done()
		return artifactReviewDoneMsg{}
	}
}

func (m ArtifactReviewModel) handleSDKMessages(msgs []llm.SDKMessage) (ArtifactReviewModel, tea.Cmd) {
	for _, sdkMsg := range msgs {
		switch {
		case sdkMsg.ControlRequest != nil:
			m.pendingPermRequestID = sdkMsg.ControlRequest.RequestID
			m.pendingPermToolName = sdkMsg.ControlRequest.Request.ToolName
			raw, _ := json.Marshal(sdkMsg.ControlRequest.Request.Input)
			m.pendingPermToolInput = string(raw)

		case sdkMsg.Assistant != nil:
			// Concatenate this message's text blocks before deciding how
			// to render them. Codex deltas arrive with Subtype == "partial"
			// and accumulated-so-far text that often contains half-written
			// fenced code blocks; rendering those through the markdown
			// pipeline can produce incomplete output and repeat parser work
			// on every delta.
			var turnText strings.Builder
			hasText := false
			for _, block := range sdkMsg.Assistant.Message.Content {
				if block.IsText() && block.Text != "" {
					if hasText {
						turnText.WriteString("\n")
					}
					turnText.WriteString(block.Text)
					hasText = true
				}
				if block.IsToolUse() {
					m.thinkingLine = fmt.Sprintf("Using %s...", block.Name)
					m.detectAgentEdit(block)
				}
			}
			if hasText {
				m.thinkingLine = ""
				label := "\n\n" + lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")).Render("Agent") + "\n"
				body := turnText.String()
				if sdkMsg.Subtype == "partial" {
					// Keep partial streaming text in its own buffer so the
					// final version can replace it cleanly.
					chatW := max(m.chatViewport.Width(), 20)
					m.partialAgentText = label + lipgloss.NewStyle().Width(chatW).Render(body)
				} else {
					m.partialAgentText = ""
					chatW := max(m.chatViewport.Width(), 20)
					m.chatHistory += label + renderMarkdown(body, chatW)
				}
			}

		case sdkMsg.Result != nil:
			m.agentResponding = false
			m.thinkingLine = ""
			if sdkMsg.Result.IsSuccess() && m.pendingAgentEdit {
				m.reloadAfterAgentEdit()
			}
			if sdkMsg.Result.IsError {
				m.pendingAgentEdit = false
			}

		case sdkMsg.ToolProgress != nil:
			m.thinkingLine = fmt.Sprintf("Using %s...", sdkMsg.ToolProgress.ToolName)
		}
	}

	m.chatViewport.SetContent(m.chatHistory + m.partialAgentText)
	m.chatViewport.GotoBottom()

	if m.sess != nil {
		return m, pollArtifactReviewChCmd(m.sess)
	}
	return m, nil
}

func (m *ArtifactReviewModel) detectAgentEdit(block llm.ContentBlock) {
	if !block.IsToolUse() {
		return
	}
	toolName := block.Name
	if toolName != "Edit" && toolName != "Write" {
		return
	}
	// Check if the tool input references our artifact
	inputStr := string(block.Input)
	if !strings.Contains(inputStr, m.artifactPath) {
		return
	}
	// Save any pending user edits before agent writes
	if m.editor.Dirty() {
		_ = m.editor.Save()
	}
	m.pendingAgentEdit = true
	m.preEditContent = m.editor.Content()
}

func (m *ArtifactReviewModel) reloadAfterAgentEdit() {
	data, err := os.ReadFile(m.artifactPath)
	if err != nil {
		m.pendingAgentEdit = false
		return
	}
	newContent := string(data)
	m.editor.SetContent(newContent, true)
	// Content was just loaded from disk, so it is not unsaved.
	m.editor.MarkClean()
	m.pendingAgentEdit = false
}

func (m ArtifactReviewModel) handlePermKey(msg tea.KeyPressMsg) (ArtifactReviewModel, tea.Cmd) {
	switch msg.String() {
	case "y":
		reqID := m.pendingPermRequestID
		m.pendingPermRequestID = ""
		m.pendingPermToolName = ""
		m.pendingPermToolInput = ""
		if m.sess != nil {
			sess := m.sess
			featureID := sess.FeatureID()
			return m, func() tea.Msg {
				_ = sess.RespondToControl(reqID, true, "")
				sess.ResetWaitingStatus()
				return HelpResolvedMsg{FeatureID: featureID}
			}
		}
	case "n":
		reqID := m.pendingPermRequestID
		m.pendingPermRequestID = ""
		m.pendingPermToolName = ""
		m.pendingPermToolInput = ""
		if m.sess != nil {
			sess := m.sess
			featureID := sess.FeatureID()
			return m, func() tea.Msg {
				_ = sess.RespondToControl(reqID, false, "denied by user")
				sess.ResetWaitingStatus()
				return HelpResolvedMsg{FeatureID: featureID}
			}
		}
	}
	return m, nil
}

func (m ArtifactReviewModel) handleMenuKey(msg tea.KeyPressMsg) (ArtifactReviewModel, tea.Cmd) {
	menuItems := m.menuItems()

	switch msg.String() {
	case "up", "k":
		if m.menuChoice > 0 {
			m.menuChoice--
		}
		return m, nil
	case "down", "j":
		if m.menuChoice < len(menuItems)-1 {
			m.menuChoice++
		}
		return m, nil
	case "enter":
		decision := menuItems[m.menuChoice].decision
		if decision == "detach" {
			m.detached = true
			m.showMenu = false
			return m, nil
		}
		// Need-user-input mode blocks resume until every answer has been
		// filled in. Keep the menu open so the user sees that the choice
		// did not commit.
		if m.nuiForm != nil && decision == "resume" && !m.nuiForm.AllAnswered() {
			return m, nil
		}
		// Persist the latest draft answers before the orchestrator reads
		// them (Resume) or before the gate is closed (Abort).
		if m.nuiForm != nil {
			_ = m.nuiForm.Persist()
		}
		m.detached = true
		m.decided = true
		return m, m.emitDecision(decision)
	case "esc":
		m.showMenu = false
		return m, nil
	}

	return m, nil
}

type menuItem struct {
	label    string
	decision string
}

func (m ArtifactReviewModel) menuItems() []menuItem {
	switch m.reviewMode {
	case "plan":
		items := []menuItem{
			{label: "Iterate more (+3 rounds)", decision: "iterate"},
			{label: "Proceed with current plan", decision: "proceed"},
			{label: "Return to dashboard", decision: "detach"},
		}
		if m.criticApproved {
			// Drop the iterate option when the critic already approved:
			// the planning loop would short-circuit on APPROVED and nothing
			// useful would happen.
			items = items[1:]
		}
		return items
	case "gate":
		return []menuItem{
			{label: "Proceed to next phase", decision: "proceed"},
			{label: "Return to dashboard", decision: "detach"},
		}
	case reviewModeNeedUserInput:
		// Surface the gating reason directly in the label so the user
		// understands why "Resume" did nothing if they pressed enter
		// before filling answers.
		resumeLabel := "Resume implementation"
		if m.nuiForm != nil && !m.nuiForm.AllAnswered() {
			resumeLabel += " (answer all questions to enable)"
		}
		return []menuItem{
			{label: resumeLabel, decision: "resume"},
			{label: "Abort", decision: "abort"},
			{label: "Return to dashboard", decision: "detach"},
		}
	default: // "rewind"
		return []menuItem{
			{label: "Proceed with rewind", decision: "proceed"},
			{label: "Return to dashboard", decision: "detach"},
		}
	}
}

func (m ArtifactReviewModel) emitDecision(decision string) tea.Cmd {
	switch m.reviewMode {
	case "plan":
		fid := m.featureID
		return func() tea.Msg {
			return PlanReviewDecisionMsg{FeatureID: fid, Decision: decision}
		}
	case "gate":
		fid := m.featureID
		phase := m.rewindPhase // reuse this field for the target phase
		return func() tea.Msg {
			return GateReviewDecisionMsg{FeatureID: fid, Phase: phase, Decision: decision}
		}
	case reviewModeNeedUserInput:
		fid := m.featureID
		repo := m.repoName
		cycle := m.cycleType
		return func() tea.Msg {
			return NeedUserInputDecisionMsg{FeatureID: fid, RepoName: repo, CycleType: cycle, Decision: decision}
		}
	default: // "rewind"
		fid := m.featureID
		phase := m.rewindPhase
		return func() tea.Msg {
			return RewindReviewDecisionMsg{FeatureID: fid, Phase: phase, Decision: decision}
		}
	}
}

// Detached returns true if the user chose to detach.
func (m ArtifactReviewModel) Detached() bool {
	return m.detached
}

// FeatureID returns the feature ID for this review.
func (m ArtifactReviewModel) FeatureID() string {
	return m.featureID
}

// ReviewMode returns the review mode ("plan" or "rewind").
func (m ArtifactReviewModel) ReviewMode() string {
	return m.reviewMode
}

// RepoName returns the repo identifier for repo-scoped reviews. Empty for
// feature-scoped reviews and non-need-user-input modes.
func (m ArtifactReviewModel) RepoName() string {
	return m.repoName
}

// SetRepoName attaches a repo identifier to this review. Used by the TUI
// attach handler to route repo-scoped need-user-input gates so the
// emitted NeedUserInputDecisionMsg carries the correct repo target.
func (m ArtifactReviewModel) SetRepoName(repoName string) ArtifactReviewModel {
	m.repoName = repoName
	return m
}

// CycleType returns the cycle this need-user-input review targets. Empty
// for feature/repo-scoped reviews and non-need-user-input modes.
func (m ArtifactReviewModel) CycleType() feature.RepoCycleType {
	return m.cycleType
}

// SetCycleType attaches a cycle identifier to this review. Used by the
// TUI attach handler so the emitted NeedUserInputDecisionMsg routes
// through the cycle-scoped resume/abort path.
func (m ArtifactReviewModel) SetCycleType(cycleType feature.RepoCycleType) ArtifactReviewModel {
	m.cycleType = cycleType
	return m
}

// Decided returns true if the user has already made a menu decision (proceed/iterate).
// A decided review must not be reattached.
func (m ArtifactReviewModel) Decided() bool {
	return m.decided
}

// Reattach resets the detached state so the model can be re-shown.
func (m *ArtifactReviewModel) Reattach() tea.Cmd {
	m.detached = false
	if m.nuiForm != nil {
		m.showMenu = false
		return m.nuiForm.Focus()
	}
	m.showMenu = false
	m.chatFocused = false
	m.chatInput.Blur()
	m.recalcLayout()
	return m.editor.Focus()
}

// WithDecisionError records an error returned by the orchestrator's
// HandleNeedUserInputDecision call so the user can see what went wrong
// and retry / abort from the same questionnaire. Only meaningful when
// reviewMode == reviewModeNeedUserInput; for other review modes it is
// a no-op so callers can route uniformly.
func (m ArtifactReviewModel) WithDecisionError(err error) ArtifactReviewModel {
	if m.nuiForm == nil {
		return m
	}
	m.nuiForm.decisionErr = err
	m.detached = false
	m.decided = false
	m.showMenu = false
	_ = m.nuiForm.Focus()
	return m
}

// DecisionError returns the last orchestrator decision error surfaced
// on the questionnaire, if any. Returns nil for review modes that do
// not use this surface.
func (m ArtifactReviewModel) DecisionError() error {
	if m.nuiForm == nil {
		return nil
	}
	return m.nuiForm.decisionErr
}

// AllAnswered reports whether every question in the need-user-input
// gate has a non-empty answer. Returns false for non-questionnaire
// review modes so callers can guard uniformly.
func (m ArtifactReviewModel) AllAnswered() bool {
	if m.nuiForm == nil {
		return false
	}
	return m.nuiForm.AllAnswered()
}

// GatePath returns the gate artifact path (== artifactPath) when in
// need-user-input mode, otherwise empty.
func (m ArtifactReviewModel) GatePath() string {
	if m.nuiForm == nil {
		return ""
	}
	return m.nuiForm.gatePath
}

// SetAnswer is a test seam that fills the i-th questionnaire input.
// Returns the model unchanged for non-questionnaire modes.
func (m ArtifactReviewModel) SetAnswer(i int, answer string) ArtifactReviewModel {
	if m.nuiForm == nil {
		return m
	}
	m.nuiForm.SetAnswer(i, answer)
	return m
}

// MenuOpen reports whether the Ctrl+D action menu overlay is active.
func (m ArtifactReviewModel) MenuOpen() bool { return m.showMenu }

// MenuItemLabels returns the menu's labels in render order. Used by
// tests to verify the resume label gates on AllAnswered in need-user-
// input mode.
func (m ArtifactReviewModel) MenuItemLabels() []string {
	items := m.menuItems()
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.label)
	}
	return out
}

// StopSession stops the lazy chat session if one was started (or is still
// starting). Call this for terminal review decisions, explicit cancellation,
// or app shutdown. Plain detach keeps the session alive for reattach.
func (m *ArtifactReviewModel) StopSession() {
	if !m.sessionStarted && !m.sessionStarting {
		return
	}
	if m.sessionMgr != nil {
		_ = m.sessionMgr.StopSession(m.sessionID)
	}
	m.sess = nil
	m.sessionStarted = false
	m.sessionStarting = false
	// Clear transient chat/session state so stale prompts and permission
	// data from a previous attempt cannot leak into a future session.
	m.pendingMessages = nil
	m.pendingPermRequestID = ""
	m.pendingPermToolName = ""
	m.pendingPermToolInput = ""
}

func (m ArtifactReviewModel) chatHeight() int {
	if !m.chatFocused {
		return chatPanelCollapsedH
	}
	// Target ~30% of the screen for the focused chat panel so users can
	// see more of the conversation. Floor at chatPanelExpandedH so small
	// terminals still get a usable chat, and cap so the editor retains at
	// least a minimum number of visible rows.
	h := max(m.height*30/100, chatPanelExpandedH)
	const minEditorH = 8
	maxChat := max(m.height-artifactReviewHeaderH-artifactReviewFooterH-artifactReviewSpacingH-chatSpacingH-minEditorH, chatPanelCollapsedH)
	if h > maxChat {
		h = maxChat
	}
	return h
}

func (m ArtifactReviewModel) editorHeight() int {
	return max(m.height-artifactReviewHeaderH-artifactReviewFooterH-artifactReviewSpacingH-m.chatHeight()-chatSpacingH, 4)
}

func (m ArtifactReviewModel) editorContentWidth() int {
	panelW := max(m.width-2, 1)
	return max(panelW-4, 1)
}

func (m *ArtifactReviewModel) recalcLayout() {
	contentW := m.editorContentWidth()
	editorH := m.editorHeight()
	m.editor.SetSize(contentW, editorH)
	m.previewViewport.SetWidth(contentW)
	m.previewViewport.SetHeight(editorH)

	chatContentW := max(m.width-6, 1)
	chatContentH := max(m.chatHeight()-chatSpacingH, 1)
	m.chatViewport.SetWidth(chatContentW)
	m.chatViewport.SetHeight(max(chatContentH-3, 1)) // Reserve space for input
	m.chatInput.SetWidth(chatContentW)
}

// View renders the artifact review model.
func (m ArtifactReviewModel) View() string {
	if m.nuiForm != nil {
		return m.renderNeedUserInputView()
	}
	var sb strings.Builder

	sb.WriteString(m.renderHeader())

	panelW := max(m.width-2, 1)

	editorView := m.renderDocumentContent()
	editorStyle := panelStyle(!m.chatFocused).Width(panelW)
	editorPanel := editorStyle.Render(editorView)

	title := m.documentTitleForWidth(panelW)
	titleStyle := lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	editorPanel = renderBorderTitle(editorPanel, title, titleStyle)

	sb.WriteString(editorPanel)
	sb.WriteString("\n")

	chatContent := m.renderChatContent()
	chatStyle := panelStyle(m.chatFocused).Width(panelW)
	chatPanel := chatStyle.Render(chatContent)

	chatTitle := " Chat "
	if m.agentResponding {
		status := "thinking..."
		if m.thinkingLine != "" {
			status = m.thinkingLine
		}
		// Truncate to fit within the panel width (leave room for border + padding)
		maxStatusLen := max(panelW-16, 10)
		if len(status) > maxStatusLen {
			status = status[:maxStatusLen-3] + "..."
		}
		chatTitle = " Chat [" + status + "] "
	}
	if m.pendingPermRequestID != "" {
		chatTitle = " Chat [permission needed] "
	}
	chatTitleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	chatPanel = renderBorderTitle(chatPanel, chatTitle, chatTitleStyle)

	sb.WriteString(chatPanel)
	sb.WriteString("\n")

	sb.WriteString(m.renderFooter())

	if m.showMenu {
		content := sb.String()
		return m.renderMenuOverlay(content)
	}

	return sb.String()
}

func (m ArtifactReviewModel) renderDocumentContent() string {
	if m.documentMode != artifactDocumentPreview {
		return m.editor.View()
	}
	m.refreshPreviewContent()
	return m.previewViewport.View()
}

func (m ArtifactReviewModel) documentTitle() string {
	filename := filepath.Base(m.artifactPath)
	if m.documentMode == artifactDocumentPreview {
		title := " " + filename + " [Rendered preview] "
		if m.editor.Dirty() {
			title = " " + filename + " [Rendered preview] [+] "
		}
		if len(m.editor.highlightedLines) > 0 {
			title += "[agent edits pending] "
		}
		return title
	}

	modeStr := "NORMAL"
	if m.editor.Mode() == InsertMode {
		modeStr = "INSERT"
	}
	title := " " + filename + " [Source " + modeStr + "] "
	if m.editor.Dirty() {
		title = " " + filename + " [Source " + modeStr + "] [+] "
	}
	if len(m.editor.highlightedLines) > 0 {
		title += "[agent edits pending] "
	}
	return title
}

func (m ArtifactReviewModel) documentTitleForWidth(width int) string {
	title := m.documentTitle()
	if lipgloss.Width(title) <= max(width-5, 1) {
		return title
	}

	filename := filepath.Base(m.artifactPath)
	if m.documentMode == artifactDocumentPreview {
		status := "Preview"
		if m.editor.Dirty() {
			status += " +"
		}
		title = " " + filename + " [" + status + "] "
		if len(m.editor.highlightedLines) > 0 {
			title += "[edits] "
		}
		return title
	}

	modeStr := "NORM"
	if m.editor.Mode() == InsertMode {
		modeStr = "INS"
	}
	status := "Src " + modeStr
	if m.editor.Dirty() {
		status += " +"
	}
	title = " " + filename + " [" + status + "] "
	if len(m.editor.highlightedLines) > 0 {
		title += "[edits] "
	}
	return title
}

func (m ArtifactReviewModel) renderChatContent() string {
	var sb strings.Builder

	// Chat history viewport — chatHistory holds completed turns, and
	// partialAgentText holds the in-flight streaming assistant turn. They
	// are concatenated here so the user sees the live stream without its
	// text being permanently committed to history until the turn finishes.
	viewportContent := m.chatHistory + m.partialAgentText
	if viewportContent == "" {
		viewportContent = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("No messages yet. Press Tab to focus chat, type your message, then Enter to send.")
	}
	m.chatViewport.SetContent(viewportContent)
	sb.WriteString(m.chatViewport.View())

	// Separator line between history and input
	chatContentW := max(m.chatViewport.Width(), 1)
	sepStyle := lipgloss.NewStyle().Foreground(colorSurface)
	sb.WriteString("\n")
	sb.WriteString(sepStyle.Render(strings.Repeat("─", chatContentW)))
	sb.WriteString("\n")

	// Permission prompt or input — rendered with a subtle background
	inputBg := compat.AdaptiveColor{Light: lipgloss.Color("#dce0e8"), Dark: lipgloss.Color("#181825")}
	inputBgStyle := lipgloss.NewStyle().Background(inputBg)
	if m.pendingPermRequestID != "" {
		permStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("208")).Background(inputBg)
		line := permStyle.Render(fmt.Sprintf("Allow %s? [y/n]", m.pendingPermToolName))
		padW := chatContentW - lipgloss.Width(line)
		if padW > 0 {
			line += inputBgStyle.Render(strings.Repeat(" ", padW))
		}
		sb.WriteString(line)
	} else if m.chatFocused {
		// Background is applied natively by SimpleTextarea.BackgroundColor
		// to avoid conflicting with cursor blink ANSI sequences.
		sb.WriteString(m.chatInput.View())
	} else {
		dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Background(inputBg)
		line := dimStyle.Render("Tab to chat...")
		padW := chatContentW - lipgloss.Width(line)
		if padW > 0 {
			line += inputBgStyle.Render(strings.Repeat(" ", padW))
		}
		sb.WriteString(line)
	}

	return sb.String()
}

func (m ArtifactReviewModel) renderHeader() string {
	width := max(m.width, 1)
	artLines := []string{
		" \u2584\u2580\u2588 \u2588\u2580\u2580 \u2588\u2580\u2580 \u2588\u2584\u2591\u2588 \u2580\u2588\u2580 \u2588 \u2588\u2580\u2580",
		" \u2588\u2580\u2588 \u2588\u2584\u2588 \u2588\u2588\u2584 \u2588\u2591\u2580\u2588 \u2591\u2588\u2591 \u2588 \u2588\u2584\u2584",
	}
	brandStyle := lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(colorSurface)

	var header strings.Builder
	for _, line := range artLines {
		header.WriteString(brandStyle.Render(ansi.Truncate(line, width, "…")))
		header.WriteByte('\n')
	}
	header.WriteString(dimStyle.Render(strings.Repeat("\u2500", width)))
	header.WriteByte('\n')
	return header.String()
}

func (m ArtifactReviewModel) renderFooter() string {
	hintStyle := lipgloss.NewStyle().Foreground(colorSubtext)

	if m.pendingPermRequestID != "" {
		return hintStyle.Render("y: allow │ n: deny")
	}

	var hints []string
	if m.chatFocused {
		target := "editor"
		if m.documentMode == artifactDocumentPreview {
			target = "preview"
		}
		hints = append(hints, "Tab: "+target, "↑/↓: scroll", "Enter: send", "Shift+Enter: newline", "Ctrl+D: done")
	} else {
		if m.documentMode == artifactDocumentPreview {
			hints = append(hints, "Preview", "Ctrl+U/F: scroll", "Ctrl+P: raw")
		} else {
			hints = append(hints, "Raw")
			if m.supportsRenderedPreview() {
				hints = append(hints, "Ctrl+P: preview")
			}
			if m.editor.Mode() == InsertMode {
				hints = append(hints, "Esc: normal mode")
			} else {
				hints = append(hints, "i: insert")
			}
			hints = append(hints, "Ctrl+U/F: scroll")
		}
		hints = append(hints, "Tab: chat", "Ctrl+D: done", "Esc: back")
	}
	if m.editor.Dirty() {
		hints = append(hints, "Unsaved")
	}
	if len(m.editor.highlightedLines) > 0 {
		hints = append(hints, "Ctrl+Y: accept edits", "Ctrl+Z: reject edits")
	}

	width := max(m.width, 1)
	footer := strings.Join(hints, " │ ")
	if lipgloss.Width(footer) > width {
		footer = strings.Join(m.compactFooterHints(), " ")
	}
	return hintStyle.Render(ansi.Truncate(footer, width, "…"))
}

func (m ArtifactReviewModel) compactFooterHints() []string {
	if len(m.editor.highlightedLines) > 0 {
		hints := []string{"Preview"}
		if m.documentMode != artifactDocumentPreview {
			hints = []string{"Raw"}
		}
		if m.editor.Dirty() {
			hints = append(hints, "Unsaved")
		}
		hints = append(hints, "Ctrl+Y: accept edits", "Ctrl+Z: reject edits")
		if m.supportsRenderedPreview() {
			if m.documentMode == artifactDocumentPreview {
				hints = append(hints, "Ctrl+P:raw")
			} else {
				hints = append(hints, "Ctrl+P:preview")
			}
		}
		return hints
	}

	var hints []string
	if m.chatFocused {
		target := "editor"
		if m.documentMode == artifactDocumentPreview {
			target = "preview"
		}
		hints = append(hints, "Tab:"+target, "Enter:send", "Ctrl+D")
	} else if m.documentMode == artifactDocumentPreview {
		hints = append(hints, "Preview", "Ctrl+P:raw", "Tab:chat", "Ctrl+D")
	} else {
		hints = append(hints, "Raw")
		if m.supportsRenderedPreview() {
			hints = append(hints, "Ctrl+P:preview")
		}
		if m.editor.Mode() == InsertMode {
			hints = append(hints, "Esc")
		} else {
			hints = append(hints, "i")
		}
		hints = append(hints, "Tab:chat", "Ctrl+D")
	}
	if m.editor.Dirty() {
		hints = append(hints, "Unsaved")
	}
	return hints
}

func (m ArtifactReviewModel) renderMenuOverlay(bg string) string {
	items := m.menuItems()
	var menuLines []string
	menuWidth := min(max(m.width-2, 1), 48)
	menuContentWidth := max(menuWidth-6, 1)

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorBrand)
	menuLines = append(menuLines, titleStyle.Render(ansi.Truncate("Choose an action:", menuContentWidth, "…")))
	menuLines = append(menuLines, "")

	for i, item := range items {
		prefix := "  "
		style := lipgloss.NewStyle()
		if i == m.menuChoice {
			prefix = "▸ "
			style = style.Bold(true).Foreground(colorBrand)
		}
		// Visually dim Resume in need-user-input mode while answers are
		// still incomplete so the gating reason is obvious to the user.
		if m.nuiForm != nil && item.decision == "resume" && !m.nuiForm.AllAnswered() {
			style = style.Foreground(lipgloss.Color("240"))
		}
		menuLines = append(menuLines, style.Render(ansi.Truncate(prefix+item.label, menuContentWidth, "…")))
	}

	menuLines = append(menuLines, "")
	dimStyle := lipgloss.NewStyle().Foreground(colorSurface)
	menuLines = append(menuLines, dimStyle.Render(ansi.Truncate("Enter: select │ Esc: cancel", menuContentWidth, "…")))

	menuContent := strings.Join(menuLines, "\n")

	menuBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorBrand).
		Padding(1, 2).
		Width(menuWidth).
		Render(menuContent)

	return overlayModal(bg, menuBox, m.width, m.height)
}

// renderNeedUserInputView renders the questionnaire-mode body wrapped
// in the same artifact-review header / footer / menu chrome the user
// already knows from review gates.
func (m ArtifactReviewModel) renderNeedUserInputView() string {
	var sb strings.Builder
	sb.WriteString(m.renderHeader())

	panelW := max(m.width-2, 1)
	body := m.nuiForm.View()
	box := panelStyle(true).Width(panelW).Render(body)
	titleStyle := lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	box = renderBorderTitle(box, " Need User Input ", titleStyle)
	sb.WriteString(box)
	sb.WriteString("\n")

	hintStyle := lipgloss.NewStyle().Foreground(colorSubtext)
	sb.WriteString(hintStyle.Render("Tab/Shift+Tab: navigate │ Ctrl+D: actions menu │ Esc: actions menu"))

	if m.showMenu {
		return m.renderMenuOverlay(sb.String())
	}
	return sb.String()
}
