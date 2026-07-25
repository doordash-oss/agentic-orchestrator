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

package llm

// ResolveEffort combines a configured effort value, the selected model's
// capability list, and the pipeline-derived effort level into a provider-safe
// effective effort and an auto|explicit source.
//
//   - When configured is empty or EffortAuto, the pipeline effort is used with
//     source auto. This preserves current pipeline behavior.
//   - When configured is an explicit level (low|medium|high|xhigh|max) and the model
//     supports it, the configured value is used unchanged with source explicit.
//   - When configured is explicit but the model no longer supports it
//     (capability drift), the pipeline effort is used with source auto. The
//     caller is expected to emit a runtime warning through the existing channel
//     and leave persisted state untouched until an ordinary save.
//   - When configured is explicit but the model has no capabilities at all
//     (unknown or unsupported backend), the pipeline effort is used with source
//     auto, matching the "Auto-only" behavior for models without effort control.
func ResolveEffort(configured EffortLevel, capabilities []EffortLevel, pipelineEffort EffortLevel) (EffortLevel, EffortSource) {
	if configured == "" || configured == EffortAuto {
		return pipelineEffort, EffortSourceAuto
	}
	if EffortCapabilitySupported(capabilities, configured) {
		return configured, EffortSourceExplicit
	}
	return pipelineEffort, EffortSourceAuto
}

// ResolveEffortFromString is a convenience wrapper that accepts the string form
// from persisted configuration and returns the resolved level and source. It
// normalizes empty and "auto" to EffortAuto before delegating to ResolveEffort.
func ResolveEffortFromString(configured string, capabilities []EffortLevel, pipelineEffort EffortLevel) (EffortLevel, EffortSource) {
	return ResolveEffort(EffortLevel(configured), capabilities, pipelineEffort)
}

// EffortDrifted reports whether a configured effort value represents capability
// drift: the value is a valid explicit level but is not in the model's
// capability list. This lets callers emit a runtime warning only when an
// explicit choice is silently downgraded to Auto, not when Auto was configured
// or the model simply lacks effort control.
func EffortDrifted(configured EffortLevel, capabilities []EffortLevel) bool {
	if configured == "" || configured == EffortAuto {
		return false
	}
	if !IsValidExplicitEffort(configured) {
		return false
	}
	if len(capabilities) == 0 {
		return false
	}
	return !EffortCapabilitySupported(capabilities, configured)
}
