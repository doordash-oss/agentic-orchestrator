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
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// testRepoNameWorker is a fixture repo-name literal reused across this
// file's freshness-suffix tests.
const testRepoNameWorker = "worker"

// reposBlockFreshnessLocalChanges, reposBlockFreshnessInSync, and
// reposBlockFreshnessLocalOnly are fixture RepoState.Freshness values reused
// across this file's freshness-suffix tests.
const (
	reposBlockFreshnessLocalChanges = "local changes"
	reposBlockFreshnessInSync       = "in sync"
	reposBlockFreshnessLocalOnly    = "local only"
)

// stripANSI removes ANSI colour escape sequences so assertions can match the
// plain text payload of a rendered row.
func stripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}

func TestRenderReposBlockTouchedMatrix(t *testing.T) {
	t.Parallel()
	type row struct {
		repo     string
		contains []string // substrings expected on the row (or its continuation)
		absent   []string // substrings that must not appear on the row
	}

	tests := []struct {
		name   string
		f      *feature.Feature
		expect []row
	}{
		{
			name: "untouched pre-review-passed renders unpublished",
			f: &feature.Feature{
				Status: feature.StatusImplementing,
				Repos:  []feature.FeatureRepo{{Name: "alpha"}, {Name: "beta"}},
				RepoStates: map[string]*feature.RepoState{
					"alpha": {},
					"beta":  {},
				},
			},
			expect: []row{
				{repo: "alpha", contains: []string{"alpha", "unpublished"}, absent: []string{"skipped"}},
				{repo: "beta", contains: []string{"beta", "unpublished"}, absent: []string{"skipped"}},
			},
		},
		{
			name: "untouched post-review-passed renders skipped",
			f: &feature.Feature{
				Status: feature.StatusReviewPassed,
				Repos:  []feature.FeatureRepo{{Name: "alpha"}, {Name: "beta"}},
				RepoStates: map[string]*feature.RepoState{
					"alpha": {},
					"beta":  {},
				},
			},
			expect: []row{
				{repo: "alpha", contains: []string{"alpha", "skipped"}},
				{repo: "beta", contains: []string{"beta", "skipped"}},
			},
		},
		{
			name: "touched without PR URL renders unpublished",
			f: &feature.Feature{
				Status: feature.StatusImplementing,
				Repos:  []feature.FeatureRepo{{Name: "alpha"}},
				RepoStates: map[string]*feature.RepoState{
					"alpha": {Touched: true},
				},
			},
			expect: []row{
				{repo: "alpha", contains: []string{"alpha", "unpublished"}},
			},
		},
		{
			name: "touched with PR URL renders the URL",
			f: &feature.Feature{
				Status: feature.StatusPublished,
				Repos:  []feature.FeatureRepo{{Name: "alpha"}},
				RepoStates: map[string]*feature.RepoState{
					"alpha": {Touched: true, PRURL: "https://github.com/org/alpha/pull/1"},
				},
			},
			expect: []row{
				{repo: "alpha", contains: []string{"alpha", "https://github.com/org/alpha/pull/1"}, absent: []string{"unpublished", "skipped"}},
			},
		},
		{
			name: "touched with last error renders failed marker plus continuation",
			f: &feature.Feature{
				Status: feature.StatusFailed,
				Repos:  []feature.FeatureRepo{{Name: "alpha"}},
				RepoStates: map[string]*feature.RepoState{
					"alpha": {Touched: true, LastError: "rebase blew up"},
				},
			},
			expect: []row{
				{repo: "alpha", contains: []string{"alpha", "✗ failed"}},
				{contains: []string{"rebase blew up"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := renderReposBlock(tt.f)
			if len(rows) != len(tt.expect) {
				t.Fatalf("expected %d rows, got %d: %q", len(tt.expect), len(rows), rows)
			}
			for i, want := range tt.expect {
				plain := stripANSI(rows[i])
				for _, sub := range want.contains {
					if !strings.Contains(plain, sub) {
						t.Errorf("row %d %q: expected substring %q", i, plain, sub)
					}
				}
				for _, sub := range want.absent {
					if strings.Contains(plain, sub) {
						t.Errorf("row %d %q: did not expect substring %q", i, plain, sub)
					}
				}
			}
		})
	}
}

func TestRenderReposBlockCycleSuffix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		cycleType feature.RepoCycleType
		status    string
		lastErr   string
		want      []string
		absent    []string
	}{
		{
			name:      "rebase running",
			cycleType: feature.CycleRebase,
			status:    feature.RepoCycleRunning,
			want:      []string{"⟳", "rebasing"},
		},
		{
			name:      "tweak running",
			cycleType: feature.CycleTweak,
			status:    feature.RepoCycleRunning,
			want:      []string{"⟳", "tweaking"},
		},
		{
			name:      "refactor running",
			cycleType: feature.CycleRefactor,
			status:    feature.RepoCycleRunning,
			want:      []string{"⟳", "refactoring"},
		},
		{
			name:      "review-comments running",
			cycleType: feature.CycleReviewComments,
			status:    feature.RepoCycleRunning,
			want:      []string{"⟳", "applying review comments"},
		},
		{
			name:      "rebase failed",
			cycleType: feature.CycleRebase,
			status:    feature.RepoCycleFailed,
			lastErr:   "merge conflict",
			want:      []string{"✗", "rebase failed", "merge conflict"},
		},
		{
			name:      "tweak needs input",
			cycleType: feature.CycleTweak,
			status:    feature.RepoCycleNeedUserInput,
			want:      []string{"⚠", "tweak needs input"},
		},
		{
			name:      "refactor needs input",
			cycleType: feature.CycleRefactor,
			status:    feature.RepoCycleNeedUserInput,
			want:      []string{"⚠", "refactor needs input"},
		},
	}

	const prURL = "https://github.com/org/alpha/pull/42"

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &feature.Feature{
				Status: feature.StatusPublished,
				Repos:  []feature.FeatureRepo{{Name: "alpha"}},
				RepoStates: map[string]*feature.RepoState{
					"alpha": {Touched: true, PRURL: prURL},
				},
				RepoCycles: map[string]*feature.RepoCycleState{
					"alpha": {Type: tt.cycleType, Status: tt.status, LastError: tt.lastErr},
				},
			}
			rows := renderReposBlock(f)
			if len(rows) == 0 {
				t.Fatal("expected at least one row")
			}
			plain := stripANSI(rows[0])
			// PR URL must remain visible — cycle suffix is appended, not a replacement.
			if !strings.Contains(plain, prURL) {
				t.Errorf("expected PR URL to remain visible alongside cycle suffix; got %q", plain)
			}
			for _, sub := range tt.want {
				if !strings.Contains(plain, sub) {
					t.Errorf("expected substring %q in %q", sub, plain)
				}
			}
			for _, sub := range tt.absent {
				if strings.Contains(plain, sub) {
					t.Errorf("did not expect substring %q in %q", sub, plain)
				}
			}
		})
	}
}

func TestRenderReposBlockFreshnessSuffix(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		Status: feature.StatusCodeReady,
		Repos:  []feature.FeatureRepo{{Name: testRepoNameAPI}, {Name: testRepoNameWeb}, {Name: testRepoNameWorker}},
		RepoStates: map[string]*feature.RepoState{
			testRepoNameAPI:    {Touched: true, Freshness: reposBlockFreshnessLocalChanges},
			testRepoNameWeb:    {Touched: true, Freshness: reposBlockFreshnessInSync},
			testRepoNameWorker: {Touched: true, Freshness: reposBlockFreshnessLocalOnly},
		},
	}

	rows := renderReposBlock(f)
	joined := stripANSI(strings.Join(rows, "\n"))
	for _, want := range []string{testRepoNameAPI, reposBlockFreshnessLocalChanges, testRepoNameWeb, reposBlockFreshnessInSync, testRepoNameWorker, reposBlockFreshnessLocalOnly} {
		if !strings.Contains(joined, want) {
			t.Fatalf("renderReposBlock() missing %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "unknown") {
		t.Fatalf("renderReposBlock() should not render unknown freshness:\n%s", joined)
	}
}

func TestRenderReposBlockRebaseOperationSuffix(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		Status: feature.StatusCodeReady,
		Repos:  []feature.FeatureRepo{{Name: testRepoNameAPI}, {Name: testRepoNameWeb}},
		RepoStates: map[string]*feature.RepoState{
			testRepoNameAPI: {Touched: true, Freshness: reposBlockFreshnessLocalChanges},
			testRepoNameWeb: {Touched: true, Freshness: reposBlockFreshnessInSync},
		},
		RebaseOperation: &feature.RebaseOperationState{
			Stage: feature.RebaseStageSmartRebase,
			Repos: map[string]*feature.RebaseRepoProgress{
				testRepoNameAPI: {Status: feature.RebaseRepoStatusConflict, ConflictFiles: []string{"service.go"}},
				testRepoNameWeb: {Status: feature.RebaseRepoStatusUpToDate},
			},
		},
	}

	rows := renderReposBlock(f)
	joined := stripANSI(strings.Join(rows, "\n"))
	for _, want := range []string{testRepoNameAPI, "conflict: service.go", reposBlockFreshnessLocalChanges, testRepoNameWeb, reposBlockFreshnessInSync} {
		if !strings.Contains(joined, want) {
			t.Fatalf("renderReposBlock() missing %q in:\n%s", want, joined)
		}
	}
	if strings.Count(joined, testRepoNameWeb) != 1 || strings.Count(joined, reposBlockFreshnessInSync) != 1 {
		t.Fatalf("renderReposBlock() should not duplicate up-to-date freshness:\n%s", joined)
	}
}

func TestRenderReposBlockPreImplementation(t *testing.T) {
	t.Parallel()
	preImplStatuses := []feature.Status{
		feature.StatusInquiring,
		feature.StatusResearching,
		feature.StatusDesigning,
		feature.StatusPlanning,
	}
	for _, s := range preImplStatuses {
		t.Run(s.String(), func(t *testing.T) {
			f := &feature.Feature{
				Status: s,
				Repos:  []feature.FeatureRepo{{Name: "alpha"}, {Name: "beta"}},
				// Even if RepoImpl somehow carries non-pending values during a
				// pre-implementation phase, the block must render uniformly as
				// `unpublished`.
				RepoStates: map[string]*feature.RepoState{
					"alpha": {Touched: true, PRURL: "https://example.com/x"},
					"beta":  {Touched: true, LastError: "leaked"},
				},
			}
			rows := renderReposBlock(f)
			if len(rows) != 2 {
				t.Fatalf("expected 2 rows, got %d", len(rows))
			}
			for i, row := range rows {
				plain := stripANSI(row)
				if !strings.Contains(plain, "unpublished") {
					t.Errorf("row %d %q: expected 'unpublished' for pre-implementation status %s", i, plain, s)
				}
				if strings.Contains(plain, "skipped") || strings.Contains(plain, "✗") {
					t.Errorf("row %d %q: pre-implementation row must not leak post-impl markers", i, plain)
				}
			}
		})
	}
}

func TestRenderReposBlockSingleAndMultiRepo(t *testing.T) {
	t.Parallel()
	t.Run("single repo renders one row", func(t *testing.T) {
		f := &feature.Feature{
			Status: feature.StatusImplementing,
			Repos:  []feature.FeatureRepo{{Name: "solo"}},
			RepoStates: map[string]*feature.RepoState{
				"solo": {},
			},
		}
		rows := renderReposBlock(f)
		if len(rows) != 1 {
			t.Fatalf("expected 1 row for single-repo feature, got %d", len(rows))
		}
		if !strings.Contains(stripANSI(rows[0]), "solo") {
			t.Errorf("expected row to contain repo name; got %q", rows[0])
		}
	})

	t.Run("multi repo sorted by name", func(t *testing.T) {
		f := &feature.Feature{
			Status: feature.StatusImplementing,
			Repos:  []feature.FeatureRepo{{Name: "zeta"}, {Name: "alpha"}, {Name: "mu"}},
			RepoStates: map[string]*feature.RepoState{
				"alpha": {},
				"mu":    {},
				"zeta":  {},
			},
		}
		rows := renderReposBlock(f)
		if len(rows) != 3 {
			t.Fatalf("expected 3 rows, got %d", len(rows))
		}
		want := []string{"alpha", "mu", "zeta"}
		for i, name := range want {
			if !strings.Contains(stripANSI(rows[i]), name) {
				t.Errorf("row %d: expected %q in %q", i, name, rows[i])
			}
		}
	})
}

func TestRenderReposBlockLongRepoNameStaysOnOneLine(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		Status: feature.StatusBuildingKB,
		Repos:  []feature.FeatureRepo{{Name: "chessplaytest"}},
		RepoStates: map[string]*feature.RepoState{
			"chessplaytest": {},
		},
	}

	rows := renderReposBlock(f)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d: %q", len(rows), rows)
	}
	plain := stripANSI(rows[0])
	if strings.Contains(plain, "\n") {
		t.Fatalf("repo row wrapped before panel rendering: %q", plain)
	}
	if !strings.Contains(plain, "chessplaytest  unpublished") {
		t.Fatalf("repo row = %q, want repo name and status on one line", plain)
	}
}

func TestRenderReposBlockTruncation(t *testing.T) {
	t.Parallel()
	t.Run("long error truncates to 60-char continuation", func(t *testing.T) {
		longErr := strings.Repeat("x", 200)
		f := &feature.Feature{
			Status: feature.StatusFailed,
			Repos:  []feature.FeatureRepo{{Name: "alpha"}},
			RepoStates: map[string]*feature.RepoState{
				"alpha": {Touched: true, LastError: longErr},
			},
		}
		rows := renderReposBlock(f)
		if len(rows) != 2 {
			t.Fatalf("expected 2 rows (label + continuation), got %d", len(rows))
		}
		cont := stripANSI(rows[1])
		// truncateText replaces the final char with ellipsis, so the result
		// length stays at maxLen rather than maxLen-1. Allow a little slack
		// for any leading whitespace baked into the continuation.
		trimmed := strings.TrimSpace(cont)
		if len([]rune(trimmed)) > 60 {
			t.Errorf("expected continuation truncated to <=60 runes, got %d (%q)", len([]rune(trimmed)), trimmed)
		}
		if !strings.HasSuffix(trimmed, "…") {
			t.Errorf("expected trailing ellipsis on truncated error; got %q", trimmed)
		}
	})

	t.Run("long PR URL renders unmodified — terminal handles overflow", func(t *testing.T) {
		// Lipgloss style is colour-only, so the renderer must not add custom
		// truncation on PR URLs. The terminal/lipgloss panel handles overflow.
		longURL := "https://github.com/org/alpha/pull/" + strings.Repeat("9", 200)
		f := &feature.Feature{
			Status: feature.StatusPublished,
			Repos:  []feature.FeatureRepo{{Name: "alpha"}},
			RepoStates: map[string]*feature.RepoState{
				"alpha": {Touched: true, PRURL: longURL},
			},
		}
		rows := renderReposBlock(f)
		if len(rows) != 1 {
			t.Fatalf("expected 1 row, got %d", len(rows))
		}
		if !strings.Contains(stripANSI(rows[0]), longURL) {
			t.Errorf("expected long PR URL preserved verbatim — terminal owns overflow; got %q", rows[0])
		}
	})
}

func TestRenderReposBlockNilOrEmpty(t *testing.T) {
	t.Parallel()
	t.Run("nil feature returns nil", func(t *testing.T) {
		if rows := renderReposBlock(nil); rows != nil {
			t.Errorf("expected nil rows for nil feature, got %v", rows)
		}
	})

	t.Run("empty repos returns nil", func(t *testing.T) {
		f := &feature.Feature{Status: feature.StatusImplementing}
		if rows := renderReposBlock(f); rows != nil {
			t.Errorf("expected nil rows for feature with no repos, got %v", rows)
		}
	})

	t.Run("missing RepoImpl entry treats repo as untouched", func(t *testing.T) {
		f := &feature.Feature{
			Status: feature.StatusImplementing,
			Repos:  []feature.FeatureRepo{{Name: "alpha"}},
			// RepoImpl is nil — derivation must not panic and must default to
			// untouched/unpublished.
		}
		rows := renderReposBlock(f)
		if len(rows) != 1 {
			t.Fatalf("expected 1 row, got %d", len(rows))
		}
		if !strings.Contains(stripANSI(rows[0]), "unpublished") {
			t.Errorf("expected 'unpublished' for missing RepoImpl entry; got %q", rows[0])
		}
	})
}

func TestRenderMetadataIncludesReposBlock(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "f1",
		Slug:         "multi-repo",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Repos:        []feature.FeatureRepo{{Name: "alpha"}, {Name: "beta"}},
		RepoStates: map[string]*feature.RepoState{
			"alpha": {Touched: true, PRURL: "https://github.com/org/alpha/pull/9"},
			"beta":  {},
		},
	}
	m := NewDetailModel(f, "")

	compact := stripANSI(m.renderMetadataCompact(f))
	if !strings.Contains(compact, "Repo Status") {
		t.Error("expected 'Repo Status' label in renderMetadataCompact")
	}
	if !strings.Contains(compact, "https://github.com/org/alpha/pull/9") {
		t.Error("expected PR URL row in compact metadata")
	}
	if !strings.Contains(compact, "unpublished") {
		t.Error("expected 'unpublished' row in compact metadata")
	}
}
