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

package agent

import (
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// TestBehavioralEvidenceImplementSection_NilFeatureNoBlock: safety — never
// panic, emit nothing when the feature pointer is nil.
func TestBehavioralEvidenceImplementSection_NilFeatureNoBlock(t *testing.T) {
	if got := behavioralEvidenceImplementSection(nil, "/tmp/iter"); got != "" {
		t.Errorf("behavioralEvidenceImplementSection(nil, _) = %q, want empty", got)
	}
}

// TestBehavioralEvidenceImplementSection_EmptyIterDirNoBlock: without a path
// to publish, the block is useless.
func TestBehavioralEvidenceImplementSection_EmptyIterDirNoBlock(t *testing.T) {
	f := &feature.Feature{Tags: []string{feature.TagFrontend}}
	if got := behavioralEvidenceImplementSection(f, ""); got != "" {
		t.Errorf("behavioralEvidenceImplementSection(frontend, \"\") = %q, want empty", got)
	}
}

// TestBehavioralEvidenceImplementSection_BackendTagNoBlock: backend features
// don't carry the behaviors block in their implement prompts.
func TestBehavioralEvidenceImplementSection_BackendTagNoBlock(t *testing.T) {
	f := &feature.Feature{Tags: []string{feature.TagBackend}}
	if got := behavioralEvidenceImplementSection(f, "/tmp/iter"); got != "" {
		t.Errorf("backend-tagged feature unexpectedly carries behaviors block:\n%s", got)
	}
}

// TestBehavioralEvidenceImplementSection_UntaggedFeatureNoBlock: features
// without tags don't get the block.
func TestBehavioralEvidenceImplementSection_UntaggedFeatureNoBlock(t *testing.T) {
	f := &feature.Feature{}
	if got := behavioralEvidenceImplementSection(f, "/tmp/iter"); got != "" {
		t.Errorf("untagged feature unexpectedly carries behaviors block:\n%s", got)
	}
}

// TestBehavioralEvidenceImplementSection_FrontendTagEmitsBlock: frontend
// features must carry the behaviors path so the implementer knows where to
// deposit driven user-journey artifacts.
func TestBehavioralEvidenceImplementSection_FrontendTagEmitsBlock(t *testing.T) {
	f := &feature.Feature{Tags: []string{feature.TagFrontend}}
	iterDir := "/tmp/feat/run-001/phase-01/implement/agentic/iteration-02"
	got := behavioralEvidenceImplementSection(f, iterDir)
	if got == "" {
		t.Fatalf("frontend-tagged feature got empty block")
	}
	if !strings.Contains(got, "Behavioral Evidence") {
		t.Errorf("block missing header; got:\n%s", got)
	}
	if !strings.Contains(got, iterDir+"/behaviors/") {
		t.Errorf("block missing behaviors path; got:\n%s", got)
	}
	// The block must NOT duplicate methodology that belongs in the skill.
	// Keep the call-site prose thin — the skill's Behavioral Evidence Loop
	// section is the authoritative reference.
	if strings.Contains(got, "Cypress") || strings.Contains(got, "Detox") {
		t.Errorf("block is duplicating methodology that belongs in the skill; got:\n%s", got)
	}
}

func TestBehavioralEvidenceReviewSection_NilFeatureNoBlock(t *testing.T) {
	if got := behavioralEvidenceReviewSection(nil, "/tmp/iter"); got != "" {
		t.Errorf("behavioralEvidenceReviewSection(nil, _) = %q, want empty", got)
	}
}

func TestBehavioralEvidenceReviewSection_BackendTagNoBlock(t *testing.T) {
	f := &feature.Feature{Tags: []string{feature.TagBackend}}
	if got := behavioralEvidenceReviewSection(f, "/tmp/iter"); got != "" {
		t.Errorf("backend-tagged feature unexpectedly carries behaviors review block:\n%s", got)
	}
}

// TestBehavioralEvidenceReviewSection_FrontendTagGatesOnBehaviors: the
// approval-gate safety rail. The reviewer must know that an iteration which
// touches a user-mutation surface but ships no behavioral evidence is a
// CHANGES_REQUESTED finding — that's exactly the gap this loop closes.
func TestBehavioralEvidenceReviewSection_FrontendTagGatesOnBehaviors(t *testing.T) {
	f := &feature.Feature{Tags: []string{feature.TagFrontend}}
	iterDir := "/tmp/iter-02"
	got := behavioralEvidenceReviewSection(f, iterDir)
	if got == "" {
		t.Fatalf("frontend-tagged review got empty block")
	}
	if !strings.Contains(got, "Behavioral Evidence From This Iteration") {
		t.Errorf("review block missing header; got:\n%s", got)
	}
	if !strings.Contains(got, iterDir+"/behaviors/") {
		t.Errorf("review block missing behaviors path; got:\n%s", got)
	}
	if !strings.Contains(got, "CHANGES_REQUESTED") {
		t.Errorf("review block should encode the approval gate; got:\n%s", got)
	}
	// Reviewer prose must explicitly cover the failure mode the loop closes:
	// the "binary launches but the user-task does not complete" case is the
	// reason this exists at all.
	if !strings.Contains(got, "complete") {
		t.Errorf("review block should reference journey completion; got:\n%s", got)
	}
}
