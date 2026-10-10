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

package feature

import "sort"

// rewindPlan holds every choice and side effect decision shared by preview
// and execution. Worktrees retain feature repo order; PRs use repo-name order.
type rewindPlan struct {
	SourceRevision     string
	Choices            []RewindChoice
	UpgradeOptions     []PipelineProfile
	EffectiveTarget    Phase
	ValidRoadmapPhases []int
	CarriedPhases      []string
	PRs                []RewindPRConsequence
	Worktrees          []rewindWorktreePlan
	BackupRepos        []string
	Partial            partialRewindPlan
}

type rewindWorktreePlan struct {
	Repo      FeatureRepo
	ResetKind string
}

func buildRewindPlan(f *Feature, sealedRunDir string, request RewindRequest, upgrade PipelineProfile) (rewindPlan, error) {
	plan := rewindPlan{
		SourceRevision: RewindRevision(f),
		Choices:        rewindChoicesForPreview(f, upgrade),
		UpgradeOptions: rewindUpgradeOptionsForPreview(f, upgrade),
	}
	if err := validateRewindTarget(plan.Choices, request.TargetPhase, f.Status); err != nil {
		return plan, err
	}
	upgraded := FeatureWithUpgrade(f, upgrade)
	plan.EffectiveTarget = effectiveRewindPhase(&upgraded, request.TargetPhase)
	partial, err := validatePartialRewindRequestForFeature(f, request)
	if err != nil {
		return plan, err
	}
	plan.Partial = partial
	plan.ValidRoadmapPhases = validRoadmapPhasesForFeature(f, request.TargetPhase)
	plan.CarriedPhases, err = carryForwardSet(request.TargetPhase, sealedRunDir, partial.roadmapPhase)
	if err != nil {
		return plan, err
	}

	if f.IsPublishable() {
		for repo, url := range f.PRURLs() {
			if url != "" {
				plan.PRs = append(plan.PRs, RewindPRConsequence{Repo: repo, PRURL: url})
			}
		}
		sort.Slice(plan.PRs, func(i, j int) bool { return plan.PRs[i].Repo < plan.PRs[j].Repo })
	}
	if request.TargetPhase.logicalOrder() <= PhaseImplement.logicalOrder() {
		for _, repo := range f.Repos {
			if repo.WorktreePath == "" {
				continue
			}
			plan.Worktrees = append(plan.Worktrees, rewindWorktreePlan{
				Repo: repo, ResetKind: worktreeResetKind(repo, partial.enabled, partial.roadmapPhase),
			})
			plan.BackupRepos = append(plan.BackupRepos, repo.Name)
		}
		sort.Strings(plan.BackupRepos)
	}
	return plan, nil
}
