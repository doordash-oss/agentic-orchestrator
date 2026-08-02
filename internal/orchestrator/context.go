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
	"os"
	"path/filepath"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// stateDir returns the feature state base directory.
// Uses PhaseRunner.StateDir which points at the same base dir as
// feature.Store.BaseDir. Falls back to feature.Store.BaseDir when
// PhaseRunner is unset (tests that don't use context assembly), so
// path-resolution helpers (RecordRoadmapRejection, artifact resolution)
// continue to work in unit tests that inject a real feature.Store
// but no PhaseRunner.
func (o *Orchestrator) stateDir() string {
	if o.deps.PhaseRunner != nil && o.deps.PhaseRunner.StateDir != "" {
		return o.deps.PhaseRunner.StateDir
	}
	if s, ok := o.deps.Store.(*feature.Store); ok && s != nil {
		return s.BaseDir
	}
	return ""
}

// computeKBInfos builds KB info metadata for all feature repos.
// For child features, the KB info points at the disposable workspace
// instead of the canonical KB.
func (o *Orchestrator) computeKBInfos(f *feature.Feature) []agent.KBInfo {
	baseDir := o.stateDir()
	if baseDir == "" {
		return nil
	}
	var infos []agent.KBInfo
	for _, repo := range f.Repos {
		var kbDir string
		if f.IsChild() {
			kbDir = feature.ChildKBWorkspaceDir(baseDir, f.ID, repo.Name)
		} else {
			kbDir = agent.KBStateDir(baseDir, repo.Name)
		}
		indexPath := agent.KBPath(kbDir)
		if _, err := os.Stat(indexPath); err == nil {
			infos = append(infos, agent.KBInfo{Name: repo.Name, IndexPath: indexPath, RootDir: kbDir})
		}
	}
	return infos
}

// resolveArtifactPath returns the absolute filesystem path to a phase
// artifact without reading its content. It reads state from PhaseRunner.
func (o *Orchestrator) resolveArtifactPath(f *feature.Feature, phase string) string {
	return o.resolveArtifactPathForKey(f, phase)
}

func (o *Orchestrator) resolveArtifactPathForKey(f *feature.Feature, phase string) string {
	baseDir := o.stateDir()
	artifactPath, ok := f.Artifacts[phase]
	if !ok || artifactPath == "" {
		// No stored artifact path — try the well-known phase directory.
		phaseDir := o.resolvePhaseDirForKey(f, phase)
		return globPhaseArtifact(phaseDir)
	}

	if filepath.IsAbs(artifactPath) {
		if _, err := os.Stat(artifactPath); err == nil {
			return artifactPath
		}
		// Absolute path didn't resolve — fall through to phase-dir glob fallback.
	}

	if !filepath.IsAbs(artifactPath) {
		if baseDir != "" {
			activeRunDir := agent.ActiveRunDir(baseDir, f)

			// Run-relative form: values are relative to the active run directory,
			// e.g. `phase-01/plan/plan.md` for a roadmap pipeline's carried "plan"
			// key. Resolve by joining to ActiveRunDir directly, not to
			// `<ActiveRunDir>/<phase>/`, which would double-prefix the first path
			// segment.
			if activeRunDir != "" {
				if candidate := filepath.Join(activeRunDir, artifactPath); candidate != "" {
					if _, err := os.Stat(candidate); err == nil {
						return candidate
					}
				}
			}

			// Legacy basename form: values are relative to the phase's output
			// directory. Use resolvePhaseDirForKey so phase-N-plan keys are
			// resolved against the canonical phase-NN/plan dir, not the
			// literal "phase-N-plan" dir which does not exist on disk.
			phaseDir := o.resolvePhaseDirForKey(f, phase)
			if phaseDir == "" {
				phaseDir = filepath.Join(activeRunDir, phase)
			}
			candidate := filepath.Join(phaseDir, artifactPath)
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}

		// Try repo worktree / repo path.
		if len(f.Repos) > 0 {
			repo := f.Repos[0]
			if repo.WorktreePath != "" {
				if _, err := os.Stat(filepath.Join(repo.WorktreePath, artifactPath)); err == nil {
					return filepath.Join(repo.WorktreePath, artifactPath)
				}
			}
			if repo.Path != "" {
				if _, err := os.Stat(filepath.Join(repo.Path, artifactPath)); err == nil {
					return filepath.Join(repo.Path, artifactPath)
				}
			}
		}
	}

	// Last resort: glob for artifact files in the phase directory.
	phaseDir := o.resolvePhaseDirForKey(f, phase)
	return globPhaseArtifact(phaseDir)
}

// resolvePhaseDirForKey maps an artifact key to the correct filesystem
// directory. Phase plan keys (e.g. "phase-2-plan") map to phase-NN/plan/
// directories.
func (o *Orchestrator) resolvePhaseDirForKey(f *feature.Feature, key string) string {
	baseDir := o.stateDir()
	if baseDir == "" {
		return ""
	}
	var phaseNum int
	if _, err := fmt.Sscanf(key, "phase-%d-plan", &phaseNum); err == nil && phaseNum > 0 {
		return o.phasePlanDirForFeature(f, phaseNum)
	}
	return filepath.Join(agent.ActiveRunDir(baseDir, f), key)
}

// phasePlanDirForFeature returns the plan subdirectory for a roadmap phase.
// Mirrors the directory choice RunPhasePlanningLoop makes at
// plan_validation.go — the phase-plan artifacts live under
// <stateDir>/<featureID>/runs/run-N/phase-NN/plan. Returns "" when the state
// dir is unset. Callers that reach into phase-plan directories (iterate
// invalidation, reviewProceed per-phase dispatch, auto-approved per-phase
// dispatch) must use this helper so phase plans are resolved to the same
// directory the planner writes to.
func (o *Orchestrator) phasePlanDirForFeature(f *feature.Feature, phase int) string {
	baseDir := o.stateDir()
	if baseDir == "" {
		return ""
	}
	return agent.PhasePlanDir(baseDir, f, phase)
}

// (Per SchemaVersionCurrent = 3, callers resolve the per-phase plan
// directory by taking filepath.Dir of the plan markdown path — the planner
// authors `<plan-dir>/plan.md` and `<plan-dir>/execution-order.yaml`
// together. See StartMultiRepoImplementation in multirepo.go and
// tryLoadPhasePlan in lifecycle_delegates.go.)

// collectQAFilePaths gathers Q&A answer files from earlier phases (inquire,
// research, design, roadmap) so they can be passed to planning prompts.
// Missing files are gracefully skipped via os.Stat.
func (o *Orchestrator) collectQAFilePaths(f *feature.Feature) []string {
	baseDir := o.stateDir()
	if baseDir == "" || f == nil {
		return nil
	}
	runDir := agent.ActiveRunDir(baseDir, f)
	var paths []string
	for _, phase := range []string{"inquire", "research", "design", "roadmap"} {
		qaPath := filepath.Join(runDir, phase, "qa-answers.md")
		if _, err := os.Stat(qaPath); err == nil {
			paths = append(paths, qaPath)
		}
	}
	return paths
}

// resolvePlanPath resolves the plan artifact for implementation.
// Follows the cascade: cycle plan → stored artifact → roadmap phase plan →
// globbed plan. Returns "" if no plan is found.
func (o *Orchestrator) resolvePlanPath(f *feature.Feature) string {
	baseDir := o.stateDir()

	// Cycle plan — when a post-publish cycle is active, look in the cycle dir
	// only; do NOT fall through to roadmap/stored artifacts which belong to
	// normal implementation.
	if cyclePrefix := f.CyclePrefix(); cyclePrefix != "" {
		if baseDir == "" {
			return ""
		}
		cycleDir := filepath.Join(agent.ActiveRunDir(baseDir, f), cyclePrefix)
		return globCyclePlan(cycleDir)
	}

	// Stored artifact: absolute first, then run-relative fallback. Carried
	// values are normalized to run-relative form such as `phase-01/plan/plan.md`
	// for a roadmap pipeline.
	if stored := f.Artifacts["plan"]; stored != "" {
		if filepath.IsAbs(stored) {
			if _, err := os.Stat(stored); err == nil {
				return stored
			}
		} else if baseDir != "" {
			candidate := filepath.Join(agent.ActiveRunDir(baseDir, f), stored)
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
	}

	// Roadmap phase plan
	if f.CurrentRoadmapPhase > 0 {
		artifactKey := fmt.Sprintf("phase-%d-plan", f.CurrentRoadmapPhase)
		if p := o.resolveArtifactPath(f, artifactKey); p != "" {
			return p
		}
		if phaseDir := o.phasePlanDirForFeature(f, f.CurrentRoadmapPhase); phaseDir != "" {
			if p := globPhaseArtifact(phaseDir); p != "" {
				return p
			}
		}
	}

	// Globbed plan artifact
	return o.resolveArtifactPath(f, "plan")
}

// globCyclePlan finds the cycle plan file in a cycle directory.
func globCyclePlan(cycleDir string) string {
	matches, _ := filepath.Glob(filepath.Join(cycleDir, "*-plan.md"))
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

// writeQAFile extracts the session's QALog and writes it into phaseDir as
// qa-answers.md. Returns silently if the session view is missing or its QALog
// is empty.
func (o *Orchestrator) writeQAFile(sessionID, phaseDir string) error {
	if sessionID == "" || phaseDir == "" {
		return nil
	}
	if o.deps.Sessions == nil {
		return nil
	}
	view := o.deps.Sessions.GetSession(sessionID)
	if view == nil {
		return nil
	}
	qaLog := view.QALog()
	if len(qaLog) == 0 {
		return nil
	}
	if err := os.MkdirAll(phaseDir, 0o755); err != nil {
		return fmt.Errorf("creating phase dir for qa file: %w", err)
	}
	if _, err := agent.WriteQAFile(qaLog, phaseDir); err != nil {
		return fmt.Errorf("writing qa file: %w", err)
	}
	return nil
}

// globPhaseArtifact finds the most recent non-excluded artifact in a phase dir.
func globPhaseArtifact(phaseDir string) string {
	var bestPath string
	var bestModTime int64

	matches, _ := filepath.Glob(filepath.Join(phaseDir, "*.md"))
	for _, m := range matches {
		if agent.IsArtifactExcluded(filepath.Base(m)) {
			continue
		}
		info, err := os.Stat(m)
		if err != nil {
			continue
		}
		if mt := info.ModTime().UnixNano(); bestPath == "" || mt > bestModTime {
			bestPath = m
			bestModTime = mt
		}
	}
	return bestPath
}
