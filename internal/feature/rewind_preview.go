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

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// RewindPRConsequence describes one PR that a rewind would close.
type RewindPRConsequence struct {
	Repo  string `json:"repo"`
	PRURL string `json:"pr_url"`
}

// ResetKind constants describe the worktree reset strategy a rewind would
// perform. resetKindNone is an internal sentinel filtered before a consequence
// escapes the preview boundary.
const (
	ResetKindAnchor    = "anchor"
	resetKindBase      = "base"
	resetKindBaseLocal = "base-local"
	resetKindNone      = "none"
)

// RewindWorktreeConsequence describes one worktree reset a rewind would
// perform. ResetKind is one of the ResetKind constants above.
type RewindWorktreeConsequence struct {
	Repo      string `json:"repo"`
	ResetKind string `json:"reset_kind"`
}

// RewindPreviewResult is the authoritative, side-effect-free preview of a
// rewind. It reuses the manager's rewind-choice, partial-roadmap,
// carry-forward, and reset-anchor rules so a client never reproduces the
// matrix. No field is borrowed from a future fork; everything is computed
// from the current feature and its active run.
type RewindPreviewResult struct {
	// Eligible is false when the target is invalid for the current state;
	// ValidationFindings holds the human-readable reasons.
	Eligible bool `json:"eligible"`
	// SourceRunNumber is the active run that would be sealed.
	SourceRunNumber int `json:"source_run_number"`
	// SourceRevision is a hash of the rewind-relevant feature state at
	// preview time. Execution must present the same value (and run number)
	// or be rejected as stale.
	SourceRevision string `json:"source_revision"`
	// TargetPhase / EffectivePhase / RoadmapPhase / UpgradePipeline echo
	// the resolved request. EffectivePhase may differ from TargetPhase when
	// an upgrade-from-medium left KB unbuilt.
	TargetPhase     Phase           `json:"target_phase"`
	EffectivePhase  Phase           `json:"effective_phase"`
	RoadmapPhase    int             `json:"roadmap_phase"`
	UpgradePipeline PipelineProfile `json:"upgrade_pipeline"`
	// ValidPhases are the hierarchical phase choices the user may pick.
	ValidPhases []RewindChoice `json:"valid_phases"`
	// ValidRoadmapPhases are the roadmap phases available for a partial
	// Implement rewind (only when TargetPhase is Implement on a multi-phase
	// roadmap). The current roadmap phase is included when valid.
	ValidRoadmapPhases []int `json:"valid_roadmap_phases"`
	// UpgradePipelineOptions are the pipeline profiles the user may upgrade
	// into (empty when no upgrade is available).
	UpgradePipelineOptions []PipelineProfile `json:"upgrade_pipeline_options"`
	// CarriedPhases is the set of phase directories that would be copied
	// from the sealed run into the fork. CarriedFromRun is the source run.
	CarriedPhases  []string `json:"carried_phases"`
	CarriedFromRun int      `json:"carried_from_run"`
	// PRConsequences lists PRs that would be closed (publishable features).
	PRConsequences []RewindPRConsequence `json:"pr_consequences"`
	// WorktreeConsequences lists worktree resets that would be performed.
	WorktreeConsequences []RewindWorktreeConsequence `json:"worktree_consequences"`
	// BackupBranchRepos lists repos that would receive a backup branch.
	BackupBranchRepos []string `json:"backup_branch_repos"`
	// ValidationFindings holds redacted, human-readable validation issues
	// (empty when Eligible).
	ValidationFindings []string `json:"validation_findings"`
}

// validateRewindTarget checks that target appears in the rewind-choice list.
// It returns the same error messages used by both the preview and execution
// paths so a client never reproduces the validation logic.
func validateRewindTarget(choices []RewindChoice, target Phase, status Status) error {
	for _, c := range choices {
		if c.Phase == target {
			return nil
		}
	}
	if target == PhaseKnowledgeBase {
		return fmt.Errorf("cannot rewind to knowledge base phase")
	}
	return fmt.Errorf("cannot rewind to %s from current state %s", target, status)
}

// effectiveRewindPhase applies the pipeline-medium escalation rule: when a
// feature was upgraded from PipelineMedium without rewinding, pre-plan phases
// that PipelineMedium lacks are escalated to PhaseKnowledgeBase because KB was
// never completed. Shared by the preview and RewindWithRequest.
func effectiveRewindPhase(f *Feature, target Phase) Phase {
	if f.PipelineUpgradedFrom == PipelineMedium && !PipelineMedium.HasPhase(target) {
		return PhaseKnowledgeBase
	}
	return target
}

// worktreeResetKind decides the worktree reset strategy for a repo during a
// rewind. Returns one of the ResetKind constants, or resetKindNone when no reset
// applies for this repo/target combination. Shared by the preview and
// RewindWithRequest so the decision tree never drifts.
func worktreeResetKind(repo FeatureRepo, partial bool, roadmapPhase int) string {
	switch {
	case partial && roadmapPhase > 1:
		return ResetKindAnchor
	case repo.BaseBranch != "" && repo.Publishable != nil && !*repo.Publishable:
		return resetKindBaseLocal
	case repo.BaseBranch != "":
		return resetKindBase
	default:
		return resetKindNone
	}
}

// FeatureWithUpgrade returns a copy of f with the pipeline set to upgrade and
// PipelineUpgradedFrom preserved (set to the current effective pipeline only
// when empty). When upgrade is empty the copy is identical to f. Shared by the
// rewind preview, the preview's choice computation, and the server's upgrade
// target check.
func FeatureWithUpgrade(f *Feature, upgrade PipelineProfile) Feature {
	upgraded := *f
	if upgrade != "" {
		upgraded.Pipeline = upgrade
		if upgraded.PipelineUpgradedFrom == "" {
			upgraded.PipelineUpgradedFrom = f.EffectivePipeline()
		}
	}
	return upgraded
}

// ParsePhaseDirName resolves a phase dir-name (e.g. "implement", "plan") to a
// Phase. The accepted set covers rewind-relevant phases; "review" and
// "publish" are deliberately excluded because they are not rewind targets.
// Shared by the server's rewind-preview handler and the CLI's phase parser.
func ParsePhaseDirName(name string) (Phase, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "knowledgebase", "knowledge-base", "knowledge base", "kb":
		return PhaseKnowledgeBase, true
	case "inquire":
		return PhaseInquire, true
	case "research":
		return PhaseResearch, true
	case "design":
		return PhaseDesign, true
	case "plan":
		return PhasePlan, true
	case "implement":
		return PhaseImplement, true
	case "final-review", "final review":
		return PhaseFinalReview, true
	default:
		return 0, false
	}
}

// ParsePhaseName is the strict phase-name parser shared by the REST mutation
// module and the validate-artifacts command. It accepts every name
// ParsePhaseDirName accepts plus the review and publish aliases, which have no
// phase directory of their own.
func ParsePhaseName(in string) (Phase, error) {
	if phase, ok := ParsePhaseDirName(in); ok {
		return phase, nil
	}
	switch strings.ToLower(strings.TrimSpace(in)) {
	case "review":
		return PhaseReview, nil
	case "publish":
		return PhasePublish, nil
	default:
		return PhaseResearch, fmt.Errorf("unknown phase %q", in)
	}
}

// RewindPreviewForFeature renders the shared side-effect-free rewind plan.
// The sealedRunDir is the active run directory used to discover carried files.
func RewindPreviewForFeature(f *Feature, sealedRunDir string, request RewindRequest, upgrade PipelineProfile) RewindPreviewResult {
	if f == nil {
		return RewindPreviewResult{
			TargetPhase:        request.TargetPhase,
			RoadmapPhase:       request.RoadmapPhase,
			UpgradePipeline:    upgrade,
			ValidationFindings: []string{"feature not found"},
		}
	}
	plan, planErr := buildRewindPlan(f, sealedRunDir, request, upgrade)
	result := RewindPreviewResult{
		SourceRunNumber:        f.ActiveRun,
		SourceRevision:         plan.SourceRevision,
		TargetPhase:            request.TargetPhase,
		EffectivePhase:         plan.EffectiveTarget,
		RoadmapPhase:           request.RoadmapPhase,
		UpgradePipeline:        upgrade,
		ValidPhases:            plan.Choices,
		ValidRoadmapPhases:     plan.ValidRoadmapPhases,
		UpgradePipelineOptions: plan.UpgradeOptions,
		CarriedPhases:          plan.CarriedPhases,
		CarriedFromRun:         f.ActiveRun,
		PRConsequences:         plan.PRs,
		BackupBranchRepos:      plan.BackupRepos,
	}
	if planErr != nil {
		result.ValidationFindings = []string{planErr.Error()}
		return result
	}
	for _, worktree := range plan.Worktrees {
		if worktree.ResetKind != resetKindNone {
			result.WorktreeConsequences = append(result.WorktreeConsequences, RewindWorktreeConsequence{
				Repo: worktree.Repo.Name, ResetKind: worktree.ResetKind,
			})
		}
	}
	result.Eligible = true
	return result
}

// rewindChoicesForFeature for a possibly-upgraded profile without mutating f.
func rewindChoicesForPreview(f *Feature, upgrade PipelineProfile) []RewindChoice {
	if upgrade == "" {
		return RewindChoicesForFeature(f)
	}
	upgraded := FeatureWithUpgrade(f, upgrade)
	return RewindChoicesForFeature(&upgraded)
}

// rewindUpgradeOptionsForPreview lists pipeline profiles available from the
// effective profile, including a requested upgrade.
func rewindUpgradeOptionsForPreview(f *Feature, upgrade PipelineProfile) []PipelineProfile {
	current := f.EffectivePipeline()
	if upgrade != "" {
		current = upgrade
	}
	return current.UpgradeOptions()
}

// validRoadmapPhasesForFeature returns the roadmap phases a partial Implement
// rewind may target. Phase 1 is always valid (base reset). Phases 2..Total
// require a complete per-repo commit anchor for the previous phase.
func validRoadmapPhasesForFeature(f *Feature, target Phase) []int {
	if target != PhaseImplement || f.TotalRoadmapPhases <= 1 {
		return nil
	}
	run := f.Run()
	if run == nil {
		return nil
	}
	valid := []int{1}
	for p := 2; p <= f.TotalRoadmapPhases; p++ {
		anchors := run.RoadmapPhaseCommitAnchors[p-1]
		if len(anchors) == 0 {
			continue
		}
		complete := true
		for _, repo := range f.Repos {
			if repo.WorktreePath == "" {
				continue
			}
			if anchors[repo.Name] == "" {
				complete = false
				break
			}
		}
		if complete {
			valid = append(valid, p)
		}
	}
	return valid
}

// validatePartialRewindRequestForFeature is the pure (non-Manager) form of
// Manager.validatePartialRewindRequest, reused by the preview so the rule
// never drifts from execution.
func validatePartialRewindRequestForFeature(f *Feature, request RewindRequest) (partialRewindPlan, error) {
	if request.RoadmapPhase == 0 {
		return partialRewindPlan{}, nil
	}
	if request.TargetPhase != PhaseImplement {
		return partialRewindPlan{}, fmt.Errorf("roadmap phase rewind is only valid for Implement targets")
	}
	if f.TotalRoadmapPhases <= 1 {
		return partialRewindPlan{}, fmt.Errorf("roadmap phase rewind requires a multi-phase roadmap run")
	}
	if request.RoadmapPhase < 1 || request.RoadmapPhase > f.TotalRoadmapPhases {
		return partialRewindPlan{}, fmt.Errorf("roadmap phase %d out of range 1..%d", request.RoadmapPhase, f.TotalRoadmapPhases)
	}
	partial := partialRewindPlan{enabled: true, roadmapPhase: request.RoadmapPhase}
	if request.RoadmapPhase == 1 {
		return partial, nil
	}
	previousPhase := request.RoadmapPhase - 1
	run := f.Run()
	if run == nil {
		return partialRewindPlan{}, fmt.Errorf("missing commit anchor for roadmap phase %d", previousPhase)
	}
	anchors := run.RoadmapPhaseCommitAnchors[previousPhase]
	if len(anchors) == 0 {
		return partialRewindPlan{}, fmt.Errorf("missing commit anchor for roadmap phase %d", previousPhase)
	}
	partial.resetAnchors = make(map[string]string)
	for _, repo := range f.Repos {
		if repo.WorktreePath == "" {
			continue
		}
		sha := anchors[repo.Name]
		if sha == "" {
			return partialRewindPlan{}, fmt.Errorf("missing commit anchor for roadmap phase %d repo %s", previousPhase, repo.Name)
		}
		partial.resetAnchors[repo.Name] = sha
	}
	return partial, nil
}

// rewindRevisionState is the hashed payload for RewindRevision. It captures
// exactly the state a rewind depends on, so a stale preview (whose
// source_revision was computed against an older version of this state) is
// detectable at execution time.
type rewindRevisionState struct {
	ActiveRun          int            `json:"active_run"`
	Status             string         `json:"status"`
	CurrentPhase       string         `json:"current_phase"`
	PendingReviewPhase string         `json:"pending_review_phase"`
	IsRewind           bool           `json:"is_rewind"`
	RoadmapPhase       int            `json:"roadmap_phase"`
	TotalRoadmapPhases int            `json:"total_roadmap_phases"`
	Pipeline           string         `json:"pipeline"`
	PipelineUpgraded   string         `json:"pipeline_upgraded_from"`
	PRURLs             []string       `json:"pr_urls"`
	RepoStates         []repoStateSig `json:"repo_states"`
}

type repoStateSig struct {
	Repo    string `json:"repo"`
	Touched bool   `json:"touched"`
	PRURL   string `json:"pr_url"`
}

// RewindRevision returns a short, stable hash of the rewind-relevant feature
// state. The preview emits it as source_revision; execution must present the
// same value or be rejected as stale before any side effect. The hash changes
// when the active run, status, phase, roadmap progress, pipeline, PRs, or
// per-repo state advance — i.e. whenever a preview would no longer match the
// world it was computed against.
func RewindRevision(f *Feature) string {
	if f == nil {
		return ""
	}
	pendingReview := ""
	if f.PendingReviewPhase != nil {
		pendingReview = f.PendingReviewPhase.DirName()
	}
	prURLs := f.PRURLs()
	prList := make([]string, 0, len(prURLs))
	for name, url := range prURLs {
		if url != "" {
			prList = append(prList, name+":"+url)
		}
	}
	sort.Strings(prList)
	repoNames := make([]string, 0, len(f.RepoStates))
	for name := range f.RepoStates {
		repoNames = append(repoNames, name)
	}
	sort.Strings(repoNames)
	repoSigs := make([]repoStateSig, 0, len(repoNames))
	for _, name := range repoNames {
		st := f.RepoStates[name]
		sig := repoStateSig{Repo: name}
		if st != nil {
			sig.Touched = st.Touched
			sig.PRURL = st.PRURL
		}
		repoSigs = append(repoSigs, sig)
	}
	state := rewindRevisionState{
		ActiveRun:          f.ActiveRun,
		Status:             f.Status.String(),
		CurrentPhase:       f.CurrentPhase.DirName(),
		PendingReviewPhase: pendingReview,
		IsRewind:           f.IsRewind,
		RoadmapPhase:       f.CurrentRoadmapPhase,
		TotalRoadmapPhases: f.TotalRoadmapPhases,
		Pipeline:           string(f.Pipeline),
		PipelineUpgraded:   string(f.PipelineUpgradedFrom),
		PRURLs:             prList,
		RepoStates:         repoSigs,
	}
	data, _ := json.Marshal(state)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:12])
}
