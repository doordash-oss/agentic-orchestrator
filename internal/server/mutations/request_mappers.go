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

package mutations

import (
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
)

// refactorChildSpecFromRequest maps a validated launch request after the
// handler has resolved uploaded inputs to durable local paths.
func refactorChildSpecFromRequest(req serverruntime.RefactorFeatureRequest) (feature.RefactorChildSpec, error) {
	spec := feature.RefactorChildSpec{
		Name: req.Name, Description: req.Description, Images: req.Images,
		Attachments: req.Attachments, Checkpoints: req.Checkpoints,
		Effort: req.Effort, Models: req.Models, RiskLevel: req.RiskLevel,
		ExitCriteria: req.ExitCriteria, Inquireness: req.Inquireness,
	}
	if req.Pipeline != "" {
		pipeline, err := feature.ParsePipelineProfile(string(req.Pipeline))
		if err != nil {
			return spec, err
		}
		spec.Pipeline = pipeline
	}
	return spec, nil
}

// modelConfigToPatch omits an empty automatic-review model so a feature
// overlay does not erase the workspace setting.
func modelConfigToPatch(m config.ModelConfig) serverruntime.ModelConfigPatch {
	patch := serverruntime.ModelConfigPatch{
		Inquiry: m.Inquiry, Research: m.Research, Planning: m.Planning,
		Implementation: m.Implementation, Review: m.Review,
		Utilities: m.Utilities, KBBuild: m.KBBuild,
	}
	if m.AutomaticReview != "" {
		value := m.AutomaticReview
		patch.AutomaticReview = &value
	}
	return patch
}
