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
	"fmt"
	"slices"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// PipelineProfile controls which phases a feature runs through.
type PipelineProfile string

const (
	PipelineMedium   PipelineProfile = "medium"
	PipelineLarge    PipelineProfile = "large"
	PipelineMoonshot PipelineProfile = "moonshot"
)

// String returns the human-readable name.
func (p PipelineProfile) String() string {
	return string(p)
}

// ConfigKey returns the user-facing/profile-config key.
func (p PipelineProfile) ConfigKey() string {
	return string(p)
}

// DisplayName returns the user-facing name for a pipeline profile.
func (p PipelineProfile) DisplayName() string {
	switch p {
	case PipelineMedium:
		return "Medium"
	case PipelineLarge:
		return "Large"
	case PipelineMoonshot:
		return "Moonshot"
	default:
		return string(p)
	}
}

// IsValid returns true for known profiles.
func (p PipelineProfile) IsValid() bool {
	switch p {
	case PipelineMedium, PipelineLarge, PipelineMoonshot:
		return true
	}
	return false
}

// PhasesForProfile returns the ordered phases for this profile.
// Medium: Plan, Implement, Review, Publish
// Large/Moonshot: KnowledgeBase, Inquire, Research, Design, Plan, Implement, Review, Publish
func PhasesForProfile(profile PipelineProfile) []Phase {
	switch profile {
	case PipelineMedium:
		return []Phase{PhasePlan, PhaseImplement, PhaseReview, PhasePublish}
	default:
		// Large and Moonshot run the same phase sequence; they differ in effort
		// and validation behavior.
		return []Phase{PhaseKnowledgeBase, PhaseInquire, PhaseResearch, PhaseDesign, PhasePlan, PhaseImplement, PhaseReview, PhasePublish}
	}
}

// MinimumProfileForPhase returns the least permissive profile that includes the given phase.
// Medium includes: Plan, Implement, Review, Publish
// Large/Moonshot include all 7 phases (same phase set, different validation behavior)
func MinimumProfileForPhase(phase Phase) PipelineProfile {
	switch phase {
	case PhaseKnowledgeBase, PhaseInquire, PhaseResearch, PhaseDesign:
		return PipelineLarge
	default: // PhasePlan, PhaseImplement, PhaseReview, PhasePublish
		return PipelineMedium
	}
}

// HasPhase returns true if the given phase is part of this profile's pipeline.
func (p PipelineProfile) HasPhase(phase Phase) bool {
	return slices.Contains(PhasesForProfile(p), phase)
}

// UpgradeOptions returns the pipeline profiles this profile may upgrade into
// (higher effort, same or richer phase set). Returns nil when no upgrade is
// available. Shared by the rewind preview and the server read-model so the
// upgrade lattice never drifts.
func (p PipelineProfile) UpgradeOptions() []PipelineProfile {
	switch p {
	case PipelineMedium:
		return []PipelineProfile{PipelineLarge, PipelineMoonshot}
	case PipelineLarge:
		return []PipelineProfile{PipelineMoonshot}
	default:
		return nil
	}
}

// FirstPhase returns the first phase to execute for this profile.
// Medium: PhasePlan, Large/Moonshot: PhaseKnowledgeBase
func (p PipelineProfile) FirstPhase() Phase {
	phases := PhasesForProfile(p)
	return phases[0]
}

// NextPhase returns the phase that follows `current` in this profile's pipeline.
// Returns ("", false) if current is the last phase.
func (p PipelineProfile) NextPhase(current Phase) (Phase, bool) {
	phases := PhasesForProfile(p)
	for i, ph := range phases {
		if ph == current && i+1 < len(phases) {
			return phases[i+1], true
		}
	}
	return 0, false
}

// EffortLevel returns the provider-agnostic effort level for this pipeline profile.
// Medium → Medium; Large and Moonshot → High.
func (p PipelineProfile) EffortLevel() llm.EffortLevel {
	switch p {
	case PipelineMedium:
		return llm.EffortMedium
	case PipelineLarge, PipelineMoonshot:
		return llm.EffortHigh
	default:
		return llm.EffortHigh
	}
}

// ShouldSkipPlanValidation returns true if this profile skips plan critics.
// Medium skips them; Large and Moonshot run them.
func (p PipelineProfile) ShouldSkipPlanValidation() bool {
	return p == PipelineMedium
}

// ShouldSkipFinalReview returns true if this profile skips the deferred
// end-of-feature Final Review pass. All current profiles run Final Review;
// Medium skips iteration review but still gets this final quality gate.
func (p PipelineProfile) ShouldSkipFinalReview() bool {
	return false
}

// ShouldSkipIterationReview returns true if this profile skips per-iteration
// review gates during implementation. Medium and Large skip per-iteration
// review and rely solely on the Final Review. Moonshot retains both.
func (p PipelineProfile) ShouldSkipIterationReview() bool {
	return p == PipelineMedium || p == PipelineLarge
}

// ShouldRunImplementationHarness returns true if the deterministic
// verification harness executes the phase testing contract during
// implementation iterations. Moonshot only; Medium and Large trust the
// implementer and verify at Final Review through live-run review axes.
func (p PipelineProfile) ShouldRunImplementationHarness() bool {
	return p == PipelineMoonshot
}

// ShouldContractAgentEvidence returns true if phase plans may contract
// agent-owned evidence (visual matrices, manual observations, behavioral
// journey artifacts). Moonshot only — its per-phase harness validates that
// evidence and its Functionality/Evidence axis audits it. Medium and Large
// plans contract automated test commands only.
func (p PipelineProfile) ShouldContractAgentEvidence() bool {
	return p == PipelineMoonshot
}

// AllProfiles returns all profiles in cycle order: moonshot -> medium -> large.
func AllProfiles() []PipelineProfile {
	return []PipelineProfile{PipelineMoonshot, PipelineMedium, PipelineLarge}
}

// NextProfile returns the next profile in cycle order.
// moonshot -> medium -> large -> moonshot
func (p PipelineProfile) NextProfile() PipelineProfile {
	profiles := AllProfiles()
	for i, prof := range profiles {
		if prof == p {
			return profiles[(i+1)%len(profiles)]
		}
	}
	return PipelineMoonshot
}

// DefaultCheckpointsForProfile returns the default checkpoints for a profile.
func DefaultCheckpointsForProfile(profile PipelineProfile) Checkpoints {
	switch profile {
	case PipelineMedium:
		return Checkpoints{RoadmapReview: true, PhasePlanReview: true, ManualPublish: true}
	case PipelineLarge, PipelineMoonshot:
		return Checkpoints{InquiryReview: true, ResearchReview: true, DesignReview: true, RoadmapReview: true, PhasePlanReview: true, ManualPublish: true}
	default:
		return Checkpoints{InquiryReview: true, ResearchReview: true, DesignReview: true, RoadmapReview: true, PhasePlanReview: true, ManualPublish: true}
	}
}

// GateIndex represents a named gate position in the gate projection.
type GateIndex int

const (
	GateInquiryReview GateIndex = iota
	GateResearchReview
	GateDesignReview
	GateRoadmapReview
	GatePhasePlanReview
	GateManualPublish
)

// GateProjection is the shared gate contract for a pipeline profile.
type GateProjection struct {
	Visible     []GateIndex
	Checkpoints Checkpoints
	FromConfig  bool
}

// ApplicableGates returns which gate indices are relevant for this profile.
func (p PipelineProfile) ApplicableGates() []GateIndex {
	switch p {
	case PipelineMedium:
		return []GateIndex{GateRoadmapReview, GatePhasePlanReview, GateManualPublish}
	case PipelineLarge, PipelineMoonshot:
		return []GateIndex{GateInquiryReview, GateResearchReview, GateDesignReview, GateRoadmapReview, GatePhasePlanReview, GateManualPublish}
	default:
		return []GateIndex{GateInquiryReview, GateResearchReview, GateDesignReview, GateRoadmapReview, GatePhasePlanReview, GateManualPublish}
	}
}

// FilterCheckpoints zeroes out any gates not in this profile's applicable set.
func (p PipelineProfile) FilterCheckpoints(cp Checkpoints) Checkpoints {
	applicable := make(map[GateIndex]bool)
	for _, g := range p.ApplicableGates() {
		applicable[g] = true
	}
	if !applicable[GateInquiryReview] {
		cp.InquiryReview = false
	}
	if !applicable[GateResearchReview] {
		cp.ResearchReview = false
	}
	if !applicable[GateDesignReview] {
		cp.DesignReview = false
	}
	if !applicable[GateRoadmapReview] {
		cp.RoadmapReview = false
	}
	if !applicable[GatePhasePlanReview] {
		cp.PhasePlanReview = false
	}
	if !applicable[GateManualPublish] {
		cp.ManualPublish = false
	}
	return cp
}

// NormalizeCheckpoints filters unsupported gates and enforces unpublished-save
// invariants shared by editor snapshots and persisted feature config.
func (p PipelineProfile) NormalizeCheckpoints(cp Checkpoints, publishable bool) Checkpoints {
	cp = p.FilterCheckpoints(cp)
	if !publishable {
		cp.ManualPublish = true
	}
	return cp
}

// ProjectGates returns the visible gates plus normalized checkpoint state for a profile.
func (p PipelineProfile) ProjectGates(cp Checkpoints, publishable bool) GateProjection {
	normalized := p.NormalizeCheckpoints(cp, publishable)
	visible := append([]GateIndex(nil), p.ApplicableGates()...)
	if !publishable {
		visible = slices.DeleteFunc(visible, func(g GateIndex) bool {
			return g == GateManualPublish
		})
	}

	return GateProjection{
		Visible:     visible,
		Checkpoints: normalized,
	}
}

func mergeCheckpoints(checkpoints ...Checkpoints) Checkpoints {
	merged := Checkpoints{}
	for _, cp := range checkpoints {
		merged.InquiryReview = merged.InquiryReview || cp.InquiryReview
		merged.ResearchReview = merged.ResearchReview || cp.ResearchReview
		merged.DesignReview = merged.DesignReview || cp.DesignReview
		merged.RoadmapReview = merged.RoadmapReview || cp.RoadmapReview
		merged.PhasePlanReview = merged.PhasePlanReview || cp.PhasePlanReview
		merged.ManualPublish = merged.ManualPublish || cp.ManualPublish
		merged.DraftPublish = merged.DraftPublish || cp.DraftPublish
	}
	return merged
}

// ProjectMergedGates merges checkpoint inputs, normalizes them for the profile,
// and returns the visible gate contract for the current pipeline selection.
func (p PipelineProfile) ProjectMergedGates(base Checkpoints, overrides []Checkpoints, publishable bool) GateProjection {
	effective := p.FilterCheckpoints(base)
	fromConfig := len(overrides) > 0
	if fromConfig {
		filtered := make([]Checkpoints, 0, len(overrides))
		for _, override := range overrides {
			filtered = append(filtered, p.FilterCheckpoints(override))
		}
		effective = mergeCheckpoints(filtered...)
	}

	projection := p.ProjectGates(effective, publishable)
	projection.FromConfig = fromConfig
	return projection
}

// ParsePipelineProfile converts a string to PipelineProfile, returning an error for invalid values.
func ParsePipelineProfile(s string) (PipelineProfile, error) {
	switch s {
	case "medium":
		return PipelineMedium, nil
	case "large":
		return PipelineLarge, nil
	case "moonshot":
		return PipelineMoonshot, nil
	default:
		return "", fmt.Errorf("invalid pipeline profile %q: must be medium, large, or moonshot", s)
	}
}

// ConfigCheckpointsToFeature converts config.Checkpoints to feature.Checkpoints.
func ConfigCheckpointsToFeature(cc config.Checkpoints) Checkpoints {
	return Checkpoints{
		InquiryReview:   cc.InquiryReview,
		ResearchReview:  cc.ResearchReview,
		DesignReview:    cc.DesignReview,
		RoadmapReview:   cc.RoadmapReview,
		PhasePlanReview: cc.PhasePlanReview,
		ManualPublish:   cc.ManualPublish,
		DraftPublish:    cc.DraftPublish,
	}
}

// FeatureCheckpointsToConfig converts feature.Checkpoints to config.Checkpoints.
func FeatureCheckpointsToConfig(fc Checkpoints) config.Checkpoints {
	return config.Checkpoints{
		InquiryReview:   fc.InquiryReview,
		ResearchReview:  fc.ResearchReview,
		DesignReview:    fc.DesignReview,
		RoadmapReview:   fc.RoadmapReview,
		PhasePlanReview: fc.PhasePlanReview,
		ManualPublish:   fc.ManualPublish,
		DraftPublish:    fc.DraftPublish,
	}
}
