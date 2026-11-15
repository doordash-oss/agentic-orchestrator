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
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

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
	lastState  repoState
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
		h.recordReview(meta.Iteration, meta.AxisVerdicts, metaRepoState(meta))
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

func (h *reviewRoundHistory) recordReview(iteration int, verdicts map[string]string, state repoState) {
	for slug, verdict := range verdicts {
		rec := h.axes[slug]
		if rec == nil {
			rec = &reviewAxisRecord{}
			h.axes[slug] = rec
		}
		rec.verdict = verdict
		rec.lastReview = iteration
		rec.lastState = state
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

// planRound picks the next round's axes given the repos' current state.
// Axes without an approval run, scoped to their open findings after the
// first round. Approvals stick until the repos differ from what the axis
// approved, or a recorded fix landed after it; once nothing is open, each
// stale approval confirms once. An axis whose delta is unknown reruns in
// full. An empty plan means approval.
func (h *reviewRoundHistory) planRound(axes []implementationReviewAxis, now repoState) []reviewAxisRun {
	if h.accepted {
		return nil
	}
	var open, confirm []reviewAxisRun
	for _, axis := range axes {
		rec := h.axes[axis.Slug]
		if rec == nil {
			open = append(open, reviewAxisRun{Axis: axis, Mode: reviewRoundFull})
			continue
		}
		scoped := rec.lastState.known() && now.known()
		run := reviewAxisRun{Axis: axis, Mode: reviewRoundFull}
		if scoped {
			run = reviewAxisRun{Axis: axis, Since: rec.lastReview, SinceHeads: rec.lastState.Heads}
		}
		switch {
		case rec.verdict != ReviewApproved.String():
			if scoped {
				run.Mode = reviewRoundFollowUp
			}
			open = append(open, run)
		case h.lastFixChange >= rec.lastReview || (scoped && now.differs(rec.lastState)):
			if scoped {
				run.Mode = reviewRoundConfirm
			}
			confirm = append(confirm, run)
		}
	}
	if len(open) > 0 {
		return open
	}
	return confirm
}

// repoState is what a review saw: every repo's HEAD plus a fingerprint of
// its uncommitted work. Nil Heads means the state is unknown.
type repoState struct {
	Heads map[string]string
	Dirt  string
}

func (s repoState) known() bool { return s.Heads != nil }

// differs reports whether s and other may hold different code; unknown
// state always differs.
func (s repoState) differs(other repoState) bool {
	return !s.known() || !other.known() || !maps.Equal(s.Heads, other.Heads) || s.Dirt != other.Dirt
}

func metaRepoState(meta iterationMeta) repoState {
	return repoState{Heads: meta.ReviewHeads, Dirt: meta.ReviewDirt}
}

func currentRepoState(repos map[string]string) repoState {
	heads := repoHeads(repos)
	if heads == nil {
		return repoState{}
	}
	names := make([]string, 0, len(repos))
	for name, path := range repos {
		if path != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	paths := make([]string, 0, len(names))
	for _, name := range names {
		paths = append(paths, repos[name])
	}
	return repoState{Heads: heads, Dirt: worktreeStateFingerprint(context.Background(), nil, paths)}
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

// reposChangedSince reports whether the repos may hold different code than
// before. Unknown state counts as changed.
func reposChangedSince(repos map[string]string, before repoState) bool {
	return currentRepoState(repos).differs(before)
}

const reviewDecisionsFile = "review-decisions.md"

// disputeQuestionPrefix marks a fixer's or implementer's question disputing
// a review finding; the skills require it.
const disputeQuestionPrefix = "dispute:"

// reviewDecisionPairs keeps the answers that are review decisions: disputes
// and the handoff question. Clarifying questions and auto-picked answers
// are not decisions.
func reviewDecisionPairs(qa []ports.QAPair, handoffQuestion string) []ports.QAPair {
	var decisions []ports.QAPair
	for _, pair := range qa {
		if pair.AutoPicked || strings.TrimSpace(pair.Answer) == "" {
			continue
		}
		dispute := strings.HasPrefix(strings.ToLower(strings.TrimSpace(pair.Question)), disputeQuestionPrefix)
		if dispute || isHandoffPair(pair, handoffQuestion) {
			decisions = append(decisions, pair)
		}
	}
	return decisions
}

// appendReviewDecisions records one session's decisions under heading,
// replacing an earlier section with the same heading so a rerun session
// does not record twice. It reports whether any were recorded.
func appendReviewDecisions(path, heading string, qa []ports.QAPair) (bool, error) {
	if len(qa) == 0 {
		return false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("reading review decisions: %w", err)
	}
	body := string(data)
	if body == "" {
		body = "# Review Decisions\n\nThe user made these decisions during review. They override the design's acceptance criteria and earlier findings; a finding accepted as is is not raised again.\n"
	}
	marker := "\n## " + heading + "\n"
	if start := strings.Index(body, marker); start >= 0 {
		rest := body[start+len(marker):]
		end := len(body)
		if next := strings.Index(rest, "\n## "); next >= 0 {
			end = start + len(marker) + next
		}
		body = body[:start] + body[end:]
	}
	var b strings.Builder
	b.WriteString(body)
	fmt.Fprintf(&b, "\n## %s\n", heading)
	for _, pair := range qa {
		fmt.Fprintf(&b, "\n**Q:** %s\n\n**A:** %s\n", strings.TrimSpace(pair.Question), strings.TrimSpace(pair.Answer))
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
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
	handoffContinueLabel    = "Continue with guidance"
	handoffAcceptLabel      = "Accept and proceed to Publish"
	phaseHandoffAcceptLabel = "Accept and finish the phase"
	handoffStopLabel        = "Stop"
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

// handoffQuestionMarker is the closing line of every handoff question.
func handoffQuestionMarker(gateLabel string) string {
	return fmt.Sprintf("How should %s proceed?", gateLabel)
}

// isHandoffPair reports whether pair answers the handoff question: it
// carries the question's closing line, or its answer is one of the options.
func isHandoffPair(pair ports.QAPair, handoffQuestion string) bool {
	if handoffQuestion == "" {
		return false
	}
	lines := strings.Split(strings.TrimSpace(handoffQuestion), "\n")
	if marker := strings.TrimSpace(lines[len(lines)-1]); marker != "" && strings.Contains(pair.Question, marker) {
		return true
	}
	_, exact := handoffLabelChoice(answerLabel(pair.Answer))
	return exact
}

// handoffAnswer returns the user's answer to the handoff question, if any.
func handoffAnswer(qa []ports.QAPair, handoffQuestion string) (string, bool) {
	for i := len(qa) - 1; i >= 0; i-- {
		if !qa[i].AutoPicked && strings.TrimSpace(qa[i].Answer) != "" && isHandoffPair(qa[i], handoffQuestion) {
			return qa[i].Answer, true
		}
	}
	return "", false
}

var handoffChoices = map[string]reviewHandoffChoice{
	strings.ToLower(handoffContinueLabel):    handoffContinue,
	strings.ToLower(handoffAcceptLabel):      handoffAccept,
	strings.ToLower(phaseHandoffAcceptLabel): handoffAccept,
	strings.ToLower(handoffStopLabel):        handoffStop,
}

// handoffLabelChoice matches an option label, its leading word, or a
// shortened label.
func handoffLabelChoice(label string) (reviewHandoffChoice, bool) {
	if choice, ok := handoffChoices[label]; ok {
		return choice, true
	}
	if len(label) < 4 {
		return 0, false
	}
	var found reviewHandoffChoice
	matched := false
	for option, choice := range handoffChoices {
		if !strings.HasPrefix(option, label) {
			continue
		}
		if matched && found != choice {
			return 0, false
		}
		found, matched = choice, true
	}
	return found, matched
}

// classifyHandoffAnswer maps the handoff answer to the way forward. An
// answer naming more than one option is not an answer; any other text is
// guidance.
func classifyHandoffAnswer(answer string) reviewHandoffChoice {
	label := answerLabel(answer)
	if label == "" {
		return handoffUnanswered
	}
	if choice, ok := handoffLabelChoice(label); ok {
		return choice
	}
	if parts := strings.Split(label, ", "); len(parts) > 1 {
		seen := map[reviewHandoffChoice]bool{}
		for _, part := range parts {
			choice, ok := handoffLabelChoice(strings.TrimSpace(part))
			if !ok {
				return handoffContinue
			}
			seen[choice] = true
		}
		if len(seen) > 1 {
			return handoffUnanswered
		}
		for choice := range seen {
			return choice
		}
	}
	return handoffContinue
}

func answerLabel(answer string) string {
	label := strings.ToLower(strings.TrimSpace(answer))
	label = strings.TrimSpace(strings.TrimSuffix(label, "(recommended)"))
	return strings.TrimRight(label, ". ")
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

// reviewHandoffPromptInput is the data for the review_handoff partial.
type reviewHandoffPromptInput struct {
	GateLabel     string
	AcceptOutcome string
	Reason        string
	Question      string
	NotePath      string
	ContinueLabel string
	AcceptLabel   string
	StopLabel     string
	// Answer is the user's answer once given, so a rerun session acts on it
	// instead of asking again.
	Answer string
}

const handoffPromptFile = "handoff.yaml"

func saveHandoffPrompt(dir string, prompt *reviewHandoffPromptInput) error {
	data, err := yaml.Marshal(prompt)
	if err != nil {
		return fmt.Errorf("encoding review handoff: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, handoffPromptFile), data, 0o644); err != nil {
		return fmt.Errorf("writing review handoff: %w", err)
	}
	return nil
}

func loadHandoffPrompt(dir string) *reviewHandoffPromptInput {
	data, err := os.ReadFile(filepath.Join(dir, handoffPromptFile))
	if err != nil {
		return nil
	}
	var prompt reviewHandoffPromptInput
	if err := yaml.Unmarshal(data, &prompt); err != nil || prompt.Question == "" {
		return nil
	}
	return &prompt
}

// prepareHandoff writes the handoff note under dir and returns the prompt
// that has the session ask the user.
func prepareHandoff(dir string, iteration int, h reviewHandoff, gateLabel, acceptLabel, acceptOutcome string) (*reviewHandoffPromptInput, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating review handoff directory: %w", err)
	}
	notePath := filepath.Join(dir, "handoff.md")
	if err := writeHandoffNote(notePath, iteration, h); err != nil {
		return nil, err
	}
	return &reviewHandoffPromptInput{
		GateLabel:     gateLabel,
		AcceptOutcome: acceptOutcome,
		Reason:        h.Reason,
		Question:      handoffQuestion(gateLabel, h, notePath),
		NotePath:      notePath,
		ContinueLabel: handoffContinueLabel,
		AcceptLabel:   acceptLabel,
		StopLabel:     handoffStopLabel,
	}, nil
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
	fmt.Fprintf(&b, "\nDetails: %s\n\n%s", notePath, handoffQuestionMarker(gateLabel))
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
	AcceptanceSource  string
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
