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
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

type reviewRoundMode string

const (
	reviewRoundFull     reviewRoundMode = "full"
	reviewRoundFollowUp reviewRoundMode = "follow_up"
	reviewRoundConfirm  reviewRoundMode = "confirm"
)

type reviewAxisRecord struct {
	verdict    string
	lastReview int
	lastHeads  map[string]string
	streak     int
}

// reviewRoundHistory is the per-axis standing of a multi-axis review loop,
// rebuilt from iteration metas so a resumed loop schedules the same rounds.
type reviewRoundHistory struct {
	axes          map[string]*reviewAxisRecord
	lastFixChange int
	accepted      bool
}

func loadReviewRoundHistory(am *artifactManager, artifactDir string, upTo int) *reviewRoundHistory {
	h := &reviewRoundHistory{axes: map[string]*reviewAxisRecord{}}
	scanIterationMetas(am, artifactDir, upTo, func(meta iterationMeta) {
		h.recordReview(meta.Iteration, meta.AxisVerdicts, meta.ReviewHeads)
		h.recordFix(meta.Iteration, meta.MadeProgress)
		if meta.Handoff != "" {
			h.resetStreaks()
		}
		if meta.Handoff == reviewHandoffAccepted {
			h.accepted = true
		}
	})
	return h
}

func (h *reviewRoundHistory) recordReview(iteration int, verdicts, heads map[string]string) {
	for slug, verdict := range verdicts {
		rec := h.axes[slug]
		if rec == nil {
			rec = &reviewAxisRecord{}
			h.axes[slug] = rec
		}
		rec.verdict = verdict
		rec.lastReview = iteration
		rec.lastHeads = heads
		if verdict == reviewChangesRequested.String() {
			rec.streak++
		} else {
			rec.streak = 0
		}
	}
}

func (h *reviewRoundHistory) recordFix(iteration int, changed bool) {
	if changed {
		h.lastFixChange = iteration
	}
}

func (h *reviewRoundHistory) resetStreaks() {
	for _, rec := range h.axes {
		rec.streak = 0
	}
}

// stalledAxis returns the first axis that requested changes in
// axisStallLimit consecutive rounds.
func (h *reviewRoundHistory) stalledAxis(axes []implementationReviewAxis) (implementationReviewAxis, bool) {
	for _, axis := range axes {
		if rec := h.axes[axis.Slug]; rec != nil && rec.streak >= axisStallLimit {
			return axis, true
		}
	}
	return implementationReviewAxis{}, false
}

func (h *reviewRoundHistory) approved(slug string) bool {
	rec := h.axes[slug]
	return rec != nil && rec.verdict == ReviewApproved.String()
}

// reviewAxisRun schedules one axis for a round. Since and SinceHeads name the
// axis's previous review and the repo HEADs it saw.
type reviewAxisRun struct {
	Axis       implementationReviewAxis
	Mode       reviewRoundMode
	Since      int
	SinceHeads map[string]string
}

// planRound picks the next round's axes. Axes without an approval run, in
// full the first time and scoped to their open findings after that.
// Approvals stick: once every axis has approved, each approval older than the
// last code-changing fix confirms once. An empty plan means approval.
func (h *reviewRoundHistory) planRound(axes []implementationReviewAxis) []reviewAxisRun {
	if h.accepted {
		return nil
	}
	var open, confirm []reviewAxisRun
	for _, axis := range axes {
		rec := h.axes[axis.Slug]
		switch {
		case rec == nil:
			open = append(open, reviewAxisRun{Axis: axis, Mode: reviewRoundFull})
		case rec.verdict != ReviewApproved.String():
			open = append(open, reviewAxisRun{Axis: axis, Mode: reviewRoundFollowUp, Since: rec.lastReview, SinceHeads: rec.lastHeads})
		case h.lastFixChange >= rec.lastReview:
			confirm = append(confirm, reviewAxisRun{Axis: axis, Mode: reviewRoundConfirm, Since: rec.lastReview, SinceHeads: rec.lastHeads})
		}
	}
	if len(open) > 0 {
		return open
	}
	return confirm
}

// repoHeads returns every repo's HEAD, or nil when any is unreadable.
func repoHeads(repos map[string]string) map[string]string {
	heads := make(map[string]string, len(repos))
	for name, path := range repos {
		if path == "" {
			continue
		}
		sha, err := git.CurrentHeadSHA(path)
		if err != nil {
			return nil
		}
		heads[name] = sha
	}
	if len(heads) == 0 {
		return nil
	}
	return heads
}

// reposChangedSince reports whether any repo moved off before or holds
// uncommitted work. Unknown state counts as changed.
func reposChangedSince(repos, before map[string]string) bool {
	after := repoHeads(repos)
	if before == nil || after == nil {
		return true
	}
	for name, sha := range before {
		if after[name] != sha {
			return true
		}
	}
	for _, path := range repos {
		if path == "" {
			continue
		}
		dirty, err := git.HasUncommittedChangesExcludingUntracked(path, FinalReviewRootOrchestrationArtifacts...)
		if err != nil || dirty {
			return true
		}
	}
	return false
}

const reviewDecisionsFile = "review-decisions.md"

// appendReviewDecisions records the user's answers from one session in the
// loop's decisions file and reports whether any were recorded.
func appendReviewDecisions(path, heading string, qa []ports.QAPair) (bool, error) {
	if len(qa) == 0 {
		return false, nil
	}
	var b strings.Builder
	if _, err := os.Stat(path); os.IsNotExist(err) {
		b.WriteString("# Review Decisions\n\nThe user made these decisions during review. They override the design's acceptance criteria and earlier findings; a finding accepted as is is not raised again.\n")
	}
	fmt.Fprintf(&b, "\n## %s\n", heading)
	for _, pair := range qa {
		fmt.Fprintf(&b, "\n**Q:** %s\n\n**A:** %s\n", strings.TrimSpace(pair.Question), strings.TrimSpace(pair.Answer))
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return false, fmt.Errorf("opening review decisions: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(b.String()); err != nil {
		return false, fmt.Errorf("writing review decisions: %w", err)
	}
	return true, nil
}

func existingPath(path string) string {
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

// userAnswerPaths lists the user's recorded answers from earlier phases of
// the run.
func userAnswerPaths(runDir string) []string {
	var paths []string
	for _, pattern := range []string{"*/qa-answers.md", "*/*/qa-answers.md"} {
		matches, _ := filepath.Glob(filepath.Join(runDir, pattern))
		paths = append(paths, matches...)
	}
	sort.Strings(paths)
	return paths
}

const fixNotesFile = "fix-notes.md"

var completionTagPattern = regexp.MustCompile(`(?s)<agentico-outcome>.*?</agentico-outcome>`)

// sessionClosingNote returns the text of the session's last assistant message.
func sessionClosingNote(sess ports.SessionView) string {
	if sess == nil || sess.MessageLog() == nil {
		return ""
	}
	msgs := sess.MessageLog().Messages()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Assistant == nil {
			continue
		}
		var parts []string
		for _, block := range msgs[i].Assistant.Message.Content {
			if block.IsText() {
				parts = append(parts, block.Text)
			}
		}
		if note := strings.TrimSpace(completionTagPattern.ReplaceAllString(strings.Join(parts, "\n"), "")); note != "" {
			return note
		}
	}
	return ""
}

const (
	handoffContinueLabel = "Continue with guidance"
	handoffAcceptLabel   = "Accept and proceed to Publish"
	handoffStopLabel     = "Stop"
)

// Handoff outcomes persisted in iteration metas.
const (
	reviewHandoffContinued = "continue"
	reviewHandoffAccepted  = "accept"
	reviewHandoffStopped   = "stop"
)

type reviewHandoffChoice int

const (
	handoffUnanswered reviewHandoffChoice = iota
	handoffContinue
	handoffAccept
	handoffStop
)

// classifyHandoffAnswer maps the user's handoff answer to the way forward;
// any answer other than accept or stop is guidance.
func classifyHandoffAnswer(qa []ports.QAPair) reviewHandoffChoice {
	if len(qa) == 0 {
		return handoffUnanswered
	}
	for _, pair := range qa {
		switch answerLabel(pair.Answer) {
		case strings.ToLower(handoffAcceptLabel):
			return handoffAccept
		case strings.ToLower(handoffStopLabel):
			return handoffStop
		}
	}
	return handoffContinue
}

func answerLabel(answer string) string {
	label := strings.ToLower(strings.TrimSpace(answer))
	return strings.TrimSpace(strings.TrimSuffix(label, "(recommended)"))
}

// reviewHandoff is why a review loop stopped converging and what it leaves
// open for the human.
type reviewHandoff struct {
	Reason    string
	Findings  string
	FixerNote string
}

func writeHandoffNote(path string, iteration int, h reviewHandoff) error {
	note := h.FixerNote
	if note == "" {
		note = "(none)"
	}
	findings := strings.TrimSpace(h.Findings)
	if findings == "" {
		findings = "(none)"
	}
	body := fmt.Sprintf("# Review Handoff — Iteration %d\n\n%s\n\n## Open Findings\n\n%s\n\n## Fixer's Note\n\n%s\n", iteration, h.Reason, findings, note)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return fmt.Errorf("writing review handoff note: %w", err)
	}
	return nil
}

const (
	handoffMaxFindings   = 8
	handoffMaxLineLength = 240
	handoffMaxNoteLength = 600
)

func handoffQuestion(gateLabel string, h reviewHandoff, notePath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s needs your decision. %s\n", gateLabel, h.Reason)
	if lines := blockingFindingLines(h.Findings); len(lines) > 0 {
		b.WriteString("\nOpen findings:\n")
		for _, line := range lines {
			fmt.Fprintf(&b, "- %s\n", line)
		}
	}
	if h.FixerNote != "" {
		fmt.Fprintf(&b, "\nFixer's note: %s\n", truncateText(h.FixerNote, handoffMaxNoteLength))
	}
	fmt.Fprintf(&b, "\nDetails: %s\n\nHow should %s proceed?", notePath, gateLabel)
	return b.String()
}

// blockingFindingLines flattens an aggregate feedback's findings into
// "Axis: finding" lines, preferring Critical and High findings.
func blockingFindingLines(feedback string) []string {
	section := extractMarkdownSection(feedback, "## Findings")
	if strings.TrimSpace(section) == "" {
		section = feedback
	}
	var all, blocking []string
	axis := ""
	for raw := range strings.SplitSeq(section, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "### "):
			axis = strings.TrimSpace(strings.TrimPrefix(line, "### "))
		case strings.HasPrefix(line, "- ") && line != "- (none)":
			text := strings.TrimSpace(strings.TrimPrefix(line, "- "))
			if axis != "" {
				text = axis + ": " + text
			}
			text = truncateText(text, handoffMaxLineLength)
			all = append(all, text)
			if strings.Contains(line, "**Critical**") || strings.Contains(line, "**High**") {
				blocking = append(blocking, text)
			}
		}
	}
	lines := blocking
	if len(lines) == 0 {
		lines = all
	}
	if len(lines) > handoffMaxFindings {
		lines = lines[:handoffMaxFindings]
	}
	return lines
}

func truncateText(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}

// reviewRoundUserInput is the data passed to review_round.user.tmpl.
type reviewRoundUserInput struct {
	GateLabel         string
	Mode              reviewRoundMode
	LiveRun           bool
	Since             int
	PriorFeedbackPath string
	Deltas            []reviewRoundDelta
	FixNotePaths      []string
	RoadmapPath       string
	UserAnswerPaths   []string
	DecisionsPath     string
}

type reviewRoundDelta struct {
	Repo string
	Path string
	Base string
}

func buildReviewRoundSection(in reviewRoundUserInput) string {
	return strings.TrimSpace(prompts.MustRender(prompts.TemplateReviewRoundUser, in)) + "\n"
}

// reviewRoundDeltas lists, per repo, the diff from the HEAD an axis last
// reviewed to the current worktree.
func reviewRoundDeltas(repos, since map[string]string) []reviewRoundDelta {
	if since == nil {
		return nil
	}
	var deltas []reviewRoundDelta
	for name, path := range repos {
		base := since[name]
		if path == "" || base == "" {
			continue
		}
		deltas = append(deltas, reviewRoundDelta{Repo: name, Path: path, Base: base})
	}
	sort.Slice(deltas, func(i, j int) bool { return deltas[i].Repo < deltas[j].Repo })
	return deltas
}

// fixNotePaths lists the fixer notes written from iteration since up to, but
// excluding, iteration before.
func fixNotePaths(artifactDir string, since, before int) []string {
	var paths []string
	for i := max(since, 1); i < before; i++ {
		iterDir := filepath.Join(artifactDir, fmt.Sprintf("iteration-%02d", i))
		for _, path := range []string{filepath.Join(iterDir, fixNotesFile), filepath.Join(iterDir, handoffDirName, fixNotesFile)} {
			if existingPath(path) != "" {
				paths = append(paths, path)
			}
		}
	}
	return paths
}

const handoffDirName = "handoff"
