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

package server

import (
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// effortDriftWarnings computes transient, non-durable warnings for every
// configured role whose explicit effort value is no longer supported by the
// selected model's capabilities. The warnings are computed on-the-fly from
// current feature configuration and the provider registry — they are never
// persisted and clear automatically when the model capability or configured
// effort becomes valid. Each renders through the catalog as a canonical
// warning-class effort_capability_drift error. An empty registry or nil
// feature yields no warnings.
func effortDriftWarnings(f *feature.Feature, reg *llm.Registry) []Error {
	if f == nil || reg == nil {
		return nil
	}
	roles := []struct {
		label  string
		model  string
		effort string
	}{
		{"inquiry", f.Models.Inquiry, f.Effort.Inquiry},
		{"research", f.Models.Research, f.Effort.Research},
		{"planning", f.Models.Planning, f.Effort.Planning},
		{"implementation", f.Models.Implementation, f.Effort.Implementation},
		{"review", f.Models.Review, f.Effort.Review},
		{"utilities", f.Models.Utilities, f.Effort.Utilities},
		{"kb_build", f.Models.KBBuild, f.Effort.KBBuild},
	}
	var warnings []Error
	for _, r := range roles {
		if r.effort == "" || r.effort == string(llm.EffortAuto) {
			continue
		}
		if !llm.IsValidExplicitEffort(llm.EffortLevel(r.effort)) {
			continue
		}
		prov, resolvedModel, err := reg.ResolveModel(r.model)
		if err != nil {
			continue
		}
		caps := llm.EffortCapabilitiesForModel(prov, resolvedModel)
		if llm.EffortDrifted(llm.EffortLevel(r.effort), caps) {
			warnings = append(warnings, wireError(errcat.New(
				errcat.EffortCapabilityDrift,
				errcat.WithParams(errcat.EffortDriftParams{
					Role:   r.label,
					Effort: r.effort,
					Model:  r.model,
				}),
			)))
		}
	}
	return warnings
}
