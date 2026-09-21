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
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func TestRenderRootCardEscapesAndTruncatesRecordFields(t *testing.T) {
	f := &feature.Feature{
		Name:         strings.Repeat("feature", 30) + " <#C-NOTIFY>",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Pipeline:     feature.PipelineMoonshot,
		Repos: []feature.FeatureRepo{
			{Name: "api & <#C-NOTIFY>"},
			{Name: strings.Repeat("repository", 60)},
		},
	}

	blocks, fallback := renderRootCard(
		"agent & <@U-NOTIFY>",
		f,
		time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC),
	)

	header, ok := blocks[0].(headerBlock)
	if !ok {
		t.Fatalf("renderRootCard() first block = %T; want headerBlock", blocks[0])
	}
	if got := len(header.Text.Text); got > headerTextLimit {
		t.Errorf("renderRootCard() header length = %d; want at most %d", got, headerTextLimit)
	}
	if !strings.HasSuffix(header.Text.Text, "...") {
		t.Errorf("renderRootCard() header = %q; want a truncation ellipsis", header.Text.Text)
	}

	section, ok := blocks[1].(sectionBlock)
	if !ok {
		t.Fatalf("renderRootCard() second block = %T; want sectionBlock", blocks[1])
	}
	server := fieldWithLabel(t, section.Fields, "Server")
	if want := "*Server:* agent &amp; &lt;@U-NOTIFY&gt;"; server != want {
		t.Errorf("renderRootCard() server field = %q; want %q", server, want)
	}
	repositories := fieldWithLabel(t, section.Fields, "Repositories")
	if strings.Contains(repositories, "&amp;amp;") || strings.Contains(repositories, "&amp;lt;") {
		t.Errorf("renderRootCard() repositories field = %q; want record text escaped once", repositories)
	}
	if !strings.Contains(repositories, "api &amp; &lt;#C-NOTIFY&gt;") {
		t.Errorf("renderRootCard() repositories field = %q; want escaped channel-like text", repositories)
	}
	if got := len(repositories); got > fieldTextLimit {
		t.Errorf("renderRootCard() repositories field length = %d; want at most %d", got, fieldTextLimit)
	}
	if got := len(fallback); got > fallbackTextLimit {
		t.Errorf("renderRootCard() fallback length = %d; want at most %d", got, fallbackTextLimit)
	}
}

func TestRenderRootCardEditedStateIncludesPullRequestLinks(t *testing.T) {
	f := &feature.Feature{
		Name:         "Publish Slack notifications",
		Status:       feature.StatusDone,
		CurrentPhase: feature.PhasePublish,
		Pipeline:     feature.PipelineMoonshot,
		Repos: []feature.FeatureRepo{
			{Name: "api"},
			{Name: "desktop & shell"},
		},
		RepoStates: map[string]*feature.RepoState{
			"api":             {PRURL: "https://github.example/acme/api/pull/17"},
			"desktop & shell": {PRURL: "https://github.example/acme/desktop/pull/23"},
		},
	}

	blocks, _ := renderRootCard("Local agent", f, time.Now())
	section, ok := blocks[1].(sectionBlock)
	if !ok {
		t.Fatalf("renderRootCard() second block = %T; want sectionBlock", blocks[1])
	}
	pullRequests := fieldWithLabel(t, section.Fields, "Pull requests")
	for _, want := range []string{
		"<https://github.example/acme/api/pull/17|api>",
		"<https://github.example/acme/desktop/pull/23|desktop &amp; shell>",
	} {
		if !strings.Contains(pullRequests, want) {
			t.Errorf("renderRootCard() pull request field = %q; want link %q", pullRequests, want)
		}
	}
}

func TestPhaseWithRoadmapIncludesRoadmapPosition(t *testing.T) {
	tests := []struct {
		name         string
		currentPhase feature.Phase
		roadmapPhase int
		want         string
	}{
		{
			name:         "plan",
			currentPhase: feature.PhasePlan,
			roadmapPhase: 1,
			want:         "Plan (roadmap phase 1 of 2)",
		},
		{
			name:         "first implementation phase",
			currentPhase: feature.PhaseImplement,
			roadmapPhase: 1,
			want:         "Implementation (roadmap phase 1 of 2)",
		},
		{
			name:         "second implementation phase",
			currentPhase: feature.PhaseImplement,
			roadmapPhase: 2,
			want:         "Implementation (roadmap phase 2 of 2)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &feature.Feature{
				CurrentPhase:        tt.currentPhase,
				CurrentRoadmapPhase: tt.roadmapPhase,
				TotalRoadmapPhases:  2,
			}

			if got := phaseWithRoadmap(f); got != tt.want {
				t.Errorf("phaseWithRoadmap() = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestRenderProgressImplementationWordingDoesNotIncludeRoadmapPosition(t *testing.T) {
	f := &feature.Feature{
		CurrentRoadmapPhase: 2,
		TotalRoadmapPhases:  2,
	}
	ev := ports.Event{Type: ports.PhaseStarted, Phase: feature.PhaseImplement}

	if got, want := renderProgress(ev, f), "🔧 Implementation started"; got != want {
		t.Errorf("renderProgress() = %q; want %q", got, want)
	}
}

func TestTextLimitsIncludeEllipsis(t *testing.T) {
	if got, want := boundDisplayText("abcdefgh", 5), "ab..."; got != want {
		t.Errorf("boundDisplayText() = %q; want %q", got, want)
	}
	if got := safeText("&&&&", 8); got != "&amp;..." {
		t.Errorf("safeText() = %q; want complete escaped entity plus ellipsis", got)
	}
}

func TestLabeledFieldRawTruncatesBetweenLinkTokens(t *testing.T) {
	var links []string
	for i := 0; i < 12; i++ {
		links = append(links, "<https://github.example/acme/repository/pull/123456789|"+
			strings.Repeat("repository", 16)+">")
	}

	field := labeledFieldRaw("Pull requests", strings.Join(links, " "))
	if got := len(field.Text); got > fieldTextLimit {
		t.Fatalf("labeledFieldRaw() length = %d; want at most %d", got, fieldTextLimit)
	}
	if strings.Count(field.Text, "<") != strings.Count(field.Text, ">") {
		t.Errorf("labeledFieldRaw() = %q; want only complete link tokens", field.Text)
	}
	if !strings.HasSuffix(field.Text, "...") {
		t.Errorf("labeledFieldRaw() = %q; want truncation ellipsis", field.Text)
	}
}

func TestRenderProgressUsesReviewFeedbackAndRebasePrefixes(t *testing.T) {
	tests := []struct {
		name string
		kind string
		want string
	}{
		{
			name: "review feedback",
			kind: feature.ChildKindReviewFeedback,
			want: "🔁 Review feedback: Implementation started",
		},
		{
			name: "rebase",
			kind: feature.ChildKindRebase,
			want: "🔀 Rebase: Implementation started",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &feature.Feature{
				Parent: &feature.ChildRelationship{
					ParentID: "parent-1",
					Kind:     tt.kind,
				},
			}
			ev := ports.Event{Type: ports.PhaseStarted, Phase: feature.PhaseImplement}

			if got := renderProgress(ev, f); got != tt.want {
				t.Errorf("renderProgress(%q child) = %q; want %q", tt.kind, got, tt.want)
			}
		})
	}
}

func fieldWithLabel(t *testing.T, fields []textObject, label string) string {
	t.Helper()
	prefix := "*" + label + ":* "
	for _, field := range fields {
		if strings.HasPrefix(field.Text, prefix) {
			return field.Text
		}
	}
	t.Fatalf("field %q not found in %#v", label, fields)
	return ""
}
