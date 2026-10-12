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

// IsValid returns true for known profiles.
func (p PipelineProfile) IsValid() bool {
	switch p {
	case PipelineMedium, PipelineLarge, PipelineMoonshot:
		return true
	}
	return false
}

// phasesForProfile returns the ordered phases for this profile.
// Medium: Plan, Implement, Review, Publish
// Large/Moonshot: KnowledgeBase, Inquire, Research, Design, Plan, Implement, Review, Publish
func phasesForProfile(profile PipelineProfile) []Phase {
	switch profile {
	case PipelineMedium:
		return []Phase{PhasePlan, PhaseImplement, PhaseReview, PhasePublish}
	default:
		// Large and Moonshot run the same phase sequence; they differ in effort
		// and validation behavior.
		return []Phase{PhaseKnowledgeBase, PhaseInquire, PhaseResearch, PhaseDesign, PhasePlan, PhaseImplement, PhaseReview, PhasePublish}
	}
}

// HasPhase returns true if the given phase is part of this profile's pipeline.
func (p PipelineProfile) HasPhase(phase Phase) bool {
	return slices.Contains(phasesForProfile(p), phase)
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
	phases := phasesForProfile(p)
	return phases[0]
}

// NextPhase returns the phase that follows `current` in this profile's pipeline.
// Returns ("", false) if current is the last phase.
func (p PipelineProfile) NextPhase(current Phase) (Phase, bool) {
	phases := phasesForProfile(p)
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
	gateInquiryReview GateIndex = iota
	gateResearchReview
	gateDesignReview
	gateRoadmapReview
	gatePhasePlanReview
	gateManualPublish
)

// GateProjection is the shared gate contract for a pipeline profile.
type GateProjection struct {
	Visible     []GateIndex
	Checkpoints Checkpoints
}

// ApplicableGates returns which gate indices are relevant for this profile.
func (p PipelineProfile) applicableGates() []GateIndex {
	switch p {
	case PipelineMedium:
		return []GateIndex{gateRoadmapReview, gatePhasePlanReview, gateManualPublish}
	case PipelineLarge, PipelineMoonshot:
		return []GateIndex{gateInquiryReview, gateResearchReview, gateDesignReview, gateRoadmapReview, gatePhasePlanReview, gateManualPublish}
	default:
		return []GateIndex{gateInquiryReview, gateResearchReview, gateDesignReview, gateRoadmapReview, gatePhasePlanReview, gateManualPublish}
	}
}

// FilterCheckpoints zeroes out any gates not in this profile's applicable set.
func (p PipelineProfile) filterCheckpoints(cp Checkpoints) Checkpoints {
	applicable := make(map[GateIndex]bool)
	for _, g := range p.applicableGates() {
		applicable[g] = true
	}
	if !applicable[gateInquiryReview] {
		cp.InquiryReview = false
	}
	if !applicable[gateResearchReview] {
		cp.ResearchReview = false
	}
	if !applicable[gateDesignReview] {
		cp.DesignReview = false
	}
	if !applicable[gateRoadmapReview] {
		cp.RoadmapReview = false
	}
	if !applicable[gatePhasePlanReview] {
		cp.PhasePlanReview = false
	}
	if !applicable[gateManualPublish] {
		cp.ManualPublish = false
	}
	return cp
}

// NormalizeCheckpoints filters unsupported gates and enforces unpublished-save
// invariants shared by editor snapshots and persisted feature config.
func (p PipelineProfile) NormalizeCheckpoints(cp Checkpoints, publishable bool) Checkpoints {
	cp = p.filterCheckpoints(cp)
	if !publishable {
		cp.ManualPublish = true
	}
	return cp
}

// ProjectGates returns the visible gates plus normalized checkpoint state for a profile.
func (p PipelineProfile) ProjectGates(cp Checkpoints, publishable bool) GateProjection {
	normalized := p.NormalizeCheckpoints(cp, publishable)
	visible := append([]GateIndex(nil), p.applicableGates()...)
	if !publishable {
		visible = slices.DeleteFunc(visible, func(g GateIndex) bool {
			return g == gateManualPublish
		})
	}

	return GateProjection{
		Visible:     visible,
		Checkpoints: normalized,
	}
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
