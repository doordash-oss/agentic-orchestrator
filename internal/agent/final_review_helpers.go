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

// Package agent — final_review_helpers.go owns the shared helpers used by
// the unified feature-level Final Review loop (final_review_loop.go) and
// the post-cycle Final Review entry (also in final_review_loop.go for
// post-publish tweak/rebase/review-comments cycles).
//
// The prompt builders, verification-context resolver, and seed-source helpers
// stay here because both the feature-level Final Review and the feature-level
// post-cycle Final Review share them.
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
	"github.com/doordash-oss/agentic-orchestrator/internal/agent/roles"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// FinalReviewPromptOpts contains the parameters for building the final review prompt.
type FinalReviewPromptOpts struct {
	FeatureDescription string
	ExitCriteria       string
	DiffBase           string // branch to diff against (e.g., "main")
	WorkDir            string // repo working directory
	PreviousFeedback   string // feedback from prior review iteration (empty on first)
	Iteration          int
	RoadmapPath        string // path to the roadmap file (reviewer reads it via tool access)
	DesignArtifactPath string // retained for caller compatibility; no longer re-injected
	Images             []string
	PhaseType          string // "tracer-bullet", "tdd-fill-in", "collapsed", or ""
	CycleFocus         string // short description of the active cycle's review scope
	FeedbackPath       string // path where the reviewer must write its feedback file
	Publishable        bool   // whether the repo has a remote / is publishable

	PriorImplementationReportPaths       []string
	PriorImplementationEvidenceRootDirs  []string
	PriorImplementationEvidenceArtifacts []string
}

// FinalFixPromptOpts contains the parameters for building the fix agent prompt.
type FinalFixPromptOpts struct {
	Feedback               string
	FeedbackPath           string
	ExitCriteria           string
	IterDir                string // for phase_complete and verification report references
	VerificationReportPath string
	Iteration              int
	Publishable            bool
	DesignArtifactPath     string   // retained for caller compatibility; no longer re-injected
	Images                 []string // user-attached visual references, re-injected per iteration
}

// BuildFinalReviewPrompt constructs the prompt for the Final Review session.
// Unlike BuildReviewPrompt (print-mode, audits verification report), this
// produces a prompt for an interactive session that can explore the codebase,
// run tests/build, and read the full diff.
//
// The prose lives in internal/agent/prompts/templates/final_review.user.tmpl.
func BuildFinalReviewPrompt(opts FinalReviewPromptOpts) string {
	return roles.BuildFinalReviewPrompt(roles.FinalReviewUserInput{
		VisualReferences: prompts.VisualReferencesInput{
			Images: opts.Images,
			Label:  "conducting this final review",
		},
		Iteration:                            opts.Iteration,
		IsCycleReview:                        opts.CycleFocus != "",
		PhaseType:                            opts.PhaseType,
		DiffBase:                             opts.DiffBase,
		RoadmapPath:                          opts.RoadmapPath,
		DesignArtifactPath:                   opts.DesignArtifactPath,
		FeatureDescription:                   opts.FeatureDescription,
		ExitCriteria:                         opts.ExitCriteria,
		CycleFocus:                           opts.CycleFocus,
		PriorImplementationReportPaths:       opts.PriorImplementationReportPaths,
		PriorImplementationEvidenceRootDirs:  opts.PriorImplementationEvidenceRootDirs,
		PriorImplementationEvidenceArtifacts: opts.PriorImplementationEvidenceArtifacts,
		FeedbackPath:                         opts.FeedbackPath,
		Publishable:                          opts.Publishable,
		PreviousFeedback:                     opts.PreviousFeedback,
	})
}

// BuildFinalFixPrompt constructs the prompt for the fix agent session.
// The fix agent addresses specific review feedback without adding new features.
//
// The prose lives in internal/agent/prompts/templates/final_fix.user.tmpl
// (with the manual-verification-outcomes partial branched on a flag).
func BuildFinalFixPrompt(opts FinalFixPromptOpts) string {
	return roles.BuildFinalFixPrompt(roles.FinalFixUserInput{
		VisualReferences: prompts.VisualReferencesInput{
			Images: opts.Images,
			Label:  "applying this fix",
		},
		Iteration:                         opts.Iteration,
		ExitCriteria:                      opts.ExitCriteria,
		Feedback:                          opts.Feedback,
		FeedbackPath:                      opts.FeedbackPath,
		VerificationReportPath:            opts.VerificationReportPath,
		IncludeManualVerificationOutcomes: feedbackMentionsManualVerification(opts.Feedback),
		Publishable:                       opts.Publishable,
	})
}

// feedbackMentionsManualVerification returns true if the reviewer feedback
// references a manual-verification gate. Used to decide whether to inject
// the outcomes guidance into the fix prompt. Phrasings observed in real
// feedback: "manual verification", "manual-verification bullets", "manual
// bullet(s)", "manual evidence", "unattested manual".
func feedbackMentionsManualVerification(feedback string) bool {
	lower := strings.ToLower(feedback)
	needles := []string{
		"manual verification",
		"manual-verification",
		"manual bullet",
		"manual evidence",
		"unattested manual",
	}
	for _, needle := range needles {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

// manualVerificationOutcomesGuidance returns the fix-prompt section that
// teaches the fix agent the two valid YAML outcomes for a manual-verification
// bullet.
//
// Kept as a thin wrapper for tests that exercise the section in isolation;
// the prose itself lives in
// internal/agent/prompts/partials/manual_verification_outcomes.tmpl.
func manualVerificationOutcomesGuidance() string {
	return prompts.MustRender("manual_verification_outcomes", nil)
}

// guidelineAdditionalDirs returns a single-element slice containing dir when
// non-empty, or nil otherwise. Used to conditionally add the guidelines
// directory to AdditionalDirs.
func guidelineAdditionalDirs(dir string) []string {
	if dir == "" {
		return nil
	}
	return []string{dir}
}

// writeAggregateVerificationStub preserves the legacy non-contract final
// review behavior used by post-publish review flows that lack a phase or
// cycle testing-contract.yaml (e.g., post-tweak FR for a feature with no
// active roadmap phase).
func writeAggregateVerificationStub(path string) {
	content := `# Verification Report — Final Review Placeholder
#
# This is a placeholder. The Final Review session has full tool access
# and runs verification (tests, build, lint) directly rather than
# consuming aggregated reports from prior phases.
#
# The reviewer should:
# 1. Run the project's test suite
# 2. Run the build
# 3. Run any linting/vet checks
# 4. Verify exit criteria are met
status: pending_interactive_review
note: "The interactive reviewer runs verification directly via tool access"
`
	_ = os.WriteFile(path, []byte(content), 0o644)
}

// selectFinalReviewSeedSource picks the source file to seed this
// iteration's verification-report.yaml from. Prefers the prior iteration's
// review file (so review-time attestation accumulates across iterations);
// on iteration 1, falls back to the implementation phase's frozen report.
// Returns "" when neither is available.
func selectFinalReviewSeedSource(artifactDir, iterDir, implementationReportPath string) string {
	if prior := priorIterationFinalReviewReportPath(artifactDir, iterDir); prior != "" {
		return prior
	}
	if implementationReportPath != "" {
		if _, err := os.Stat(implementationReportPath); err == nil {
			return implementationReportPath
		}
	}
	return ""
}

// priorIterationFinalReviewReportPath returns the path to iter-(N-1)'s
// verification-report.yaml if it exists.
func priorIterationFinalReviewReportPath(artifactDir, iterDir string) string {
	var n int
	if _, err := fmt.Sscanf(filepath.Base(iterDir), "iteration-%02d", &n); err != nil || n <= 1 {
		return ""
	}
	prior := filepath.Join(artifactDir, fmt.Sprintf("iteration-%02d", n-1), "verification-report.yaml")
	if _, err := os.Stat(prior); err != nil {
		return ""
	}
	return prior
}

// copyFile copies src to dst, creating dst's parent directory if needed.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("reading seed source %s: %w", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("creating seed destination directory: %w", err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return fmt.Errorf("writing seed destination %s: %w", dst, err)
	}
	return nil
}

// latestImplementationVerificationReportPath returns the path to the most
// recent implementation verification report for a feature/repo, or "" if
// none exists.
func latestImplementationVerificationReportPath(stateDir string, f *feature.Feature, repoName string) string {
	if f == nil || f.CurrentRoadmapPhase <= 0 {
		return ""
	}
	implementDir := PhaseImplementDir(stateDir, f, f.CurrentRoadmapPhase)
	if repoName != "" {
		implementDir = filepath.Join(implementDir, repoName)
	}
	iteration := NewArtifactManager(implementDir).LatestIteration()
	if iteration == 0 {
		return ""
	}
	return filepath.Join(implementDir, fmt.Sprintf("iteration-%02d", iteration), "verification-report.yaml")
}

type priorImplementationEvidenceContext struct {
	ReportPaths           []string
	EvidenceRootDirs      []string
	EvidenceArtifactPaths []string
}

func priorImplementationEvidenceContextForRun(runDir string) priorImplementationEvidenceContext {
	var ctx priorImplementationEvidenceContext
	addImplementationDir := func(scopeDir string) {
		if reportPath := latestCompletedImplementationReportPath(filepath.Join(scopeDir, feature.PhaseImplement.DirName())); reportPath != "" {
			ctx.ReportPaths = append(ctx.ReportPaths, reportPath)
			ctx.EvidenceRootDirs = append(ctx.EvidenceRootDirs, filepath.Dir(reportPath))
			ctx.EvidenceArtifactPaths = appendEvidenceArtifactPaths(ctx.EvidenceArtifactPaths, reportPath)
		}
	}

	addImplementationDir(runDir)
	entries, err := os.ReadDir(runDir)
	if err != nil {
		return ctx
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "phase-") {
			continue
		}
		addImplementationDir(filepath.Join(runDir, entry.Name()))
	}
	return ctx
}

func appendEvidenceArtifactPaths(out []string, reportPath string) []string {
	report, err := ReadVerificationReport(reportPath)
	if err != nil {
		return out
	}
	seen := make(map[string]bool, len(out))
	for _, path := range out {
		seen[path] = true
	}
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		path := raw
		if !filepath.IsAbs(path) {
			path = filepath.Join(filepath.Dir(reportPath), path)
		}
		path = filepath.Clean(path)
		if seen[path] {
			return
		}
		seen[path] = true
		out = append(out, path)
	}
	for _, check := range allVerificationChecks(report) {
		add(check.EvidenceData.Primary)
		for _, attachment := range check.EvidenceData.Attachments {
			add(attachment)
		}
	}
	return out
}

func allVerificationChecks(report *VerificationReport) []VerificationCheckResult {
	if report == nil {
		return nil
	}
	checks := make([]VerificationCheckResult, 0, len(report.Results)+len(report.RequiredChecks)+len(report.AdditionalChecks))
	checks = append(checks, report.Results...)
	checks = append(checks, report.RequiredChecks...)
	checks = append(checks, report.AdditionalChecks...)
	return checks
}
