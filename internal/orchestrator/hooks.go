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
	"path/filepath"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// BuildHooks constructs an orchestrator.Hooks bundle from the side-effectful
// collaborators the orchestrator needs to notify at lifecycle points:
//
//   - obs       — observability facade (nil-safe; methods no-op on nil receiver)
//   - permStore — permission rule store (nil permitted; ImportRepoSettings is skipped)
//   - fs        — feature store (used to reload feature state when building a
//     SpanContext for events that carry no full feature payload)
//   - baseDir   — feature store base directory; used by OnFeatureSummaryNeeded
//     to compute per-feature directory paths. Empty string disables summary
//     writes (tests wire nil fs and "" baseDir).
//
// Every returned hook is safe to call without the side-effect collaborator
// being ready: nil checks short-circuit at the top of each hook. This keeps
// tests simple (pass nils / zero values) and lets the fx module hand back a
// working orchestrator even if observability is disabled.
func BuildHooks(obs *observe.Observer, permStore *permission.Store, fs ports.FeatureStore, baseDir ...string) Hooks {
	// Variadic for backwards compatibility — existing tests call
	// BuildHooks(obs, permStore, fs) without a baseDir. Production wiring
	// (module.go) passes the store's BaseDir explicitly.
	var summaryBaseDir string
	if len(baseDir) > 0 {
		summaryBaseDir = baseDir[0]
	}

	// loadSpan re-reads the feature and builds a SpanContext rooted at the
	// feature-level span. Used by hooks that only receive a featureID.
	loadSpan := func(featureID string) (observe.SpanContext, bool) {
		if fs == nil {
			return observe.SpanContext{}, false
		}
		f, err := fs.Load(featureID)
		if err != nil || f == nil {
			return observe.SpanContext{}, false
		}
		return observe.SpanContextForFeature(featureID, f.TraceID, f.Name, f.FeatureSpanID).WithRun(f.ActiveRun), true
	}

	return Hooks{
		OnFeatureCreated: func(f *feature.Feature) {
			if f == nil || permStore == nil {
				return
			}
			for _, repo := range f.Repos {
				_ = permission.ImportRepoSettings(repo.Path, repo.Name, permStore)
			}
		},
		OnFeatureStarted: func(featureID string) {
			if obs == nil || fs == nil {
				return
			}
			f, err := fs.Load(featureID)
			if err != nil || f == nil {
				return
			}
			sc := observe.SpanContextForFeature(featureID, f.TraceID, f.Name, f.FeatureSpanID).WithRun(f.ActiveRun)
			if f.FeatureSpanID == "" {
				f.FeatureSpanID = sc.SpanID
				_ = fs.Save(f)
			}
			repos := make([]string, len(f.Repos))
			for i, r := range f.Repos {
				repos[i] = r.Name
			}
			obs.FeatureStarted(sc, f.Name, repos, string(f.EffectivePipeline()))
		},
		OnFeatureInterrupted: func(featureID string) {
			if obs == nil {
				return
			}
			sc, ok := loadSpan(featureID)
			if !ok {
				return
			}
			// Phase string is best-effort — the caller knows only the ID.
			f, err := fs.Load(featureID)
			if err != nil || f == nil {
				obs.FeatureInterrupted(sc, "")
				return
			}
			obs.FeatureInterrupted(sc, f.CurrentPhase.String())
		},
		OnSetupEvent: func(ev feature.SetupEvent) {
			if obs == nil {
				return
			}
			sc, ok := loadSpan(ev.FeatureID)
			if !ok {
				return
			}
			if ev.RunNumber > 0 {
				sc = sc.WithRun(ev.RunNumber)
			}
			obs.SetupLifecycle(sc, ev)
		},
		OnPhaseStarted: func(featureID string, phase feature.Phase) {
			if obs == nil {
				return
			}
			sc, ok := loadSpan(featureID)
			if !ok {
				return
			}
			obs.PhaseStarted(sc, phase.String())
		},
		OnPhaseCompleted: func(featureID string, phase feature.Phase, err error) {
			if obs == nil {
				return
			}
			sc, ok := obs.ActivePhaseSpanContext(featureID)
			if !ok {
				var spanOk bool
				sc, spanOk = loadSpan(featureID)
				if !spanOk {
					return
				}
			}
			obs.PhaseCompleted(sc, phase.String(), 0, err)
		},
		OnRecoveryScanned: func(items []ports.RecoveryItem) {
			if obs == nil {
				return
			}
			// Per-feature fan-out: build alive/total counts per feature, then
			// emit one recovery.scanned event per feature.
			type featureCount struct {
				total int
				alive int
			}
			counts := map[string]*featureCount{}
			for _, item := range items {
				if item.Feature == nil {
					continue
				}
				fid := item.Feature.ID
				c, ok := counts[fid]
				if !ok {
					c = &featureCount{}
					counts[fid] = c
				}
				c.total++
				if item.ProcessAlive {
					c.alive++
				}
			}
			emitted := map[string]bool{}
			for _, item := range items {
				if item.Feature == nil {
					continue
				}
				fid := item.Feature.ID
				if emitted[fid] {
					continue
				}
				c, ok := counts[fid]
				if !ok {
					continue
				}
				sc := observe.SpanContextForFeature(fid, item.Feature.TraceID, item.Feature.Name, item.Feature.FeatureSpanID).WithRun(item.Feature.ActiveRun)
				obs.RecoveryScanned(sc, c.total, c.alive)
				emitted[fid] = true
			}
		},
		OnRecoveryAction: func(featureID, repoName, action string) {
			if obs == nil {
				return
			}
			sc, ok := loadSpan(featureID)
			if !ok {
				return
			}
			f, err := fs.Load(featureID)
			phase := ""
			if err == nil && f != nil {
				phase = f.CurrentPhase.String()
			}
			obs.RecoveryAction(sc, action, phase, true)
		},
		OnFeatureCompleted: func(featureID string, f *feature.Feature) {
			if obs == nil || f == nil {
				return
			}
			sc := observe.SpanContextForFeature(featureID, f.TraceID, f.Name, f.FeatureSpanID).WithRun(f.ActiveRun)
			obs.FeatureCompleted(sc, f.TotalCost(), f.TotalRuntime())
		},
		OnFeatureFailed: func(featureID string, code errcat.Code, class errcat.Class, diagnostics string) {
			if obs == nil {
				return
			}
			sc, ok := loadSpan(featureID)
			if !ok {
				return
			}
			obs.FeatureFailed(sc, string(code), string(class), diagnostics)
		},
		OnReviewRequired: func(featureID string, phase feature.Phase) {
			if obs == nil {
				return
			}
			sc, ok := loadSpan(featureID)
			if !ok {
				return
			}
			obs.ReviewStarted(sc, 0)
		},
		OnPublishStarted: func(featureID string) {
			if obs == nil {
				return
			}
			sc, ok := loadSpan(featureID)
			if !ok {
				return
			}
			obs.PhaseStarted(sc, feature.PhasePublish.String())
		},
		OnPublishCompleted: func(featureID string, prURLs map[string]string, err error) {
			if obs == nil {
				return
			}
			sc, ok := loadSpan(featureID)
			if !ok {
				return
			}
			obs.PhaseCompleted(sc, feature.PhasePublish.String(), 0, err)
		},
		OnFeatureSummaryNeeded: func(featureID string, f *feature.Feature) {
			if obs == nil || summaryBaseDir == "" {
				return
			}
			// If the caller did not pass a feature, best-effort reload so the
			// summary sees the freshest state after terminal transitions.
			if f == nil && fs != nil {
				loaded, err := fs.Load(featureID)
				if err == nil && loaded != nil {
					f = loaded
				}
			}
			if f == nil {
				return
			}
			repoStates := make(map[string]observe.RepoSummaryInput)
			for name, rs := range f.RepoStates {
				if rs == nil {
					continue
				}
				// Iteration counters live on the run / feature under the
				// unified flow, not per-repo.
				status := "untouched"
				switch {
				case rs.LastError != "":
					status = "failed"
				case rs.PRURL != "":
					status = "published"
				case rs.Touched:
					status = "touched"
				}
				repoStates[name] = observe.RepoSummaryInput{
					Status:    status,
					Iteration: 0,
					PRURL:     rs.PRURL,
					LastError: rs.LastError,
				}
			}
			errorCode, errorClass := "", ""
			if rec := f.FailureRecord(); rec != nil {
				rendered := errcat.RenderRecord(*rec)
				errorCode = string(rendered.Code)
				errorClass = string(rendered.Class)
			}
			input := observe.BuildFeatureSummaryInput(
				f.ID,
				filepath.Join(summaryBaseDir, f.ID),
				f.Name,
				f.Status.String(),
				errorCode,
				errorClass,
				f.CurrentRoadmapPhase,
				f.TotalRoadmapPhases,
				f.PhaseTimings,
				f.PhaseCosts,
				repoStates,
				f.ActiveRun,
			)
			_ = obs.WriteFeatureSummary(input)
		},
		OnFeatureConfigChanged: func(featureID string, before, after feature.ConfigSnapshot) {
			if obs == nil {
				return
			}
			sc, ok := loadSpan(featureID)
			if !ok {
				return
			}
			obs.ConfigChanged(sc, before, after)
		},
		OnFeatureRewound: func(featureID string, request feature.RewindRequest, effectiveTarget feature.Phase, sourceRun, newRun int) {
			if obs == nil || fs == nil {
				return
			}
			f, err := fs.Load(featureID)
			if err != nil || f == nil {
				return
			}
			if newRun == 0 {
				newRun = f.ActiveRun
			}
			sc := observe.SpanContextForFeature(featureID, f.TraceID, f.Name, f.FeatureSpanID).WithRun(f.ActiveRun)
			input := observe.RewindEventInput{
				TargetPhase:        request.TargetPhase,
				EffectiveTarget:    effectiveTarget,
				RoadmapPhase:       request.RoadmapPhase,
				TotalRoadmapPhases: f.TotalRoadmapPhases,
				SourceRun:          sourceRun,
				NewRun:             newRun,
			}
			if sourceRun > 0 {
				if sealed, err := fs.LoadRun(featureID, sourceRun); err == nil && sealed != nil {
					input.BackupBranches = sealed.BackupBranches
				}
			}
			if newRun > 0 {
				if forked, err := fs.LoadRun(featureID, newRun); err == nil && forked != nil {
					input.CarriedPhases = forked.CarriedPhases
					if input.TotalRoadmapPhases == 0 {
						input.TotalRoadmapPhases = forked.TotalRoadmapPhases
					}
				}
			}
			obs.FeatureRewound(sc, input)
		},
	}
}
