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
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
)

func TestPipelineProfileIsValid(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name    string
		profile PipelineProfile
		want    bool
	}{
		{"medium is valid", PipelineMedium, true},
		{"large is valid", PipelineLarge, true},
		{"moonshot is valid", PipelineMoonshot, true},
		{"empty string is invalid", PipelineProfile(""), false},
		{"garbage is invalid", PipelineProfile("garbage"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.profile.IsValid(); got != tt.want {
				t.Errorf("PipelineProfile(%q).IsValid() = %v, want %v", tt.profile, got, tt.want)
			}
		})
	}
}

func TestPipelineProfilePhaseProgression(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name       string
		profile    PipelineProfile
		wantPhases []Phase
		hasPhase   Phase
		wantHas    bool
	}{
		{
			name:       "medium",
			profile:    PipelineMedium,
			wantPhases: []Phase{PhasePlan, PhaseImplement, PhaseReview, PhasePublish},
			hasPhase:   PhaseResearch,
			wantHas:    false,
		},
		{
			name:       "large",
			profile:    PipelineLarge,
			wantPhases: []Phase{PhaseKnowledgeBase, PhaseInquire, PhaseResearch, PhaseDesign, PhasePlan, PhaseImplement, PhaseReview, PhasePublish},
			hasPhase:   PhaseResearch,
			wantHas:    true,
		},
		{
			name:       "moonshot",
			profile:    PipelineMoonshot,
			wantPhases: []Phase{PhaseKnowledgeBase, PhaseInquire, PhaseResearch, PhaseDesign, PhasePlan, PhaseImplement, PhaseReview, PhasePublish},
			hasPhase:   PhaseResearch,
			wantHas:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PhasesForProfile(tt.profile)
			if len(got) != len(tt.wantPhases) {
				t.Fatalf("PhasesForProfile(%q) returned %d phases, want %d", tt.profile, len(got), len(tt.wantPhases))
			}
			for i := range tt.wantPhases {
				if got[i] != tt.wantPhases[i] {
					t.Errorf("PhasesForProfile(%q)[%d] = %v, want %v", tt.profile, i, got[i], tt.wantPhases[i])
				}
			}
			if got := tt.profile.FirstPhase(); got != tt.wantPhases[0] {
				t.Errorf("%s.FirstPhase() = %v, want %v", tt.profile, got, tt.wantPhases[0])
			}
			if got := tt.profile.HasPhase(tt.hasPhase); got != tt.wantHas {
				t.Errorf("%s.HasPhase(%v) = %v, want %v", tt.profile, tt.hasPhase, got, tt.wantHas)
			}

			for i := 0; i < len(tt.wantPhases)-1; i++ {
				current := tt.wantPhases[i]
				wantNext := tt.wantPhases[i+1]
				gotNext, ok := tt.profile.NextPhase(current)
				if !ok {
					t.Errorf("%s.NextPhase(%v) ok = false, want true", tt.profile, current)
					continue
				}
				if gotNext != wantNext {
					t.Errorf("%s.NextPhase(%v) = %v, want %v", tt.profile, current, gotNext, wantNext)
				}
			}
			last := tt.wantPhases[len(tt.wantPhases)-1]
			if gotNext, ok := tt.profile.NextPhase(last); ok {
				t.Errorf("%s.NextPhase(%v) = %v, true; want no next phase", tt.profile, last, gotNext)
			}
		})
	}
}

func TestPipelineProfileNextProfile(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name    string
		profile PipelineProfile
		want    PipelineProfile
	}{
		{"moonshot to medium", PipelineMoonshot, PipelineMedium},
		{"medium to large", PipelineMedium, PipelineLarge},
		{"large to moonshot", PipelineLarge, PipelineMoonshot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.profile.NextProfile(); got != tt.want {
				t.Errorf("%s.NextProfile() = %v, want %v", tt.profile, got, tt.want)
			}
		})
	}
}

func TestShouldSkipPlanValidation(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name    string
		profile PipelineProfile
		want    bool
	}{
		{"medium skips plan validation", PipelineMedium, true},
		{"large does not skip plan validation", PipelineLarge, false},
		{"moonshot does not skip plan validation", PipelineMoonshot, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.profile.ShouldSkipPlanValidation(); got != tt.want {
				t.Errorf("%s.ShouldSkipPlanValidation() = %v, want %v", tt.profile, got, tt.want)
			}
		})
	}
}

func TestShouldSkipFinalReview(t *testing.T) {
	tests := []struct {
		name    string
		profile PipelineProfile
		want    bool
	}{
		{"medium does not skip final review", PipelineMedium, false},
		{"large does not skip final review", PipelineLarge, false},
		{"moonshot does not skip final review", PipelineMoonshot, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.profile.ShouldSkipFinalReview(); got != tt.want {
				t.Errorf("%s.ShouldSkipFinalReview() = %v, want %v", tt.profile, got, tt.want)
			}
		})
	}
}

func TestShouldSkipIterationReview(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name    string
		profile PipelineProfile
		want    bool
	}{
		{"medium skips iteration review", PipelineMedium, true},
		{"large skips iteration review", PipelineLarge, true},
		{"moonshot does not skip iteration review", PipelineMoonshot, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.profile.ShouldSkipIterationReview(); got != tt.want {
				t.Errorf("%s.ShouldSkipIterationReview() = %v, want %v", tt.profile, got, tt.want)
			}
		})
	}
}

func TestDefaultCheckpointsForProfile(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name    string
		profile PipelineProfile
		want    Checkpoints
	}{
		{"medium: roadmap + phase-plan + manual publish", PipelineMedium, Checkpoints{RoadmapReview: true, PhasePlanReview: true, ManualPublish: true}},
		{"large: every applicable checkpoint", PipelineLarge, Checkpoints{InquiryReview: true, ResearchReview: true, DesignReview: true, RoadmapReview: true, PhasePlanReview: true, ManualPublish: true}},
		{"moonshot: every applicable checkpoint", PipelineMoonshot, Checkpoints{InquiryReview: true, ResearchReview: true, DesignReview: true, RoadmapReview: true, PhasePlanReview: true, ManualPublish: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DefaultCheckpointsForProfile(tt.profile)
			if got != tt.want {
				t.Errorf("DefaultCheckpointsForProfile(%v) = %+v, want %+v", tt.profile, got, tt.want)
			}
		})
	}
}

func requireGateSliceEqual(t *testing.T, got, want []GateIndex) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d gates %v, want %d gates %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("gate[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestPipelineProfileApplicableGates(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name    string
		profile PipelineProfile
		want    []GateIndex
	}{
		{"medium", PipelineMedium, []GateIndex{GateRoadmapReview, GatePhasePlanReview, GateManualPublish}},
		{"large", PipelineLarge, []GateIndex{GateInquiryReview, GateResearchReview, GateDesignReview, GateRoadmapReview, GatePhasePlanReview, GateManualPublish}},
		{"moonshot", PipelineMoonshot, []GateIndex{GateInquiryReview, GateResearchReview, GateDesignReview, GateRoadmapReview, GatePhasePlanReview, GateManualPublish}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireGateSliceEqual(t, tt.profile.ApplicableGates(), tt.want)
		})
	}
}

func TestProjectGatesNormalizeCheckpointsForPersistence(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name        string
		profile     PipelineProfile
		input       Checkpoints
		publishable bool
		want        Checkpoints
		wantVisible []GateIndex
	}{
		{
			name:        "medium keeps planning and publish gates",
			profile:     PipelineMedium,
			input:       Checkpoints{InquiryReview: true, DesignReview: true, RoadmapReview: true, PhasePlanReview: true, ManualPublish: true},
			publishable: true,
			want:        Checkpoints{RoadmapReview: true, PhasePlanReview: true, ManualPublish: true},
			wantVisible: []GateIndex{GateRoadmapReview, GatePhasePlanReview, GateManualPublish},
		},
		{
			name:        "unpublished large forces manual publish while hiding the row",
			profile:     PipelineLarge,
			input:       Checkpoints{DesignReview: true, RoadmapReview: true, PhasePlanReview: true, ManualPublish: false},
			publishable: false,
			want:        Checkpoints{DesignReview: true, RoadmapReview: true, PhasePlanReview: true, ManualPublish: true},
			wantVisible: []GateIndex{GateInquiryReview, GateResearchReview, GateDesignReview, GateRoadmapReview, GatePhasePlanReview},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.profile.ProjectGates(tt.input, tt.publishable)
			if got.Checkpoints != tt.want {
				t.Fatalf("ProjectGates(%s) checkpoints = %+v, want %+v", tt.name, got.Checkpoints, tt.want)
			}
			requireGateSliceEqual(t, got.Visible, tt.wantVisible)
		})
	}
}

func TestPipelineProfileFilterCheckpoints(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	allTrue := Checkpoints{
		InquiryReview:   true,
		ResearchReview:  true,
		DesignReview:    true,
		RoadmapReview:   true,
		PhasePlanReview: true,
		ManualPublish:   true,
	}
	allGates := Checkpoints{
		InquiryReview:   true,
		ResearchReview:  true,
		DesignReview:    true,
		RoadmapReview:   true,
		PhasePlanReview: true,
		ManualPublish:   true,
	}
	tests := []struct {
		name    string
		profile PipelineProfile
		want    Checkpoints
	}{
		{"medium", PipelineMedium, Checkpoints{RoadmapReview: true, PhasePlanReview: true, ManualPublish: true}},
		{"large", PipelineLarge, allGates},
		{"moonshot", PipelineMoonshot, allGates},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.profile.FilterCheckpoints(allTrue)
			if got != tt.want {
				t.Errorf("%s.FilterCheckpoints(allTrue) = %+v, want %+v", tt.profile, got, tt.want)
			}
		})
	}
}

func TestProjectMergedGates(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name           string
		profile        PipelineProfile
		base           Checkpoints
		overrides      []Checkpoints
		publishable    bool
		wantCheckpoint Checkpoints
		wantVisible    []GateIndex
		wantFromConfig bool
	}{
		{
			name:    "medium keeps planning gate overrides",
			profile: PipelineMedium,
			base:    DefaultCheckpointsForProfile(PipelineMedium),
			overrides: []Checkpoints{{
				InquiryReview:   true,
				DesignReview:    true,
				RoadmapReview:   true,
				PhasePlanReview: true,
				ManualPublish:   true,
			}},
			publishable:    true,
			wantCheckpoint: Checkpoints{RoadmapReview: true, PhasePlanReview: true, ManualPublish: true},
			wantVisible:    []GateIndex{GateRoadmapReview, GatePhasePlanReview, GateManualPublish},
			wantFromConfig: true,
		},
		{
			name:    "large keeps every gate override",
			profile: PipelineLarge,
			base:    DefaultCheckpointsForProfile(PipelineLarge),
			overrides: []Checkpoints{
				{InquiryReview: true, ManualPublish: true},
				{ResearchReview: true, DesignReview: true, RoadmapReview: true, PhasePlanReview: true},
			},
			publishable:    true,
			wantCheckpoint: Checkpoints{InquiryReview: true, ResearchReview: true, DesignReview: true, RoadmapReview: true, PhasePlanReview: true, ManualPublish: true},
			wantVisible:    []GateIndex{GateInquiryReview, GateResearchReview, GateDesignReview, GateRoadmapReview, GatePhasePlanReview, GateManualPublish},
			wantFromConfig: true,
		},
		{
			name:           "unpublished hides publish gate but keeps checkpoint true",
			profile:        PipelineMedium,
			base:           DefaultCheckpointsForProfile(PipelineMedium),
			publishable:    false,
			wantCheckpoint: Checkpoints{RoadmapReview: true, PhasePlanReview: true, ManualPublish: true},
			wantVisible:    []GateIndex{GateRoadmapReview, GatePhasePlanReview},
			wantFromConfig: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.profile.ProjectMergedGates(tt.base, tt.overrides, tt.publishable)
			if got.Checkpoints != tt.wantCheckpoint {
				t.Fatalf("ProjectMergedGates(%s).Checkpoints = %+v, want %+v", tt.profile, got.Checkpoints, tt.wantCheckpoint)
			}
			requireGateSliceEqual(t, got.Visible, tt.wantVisible)
			if got.FromConfig != tt.wantFromConfig {
				t.Fatalf("ProjectMergedGates(%s).FromConfig = %v, want %v", tt.profile, got.FromConfig, tt.wantFromConfig)
			}
		})
	}
}

func TestParsePipelineProfileValid(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name  string
		input string
		want  PipelineProfile
	}{
		{"medium", "medium", PipelineMedium},
		{"large", "large", PipelineLarge},
		{"moonshot", "moonshot", PipelineMoonshot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePipelineProfile(tt.input)
			if err != nil {
				t.Fatalf("ParsePipelineProfile(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("ParsePipelineProfile(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestPipelineEffortLevel(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		profile PipelineProfile
		want    string
	}{
		{PipelineMedium, "medium"},
		{PipelineLarge, "high"},
		{PipelineMoonshot, "max"},
	}
	for _, tt := range tests {
		t.Run(string(tt.profile), func(t *testing.T) {
			got := tt.profile.EffortLevel()
			if string(got) != tt.want {
				t.Errorf("EffortLevel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParsePipelineProfileInvalid(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name  string
		input string
	}{
		{"empty string", ""},
		{"garbage", "garbage"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParsePipelineProfile(tt.input)
			if err == nil {
				t.Errorf("ParsePipelineProfile(%q) expected error, got nil", tt.input)
			}
		})
	}
}

func TestConfigCheckpointsToFeatureCheckpoints(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name  string
		input config.Checkpoints
		want  Checkpoints
	}{
		{
			"all false",
			config.Checkpoints{},
			Checkpoints{},
		},
		{
			"all true",
			config.Checkpoints{
				InquiryReview:   true,
				ResearchReview:  true,
				DesignReview:    true,
				RoadmapReview:   true,
				PhasePlanReview: true,
				ManualPublish:   true,
			},
			Checkpoints{
				InquiryReview:   true,
				ResearchReview:  true,
				DesignReview:    true,
				RoadmapReview:   true,
				PhasePlanReview: true,
				ManualPublish:   true,
			},
		},
		{
			"planning gates",
			config.Checkpoints{RoadmapReview: true, PhasePlanReview: true},
			Checkpoints{RoadmapReview: true, PhasePlanReview: true},
		},
		{
			"mixed",
			config.Checkpoints{
				InquiryReview: true,
				DesignReview:  true,
				ManualPublish: true,
			},
			Checkpoints{
				InquiryReview: true,
				DesignReview:  true,
				ManualPublish: true,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ConfigCheckpointsToFeature(tt.input)
			if got != tt.want {
				t.Errorf("ConfigCheckpointsToFeature(%+v) = %+v, want %+v", tt.input, got, tt.want)
			}
		})
	}
}

func TestFeatureCheckpointsToConfigCheckpoints(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name  string
		input Checkpoints
		want  config.Checkpoints
	}{
		{
			"all false",
			Checkpoints{},
			config.Checkpoints{},
		},
		{
			"all true",
			Checkpoints{
				InquiryReview:   true,
				ResearchReview:  true,
				DesignReview:    true,
				RoadmapReview:   true,
				PhasePlanReview: true,
				ManualPublish:   true,
			},
			config.Checkpoints{
				InquiryReview:   true,
				ResearchReview:  true,
				DesignReview:    true,
				RoadmapReview:   true,
				PhasePlanReview: true,
				ManualPublish:   true,
			},
		},
		{
			"planning gates",
			Checkpoints{RoadmapReview: true, PhasePlanReview: true},
			config.Checkpoints{RoadmapReview: true, PhasePlanReview: true},
		},
		{
			"mixed",
			Checkpoints{
				ResearchReview:  true,
				PhasePlanReview: true,
			},
			config.Checkpoints{
				ResearchReview:  true,
				PhasePlanReview: true,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FeatureCheckpointsToConfig(tt.input)
			if got.InquiryReview != tt.want.InquiryReview ||
				got.ResearchReview != tt.want.ResearchReview ||
				got.DesignReview != tt.want.DesignReview ||
				got.RoadmapReview != tt.want.RoadmapReview ||
				got.PhasePlanReview != tt.want.PhasePlanReview ||
				got.ManualPublish != tt.want.ManualPublish {
				t.Errorf("FeatureCheckpointsToConfig(%+v) = %+v, want %+v", tt.input, got, tt.want)
			}
		})
	}
}

func TestMinimumProfileForPhase(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name  string
		phase Phase
		want  PipelineProfile
	}{
		{"KB requires large", PhaseKnowledgeBase, PipelineLarge},
		{"Inquire requires large", PhaseInquire, PipelineLarge},
		{"Research requires large", PhaseResearch, PipelineLarge},
		{"Design requires large", PhaseDesign, PipelineLarge},
		{"Plan available in medium", PhasePlan, PipelineMedium},
		{"Implement available in medium", PhaseImplement, PipelineMedium},
		{"Publish available in medium", PhasePublish, PipelineMedium},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MinimumProfileForPhase(tt.phase)
			if got != tt.want {
				t.Errorf("MinimumProfileForPhase(%v) = %v, want %v", tt.phase, got, tt.want)
			}
		})
	}
}
