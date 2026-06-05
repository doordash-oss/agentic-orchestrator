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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestDetailView(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:               "feat-1",
		Name:             "Test Feature",
		Slug:             "test-feature",
		Status:           feature.StatusImplementing,
		CurrentPhase:     feature.PhaseImplement,
		CurrentIteration: 4,
		Repos:            []feature.FeatureRepo{{Name: "payments", Path: "/tmp/payments"}},
		Models: config.ModelConfig{
			Research:       "opus",
			Planning:       "opus",
			Implementation: "opus",
			Review:         "opus",
		},
	}

	m := NewDetailModel(f, "")
	view := m.View()

	if !strings.Contains(view, "test-feature") {
		t.Error("expected feature slug in view")
	}
	if !strings.Contains(view, "Implementing") {
		t.Error("expected status in view")
	}
}

func TestDetailViewNoFeature(t *testing.T) {
	t.Parallel()
	m := NewDetailModel(nil, "")
	view := m.View()
	if !strings.Contains(view, "No feature selected") {
		t.Error("expected no feature message")
	}
}

func TestDetailFeatureID(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{ID: "test-id"}
	m := NewDetailModel(f, "")
	if m.FeatureID() != "test-id" {
		t.Errorf("expected test-id, got %s", m.FeatureID())
	}
}

func TestDetailViewFooterActions(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "feat-1",
		Slug:         "test-feature",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Models:       config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
	}

	m := NewDetailModel(f, "")
	view := m.View()

	// [a] Watch, [N] Input alerts, [s] Stop, [d] Delete, [esc] Back are expected for Implementing.
	// [r] Restart is hidden while running — [s] Stop takes its slot.
	// [p] Publish is only shown for PRReady status.
	for _, action := range []string{"[a]", "[Shift+N]", "[s] Stop", "[d]", "[esc]"} {
		if !strings.Contains(view, action) {
			t.Errorf("expected %q in footer", action)
		}
	}
	if strings.Contains(view, "[r] Restart") {
		t.Error("did not expect [r] Restart for Implementing (running) status — [s] Stop should take its slot")
	}
	if strings.Contains(view, "[p]") {
		t.Error("did not expect [p] Publish for Implementing status")
	}
}

func TestDetailViewFooterUsesContextualAttentionLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		f    *feature.Feature
		want string
	}{
		{
			name: "permission",
			f: &feature.Feature{
				ID: "perm", Slug: "perm", Status: feature.StatusImplementing,
				PermissionsQueue: []feature.PermissionRequest{{Tool: "Bash", Pending: true}},
				Models:           config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
			},
			want: "[a] Approve",
		},
		{
			name: "ask user",
			f: &feature.Feature{
				ID: "ask", Slug: "ask", Status: feature.StatusImplementing,
				HelpQueue: []feature.HelpRequest{{Question: "Need input?", Pending: true}},
				Models:    config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
			},
			want: "[a] Answer",
		},
		{
			name: "watch",
			f: &feature.Feature{
				ID: "watch", Slug: "watch", Status: feature.StatusImplementing,
				Models: config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
			},
			want: "[a] Watch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewDetailModel(tt.f, "")
			view := m.View()
			if !strings.Contains(view, tt.want) {
				t.Fatalf("DetailModel.View(%s) missing %q in:\n%s", tt.name, tt.want, view)
			}
		})
	}
}

func TestDetailViewPlanNeedsReview(t *testing.T) {
	t.Parallel()
	t.Run("roadmap awaiting review", func(t *testing.T) {
		f := &feature.Feature{
			ID:                  "feat-review",
			Slug:                "roadmap-review",
			Status:              feature.StatusPlanNeedsReview,
			CurrentPhase:        feature.PhasePlan,
			CurrentRoadmapPhase: 0,
			TotalRoadmapPhases:  3,
			Models:              config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
		}
		m := NewDetailModel(f, "")
		view := m.View()

		// Roadmap sub-item must reflect needs-review, not "in progress".
		if !strings.Contains(view, "needs review") {
			t.Error("expected 'needs review' label in phase progress for roadmap")
		}
		if strings.Contains(view, "Roadmap") && strings.Contains(view, "in progress") {
			// Only tolerate "in progress" if it refers to something other than Roadmap.
			// Roadmap line should contain "needs review" instead.
			roadmapLine := ""
			for _, line := range strings.Split(view, "\n") {
				if strings.Contains(line, "Roadmap") {
					roadmapLine = line
					break
				}
			}
			if strings.Contains(roadmapLine, "in progress") {
				t.Errorf("Roadmap sub-item still says 'in progress' when plan needs review: %q", roadmapLine)
			}
		}

		// Needs-Review banner must be present.
		if !strings.Contains(view, "Needs Review") {
			t.Error("expected 'Needs Review' banner title")
		}
		if !strings.Contains(view, "press") || !strings.Contains(view, "[a]") {
			t.Error("expected banner call-to-action with [a] key")
		}

		// Footer must expose the [a] Review hint prominently.
		if !strings.Contains(view, "[a] Review") {
			t.Error("expected '[a] Review' hint in footer for needs-review state")
		}
	})

	t.Run("phase plan awaiting review shows phase label", func(t *testing.T) {
		f := &feature.Feature{
			ID:                  "feat-phase-review",
			Slug:                "phase-plan-review",
			Status:              feature.StatusPlanNeedsReview,
			CurrentPhase:        feature.PhasePlan,
			CurrentRoadmapPhase: 2,
			TotalRoadmapPhases:  3,
			Models:              config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
		}
		m := NewDetailModel(f, "")
		view := m.View()

		if !strings.Contains(view, "Phase 2 plan") && !strings.Contains(view, "Phase 2 Plan") {
			t.Error("expected banner to reference 'Phase 2 plan'")
		}
		if !strings.Contains(view, "[a] Review") {
			t.Error("expected '[a] Review' hint in footer")
		}
	})

	t.Run("footer renders [a] Review on the same line as other hints", func(t *testing.T) {
		f := &feature.Feature{
			ID:                  "feat-footer-line",
			Slug:                "footer-line",
			Status:              feature.StatusPlanNeedsReview,
			CurrentPhase:        feature.PhasePlan,
			CurrentRoadmapPhase: 0,
			TotalRoadmapPhases:  2,
			Models:              config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
		}
		m := NewDetailModel(f, "")
		view := m.View()

		// Find the footer line that contains "[a] Review" and confirm
		// neighbouring hints (at minimum [r] Restart) are on the SAME line, not
		// stranded below.
		for _, line := range strings.Split(view, "\n") {
			if strings.Contains(line, "[a] Review") && strings.Contains(line, "[r] Restart") {
				return
			}
		}
		t.Error("[a] Review not found on the same footer line as [r] Restart")
	})

	t.Run("compact view shows banner and [a] Review affordance", func(t *testing.T) {
		f := &feature.Feature{
			ID:                  "feat-compact-review",
			Slug:                "compact-review",
			Status:              feature.StatusPlanNeedsReview,
			CurrentPhase:        feature.PhasePlan,
			CurrentRoadmapPhase: 0,
			TotalRoadmapPhases:  2,
			Models:              config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
		}
		m := NewDetailModel(f, "")
		view := m.ViewCompact(120)

		if !strings.Contains(view, "Needs Review") {
			t.Error("expected 'Needs Review' banner in compact view")
		}
		if !strings.Contains(view, "needs review") {
			t.Error("expected 'needs review' label for Roadmap sub-item in compact view")
		}
	})
}

func TestDetailViewFailedFeature(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "feat-fail",
		Slug:         "failed-feature",
		Status:       feature.StatusFailed,
		CurrentPhase: feature.PhaseImplement,
		FailureType:  feature.FailureSafetyRail,
		LastError:    "no progress for 3 consecutive iterations",
		Models:       config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
	}

	m := NewDetailModel(f, "")
	view := m.View()

	if !strings.Contains(view, "Failure Info") {
		t.Error("expected Failure Info section")
	}
	if !strings.Contains(view, "safety rail") {
		t.Error("expected failure type in view")
	}
	if !strings.Contains(view, "no progress") {
		t.Error("expected error message in view")
	}
	if !strings.Contains(view, "[l]") {
		t.Error("expected [l] Logs hint in footer")
	}
	if !strings.Contains(view, "simplifying the task") {
		t.Error("expected recovery suggestion")
	}
}

func TestProtocolViolationFailureRendering(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "feat-protocol",
		Slug:         "protocol-fail",
		Status:       feature.StatusFailed,
		CurrentPhase: feature.PhaseImplement,
		FailureType:  feature.FailureProtocolViolation,
		LastError:    "protocol violation: implementer @ /tmp/iteration-01: phase_complete: SDK reported success but phase_complete was not present",
		Models:       config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
	}

	if got := formatFailureType(f.FailureType); got != "protocol violation" {
		t.Fatalf("formatFailureType(%q) = %q, want %q", f.FailureType, got, "protocol violation")
	}

	m := NewDetailModel(f, "")
	view := m.View()
	for _, want := range []string{
		"Failure Info",
		"protocol violation",
		"phase_complete was not present",
		"did not produce the required",
		"artifacts. Press [r] to retry",
		"[r] to retry",
		"[l] to view",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("DetailModel.View() missing %q in protocol violation rendering:\n%s", want, view)
		}
	}
}

func TestDetailStatusShowsActiveArtifactPhaseDuringProtocolRetry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		status      feature.Status
		current     feature.Phase
		consecutive int
		want        string
	}{
		{"inquire_first_retry", feature.StatusInquiring, feature.PhaseInquire, 1, "Inquiring"},
		{"research_second_retry", feature.StatusResearching, feature.PhaseResearch, 2, "Researching"},
		{"design_second_retry", feature.StatusDesigning, feature.PhaseDesign, 2, "Designing"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &feature.Feature{
				ID:           tt.name,
				Slug:         tt.name,
				Status:       tt.status,
				CurrentPhase: tt.current,
				FailureType:  "",
				LastError:    "",
			}
			view := NewDetailModel(f, "").View()
			if !strings.Contains(view, tt.want) {
				t.Fatalf("DetailModel.View() missing active label %q for retry streak %d:\n%s", tt.want, tt.consecutive, view)
			}
			if strings.Contains(view, "Failure Info") || strings.Contains(view, "protocol violation") {
				t.Fatalf("DetailModel.View() rendered failure during retry streak %d:\n%s", tt.consecutive, view)
			}
		})
	}
}

func TestDetailStatusShowsBuildingKBDuringProtocolRetry(t *testing.T) {
	t.Parallel()
	for _, consecutive := range []int{1, 2} {
		t.Run(fmt.Sprintf("kb_retry_%d", consecutive), func(t *testing.T) {
			f := &feature.Feature{
				ID:           fmt.Sprintf("feat-kb-retry-%d", consecutive),
				Slug:         "kb-retry",
				Status:       feature.StatusBuildingKB,
				CurrentPhase: feature.PhaseKnowledgeBase,
				Repos:        []feature.FeatureRepo{{Name: "repo-a"}},
				KBStatus:     map[string]string{"repo-a": "building"},
				FailureType:  "",
				LastError:    "",
			}
			view := NewDetailModel(f, "").View()
			if !strings.Contains(view, "Building Knowledge Base") && !strings.Contains(view, "BuildingKB") {
				t.Fatalf("DetailModel.View() missing active KB label for retry streak %d:\n%s", consecutive, view)
			}
			if strings.Contains(view, "Failure Info") || strings.Contains(view, "protocol violation") {
				t.Fatalf("DetailModel.View() rendered failure during KB retry streak %d:\n%s", consecutive, view)
			}
		})
	}
}

func TestDetailStatusShowsTerminalKBProtocolViolation(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "feat-kb-terminal",
		Slug:         "kb-terminal",
		Status:       feature.StatusFailed,
		CurrentPhase: feature.PhaseKnowledgeBase,
		FailureType:  feature.FailureProtocolViolation,
		LastError:    "protocol violation: knowledge_base_builder @ /tmp/kb: index.md: missing",
	}
	view := NewDetailModel(f, "").View()
	for _, want := range []string{"Failed (protocol violation)", "Failure Info", "knowledge_base_builder", "index.md"} {
		if !strings.Contains(view, want) {
			t.Fatalf("DetailModel.View() missing %q for terminal KB protocol violation:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Building KB") {
		t.Fatalf("DetailModel.View() rendered active KB after terminal failure:\n%s", view)
	}
}

func TestDetailViewFailedNoContext(t *testing.T) {
	t.Parallel()
	// Legacy features with no failure context still render correctly
	f := &feature.Feature{
		ID:           "feat-legacy",
		Slug:         "legacy-fail",
		Status:       feature.StatusFailed,
		CurrentPhase: feature.PhaseResearch,
		Models:       config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
	}

	m := NewDetailModel(f, "")
	view := m.View()

	if !strings.Contains(view, "Failed") {
		t.Error("expected Failed status")
	}
	// Should NOT show Failure Info box when there's no context
	if strings.Contains(view, "Failure Info") {
		t.Error("should not show Failure Info when no context available")
	}
}

func TestDetailViewDescription(t *testing.T) {
	t.Parallel()
	t.Run("shows description in Info box", func(t *testing.T) {
		f := &feature.Feature{
			ID:           "feat-desc",
			Slug:         "desc-test",
			Description:  "Add caching layer to the API gateway",
			Status:       feature.StatusImplementing,
			CurrentPhase: feature.PhaseImplement,
			Models:       config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
		}
		m := NewDetailModel(f, "")
		view := m.View()
		if !strings.Contains(view, "Desc") {
			t.Error("expected Desc label in view")
		}
		if !strings.Contains(view, "Add caching layer") {
			t.Error("expected description text in view")
		}
	})

	t.Run("truncates long description", func(t *testing.T) {
		long := strings.Repeat("a", 200)
		f := &feature.Feature{
			ID:           "feat-long",
			Slug:         "long-desc",
			Description:  long,
			Status:       feature.StatusCreated,
			CurrentPhase: feature.PhaseResearch,
			Models:       config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
		}
		m := NewDetailModel(f, "")
		view := m.View()
		if !strings.Contains(view, "…") {
			t.Error("expected truncation ellipsis for long description")
		}
	})

	t.Run("omits description when empty", func(t *testing.T) {
		f := &feature.Feature{
			ID:           "feat-empty",
			Slug:         "no-desc",
			Status:       feature.StatusCreated,
			CurrentPhase: feature.PhaseResearch,
			Models:       config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
		}
		m := NewDetailModel(f, "")
		view := m.View()
		if strings.Contains(view, "Desc") {
			t.Error("should not show Desc label when description is empty")
		}
	})

	t.Run("summary takes precedence over description", func(t *testing.T) {
		f := &feature.Feature{
			ID:           "feat-sum",
			Slug:         "summary-test",
			Description:  "This is a very long raw user prompt that rambles on and on about many things",
			Summary:      "Add caching to the API gateway.",
			Status:       feature.StatusImplementing,
			CurrentPhase: feature.PhaseImplement,
			Models:       config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
		}
		m := NewDetailModel(f, "")
		view := m.View()
		if !strings.Contains(view, "Add caching to the API gateway.") {
			t.Error("expected summary text in view")
		}
		if strings.Contains(view, "rambles on") {
			t.Error("should show summary, not raw description")
		}
	})

	t.Run("falls back to description when no summary", func(t *testing.T) {
		f := &feature.Feature{
			ID:           "feat-nosummary",
			Slug:         "fallback-test",
			Description:  "Implement user authentication",
			Status:       feature.StatusCreated,
			CurrentPhase: feature.PhaseResearch,
			Models:       config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
		}
		m := NewDetailModel(f, "")
		view := m.View()
		if !strings.Contains(view, "Implement user authentication") {
			t.Error("expected fallback to description when summary is empty")
		}
	})
}

func TestDetailHelpQueue(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "feat-1",
		Slug:         "help-test",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Models:       config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
		HelpQueue: []feature.HelpRequest{
			{Question: "How do I fix this?", Pending: true},
		},
	}

	m := NewDetailModel(f, "")
	view := m.View()

	if !strings.Contains(view, "Attention") {
		t.Error("expected attention section")
	}
	if !strings.Contains(view, "How do I fix this?") {
		t.Error("expected help question text")
	}
}

func TestDetailHelpQueueNormalizesLegacyAPIErrorCopy(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "feat-api-help",
		Slug:         "api-help-test",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Models:       config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
		HelpQueue: []feature.HelpRequest{
			{Question: "API error: rate limit exceeded (429) — attach with 'a' to respond", Pending: true},
		},
	}

	m := NewDetailModel(f, "")
	view := m.View()

	const want = "API error: rate limit exceeded (429) — press 'a' to answer"
	if !strings.Contains(view, want) {
		t.Errorf("DetailModel.View() missing normalized API error %q", want)
	}
	if strings.Contains(strings.ToLower(view), "attach with") {
		t.Errorf("DetailModel.View() contains retired API error copy:\n%s", view)
	}
}

func TestDetailViewShowsPhaseTiming(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:               "feat-timing",
		Slug:             "timing-test",
		Status:           feature.StatusImplementing,
		CurrentPhase:     feature.PhaseImplement,
		CurrentIteration: 2,
		Models:           config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
		PhaseTimings: map[string]time.Duration{
			"research": 5 * time.Minute,
			"plan":     12 * time.Minute,
		},
	}

	m := NewDetailModel(f, "")
	view := m.ViewCompact(80)

	if !strings.Contains(view, "5m") {
		t.Error("expected research duration in view")
	}
	if !strings.Contains(view, "12m") {
		t.Error("expected plan duration in view")
	}
}

func TestDetailViewShowsCycleTiming(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "feat-cycle",
		Slug:         "cycle-test",
		Status:       feature.StatusPublished,
		CurrentPhase: feature.PhasePublish,
		Models:       config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
		PhaseTimings: map[string]time.Duration{
			"research":  3 * time.Minute,
			"plan":      8 * time.Minute,
			"implement": 45 * time.Minute,
			"rebase-1":  15 * time.Minute,
			"tweak-1":   10 * time.Minute,
		},
	}
	f.SetRebaseCount(1)
	f.SetTweakCount(1)

	m := NewDetailModel(f, "")
	view := m.ViewCompact(80)

	if !strings.Contains(view, "Rebase #1") {
		t.Error("expected Rebase #1 sub-item in view")
	}
	if !strings.Contains(view, "Tweak #1") {
		t.Error("expected Tweak #1 sub-item in view")
	}
}

// TestDetailViewShowsInFlightRebase covers the case where a rebase is mid-flight
// (ActiveCycle.Status == running) but PhaseTimings does not yet have a rebase-N
// entry and ActiveTimingKey is stale (e.g. still pointing at the previous tweak).
// The right-panel Phase Progress must still surface the running rebase row so it
// matches the left-panel "Rebasing" status.
func TestDetailViewShowsInFlightRebase(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "feat-inflight-rebase",
		Slug:         "inflight-rebase",
		Status:       feature.StatusCodeReady,
		CurrentPhase: feature.PhasePublish,
		Models:       config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
		PhaseTimings: map[string]time.Duration{
			"implement": 45 * time.Minute,
			"rebase-1":  15 * time.Minute,
			"tweak-1":   10 * time.Second,
		},
		ActiveTimingKey: "tweak-1",
		ActiveCycle: &feature.CycleState{
			Type:   feature.CycleRebase,
			Status: feature.RepoCycleRunning,
			Count:  3,
		},
	}
	f.SetRebaseCount(3)
	f.SetTweakCount(1)
	f.SetActiveCycleType(feature.CycleRebase)

	m := NewDetailModel(f, "")
	const sentinelSpinner = "##SPIN##"
	m.spinnerView = sentinelSpinner
	m.contextBox = contextBox{mainFill: 42_000, window: 200_000, threshold: 100_000}
	view := m.ViewCompact(80)

	// phaseTimingKeys collapses multiple cycles of the same type into a single
	// aggregated row labelled with the latest index. With rebase-1 timed and
	// rebase-3 in flight, the label should be "Rebase #3 (2 total)".
	if !strings.Contains(view, "Rebase #3") {
		t.Errorf("expected in-flight Rebase #3 row in view; got:\n%s", view)
	}
	if !strings.Contains(view, "(2 total)") {
		t.Errorf("expected aggregated count of completed+in-flight rebases; got:\n%s", view)
	}
	if !strings.Contains(view, "in progress") {
		t.Errorf("expected in-flight rebase row to render with 'in progress'; got:\n%s", view)
	}
	rebaseLine := ""
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "Rebase #3") {
			rebaseLine = line
			break
		}
	}
	if rebaseLine == "" {
		t.Fatalf("could not locate Rebase #3 line in view:\n%s", view)
	}
	if !strings.Contains(rebaseLine, sentinelSpinner) {
		t.Errorf("expected in-flight rebase row to render the live spinner; got: %q", rebaseLine)
	}
	if !strings.Contains(rebaseLine, "42K / 200K (21%)") {
		t.Errorf("expected in-flight rebase row to render Smart Zone context; got: %q", rebaseLine)
	}
	if !strings.Contains(view, "Tweak #1") {
		t.Error("expected completed Tweak #1 sub-item in view")
	}

	// Regression: with a fresh rebase running, the stale ActiveTimingKey
	// pointing at the previous tweak must not make the Tweak row render
	// as "in progress". Strip the rebase line to isolate the tweak line.
	tweakLine := ""
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "Tweak #1") {
			tweakLine = line
			break
		}
	}
	if tweakLine == "" {
		t.Fatalf("could not locate Tweak #1 line in view:\n%s", view)
	}
	if strings.Contains(tweakLine, "in progress") {
		t.Errorf("Tweak #1 row must NOT render as in progress while a rebase is running; got: %q", tweakLine)
	}
}

// TestDetailViewSpinnerOnFinalReview asserts that StatusFinalReviewing drives
// the Final Review row to render with the live spinner instead of the static
// "current" caret. Previously the TUI's local isRunningStatus helper omitted
// StatusFinalReviewing, so the row showed ▶ instead of the spinner glyph.
func TestDetailViewSpinnerOnFinalReview(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "feat-fr",
		Slug:         "fr-test",
		Status:       feature.StatusFinalReviewing,
		CurrentPhase: feature.PhaseFinalReview,
		Models:       config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
	}
	m := NewDetailModel(f, "")
	const sentinelSpinner = "##SPIN##"
	m.spinnerView = sentinelSpinner
	view := m.View()

	// Locate the Final Review line and confirm it carries the spinner.
	frLine := ""
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "Final Review") && !strings.Contains(line, "Status") {
			frLine = line
			break
		}
	}
	if frLine == "" {
		t.Fatalf("could not locate Final Review line in view:\n%s", view)
	}
	if !strings.Contains(frLine, sentinelSpinner) {
		t.Errorf("expected Final Review row to render the live spinner during StatusFinalReviewing; got: %q", frLine)
	}
}

func TestDetailViewFinalReviewShowsSubphaseAndIteration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		fixing     bool
		iteration  int
		wantStatus string
		wantPhase  string
	}{
		{
			name:       "review subphase",
			iteration:  3,
			wantStatus: "Final Review: reviewing iteration 3",
			wantPhase:  "reviewing iteration 3",
		},
		{
			name:       "fix subphase",
			fixing:     true,
			iteration:  4,
			wantStatus: "Final Review: fixing iteration 4",
			wantPhase:  "fixing iteration 4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &feature.Feature{
				ID:              "feat-fr-detail",
				Slug:            "fr-detail",
				Status:          feature.StatusFinalReviewing,
				CurrentPhase:    feature.PhaseFinalReview,
				ReviewFixing:    tt.fixing,
				ReviewIteration: tt.iteration,
				Models:          config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
			}
			m := NewDetailModel(f, "")
			view := m.ViewCompact(100)

			if !strings.Contains(view, tt.wantStatus) {
				t.Fatalf("ViewCompact() = %q, want status %q", view, tt.wantStatus)
			}
			frLine := ""
			for _, line := range strings.Split(view, "\n") {
				if strings.Contains(line, "Final Review") && !strings.Contains(line, "Status") {
					frLine = line
					break
				}
			}
			if frLine == "" {
				t.Fatalf("could not locate Final Review line in view:\n%s", view)
			}
			if !strings.Contains(frLine, tt.wantPhase) {
				t.Errorf("Final Review progress line = %q, want %q", frLine, tt.wantPhase)
			}
			if strings.Contains(view, "FinalReviewing") {
				t.Errorf("ViewCompact() = %q, should not fall back to raw status", view)
			}
		})
	}
}

func TestDetailViewTerminalFinalReviewFailureOverridesCodeReadyProjection(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "feat-fr-corrupt",
		Slug:         "fr-corrupt",
		Status:       feature.StatusCodeReady,
		CurrentPhase: feature.PhasePublish,
		FailureType:  feature.FailureProtocolViolation,
		LastError:    "protocol violation: final_review_reviewer @ /tmp/iter: invalid report",
		Models:       config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
	}
	m := NewDetailModel(f, "")
	m.width = 100
	m.height = 40

	view := stripANSI(m.View())
	if !strings.Contains(view, "Failed (protocol violation) — press [r] to restart") {
		t.Fatalf("View() = %q, want failed restart status", view)
	}
	frLine := ""
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "Final Review") && !strings.Contains(line, "Status") {
			frLine = line
			break
		}
	}
	if frLine == "" {
		t.Fatalf("could not locate Final Review line in view:\n%s", view)
	}
	if !strings.Contains(frLine, "failed") {
		t.Fatalf("Final Review line = %q, want failed", frLine)
	}
	if strings.Contains(frLine, "complete") {
		t.Fatalf("Final Review line = %q, must not render complete", frLine)
	}
	if !strings.Contains(view, "[r] Restart") || !strings.Contains(view, "[l] Logs") {
		t.Fatalf("View() = %q, want restart and logs actions", view)
	}
}

func TestDetailViewSmartZoneContext(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		box        contextBox
		status     feature.Status
		wantStr    string
		wantAbsent string
	}{
		{"no data shows calculating", contextBox{mainFill: -1}, feature.StatusImplementing, "calculating", ""},
		{"neutral below threshold", contextBox{mainFill: 42_000, window: 200_000, threshold: 100_000}, feature.StatusImplementing, "42K / 200K (21%)", "calculating"},
		{"yellow at threshold", contextBox{mainFill: 100_000, window: 200_000, threshold: 100_000}, feature.StatusImplementing, "100K / 200K (50%)", "calculating"},
		{"over threshold below 80pct window", contextBox{mainFill: 150_000, window: 200_000, threshold: 100_000}, feature.StatusImplementing, "150K / 200K (75%)", "calculating"},
		{"red at 80pct window", contextBox{mainFill: 160_000, window: 200_000, threshold: 100_000}, feature.StatusImplementing, "160K / 200K (80%)", "calculating"},
		{"not shown for completed", contextBox{mainFill: 42_000, window: 200_000, threshold: 100_000}, feature.StatusCodeReady, "", "42K / 200K"},
		{"calculating not shown for completed", contextBox{mainFill: -1}, feature.StatusCodeReady, "", "calculating"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &feature.Feature{
				ID:               "feat-ctx",
				Slug:             "ctx-test",
				Status:           tt.status,
				CurrentPhase:     feature.PhaseImplement,
				CurrentIteration: 1,
				Models:           config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
			}
			m := NewDetailModel(f, "")
			m.contextBox = tt.box
			view := m.View()
			if tt.wantStr != "" && !strings.Contains(view, tt.wantStr) {
				t.Errorf("expected %q in view, not found", tt.wantStr)
			}
			if tt.wantAbsent != "" && strings.Contains(view, tt.wantAbsent) {
				t.Errorf("did not expect %q in view, found", tt.wantAbsent)
			}
		})
	}
}

func TestDetailViewSmartZoneContextInCompact(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:               "feat-compact",
		Slug:             "compact-test",
		Status:           feature.StatusImplementing,
		CurrentPhase:     feature.PhaseImplement,
		CurrentIteration: 1,
		Models:           config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
	}
	m := NewDetailModel(f, "")
	m.contextBox = contextBox{mainFill: 42_000, window: 200_000, threshold: 100_000}
	compact := m.ViewCompact(80)
	if !strings.Contains(compact, "42K / 200K (21%)") {
		t.Error("Smart Zone context should appear in compact view for active implementing feature")
	}
}

func TestDetailViewRoadmapPlanSubItems(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:                  "feat-roadmap",
		Slug:                "roadmap-test",
		Status:              feature.StatusPlanning,
		CurrentPhase:        feature.PhasePlan,
		CurrentRoadmapPhase: 2,
		TotalRoadmapPhases:  3,
		Models:              config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
	}

	m := NewDetailModel(f, "")

	// Full-screen view
	view := m.View()
	if !strings.Contains(view, "Roadmap") {
		t.Error("expected Roadmap sub-item in full-screen view")
	}
	if !strings.Contains(view, "Phase 1 Plan") {
		t.Error("expected Phase 1 Plan sub-item in full-screen view")
	}
	if !strings.Contains(view, "Phase 2 Plan") {
		t.Error("expected Phase 2 Plan sub-item in full-screen view")
	}

	// Compact view
	compact := m.ViewCompact(80)
	if !strings.Contains(compact, "Roadmap") {
		t.Error("expected Roadmap sub-item in compact view")
	}
	if !strings.Contains(compact, "Phase 1 Plan") {
		t.Error("expected Phase 1 Plan sub-item in compact view")
	}
}

func TestDetailViewRoadmapImplSubItems(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:                  "feat-impl-phases",
		Slug:                "impl-phases",
		Status:              feature.StatusImplementing,
		CurrentPhase:        feature.PhaseImplement,
		CurrentIteration:    2,
		CurrentRoadmapPhase: 2,
		TotalRoadmapPhases:  3,
		Models:              config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
	}

	m := NewDetailModel(f, "")
	view := m.View()

	// Phase 1 should show as complete (< CurrentRoadmapPhase)
	if !strings.Contains(view, "Phase 1") {
		t.Error("expected Phase 1 sub-item under Implement")
	}
	// Phase 2 should show as in progress (== CurrentRoadmapPhase)
	if !strings.Contains(view, "Phase 2") {
		t.Error("expected Phase 2 sub-item under Implement")
	}
}

func TestDetailViewNoCycleTimingWhenNone(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "feat-no-cycle",
		Slug:         "no-cycle",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Models:       config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
		PhaseTimings: map[string]time.Duration{
			"research": 5 * time.Minute,
			"plan":     10 * time.Minute,
		},
	}

	m := NewDetailModel(f, "")
	view := m.ViewCompact(80)

	if strings.Contains(view, "Rebase") || strings.Contains(view, "Tweak") {
		t.Error("should not show cycle sub-items when none exist")
	}
}

func TestRepoDisplayOrder(t *testing.T) {
	t.Parallel()
	// Per SchemaVersionCurrent = 3, the per-feature ExecutionPlan field has
	// been removed; the TUI falls back to f.Repos declaration order, which
	// is stable and deterministic across phases.
	t.Run("uses repos declaration order", func(t *testing.T) {
		f := &feature.Feature{
			Repos: []feature.FeatureRepo{{Name: "b"}, {Name: "a"}},
		}
		got := repoDisplayOrder(f)
		if len(got) != 2 || got[0] != "b" || got[1] != "a" {
			t.Errorf("expected [b a], got %v", got)
		}
	})
}

func TestDetailViewWorkDirMultiRepo(t *testing.T) {
	t.Parallel()
	t.Run("multi-repo shows worktree parent", func(t *testing.T) {
		f := &feature.Feature{
			Repos: []feature.FeatureRepo{
				{Name: "a", WorktreePath: "/home/user/.agentic-workflow/worktrees/my-feature/a"},
				{Name: "b", WorktreePath: "/home/user/.agentic-workflow/worktrees/my-feature/b"},
			},
			Models: config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
		}
		m := NewDetailModel(f, "")
		m.width = 80
		m.height = 40
		view := m.View()
		// Should show the parent directory, not the repo-specific path
		if !strings.Contains(view, "/home/user/.agentic-workflow/worktrees/my-feature") {
			t.Error("expected worktree parent path for multi-repo")
		}
		// Should NOT show the repo-specific subdirectory path as the full path
		// (it might contain it as a substring of the parent, but the actual displayed path should be the parent)
	})

	t.Run("single-repo shows repo worktree path", func(t *testing.T) {
		f := &feature.Feature{
			Repos: []feature.FeatureRepo{
				{Name: "a", WorktreePath: "/home/user/.agentic-workflow/worktrees/my-feature/a"},
			},
			Models: config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
		}
		m := NewDetailModel(f, "")
		m.width = 80
		m.height = 40
		view := m.View()
		if !strings.Contains(view, "/home/user/.agentic-workflow/worktrees/my-feature/a") {
			t.Error("expected single repo worktree path")
		}
	})
}

// TestRetryPhaseHiddenWhileImplementing verifies that [Shift+R] Retry phase
// does NOT appear in the footer when the feature is still StatusImplementing,
// even if a repo has failed. This prevents killing unrelated in-flight sessions.
func TestRetryPhaseHiddenWhileImplementing(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "test-retry-gate",
		Name:         "retry gate test",
		Slug:         "retry-gate",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Models:       config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
		Repos:        []feature.FeatureRepo{{Name: "a"}, {Name: "b"}},
		RepoStates: map[string]*feature.RepoState{
			"a": {Touched: true, LastError: "test failure"},
			"b": {},
		},
	}

	m := NewDetailModel(f, "")
	m.width = 80
	m.height = 40

	view := m.View()
	if strings.Contains(view, "Retry phase") {
		t.Error("expected [Shift+R] Retry phase to be hidden while feature is StatusImplementing")
	}
}

// TestRetryPhaseShownWhenFeatureFailed verifies that [Shift+R] Retry phase
// appears in the footer when the feature is StatusFailed with a failed repo.
func TestRetryPhaseShownWhenFeatureFailed(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "test-retry-shown",
		Name:         "retry shown test",
		Slug:         "retry-shown",
		Status:       feature.StatusFailed,
		CurrentPhase: feature.PhaseImplement,
		Models:       config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
		Repos:        []feature.FeatureRepo{{Name: "a"}, {Name: "b"}},
		RepoStates: map[string]*feature.RepoState{
			"a": {Touched: true, LastError: "test failure"},
			"b": {Touched: true},
		},
	}

	m := NewDetailModel(f, "")
	m.width = 80
	m.height = 40

	view := m.View()
	if !strings.Contains(view, "Retry phase") {
		t.Error("expected [Shift+R] Retry phase to appear when feature is StatusFailed with a failed repo")
	}
}

func TestDetailViewShowsRefactorTiming(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "feat-refactor",
		Slug:         "refactor-test",
		Status:       feature.StatusPublished,
		CurrentPhase: feature.PhasePublish,
		Models:       config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
		PhaseTimings: map[string]time.Duration{
			"research":   3 * time.Minute,
			"plan":       8 * time.Minute,
			"implement":  45 * time.Minute,
			"refactor-1": 5 * time.Minute,
			"refactor-2": 3 * time.Minute,
		},
	}
	f.SetRefactorCount(2)

	m := NewDetailModel(f, "")
	view := m.ViewCompact(80)

	if !strings.Contains(view, "Refactor #2 (2 total)") {
		t.Errorf("expected grouped Refactor #2 (2 total) sub-item in view, got:\n%s", view)
	}
	if strings.Contains(view, "Refactor #1 ") {
		t.Error("expected only the latest refactor entry to be rendered, not each one")
	}
}

func TestDetailViewNoRefactorTimingWhenNone(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "feat-no-refactor",
		Slug:         "no-refactor",
		Status:       feature.StatusPublished,
		CurrentPhase: feature.PhasePublish,
		Models:       config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
		PhaseTimings: map[string]time.Duration{
			"research": 3 * time.Minute,
			"plan":     8 * time.Minute,
		},
	}

	m := NewDetailModel(f, "")
	view := m.ViewCompact(80)

	if strings.Contains(view, "Refactor #") {
		t.Error("should not show refactor sub-items when none exist")
	}
}

func TestDetailViewFooterHidesPublishHintsForUnpublished(t *testing.T) {
	t.Parallel()
	falseBool := false
	f := &feature.Feature{
		ID:          "f1",
		Slug:        "test",
		Status:      feature.StatusCodeReady,
		Repos:       []feature.FeatureRepo{{Name: "r", Publishable: &falseBool}},
		Checkpoints: feature.Checkpoints{ManualPublish: true},
	}
	m := NewDetailModel(f, "")
	view := m.View()
	if strings.Contains(view, "[p]") {
		t.Error("expected [p] hint to be hidden for unpublished feature")
	}
	if strings.Contains(view, "[m]") {
		t.Error("expected [m] hint to be hidden for unpublished feature")
	}
}

func TestDetailFormatStatusHidesPublishHintsForUnpublished(t *testing.T) {
	t.Parallel()
	falseBool := false
	f := &feature.Feature{
		Repos:       []feature.FeatureRepo{{Publishable: &falseBool}},
		Checkpoints: feature.Checkpoints{ManualPublish: true},
	}

	// Test StatusCodeReady
	f.Status = feature.StatusCodeReady
	result := formatDetailStatus(f)
	if strings.Contains(result, "[p]") {
		t.Error("expected [p] to be hidden in formatDetailStatus for unpublished feature")
	}
	if strings.Contains(result, "[m]") {
		t.Error("expected [m] to be hidden in formatDetailStatus for unpublished feature")
	}
	// [t] and [F] should still be present
	if !strings.Contains(result, "[t]") {
		t.Error("expected [t] to still be present for unpublished feature")
	}
	if !strings.Contains(result, "[Shift+F]") {
		t.Error("expected [Shift+F] to still be present for unpublished feature")
	}

	// Test StatusPublished
	f.Status = feature.StatusPublished
	f.SetPRURL("https://github.com/org/repo/pull/1")
	result = formatDetailStatus(f)
	if strings.Contains(result, "[b]") {
		t.Error("expected [b] to be hidden in formatDetailStatus for unpublished feature")
	}
	if strings.Contains(result, "[g]") {
		t.Error("expected [g] to be hidden in formatDetailStatus for unpublished feature")
	}
	// [Shift+D] mark done should still be present
	if !strings.Contains(result, "[Shift+D]") {
		t.Error("expected [Shift+D] to still be present for unpublished feature")
	}
}

func TestDetailFormatStatus_ShowsWatchHintForActivePublishedCycle(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		Status:           feature.StatusPublished,
		CurrentIteration: 1,
		Repos:            []feature.FeatureRepo{{Name: "agentic"}},
		RepoCycles: map[string]*feature.RepoCycleState{
			"agentic": {Type: feature.CycleReviewComments, Status: "running"},
		},
	}

	got := formatDetailStatus(f)
	if !strings.Contains(got, "Addressing Review Comments [1]") {
		t.Fatalf("formatDetailStatus() = %q, want active review-comments cycle label", got)
	}
	if !strings.Contains(got, "[a] Watch") {
		t.Errorf("formatDetailStatus() = %q, want watch hint while cycle is active", got)
	}
}

func TestRenderLightbulbHint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		pipeline       feature.PipelineProfile
		status         feature.Status
		wantEmpty      bool
		wantContain    []string
		wantNotContain []string
	}{
		{
			name:        "shown for medium feature",
			pipeline:    feature.PipelineMedium,
			status:      feature.StatusImplementing,
			wantContain: []string{"medium", "large or moonshot"},
		},
		{
			name:        "shown for large feature",
			pipeline:    feature.PipelineLarge,
			status:      feature.StatusImplementing,
			wantContain: []string{"large", "moonshot"},
		},
		{
			name:      "not shown for moonshot feature",
			pipeline:  feature.PipelineMoonshot,
			status:    feature.StatusImplementing,
			wantEmpty: true,
		},
		{
			name:        "rewind prompt for failed medium",
			pipeline:    feature.PipelineMedium,
			status:      feature.StatusFailed,
			wantContain: []string{"ctrl+r", "Rewind & Upgrade", "medium"},
		},
		{
			name:        "rewind prompt for interrupted large",
			pipeline:    feature.PipelineLarge,
			status:      feature.StatusInterrupted,
			wantContain: []string{"ctrl+r", "Rewind & Upgrade", "large"},
		},
		{
			name:        "ctrl+r hint shown for running medium too",
			pipeline:    feature.PipelineMedium,
			status:      feature.StatusImplementing,
			wantContain: []string{"medium", "ctrl+r"},
		},
		{
			name:        "upgrade path medium shows large or moonshot",
			pipeline:    feature.PipelineMedium,
			status:      feature.StatusImplementing,
			wantContain: []string{"large or moonshot"},
		},
		{
			name:           "upgrade path large shows moonshot only",
			pipeline:       feature.PipelineLarge,
			status:         feature.StatusImplementing,
			wantContain:    []string{"moonshot"},
			wantNotContain: []string{"large or moonshot"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &feature.Feature{
				Pipeline: tt.pipeline,
				Status:   tt.status,
			}
			got := renderLightbulbHint(f, 60)
			if tt.wantEmpty {
				if got != "" {
					t.Errorf("expected empty, got %q", got)
				}
				return
			}
			for _, s := range tt.wantContain {
				if !strings.Contains(got, s) {
					t.Errorf("expected %q in output, got %q", s, got)
				}
			}
			for _, s := range tt.wantNotContain {
				if strings.Contains(got, s) {
					t.Errorf("did not expect %q in output, got %q", s, got)
				}
			}
		})
	}
}

func TestRenderMetadataFull_WorkDir_FullPathForSingleRepo(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:     "feat-1",
		Slug:   "test",
		Status: feature.StatusImplementing,
		Repos:  []feature.FeatureRepo{{Name: "payments", WorktreePath: "/tmp/wt/feat/repo-a"}},
		Models: config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
	}
	m := NewDetailModel(f, "")
	got := m.renderMetadataFull(f)
	if !strings.Contains(got, "/tmp/wt/feat/repo-a") {
		t.Errorf("renderMetadataFull() = %q, want full worktree path for single-repo", got)
	}
}

func TestRenderMetadataFull_WorkDir_ParentDirForMultiRepo(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:     "feat-1",
		Slug:   "test",
		Status: feature.StatusImplementing,
		Repos: []feature.FeatureRepo{
			{Name: "payments", WorktreePath: "/tmp/wt/feat/repo-a"},
			{Name: "worker", WorktreePath: "/tmp/wt/feat/repo-b"},
		},
		Models: config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
	}
	m := NewDetailModel(f, "")
	got := m.renderMetadataFull(f)
	if !strings.Contains(got, "/tmp/wt/feat") {
		t.Errorf("renderMetadataFull() = %q, want parent dir of worktree for multi-repo", got)
	}
	if strings.Contains(got, "/tmp/wt/feat/repo-a") {
		t.Errorf("renderMetadataFull() = %q, did not expect first-repo full path for multi-repo", got)
	}
}

func TestRenderMetadataCompact_WorkDir_FullPathForSingleRepo(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:     "feat-1",
		Slug:   "test",
		Status: feature.StatusImplementing,
		Repos:  []feature.FeatureRepo{{Name: "payments", WorktreePath: "/tmp/wt/feat/repo-a"}},
		Models: config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
	}
	m := NewDetailModel(f, "")
	got := m.renderMetadataCompact(f)
	if !strings.Contains(got, "/tmp/wt/feat/repo-a") {
		t.Errorf("renderMetadataCompact() = %q, want full worktree path for single-repo", got)
	}
}

func TestRenderMetadataCompact_WorkDir_ParentDirForMultiRepo(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:     "feat-1",
		Slug:   "test",
		Status: feature.StatusImplementing,
		Repos: []feature.FeatureRepo{
			{Name: "payments", WorktreePath: "/tmp/wt/feat/repo-a"},
			{Name: "worker", WorktreePath: "/tmp/wt/feat/repo-b"},
		},
		Models: config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
	}
	m := NewDetailModel(f, "")
	got := m.renderMetadataCompact(f)
	if !strings.Contains(got, "/tmp/wt/feat") {
		t.Errorf("renderMetadataCompact() = %q, want parent dir of worktree for multi-repo", got)
	}
	if strings.Contains(got, "/tmp/wt/feat/repo-a") {
		t.Errorf("renderMetadataCompact() = %q, did not expect first-repo full path for multi-repo", got)
	}
}

func TestRenderPhaseProgressFull_KBSubRows_HiddenForSingleRepo(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "feat-1",
		Slug:         "test",
		Status:       feature.StatusBuildingKB,
		CurrentPhase: feature.PhaseKnowledgeBase,
		Repos:        []feature.FeatureRepo{{Name: "payments"}},
		KBStatus:     map[string]string{"payments": "completed"},
		Models:       config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
	}
	m := NewDetailModel(f, "")
	got := m.renderPhaseProgressFull(f)
	if strings.Contains(got, "↳") {
		t.Errorf("renderPhaseProgressFull() = %q, did not expect KB sub-row marker for single-repo", got)
	}
}

func TestRenderPhaseProgressFull_KBSubRows_RenderedForMultiRepo(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "feat-1",
		Slug:         "test",
		Status:       feature.StatusBuildingKB,
		CurrentPhase: feature.PhaseKnowledgeBase,
		Repos:        []feature.FeatureRepo{{Name: "payments"}, {Name: "worker"}},
		KBStatus:     map[string]string{"payments": "completed", "worker": "pending"},
		Models:       config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
	}
	m := NewDetailModel(f, "")
	got := m.renderPhaseProgressFull(f)
	if !strings.Contains(got, "↳") {
		t.Fatalf("renderPhaseProgressFull() = %q, want KB sub-row marker in multi-repo", got)
	}
	if !strings.Contains(got, "payments") {
		t.Errorf("renderPhaseProgressFull() = %q, want payments name in KB sub-rows", got)
	}
	if !strings.Contains(got, "worker") {
		t.Errorf("renderPhaseProgressFull() = %q, want worker name in KB sub-rows", got)
	}
}

func TestRenderPhaseProgress_KBSubRows_HiddenForSingleRepo(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "feat-1",
		Slug:         "test",
		Status:       feature.StatusBuildingKB,
		CurrentPhase: feature.PhaseKnowledgeBase,
		Repos:        []feature.FeatureRepo{{Name: "payments"}},
		KBStatus:     map[string]string{"payments": "completed"},
		Models:       config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
	}
	m := NewDetailModel(f, "")
	got := m.renderPhaseProgress(f)
	if strings.Contains(got, "↳") {
		t.Errorf("renderPhaseProgress() = %q, did not expect KB sub-row marker for single-repo", got)
	}
}

func TestRenderPhaseProgress_KBSubRows_RenderedForMultiRepo(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "feat-1",
		Slug:         "test",
		Status:       feature.StatusBuildingKB,
		CurrentPhase: feature.PhaseKnowledgeBase,
		Repos:        []feature.FeatureRepo{{Name: "payments"}, {Name: "worker"}},
		KBStatus:     map[string]string{"payments": "completed", "worker": "pending"},
		Models:       config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
	}
	m := NewDetailModel(f, "")
	got := m.renderPhaseProgress(f)
	if !strings.Contains(got, "↳") {
		t.Fatalf("renderPhaseProgress() = %q, want KB sub-row marker in multi-repo", got)
	}
	if !strings.Contains(got, "payments") {
		t.Errorf("renderPhaseProgress() = %q, want payments name in KB sub-rows", got)
	}
	if !strings.Contains(got, "worker") {
		t.Errorf("renderPhaseProgress() = %q, want worker name in KB sub-rows", got)
	}
}
