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

package orchestrator

import (
	"fmt"
	"path/filepath"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// readOnlyGuardDir joins the active run dir with a caller-supplied tail,
// returning "" when the feature or state dir is unresolved. suffix is only
// invoked once the base path is known to be valid.
func (o *Orchestrator) readOnlyGuardDir(f *feature.Feature, suffix func() string) string {
	baseDir := o.stateDir()
	if f == nil || baseDir == "" {
		return ""
	}
	base := agent.ActiveRunDir(baseDir, f)
	return filepath.Join(base, suffix())
}

func (o *Orchestrator) artifactReadOnlyGuardDir(f *feature.Feature, phaseKey string) string {
	if phaseKey == "" {
		return ""
	}
	return o.readOnlyGuardDir(f, func() string { return phaseKey })
}

func (o *Orchestrator) planReadOnlyGuardDir(f *feature.Feature) string {
	return o.readOnlyGuardDir(f, func() string {
		if f.CurrentRoadmapPhase > 0 {
			return filepath.Join(fmt.Sprintf("phase-%02d", f.CurrentRoadmapPhase), "plan")
		}
		return "roadmap"
	})
}
