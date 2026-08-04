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
// the unified feature-level Final Review loop (final_review_loop.go).
//
// The prompt builders and prior-implementation-evidence resolver stay here
// because the feature-level Final Review loop is the single consumer.
package agent

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
	"github.com/doordash-oss/agentic-orchestrator/internal/agent/roles"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// FinalFixPromptOpts contains the parameters for building the fix agent prompt.
type FinalFixPromptOpts struct {
	Feedback           string
	FeedbackPath       string
	ExitCriteria       string
	AcceptanceClause   string
	Iteration          int
	Publishable        bool
	DesignArtifactPath string   // retained for caller compatibility; no longer re-injected
	Images             []string // user-attached visual references, re-injected per iteration
	// RefactorPassForkPoint resolves the spec's "fork point" references for a
	// refactor child ("repo @ sha"). Empty for top-level features.
	RefactorPassForkPoint string
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
		AcceptanceClause:                  opts.AcceptanceClause,
		Feedback:                          opts.Feedback,
		FeedbackPath:                      opts.FeedbackPath,
		IncludeManualVerificationOutcomes: feedbackMentionsManualVerification(opts.Feedback),
		Publishable:                       opts.Publishable,
		RefactorPassForkPoint:             opts.RefactorPassForkPoint,
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

// guidelineAdditionalDirs returns a single-element slice containing dir when
// non-empty, or nil otherwise. Used to conditionally add the guidelines
// directory to AdditionalDirs.
func guidelineAdditionalDirs(dir string) []string {
	if dir == "" {
		return nil
	}
	return []string{dir}
}

func finalReviewArtifactPath(stateDir string, f *feature.Feature, key string) string {
	if f == nil {
		return ""
	}
	path := f.Artifacts[key]
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(ActiveRunDir(stateDir, f), path)
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
