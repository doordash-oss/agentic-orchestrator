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
	"fmt"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// effortDriftWarningCode is the Warning.Code for capability drift warnings.
const effortDriftWarningCode = "effort_capability_drift"

// effortDriftWarnings computes transient, non-durable warnings for every
// configured role whose explicit effort value is no longer supported by the
// selected model's capabilities. The warnings are computed on-the-fly from
// current feature configuration and the provider registry — they are never
// persisted and clear automatically when the model capability or configured
// effort becomes valid. An empty registry or nil feature yields no warnings.
func effortDriftWarnings(f *feature.Feature, reg *llm.Registry) []Warning {
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
	var warnings []Warning
	for _, r := range roles {
		if r.effort == "" || r.effort == string(llm.EffortAuto) {
			continue
		}
		if !llm.IsValidExplicitEffort(llm.EffortLevel(r.effort)) {
			continue
		}
		prov, _, err := reg.ResolveModel(r.model)
		if err != nil {
			continue
		}
		caps := llm.EffortCapabilitiesForModel(prov, r.model)
		if llm.EffortDrifted(llm.EffortLevel(r.effort), caps) {
			warnings = append(warnings, Warning{
				Code:      effortDriftWarningCode,
				FeatureID: f.ID,
				Message: fmt.Sprintf("%s effort %q is not supported by model %q; using Auto until the configuration is updated",
					r.label, r.effort, r.model),
			})
		}
	}
	return warnings
}
