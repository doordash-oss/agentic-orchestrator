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
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// freshnessInSync and freshnessLocalOnly are RepoState.Freshness values not
// already exported by internal/git (which only exports FreshnessUnknown and
// FreshnessLocalChanges).
const (
	freshnessInSync    = "in sync"
	freshnessLocalOnly = "local only"
)

// rebaseFailedLabel is the display label for a repo whose rebase operation
// ended in RebaseRepoStatusFailed.
const rebaseFailedLabel = "rebase failed"

// renderReposBlock returns one rendered row per repo in the feature, sorted by
// repo name. Each row's tail conveys the per-repo publishing surface:
// `unpublished`, `skipped`, a published PR URL, or a `✗ failed` marker
// followed by an indented continuation line carrying the truncated error.
//
// When a per-repo cycle is active (rebase / refactor / review-comments)
// a suffix is appended to the row — the PR URL stays visible.
//
// Pure: no I/O, no terminal coupling. The caller owns line joining and
// terminal-driven overflow truncation (lipgloss styles are colour-only and do
// not enforce width).
func renderReposBlock(f *feature.Feature) []string {
	if f == nil || len(f.Repos) == 0 {
		return nil
	}

	names := make([]string, len(f.Repos))
	for i, r := range f.Repos {
		names[i] = r.Name
	}
	sort.Strings(names)
	repoNameWidth := 12
	for _, name := range names {
		repoNameWidth = max(repoNameWidth, lipgloss.Width(name))
	}

	preImpl := isPreImplementationStatus(f.Status)
	postReview := isPostReviewPassedStatus(f.Status)

	rows := make([]string, 0, len(names))
	for _, name := range names {
		touched, prURL, lastErr, freshness := derivePerRepoView(f, name)

		// Pre-implementation features uniformly render `unpublished` —
		// nothing has been scoped onto repos yet.
		var tail string
		var errCont string
		switch {
		case preImpl:
			tail = MutedStyle.Render("unpublished")
		case !touched && postReview:
			tail = MutedStyle.Render("skipped")
		case !touched:
			tail = MutedStyle.Render("unpublished")
		case lastErr != "":
			tail = ErrorStyle.Render("✗ failed")
			errCont = "    " + ErrorStyle.Render(truncateText(lastErr, 60))
		case prURL != "":
			tail = SuccessStyle.Render(prURL)
		default:
			tail = MutedStyle.Render("unpublished")
		}

		// Cycle suffix is appended (not a replacement) so a published PR URL
		// stays visible while a rebase/refactor/review-comments runs.
		rebaseSuffix := ""
		if !preImpl {
			if suffix := rebaseOperationSuffix(f, name); suffix != "" {
				rebaseSuffix = suffix
				tail = strings.TrimRight(tail, " ") + "  " + rebaseSuffix
			} else if suffix := cycleSuffix(f, name); suffix != "" {
				tail = strings.TrimRight(tail, " ") + "  " + suffix
			}
		}
		if suffix := freshnessSuffix(freshness); suffix != "" &&
			(rebaseSuffix == "" || strings.TrimSpace(freshness) != freshnessInSync) {
			tail = strings.TrimRight(tail, " ") + "  " + suffix
		}

		row := LabelStyle.Width(repoNameWidth).Render(name) + "  " + tail
		rows = append(rows, row)
		if errCont != "" {
			rows = append(rows, errCont)
		}
	}
	return rows
}

func derivePerRepoView(f *feature.Feature, name string) (touched bool, prURL string, lastErr string, freshness string) {
	state, ok := f.RepoStates[name]
	if !ok || state == nil {
		return false, "", "", ""
	}
	return state.Touched, state.PRURL, state.LastError, state.Freshness
}

func rebaseOperationSuffix(f *feature.Feature, name string) string {
	if f == nil || f.RebaseOperation == nil {
		return ""
	}
	progress := f.RebaseOperation.Repos[name]
	if progress == nil {
		return ""
	}
	switch progress.Status {
	case feature.RebaseRepoStatusChecking:
		return MutedStyle.Render("⟳ checking")
	case feature.RebaseRepoStatusRebasing:
		return MutedStyle.Render("⟳ rebasing")
	case feature.RebaseRepoStatusUpToDate:
		return SuccessStyle.Render("✓ in sync")
	case feature.RebaseRepoStatusChanged:
		return SuccessStyle.Render("✓ rebased")
	case feature.RebaseRepoStatusConflict:
		label := "conflict"
		if len(progress.ConflictFiles) > 0 {
			label += ": " + truncateText(strings.Join(progress.ConflictFiles, ", "), 60)
		}
		return WarningStyle.Render("⚠ " + label)
	case feature.RebaseRepoStatusFailed:
		label := rebaseFailedLabel
		if progress.LastError != "" {
			label += ": " + truncateText(progress.LastError, 60)
		}
		return ErrorStyle.Render("✗ " + label)
	default:
		return ""
	}
}

func freshnessSuffix(freshness string) string {
	switch strings.TrimSpace(freshness) {
	case "", git.FreshnessUnknown:
		return ""
	case "in sync":
		return SuccessStyle.Render("✓ in sync")
	case git.FreshnessLocalChanges:
		return WarningStyle.Render("local changes")
	case freshnessLocalOnly:
		return MutedStyle.Render("local only")
	default:
		return MutedStyle.Render(freshness)
	}
}

// cycleSuffix returns a styled trailing annotation when the repo has an
// active post-publish cycle entry. Empty when no cycle is active.
func cycleSuffix(f *feature.Feature, name string) string {
	rc, ok := f.RepoCycles[name]
	if !ok || rc == nil {
		return ""
	}
	switch rc.Status {
	case feature.RepoCycleRunning:
		return MutedStyle.Render("⟳ " + cycleRunningLabel(rc.Type))
	case feature.RepoCycleNeedUserInput:
		return WarningStyle.Render("⚠ " + string(rc.Type) + " needs input")
	case feature.RepoCycleInterrupted:
		return WarningStyle.Render("⏸ " + string(rc.Type) + " interrupted — [r] restart")
	case feature.RepoCycleFailed:
		base := ErrorStyle.Render("✗ " + string(rc.Type) + " failed")
		if rc.LastError != "" {
			return base + ErrorStyle.Render(": "+truncateText(rc.LastError, 60))
		}
		return base
	}
	return ""
}

// cycleRunningLabel maps cycle types to the gerund label used inline.
func cycleRunningLabel(t feature.RepoCycleType) string {
	switch t {
	case feature.CycleRebase:
		return "rebasing"
	case feature.CycleRefactor:
		return "refactoring"
	case feature.CycleReviewComments:
		return "applying review comments"
	default:
		return string(t)
	}
}

// isPreImplementationStatus returns true for feature statuses where no repo
// has been scoped for implementation yet. Slice spec: "Pre-implementation
// feature (Inquiring/Researching/Designing/Planning) → all rows
// uniformly render `unpublished`".
func isPreImplementationStatus(s feature.Status) bool {
	switch s {
	case feature.StatusInquiring,
		feature.StatusResearching,
		feature.StatusDesigning,
		feature.StatusPlanning,
		feature.StatusInquireReady,
		feature.StatusDesignReady,
		feature.StatusPlanReady,
		feature.StatusImplementReady,
		feature.StatusBuildingKB,
		feature.StatusCreated,
		feature.StatusPromptNeedsReview,
		feature.StatusInquiryNeedsReview,
		feature.StatusResearchNeedsReview,
		feature.StatusDesignNeedsReview,
		feature.StatusPlanNeedsReview:
		return true
	}
	return false
}

// isPostReviewPassedStatus returns true once the feature has reached the
// state where untouched repos should render as `skipped` rather than
// `unpublished`. The Status enum is iota-ordered but later additions broke
// numeric ordering, so this is an explicit set rather than a `>=` compare.
func isPostReviewPassedStatus(s feature.Status) bool {
	switch s {
	case feature.StatusReviewPassed,
		feature.StatusFinalReviewing,
		feature.StatusCodeReady,
		feature.StatusPublished,
		feature.StatusDone:
		return true
	}
	return false
}
