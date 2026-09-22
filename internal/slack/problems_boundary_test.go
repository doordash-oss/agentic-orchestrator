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
		`"Authorization": "Bearer REVIEW_SENTINEL"`,
		`{"Authorization":"Token REVIEW_SENTINEL","path":"/tmp/alpha"}`,
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
				CanonicalError: accessibleProblem(
					errcat.SessionCrashed, errcat.ClassBlocking, "restart",
					&errcat.Context{Phase: &errcat.CodePhase{Name: "implement"}},
				),
			},
			want: []string{"restart", "implement", "session_crashed", "blocking", "Agentico"},
		},
		{
			name: "setup",
			ev: ports.Event{
				Type: ports.SetupFailed, FeatureID: "F-1",
				SetupTask: "worktree:alpha", RepoName: "alpha",
				CanonicalError: accessibleProblem(
					errcat.WorktreeSetupFailed, errcat.ClassBlocking, "setup",
					&errcat.Context{
						Repositories: []errcat.CodeRepository{{Name: "alpha"}},
						SetupTask:    &errcat.CodeSetupTask{Label: "Worktree: alpha"},
					},
				),
			},
			want: []string{"setup", "alpha", "worktree_setup_failed", "blocking", "Agentico"},
		},
		{
			name: "publish",
			ev: ports.Event{
				Type: ports.PublishCompleted, FeatureID: "F-1",
				CanonicalError: accessibleProblem(
					errcat.PublishRebaseConflict, errcat.ClassNeedsAction, "publish",
					&errcat.Context{Repositories: []errcat.CodeRepository{{Name: "alpha"}}},
				),
			},
			want: []string{"publish", "alpha", "publish_rebase_conflict", "needs your action", "Agentico"},
		},
		{
			name: "child attention",
			ev: ports.Event{
				Type: ports.RelationshipIntegrationChanged, FeatureID: "F-2",
				ParentID: "F-1", ChildID: "F-2",
				CanonicalError: accessibleProblem(
					errcat.IntegrationMergeConflict, errcat.ClassNeedsAction, "resolve",
					&errcat.Context{Repositories: []errcat.CodeRepository{{Name: "alpha"}}},
				),
			},
			want: []string{"Refactor", "resolve", "integration_merge_conflict", "needs your action", "Agentico"},
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
			if strings.Contains(fallback, "REVIEW_SENTINEL") {
				t.Errorf("outbound fallback leaked credential: %q", fallback)
			}
		})
	}
}

func accessibleProblem(
	code errcat.Code,
	class errcat.Class,
	action string,
	context *errcat.Context,
) *errcat.Error {
	problem := errcat.Error{
		Code:    code,
		Class:   class,
		Title:   "Failure title",
		Summary: strings.Repeat("A long summary obscures trailing fallback content. ", 20),
		Remediation: &errcat.Remediation{
			Hint:    "Use Authorization: Token REVIEW_SENTINEL, then retry.",
			Actions: []string{action},
		},
		Context:     context,
		Diagnostics: "provider failed at /tmp/alpha; open the full trace",
	}
	return &problem
}
