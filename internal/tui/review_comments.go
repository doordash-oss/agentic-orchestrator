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
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
)

type reviewCommentItem struct {
	ID        int
	Type      string
	RepoName  string
	Path      string
	Line      int
	Author    string
	Body      string
	DiffHunk  string
	CreatedAt string
}

type reviewCommentsBrowserModel struct {
	featureSlug string
	repoName    string
	items       []reviewCommentItem
	included    map[int]bool
	selected    int
	filter      string
	filtering   bool
	detail      viewport.Model
	width       int
	height      int
	status      string
}

type ReviewCommentsActionMode string

const (
	ReviewCommentsActionAll      ReviewCommentsActionMode = "all"
	ReviewCommentsActionIncluded ReviewCommentsActionMode = "included"
)

type ReviewCommentsActionMsg struct {
	FeatureID string
	Mode      ReviewCommentsActionMode
	Comments  []git.ReviewComment
}

// ReviewCommentsModel displays fetched PR review comments and lets the user
// choose how to handle them.
type ReviewCommentsModel struct {
	featureID   string
	featureSlug string
	comments    []git.ReviewComment
	browser     reviewCommentsBrowserModel
	width       int
	height      int
}

func NewReviewCommentsModel(featureID, slug string, comments []git.ReviewComment, width, height int) ReviewCommentsModel {
	browser := newReviewCommentsBrowserModel(slug, "", reviewCommentItemsFromGit(comments), width, height)
	return ReviewCommentsModel{
		featureID:   featureID,
		featureSlug: slug,
		comments:    append([]git.ReviewComment(nil), comments...),
		browser:     browser,
		width:       width,
		height:      height,
	}
}

func (m ReviewCommentsModel) Init() tea.Cmd {
	return nil
}

func (m ReviewCommentsModel) Update(msg tea.Msg) (ReviewCommentsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.browser.resize(msg.Width, msg.Height)
	case tea.KeyPressMsg:
		switch {
		case msg.Code == tea.KeyEnter:
			included := m.includedComments()
			if len(included) == 0 {
				m.browser.status = "No comments included. Press space to include one, or Shift+A to address all."
				return m, nil
			}
			return m, reviewCommentsActionCmd(m.featureID, ReviewCommentsActionIncluded, included)
		case msg.Code == 'A' && msg.Text == "A":
			return m, reviewCommentsActionCmd(m.featureID, ReviewCommentsActionAll, m.comments)
		}
		var cmd tea.Cmd
		m.browser, cmd = m.browser.Update(msg)
		return m, cmd
	}
	return m, nil
}

// WithSize returns a copy with updated dimensions.
func (m ReviewCommentsModel) WithSize(width, height int) ReviewCommentsModel {
	m.width = width
	m.height = height
	m.browser.resize(width, height)
	return m
}

func (m ReviewCommentsModel) View() string {
	return m.browser.View()
}

func (m ReviewCommentsModel) includedComments() []git.ReviewComment {
	out := make([]git.ReviewComment, 0, len(m.comments))
	for _, c := range m.comments {
		if m.browser.included[c.ID] {
			out = append(out, c)
		}
	}
	return out
}

func reviewCommentsActionCmd(featureID string, mode ReviewCommentsActionMode, comments []git.ReviewComment) tea.Cmd {
	return func() tea.Msg {
		return ReviewCommentsActionMsg{
			FeatureID: featureID,
			Mode:      mode,
			Comments:  append([]git.ReviewComment(nil), comments...),
		}
	}
}

func reviewCommentItemsFromGit(comments []git.ReviewComment) []reviewCommentItem {
	items := make([]reviewCommentItem, 0, len(comments))
	for _, c := range comments {
		items = append(items, reviewCommentItem{
			ID:        c.ID,
			Type:      c.Type,
			RepoName:  c.RepoName,
			Path:      c.Path,
			Line:      c.Line,
			Author:    c.User.Login,
			Body:      c.Body,
			DiffHunk:  c.DiffHunk,
			CreatedAt: c.CreatedAt,
		})
	}
	return items
}

func reviewCommentItemsFromDTO(comments []server.ReviewCommentDTO) []reviewCommentItem {
	items := make([]reviewCommentItem, 0, len(comments))
	for _, c := range comments {
		items = append(items, reviewCommentItem{
			ID:        c.ID,
			Type:      c.Type,
			RepoName:  c.RepoName,
			Path:      c.Path,
			Line:      c.Line,
			Author:    c.UserLogin,
			Body:      c.Body,
			DiffHunk:  c.DiffHunk,
			CreatedAt: c.CreatedAt,
		})
	}
	return items
}

func newReviewCommentsBrowserModel(slug, repo string, items []reviewCommentItem, width, height int) reviewCommentsBrowserModel {
	detail := viewport.New(
		viewport.WithWidth(reviewCommentsDetailWidth(width)),
		viewport.WithHeight(reviewCommentsBodyHeight(height)),
	)
	detail.SoftWrap = true
	m := reviewCommentsBrowserModel{
		featureSlug: slug,
		repoName:    repo,
		items:       append([]reviewCommentItem(nil), items...),
		included:    make(map[int]bool, len(items)),
		detail:      detail,
		width:       width,
		height:      height,
	}
	for _, item := range items {
		m.included[item.ID] = true
	}
	m.refreshDetail()
	return m
}

func reviewCommentsBodyHeight(height int) int {
	return max(height-5, 8)
}

func reviewCommentsQueueWidth(width int) int {
	if width < 88 {
		return max(width-4, 40)
	}
	return max(width/3, 30)
}

func reviewCommentsDetailWidth(width int) int {
	if width < 88 {
		return max(width-4, 40)
	}
	return max(width-reviewCommentsQueueWidth(width)-7, 40)
}

func (m *reviewCommentsBrowserModel) resize(width, height int) {
	m.width = width
	m.height = height
	m.detail.SetWidth(reviewCommentsDetailWidth(width))
	m.detail.SetHeight(reviewCommentsBodyHeight(height))
	m.refreshDetail()
}

func (m reviewCommentsBrowserModel) Update(msg tea.Msg) (reviewCommentsBrowserModel, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		var cmd tea.Cmd
		m.detail, cmd = m.detail.Update(msg)
		return m, cmd
	}

	if m.filtering {
		switch keyMsg.Code {
		case tea.KeyEscape:
			m.clearFilter()
			return m, nil
		case tea.KeyBackspace:
			m.backspaceFilter()
			return m, nil
		}
		if keyMsg.Text != "" && keyMsg.Code != tea.KeySpace {
			m.appendFilterRune(keyMsg.Text)
			return m, nil
		}
	}

	switch {
	case keyMsg.Code == tea.KeyDown || keyMsg.Text == "j":
		m.moveSelection(1)
		return m, nil
	case keyMsg.Code == tea.KeyUp || keyMsg.Text == "k":
		m.moveSelection(-1)
		return m, nil
	case keyMsg.Code == tea.KeyHome:
		m.selected = 0
		m.refreshDetail()
		return m, nil
	case keyMsg.Code == tea.KeyEnd:
		visible := m.visibleItems()
		if len(visible) > 0 {
			m.selected = len(visible) - 1
			m.refreshDetail()
		}
		return m, nil
	case keyMsg.Code == tea.KeySpace:
		m.toggleSelectedIncluded()
		return m, nil
	case keyMsg.Code == '/':
		m.startFilter()
		return m, nil
	case keyMsg.Code == tea.KeyPgDown || keyMsg.Code == tea.KeyPgUp:
		var cmd tea.Cmd
		m.detail, cmd = m.detail.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m reviewCommentsBrowserModel) View() string {
	w := max(m.width, 80)
	visible := m.visibleItems()
	if len(visible) == 0 {
		return m.renderNoMatches()
	}
	if len(m.items) == 0 {
		return m.renderEmpty(w)
	}

	var b strings.Builder
	b.WriteString(m.renderHeader())
	b.WriteString("\n")
	body := m.renderBody()
	b.WriteString(body)
	b.WriteString("\n")
	if m.status != "" {
		b.WriteString(" ")
		b.WriteString(WarningStyle.Render(m.status))
		b.WriteString("\n")
	}
	b.WriteString(m.renderFooter())
	b.WriteString("\n")
	return b.String()
}

func (m reviewCommentsBrowserModel) renderEmpty(width int) string {
	var b strings.Builder
	b.WriteString(TitleStyle.Render(" Review Comments"))
	if m.featureSlug != "" {
		b.WriteString(MutedStyle.Render("  " + m.featureSlug))
	}
	b.WriteString("\n\n")
	b.WriteString(" ")
	b.WriteString(SuccessStyle.Render("No pending review comments for this PR."))
	b.WriteString("\n\n")
	b.WriteString(KeyHelpStyle.Render(" [esc] Back   [q] Quit"))
	b.WriteString("\n")
	return truncateRenderedLines(b.String(), width)
}

func (m reviewCommentsBrowserModel) renderHeader() string {
	parts := []string{
		TitleStyle.Render(" Review Comments"),
		MutedStyle.Render(m.featureSlug),
		fmt.Sprintf("%d pending", len(m.items)),
		fmt.Sprintf("%d included", m.includedCount()),
	}
	if m.repoName != "" {
		parts = append(parts[:2], append([]string{MutedStyle.Render(m.repoName)}, parts[2:]...)...)
	}
	if m.filter != "" {
		parts = append(parts, WarningStyle.Render("filter: "+m.filter))
	}
	return strings.Join(parts, "  ")
}

func (m reviewCommentsBrowserModel) renderBody() string {
	if m.width < 88 {
		queue := panelStyle(false).Width(reviewCommentsQueueWidth(m.width)).Render(m.renderQueue(reviewCommentsQueueWidth(m.width) - 4))
		detail := panelStyle(true).Width(reviewCommentsQueueWidth(m.width)).Render(m.detail.View())
		return " " + strings.ReplaceAll(queue+"\n"+detail, "\n", "\n ")
	}

	queueW := reviewCommentsQueueWidth(m.width)
	detailW := reviewCommentsDetailWidth(m.width)
	queue := panelStyle(true).Width(queueW).Render(m.renderQueue(queueW - 4))
	detail := panelStyle(false).Width(detailW).Render(m.detail.View())
	return " " + lipgloss.JoinHorizontal(lipgloss.Top, queue, " ", detail)
}

func (m reviewCommentsBrowserModel) renderQueue(width int) string {
	var b strings.Builder
	b.WriteString(TitleStyle.Render("Queue"))
	b.WriteString("\n")
	for i, item := range m.visibleItems() {
		row := m.renderQueueRow(i, item, width)
		if i == m.selected {
			row = SelectedRowStyle.Render(row)
		}
		b.WriteString(row)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m reviewCommentsBrowserModel) renderQueueRow(index int, item reviewCommentItem, width int) string {
	marker := "[ ]"
	if m.included[item.ID] {
		marker = "[x]"
	}
	cursor := " "
	if index == m.selected {
		cursor = ">"
	}
	location := reviewCommentLocation(item)
	line := fmt.Sprintf("%s %s %s", cursor, marker, location)
	if item.Author != "" {
		line += " @" + item.Author
	}
	if item.Body != "" {
		line += " - " + strings.TrimSpace(strings.Split(item.Body, "\n")[0])
	}
	return truncatePlain(line, max(width, 20))
}

func (m *reviewCommentsBrowserModel) refreshDetail() {
	m.detail.SetContent(m.renderDetail())
}

func (m reviewCommentsBrowserModel) renderDetail() string {
	if len(m.items) == 0 {
		return ""
	}
	item, ok := m.selectedItem()
	if !ok {
		return MutedStyle.Render("No comments match " + m.filter)
	}
	width := max(m.detail.Width(), 40)
	var b strings.Builder
	b.WriteString(TitleStyle.Render("Detail"))
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("%s %s\n", LabelStyle.Render("Location"), reviewCommentLocation(item)))
	if item.Author != "" {
		b.WriteString(fmt.Sprintf("%s @%s\n", LabelStyle.Render("Author"), item.Author))
	}
	b.WriteString(fmt.Sprintf("%s %s\n", LabelStyle.Render("Type"), reviewCommentTypeLabel(item.Type)))
	if item.CreatedAt != "" {
		b.WriteString(fmt.Sprintf("%s %s\n", LabelStyle.Render("Created"), item.CreatedAt))
	}
	b.WriteString("\n")
	if strings.TrimSpace(item.Body) != "" {
		b.WriteString(WarningStyle.Render("Reviewer comment"))
		b.WriteString("\n")
		b.WriteString(wrapForViewport(strings.TrimSpace(item.Body), width))
		b.WriteString("\n\n")
	}
	b.WriteString(WarningStyle.Render("Context"))
	b.WriteString("\n")
	if strings.TrimSpace(item.DiffHunk) == "" {
		b.WriteString(MutedStyle.Render("No diff context available"))
		return b.String()
	}
	b.WriteString(colorizeDiff(wrapForViewport(strings.TrimSpace(item.DiffHunk), width)))
	return b.String()
}

func (m reviewCommentsBrowserModel) renderFooter() string {
	all := len(m.items)
	included := m.includedCount()
	return KeyHelpStyle.Render(fmt.Sprintf(" [Shift+A] Address all %d   [enter] Address included %d   [space] Include/exclude   [/] Filter   [esc] Back", all, included))
}

func (m reviewCommentsBrowserModel) includedCount() int {
	count := 0
	for _, item := range m.items {
		if m.included[item.ID] {
			count++
		}
	}
	return count
}

func (m reviewCommentsBrowserModel) visibleItems() []reviewCommentItem {
	if strings.TrimSpace(m.filter) == "" {
		return append([]reviewCommentItem(nil), m.items...)
	}
	query := strings.ToLower(strings.TrimSpace(m.filter))
	out := make([]reviewCommentItem, 0, len(m.items))
	for _, item := range m.items {
		haystack := strings.ToLower(strings.Join([]string{
			reviewCommentLocation(item),
			item.Author,
			item.Body,
			item.Type,
			reviewCommentTypeLabel(item.Type),
		}, "\n"))
		if strings.Contains(haystack, query) {
			out = append(out, item)
		}
	}
	return out
}

func (m reviewCommentsBrowserModel) selectedItem() (reviewCommentItem, bool) {
	visible := m.visibleItems()
	if len(visible) == 0 {
		return reviewCommentItem{}, false
	}
	idx := min(max(m.selected, 0), len(visible)-1)
	return visible[idx], true
}

func (m *reviewCommentsBrowserModel) toggleSelectedIncluded() {
	item, ok := m.selectedItem()
	if !ok {
		return
	}
	m.included[item.ID] = !m.included[item.ID]
	m.status = ""
}

func (m *reviewCommentsBrowserModel) moveSelection(delta int) {
	visible := m.visibleItems()
	if len(visible) == 0 {
		m.selected = 0
		m.refreshDetail()
		return
	}
	m.selected += delta
	if m.selected < 0 {
		m.selected = 0
	}
	if m.selected >= len(visible) {
		m.selected = len(visible) - 1
	}
	m.refreshDetail()
}

func (m *reviewCommentsBrowserModel) startFilter() {
	m.filtering = true
	m.filter = ""
	m.selected = 0
	m.status = "Filtering comments"
	m.refreshDetail()
}

func (m *reviewCommentsBrowserModel) appendFilterRune(text string) {
	m.filter += text
	m.selected = 0
	m.status = ""
	m.refreshDetail()
}

func (m *reviewCommentsBrowserModel) backspaceFilter() {
	if m.filter == "" {
		return
	}
	runes := []rune(m.filter)
	m.filter = string(runes[:len(runes)-1])
	m.selected = 0
	m.refreshDetail()
}

func (m *reviewCommentsBrowserModel) clearFilter() {
	m.filter = ""
	m.filtering = false
	m.selected = 0
	m.status = ""
	m.refreshDetail()
}

func (m reviewCommentsBrowserModel) renderNoMatches() string {
	var b strings.Builder
	b.WriteString(m.renderHeader())
	b.WriteString("\n\n ")
	b.WriteString(MutedStyle.Render(fmt.Sprintf("No comments match %q", m.filter)))
	b.WriteString("\n\n")
	b.WriteString(m.renderFooter())
	b.WriteString("\n")
	return b.String()
}

func reviewCommentLocation(item reviewCommentItem) string {
	if item.Path != "" {
		if item.Line > 0 {
			return fmt.Sprintf("%s:%d", item.Path, item.Line)
		}
		return item.Path
	}
	switch item.Type {
	case git.CommentTypeIssue:
		return "PR conversation"
	case git.CommentTypeReviewBody:
		return "PR review"
	default:
		return strings.TrimSpace(item.Type)
	}
}

func reviewCommentTypeLabel(typ string) string {
	switch typ {
	case git.CommentTypeIssue:
		return "PR conversation"
	case git.CommentTypeReviewBody:
		return "PR review"
	case git.CommentTypeReview:
		return "Inline review comment"
	default:
		if strings.TrimSpace(typ) == "" {
			return "Review comment"
		}
		return typ
	}
}
