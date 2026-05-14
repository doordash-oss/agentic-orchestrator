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
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

func TestLivePreviewEligible(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		f    *feature.Feature
		want bool
	}{
		{
			name: "running phase",
			f:    &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement},
			want: true,
		},
		{
			name: "created startup",
			f:    &feature.Feature{Status: feature.StatusCreated, CurrentPhase: feature.PhaseImplement},
			want: true,
		},
		{
			name: "active post publish cycle",
			f: &feature.Feature{
				Status: feature.StatusPublished,
				RepoCycles: map[string]*feature.RepoCycleState{
					"api": {Type: feature.CycleTweak, Status: feature.RepoCycleRunning},
				},
			},
			want: true,
		},
		{
			name: "done",
			f:    &feature.Feature{Status: feature.StatusDone},
			want: false,
		},
		{
			name: "failed",
			f:    &feature.Feature{Status: feature.StatusFailed},
			want: false,
		},
		{
			name: "interrupted",
			f:    &feature.Feature{Status: feature.StatusInterrupted},
			want: false,
		},
		{
			name: "published without active cycle",
			f:    &feature.Feature{Status: feature.StatusPublished},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isLivePreviewEligible(tt.f); got != tt.want {
				t.Errorf("isLivePreviewEligible(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestContextualAActionHint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		f        *feature.Feature
		wantHint string
		wantLead bool
	}{
		{
			name:     "ordinary live state watches",
			f:        &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement},
			wantHint: "[a] Watch",
		},
		{
			name:     "review behavior preserved",
			f:        &feature.Feature{Status: feature.StatusPlanNeedsReview},
			wantHint: "[a] Review",
			wantLead: true,
		},
		{
			name:     "answer behavior preserved",
			f:        &feature.Feature{Status: feature.StatusNeedUserInput},
			wantHint: "[a] Answer",
			wantLead: true,
		},
		{
			name: "waiting permission still attaches",
			f: &feature.Feature{
				Status: feature.StatusImplementing,
				PermissionsQueue: []feature.PermissionRequest{
					{Tool: "Bash", Pending: true},
				},
			},
			wantHint: "[a] Attach (⚠)",
		},
		{
			name:     "static published has no a action",
			f:        &feature.Feature{Status: feature.StatusPublished},
			wantHint: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotHint, gotLead := contextualAActionHint(tt.f)
			if gotHint != tt.wantHint || gotLead != tt.wantLead {
				t.Errorf("contextualAActionHint(%s) = (%q, %v), want (%q, %v)", tt.name, gotHint, gotLead, tt.wantHint, tt.wantLead)
			}
		})
	}
}

func TestLivePreviewActivityLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		f    *feature.Feature
		sess session.SessionView
		want string
	}{
		{
			name: "nil session",
			f:    &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement},
			want: "Working on Implement...",
		},
		{
			name: "empty session",
			f:    &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement},
			sess: newLivePreviewSession("empty", feature.PhaseImplement),
			want: "Working on Implement...",
		},
		{
			name: "idle running session",
			f:    &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement},
			sess: newLivePreviewSession("idle", feature.PhaseImplement,
				llm.SDKMessage{Type: "system", Init: &llm.SystemInitMessage{SessionID: "idle"}}),
			want: "Working on Implement...",
		},
		{
			name: "assistant thinking",
			f:    &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement},
			sess: newLivePreviewSession("thinking", feature.PhaseImplement,
				assistantMessage(llm.ContentBlock{Type: "thinking", Thinking: "checking"})),
			want: "Thinking...",
		},
		{
			name: "active tool use",
			f:    &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement},
			sess: newLivePreviewSession("tool", feature.PhaseImplement,
				assistantMessage(llm.ContentBlock{Type: "tool_use", Name: "Bash"})),
			want: "Using Bash...",
		},
		{
			name: "tool progress",
			f:    &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement},
			sess: newLivePreviewSession("tool-progress", feature.PhaseImplement,
				llm.SDKMessage{Type: "tool_progress", ToolProgress: &llm.ToolProgressMessage{ToolName: "Edit"}}),
			want: "Using Edit...",
		},
		{
			name: "completed turn fallback",
			f:    &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement},
			sess: newLivePreviewSession("result", feature.PhaseImplement,
				llm.SDKMessage{Type: "result", Result: &llm.ResultMessage{Subtype: "success"}}),
			want: "Working on Implement...",
		},
		{
			name: "created startup",
			f:    &feature.Feature{Status: feature.StatusCreated, CurrentPhase: feature.PhasePlan},
			want: "Starting Plan...",
		},
		{
			name: "tweak awaiting input",
			f: &feature.Feature{
				Status:       feature.StatusPublished,
				CurrentPhase: feature.PhaseImplement,
				RepoCycles: map[string]*feature.RepoCycleState{
					"api": {Type: feature.CycleTweak, Status: feature.RepoCycleRunning},
				},
			},
			sess: tweakLivePreviewSession("tweak-input",
				llm.SDKMessage{Type: "result", Result: &llm.ResultMessage{Subtype: "success"}}),
			want: "Waiting for tweak input...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := livePreviewActivityLine(tt.f, tt.sess); got != tt.want {
				t.Errorf("livePreviewActivityLine(%s) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestDashboardRendersLivePreviewForEligibleFeature(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:               "feat-live",
		Name:             "Live Feature",
		Slug:             "live-feature",
		Status:           feature.StatusImplementing,
		CurrentPhase:     feature.PhaseImplement,
		CurrentIteration: 2,
		Created:          time.Now(),
		PhaseTimings: map[string]time.Duration{
			"implement": 12 * time.Minute,
		},
		PhaseCosts: map[string]float64{
			"implement": 0.42,
		},
	}
	sess := newLivePreviewSession("feat-live-impl", feature.PhaseImplement,
		assistantMessage(llm.ContentBlock{Type: "tool_use", Name: "Bash"}))
	m := dashboardWithSelectedFeature(f)
	m.focusPanel = 1
	m.spinnerView = "spin"
	m.livePreview.session = sess

	view := m.View()
	for _, want := range []string{"Live Preview", "Phase", "Implement", "Status", "Implementing", "Elapsed", "12m", "Cost", "$0.42", "Using Bash...", "[a] Watch"} {
		if !strings.Contains(view, want) {
			t.Fatalf("dashboard live preview missing %q in:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Phase Progress") {
		t.Fatalf("live preview should replace static detail phase progress, got:\n%s", view)
	}
}

func TestDashboardRendersStartupLivePreviewWithoutSession(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "feat-created",
		Name:         "Created Feature",
		Slug:         "created-feature",
		Status:       feature.StatusCreated,
		CurrentPhase: feature.PhaseResearch,
		Created:      time.Now(),
	}
	m := dashboardWithSelectedFeature(f)
	m.focusPanel = 1
	m.spinnerView = "spin"

	view := m.View()
	if !strings.Contains(view, "Live Preview") || !strings.Contains(view, "Starting Research...") {
		t.Fatalf("created feature should render startup live preview, got:\n%s", view)
	}
	if strings.Contains(view, "No transcript") {
		t.Fatalf("created startup preview must not render an empty transcript placeholder, got:\n%s", view)
	}
}

func TestDashboardRendersStaticDetailForFallbackFeature(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "feat-static",
		Name:         "Static Feature",
		Slug:         "static-feature",
		Status:       feature.StatusFailed,
		CurrentPhase: feature.PhaseImplement,
		Created:      time.Now(),
	}
	m := dashboardWithSelectedFeature(f)
	m.focusPanel = 1
	m.spinnerView = "spin"

	view := m.View()
	if strings.Contains(view, "Live Preview") {
		t.Fatalf("failed feature should render static detail, got:\n%s", view)
	}
	if !strings.Contains(view, "Phase Progress") {
		t.Fatalf("failed feature should retain static detail panel, got:\n%s", view)
	}
}

func TestDashboardFeatureRowSpinnerUsesLivePreviewPredicate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		f        *feature.Feature
		wantSpin bool
	}{
		{
			name:     "running",
			f:        &feature.Feature{ID: "running", Slug: "running", Status: feature.StatusImplementing},
			wantSpin: true,
		},
		{
			name:     "created",
			f:        &feature.Feature{ID: "created", Slug: "created", Status: feature.StatusCreated},
			wantSpin: true,
		},
		{
			name: "active cycle",
			f: &feature.Feature{
				ID:     "cycle",
				Slug:   "cycle",
				Status: feature.StatusPublished,
				RepoCycles: map[string]*feature.RepoCycleState{
					"api": {Type: feature.CycleReviewComments, Status: feature.RepoCycleRunning},
				},
			},
			wantSpin: true,
		},
		{
			name:     "failed",
			f:        &feature.Feature{ID: "failed", Slug: "failed", Status: feature.StatusFailed},
			wantSpin: false,
		},
		{
			name:     "idle published",
			f:        &feature.Feature{ID: "published", Slug: "published", Status: feature.StatusPublished},
			wantSpin: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewDashboardModel([]*feature.Feature{tt.f}, "")
			m.spinnerView = "spin"
			row := m.renderFeatureRowCompact(tt.f, false)
			hasSpin := strings.Contains(row, "spin")
			if hasSpin != tt.wantSpin {
				t.Errorf("renderFeatureRowCompact(%s) spinner = %v, want %v; row=%q", tt.name, hasSpin, tt.wantSpin, row)
			}
		})
	}
}

func newLivePreviewSession(id string, phase feature.Phase, messages ...llm.SDKMessage) session.SessionView {
	sess := session.NewSession(id, "feat-live", phase)
	sess.SetStatus(session.SessionRunning)
	for _, msg := range messages {
		sess.MessageLog().Append(msg)
	}
	return sess
}

func tweakLivePreviewSession(id string, messages ...llm.SDKMessage) session.SessionView {
	sess := session.NewSession(id, "feat-live", feature.PhaseImplement)
	sess.SetKind(ports.KindTweak)
	sess.SetStatus(session.SessionRunning)
	for _, msg := range messages {
		sess.MessageLog().Append(msg)
	}
	return sess
}

func assistantMessage(blocks ...llm.ContentBlock) llm.SDKMessage {
	return llm.SDKMessage{
		Type: "assistant",
		Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{
				Role:    "assistant",
				Content: blocks,
			},
		},
	}
}

func dashboardWithSelectedFeature(f *feature.Feature) DashboardModel {
	m := NewDashboardModel([]*feature.Feature{f}, "")
	m.width = 120
	m.height = 30
	m.cursor = 1
	m.syncPreview()
	return m
}
