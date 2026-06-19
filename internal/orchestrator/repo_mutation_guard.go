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
	"context"
	"fmt"
	"path/filepath"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func (o *Orchestrator) recordReadOnlyRepoBaseline(ctx context.Context, f *feature.Feature, phaseDir string, repoNames ...string) error {
	return agent.RecordReadOnlyRepoBaseline(ctx, o.deps.CmdRunner, f, phaseDir, repoNames...)
}

func (o *Orchestrator) enforceReadOnlyRepoMutations(ctx context.Context, f *feature.Feature, phase feature.Phase, phaseDir string, repoNames ...string) ([]agent.ProtocolViolation, error) {
	return agent.EnforceReadOnlyRepoMutations(ctx, o.deps.CmdRunner, f, phase, phaseDir, repoNames...)
}

func (o *Orchestrator) artifactReadOnlyGuardDir(f *feature.Feature, phaseKey string) string {
	baseDir := o.stateDir()
	if f == nil || baseDir == "" || phaseKey == "" {
		return ""
	}
	return filepath.Join(agent.ActiveRunDir(baseDir, f), f.RefactorPrefix(), phaseKey)
}

func (o *Orchestrator) planReadOnlyGuardDir(f *feature.Feature) string {
	baseDir := o.stateDir()
	if f == nil || baseDir == "" {
		return ""
	}
	base := filepath.Join(agent.ActiveRunDir(baseDir, f), f.RefactorPrefix())
	if f.CurrentRoadmapPhase > 0 {
		return filepath.Join(base, fmt.Sprintf("phase-%02d", f.CurrentRoadmapPhase), "plan")
	}
	return filepath.Join(base, "roadmap")
}
