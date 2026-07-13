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

import "github.com/doordash-oss/agentic-orchestrator/internal/feature"

type HelpResolvedMsg struct {
	FeatureID string
	RequestID string
}

type PlanReviewDecisionMsg struct {
	FeatureID string
	Decision  string
}

type RoadmapReviewDecisionMsg struct {
	FeatureID string
	Decision  string
	Comment   string
}

type RewindReviewDecisionMsg struct {
	FeatureID string
	Phase     feature.Phase
	Decision  string
}

type GateReviewDecisionMsg struct {
	FeatureID string
	Phase     feature.Phase
	Decision  string
}

type ArtifactReviewDraftSaveMsg struct {
	FeatureID    string
	ReviewID     string
	BaseRevision string
	Text         string
}

type ArtifactReviewSessionDecisionMsg struct {
	FeatureID    string
	ReviewID     string
	Decision     string
	BaseRevision string
	Text         string
}
