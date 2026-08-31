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

package feature_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestLoadReviewFeedbackOutcomesMissingFile(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	f := &feature.Feature{ID: "child-1", ActiveRun: 1}
	outcomes := feature.LoadReviewFeedbackOutcomes(stateDir, f)
	if len(outcomes) != 0 {
		t.Fatalf("missing file: outcomes = %v, want empty map", outcomes)
	}
}

func TestLoadReviewFeedbackOutcomesMalformedJSON(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	f := &feature.Feature{ID: "child-1", ActiveRun: 1}
	path := feature.ReviewFeedbackOutcomesPath(stateDir, f)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	outcomes := feature.LoadReviewFeedbackOutcomes(stateDir, f)
	if len(outcomes) != 0 {
		t.Fatalf("malformed JSON: outcomes = %v, want empty map", outcomes)
	}
}

func TestLoadReviewFeedbackOutcomesValidFile(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	f := &feature.Feature{ID: "child-1", ActiveRun: 1}
	path := feature.ReviewFeedbackOutcomesPath(stateDir, f)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	entries := []feature.ReviewFeedbackOutcome{
		{ID: 11, Disposition: feature.ReviewFeedbackOutcomeDispositionAddressed, Explanation: "fixed the handler"},
		{ID: 21, Disposition: feature.ReviewFeedbackOutcomeDispositionDismissed, Explanation: "not applicable"},
	}
	data, _ := json.Marshal(entries)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	outcomes := feature.LoadReviewFeedbackOutcomes(stateDir, f)
	if len(outcomes) != 2 {
		t.Fatalf("outcomes = %v, want 2 entries", outcomes)
	}
	if outcomes[11].Disposition != feature.ReviewFeedbackOutcomeDispositionAddressed {
		t.Errorf("outcome[11].Disposition = %q, want %q", outcomes[11].Disposition, feature.ReviewFeedbackOutcomeDispositionAddressed)
	}
	if outcomes[21].Disposition != feature.ReviewFeedbackOutcomeDispositionDismissed {
		t.Errorf("outcome[21].Disposition = %q, want %q", outcomes[21].Disposition, feature.ReviewFeedbackOutcomeDispositionDismissed)
	}
}

func TestLoadReviewFeedbackOutcomesPartialEntries(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	f := &feature.Feature{ID: "child-1", ActiveRun: 1}
	path := feature.ReviewFeedbackOutcomesPath(stateDir, f)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	entries := []feature.ReviewFeedbackOutcome{
		{ID: 11, Disposition: feature.ReviewFeedbackOutcomeDispositionAddressed, Explanation: "fixed"},
	}
	data, _ := json.Marshal(entries)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	outcomes := feature.LoadReviewFeedbackOutcomes(stateDir, f)
	if len(outcomes) != 1 {
		t.Fatalf("outcomes = %v, want 1 entry (partial)", outcomes)
	}
	if _, ok := outcomes[99]; ok {
		t.Errorf("outcomes has entry for ID 99, want missing (partial)")
	}
}

func TestReviewFeedbackReplyBody(t *testing.T) {
	t.Parallel()
	mergeSHA := "abc123"
	tests := []struct {
		name     string
		outcome  feature.ReviewFeedbackOutcome
		ok       bool
		mergeSHA string
		want     string
	}{
		{
			name:    "addressed with explanation",
			outcome: feature.ReviewFeedbackOutcome{Disposition: feature.ReviewFeedbackOutcomeDispositionAddressed, Explanation: "refactored the handler"},
			ok:      true,
			want:    "Addressed in `abc123` — refactored the handler",
		},
		{
			name:    "addressed without explanation",
			outcome: feature.ReviewFeedbackOutcome{Disposition: feature.ReviewFeedbackOutcomeDispositionAddressed, Explanation: ""},
			ok:      true,
			want:    "Addressed in `abc123`",
		},
		{
			name:    "dismissed",
			outcome: feature.ReviewFeedbackOutcome{Disposition: feature.ReviewFeedbackOutcomeDispositionDismissed, Explanation: "not applicable here"},
			ok:      true,
			want:    "Dismissed: not applicable here",
		},
		{
			name: "no outcome (fallback)",
			ok:   false,
			want: "Addressed in `abc123`",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := feature.ReviewFeedbackReplyBody(tt.outcome, tt.ok, mergeSHA)
			if got != tt.want {
				t.Errorf("ReviewFeedbackReplyBody() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReviewFeedbackExitCriteriaDeterministic(t *testing.T) {
	t.Parallel()
	comments := []feature.ReviewFeedbackComment{
		{Repo: "api", ID: 101, Type: "review", Path: "handler.go", Line: 42},
		{Repo: "web", ID: 202, Type: "issue"},
	}
	heads := map[string]string{
		"/wt/api": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"/wt/web": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	mgr := newChildTestManager(t, heads, cleanEverywhere())

	// Two identical parents with the same comments should produce
	// byte-identical exit criteria.
	for _, pid := range []string{"parent-det-1", "parent-det-2"} {
		parent := &feature.Feature{
			ID:       pid,
			Name:     "Parent",
			Slug:     "parent",
			Status:   feature.StatusPublished,
			Pipeline: feature.PipelineMoonshot,
			Repos: []feature.FeatureRepo{
				{Name: "api", Path: "/src/api", WorktreePath: "/wt/api", Branch: "feature/p", BaseBranch: "main"},
				{Name: "web", Path: "/src/web", WorktreePath: "/wt/web", Branch: "feature/p", BaseBranch: "main"},
			},
			Models:       config.ModelConfig{Planning: "m"},
			Effort:       config.EffortConfig{Planning: "high"},
			RiskLevel:    feature.RiskHigh,
			ExitCriteria: "parent exit",
			Inquireness:  feature.InquirenessHigh,
			Checkpoints:  feature.Checkpoints{RoadmapReview: true, ManualPublish: true},
			RepoStates: map[string]*feature.RepoState{
				"api": {PRURL: "https://github.example/acme/api/pull/1"},
				"web": {PRURL: "https://github.example/acme/web/pull/2"},
			},
		}
		saveChildTestParent(t, mgr, parent)
	}

	gate := true
	child1, err := mgr.CreateReviewFeedbackChild("parent-det-1", feature.ReviewFeedbackChildSpec{
		Comments: comments, GateEnabled: &gate,
	})
	if err != nil {
		t.Fatalf("first CreateReviewFeedbackChild: %v", err)
	}
	child2, err := mgr.CreateReviewFeedbackChild("parent-det-2", feature.ReviewFeedbackChildSpec{
		Comments: comments, GateEnabled: &gate,
	})
	if err != nil {
		t.Fatalf("second CreateReviewFeedbackChild: %v", err)
	}
	if child1.ExitCriteria != child2.ExitCriteria {
		t.Errorf("exit criteria not deterministic:\nfirst:\n%s\nsecond:\n%s", child1.ExitCriteria, child2.ExitCriteria)
	}
	if child1.ExitCriteria == "parent exit" {
		t.Errorf("exit criteria inherited parent's; want deterministic outcomes contract")
	}
}
