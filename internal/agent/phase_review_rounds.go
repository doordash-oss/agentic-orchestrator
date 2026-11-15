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
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// reviewHandoffAsked marks the per-phase iteration that raised a handoff; the
// next implement session asks it.
const reviewHandoffAsked = "asked"

// phaseReviewGate applies the review round rules to Moonshot's per-phase
// review. Unlike Final Review, the fix for a review is the next implement
// iteration, and a handoff is asked by that iteration's session.
type phaseReviewGate struct {
	cfg     ImplementConfig
	repos   map[string]string
	axes    []implementationReviewAxis
	history *reviewRoundHistory

	lastReview int
	lastState  repoState
	pending    *reviewHandoffPromptInput
	pendingDir string
}

func newPhaseReviewGate(cfg ImplementConfig, am *artifactManager, upTo int) *phaseReviewGate {
	g := &phaseReviewGate{
		cfg:     cfg,
		repos:   implementRoundCommitRepos(cfg),
		axes:    perPhaseReviewAxes(cfg),
		history: &reviewRoundHistory{axes: map[string]*reviewAxisRecord{}, loadedAt: upTo},
	}
	askedAt := 0
	scanIterationMetas(am, cfg.ArtifactDir, upTo, func(meta iterationMeta) {
		switch meta.Handoff {
		case reviewHandoffAsked:
			askedAt = meta.Iteration
		case reviewHandoffAccepted:
			askedAt = 0
			g.history.accepted = true
		case reviewHandoffContinued, reviewHandoffStopped:
			askedAt = 0
			g.history.resetStreaks()
		}
		if len(meta.AxisVerdicts) > 0 {
			g.recordReview(meta.Iteration, meta.AxisVerdicts, metaRepoState(meta), meta.CodeChanged)
		}
	})
	if askedAt > 0 {
		g.pendingDir = filepath.Join(g.iterDir(askedAt), handoffDirName)
		g.pending = loadHandoffPrompt(g.pendingDir)
	}
	return g
}

func perPhaseReviewAxes(cfg ImplementConfig) []implementationReviewAxis {
	selection := implementationReviewAxisSelection{Profile: feature.PipelineMoonshot}
	if cfg.Feature != nil {
		selection = implementationReviewAxisSelection{
			Profile:              cfg.Feature.EffectivePipeline(),
			CurrentPhaseFrontend: cfg.Feature.RoadmapPhaseFrontend(cfg.Feature.CurrentRoadmapPhase),
			AnyPhaseFrontend:     cfg.Feature.AnyRoadmapPhaseFrontend(),
		}
	}
	return implementationReviewAxesForGate(implementationReviewGatePerPhase, selection)
}

func (g *phaseReviewGate) iterDir(iteration int) string {
	return filepath.Join(g.cfg.ArtifactDir, fmt.Sprintf("iteration-%02d", iteration))
}

func (g *phaseReviewGate) decisionsPath() string {
	return filepath.Join(g.cfg.ArtifactDir, reviewDecisionsFile)
}

func (g *phaseReviewGate) plan(now repoState) []reviewAxisRun {
	return g.history.planRound(g.axes, now)
}

// recordReview records a completed round; changed says code changed since
// the previous round.
func (g *phaseReviewGate) recordReview(iteration int, verdicts map[string]string, state repoState, changed bool) {
	if changed {
		g.history.recordFix(g.lastReview, true)
	}
	g.history.recordReview(iteration, verdicts, state)
	g.lastReview, g.lastState = iteration, state
}

// decided reports whether a session since the latest review recorded a
// decision.
func (g *phaseReviewGate) decided() bool {
	return decidedAfter(g.decisionsPath(), g.lastReview)
}

// awaitingFix reports whether the latest review left findings open.
func (g *phaseReviewGate) awaitingFix() bool {
	if g.history.accepted {
		return false
	}
	for _, axis := range g.axes {
		if rec := g.history.axes[axis.Slug]; rec != nil && rec.verdict != ReviewApproved.String() {
			return true
		}
	}
	return false
}

func (g *phaseReviewGate) changedSinceReview() bool {
	return reposChangedSince(g.repos, g.lastState)
}

// recordSession records the session's dispute and handoff answers in the
// decisions file, keeps a handoff answer with the pending handoff, and keeps
// the closing note for the next reviewers when it worked on findings.
func (g *phaseReviewGate) recordSession(iteration int, iterDir string, sess ports.SessionView) string {
	if sess == nil {
		return ""
	}
	heading := fmt.Sprintf("Iteration %d implementation", iteration)
	question := ""
	if g.pending != nil {
		heading = fmt.Sprintf("Iteration %d handoff", iteration)
		question = g.pending.Question
		if answer, ok := handoffAnswer(sess.QALog(), question); ok && g.pending.Answer == "" {
			g.pending.Answer = answer
			if err := saveHandoffPrompt(g.pendingDir, g.pending); err != nil {
				log.Printf("phase review: %v", err)
			}
		}
	}
	if _, err := appendReviewDecisions(g.decisionsPath(), heading, reviewDecisionPairs(sess.QALog(), question)); err != nil {
		log.Printf("phase review: %v", err)
	}
	note := sessionClosingNote(sess)
	if note != "" && (g.awaitingFix() || g.pending != nil) {
		_ = os.WriteFile(filepath.Join(iterDir, fixNotesFile), []byte(note+"\n"), 0o644)
	}
	return note
}

func (g *phaseReviewGate) label() string {
	if g.cfg.Feature != nil && g.cfg.Feature.CurrentRoadmapPhase > 0 {
		return fmt.Sprintf("Phase %d review", g.cfg.Feature.CurrentRoadmapPhase)
	}
	return "Implementation review"
}

// raiseHandoff writes the handoff under iterDir; the next implement session
// asks it.
func (g *phaseReviewGate) raiseHandoff(iteration int, iterDir string, h reviewHandoff) error {
	dir := filepath.Join(iterDir, handoffDirName)
	prompt, err := prepareHandoff(dir, iteration, h, g.label(), phaseHandoffAcceptLabel, "the phase passes")
	if err != nil {
		return err
	}
	if err := saveHandoffPrompt(dir, prompt); err != nil {
		return err
	}
	g.pending, g.pendingDir = prompt, dir
	return nil
}

// resolveHandoff classifies the user's answer to the pending handoff. A
// session that ended without an answer keeps the handoff pending.
func (g *phaseReviewGate) resolveHandoff() reviewHandoffChoice {
	choice := classifyHandoffAnswer(g.pending.Answer)
	switch choice {
	case handoffAccept:
		g.history.accepted = true
		g.pending = nil
	case handoffStop:
		g.pending = nil
	case handoffContinue:
		g.history.resetStreaks()
		g.pending = nil
	}
	return choice
}

// latestNote returns the most recent implementer note before iteration.
func (g *phaseReviewGate) latestNote(iteration int) string {
	paths := fixNotePaths(g.cfg.ArtifactDir, 1, iteration+1)
	if len(paths) == 0 {
		return ""
	}
	data, err := os.ReadFile(paths[len(paths)-1])
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// reviewInput scopes the round's axes to their open findings or approval
// plus the delta since their last review.
func (g *phaseReviewGate) reviewInput(iteration int, plan []reviewAxisRun) implementationReviewInput {
	planned := make(map[string]bool, len(plan))
	for _, run := range plan {
		planned[run.Axis.Slug] = true
	}
	var sticky []string
	for _, axis := range g.axes {
		if !planned[axis.Slug] && g.history.approved(axis.Slug) {
			sticky = append(sticky, axis.Name)
		}
	}
	return implementationReviewInput{
		ProgressPath: existingPath(filepath.Join(g.cfg.ArtifactDir, "progress.md")),
		Plan:         plan,
		Sticky:       sticky,
		RoundSection: func(run reviewAxisRun) string {
			return buildReviewRoundSection(g.reviewRoundInput(iteration, run))
		},
	}
}

func (g *phaseReviewGate) reviewRoundInput(iteration int, run reviewAxisRun) reviewRoundUserInput {
	in := reviewRoundUserInput{
		GateLabel:        implementationReviewGateLabel(implementationReviewGatePerPhase),
		AcceptanceSource: "the acceptance criteria in the design and the phase plan",
		Mode:             run.Mode,
		LiveRun:          run.Axis.ExecutionPosture == reviewPostureLiveRun,
		RoadmapPath:      g.cfg.RoadmapPath,
		DecisionsPath:    existingPath(g.decisionsPath()),
	}
	if g.cfg.RunDir != "" {
		in.UserAnswerPaths = userAnswerPaths(g.cfg.RunDir)
	}
	if run.Mode == reviewRoundFull {
		return in
	}
	in.Since = run.Since
	in.PriorFeedbackPath = g.axisFeedbackPath(run.Since, run.Axis.Slug)
	in.Deltas = reviewRoundDeltas(g.repos, run.SinceHeads)
	in.FixNotePaths = fixNotePaths(g.cfg.ArtifactDir, run.Since+1, iteration+1)
	return in
}

// axisFeedbackPath is where the axis wrote its verdict in iteration, which
// is under confirm/ when it ran in that iteration's confirmation round.
func (g *phaseReviewGate) axisFeedbackPath(iteration int, slug string) string {
	reviewDir := filepath.Join(g.iterDir(iteration), "review")
	if path := existingPath(filepath.Join(reviewDir, confirmRoundDir, slug, "review-feedback.md")); path != "" {
		return path
	}
	return filepath.Join(reviewDir, slug, "review-feedback.md")
}

const confirmRoundDir = "confirm"

// clone copies the history so a confirmation round can be planned before the
// iteration's verdicts are committed.
func (h *reviewRoundHistory) clone() *reviewRoundHistory {
	c := &reviewRoundHistory{axes: make(map[string]*reviewAxisRecord, len(h.axes)), lastFixChange: h.lastFixChange, loadedAt: h.loadedAt, accepted: h.accepted}
	for slug, rec := range h.axes {
		copied := *rec
		c.axes[slug] = &copied
	}
	return c
}

// confirmPlan is the confirmation round due once this iteration's verdicts
// are recorded.
func (g *phaseReviewGate) confirmPlan(iteration int, verdicts map[string]string, state repoState, changed bool) []reviewAxisRun {
	h := g.history.clone()
	if changed {
		h.recordFix(g.lastReview, true)
	}
	h.recordReview(iteration, verdicts, state)
	return h.planRound(g.axes, state)
}

// openFindings returns the latest review's aggregate findings.
func (g *phaseReviewGate) openFindings(fallback string) string {
	if g.lastReview > 0 {
		if data, err := os.ReadFile(filepath.Join(g.iterDir(g.lastReview), "review-feedback.md")); err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	return fallback
}

// carryOpenFindings keeps the open findings with an iteration that ran no
// review, so a resumed loop hands them to the next implement session.
func carryOpenFindings(iterDir, findings string) {
	if strings.TrimSpace(findings) != "" {
		_ = os.WriteFile(filepath.Join(iterDir, "review-feedback.md"), []byte(findings), 0o644)
	}
}

// implementContext is the review context the next implement session reads
// first: a pending handoff and the phase's recorded decisions.
func (g *phaseReviewGate) implementContext() string {
	in := implementReviewUserInput{Handoff: g.pending, DecisionsPath: existingPath(g.decisionsPath())}
	if in.Handoff == nil && in.DecisionsPath == "" {
		return ""
	}
	return prompts.MustRender(prompts.TemplateImplementReviewUser, in)
}

// implementReviewUserInput is the data passed to implement_review.user.tmpl.
type implementReviewUserInput struct {
	Handoff       *reviewHandoffPromptInput
	DecisionsPath string
}

func gatePending(g *phaseReviewGate) *reviewHandoffPromptInput {
	if g == nil {
		return nil
	}
	return g.pending
}
