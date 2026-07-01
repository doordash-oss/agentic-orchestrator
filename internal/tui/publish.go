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
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

type publishStep int

const (
	publishStepRepoSelect publishStep = iota // only shown for multi-repo features
	publishStepDiff
	publishStepCommits
	publishStepPRDesc
	publishStepConfirm
	publishStepExecute
	publishStepDone
)

// publishExecuteResultMsg carries the result of a publish execution.
type publishExecuteResultMsg struct {
	prURL            string
	err              error
	conflictDetected bool   // true if pull-rebase encountered merge conflicts
	branch           string // branch name, set when conflictDetected is true
	rebaseTarget     string // base branch for follow-up rebase conflict resolution
	featureID        string // feature ID, set when conflictDetected is true
	repoName         string // for multi-repo per-repo state tracking
}

// publishRepoEntry represents a repo in the publish repo selector.
type publishRepoEntry struct {
	Name        string
	Branch      string
	WorktreeDir string
	RepoPath    string
	BaseBranch  string
	PRStatus    string // "published", "pending", "failed"
	PRURL       string // set if already published
}

// publishDescGeneratedMsg carries the generated PR description along with any
// underlying generation error. Callers use `err` for observability (logging);
// `title` and `body` are always populated (deterministic fallback fills any
// gaps), so the UI need not special-case the failure path.
type publishDescGeneratedMsg struct {
	title     string
	body      string
	err       error
	featureID string // passed through for logPhaseError
}

type publishDescriptionRunner func(context.Context, string, agent.PRContext) (string, string, error)

type PublishModel struct {
	step        publishStep
	featureID   string
	diff        string
	commitLog   string
	planText    string
	prTitle     string
	prBody      string
	prURL       string
	errMsg      string
	worktreeDir string
	branch      string
	repoPath    string
	descModel   string          // model name for description generation
	baseBranch  string          // for stacked PRs: target branch instead of default
	prCtx       agent.PRContext // lean context for PR description generation; filled by caller
	runDesc     publishDescriptionRunner
	spinnerView string // set by parent from app-level spinner
	viewport    reviewViewportModel
	titleInput  textinput.Model
	bodyInput   textarea.Model
	editingBody bool // true when body textarea is focused
	generating  bool
	width       int
	height      int

	// Multi-repo fields
	repos           []publishRepoEntry  // all repos with their state, for selector
	selectedRepo    int                 // cursor index in repo selector
	hasRepoSelect   bool                // true when >1 repo available for publish
	repoName        string              // name of the selected repo (for cross-ref tracking)
	featureName     string              // feature name for cross-ref section
	crossRefEntries []git.CrossRefEntry // built from repos + RepoStates
	existingPRURL   string              // set when re-publishing an already-published repo
	publishable     bool                // true if all feature repos have origin remote
	draft           bool                // true when the feature's checkpoints request a draft PR
	leasePush       bool                // true when manual CodeReady publish should update origin with --force-with-lease
}

// newPublishViewport builds the shared viewport, title input, and body
// textarea used by every publish model. The trio's dimensions and behavior
// are identical for single- and multi-repo publish flows; only the gating
// of the repo-select step (driven by len(f.Repos) > 1) differs across N.
func newPublishViewport(width, height int, prTitle, prBody, diff string) (reviewViewportModel, textinput.Model, textarea.Model) {
	vp := newReviewViewportModel(width, height, colorizeDiff(diff))
	ti := textinput.New()
	ti.Placeholder = "PR title"
	ti.CharLimit = 120
	if prTitle != "" {
		ti.SetValue(prTitle)
	}

	ta := newStyledTextarea()
	ta.Placeholder = "PR body (markdown)"
	ta.SetWidth(max(width-8, 40))
	ta.SetHeight(max(height-14, 8))
	if prBody != "" {
		ta.SetValue(prBody)
	}
	return vp, ti, ta
}

// NewPublishModel creates the unified publish model. The repo-select step is
// rendered only when the feature has more than one repo (len(f.Repos) > 1).
//
// When len(f.Repos) > 1, the model starts at publishStepRepoSelect with
// totalSteps=7. Otherwise, when repos has at least 1 entry, the model
// auto-selects repos[0] and starts at publishStepDiff with totalSteps=6 —
// preserving today's single-repo publish UX byte-for-byte. With zero repos,
// the model falls through with publishStepDiff as the default (degenerate
// test fixtures).
func NewPublishModel(f *feature.Feature, repos []publishRepoEntry, planText, descModel string, width, height int) PublishModel {
	vp, ti, ta := newPublishViewport(width, height, "", "", "")

	var featureID, featureName string
	if f != nil {
		featureID = f.ID
		featureName = f.Name
	}

	var draft bool
	var leasePush bool
	if f != nil {
		draft = f.Checkpoints.DraftPublish
		leasePush = f.Status == feature.StatusCodeReady && !f.Checkpoints.AutoPublish()
	}

	m := PublishModel{
		step:        publishStepDiff,
		featureID:   featureID,
		featureName: featureName,
		repos:       repos,
		planText:    planText,
		descModel:   descModel,
		viewport:    vp,
		titleInput:  ti,
		bodyInput:   ta,
		width:       width,
		height:      height,
		publishable: true,
		draft:       draft,
		leasePush:   leasePush,
	}

	if f != nil && len(f.Repos) > 1 {
		m.hasRepoSelect = true
		m.step = publishStepRepoSelect
	} else if len(repos) >= 1 {
		// Auto-select first repo (N=1 or degenerate caller). The
		// len(repos) >= 1 check is data plumbing for the auto-select.
		r := repos[0]
		m.worktreeDir = r.WorktreeDir
		m.branch = r.Branch
		m.repoPath = r.RepoPath
		m.repoName = r.Name
		m.baseBranch = r.BaseBranch
		m.crossRefEntries = buildCrossRefEntriesFromPublishRepos(repos, featureName)
		m.diff, m.commitLog = loadPublishRepoContext(r.WorktreeDir, r.BaseBranch)
		m.viewport.SetContent(colorizeDiff(m.diff))
		m.step = publishStepDiff
	}

	return m
}

func loadPublishRepoContext(worktreeDir, baseBranch string) (string, string) {
	if worktreeDir == "" {
		return "", ""
	}
	if info, err := os.Stat(worktreeDir); err != nil || !info.IsDir() {
		return "", ""
	}
	if _, err := os.Stat(filepath.Join(worktreeDir, ".git")); err != nil {
		return "", ""
	}
	diff, _ := git.DiffSummary(worktreeDir, baseBranch)
	commitLog, _ := git.CommitLog(worktreeDir, baseBranch)
	return diff, commitLog
}

// buildCrossRefEntriesFromPublishRepos converts publish repo entries to cross-ref entries.
func buildCrossRefEntriesFromPublishRepos(repos []publishRepoEntry, _ string) []git.CrossRefEntry {
	entries := make([]git.CrossRefEntry, 0, len(repos))
	for _, r := range repos {
		entry := git.CrossRefEntry{
			RepoName: r.Name,
			Branch:   r.Branch,
		}
		switch r.PRStatus {
		case "published":
			entry.PRURL = r.PRURL
		case "failed":
			entry.PRURL = "(failed)"
		}
		entries = append(entries, entry)
	}
	return entries
}

func (m PublishModel) Init() tea.Cmd {
	return nil
}

func (m PublishModel) Update(msg tea.Msg) (PublishModel, tea.Cmd) {
	switch msg := msg.(type) {
	case publishExecuteResultMsg:
		if msg.err != nil {
			m.errMsg = msg.err.Error()
		} else {
			m.prURL = msg.prURL
		}
		m.step = publishStepDone
		return m, nil

	case publishDescGeneratedMsg:
		m.generating = false
		m.prTitle = msg.title
		m.prBody = msg.body
		m.titleInput.SetValue(msg.title)
		m.titleInput.Focus()
		m.bodyInput.SetValue(msg.body)
		m.editingBody = false
		return m, textinput.Blink

	case tea.KeyPressMsg:
		if m.step == publishStepExecute {
			return m, nil
		}

		// Repo selector navigation
		if m.step == publishStepRepoSelect {
			switch {
			case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
				m.step = publishStepDone
				return m, nil
			case key.Matches(msg, key.NewBinding(key.WithKeys("up", "k"))):
				if m.selectedRepo > 0 {
					m.selectedRepo--
				}
				return m, nil
			case key.Matches(msg, key.NewBinding(key.WithKeys("down", "j"))):
				if m.selectedRepo < len(m.repos)-1 {
					m.selectedRepo++
				}
				return m, nil
			case key.Matches(msg, keys.Enter):
				return m.advanceStep()
			}
			return m, nil
		}

		if m.step == publishStepConfirm && key.Matches(msg, key.NewBinding(key.WithKeys("d"))) {
			m.draft = !m.draft
			return m, nil
		}

		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
			m.step = publishStepDone
			return m, nil
		case key.Matches(msg, key.NewBinding(key.WithKeys("tab"))):
			if m.step == publishStepPRDesc {
				// Toggle focus between title and body
				m.editingBody = !m.editingBody
				if m.editingBody {
					m.titleInput.Blur()
					m.bodyInput.Focus()
					return m, textarea.Blink
				}
				m.bodyInput.Blur()
				m.titleInput.Focus()
				return m, textinput.Blink
			}
		case key.Matches(msg, keys.Enter):
			if m.step == publishStepPRDesc && !m.editingBody {
				// Capture edited values before advancing
				m.prTitle = m.titleInput.Value()
				m.prBody = m.bodyInput.Value()
				return m.advanceStep()
			}
			if m.step != publishStepPRDesc {
				return m.advanceStep()
			}
		}

		if m.step == publishStepDiff || m.step == publishStepCommits {
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
		if m.step == publishStepPRDesc {
			if m.editingBody {
				var cmd tea.Cmd
				m.bodyInput, cmd = m.bodyInput.Update(msg)
				return m, cmd
			}
			var cmd tea.Cmd
			m.titleInput, cmd = m.titleInput.Update(msg)
			return m, cmd
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.viewport.Resize(msg.Width, msg.Height)
		m.bodyInput.SetWidth(max(msg.Width-8, 40))
		m.bodyInput.SetHeight(max(msg.Height-14, 8))
	}
	return m, nil
}

func (m PublishModel) advanceStep() (PublishModel, tea.Cmd) {
	switch m.step {
	case publishStepRepoSelect:
		if len(m.repos) > 0 {
			selected := m.repos[m.selectedRepo]
			m.worktreeDir = selected.WorktreeDir
			m.branch = selected.Branch
			m.repoPath = selected.RepoPath
			m.repoName = selected.Name
			m.baseBranch = selected.BaseBranch
			m.crossRefEntries = buildCrossRefEntriesFromPublishRepos(m.repos, m.featureName)
			// Track existing PR URL for re-publish flow
			if selected.PRStatus == "published" && selected.PRURL != "" {
				m.existingPRURL = selected.PRURL
			} else {
				m.existingPRURL = ""
			}
			// Load diff from selected repo
			m.diff, m.commitLog = loadPublishRepoContext(selected.WorktreeDir, selected.BaseBranch)
			m.viewport.SetContent(colorizeDiff(m.diff))
		}
		m.step = publishStepDiff
	case publishStepDiff:
		m.step = publishStepCommits
		m.viewport.SetContent(m.commitLog)
		m.viewport.GotoTop()
	case publishStepCommits:
		m.step = publishStepPRDesc
		// Generate PR description if not already provided
		if m.prTitle == "" && m.prBody == "" {
			m.generating = true
			return m, m.generateDescription()
		}
		// If already have title/body, set up editing
		m.titleInput.SetValue(m.prTitle)
		m.titleInput.Focus()
		m.bodyInput.SetValue(m.prBody)
		m.editingBody = false
		return m, textinput.Blink
	case publishStepPRDesc:
		m.prTitle = m.titleInput.Value()
		m.prBody = m.bodyInput.Value()
		m.step = publishStepConfirm
	case publishStepConfirm:
		m.step = publishStepExecute
		return m, m.executePublish()
	case publishStepExecute:
		m.step = publishStepDone
	}
	return m, nil
}

// generateDescription runs the claude CLI to generate a PR description using
// the model's current PRContext. If the caller did not populate m.prCtx, the
// roadmap is synthesized from m.planText so older callers still benefit from
// the lean-prompt path. Errors are surfaced via the returned message (never
// silently swallowed) so the app-level handler can log them.
func (m PublishModel) generateDescription() tea.Cmd {
	prCtx := m.prCtx
	if prCtx.Roadmap == "" && m.planText != "" {
		prCtx.Roadmap = m.planText
	}
	model := m.descModel
	featureID := m.featureID

	return func() tea.Msg {
		if model == "" {
			model = "sonnet"
		}
		if m.runDesc == nil {
			title, body := agent.BuildPRDescriptionFallback(prCtx)
			return publishDescGeneratedMsg{
				title:     title,
				body:      body,
				err:       fmt.Errorf("description generator not configured"),
				featureID: featureID,
			}
		}
		title, body, err := m.runDesc(context.Background(), model, prCtx)
		return publishDescGeneratedMsg{title: title, body: body, err: err, featureID: featureID}
	}
}

// executePublish performs the actual git push and PR creation.
func (m PublishModel) executePublish() tea.Cmd {
	worktreeDir := m.worktreeDir
	branch := m.branch
	repoPath := m.repoPath
	baseBranch := m.baseBranch
	prTitle := m.prTitle
	prBody := m.prBody
	repoName := m.repoName
	featureName := m.featureName
	crossRefEntries := m.crossRefEntries
	existingPRURL := m.existingPRURL
	publishable := m.publishable
	draft := m.draft
	leasePush := m.leasePush

	if worktreeDir == "" || branch == "" {
		return func() tea.Msg {
			return publishExecuteResultMsg{
				err: fmt.Errorf("publish not configured: missing worktree or branch"),
			}
		}
	}

	featureID := m.featureID

	return func() tea.Msg {
		if !publishable {
			return publishExecuteResultMsg{
				featureID: featureID,
				repoName:  repoName,
				err:       fmt.Errorf("publish skipped: feature repos are not publishable (no origin remote)"),
			}
		}

		// For multi-repo manual publish, commit any uncommitted changes before pushing.
		// Single-repo manual publish already commits in transitionToPublish; auto-publish
		// commits in autoPublishRepoCmd. This ensures multi-repo parity.
		if repoName != "" && git.HasUncommittedChanges(worktreeDir) {
			if err := git.CommitAll(worktreeDir, featureName); err != nil {
				return publishExecuteResultMsg{
					featureID: featureID,
					repoName:  repoName,
					err:       fmt.Errorf("commit failed: %w", err),
				}
			}
		}

		if leasePush {
			// Manual publish is an explicit post-review update. Use a lease push so
			// rebased feature branches update origin/<branch> instead of rebasing
			// local history back onto the pre-rebase remote branch.
			if err := git.ForcePush(worktreeDir, branch); err != nil {
				return publishExecuteResultMsg{featureID: featureID, repoName: repoName, err: fmt.Errorf("force push failed: %w", err)}
			}
		} else {
			// Pull-rebase before regular push to sync with any remote changes.
			prResult := git.PullRebase(worktreeDir, branch)
			switch prResult.Outcome {
			case git.PullRebaseConflict:
				rebaseTarget := baseBranch
				if rebaseTarget == "" {
					defaultRepoPath := repoPath
					if defaultRepoPath == "" {
						defaultRepoPath = worktreeDir
					}
					rebaseTarget = git.DefaultBranch(defaultRepoPath)
				}
				return publishExecuteResultMsg{
					featureID:        featureID,
					repoName:         repoName,
					err:              fmt.Errorf("pull-rebase conflict: %w", prResult.Err),
					conflictDetected: true,
					branch:           branch,
					rebaseTarget:     rebaseTarget,
				}
			case git.PullRebaseFailure:
				return publishExecuteResultMsg{
					featureID: featureID,
					repoName:  repoName,
					err:       fmt.Errorf("pull-rebase failed: %w", prResult.Err),
				}
			}

			if err := git.Push(worktreeDir, branch); err != nil {
				return publishExecuteResultMsg{featureID: featureID, repoName: repoName, err: fmt.Errorf("push failed: %w", err)}
			}
		}

		// Create or update PR
		effectiveRepoPath := repoPath
		if effectiveRepoPath == "" {
			effectiveRepoPath = worktreeDir
		}

		if prTitle == "" {
			prTitle = "Feature implementation"
		}
		if prBody == "" {
			prBody = "Automated PR from agentic workflow orchestrator."
		}

		var prURL string
		if existingPRURL != "" {
			// Re-publish: push already done above; update existing PR body
			prURL = existingPRURL
			updatedBody := git.InjectPRSignature(prBody)
			if updateErr := git.UpdatePRBody(prURL, updatedBody); updateErr != nil {
				log.Printf("re-publish: failed to update PR body for %s: %v", prURL, updateErr)
			}
		} else {
			var err error
			prURL, err = git.CreatePR(effectiveRepoPath, branch, prTitle, prBody, draft, baseBranch)
			if err != nil {
				return publishExecuteResultMsg{featureID: featureID, repoName: repoName, err: fmt.Errorf("PR creation failed: %w", err)}
			}
		}

		// Inject cross-references for multi-repo features
		if len(crossRefEntries) > 1 {
			// Update entries with the current PR URL
			updatedEntries := make([]git.CrossRefEntry, len(crossRefEntries))
			copy(updatedEntries, crossRefEntries)
			for i := range updatedEntries {
				if updatedEntries[i].RepoName == repoName {
					updatedEntries[i].PRURL = prURL
					break
				}
			}
			crossRefSection := git.BuildCrossReferenceSection(featureName, updatedEntries)
			if crossRefSection != "" {
				currentBody, getErr := git.GetPRBody(prURL)
				if getErr != nil {
					log.Printf("cross-ref: failed to get PR body for %s: %v", prURL, getErr)
				} else {
					updatedBody := git.InjectCrossReferenceSection(currentBody, crossRefSection)
					if updateErr := git.UpdatePRBody(prURL, updatedBody); updateErr != nil {
						log.Printf("cross-ref: failed to update PR body for %s: %v", prURL, updateErr)
					}
				}
				// Retroactively update earlier PRs
				if retroErr := git.RetroactivelyUpdateCrossRefs(featureName, updatedEntries, repoName); retroErr != nil {
					log.Printf("cross-ref: failed to retroactively update PRs: %v", retroErr)
				}
			}
		}

		return publishExecuteResultMsg{prURL: prURL, repoName: repoName}
	}
}

func (m PublishModel) View() string {
	var b strings.Builder
	w := m.width
	if w < 40 {
		w = 80
	}
	boxWidth := w - 2

	b.WriteString(TitleStyle.Render(" Publish Feature"))
	// Dynamic step count: 7 with repo selector, 6 without
	totalSteps := 6
	displayStep := int(m.step) + 1
	if m.hasRepoSelect {
		totalSteps = 7
	} else {
		// Without repo select, step numbers start from publishStepDiff (1)
		displayStep = int(m.step)
	}
	fmt.Fprintf(&b, "  Step %d/%d\n\n", displayStep, totalSteps)

	var content string
	var title string
	switch m.step {
	case publishStepRepoSelect:
		title = "Select Repository"
		var sel strings.Builder
		for i, r := range m.repos {
			cursor := "  "
			if i == m.selectedRepo {
				cursor = "> "
			}
			status := r.PRStatus
			if r.PRURL != "" && r.PRStatus == "published" {
				// Extract PR number from URL for display
				num := extractPRNumber(r.PRURL)
				if num != "" {
					status = "PR #" + num
				}
			}
			fmt.Fprintf(&sel, "%s%-20s %-30s %s\n", cursor, r.Name, r.Branch, status)
		}
		content = sel.String()

	case publishStepDiff:
		title = "Diff Review"
		content = m.viewport.View()

	case publishStepCommits:
		title = "Commit Log"
		content = m.viewport.View()

	case publishStepPRDesc:
		title = "PR Description"
		if m.generating {
			content = m.spinnerView + " Generating PR description..."
		} else {
			content = "Title: " + m.titleInput.View() + "\n\nBody:\n" + m.bodyInput.View()
		}

	case publishStepConfirm:
		title = "Confirm"
		confirmContent := "Ready to push and create PR?\n\n"
		confirmContent += fmt.Sprintf("  Title: %s\n", m.prTitle)
		if m.branch != "" {
			confirmContent += fmt.Sprintf("  Branch: %s\n", m.branch)
		}
		var draftIndicator string
		if m.draft {
			draftIndicator = BadgeStyle.Render("[Draft PR]")
		} else {
			draftIndicator = SuccessStyle.Render("[Ready for review]")
		}
		confirmContent += fmt.Sprintf("  %s\n", draftIndicator)
		confirmContent += "\n  Press Enter to confirm, Esc to cancel"
		content = confirmContent

	case publishStepExecute:
		title = "Publishing"
		if m.errMsg != "" {
			content = ErrorStyle.Render(fmt.Sprintf("Error: %s", m.errMsg))
		} else if m.prURL != "" {
			content = SuccessStyle.Render(fmt.Sprintf("PR created: %s", m.prURL))
		} else {
			content = "Pushing and creating PR..."
		}

	case publishStepDone:
		title = "Done"
		if m.prURL != "" {
			content = SuccessStyle.Render(fmt.Sprintf("PR created: %s", m.prURL))
		} else if m.errMsg != "" {
			content = ErrorStyle.Render(fmt.Sprintf("Error: %s", m.errMsg))
		} else {
			content = "Done."
		}
	}

	box := panelStyle(true).Width(boxWidth).Render(content)
	if m.step == publishStepDiff || m.step == publishStepCommits {
		box = renderReviewViewportBox(boxWidth, title, m.viewport)
	} else {
		box = renderBorderTitle(box, title, TitleStyle)
	}
	b.WriteString(" " + strings.ReplaceAll(box, "\n", "\n "))

	// Scroll indicator for viewport steps
	if m.step == publishStepDiff || m.step == publishStepCommits {
		b.WriteString("\n")
		b.WriteString(renderReviewViewportScrollPercent(m.viewport))
	}

	b.WriteString("\n")
	if m.step == publishStepExecute {
		b.WriteString(MutedStyle.Render(" Publishing in progress..."))
	} else if m.step == publishStepRepoSelect {
		b.WriteString(KeyHelpStyle.Render(" [↑/↓] Select   [enter] Confirm   [esc] Cancel"))
	} else if m.step == publishStepConfirm {
		b.WriteString(KeyHelpStyle.Render(" [enter] Next   [esc] Cancel   [d] Toggle draft"))
	} else {
		b.WriteString(KeyHelpStyle.Render(" [enter] Next   [esc] Cancel   [↑/↓] Scroll"))
	}
	b.WriteString("\n")

	return b.String()
}

func (m PublishModel) IsDone() bool {
	return m.step == publishStepDone
}

// extractPRNumber extracts the PR number from a GitHub PR URL.
// e.g., "https://github.com/org/repo/pull/42" returns "42".
func extractPRNumber(prURL string) string {
	parts := strings.Split(prURL, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}
