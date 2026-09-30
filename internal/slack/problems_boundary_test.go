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

package slack

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func TestScrubRedactsCompleteAuthorizationHeaderValues(t *testing.T) {
	for _, input := range []string{
		"Authorization: Token REVIEW_SENTINEL",
		"authorization=Custom REVIEW_SENTINEL",
		`Authorization: Digest username="operator", response="REVIEW_SENTINEL"`,
		`Authorization: Digest username="ops;bot", response="REVIEW_SENTINEL"`,
		`"Authorization": "Bearer REVIEW_SENTINEL"`,
		`{"Authorization":"Token REVIEW_SENTINEL","path":"/tmp/alpha"}`,
		`{"Authorization":["Bearer REVIEW_SENTINEL"],"path":"/tmp/alpha"}`,
	} {
		t.Run(input, func(t *testing.T) {
			got := scrub("", input)
			if strings.Contains(got, "REVIEW_SENTINEL") {
				t.Errorf("scrub(%q) = %q; credential remains", input, got)
			}
			if !strings.Contains(got, "[REDACTED]") {
				t.Errorf("scrub(%q) = %q; want redaction marker", input, got)
			}
		})
	}
}

func TestScrubDoesNotNestRedactionMarkersInAuthorizationValues(t *testing.T) {
	const secret = "xoxp-NESTED-REDACTION-123456"
	got := scrub("", "Question Authorization: Bearer "+secret+"?")
	if strings.Contains(got, "]]") {
		t.Fatalf("scrub() = %q; want a single redaction marker", got)
	}
	if !strings.Contains(got, "Authorization: [REDACTED]") {
		t.Fatalf("scrub() = %q; want redacted authorization value", got)
	}
}

func TestScrubPreservesDiagnosticsAroundDigestAuthorizationHeader(t *testing.T) {
	input := `repo alpha; Authorization: Digest username="ops;bot", response="REVIEW_SENTINEL"; path /tmp/worktree exit 17`
	got := scrub("", input)

	if strings.Contains(got, "ops;bot") || strings.Contains(got, "REVIEW_SENTINEL") {
		t.Fatalf("scrub(%q) = %q; Digest credentials remain", input, got)
	}
	for _, want := range []string{"repo alpha", "[REDACTED]", "path /tmp/worktree", "exit 17"} {
		if !strings.Contains(got, want) {
			t.Errorf("scrub(%q) = %q; want preserved detail %q", input, got, want)
		}
	}
}

func TestProblemsOutboundRedactsAuthorizationRepresentations(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(
		testToken,
		ports.SlackRecipient{Kind: ports.SlackRecipientChannel, ID: "C-ENG", DisplayName: "#eng"},
	))
	harness.seedFeature("F-1", nil)
	harness.start(0)
	harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
	waitFor(t, 10*time.Second, func() bool {
		return len(postsTo(harness.server, "C-ENG")) == 1
	})

	tests := []struct {
		canonicalDetail string
		fallbackDetail  string
	}{
		{
			canonicalDetail: `{"Authorization":["Bearer CANONICAL_ARRAY_REVIEW_SENTINEL"],"path":"/tmp/canonical-array","exit":17}`,
			fallbackDetail:  `{"Authorization":["Bearer FALLBACK_ARRAY_REVIEW_SENTINEL"],"path":"/tmp/fallback-array","exit":23}`,
		},
		{
			canonicalDetail: `repo alpha; Authorization: Digest username="ops;bot", response="CANONICAL_DIGEST_REVIEW_SENTINEL"; path /tmp/canonical-digest exit 29`,
			fallbackDetail:  `repo beta; Authorization: Digest username="ops;bot", response="FALLBACK_DIGEST_REVIEW_SENTINEL"; path /tmp/fallback-digest exit 31`,
		},
	}
	for _, tt := range tests {
		wantPosts := len(postsTo(harness.server, "C-ENG")) + 1
		problem := errcat.New(
			errcat.SessionCrashed,
			errcat.WithDiagnostics(tt.canonicalDetail),
		)
		harness.feed(ports.Event{
			Type: ports.FeatureFailed, FeatureID: "F-1", CanonicalError: &problem,
		})
		waitFor(t, 10*time.Second, func() bool {
			return len(postsTo(harness.server, "C-ENG")) == wantPosts
		})

		wantPosts++
		harness.feed(ports.Event{
			Type:      ports.FeatureFailed,
			FeatureID: "F-1",
			Message:   tt.fallbackDetail,
		})
		waitFor(t, 10*time.Second, func() bool {
			return len(postsTo(harness.server, "C-ENG")) == wantPosts
		})
	}

	encoded, err := json.Marshal(postsTo(harness.server, "C-ENG")[1:])
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for _, secret := range []string{
		"CANONICAL_ARRAY_REVIEW_SENTINEL",
		"FALLBACK_ARRAY_REVIEW_SENTINEL",
		"CANONICAL_DIGEST_REVIEW_SENTINEL",
		"FALLBACK_DIGEST_REVIEW_SENTINEL",
		"ops;bot",
	} {
		if strings.Contains(body, secret) {
			t.Errorf("Problems outbound body leaked %q: %s", secret, body)
		}
	}
	for _, want := range []string{
		"/tmp/canonical-array", "17",
		"/tmp/fallback-array", "23",
		"repo alpha", "/tmp/canonical-digest", "exit 29",
		"repo beta", "/tmp/fallback-digest", "exit 31",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Problems outbound body missing neighboring diagnostic %q: %s", want, body)
		}
	}
}

func TestRenderProblemDoesNotShortenDiagnosticsThatFit(t *testing.T) {
	diagnostics := strings.Repeat("a", sectionTextLimit-len("```\n\n```"))
	blocks, _, _ := renderProblem("", errcat.New(
		errcat.SessionCrashed,
		errcat.WithDiagnostics(diagnostics),
	), &feature.Feature{})

	encoded, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if strings.Contains(text, "Diagnostics were shortened") {
		t.Fatalf("renderProblem() shortened diagnostics that fit: %s", text)
	}
	if !strings.Contains(text, diagnostics) {
		t.Fatal("renderProblem() did not preserve fitting diagnostics")
	}
}

func TestProblemsOutboundFallbackCarriesRecoveryGuidance(t *testing.T) {
	longRepo := "alpha-" + strings.Repeat("service-", 24)
	longBranch := "feature/" + strings.Repeat("accessible-fallback-", 16)
	longConflictFiles := make([]string, 30)
	for i := range longConflictFiles {
		longConflictFiles[i] = fmt.Sprintf(
			"internal/notifications/integration/conflict_handler_%02d_test.go",
			i,
		)
	}
	harness := newNotifierHarness(t, defaultTestSettings(
		testToken,
		ports.SlackRecipient{Kind: ports.SlackRecipientChannel, ID: "C-ENG", DisplayName: "#eng"},
	))
	harness.seedFeature("F-1", nil)
	harness.seedFeature("F-2", func(f *feature.Feature) {
		f.Parent = &feature.ChildRelationship{
			ParentID: "F-1",
			Kind:     feature.ChildKindRefactor,
		}
	})
	harness.start(0)
	harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
	waitFor(t, 10*time.Second, func() bool {
		return len(postsTo(harness.server, "C-ENG")) == 1
	})

	tests := []struct {
		name string
		ev   ports.Event
		want []string
	}{
		{
			name: "blocking",
			ev: ports.Event{
				Type: ports.FeatureFailed, FeatureID: "F-1",
				CanonicalError: problemPointer(errcat.New(
					errcat.SessionCrashed,
					errcat.WithParams(errcat.RunFailureParams{
						Phase: "implement", Iteration: 7, Repositories: []string{longRepo},
					}),
					errcat.WithRepositories(errcat.CodeRepository{Name: longRepo}),
					errcat.WithPhase(errcat.CodePhase{Name: "implement", Iteration: 7}),
					errcat.WithDiagnostics(strings.Repeat(
						"provider stack frame in /tmp/worktrees/alpha/session.log\n",
						30,
					)),
				)),
			},
			want: []string{
				"Actions: restart. Restart the phase; the session log has the crash details.",
				"repository " + longRepo,
				"phase implement (iteration 7)",
				"session_crashed (blocking)",
				"Open Agentico for the full diagnostics.",
			},
		},
		{
			name: "setup",
			ev: ports.Event{
				Type: ports.SetupFailed, FeatureID: "F-1",
				SetupTask: "worktree:alpha", RepoName: "alpha",
				CanonicalError: problemPointer(errcat.New(
					errcat.WorktreeSetupFailed,
					errcat.WithParams(errcat.SetupFailureParams{
						TaskLabel: "Worktree: alpha", Repositories: []string{"alpha"},
					}),
					errcat.WithRepositories(errcat.CodeRepository{Name: "alpha"}),
					errcat.WithSetupTask(errcat.CodeSetupTask{
						Key: "worktree:alpha", Kind: "worktree", Label: "Worktree: alpha",
					}),
					errcat.WithDiagnostics(strings.Repeat(
						"git worktree add failed after checking repository state; ",
						24,
					)),
				)),
			},
			want: []string{
				"Actions: setup. Resolve the reported problem in the repository or branch, then retry setup.",
				"repository alpha",
				"setup task Worktree: alpha",
				"worktree_setup_failed (blocking)",
				"Open Agentico for the full diagnostics.",
			},
		},
		{
			name: "publish",
			ev: ports.Event{
				Type: ports.PublishCompleted, FeatureID: "F-1",
				CanonicalError: problemPointer(errcat.New(
					errcat.PublishRebaseConflict,
					errcat.WithParams(errcat.PublishRepoParams{
						Repo: "alpha", Branch: longBranch, RebaseTarget: "main",
					}),
					errcat.WithRepositories(errcat.CodeRepository{
						Name: "alpha", Branch: longBranch, RebaseTarget: "main",
					}),
					errcat.WithDiagnostics(strings.Repeat(
						"CONFLICT in internal/slack/render.go while replaying commit; ",
						24,
					)),
				)),
			},
			want: []string{
				"Actions: publish. Resolve the conflict in the worktree or run a rebase pass, then retry.",
				"repository alpha (" + longBranch + "); target: main",
				"publish_rebase_conflict (needs your action)",
				"Open Agentico for the full diagnostics.",
			},
		},
		{
			name: "child attention",
			ev: ports.Event{
				Type: ports.RelationshipIntegrationChanged, FeatureID: "F-2",
				ParentID: "F-1", ChildID: "F-2",
				CanonicalError: problemPointer(errcat.New(
					errcat.IntegrationMergeConflict,
					errcat.WithParams(errcat.IntegrationRepoParams{
						Repositories: []errcat.CodeRepository{{Name: "alpha"}},
					}),
					errcat.WithRepositories(errcat.CodeRepository{
						Name:          "alpha",
						Branch:        "feature/refactor",
						ConflictFiles: longConflictFiles,
					}),
					errcat.WithDiagnostics(strings.Repeat(
						"merge conflict in internal/slack/render.go; ",
						30,
					)),
				)),
			},
			want: []string{
				"Refactor: Integration merge conflict",
				"Actions: retry. Resolve the conflict in the pass worktree and retry; the pass re-enters final review if its code changed.",
				"repository alpha (feature/refactor); conflicts: internal/notifications/integration/conflict_handler_00_test.go",
				", ...",
				"integration_merge_conflict (needs your action)",
				"Open Agentico for the full diagnostics.",
			},
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			harness.feed(tt.ev)
			waitFor(t, 10*time.Second, func() bool {
				return len(postsTo(harness.server, "C-ENG")) == i+2
			})
			fallback := fieldString(postsTo(harness.server, "C-ENG")[i+1], "text")
			for _, want := range tt.want {
				if !strings.Contains(fallback, want) {
					t.Errorf("outbound fallback = %q; want %q", fallback, want)
				}
			}
			if len(fallback) > problemFallbackTextLimit {
				t.Errorf(
					"outbound fallback length = %d; want <= %d",
					len(fallback),
					problemFallbackTextLimit,
				)
			}
		})
	}
}

func problemPointer(problem errcat.Error) *errcat.Error {
	return &problem
}
