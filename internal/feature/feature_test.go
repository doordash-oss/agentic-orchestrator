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

package feature

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestTransitionValid(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure state-machine table with no shared state.
	tests := []struct {
		name string
		from Status
		to   Status
	}{
		{"Created->Inquiring", StatusCreated, StatusInquiring},
		{"Created->Researching", StatusCreated, StatusResearching},
		{"Created->BuildingKB", StatusCreated, StatusBuildingKB},
		{"Created->PlanReady", StatusCreated, StatusPlanReady},
		{"Created->Failed", StatusCreated, StatusFailed},
		{"Researching->DesignReady", StatusResearching, StatusDesignReady},
		{"Researching->PlanReady", StatusResearching, StatusPlanReady},
		{"Researching->Failed", StatusResearching, StatusFailed},
		{"Researching->Interrupted", StatusResearching, StatusInterrupted},
		{"BuildingKB->Created", StatusBuildingKB, StatusCreated},
		{"BuildingKB->Failed", StatusBuildingKB, StatusFailed},
		{"BuildingKB->Interrupted", StatusBuildingKB, StatusInterrupted},
		{"Inquiring->InquireReady", StatusInquiring, StatusInquireReady},
		{"Inquiring->Failed", StatusInquiring, StatusFailed},
		{"Inquiring->Interrupted", StatusInquiring, StatusInterrupted},
		{"InquireReady->Researching", StatusInquireReady, StatusResearching},
		{"InquireReady->Failed", StatusInquireReady, StatusFailed},
		{"DesignReady->Designing", StatusDesignReady, StatusDesigning},
		{"DesignReady->Failed", StatusDesignReady, StatusFailed},
		{"Designing->PlanReady", StatusDesigning, StatusPlanReady},
		{"Designing->Failed", StatusDesigning, StatusFailed},
		{"Designing->Interrupted", StatusDesigning, StatusInterrupted},
		{"PlanReady->Planning", StatusPlanReady, StatusPlanning},
		{"PlanReady->Failed", StatusPlanReady, StatusFailed},
		{"Planning->ImplementReady", StatusPlanning, StatusImplementReady},
		{"Planning->PlanNeedsReview", StatusPlanning, StatusPlanNeedsReview},
		{"Planning->Failed", StatusPlanning, StatusFailed},
		{"Planning->Interrupted", StatusPlanning, StatusInterrupted},
		{"ImplementReady->Implementing", StatusImplementReady, StatusImplementing},
		{"ImplementReady->Failed", StatusImplementReady, StatusFailed},
		{"Implementing->ReviewPassed", StatusImplementing, StatusReviewPassed},
		{"Implementing->ImplementReady", StatusImplementing, StatusImplementReady},
		{"Implementing->NeedUserInput", StatusImplementing, StatusNeedUserInput},
		{"Implementing->Failed", StatusImplementing, StatusFailed},
		{"Implementing->Interrupted", StatusImplementing, StatusInterrupted},
		{"NeedUserInput->Implementing", StatusNeedUserInput, StatusImplementing},
		{"NeedUserInput->Failed", StatusNeedUserInput, StatusFailed},
		{"NeedUserInput->Interrupted", StatusNeedUserInput, StatusInterrupted},
		{"ReviewPassed->CodeReady", StatusReviewPassed, StatusCodeReady},
		{"ReviewPassed->Done", StatusReviewPassed, StatusDone},
		{"ReviewPassed->Implementing", StatusReviewPassed, StatusImplementing},
		{"ReviewPassed->ImplementReady", StatusReviewPassed, StatusImplementReady},
		{"ReviewPassed->Failed", StatusReviewPassed, StatusFailed},
		{"ReviewPassed->Planning", StatusReviewPassed, StatusPlanning},
		{"ReviewPassed->Reviewing", StatusReviewPassed, StatusReviewing},
		{"ReviewPassed->FinalReviewing", StatusReviewPassed, StatusFinalReviewing},
		{"FinalReviewing->CodeReady", StatusFinalReviewing, StatusCodeReady},
		{"FinalReviewing->ReviewPassed", StatusFinalReviewing, StatusReviewPassed},
		{"FinalReviewing->Failed", StatusFinalReviewing, StatusFailed},
		{"FinalReviewing->Interrupted", StatusFinalReviewing, StatusInterrupted},
		{"Reviewing->CodeReady", StatusReviewing, StatusCodeReady},
		{"Reviewing->Failed", StatusReviewing, StatusFailed},
		{"Reviewing->Interrupted", StatusReviewing, StatusInterrupted},
		{"CodeReady->Published", StatusCodeReady, StatusPublished},
		{"CodeReady->Done", StatusCodeReady, StatusDone},
		{"CodeReady->Failed", StatusCodeReady, StatusFailed},
		{"CodeReady->ImplementReady", StatusCodeReady, StatusImplementReady},
		{"CodeReady->Inquiring", StatusCodeReady, StatusInquiring},
		{"Published->Done", StatusPublished, StatusDone},
		{"Published->Failed", StatusPublished, StatusFailed},
		{"Published->ImplementReady", StatusPublished, StatusImplementReady},
		{"Published->Inquiring", StatusPublished, StatusInquiring},
		{"Published->PlanReady", StatusPublished, StatusPlanReady},
		{"Failed->Created", StatusFailed, StatusCreated},
		{"Failed->BuildingKB", StatusFailed, StatusBuildingKB},
		{"Failed->Inquiring", StatusFailed, StatusInquiring},
		{"Failed->Researching", StatusFailed, StatusResearching},
		{"Failed->Designing", StatusFailed, StatusDesigning},
		{"Failed->ImplementReady", StatusFailed, StatusImplementReady},
		{"Failed->CodeReady", StatusFailed, StatusCodeReady},
		{"Interrupted->BuildingKB", StatusInterrupted, StatusBuildingKB},
		{"Interrupted->Inquiring", StatusInterrupted, StatusInquiring},
		{"Interrupted->Researching", StatusInterrupted, StatusResearching},
		{"Interrupted->Designing", StatusInterrupted, StatusDesigning},
		{"Interrupted->Planning", StatusInterrupted, StatusPlanning},
		{"Interrupted->Implementing", StatusInterrupted, StatusImplementing},
		{"Interrupted->FinalReviewing", StatusInterrupted, StatusFinalReviewing},
		{"Interrupted->Failed", StatusInterrupted, StatusFailed},
		{"PlanNeedsReview->Planning", StatusPlanNeedsReview, StatusPlanning},
		{"PlanNeedsReview->ImplementReady", StatusPlanNeedsReview, StatusImplementReady},
		{"PlanNeedsReview->Failed", StatusPlanNeedsReview, StatusFailed},
		{"PromptNeedsReview->Inquiring", StatusPromptNeedsReview, StatusInquiring},
		{"PromptNeedsReview->Failed", StatusPromptNeedsReview, StatusFailed},
		{"InquiryNeedsReview->Researching", StatusInquiryNeedsReview, StatusResearching},
		{"InquiryNeedsReview->Failed", StatusInquiryNeedsReview, StatusFailed},
		{"ResearchNeedsReview->Designing", StatusResearchNeedsReview, StatusDesigning},
		{"ResearchNeedsReview->Failed", StatusResearchNeedsReview, StatusFailed},
		{"DesignNeedsReview->Planning", StatusDesignNeedsReview, StatusPlanning},
		{"DesignNeedsReview->Failed", StatusDesignNeedsReview, StatusFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &Feature{Status: tt.from}
			if err := f.Transition(tt.to); err != nil {
				t.Errorf("expected valid transition from %s to %s, got error: %v", tt.from, tt.to, err)
			}
			if f.Status != tt.to {
				t.Errorf("status should be %s, got %s", tt.to, f.Status)
			}
		})
	}
}

func TestTransitionInvalid(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure state-machine table with no shared state.
	tests := []struct {
		name string
		from Status
		to   Status
	}{
		{"created to implementing", StatusCreated, StatusImplementing},
		{"created to done", StatusCreated, StatusDone},
		{"researching to implementing", StatusResearching, StatusImplementing},
		{"done to created", StatusDone, StatusCreated},
		{"implementing to done", StatusImplementing, StatusDone},
		{"plan ready to pr ready", StatusPlanReady, StatusCodeReady},
		{"plan needs review to researching", StatusPlanNeedsReview, StatusResearching},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &Feature{Status: tt.from}
			if err := f.Transition(tt.to); err == nil {
				t.Errorf("expected error for invalid transition from %s to %s", tt.from, tt.to)
			}
		})
	}
}

func TestPhaseString(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		phase Phase
		want  string
	}{
		{PhaseResearch, "Research"},
		{PhaseInquire, "Inquire"},
		{PhaseDesign, "Design"},
		{PhaseDesign, "Design"},
		{PhasePlan, "Plan"},
		{PhaseImplement, "Implement"},
		{PhasePublish, "Publish"},
	}
	for _, tt := range tests {
		if got := tt.phase.String(); got != tt.want {
			t.Errorf("Phase(%d).String() = %s, want %s", tt.phase, got, tt.want)
		}
	}
}

// TestPhaseRequiresGrilling pins the set of phases whose prompts contain a
// [grill-me] directive. Session builders override the user's Claude Code
// "auto" defaultMode for these phases so the directive is not suppressed by
// auto mode's "work without stopping for clarifying questions" reminder.
func TestPhaseRequiresGrilling(t *testing.T) {
	t.Parallel()
	tests := []struct {
		phase Phase
		want  bool
	}{
		{PhaseInquire, true},
		{PhaseDesign, true},
		{PhasePlan, true},
		{PhaseResearch, false},
		{PhaseImplement, false},
		{PhaseReview, false},
		{PhaseFinalReview, false},
		{PhaseKnowledgeBase, false},
		{PhasePublish, false},
	}
	for _, tt := range tests {
		if got := tt.phase.RequiresGrilling(); got != tt.want {
			t.Errorf("Phase(%s).RequiresGrilling() = %v, want %v", tt.phase.String(), got, tt.want)
		}
	}
}

func TestRoadmapPhaseFrontendHelpersAndPersistence(t *testing.T) {
	t.Parallel()

	f := &Feature{ID: "frontend-feature", Name: "Frontend Feature", Slug: "frontend-feature", ActiveRun: 1, RunCount: 1, SchemaVersion: SchemaVersionCurrent}
	if f.AnyRoadmapPhaseFrontend() {
		t.Fatal("AnyRoadmapPhaseFrontend() = true, want false for an unrecorded feature")
	}
	f.SetRoadmapPhaseFrontend(1, false)
	f.SetRoadmapPhaseFrontend(2, true)
	f.SetRoadmapPhaseFrontend(3, false)
	f.SetRoadmapPhaseFrontend(0, true)
	if f.RoadmapPhaseFrontend(0) {
		t.Fatal("RoadmapPhaseFrontend(0) = true, want false for invalid phase")
	}
	if !f.AnyRoadmapPhaseFrontend() {
		t.Fatal("AnyRoadmapPhaseFrontend() = false, want true after phase 2 was recorded frontend")
	}
	if f.RoadmapPhaseFrontend(1) {
		t.Error("RoadmapPhaseFrontend(1) = true, want false")
	}
	if !f.RoadmapPhaseFrontend(2) {
		t.Error("RoadmapPhaseFrontend(2) = false, want true")
	}
	if f.RoadmapPhaseFrontend(99) {
		t.Error("RoadmapPhaseFrontend(99) = true, want false for absent phase")
	}

	store := NewStore(t.TempDir())
	if err := store.Save(f); err != nil {
		t.Fatalf("Save(feature) error = %v", err)
	}
	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load(feature) error = %v", err)
	}
	if !loaded.AnyRoadmapPhaseFrontend() || !loaded.RoadmapPhaseFrontend(2) {
		t.Fatalf("loaded frontend map lost: %#v", loaded.RoadmapPhaseFrontendByPhase)
	}
	if got := loaded.Run().RoadmapPhaseFrontendByPhase[2]; !got {
		t.Fatalf("loaded run RoadmapPhaseFrontendByPhase[2] = %v, want true", got)
	}
}

func TestPhaseDirName(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		phase Phase
		want  string
	}{
		{PhaseResearch, "research"},
		{PhaseInquire, "inquire"},
		{PhaseDesign, "design"},
		{PhasePlan, "plan"},
		{PhaseImplement, "implement"},
		{PhasePublish, "publish"},
	}
	for _, tt := range tests {
		if got := tt.phase.DirName(); got != tt.want {
			t.Errorf("Phase(%d).DirName() = %s, want %s", tt.phase, got, tt.want)
		}
	}
}

func TestStatusString(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		status Status
		want   string
	}{
		{StatusCreated, "Created"},
		{StatusResearching, "Researching"},
		{StatusImplementing, "Implementing"},
		{StatusDone, "Done"},
		{StatusFailed, "Failed"},
		{StatusInquiring, "Inquiring"},
		{StatusInquireReady, "InquireReady"},
		{StatusDesignReady, "DesignReady"},
		{StatusDesigning, "Designing"},
		{StatusDesignReady, "DesignReady"},
		{StatusDesigning, "Designing"},
	}
	for _, tt := range tests {
		if got := tt.status.String(); got != tt.want {
			t.Errorf("Status(%d).String() = %s, want %s", tt.status, got, tt.want)
		}
	}
}

func TestStatusYAMLRoundTrip(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	// Test that all statuses survive a YAML round-trip as strings
	statuses := []Status{
		StatusCreated, StatusResearching, StatusPlanReady, StatusPlanning,
		StatusImplementReady, StatusImplementing, StatusReviewPassed,
		StatusCodeReady, StatusPublished, StatusFailed, StatusInterrupted,
		StatusDone, StatusBuildingKB,
		StatusPlanNeedsReview, StatusInquiring, StatusInquireReady,
		StatusDesignReady, StatusDesigning, StatusNeedUserInput,
	}
	for _, s := range statuses {
		data, err := yaml.Marshal(s)
		if err != nil {
			t.Fatalf("Marshal %v: %v", s, err)
		}
		var got Status
		if err := yaml.Unmarshal(data, &got); err != nil {
			t.Fatalf("Unmarshal %v from %q: %v", s, string(data), err)
		}
		if got != s {
			t.Errorf("round-trip: got %v, want %v (yaml: %q)", got, s, string(data))
		}
	}
}

// TestDesignStatusBehavior pins the Design status names, transitions, and
// canonical YAML serialization.
func TestDesignStatusBehavior(t *testing.T) {
	t.Parallel()

	if got := StatusDesigning.String(); got != "Designing" {
		t.Errorf("StatusDesigning.String() = %q, want %q", got, "Designing")
	}
	if got := StatusDesignReady.String(); got != "DesignReady" {
		t.Errorf("StatusDesignReady.String() = %q, want %q", got, "DesignReady")
	}
	if got := PhaseDesign.String(); got != "Design" {
		t.Errorf("PhaseDesign.String() = %q, want %q", got, "Design")
	}

	roundTrip := map[string]Status{
		"Designing":   StatusDesigning,
		"DesignReady": StatusDesignReady,
	}
	for in, want := range roundTrip {
		var got Status
		if err := yaml.Unmarshal([]byte(in), &got); err != nil {
			t.Errorf("Unmarshal %q: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("Unmarshal %q: got %v want %v", in, got, want)
		}
	}

	out, err := yaml.Marshal(StatusDesigning)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !bytes.Contains(out, []byte("Designing")) {
		t.Errorf("Marshal(StatusDesigning) = %q, expected canonical %q", string(out), "Designing")
	}

	for _, tc := range []struct {
		from, to Status
	}{
		{StatusResearching, StatusDesignReady},
		{StatusDesignReady, StatusDesigning},
		{StatusDesigning, StatusPlanReady},
		{StatusDesigning, StatusFailed},
		{StatusDesigning, StatusInterrupted},
		{StatusFailed, StatusDesigning},
		{StatusInterrupted, StatusDesigning},
	} {
		f := &Feature{Status: tc.from}
		if err := f.Transition(tc.to); err != nil {
			t.Errorf("expected transition %s -> %s, got: %v", tc.from, tc.to, err)
		}
	}
}

func TestStatusYAMLRejectsNonCanonicalValues(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	for _, value := range []string{"PR" + "Ready", "Brain" + "stormReady", "Brain" + "storming", "0", "12"} {
		t.Run(value, func(t *testing.T) {
			var got Status
			err := yaml.Unmarshal([]byte(value), &got)
			if err == nil || !strings.Contains(err.Error(), "unknown status") {
				t.Errorf("Unmarshal(%q) error = %v, want unknown status error", value, err)
			}
		})
	}
}

func TestSlugify(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name string
		want string
	}{
		{"Fix Database Query", "fix-database-query"},
		{"add-auth-middleware", "add-auth-middleware"},
		{"Hello World!", "hello-world"},
		{"  spaces  everywhere  ", "spaces-everywhere"},
		{"UPPERCASE", "uppercase"},
		{"special!@#$%chars", "special-chars"},
		{"a-very-long-feature-name-that-exceeds-forty-characters-limit", "a-very-long-feature-name-that-exceeds-fo"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Slugify(tt.name)
			if got != tt.want {
				t.Errorf("Slugify(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestIsRunning(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		status Status
		want   bool
	}{
		{StatusCreated, false},
		{StatusResearching, true},
		{StatusPlanReady, false},
		{StatusPlanning, true},
		{StatusImplementReady, false},
		{StatusImplementing, true},
		{StatusReviewPassed, false},
		{StatusCodeReady, false},
		{StatusPublished, false},
		{StatusDone, false},
		{StatusFailed, false},
		{StatusInterrupted, false},
		{StatusBuildingKB, true},
		{StatusInquiring, true},
		{StatusInquireReady, false},
		{StatusDesignReady, false},
		{StatusDesigning, true},
		{StatusPlanNeedsReview, false},
	}
	for _, tt := range tests {
		t.Run(tt.status.String(), func(t *testing.T) {
			if got := tt.status.IsRunning(); got != tt.want {
				t.Errorf("Status(%s).IsRunning() = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestTransitionAccumulatesTime(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	start := time.Now().Add(-5 * time.Minute)
	f := &Feature{
		Status:           StatusResearching,
		ActiveTimingKey:  "research",
		ActivePhaseStart: &start,
	}

	err := f.Transition(StatusPlanReady)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if f.PhaseTimings == nil {
		t.Fatal("expected PhaseTimings to be initialized")
	}
	d := f.PhaseTimings["research"]
	if d < 5*time.Minute {
		t.Errorf("expected at least 5m accumulated, got %v", d)
	}
	if f.ActivePhaseStart != nil {
		t.Error("expected ActivePhaseStart to be nil")
	}
	// ActiveTimingKey is intentionally preserved so cycle keys survive resume
	if f.ActiveTimingKey != "research" {
		t.Errorf("expected ActiveTimingKey to be preserved as 'research', got %q", f.ActiveTimingKey)
	}
}

func TestTransitionDoesNotAccumulateWithinRunning(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	f := &Feature{
		Status:          StatusResearching,
		ActiveTimingKey: "research",
	}
	_ = f.Transition(StatusPlanReady)
	_ = f.Transition(StatusPlanning)
	if f.ActivePhaseStart != nil {
		t.Error("ActivePhaseStart should be nil after leaving running state")
	}
}

func TestTotalRuntimeWithPhaseTimings(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	f := &Feature{
		PhaseTimings: map[string]time.Duration{
			"research": 5 * time.Minute,
			"plan":     10 * time.Minute,
		},
	}
	total := f.TotalRuntime()
	if total != 15*time.Minute {
		t.Errorf("TotalRuntime() = %v, want 15m", total)
	}
}

func TestTotalRuntimeWithActivePhase(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	start := time.Now().Add(-3 * time.Minute)
	f := &Feature{
		PhaseTimings: map[string]time.Duration{
			"research": 5 * time.Minute,
		},
		ActivePhaseStart: &start,
		ActiveTimingKey:  "plan",
	}
	total := f.TotalRuntime()
	if total < 8*time.Minute {
		t.Errorf("TotalRuntime() = %v, want >= 8m", total)
	}
}

func TestTotalRuntimeLegacyFallback(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	start := time.Now().Add(-10 * time.Minute)
	f := &Feature{
		StartedAt: &start,
	}
	total := f.TotalRuntime()
	if total < 10*time.Minute {
		t.Errorf("TotalRuntime() = %v, want >= 10m (legacy fallback)", total)
	}
}

func TestTotalRuntimeNoData(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	f := &Feature{}
	if f.TotalRuntime() != 0 {
		t.Error("expected 0 for feature with no timing data")
	}
}

func TestPhaseRuntime(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	start := time.Now().Add(-2 * time.Minute)
	f := &Feature{
		PhaseTimings: map[string]time.Duration{
			"research":  5 * time.Minute,
			"implement": 30 * time.Minute,
		},
		ActiveTimingKey:  "implement",
		ActivePhaseStart: &start,
	}

	if d := f.PhaseRuntime("research"); d != 5*time.Minute {
		t.Errorf("PhaseRuntime(research) = %v, want 5m", d)
	}

	d := f.PhaseRuntime("implement")
	if d < 32*time.Minute {
		t.Errorf("PhaseRuntime(implement) = %v, want >= 32m", d)
	}

	if d := f.PhaseRuntime("publish"); d != 0 {
		t.Errorf("PhaseRuntime(publish) = %v, want 0", d)
	}
}

func TestTransitionToFailedAccumulatesTime(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	start := time.Now().Add(-3 * time.Minute)
	f := &Feature{
		Status:           StatusImplementing,
		ActiveTimingKey:  "implement",
		ActivePhaseStart: &start,
	}
	_ = f.Transition(StatusFailed)
	if d := f.PhaseTimings["implement"]; d < 3*time.Minute {
		t.Errorf("expected at least 3m accumulated on failure, got %v", d)
	}
}

func TestTransitionToInterruptedAccumulatesTime(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	start := time.Now().Add(-7 * time.Minute)
	f := &Feature{
		Status:           StatusPlanning,
		ActiveTimingKey:  "plan",
		ActivePhaseStart: &start,
	}
	_ = f.Transition(StatusInterrupted)
	if d := f.PhaseTimings["plan"]; d < 7*time.Minute {
		t.Errorf("expected at least 7m accumulated on interruption, got %v", d)
	}
}

func TestResumeRebaseCyclePreservesTimingKey(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	// Simulate: implementing during rebase-1, interrupted, then resumed
	start := time.Now().Add(-10 * time.Minute)
	f := &Feature{
		Status:           StatusImplementing,
		CurrentPhase:     PhaseImplement,
		ActiveTimingKey:  "rebase-1",
		ActivePhaseStart: &start,
	}
	f.SetRebaseCount(1)

	// Interrupt accumulates time but preserves ActiveTimingKey
	if err := f.Transition(StatusInterrupted); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d := f.PhaseTimings["rebase-1"]; d < 10*time.Minute {
		t.Errorf("expected at least 10m accumulated for rebase-1, got %v", d)
	}
	if f.ActiveTimingKey != "rebase-1" {
		t.Errorf("expected ActiveTimingKey preserved as 'rebase-1', got %q", f.ActiveTimingKey)
	}

	// Resume: transition back to Implementing (via Interrupted → Implementing)
	if err := f.Transition(StatusImplementing); err != nil {
		t.Fatalf("unexpected error resuming: %v", err)
	}
	// isImplementTimingKey should recognize this as an implement key
	if !isImplementTimingKey(f.ActiveTimingKey) {
		t.Errorf("expected 'rebase-1' to be recognized as implement timing key")
	}
	// Key should still be "rebase-1" — not overwritten
	if f.ActiveTimingKey != "rebase-1" {
		t.Errorf("expected ActiveTimingKey to remain 'rebase-1' after resume, got %q", f.ActiveTimingKey)
	}
}

func TestIsImplementTimingKey(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		key  string
		want bool
	}{
		{"implement", true},
		{"rebase-1", true},
		{"rebase-10", true},
		{"research", false},
		{"plan", false},
		{"", false},
		{"publish", false},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			if got := isImplementTimingKey(tt.key); got != tt.want {
				t.Errorf("isImplementTimingKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestAddPhaseCost(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	f := &Feature{}

	f.AddPhaseCost("research", 0.50)
	f.AddPhaseCost("implement", 1.25)

	if got := f.TotalCost(); got != 1.75 {
		t.Errorf("TotalCost() = %v, want 1.75", got)
	}
	if got := f.PhaseCost("research"); got != 0.50 {
		t.Errorf("PhaseCost(research) = %v, want 0.50", got)
	}
	if got := f.PhaseCost("implement"); got != 1.25 {
		t.Errorf("PhaseCost(implement) = %v, want 1.25", got)
	}
}

func TestAddPhaseCostAccumulates(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	f := &Feature{}
	f.AddPhaseCost("implement", 0.50)
	f.AddPhaseCost("implement", 0.75)
	if got := f.PhaseCost("implement"); got != 1.25 {
		t.Errorf("PhaseCost(implement) after two adds = %v, want 1.25", got)
	}
}

func TestRecordSessionCostPersistsAndAggregates(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp-dir, no shared fixtures.
	store := NewStore(t.TempDir())
	f := &Feature{
		ID:            "test-session-costs",
		Status:        StatusCreated,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: SchemaVersionCurrent,
	}
	f.RecordSessionCost(SessionCostRecord{
		SessionID:     "sess-plan-1",
		PhaseKey:      "phase-1-plan",
		ObserverPhase: "review",
		RepoName:      "repo-a",
		CostUSD:       0.12,
	})
	f.RecordSessionCost(SessionCostRecord{
		SessionID:     "sess-plan-2",
		PhaseKey:      "phase-1-plan",
		ObserverPhase: "plan",
		RepoName:      "repo-a",
		CostUSD:       0.34,
	})
	if err := store.Save(f); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.PhaseCost("phase-1-plan") != 0.46 {
		t.Errorf("PhaseCost(phase-1-plan) = %v, want 0.46", got.PhaseCost("phase-1-plan"))
	}
	if got.TotalCost() != 0.46 {
		t.Errorf("TotalCost() = %v, want 0.46", got.TotalCost())
	}
	if len(got.SessionCosts) != 2 {
		t.Fatalf("len(SessionCosts) = %d, want 2", len(got.SessionCosts))
	}
	first := got.SessionCosts[0]
	if first.SessionID != "sess-plan-1" || first.PhaseKey != "phase-1-plan" || first.ObserverPhase != "review" || first.RepoName != "repo-a" || first.CostUSD != 0.12 {
		t.Errorf("SessionCosts[0] = %+v, want sess-plan-1 phase-1-plan review repo-a 0.12", first)
	}
	second := got.SessionCosts[1]
	if second.SessionID != "sess-plan-2" || second.PhaseKey != "phase-1-plan" || second.ObserverPhase != "plan" || second.RepoName != "repo-a" || second.CostUSD != 0.34 {
		t.Errorf("SessionCosts[1] = %+v, want sess-plan-2 phase-1-plan plan repo-a 0.34", second)
	}
}

func TestAddPhaseCostZeroNegativeNoop(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	f := &Feature{}
	f.AddPhaseCost("research", 0)
	f.AddPhaseCost("research", -1.0)
	if f.PhaseCosts != nil {
		t.Error("expected PhaseCosts to remain nil for zero/negative costs")
	}
	if f.TotalCost() != 0 {
		t.Error("expected TotalCost() == 0")
	}
}

func TestPhaseCostNilMap(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	f := &Feature{}
	if got := f.PhaseCost("research"); got != 0 {
		t.Errorf("PhaseCost on nil map = %v, want 0", got)
	}
	if got := f.TotalCost(); got != 0 {
		t.Errorf("TotalCost on nil map = %v, want 0", got)
	}
}

func TestPhaseCostsYAMLRoundTrip(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	// Per-run fields now live on the companion Run struct; the persistence
	// round-trip happens via Store (feature.yaml + runs/run-NNN/run.yaml).
	store := NewStore(t.TempDir())
	f := &Feature{
		ID:            "test",
		Status:        StatusCreated,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: SchemaVersionCurrent,
		PhaseCosts: map[string]float64{
			"research":  0.45,
			"plan":      0.30,
			"implement": 2.50,
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.TotalCost() != f.TotalCost() {
		t.Errorf("round-trip TotalCost: got %v, want %v", got.TotalCost(), f.TotalCost())
	}
	for _, key := range []string{"research", "plan", "implement"} {
		if got.PhaseCost(key) != f.PhaseCost(key) {
			t.Errorf("round-trip PhaseCost(%s): got %v, want %v", key, got.PhaseCost(key), f.PhaseCost(key))
		}
	}
}

func TestPhaseCostsOmittedWhenNil(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	f := &Feature{
		ID:     "test",
		Status: StatusCreated,
	}
	data, err := yaml.Marshal(f)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if containsBytes(data, []byte("phase_costs")) {
		t.Error("expected phase_costs to be omitted from YAML when nil")
	}
}

func containsBytes(haystack, needle []byte) bool {
	for i := 0; i <= len(haystack)-len(needle); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func TestNormalFlowResetsTimingKeyForImplement(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	// When going through the normal flow (research → plan → implement),
	// the "plan" key persisting from the planning phase should NOT be
	// treated as an implement key, so StartImplementation should default
	// to "implement". We test the isImplementTimingKey guard here.
	if isImplementTimingKey("plan") {
		t.Error("'plan' should not be recognized as an implement timing key")
	}
	if isImplementTimingKey("research") {
		t.Error("'research' should not be recognized as an implement timing key")
	}
}

func TestPhaseKnowledgeBase(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	t.Run("String", func(t *testing.T) {
		if got := PhaseKnowledgeBase.String(); got != "Knowledge Base" {
			t.Errorf("PhaseKnowledgeBase.String() = %q, want %q", got, "Knowledge Base")
		}
	})
	t.Run("DirName", func(t *testing.T) {
		if got := PhaseKnowledgeBase.DirName(); got != "knowledgebase" {
			t.Errorf("PhaseKnowledgeBase.DirName() = %q, want %q", got, "knowledgebase")
		}
	})
}

func TestLogicalOrder(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		phase Phase
		order int
	}{
		{PhaseKnowledgeBase, 0},
		{PhaseInquire, 1},
		{PhaseResearch, 2},
		{PhaseDesign, 3},
		{PhasePlan, 4},
		{PhaseImplement, 5},
		{PhaseReview, 6},
		{PhasePublish, 7},
	}
	for _, tt := range tests {
		t.Run(tt.phase.String(), func(t *testing.T) {
			if got := tt.phase.LogicalOrder(); got != tt.order {
				t.Errorf("%s.LogicalOrder() = %d, want %d", tt.phase, got, tt.order)
			}
		})
	}

	// Verify ordering: each phase should have a strictly increasing order
	for i := 1; i < len(tests); i++ {
		if tests[i].order <= tests[i-1].order {
			t.Errorf("LogicalOrder not strictly increasing: %s (%d) <= %s (%d)",
				tests[i].phase, tests[i].order, tests[i-1].phase, tests[i-1].order)
		}
	}
}

func TestStatusBuildingKB(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	t.Run("String", func(t *testing.T) {
		if got := StatusBuildingKB.String(); got != "BuildingKB" {
			t.Errorf("StatusBuildingKB.String() = %q, want %q", got, "BuildingKB")
		}
	})

	t.Run("IsRunning", func(t *testing.T) {
		if !StatusBuildingKB.IsRunning() {
			t.Error("StatusBuildingKB.IsRunning() should be true")
		}
	})

	t.Run("YAML round-trip", func(t *testing.T) {
		data, err := yaml.Marshal(StatusBuildingKB)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var got Status
		if err := yaml.Unmarshal(data, &got); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if got != StatusBuildingKB {
			t.Errorf("round-trip: got %v, want %v", got, StatusBuildingKB)
		}
	})
}

func TestKBStatusPersistence(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	t.Run("round-trip with populated KBStatus", func(t *testing.T) {
		store := NewStore(t.TempDir())
		f := &Feature{
			ID:            "kb-status-test",
			Name:          "KB Status Test",
			Status:        StatusBuildingKB,
			ActiveRun:     1,
			RunCount:      1,
			SchemaVersion: SchemaVersionCurrent,
			KBStatus: map[string]string{
				"repo-a": "completed",
				"repo-b": "pending",
				"repo-c": "failed: timeout",
			},
		}
		if err := store.Save(f); err != nil {
			t.Fatalf("Save: %v", err)
		}
		got, err := store.Load(f.ID)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(got.KBStatus) != 3 {
			t.Fatalf("expected 3 KBStatus entries, got %d", len(got.KBStatus))
		}
		for _, key := range []string{"repo-a", "repo-b", "repo-c"} {
			if got.KBStatus[key] != f.KBStatus[key] {
				t.Errorf("KBStatus[%q] = %q, want %q", key, got.KBStatus[key], f.KBStatus[key])
			}
		}
	})

	t.Run("empty KBStatus omitted from feature.yaml", func(t *testing.T) {
		f := &Feature{
			ID:     "kb-status-empty",
			Name:   "KB Status Empty",
			Status: StatusCreated,
		}
		data, err := yaml.Marshal(f)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		// Under runs-first layout kb_status lives in run.yaml; the root-level
		// feature.yaml must never mention it.
		if containsBytes(data, []byte("kb_status")) {
			t.Error("expected kb_status to be absent from feature.yaml (lives in run.yaml)")
		}
	})
}

// TestInquireResearchDesignPlanProgression is a regression test ensuring the
// full state machine progression through the new phases works end-to-end.
func TestInquireResearchDesignPlanProgression(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	// Full lifecycle: Created → Inquiring → InquireReady → Researching → DesignReady → Designing → PlanReady → Planning
	transitions := []Status{
		StatusInquiring,
		StatusInquireReady,
		StatusResearching,
		StatusDesignReady,
		StatusDesigning,
		StatusPlanReady,
		StatusPlanning,
	}

	f := &Feature{Status: StatusCreated}
	for _, next := range transitions {
		prev := f.Status
		if err := f.Transition(next); err != nil {
			t.Fatalf("expected valid transition from %s to %s, got error: %v", prev, next, err)
		}
		if f.Status != next {
			t.Fatalf("status should be %s after transition from %s, got %s", next, prev, f.Status)
		}
	}
}

func TestKBTimingAccumulation(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	start := time.Now().Add(-2 * time.Minute)
	f := &Feature{
		Status:           StatusBuildingKB,
		ActiveTimingKey:  "knowledgebase",
		ActivePhaseStart: &start,
	}

	// Transition from BuildingKB (running) to Created (non-running) should accumulate
	err := f.Transition(StatusCreated)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.PhaseTimings == nil {
		t.Fatal("expected PhaseTimings to be initialized")
	}
	d := f.PhaseTimings["knowledgebase"]
	if d < 2*time.Minute {
		t.Errorf("expected at least 2m accumulated, got %v", d)
	}
}

func TestCheckpointsHasGateForPhase(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name  string
		cp    Checkpoints
		phase Phase
		want  bool
	}{
		{"inquiry gate for research", Checkpoints{InquiryReview: true}, PhaseResearch, true},
		{"research gate for design", Checkpoints{ResearchReview: true}, PhaseDesign, true},
		{"design gate for plan", Checkpoints{DesignReview: true}, PhasePlan, true},
		{"phase-plan gate for implement", Checkpoints{PhasePlanReview: true}, PhaseImplement, true},
		{"no gate for publish", Checkpoints{ManualPublish: true}, PhasePublish, false},
		{"no gate for KB", Checkpoints{InquiryReview: true}, PhaseKnowledgeBase, false},
		{"zero value has no gates", Checkpoints{}, PhaseResearch, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cp.HasGateForPhase(tc.phase); got != tc.want {
				t.Errorf("HasGateForPhase(%v) = %v, want %v", tc.phase, got, tc.want)
			}
		})
	}
}

func TestCheckpointsAutoPublish(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name string
		cp   Checkpoints
		want bool
	}{
		{"zero value is auto-publish", Checkpoints{}, true},
		{"manual publish off is auto-publish", Checkpoints{ManualPublish: false}, true},
		{"manual publish on is not auto-publish", Checkpoints{ManualPublish: true}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cp.AutoPublish(); got != tc.want {
				t.Errorf("AutoPublish() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCheckpointsYAMLRoundTrip(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	f := Feature{
		ID:   "test",
		Name: "test",
		Checkpoints: Checkpoints{
			InquiryReview:   true,
			ResearchReview:  false,
			DesignReview:    true,
			RoadmapReview:   false,
			PhasePlanReview: true,
			ManualPublish:   true,
		},
	}
	data, err := yaml.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var f2 Feature
	if err := yaml.Unmarshal(data, &f2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if f2.Checkpoints != f.Checkpoints {
		t.Errorf("round-trip mismatch: got %+v, want %+v", f2.Checkpoints, f.Checkpoints)
	}
}

func TestRoadmapFieldsYAMLRoundTrip(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	store := NewStore(t.TempDir())
	f := &Feature{
		ID:                  "test-roadmap",
		Name:                "Roadmap Test",
		Status:              StatusImplementing,
		ActiveRun:           1,
		RunCount:            1,
		SchemaVersion:       SchemaVersionCurrent,
		CurrentRoadmapPhase: 2,
		TotalRoadmapPhases:  5,
		RoadmapPhaseType:    "tdd-fill-in",
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.CurrentRoadmapPhase != 2 {
		t.Errorf("CurrentRoadmapPhase = %d, want 2", got.CurrentRoadmapPhase)
	}
	if got.TotalRoadmapPhases != 5 {
		t.Errorf("TotalRoadmapPhases = %d, want 5", got.TotalRoadmapPhases)
	}
	if got.RoadmapPhaseType != "tdd-fill-in" {
		t.Errorf("RoadmapPhaseType = %q, want %q", got.RoadmapPhaseType, "tdd-fill-in")
	}
}

func TestRoadmapPhaseCommitAnchorsYAMLRoundTrip(t *testing.T) {
	store := NewStore(t.TempDir())
	f := &Feature{
		ID:            "test-roadmap-anchors",
		Name:          "Roadmap Anchor Test",
		Status:        StatusImplementing,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: SchemaVersionCurrent,
	}
	f.Run().RoadmapPhaseCommitAnchors = map[int]map[string]string{
		1: {
			"repo-a": "1111111111111111111111111111111111111111",
			"repo-b": "2222222222222222222222222222222222222222",
		},
		2: {
			"repo-a": "3333333333333333333333333333333333333333",
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	anchors := got.Run().RoadmapPhaseCommitAnchors
	if got := anchors[1]["repo-a"]; got != "1111111111111111111111111111111111111111" {
		t.Errorf("phase 1 repo-a anchor = %q", got)
	}
	if got := anchors[1]["repo-b"]; got != "2222222222222222222222222222222222222222" {
		t.Errorf("phase 1 repo-b anchor = %q", got)
	}
	if got := anchors[2]["repo-a"]; got != "3333333333333333333333333333333333333333" {
		t.Errorf("phase 2 repo-a anchor = %q", got)
	}
}

func TestRunYAMLWithoutRoadmapPhaseCommitAnchorsLoads(t *testing.T) {
	var r Run
	data := []byte("run_number: 1\ncurrent_roadmap_phase: 2\n")
	if err := yaml.Unmarshal(data, &r); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if r.RoadmapPhaseCommitAnchors != nil {
		t.Errorf("RoadmapPhaseCommitAnchors = %#v, want nil", r.RoadmapPhaseCommitAnchors)
	}
}

func TestRewindRoadmapPhaseYAMLRoundTripAndLegacyOmit(t *testing.T) {
	roadmapPhase := 2
	r := Run{
		RunNumber:          1,
		RewindRoadmapPhase: &roadmapPhase,
	}
	data, err := yaml.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !containsBytes(data, []byte("rewind_roadmap_phase: 2")) {
		t.Fatalf("run.yaml missing rewind_roadmap_phase: %s", string(data))
	}
	var got Run
	if err := yaml.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.RewindRoadmapPhase == nil || *got.RewindRoadmapPhase != 2 {
		t.Fatalf("RewindRoadmapPhase = %v, want 2", got.RewindRoadmapPhase)
	}

	legacy := []byte("run_number: 1\nrewind_target: 2\n")
	var old Run
	if err := yaml.Unmarshal(legacy, &old); err != nil {
		t.Fatalf("Unmarshal legacy: %v", err)
	}
	if old.RewindRoadmapPhase != nil {
		t.Errorf("legacy RewindRoadmapPhase = %v, want nil", old.RewindRoadmapPhase)
	}
}

func TestRoadmapFieldsOmittedWhenZero(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	f := Feature{
		ID:     "test-no-roadmap",
		Name:   "No Roadmap",
		Status: StatusCreated,
	}
	data, err := yaml.Marshal(f)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, field := range []string{"current_roadmap_phase", "total_roadmap_phases", "roadmap_phase_type"} {
		if containsBytes(data, []byte(field)) {
			t.Errorf("expected %s to be omitted from YAML when zero, but found in output", field)
		}
	}
}

func TestCheckpointsZeroValueFromYAML(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	// Old features without checkpoints key should deserialize with zero value
	yamlData := []byte("id: old-feature\nname: old\n")
	var f Feature
	if err := yaml.Unmarshal(yamlData, &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if f.Checkpoints != (Checkpoints{}) {
		t.Errorf("expected zero Checkpoints, got %+v", f.Checkpoints)
	}
	if !f.Checkpoints.AutoPublish() {
		t.Error("zero Checkpoints should mean AutoPublish=true")
	}
}

func TestRepoStateYAMLRoundTrip(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	// RepoStates lives on Run and persists to run.yaml; the test rounds
	// through the Store so both files are exercised.
	store := NewStore(t.TempDir())
	f := &Feature{
		ID:            "test-123",
		Name:          "Test Feature",
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: SchemaVersionCurrent,
		RepoStates: map[string]*RepoState{
			"repo-a": {},
			"repo-b": {Touched: true, LastError: "some error"},
		},
	}

	if err := store.Save(f); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(got.RepoStates) != 2 {
		t.Fatalf("expected 2 repo state entries, got %d", len(got.RepoStates))
	}
	if got.RepoStates["repo-a"].Touched {
		t.Errorf("repo-a Touched = true, want false")
	}
	if got.RepoStates["repo-b"].LastError != "some error" {
		t.Errorf("repo-b last_error = %q, want %q", got.RepoStates["repo-b"].LastError, "some error")
	}
	if !got.RepoStates["repo-b"].Touched {
		t.Errorf("repo-b Touched = false, want true")
	}
}

func TestValidTransitionsNoRetiredStates(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	// Verify that no retired states appear in the transition table.
	// Build retired names dynamically so they don't match dead-reference greps.
	retired := map[string]bool{
		"Dependency" + "Blocked": true,
	}
	for from, targets := range validTransitions {
		if retired[from.String()] {
			t.Errorf("validTransitions has retired state %q as a source", from)
		}
		for _, to := range targets {
			if retired[to.String()] {
				t.Errorf("validTransitions[%s] includes retired state %q as target", from, to)
			}
		}
	}
}

func TestFirstRepoPRURL(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name     string
		repos    []FeatureRepo
		repoImpl map[string]*RepoState
		want     string
	}{
		{
			name:  "first repo has URL",
			repos: []FeatureRepo{{Name: "a"}, {Name: "b"}},
			repoImpl: map[string]*RepoState{
				"a": {PRURL: "url-a"},
				"b": {PRURL: "url-b"},
			},
			want: "url-a",
		},
		{
			name:  "second repo has URL first doesnt",
			repos: []FeatureRepo{{Name: "a"}, {Name: "b"}},
			repoImpl: map[string]*RepoState{
				"a": {PRURL: ""},
				"b": {PRURL: "url-b"},
			},
			want: "url-b",
		},
		{
			name:  "no repos have URLs",
			repos: []FeatureRepo{{Name: "a"}, {Name: "b"}},
			repoImpl: map[string]*RepoState{
				"a": {PRURL: ""},
				"b": {PRURL: ""},
			},
			want: "",
		},
		{
			name:     "empty RepoImpl",
			repos:    []FeatureRepo{{Name: "a"}},
			repoImpl: map[string]*RepoState{},
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &Feature{Repos: tt.repos, RepoStates: tt.repoImpl}
			got := f.FirstRepoPRURL()
			if got != tt.want {
				t.Errorf("FirstRepoPRURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestAllReposPublished covers the strangler-implant predicate that supersedes
// AllReposCodeReady. Reads exclusively from the new RepoState shape; legacy
// RepoImpl is dual-written but unread by orchestration.
func TestAllReposPublished(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name   string
		repos  []FeatureRepo
		states map[string]*RepoState
		want   bool
	}{
		{
			name:   "no repos configured returns false",
			repos:  nil,
			states: map[string]*RepoState{},
			want:   false,
		},
		{
			name:   "all repos untouched (vacuously published)",
			repos:  []FeatureRepo{{Name: "api"}, {Name: "web"}},
			states: map[string]*RepoState{},
			want:   true,
		},
		{
			name:  "all touched repos have PR URL",
			repos: []FeatureRepo{{Name: "api"}, {Name: "web"}},
			states: map[string]*RepoState{
				"api": {Touched: true, PRURL: "url-a"},
				"web": {Touched: true, PRURL: "url-b"},
			},
			want: true,
		},
		{
			name:  "one touched repo missing PR URL blocks",
			repos: []FeatureRepo{{Name: "api"}, {Name: "web"}},
			states: map[string]*RepoState{
				"api": {Touched: true, PRURL: "url-a"},
				"web": {Touched: true, PRURL: ""},
			},
			want: false,
		},
		{
			name:  "untouched mixed with published touched",
			repos: []FeatureRepo{{Name: "api"}, {Name: "web"}, {Name: "infra"}},
			states: map[string]*RepoState{
				"api":   {Touched: true, PRURL: "url-a"},
				"infra": {Touched: false},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &Feature{Repos: tt.repos, RepoStates: tt.states}
			if got := f.AllReposPublished(); got != tt.want {
				t.Errorf("AllReposPublished() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestTouchedRepos covers the strangler-implant FR-staged-subset reader.
func TestTouchedRepos(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name   string
		states map[string]*RepoState
		want   []string
	}{
		{
			name:   "nil map returns nil",
			states: nil,
			want:   nil,
		},
		{
			name:   "empty map returns nil",
			states: map[string]*RepoState{},
			want:   nil,
		},
		{
			name: "skips untouched and nil entries",
			states: map[string]*RepoState{
				"web":   nil,
				"api":   {Touched: false},
				"infra": {Touched: true},
			},
			want: []string{"infra"},
		},
		{
			name: "returns sorted touched names",
			states: map[string]*RepoState{
				"web":   {Touched: true},
				"api":   {Touched: true},
				"infra": {Touched: true},
			},
			want: []string{"api", "infra", "web"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &Feature{RepoStates: tt.states}
			got := f.TouchedRepos()
			if len(got) != len(tt.want) {
				t.Fatalf("TouchedRepos() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("TouchedRepos()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestCyclePrefix(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name            string
		activeCycleType RepoCycleType
		rebaseCount     int
		want            string
	}{
		{"no cycle", "", 0, ""},
		{"rebase count 1", CycleRebase, 1, "rebase-1"},
		{"rebase count 3", CycleRebase, 3, "rebase-3"},
		{"rebase count 0 fallback", CycleRebase, 0, "rebase"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &Feature{}
			f.SetActiveCycleType(tt.activeCycleType)
			f.SetRebaseCount(tt.rebaseCount)
			if got := f.CyclePrefix(); got != tt.want {
				t.Errorf("CyclePrefix() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRepoCycleTypeIsValidAcceptsOnlyRebase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cycleType RepoCycleType
		want      bool
	}{
		{name: "rebase", cycleType: CycleRebase, want: true},
		{name: "unknown", cycleType: RepoCycleType("unknown"), want: false},
		{name: "empty", cycleType: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.cycleType.IsValid(); got != tt.want {
				t.Errorf("RepoCycleType(%q).IsValid() = %v, want %v", tt.cycleType, got, tt.want)
			}
		})
	}
}

func TestRepoCycleDirName(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name      string
		cycleType RepoCycleType
		count     int
		want      string
	}{
		{"rebase count 1", CycleRebase, 1, "rebase-1"},
		{"rebase count 5", CycleRebase, 5, "rebase-5"},
		{"rebase count 0 fallback", CycleRebase, 0, "rebase"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RepoCycleDirName(tt.cycleType, tt.count); got != tt.want {
				t.Errorf("RepoCycleDirName(%q, %d) = %q, want %q", tt.cycleType, tt.count, got, tt.want)
			}
		})
	}
}

func TestEffectivePipeline(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name     string
		pipeline PipelineProfile
		want     PipelineProfile
	}{
		{"empty defaults to moonshot", "", PipelineMoonshot},
		{"medium returns medium", PipelineMedium, PipelineMedium},
		{"moonshot returns moonshot", PipelineMoonshot, PipelineMoonshot},
		{"large returns large", PipelineLarge, PipelineLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &Feature{Pipeline: tt.pipeline}
			if got := f.EffectivePipeline(); got != tt.want {
				t.Errorf("EffectivePipeline() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPipelineYAMLRoundTrip(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	f := &Feature{
		ID:       "test",
		Name:     "test",
		Pipeline: PipelineMedium,
		Status:   StatusCreated,
	}
	data, err := yaml.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("pipeline: medium")) {
		t.Errorf("YAML should contain 'pipeline: medium', got:\n%s", data)
	}
	var loaded Feature
	if err := yaml.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.Pipeline != PipelineMedium {
		t.Errorf("Pipeline = %v, want medium", loaded.Pipeline)
	}
}

func TestPipelineYAMLBackwardCompat(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	// Feature YAML with no pipeline field loads as empty string
	yamlData := []byte("id: test\nname: test\nstatus: Created\n")
	var f Feature
	if err := yaml.Unmarshal(yamlData, &f); err != nil {
		t.Fatal(err)
	}
	if f.Pipeline != "" {
		t.Errorf("Pipeline should be empty for legacy YAML, got %v", f.Pipeline)
	}
	if f.EffectivePipeline() != PipelineMoonshot {
		t.Errorf("EffectivePipeline() should be moonshot for legacy features, got %v", f.EffectivePipeline())
	}
}

func boolPtr(b bool) *bool { return &b }

func TestIsPublishable(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name  string
		repos []FeatureRepo
		want  bool
	}{
		{
			name: "all repos Publishable nil (backward compat)",
			repos: []FeatureRepo{
				{Name: "a", Path: "/a"},
				{Name: "b", Path: "/b"},
			},
			want: true,
		},
		{
			name: "all repos Publishable true",
			repos: []FeatureRepo{
				{Name: "a", Path: "/a", Publishable: boolPtr(true)},
				{Name: "b", Path: "/b", Publishable: boolPtr(true)},
			},
			want: true,
		},
		{
			name: "one repo Publishable false",
			repos: []FeatureRepo{
				{Name: "a", Path: "/a", Publishable: boolPtr(true)},
				{Name: "b", Path: "/b", Publishable: boolPtr(false)},
			},
			want: false,
		},
		{
			name: "mixed nil and false",
			repos: []FeatureRepo{
				{Name: "a", Path: "/a"},
				{Name: "b", Path: "/b", Publishable: boolPtr(false)},
			},
			want: false,
		},
		{
			name:  "no repos",
			repos: nil,
			want:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &Feature{Repos: tt.repos}
			if got := f.IsPublishable(); got != tt.want {
				t.Errorf("IsPublishable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEffectivePhases(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name           string
		pipeline       PipelineProfile
		publishable    bool
		wantHasPublish bool
	}{
		{
			name:           "medium publishable includes Publish",
			pipeline:       PipelineMedium,
			publishable:    true,
			wantHasPublish: true,
		},
		{
			name:           "moonshot publishable includes Publish",
			pipeline:       PipelineMoonshot,
			publishable:    true,
			wantHasPublish: true,
		},
		{
			name:           "medium unpublishable excludes Publish",
			pipeline:       PipelineMedium,
			publishable:    false,
			wantHasPublish: false,
		},
		{
			name:           "moonshot unpublishable excludes Publish",
			pipeline:       PipelineMoonshot,
			publishable:    false,
			wantHasPublish: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var repos []FeatureRepo
			if tt.publishable {
				repos = []FeatureRepo{{Name: "a", Path: "/a", Publishable: boolPtr(true)}}
			} else {
				repos = []FeatureRepo{{Name: "a", Path: "/a", Publishable: boolPtr(false)}}
			}
			f := &Feature{
				Pipeline: tt.pipeline,
				Repos:    repos,
			}
			phases := f.EffectivePhases()
			hasPublish := false
			for _, p := range phases {
				if p == PhasePublish {
					hasPublish = true
					break
				}
			}
			if hasPublish != tt.wantHasPublish {
				t.Errorf("EffectivePhases() hasPublish = %v, want %v (phases: %v)", hasPublish, tt.wantHasPublish, phases)
			}
			// Verify non-publish phases are preserved
			basePhases := PhasesForProfile(tt.pipeline)
			for _, bp := range basePhases {
				if bp == PhasePublish {
					continue
				}
				found := false
				for _, p := range phases {
					if p == bp {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("EffectivePhases() missing expected phase %v", bp)
				}
			}
		})
	}
}

func TestFeature_HasActiveRepoCycles(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name  string
		repos map[string]*RepoCycleState
		want  bool
	}{
		{"nil repo cycles", nil, false},
		{"empty repo cycles", map[string]*RepoCycleState{}, false},
		{
			name:  "single running cycle",
			repos: map[string]*RepoCycleState{"repo-a": {Type: CycleRebase, Status: RepoCycleRunning}},
			want:  true,
		},
		{
			name:  "single reviewing cycle",
			repos: map[string]*RepoCycleState{"repo-a": {Type: CycleRebase, Status: RepoCycleReviewing}},
			want:  true,
		},
		{
			name:  "paused need-user-input cycle is active",
			repos: map[string]*RepoCycleState{"repo-a": {Type: CycleRebase, Status: RepoCycleNeedUserInput}},
			want:  true,
		},
		{
			name:  "single done cycle",
			repos: map[string]*RepoCycleState{"repo-a": {Type: CycleRebase, Status: "done"}},
			want:  false,
		},
		{
			name:  "unknown cycle type running is not active",
			repos: map[string]*RepoCycleState{"repo-a": {Type: "refactor", Status: RepoCycleRunning}},
			want:  false,
		},
		{
			name: "mixed cycles",
			repos: map[string]*RepoCycleState{
				"repo-a": {Type: CycleRebase, Status: "done"},
				"repo-b": {Type: CycleRebase, Status: RepoCycleRunning},
			},
			want: true,
		},
		{
			name: "nil entry is skipped",
			repos: map[string]*RepoCycleState{
				"repo-a": nil,
				"repo-b": {Type: CycleRebase, Status: "done"},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &Feature{RepoCycles: tt.repos}
			if got := f.HasActiveRepoCycles(); got != tt.want {
				t.Errorf("HasActiveRepoCycles() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestIsReviewing covers the IsReviewing accessor under SchemaVersionCurrent = 4.
// Per-repo "awaiting_final_review" was deleted; the unified flow uses the
// feature-level StatusFinalReviewing instead.
func TestIsReviewing(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name   string
		status Status
		want   bool
	}{
		{
			name:   "implementing not reviewing",
			status: StatusImplementing,
			want:   false,
		},
		{
			name:   "feature-level final reviewing",
			status: StatusFinalReviewing,
			want:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &Feature{Status: tt.status}
			if got := f.IsReviewing(); got != tt.want {
				t.Errorf("IsReviewing() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPRURLs covers the PRURLs accessor. Per-repo entries from RepoImpl
// are the only source of PR URLs.
func TestPRURLs(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name     string
		repos    []FeatureRepo
		repoImpl map[string]*RepoState
		prURL    string
		want     map[string]string
	}{
		{
			name:     "per-repo url present",
			repos:    []FeatureRepo{{Name: "a"}},
			repoImpl: map[string]*RepoState{"a": {PRURL: "u-a"}},
			want:     map[string]string{"a": "u-a"},
		},
		{
			name:     "multi repo only one populated",
			repos:    []FeatureRepo{{Name: "a"}, {Name: "b"}},
			repoImpl: map[string]*RepoState{"a": {PRURL: "u-a"}, "b": {PRURL: ""}},
			want:     map[string]string{"a": "u-a"},
		},
		{
			name:     "multi repo legacy fallback ignored",
			repos:    []FeatureRepo{{Name: "a"}, {Name: "b"}},
			repoImpl: map[string]*RepoState{},
			prURL:    "legacy",
			want:     map[string]string{},
		},
		{
			name:  "no repos",
			repos: nil,
			prURL: "legacy",
			want:  map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &Feature{Repos: tt.repos, RepoStates: tt.repoImpl}
			f.SetPRURL(tt.prURL)
			got := f.PRURLs()
			if len(got) != len(tt.want) {
				t.Errorf("PRURLs() length = %d, want %d (got=%v want=%v)", len(got), len(tt.want), got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("PRURLs()[%s] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

// TestPRURLs_NoLegacyFallback verifies that the legacy single-repo fallback
// (f.PRURL → repos[0].Name when RepoImpl[name].PRURL is empty) is gone.
// A single-repo feature with empty RepoImpl PR URL and a non-empty f.PRURL
// shadow returns an empty map.
func TestPRURLs_NoLegacyFallback(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	f := &Feature{
		Repos:      []FeatureRepo{{Name: "only"}},
		RepoStates: map[string]*RepoState{"only": {PRURL: ""}},
	}
	f.SetPRURL("https://example.com/legacy-pr")
	got := f.PRURLs()
	if len(got) != 0 {
		t.Errorf("PRURLs() = %v, want empty (no legacy fallback)", got)
	}
}

func TestResolveAutomaticReview(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		mode       AutomaticReviewMode
		global     bool
		wantEnable bool
		wantSource AutomaticReviewSource
	}{
		{"empty inherits disabled global", "", false, false, AutomaticReviewSourceGlobal},
		{"default inherits enabled global", "default", true, true, AutomaticReviewSourceGlobal},
		{"enabled overrides disabled global", "enabled", false, true, AutomaticReviewSourceFeature},
		{"enabled overrides enabled global", "enabled", true, true, AutomaticReviewSourceFeature},
		{"disabled overrides enabled global", "disabled", true, false, AutomaticReviewSourceFeature},
		{"invalid inherits enabled global", "bogus", true, true, AutomaticReviewSourceGlobal},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotEnabled, gotSource := ResolveAutomaticReview(tt.mode, tt.global)
			if gotEnabled != tt.wantEnable || gotSource != tt.wantSource {
				t.Errorf("ResolveAutomaticReview(%q, %v) = (%v, %q), want (%v, %q)",
					tt.mode, tt.global, gotEnabled, gotSource, tt.wantEnable, tt.wantSource)
			}
		})
	}
}

func TestParseAutomaticReviewMode(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"", "default", "enabled", "disabled"} {
		input := input
		t.Run("accepts "+input, func(t *testing.T) {
			t.Parallel()
			got, err := ParseAutomaticReviewMode(input)
			if err != nil {
				t.Fatalf("ParseAutomaticReviewMode(%q) error = %v", input, err)
			}
			if got != NormalizeAutomaticReviewMode(AutomaticReviewMode(input)) {
				t.Errorf("ParseAutomaticReviewMode(%q) = %q, want %q",
					input, got, NormalizeAutomaticReviewMode(AutomaticReviewMode(input)))
			}
		})
	}

	if got, err := ParseAutomaticReviewMode("bogus"); err == nil {
		t.Fatalf("ParseAutomaticReviewMode(bogus) = %q, nil; want error", got)
	}
}
