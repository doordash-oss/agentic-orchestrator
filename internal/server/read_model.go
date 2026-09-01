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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const (
	maxFeatureDetailHistoricalRuns = 5
	promptSnapshotTooLargeCode     = "prompt_snapshot_too_large"
)

var errNeedUserInputGateCollectionTooLarge = errors.New("pending prompt snapshot exceeds the safe response limit")

// Tool names as reported by the LLM control protocol; shared across
// read_model.go, session_model.go, sse.go and their tests.
const (
	toolNameAskUserQuestion = "AskUserQuestion"
	toolNameBash            = "Bash"
	toolNameWrite           = "Write"
	toolNameEdit            = "Edit"
	toolNameMultiEdit       = "MultiEdit"
)

// agentQuestionPrompt is the synthetic question text shown when an
// AskUserQuestion control request has no readable question of its own.
const agentQuestionPrompt = "Agent has a question"

// HelpQueue.Kind values. "question" and "input" both await the user; only
// "coordinating" is a byproduct of a phase session parking between turns, which
// no human needs to answer.
const (
	helpKindQuestion     = "question"
	helpKindInput        = "input"
	helpKindCoordinating = "coordinating"
)

// actionInputKindEnum and actionInputKindString are ActionInput.Kind
// values: the former for inputs whose value must be one of
// ActionInput.Options, the latter for free-form string inputs.
const (
	actionInputKindEnum   = "enum"
	actionInputKindString = "string"
)

// disabledNotLocalOnly is the ActionDisabledReason.Code used when merge
// is requested on a feature that is not local-only.
const disabledNotLocalOnly = "not_local_only"

// disabledStatusNotAllowed is the ActionDisabledReason.Code used when an
// action is blocked solely by the feature's current status.
const disabledStatusNotAllowed = "status_not_allowed"

// controlRequestStatusPending is the ControlRequest/TranscriptMessage
// Status value for a control request awaiting a user decision.
const controlRequestStatusPending = "pending"

// actionCleanup, actionDelete, actionMarkDone, actionMerge, actionPauseStop
// and the entries below are feature action IDs shared between the action
// catalog, the mutation dispatcher and the client request builder.
const (
	actionCleanup        = "cleanup"
	actionDelete         = "delete"
	actionDiscard        = "discard"
	actionMarkDone       = "mark-done"
	actionMerge          = "merge"
	actionNeedUserInput  = "need-user-input"
	actionNeedInputDraft = "need-user-input-draft"
	actionPauseStop      = "pause-stop"
	actionPublish        = "publish"
	actionRebase         = "rebase"
	actionRefactor       = "refactor"
	actionRestart        = "restart"
	actionResume         = "resume"
	actionSetup          = "setup"
	actionStart          = "start"
	actionRetry          = "retry"
	actionRewind         = "rewind"
)

func revisionForAny(v any) string {
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:12])
}

func (h *apiHandler) featureDetailDTO(f *feature.Feature) (FeatureDetail, error) {
	var activeRelationshipChild *feature.Feature
	var relationshipChildren *feature.RelationshipChildren
	active := runSummaryDTO(f.Run(), f)
	historyRunNumbers := boundedHistoricalRunNumbers(f.ActiveRun, f.RunCount, maxFeatureDetailHistoricalRuns)
	history := make([]RunSummary, 0, len(historyRunNumbers))
	if h.store != nil {
		for _, n := range historyRunNumbers {
			run, err := h.store.LoadRun(f.ID, n)
			if err == nil {
				history = append(history, runSummaryDTO(run, f))
			}
		}
	}
	detail := featureDetailFromSummary(h.featureSummaryDTO(f))
	if f.IsChild() {
		detail.ParentID = f.Parent.ParentID
		detail.ParentKind = f.Parent.Kind
		detail.Active = f.IsActiveChild()
		detail.SetupComplete = childSetupComplete(f)
		detail.CloseOutcome = f.Parent.CloseOutcome
		detail.ClosedAt = f.Parent.ClosedAt
		for _, base := range f.Parent.Bases {
			detail.Bases = append(detail.Bases, ChildRepoBase{
				Repo:         base.Repo,
				Sha:          base.SHA,
				ParentBranch: base.ParentBranch,
			})
		}
		// Project the transaction journal for child integration.
		if tx := f.Parent.Transaction; tx != nil {
			detail.Transaction = TransactionJournal{
				Phase:     string(tx.Phase),
				Attention: tx.Attention,
			}
			for _, e := range tx.Entries {
				entry := RepoTransactionEntry{
					Repo:            e.Repo,
					ParentBranch:    e.ParentBranch,
					ParentAnchorSha: e.ParentAnchorSHA,
					ExpectedRefSha:  e.ExpectedRefSHA,
					ChildHeadSha:    e.ChildHeadSHA,
					CandidateSha:    e.CandidateSHA,
					MergeHead:       e.MergeHEAD,
					PrepState:       string(e.PrepState),
					ApplyState:      string(e.ApplyState),
					ObservedSha:     e.ObservedSHA,
					ConflictFiles:   e.ConflictFiles,
					CleanupWarning:  e.CleanupWarning,
					Diagnostics:     e.Diagnostics,
				}
				for _, d := range e.Dirty {
					entry.Dirty = append(entry.Dirty, ChildDirtyDiagnostics{
						Repo:           d.Repo,
						Path:           d.Path,
						Staged:         d.Staged,
						Unstaged:       d.Unstaged,
						Untracked:      d.Untracked,
						StagedTotal:    d.StagedTotal,
						UnstagedTotal:  d.UnstagedTotal,
						UntrackedTotal: d.UntrackedTotal,
					})
				}
				detail.Transaction.Entries = append(detail.Transaction.Entries, entry)
			}
		}
		relationship := relationshipChildDTO(f)
		detail.Relationship = relationship
		if len(f.ReviewFeedback) > 0 {
			detail.ReviewFeedback = append(detail.ReviewFeedback, f.ReviewFeedback...)
		}
	} else {
		children, err := h.relationshipChildrenOf(f.ID, nil)
		if err != nil {
			return FeatureDetail{}, err
		}
		relationshipChildren = children
		detail.ActiveChild = relationshipChildDTO(children.Active)
		detail.ChildHistory = relationshipChildDTOs(children.Closed)
		detail.ChildHistoryTotal = len(detail.ChildHistory)
		activeRelationshipChild = children.Active
	}
	detail.Description = SafeDisplayText(f.Description, 500)
	detail.Summary = SafeDisplayText(f.Summary, 500)
	detail.WaitReason = SafeDisplayText(f.KBWaitMessage, 240)
	detail.Pipeline = string(f.Pipeline)
	detail.Models = f.Models
	detail.Effort = f.Effort
	detail.Inquireness = f.Inquireness
	detail.RiskLevel = f.RiskLevel
	detail.ExitCriteria = SafeDisplayText(f.ExitCriteria, 500)
	autoReviewEnabled, autoReviewSource := feature.ResolveAutomaticReview(
		f.AutomaticReviewMode,
		h.configOrDefault().Defaults.AutomaticReviewEnabled,
	)
	detail.AutomaticReview = AutomaticReviewState{
		Mode:    AutomaticReviewStateMode(feature.NormalizeAutomaticReviewMode(f.AutomaticReviewMode)),
		Enabled: autoReviewEnabled,
		Source:  AutomaticReviewStateSource(autoReviewSource),
	}
	detail.ActiveRunDetail = &active
	detail.HistoricalRuns = history
	detail.RepoStatus = h.repoStatusDTOs(f)
	detail.Timing = timingDTO(f)
	detail.Cost = costDTO(f)
	detail.ReviewGate = ReviewGate{
		ReviewingGate:     f.ReviewingGate,
		ReviewFixing:      f.ReviewFixing,
		ValidatingPlan:    f.ValidatingPlan,
		ValidatorStatuses: copyStringMap(f.ValidatorStatuses),
	}
	for _, item := range f.VerificationItems {
		detail.VerificationItems = append(detail.VerificationItems, VerificationItem{Name: item.Name, State: item.State})
	}
	// FeatureDetail carries no cycle field: the cycle subsystem is deleted.
	detail.Actions = h.actionCatalogDTOs(f, detail.ActiveChild != nil)
	// Destructive actions carry a server-authoritative impact preview so
	// confirmations enumerate the exact relationship and resources at stake.
	if f.IsChild() {
		attachImpactPreview(detail.Actions, actionDiscard, h.childDiscardImpactPreview(f))
	} else {
		attachImpactPreview(detail.Actions, actionDelete, h.parentCascadeDeleteImpactPreview(f, relationshipChildren))
	}
	if childHasIntegrationAttention(activeRelationshipChild) {
		appendDisabledReason(detail.Actions, ActionDisabledReason{
			Code:    "integration_attention",
			Message: "the active child relationship requires integration attention",
		}, actionDelete)
	}
	if !f.IsChild() && detail.ActiveChild == nil {
		if reason, dirty := h.refactorEntryDisabledReason(f); dirty {
			disableAction(detail.Actions, actionRefactor, reason)
			disableAction(detail.Actions, actionReviewFeedback, reason)
			disableAction(detail.Actions, actionRebase, reason)
		}
	}
	if f.HasTerminalFailure() {
		detail.Failure = &Failure{
			Type:    f.FailureType,
			Message: SafeDisplayText(f.LastError, 240),
		}
	}
	if f.Status == feature.StatusNeedUserInput && f.PendingNeedUserInputPath != "" {
		gate := needUserInputGateDTO(f.ID, entityFeature, "", f.CurrentIteration, f.InputNotifications, f.PendingNeedUserInputPath)
		detail.NeedUserInput = &gate
	}
	detail.Warnings = append(detail.Warnings, effortDriftWarnings(f, h.registry)...)
	return detail, nil
}

func (h *apiHandler) activeResumeIndicator(f *feature.Feature) (bool, int) {
	if h == nil || h.store == nil || f == nil || f.ActiveRun <= 0 {
		return false, 0
	}
	unitDir, ok := agent.ResumeUnitDir(stateDirForFeatureRead(h.store, f), f)
	if !ok {
		return false, 0
	}
	var resumed bool
	var resumeCount int
	record, err := agent.ReadResumeRecord(unitDir)
	if err == nil && record != nil {
		resumed = record.Resumed || record.ResumeCount > 0
		resumeCount = max(record.ResumeCount, 0)
	} else if f.CurrentPhase == feature.PhaseImplement {
		meta, metaErr := agent.NewArtifactManager("").ReadMeta(unitDir)
		if metaErr == nil {
			resumed = meta.Resumed || meta.ResumeCount > 0
			resumeCount = max(meta.ResumeCount, 0)
		}
	}
	childResumed, childCount := aggregateChildResumeRecords(unitDir)
	return resumed || childResumed, resumeCount + childCount
}

func aggregateChildResumeRecords(unitDir string) (bool, int) {
	var resumed bool
	var resumeCount int
	rootSidecar := filepath.Join(unitDir, agent.ResumeSidecarFile)
	_ = filepath.WalkDir(unitDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || entry.Name() != agent.ResumeSidecarFile || path == rootSidecar {
			return nil
		}
		record, readErr := agent.ReadResumeRecord(filepath.Dir(path))
		if readErr != nil || record == nil {
			return nil
		}
		if record.Resumed || record.ResumeCount > 0 {
			resumed = true
			resumeCount += max(record.ResumeCount, 0)
		}
		return nil
	})
	return resumed, resumeCount
}

func activeImplementIterationDirForRead(runDir string, f *feature.Feature) string {
	if f == nil {
		return ""
	}
	// No cycle/refactor path prefix applies: those subsystems were deleted and
	// their prefixes are treated as empty.
	var implementDir string
	if f.CurrentRoadmapPhase > 0 {
		implementDir = filepath.Join(runDir, fmt.Sprintf("phase-%02d", f.CurrentRoadmapPhase), "implement")
	} else {
		implementDir = filepath.Join(runDir, "implement")
	}
	return filepath.Join(implementDir, fmt.Sprintf("iteration-%02d", f.CurrentIteration))
}

func stateDirForFeatureRead(store FeatureReader, f *feature.Feature) string {
	if store == nil || f == nil || f.ActiveRun <= 0 {
		return ""
	}
	runDir := store.RunDir(f.ID, f.ActiveRun)
	return filepath.Dir(filepath.Dir(filepath.Dir(runDir)))
}

func childHasIntegrationAttention(child *feature.Feature) bool {
	if child == nil || child.Parent == nil || child.Parent.Transaction == nil {
		return false
	}
	tx := child.Parent.Transaction
	return tx.Phase == feature.TransactionPhaseAttention || tx.Attention != "" || tx.AnyApplyAttention()
}

func appendDisabledReason(actions []Action, reason ActionDisabledReason, excludedActionID string) {
	for i := range actions {
		if actions[i].Enabled || actions[i].ID == excludedActionID {
			continue
		}
		actions[i].DisabledReasons = append(actions[i].DisabledReasons, reason)
	}
}

// refactorEntryDisabledReason reports the dirty_parent disabled reason for a
// refactor-eligible parent whose worktrees carry uncommitted changes. When
// the worktree capability is wired it is the authoritative check — its
// staged/unstaged/untracked inspection matches the launch-time preflight —
// and the reason carries the same structured per-repository diagnostics the
// parent_worktrees_dirty mutation error returns, so clients can present the
// remediation immediately instead of only after a failed submission. Without
// the capability, the coarser freshness probe keeps the historical boolean.
//
// A repository whose state could not be determined — probe timeout, failure,
// or unknown freshness — blocks entry too: these actions rewrite the parent's
// worktrees, so an unverifiable worktree must never pass as a clean one.
func (h *apiHandler) refactorEntryDisabledReason(f *feature.Feature) (ActionDisabledReason, bool) {
	if f == nil {
		return ActionDisabledReason{}, false
	}
	reason := ActionDisabledReason{
		Code:    "dirty_parent",
		Message: "worktree has uncommitted changes",
	}
	if h.cleanliness != nil {
		payload, indeterminate := h.dirtyRepoDiagnostics(f.Repos)
		if len(payload) > 0 {
			reason.Target = map[string]any{"repos": payload}
			return reason, true
		}
		if len(indeterminate) > 0 {
			return unknownWorktreeStateReason(indeterminate), true
		}
		return ActionDisabledReason{}, false
	}
	if h.freshness == nil {
		return ActionDisabledReason{}, false
	}
	var indeterminate []string
	for name, freshness := range h.probeRepoFreshness(f) {
		if freshness == RepoFreshnessLocalChanges {
			return reason, true
		}
		if freshness == RepoFreshnessUnknown {
			indeterminate = append(indeterminate, name)
		}
	}
	if len(indeterminate) > 0 {
		sort.Strings(indeterminate)
		return unknownWorktreeStateReason(indeterminate), true
	}
	return ActionDisabledReason{}, false
}

// unknownWorktreeStateReason blocks an entry action whose preconditions could
// not be evaluated, naming the repositories whose state is unknown.
func unknownWorktreeStateReason(repos []string) ActionDisabledReason {
	return ActionDisabledReason{
		Code:    "worktree_state_unknown",
		Message: "worktree state could not be determined; a clean worktree cannot be confirmed",
		Target:  map[string]any{"repos": repos},
	}
}

// dirtyRepoDiagnostics converts the dirty parent repositories into the
// categorized payload shared with the launch-time error mapping. The second
// return names repositories whose worktree could not be read at all, which
// callers must treat as not clean. A probe that only exceeded its bound is
// omitted from both: it carries no evidence either way, and blocking on it
// would permanently disable actions on a repo slower than the bound.
func (h *apiHandler) dirtyRepoDiagnostics(repos []feature.FeatureRepo) ([]map[string]any, []string) {
	if h.cleanliness == nil {
		return nil, nil
	}
	// Inspections shell out to git per repo; run them concurrently and keep
	// the declared repo order for the payload.
	reports := make([]*feature.RepoDirtyDiagnostics, len(repos))
	unknown := make([]string, len(repos))
	var wg sync.WaitGroup
	for i, repo := range repos {
		path := repo.WorktreePath
		if path == "" {
			path = repo.Path
		}
		if path == "" {
			continue
		}
		wg.Add(1)
		go func(i int, name, path string) {
			defer wg.Done()
			report, err := h.cleanliness.InspectCleanliness(path, feature.DefaultDirtyPathLimit)
			if err != nil || report == nil {
				// A probe that timed out or yielded to a mutation says nothing
				// about the worktree. Only an unreadable worktree is treated as
				// unverifiable; launch-time inspection re-checks against the
				// uncached authority either way.
				if !errors.Is(err, git.ErrProbeTimeout) && !errors.Is(err, git.ErrWorktreeBusy) {
					unknown[i] = name
				}
				return
			}
			if !report.Dirty() {
				return
			}
			reports[i] = &feature.RepoDirtyDiagnostics{
				Repo:           name,
				Path:           path,
				Staged:         report.Staged,
				Unstaged:       report.Unstaged,
				Untracked:      report.Untracked,
				StagedTotal:    report.StagedTotal,
				UnstagedTotal:  report.UnstagedTotal,
				UntrackedTotal: report.UntrackedTotal,
			}
		}(i, repo.Name, path)
	}
	wg.Wait()
	dirty := make([]feature.RepoDirtyDiagnostics, 0, len(repos))
	for _, report := range reports {
		if report != nil {
			dirty = append(dirty, *report)
		}
	}
	indeterminate := make([]string, 0, len(repos))
	for _, name := range unknown {
		if name != "" {
			indeterminate = append(indeterminate, name)
		}
	}
	if len(dirty) == 0 {
		return nil, indeterminate
	}
	return dirtyDiagnosticsPayload(dirty), indeterminate
}

func disableAction(actions []Action, actionID string, reason ActionDisabledReason) {
	for i := range actions {
		if actions[i].ID != actionID || !actions[i].Enabled {
			continue
		}
		actions[i].Enabled = false
		actions[i].DisabledReasons = []ActionDisabledReason{reason}
		return
	}
}

func featureDetailFromSummary(summary FeatureSummary) FeatureDetail {
	return FeatureDetail{
		ID:           summary.ID,
		Name:         summary.Name,
		Slug:         summary.Slug,
		Status:       summary.Status,
		CurrentPhase: summary.CurrentPhase,
		ActiveRun:    summary.ActiveRun,
		RunCount:     summary.RunCount,
		Repos:        summary.Repos,
		CreatedAt:    summary.CreatedAt,
		Checkpoints:  summary.Checkpoints,
		Progress:     summary.Progress,
		Resumed:      summary.Resumed,
		ResumeCount:  summary.ResumeCount,
		Warnings:     summary.Warnings,
	}
}

// listChildHistoryLimit caps how many closed children a list summary carries.
// The list projection is unbounded otherwise: every closed child adds an entry
// and the aggregate can exceed a client's payload budget. Clients read the
// complete history from the feature detail route.
const listChildHistoryLimit = 5

// listChildHistory bounds a closed-child history to the newest
// listChildHistoryLimit entries, reporting the true total and whether older
// entries were dropped.
func listChildHistory(closed []*feature.Feature) (entries []RelationshipChildSummary, total int, truncated bool) {
	total = len(closed)
	if total > listChildHistoryLimit {
		closed = closed[:listChildHistoryLimit]
		truncated = true
	}
	return relationshipChildSummaryDTOs(closed), total, truncated
}

func (h *apiHandler) relationshipChildrenOf(parentID string, loaded []*feature.Feature) (*feature.RelationshipChildren, error) {
	if parentID == "" {
		return &feature.RelationshipChildren{}, nil
	}
	if reader, ok := h.store.(RelationshipReader); ok {
		children, err := reader.RelationshipChildren(parentID)
		if err != nil {
			return nil, fmt.Errorf("reading children of parent %s: %w", parentID, err)
		}
		return children, nil
	}
	if loaded == nil {
		var err error
		loaded, _, err = listFeatures(h.features)
		if err != nil {
			return nil, fmt.Errorf("listing features for relationship lookup: %w", err)
		}
	}
	children := &feature.RelationshipChildren{}
	for _, candidate := range loaded {
		if candidate == nil || !candidate.IsChild() || candidate.Parent.ParentID != parentID {
			continue
		}
		if candidate.IsActiveChild() {
			if children.Active != nil {
				return nil, fmt.Errorf("parent %s has multiple active children", parentID)
			}
			children.Active = candidate
			continue
		}
		children.Closed = append(children.Closed, candidate)
	}
	sort.Slice(children.Closed, func(i, j int) bool {
		left, right := children.Closed[i], children.Closed[j]
		if left.Parent.ClosedAt == nil || right.Parent.ClosedAt == nil {
			return left.ID < right.ID
		}
		if !left.Parent.ClosedAt.Equal(*right.Parent.ClosedAt) {
			return left.Parent.ClosedAt.After(*right.Parent.ClosedAt)
		}
		if !left.Created.Equal(right.Created) {
			return left.Created.After(right.Created)
		}
		return left.ID < right.ID
	})
	return children, nil
}

// childSetupComplete reports whether the child's active-run setup finished.
func childSetupComplete(f *feature.Feature) bool {
	if f == nil || f.Run() == nil {
		return false
	}
	setup := f.Run().Setup
	return setup != nil && setup.Status == feature.SetupStatusDone
}

// relationshipChildSummaryDTO builds the list-safe projection: every
// relationship field except the preserved diff body, which is unbounded per
// parent once several children have closed and therefore belongs only to the
// detail route (see relationshipChildDTO).
func relationshipChildSummaryDTO(child *feature.Feature) *RelationshipChildSummary {
	if child == nil {
		return nil
	}
	startedAt := child.Created
	if child.StartedAt != nil {
		startedAt = *child.StartedAt
	}
	dto := &RelationshipChildSummary{
		ID:                child.ID,
		Name:              child.Name,
		Kind:              child.Parent.Kind,
		DisplayToken:      child.Parent.Kind + ":" + child.ID,
		DisplayState:      "Active — " + child.Status.String(),
		Pipeline:          string(child.EffectivePipeline()),
		Status:            child.Status.String(),
		RelationshipState: "setting_up",
		StartedAt:         startedAt,
		Cost:              costDTO(child),
		IntegrationState:  "pending",
		Attention:         []RelationshipAttention{},
		CleanupWarnings:   []RelationshipCleanupWarning{},
	}
	if setup := child.Run().Setup; setup != nil {
		dto.SetupStatus = string(setup.Status)
		if setup.Status == feature.SetupStatusFailed {
			dto.LastError = SafeDisplayText(setup.LastError, 240)
			if dto.LastError == "" {
				dto.LastError = SafeDisplayText(child.LastError, 240)
			}
		}
	}
	if childSetupComplete(child) {
		dto.RelationshipState = "active"
		dto.LastError = ""
	}
	if child.Parent.CloseOutcome != "" {
		dto.Outcome = RelationshipChildOutcome(child.Parent.CloseOutcome)
		dto.ClosedAt = child.Parent.ClosedAt
		dto.HasDiffSummary = child.Parent.DiffSummary != ""
		dto.RelationshipState = child.Parent.CloseOutcome
		switch child.Parent.CloseOutcome {
		case feature.ChildCloseOutcomeCompleted:
			dto.DisplayState = "Closed — Completed"
		case feature.ChildCloseOutcomeDiscarded:
			dto.DisplayState = "Closed — Discarded"
		}
	}
	if tx := child.Parent.Transaction; tx != nil {
		if tx.Phase != "" {
			dto.IntegrationState = string(tx.Phase)
		}
		if tx.Attention != "" {
			dto.Attention = append(dto.Attention, RelationshipAttention{
				Code:    "integration_attention",
				Message: SafeDisplayText(tx.Attention, 240),
			})
		}
		for _, entry := range tx.Entries {
			if len(entry.Dirty) > 0 {
				dto.Attention = append(dto.Attention, RelationshipAttention{
					Code:    "dirty_parent",
					Message: "parent repository has uncommitted changes",
					Repo:    entry.Repo,
				})
			}
			if len(entry.ConflictFiles) > 0 {
				dto.Attention = append(dto.Attention, RelationshipAttention{
					Code:    "integration_conflict",
					Message: "integration has unresolved conflicts",
					Repo:    entry.Repo,
				})
			}
			if entry.Diagnostics != "" {
				dto.Attention = append(dto.Attention, RelationshipAttention{
					Code:    "integration_attention",
					Message: SafeDisplayText(entry.Diagnostics, 240),
					Repo:    entry.Repo,
				})
			}
			if entry.CleanupWarning != "" {
				dto.CleanupWarnings = append(dto.CleanupWarnings, RelationshipCleanupWarning{
					Message: SafeDisplayText(entry.CleanupWarning, 240),
					Repo:    entry.Repo,
				})
			}
			if entry.TailWarning != "" {
				dto.CleanupWarnings = append(dto.CleanupWarnings, RelationshipCleanupWarning{
					Message: SafeDisplayText("review-feedback tail: "+entry.TailWarning, 240),
					Repo:    entry.Repo,
				})
			}
		}
	}
	return dto
}

func actionCatalogDTOs(f *feature.Feature) []Action {
	return actionCatalogDTOsWithChildGuard(f, false)
}

// Both guards apply: main's active-child flag, plus the resume-eligibility
// gating that decides whether a failed feature is offered resume.
func (h *apiHandler) actionCatalogDTOs(f *feature.Feature, hasActiveChild bool) []Action {
	if f == nil || f.Status != feature.StatusFailed {
		return actionCatalogDTOsWithChildGuard(f, hasActiveChild)
	}
	eligibility := h.resumeEligibility(f)
	return actionCatalogDTOsWithGuards(f, hasActiveChild, &eligibility)
}

func (h *apiHandler) resumeEligibility(f *feature.Feature) agent.ResumeEligibility {
	var record *agent.ResumeRecord
	var unitDir string
	if h != nil && h.store != nil && f != nil && f.ActiveRun > 0 {
		if dir, ok := agent.ResumeUnitDir(stateDirForFeatureRead(h.store, f), f); ok {
			unitDir = dir
			record, _ = agent.ReadResumeRecord(unitDir)
		}
	}
	var registry *llm.Registry
	if h != nil {
		registry = h.registry
	}
	currentModel := ""
	if f != nil {
		runner := &agent.PhaseRunner{Registry: registry}
		switch f.CurrentPhase {
		case feature.PhaseInquire:
			configured := f.Models.Inquiry
			if configured == "" {
				configured = f.Models.Research
			}
			currentModel = runner.ModelForRole(configured, llm.PhaseInquiry)
		case feature.PhaseResearch:
			currentModel = runner.ModelForRole(f.Models.Research, llm.PhaseResearch)
		case feature.PhaseDesign, feature.PhasePlan:
			currentModel = runner.ModelForRole(f.Models.Planning, llm.PhasePlanning)
		case feature.PhaseImplement:
			currentModel = runner.ModelForRole(f.Models.Implementation, llm.PhaseImplementation)
			parent := agent.EvaluateResumeEligibility(f, record, currentModel, registry)
			if parent.Eligible {
				return parent
			}
			child := agent.EvaluateCompositeResumeEligibility(
				unitDir,
				f,
				registry,
				agent.ResumeParentContext{
					PhaseKey:  agent.ResumePhaseKey(f),
					Iteration: f.CurrentIteration,
				},
				func(string) string {
					return runner.ModelForRole(f.Models.Review, llm.PhaseReview)
				},
			)
			if child.Eligible {
				return child
			}
			return parent
		case feature.PhaseReview, feature.PhaseFinalReview:
			return agent.EvaluateCompositeResumeEligibility(
				unitDir,
				f,
				registry,
				agent.ResumeParentContext{
					PhaseKey:  feature.PhaseFinalReview.DirName(),
					Iteration: f.ReviewIteration,
				},
				func(childKey string) string {
					if childKey == string(agent.RoleFinalReviewFixer) {
						return runner.ModelForRole(f.Models.Implementation, llm.PhaseImplementation)
					}
					return runner.ModelForRole(f.Models.Review, llm.PhaseReview)
				},
			)
		}
	}
	return agent.EvaluateResumeEligibility(f, record, currentModel, registry)
}

// relationshipChildDTO builds the detail projection: the summary plus the
// preserved diff body.
func relationshipChildDTO(child *feature.Feature) *RelationshipChild {
	summary := relationshipChildSummaryDTO(child)
	if summary == nil {
		return nil
	}
	dto := &RelationshipChild{
		ID:                summary.ID,
		Name:              summary.Name,
		Kind:              summary.Kind,
		DisplayToken:      summary.DisplayToken,
		DisplayState:      summary.DisplayState,
		Pipeline:          summary.Pipeline,
		Status:            summary.Status,
		SetupStatus:       summary.SetupStatus,
		RelationshipState: summary.RelationshipState,
		Outcome:           summary.Outcome,
		StartedAt:         summary.StartedAt,
		ClosedAt:          summary.ClosedAt,
		Cost:              summary.Cost,
		IntegrationState:  summary.IntegrationState,
		Attention:         summary.Attention,
		CleanupWarnings:   summary.CleanupWarnings,
		LastError:         summary.LastError,
		HasDiffSummary:    summary.HasDiffSummary,
	}
	// Re-bound at read time: records persisted before the write-time bound
	// existed may hold raw multi-megabyte diffs.
	if summary.HasDiffSummary {
		dto.DiffSummary = feature.BoundDiffSummary(child.Parent.DiffSummary)
	}
	return dto
}

func relationshipChildDTOs(children []*feature.Feature) []RelationshipChild {
	out := make([]RelationshipChild, 0, len(children))
	for _, child := range children {
		if dto := relationshipChildDTO(child); dto != nil {
			out = append(out, *dto)
		}
	}
	return out
}

func relationshipChildSummaryDTOs(children []*feature.Feature) []RelationshipChildSummary {
	out := make([]RelationshipChildSummary, 0, len(children))
	for _, child := range children {
		if dto := relationshipChildSummaryDTO(child); dto != nil {
			out = append(out, *dto)
		}
	}
	return out
}

// disabledParentHasActiveChild is the ActionDisabledReason used when a
// parent action is locked because an active child exists.
var disabledParentHasActiveChild = ActionDisabledReason{
	Code:    "active_child_present",
	Message: "parent mutations are locked while a child is active; only paired config editing and cascade delete are available",
}

// Entry point for callers without resume-eligibility context; the guards
// body below carries the active-child guard and optional resume-eligibility
// gating.
func actionCatalogDTOsWithChildGuard(f *feature.Feature, hasActiveChild bool) []Action {
	return actionCatalogDTOsWithGuards(f, hasActiveChild, nil)
}

func actionCatalogDTOsWithGuards(f *feature.Feature, hasActiveChild bool, resumeEligibility *agent.ResumeEligibility) []Action {
	if f == nil {
		return nil
	}
	status := f.Status
	running := status.IsRunning()
	publishedOrManualReady := status == feature.StatusPublished ||
		(status == feature.StatusCodeReady && !f.Checkpoints.AutoPublish())

	action := func(id string, enabled bool, scope ActionScope, inputs []ActionInput, disabled ...ActionDisabledReason) Action {
		if inputs == nil {
			inputs = []ActionInput{}
		}
		dto := Action{
			ID:             id,
			Enabled:        enabled,
			Scope:          scope,
			RequiredInputs: inputs,
		}
		if !enabled {
			dto.DisabledReasons = disabled
			if len(dto.DisabledReasons) == 0 {
				dto.DisabledReasons = []ActionDisabledReason{disabledStatusReason(status)}
			}
		}
		return dto
	}
	featureScope := ActionScope{Type: entityFeature}

	if f.IsChild() {
		return childActionCatalogDTOs(f, status, running, action, disabledStatusReason)
	}

	canSetup := setupActionEligible(f)
	canStart := !running && (status == feature.StatusCreated ||
		status == feature.StatusInquireReady ||
		status == feature.StatusPlanReady ||
		status == feature.StatusDesignReady ||
		status == feature.StatusImplementReady ||
		status == feature.StatusReviewPassed)
	canStop := running || status == feature.StatusNeedUserInput
	canResume := status == feature.StatusInterrupted ||
		status == feature.StatusNeedUserInput ||
		f.PendingNeedUserInputPath != ""
	// Paused non-failed features resume on paused-session/input-gate presence;
	// failed features are gated by resume eligibility below.
	resumeDisabledReason := ActionDisabledReason{
		Code:    "not_paused",
		Message: "feature has no paused session or input gate",
	}
	if status == feature.StatusFailed && resumeEligibility != nil {
		canResume = resumeEligibility.Eligible
		if !resumeEligibility.Eligible {
			resumeDisabledReason = ActionDisabledReason{
				Code:    string(resumeEligibility.Reason),
				Message: resumeEligibility.Message,
			}
		}
	}
	canRestart := !running
	canPublish := f.IsPublishable() && status == feature.StatusCodeReady && f.Checkpoints.AutoPublish()
	canMerge := !f.IsPublishable() && (status == feature.StatusCodeReady || status == feature.StatusPublished)
	canRewind := !running && (len(feature.RewindChoicesForFeature(f)) > 0 || hasRewindUpgradeTarget(f))
	canPostPublishPass := publishedOrManualReady
	canReviewFeedback := canPostPublishPass && f.IsPublishable() && featureHasPullRequest(f)
	canRetry := status == feature.StatusFailed
	canMarkDone := publishedOrManualReady
	canCleanup := !running
	canDelete := true
	// Only Published or CodeReady top-level parents can launch a refactor
	// child, mirroring the launch validation in feature.CreateRefactorChild.
	canRefactor := status == feature.StatusPublished || status == feature.StatusCodeReady

	// While a child is active, parent mutations are locked except for
	// paired config editing. The action catalog agrees with the
	// authoritative relationship guard enforced at the mutation layer.
	if hasActiveChild {
		canSetup = false
		canStart = false
		canStop = false
		canResume = false
		canRestart = false
		canPublish = false
		canMerge = false
		canRewind = false
		canPostPublishPass = false
		canReviewFeedback = false
		canRetry = false
		canMarkDone = false
		canCleanup = false
		canRefactor = false
	}

	// Prepend the child-guard disabled reason to locked actions.
	childGuardReason := func(enabled bool, fallback ...ActionDisabledReason) []ActionDisabledReason {
		if hasActiveChild && !enabled {
			return append([]ActionDisabledReason{disabledParentHasActiveChild}, fallback...)
		}
		return fallback
	}

	return []Action{
		action(actionSetup, canSetup, featureScope, nil, childGuardReason(canSetup, ActionDisabledReason{Code: "no_pending_setup", Message: "feature has no pending or failed setup work"})...),
		action(actionStart, canStart, featureScope, nil, childGuardReason(canStart, disabledStatusReason(status))...),
		action(actionPauseStop, canStop, featureScope, nil, childGuardReason(canStop, ActionDisabledReason{Code: "not_running", Message: "feature has no active work to pause or stop"})...),
		action(actionResume, canResume, featureScope, nil, childGuardReason(canResume, resumeDisabledReason)...),
		action(actionRestart, canRestart, featureScope, nil, childGuardReason(canRestart, ActionDisabledReason{Code: "running", Message: "feature must stop before restart"})...),
		action(actionPublish, canPublish, featureScope, nil, childGuardReason(canPublish, publishDisabledReason(f))...),
		action(actionMerge, canMerge, featureScope, nil, childGuardReason(canMerge, mergeDisabledReason(f))...),
		action(actionRewind, canRewind, featureScope, []ActionInput{
			{Name: "target_phase", Kind: actionInputKindEnum, Required: true, Options: rewindPhaseOptions(f)},
			{Name: "roadmap_phase", Kind: "integer", Required: false},
			{Name: "upgrade_pipeline", Kind: actionInputKindEnum, Required: false, Options: rewindUpgradePipelineOptions(f)},
		}, childGuardReason(canRewind, ActionDisabledReason{Code: "no_rewind_targets", Message: "feature has no valid rewind targets"})...),
		action(actionRebase, canPostPublishPass, featureScope, nil, childGuardReason(canPostPublishPass, postPublishPassDisabledReason(f))...),
		action(actionReviewFeedback, canReviewFeedback, featureScope, nil, childGuardReason(canReviewFeedback, reviewFeedbackDisabledReason(f))...),
		action(actionRefactor, canRefactor, featureScope, nil, childGuardReason(canRefactor, disabledStatusReason(status))...),
		action(actionRetry, canRetry, featureScope, nil, childGuardReason(canRetry, ActionDisabledReason{Code: "not_failed", Message: "retry is only available for failed features"})...),
		action(actionMarkDone, canMarkDone, featureScope, nil, childGuardReason(canMarkDone, ActionDisabledReason{Code: "not_complete", Message: "feature is not ready to mark done"})...),
		action(actionCleanup, canCleanup, featureScope, []ActionInput{
			{Name: "target", Kind: actionInputKindEnum, Required: false, Options: []string{"worktrees"}},
		}, childGuardReason(canCleanup, ActionDisabledReason{Code: "running", Message: "cleanup is disabled while work is running"})...),
		action(actionDelete, canDelete, featureScope, nil),
	}
}

// childSetupFailed reports whether the child feature carries a failed
// worktree setup that the setup retry action can rerun. Mirrors the
// isFailedSetupFeature predicate used by the retry mutation.
func childSetupFailed(f *feature.Feature) bool {
	if f == nil {
		return false
	}
	setup := f.Run().Setup
	return f.Status == feature.StatusFailed &&
		f.FailureType == feature.FailureWorktreeSetup &&
		setup != nil &&
		setup.Status == feature.SetupStatusFailed
}

// disabledChildSetupIncomplete is the ActionDisabledReason.Code used when
// a child cannot execute because setup is queued, running, or failed.
const disabledChildSetupIncomplete = "setup_incomplete"

// disabledChildRelationshipClosed is the ActionDisabledReason.Code used
// when a child cannot execute because its relationship has settled.
const disabledChildRelationshipClosed = "relationship_closed"

// childExecutionBlockReason returns the error that currently blocks child
// execution: the settled-relationship block on a closed child, or the
// setup-state block while setup is incomplete. Nil means the child may run
// through the ordinary pipeline for its profile.
func childExecutionBlockReason(f *feature.Feature) error {
	if f == nil || !f.IsChild() {
		return nil
	}
	if !f.IsActiveChild() {
		return feature.ErrChildExecutionClosed
	}
	if !f.ChildSetupComplete() {
		return feature.ErrChildExecutionBlocked
	}
	return f.ChildExecutionCapability()
}

// childCapabilityDisabledReason maps a child execution block to the stable
// disabled-reason code echoed by the action catalog: relationship_closed or
// setup_incomplete.
func childCapabilityDisabledReason(err error) ActionDisabledReason {
	switch {
	case errors.Is(err, feature.ErrChildExecutionClosed):
		return ActionDisabledReason{Code: disabledChildRelationshipClosed, Message: "the child relationship is closed; the settled child cannot execute"}
	case errors.Is(err, feature.ErrChildExecutionBlocked):
		return ActionDisabledReason{Code: disabledChildSetupIncomplete, Message: "child setup is queued, running, or failed; only setup and setup-retry are available"}
	default:
		return ActionDisabledReason{Code: disabledStatusNotAllowed, Message: err.Error()}
	}
}

// childActionCatalogDTOs builds the restricted child catalog: ordinary
// start/resume/restart execution entries gated by the same capability check
// the mutations enforce, plus setup-retry and discard. Child delivery actions
// (publish, merge, rewind, refactor, mark-done, post-publish passes) are never
// offered: a child's work reaches the parent only through integration. Child
// cleanup and single-record delete are also unavailable while the relationship
// is active; the authoritative RelationshipGuard enforces the same policy at
// the mutation layer.
func childActionCatalogDTOs(f *feature.Feature, status feature.Status, running bool,
	action func(string, bool, ActionScope, []ActionInput, ...ActionDisabledReason) Action,
	disabledStatusReason func(feature.Status) ActionDisabledReason) []Action {

	featureScope := ActionScope{Type: entityFeature}
	if !f.IsActiveChild() {
		closed := ActionDisabledReason{
			Code:    disabledChildRelationshipClosed,
			Message: "the child relationship is closed; automatic reconciliation owns any remaining cleanup",
		}
		return []Action{
			action(actionStart, false, featureScope, nil, closed),
			action(actionPauseStop, false, featureScope, nil, closed),
			action(actionResume, false, featureScope, nil, closed),
			action(actionRestart, false, featureScope, nil, closed),
			action(actionRetry, false, featureScope, nil, closed),
			action(actionDiscard, false, featureScope, nil, closed),
		}
	}
	blockErr := childExecutionBlockReason(f)
	blockedReason := func(fallback ActionDisabledReason) ActionDisabledReason {
		if blockErr != nil {
			return childCapabilityDisabledReason(blockErr)
		}
		return fallback
	}
	runnable := blockErr == nil
	restartStopped := ActionDisabledReason{Code: "running", Message: "feature must stop before restart"}
	restartReason := blockedReason(restartStopped)
	stopped := !running
	canStart := runnable && stopped && (status == feature.StatusCreated ||
		status == feature.StatusPlanReady ||
		status == feature.StatusImplementReady ||
		status == feature.StatusReviewPassed)
	canStop := runnable && running
	canResume := runnable && (status == feature.StatusInterrupted ||
		status == feature.StatusNeedUserInput ||
		f.PendingNeedUserInputPath != "")
	canRestart := runnable && !running

	return []Action{
		action(actionStart, canStart, featureScope, nil, blockedReason(disabledStatusReason(status))),
		action(actionPauseStop, canStop, featureScope, nil, blockedReason(ActionDisabledReason{Code: "not_running", Message: "child has no active work to pause or stop"})),
		action(actionResume, canResume, featureScope, nil, blockedReason(ActionDisabledReason{Code: "not_paused", Message: "feature has no paused session or input gate"})),
		action(actionRestart, canRestart, featureScope, nil, restartReason),
		action(actionRetry, childSetupFailed(f), featureScope, nil, ActionDisabledReason{Code: "not_failed", Message: "retry is only available for failed features"}),
		// Discard must be available for every active child — running,
		// paused, failed, interrupted, review-gated, input-blocked, and
		// already-discarding — because the durable discard state machine
		// itself records intent, stops sessions, and waits for
		// quiescence. Gating it on "stopped" would hide the primary
		// abandon flow exactly when a running child needs it. Repeated
		// requests converge on the same idempotent outcome.
		action(actionDiscard, f.IsActiveChild(), featureScope, nil, ActionDisabledReason{Code: "not_active", Message: "discard is only available for active children"}),
	}
}

// setupActionEligible reports whether the setup action applies: either the
// feature has queued durable setup that has not completed (fresh run) or its
// setup failed and only the unfinished tasks are retryable.
func setupActionEligible(f *feature.Feature) bool {
	if f == nil {
		return false
	}
	setup := f.Run().Setup
	if setup == nil {
		return false
	}
	if f.Status == feature.StatusSettingUpWorktrees {
		return setup.Status == feature.SetupStatusQueued || setup.Status == feature.SetupStatusRunning
	}
	return f.Status == feature.StatusFailed &&
		f.FailureType == feature.FailureWorktreeSetup &&
		setup.Status == feature.SetupStatusFailed
}

func disabledStatusReason(status feature.Status) ActionDisabledReason {
	return ActionDisabledReason{
		Code:    disabledStatusNotAllowed,
		Message: "action is not available while feature status is " + status.String(),
	}
}

func publishDisabledReason(f *feature.Feature) ActionDisabledReason {
	if f == nil {
		return disabledStatusReason(feature.StatusCreated)
	}
	if !f.IsPublishable() {
		return ActionDisabledReason{Code: "local_only", Message: "feature has at least one local-only repo"}
	}
	if f.Status == feature.StatusPublished || f.Status == feature.StatusDone {
		return ActionDisabledReason{Code: "already_published", Message: "feature already has a published terminal state"}
	}
	if f.Checkpoints.ManualPublish && f.Status == feature.StatusCodeReady {
		return ActionDisabledReason{Code: "manual_publish_required", Message: "feature is waiting for manual publish confirmation"}
	}
	return disabledStatusReason(f.Status)
}

func mergeDisabledReason(f *feature.Feature) ActionDisabledReason {
	if f == nil {
		return disabledStatusReason(feature.StatusCreated)
	}
	if f.IsPublishable() {
		return ActionDisabledReason{Code: disabledNotLocalOnly, Message: "merge is only available for local-only features"}
	}
	return disabledStatusReason(f.Status)
}

func postPublishPassDisabledReason(f *feature.Feature) ActionDisabledReason {
	if f == nil {
		return disabledStatusReason(feature.StatusCreated)
	}
	return disabledStatusReason(f.Status)
}

func reviewFeedbackDisabledReason(f *feature.Feature) ActionDisabledReason {
	if f == nil {
		return disabledStatusReason(feature.StatusCreated)
	}
	publishedOrManualReady := f.Status == feature.StatusPublished ||
		(f.Status == feature.StatusCodeReady && !f.Checkpoints.AutoPublish())
	if !publishedOrManualReady {
		return disabledStatusReason(f.Status)
	}
	if !f.IsPublishable() {
		return ActionDisabledReason{Code: "not_publishable", Message: "review feedback requires a publishable parent"}
	}
	if !featureHasPullRequest(f) {
		return ActionDisabledReason{Code: "no_pull_request", Message: "review feedback requires at least one repository with a pull request"}
	}
	return disabledStatusReason(f.Status)
}

func featureHasPullRequest(f *feature.Feature) bool {
	if f == nil {
		return false
	}
	for _, state := range f.RepoStates {
		if state != nil && strings.TrimSpace(state.PRURL) != "" {
			return true
		}
	}
	return false
}

func rewindPhaseOptions(f *feature.Feature) []string {
	choices := feature.RewindChoicesForFeature(f)
	out := make([]string, 0, len(choices))
	for _, choice := range choices {
		out = append(out, choice.Phase.DirName())
	}
	return out
}

func rewindUpgradePipelineOptions(f *feature.Feature) []string {
	opts := f.EffectivePipeline().UpgradeOptions()
	out := make([]string, 0, len(opts))
	for _, opt := range opts {
		out = append(out, string(opt))
	}
	return out
}

func hasRewindUpgradeTarget(f *feature.Feature) bool {
	if f == nil {
		return false
	}
	current := f.EffectivePipeline()
	for _, option := range current.UpgradeOptions() {
		upgraded := feature.FeatureWithUpgrade(f, option)
		for _, choice := range feature.RewindChoicesForFeature(&upgraded) {
			if choice.Phase == feature.PhaseInquire {
				return true
			}
		}
	}
	return false
}

func boundedHistoricalRunNumbers(activeRun, runCount, limit int) []int {
	if runCount <= 0 || limit <= 0 {
		return nil
	}
	runNumbers := make([]int, 0, min(limit, max(runCount-1, 0)))
	for n := runCount; n >= 1 && len(runNumbers) < limit; n-- {
		if n == activeRun {
			continue
		}
		runNumbers = append(runNumbers, n)
	}
	sort.Ints(runNumbers)
	return runNumbers
}

func runSummaryDTO(run *feature.Run, f *feature.Feature) RunSummary {
	if run == nil {
		return RunSummary{}
	}
	dto := RunSummary{
		RunNumber:       run.RunNumber,
		StartedAt:       run.StartedAt,
		SealedAt:        run.SealedAt,
		SealReason:      string(run.SealReason),
		CurrentPhase:    f.CurrentPhase.String(),
		PhaseStatus:     run.CurrentPhaseStatus,
		Iteration:       run.CurrentIteration,
		RoadmapPhase:    run.CurrentRoadmapPhase,
		RoadmapTotal:    run.TotalRoadmapPhases,
		ArtifactCount:   len(run.Artifacts),
		HasNeedUserGate: run.PendingNeedUserInputPath != "",
		Setup:           setupDTO(run.Setup),
	}
	if run.PendingReviewPhase != nil {
		dto.PendingReviewPhase = run.PendingReviewPhase.DirName()
	}
	if run.PendingRewindReviewRoadmapPhase != nil {
		dto.PendingRewindReviewRoadmapPhase = *run.PendingRewindReviewRoadmapPhase
	}
	dto.IsRewind = run.IsRewind
	return dto
}

func setupDTO(setup *feature.SetupState) *Setup {
	if setup == nil {
		return nil
	}
	tasks := make(map[string]SetupTask, len(setup.Tasks))
	for key, task := range setup.Tasks {
		tasks[key] = setupTaskDTO(task)
	}
	return &Setup{
		Status:        string(setup.Status),
		Attempt:       setup.Attempt,
		StartedAt:     setup.StartedAt,
		CompletedAt:   setup.CompletedAt,
		LatestLogPath: SafeDisplayText(setup.LatestLogPath, 1000),
		Tasks:         tasks,
		TaskOrder:     append([]string(nil), setup.TaskOrder...),
		LastError:     SafeDisplayText(setup.LastError, 500),
	}
}

func setupTaskDTO(task feature.SetupTask) SetupTask {
	return SetupTask{
		Key:              task.Key,
		Kind:             string(task.Kind),
		Label:            SafeDisplayText(task.Label, 200),
		Repo:             SafeDisplayText(task.Repo, 200),
		Status:           string(task.Status),
		Path:             SafeDisplayText(task.Path, 1000),
		SourcePath:       SafeDisplayText(task.SourcePath, 1000),
		Branch:           SafeDisplayText(task.Branch, 500),
		StartPoint:       SafeDisplayText(task.StartPoint, 500),
		UseCurrentBranch: task.UseCurrentBranch,
		Attempt:          task.Attempt,
		StartedAt:        task.StartedAt,
		EndedAt:          task.EndedAt,
		LastError:        SafeDisplayText(task.LastError, 500),
	}
}

// probeRepoFreshness collects freshness for every repo concurrently. Each
// probe shells out to git and scans the working tree, so serial execution
// costs the sum of per-repo scans on the read path.
func (h *apiHandler) probeRepoFreshness(f *feature.Feature) map[string]RepoFreshness {
	if h.freshness == nil || len(f.Repos) == 0 {
		return nil
	}
	results := make([]RepoFreshness, len(f.Repos))
	var wg sync.WaitGroup
	for i, repo := range f.Repos {
		wg.Add(1)
		go func(i int, repo feature.FeatureRepo) {
			defer wg.Done()
			results[i] = h.freshness.Freshness(f, repo)
		}(i, repo)
	}
	wg.Wait()
	out := make(map[string]RepoFreshness, len(f.Repos))
	for i, repo := range f.Repos {
		out[repo.Name] = results[i]
	}
	return out
}

func (h *apiHandler) repoStatusDTOs(f *feature.Feature) []RepoStatus {
	if f == nil {
		return nil
	}
	freshness := h.probeRepoFreshness(f)
	out := make([]RepoStatus, 0, len(f.Repos))
	for _, repo := range f.Repos {
		state := f.RepoStates[repo.Name]
		dto := RepoStatus{
			Name:        repo.Name,
			Publishable: repo.Publishable == nil || *repo.Publishable,
		}
		if state != nil {
			dto.Touched = state.Touched
			dto.PRURL = state.PRURL
			dto.LastError = SafeDisplayText(state.LastError, 200)
		}
		if freshness != nil {
			dto.Freshness = string(freshness[repo.Name])
		}
		out = append(out, dto)
	}
	return out
}

func timingDTO(f *feature.Feature) Timing {
	byPhase := make(map[string]int64, len(f.PhaseTimings))
	var total int64
	for phase, dur := range f.PhaseTimings {
		seconds := int64(dur.Seconds())
		byPhase[phase] = seconds
		total += seconds
	}
	if f.ActiveTimingKey != "" && f.ActivePhaseStart != nil {
		activeSeconds := int64(f.PhaseRuntime(f.ActiveTimingKey).Seconds())
		total += activeSeconds - byPhase[f.ActiveTimingKey]
		byPhase[f.ActiveTimingKey] = activeSeconds
	}
	return Timing{TotalSeconds: total, ByPhase: byPhase}
}

func costDTO(f *feature.Feature) Cost {
	byPhase := make(map[string]float64, len(f.PhaseCosts))
	var total float64
	for phase, cost := range f.PhaseCosts {
		byPhase[phase] = cost
		total += cost
	}
	return Cost{TotalUSD: total, ByPhase: byPhase}
}

func (h *apiHandler) handleRuntimeConfig(w http.ResponseWriter, r *http.Request) {
	cfg := h.configOrDefault()
	repos := configRepoDTOs(cfg)
	providers := providerNames(h.registry)
	resp := RuntimeConfigResponse{
		APIVersion:      APIVersion,
		Runtime:         h.runtime,
		Defaults:        cfg.Defaults.Models,
		FeatureDefaults: featureDefaultsDTO(cfg.Defaults),
		Repos:           repos,
		WorkspaceRoots:  append([]string(nil), cfg.WorkspaceRoots...),
		Notifications: NotificationConfig{
			MuteFeatureInput: cfg.Notifications.MuteFeatureInput,
		},
		Observability: Observability{
			Events:          cfg.Observability.Events,
			OTelEnabled:     cfg.Observability.OTelEnabled,
			OTelServiceName: cfg.Observability.OTelServiceName,
		},
		Providers: providers,
	}
	revision := revisionForAny(resp)
	resp.Meta = h.responseMeta(revision)
	h.writeRevisionedJSON(w, r, revision, resp)
}

func (h *apiHandler) handleFeatureConfig(w http.ResponseWriter, r *http.Request, featureID string) {
	f, err := h.loadFeature(featureID)
	if err != nil {
		writeStoreError(w, err, featureID)
		return
	}
	cfg := h.configOrDefault()
	defaults := FeatureConfig{
		Models:             cfg.Defaults.Models,
		Inquireness:        FeatureConfigInquireness(cfg.Defaults.Inquireness),
		Checkpoints:        checkpointsDTO(featureCheckpoints(cfg.Defaults.Checkpoints)),
		Pipeline:           cfg.Defaults.Pipeline,
		InputNotifications: FeatureConfigInputNotifications(feature.InputNotificationsModeForMuted(cfg.Notifications.MuteFeatureInput)),
	}
	current := featureConfigDTO(f)
	resp := FeatureConfigResponse{
		APIVersion: APIVersion,
		FeatureID:  f.ID,
		Current:    current,
		Defaults:   defaults,
		Original:   current,
		Publish: Publishability{
			ManualPublish: f.Checkpoints.ManualPublish,
			Repos:         publishabilityByRepo(f),
		},
	}
	revision := revisionForAny(resp)
	resp.Meta = h.responseMeta(revision)
	h.writeRevisionedJSON(w, r, revision, resp)
}

func (h *apiHandler) handleModelCatalog(w http.ResponseWriter, r *http.Request) {
	resp := h.modelCatalogSnapshot()
	revision := revisionForAny(resp)
	resp.Meta = h.responseMeta(revision)
	h.writeRevisionedJSON(w, r, revision, resp)
}

func (h *apiHandler) modelCatalogSnapshot() ModelCatalogResponse {
	resp := ModelCatalogResponse{
		APIVersion:          APIVersion,
		ProviderModels:      map[string][]Model{},
		PhaseProviderModels: map[string]map[string][]string{},
		PhaseDefaults:       h.configOrDefault().Defaults.Models,
	}
	if h.registry != nil {
		defaults := h.registry.CatalogDefaultModels()
		if defaults != (config.ModelConfig{}) {
			resp.PhaseDefaults = defaults
		}
		for _, provider := range h.registry.DetectedProviders() {
			name := provider.Name()
			resp.ProviderOrder = append(resp.ProviderOrder, name)
			for _, model := range h.registry.ModelsForProvider(name) {
				resp.ProviderModels[name] = append(resp.ProviderModels[name], modelDTO(model))
			}
			if len(resp.ProviderModels[name]) == 0 {
				for _, id := range provider.AvailableModels() {
					resp.ProviderModels[name] = append(resp.ProviderModels[name], Model{ID: id})
				}
			}
		}
		for _, role := range []llm.PhaseRole{llm.PhaseInquiry, llm.PhaseResearch, llm.PhasePlanning, llm.PhaseImplementation, llm.PhaseReview, llm.PhaseChat, llm.PhaseKBBuild, llm.PhaseAutomaticReview} {
			resp.PhaseProviderModels[string(role)] = h.registry.EligibleModelsForPhase(role)
		}
	}
	return resp
}

func (h *apiHandler) handleProviderModelRefreshRoute(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if !h.requireTrustedMutation(w, r) {
		return
	}
	var req ProviderModelRefreshRequest
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	if len(req.Provider) > 100 || !validEntityID(req.Provider) {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid provider name", nil)
		return
	}
	if h.registry == nil || h.registry.ByName(req.Provider) == nil {
		writeAPIError(w, http.StatusNotFound, "provider_not_found", "provider is not registered", nil)
		return
	}

	h.providerRefreshMu.Lock()
	defer h.providerRefreshMu.Unlock()

	// Ensure a complete cached snapshot exists before replacing one entry.
	h.providerReadinessStatuses(r.Context(), false)
	status, _ := h.refreshProviderReadiness(r.Context(), req.Provider)
	if status.Ready {
		provider := h.registry.ByName(req.Provider)
		discoverer, canDiscover := provider.(llm.CatalogDiscoverer)
		enricher, canEnrich := provider.(llm.CatalogEnricher)
		if !canDiscover || !canEnrich {
			writeAPIError(w, http.StatusConflict, "provider_model_refresh_unsupported", "provider does not support model refresh", nil)
			return
		}
		discoveryCtx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		models, err := discoverer.DiscoverModelCatalog(discoveryCtx)
		cancel()
		if err != nil {
			writeAPIError(w, http.StatusBadGateway, "provider_model_refresh_failed", "provider model refresh failed", nil)
			return
		}
		if len(models) == 0 {
			writeAPIError(w, http.StatusBadGateway, "provider_model_refresh_failed", "provider returned no models", nil)
			return
		}
		if h.persistProviderModels != nil {
			if err := h.persistProviderModels(provider, models); err != nil {
				writeAPIError(w, http.StatusBadGateway, "provider_model_refresh_failed", "provider model catalog could not be persisted", nil)
				return
			}
		}
		enricher.SetModelCatalog(models)
	}

	readiness := h.readinessSnapshot(r.Context(), false)
	readinessRevision := revisionForAny(readiness)
	readiness.Meta = h.responseMeta(readinessRevision)
	catalog := h.modelCatalogSnapshot()
	catalogRevision := revisionForAny(catalog)
	catalog.Meta = h.responseMeta(catalogRevision)
	resp := ProviderModelRefreshResponse{
		APIVersion: APIVersion,
		Readiness:  readiness,
		Catalog:    catalog,
	}
	revision := revisionForAny(resp)
	resp.Meta = h.responseMeta(revision)
	writeJSON(w, http.StatusOK, resp)
}

func (h *apiHandler) handlePrompts(w http.ResponseWriter, r *http.Request) {
	asks, perms := h.pendingControls()
	_ = perms
	help, gates, err := h.featureQueues()
	if err != nil {
		writeAPIError(
			w,
			http.StatusInternalServerError,
			promptSnapshotTooLargeCode,
			errNeedUserInputGateCollectionTooLarge.Error(),
			nil,
		)
		return
	}
	resp := PromptSnapshotResponse{
		APIVersion:       APIVersion,
		AskUserQuestions: asks,
		HelpQueue:        help,
		NeedUserInputs:   gates,
	}
	revision := revisionForAny(resp)
	resp.Meta = h.responseMeta(revision)
	h.writeRevisionedJSON(w, r, revision, resp)
}

func (h *apiHandler) handlePermissions(w http.ResponseWriter, r *http.Request) {
	_, perms := h.pendingControls()
	resp := PermissionSnapshotResponse{APIVersion: APIVersion, Requests: perms}
	revision := revisionForAny(resp)
	resp.Meta = h.responseMeta(revision)
	h.writeRevisionedJSON(w, r, revision, resp)
}

func (h *apiHandler) configOrDefault() *config.Config {
	if h.cfg != nil {
		return h.cfg
	}
	return config.NewDefault()
}

func configRepoDTOs(cfg *config.Config) []ConfigRepo {
	cfg = runtimeConfigRepoSnapshot(cfg)
	allRepos := config.AllRepos(cfg)
	repos := make([]ConfigRepo, 0, len(allRepos))
	for name, repo := range allRepos {
		repos = append(repos, ConfigRepo{Name: name, Path: repo.Path, PipelineGates: copyConfigPipelineGates(repo.PipelineGates)})
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })
	return repos
}

func runtimeConfigRepoSnapshot(cfg *config.Config) *config.Config {
	if cfg == nil {
		return config.NewDefault()
	}
	snapshot := *cfg
	snapshot.WorkspaceRoots = append([]string(nil), cfg.WorkspaceRoots...)
	snapshot.Repos = copyConfigRepoMap(cfg.Repos)
	snapshot.DiscoveredRepos = copyConfigRepoMap(cfg.DiscoveredRepos)
	if len(snapshot.WorkspaceRoots) > 0 {
		config.DiscoverReposFromRoots(&snapshot)
	}
	return &snapshot
}

func copyConfigRepoMap(in map[string]config.RepoConfig) map[string]config.RepoConfig {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]config.RepoConfig, len(in))
	for name, repo := range in {
		out[name] = repo
	}
	return out
}

func copyConfigPipelineGates(in map[string]config.Checkpoints) map[string]config.Checkpoints {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]config.Checkpoints, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func featureDefaultsDTO(defaults config.DefaultsConfig) FeatureDefaults {
	var prefs map[string]config.PipelinePreference
	if len(defaults.PipelinePreferences) > 0 {
		prefs = make(map[string]config.PipelinePreference, len(defaults.PipelinePreferences))
		for key, value := range defaults.PipelinePreferences {
			prefs[key] = value
		}
	}
	return FeatureDefaults{
		Models:                 defaults.Models,
		Effort:                 defaults.Effort,
		PipelinePreferences:    prefs,
		Inquireness:            defaults.Inquireness,
		Pipeline:               defaults.Pipeline,
		Checkpoints:            defaults.Checkpoints,
		AutomaticReviewEnabled: defaults.AutomaticReviewEnabled,
	}
}

func providerNames(reg *llm.Registry) []string {
	if reg == nil {
		return nil
	}
	providers := reg.DetectedProviders()
	out := make([]string, 0, len(providers))
	for _, provider := range providers {
		out = append(out, provider.Name())
	}
	return out
}

func featureCheckpoints(c config.Checkpoints) feature.Checkpoints {
	return feature.Checkpoints{
		InquiryReview:   c.InquiryReview,
		ResearchReview:  c.ResearchReview,
		DesignReview:    c.DesignReview,
		RoadmapReview:   c.RoadmapReview,
		PhasePlanReview: c.PhasePlanReview,
		ManualPublish:   c.ManualPublish,
		DraftPublish:    c.DraftPublish,
	}
}

func publishabilityByRepo(f *feature.Feature) map[string]bool {
	out := make(map[string]bool, len(f.Repos))
	for _, repo := range f.Repos {
		out[repo.Name] = repo.Publishable == nil || *repo.Publishable
	}
	return out
}

func modelDTO(model llm.ModelInfo) Model {
	caps := make([]string, 0, len(model.EffortCapabilities))
	for _, cap := range model.EffortCapabilities {
		caps = append(caps, string(cap))
	}
	return Model{
		ID:                 model.ID,
		DisplayName:        model.DisplayName,
		ContextWindow:      model.ContextWindow,
		Aliases:            append([]string(nil), model.Aliases...),
		Category:           model.Category,
		EffortCapabilities: caps,
	}
}

func (h *apiHandler) pendingControls() ([]ControlRequest, []ControlRequest) {
	if h.sessions == nil {
		return nil, nil
	}
	var asks []orderedControlRequest
	var perms []orderedControlRequest
	for _, sess := range h.sessions.ActiveSessions() {
		for i, req := range sess.PendingControlRequests() {
			dto := controlRequestDTO(sess, req)
			entry := orderedControlRequest{
				dto:       dto,
				startedAt: sess.StartedAt(),
				sessionID: sess.ID(),
				index:     i,
			}
			if req.Request.ToolName == toolNameAskUserQuestion {
				asks = append(asks, entry)
			} else {
				perms = append(perms, entry)
			}
		}
	}
	sortOrderedControlRequests(asks)
	sortOrderedControlRequests(perms)
	dto := func(w orderedControlRequest) ControlRequest { return w.dto }
	return dtosOf(asks, dto), dtosOf(perms, dto)
}

func (h *apiHandler) featureQueues() ([]HelpQueue, []NeedUserInputGate, error) {
	features, _, _ := listFeatures(h.features)
	var help []orderedHelpQueue
	var gates []orderedNeedInputGate
	for _, f := range features {
		for i, req := range f.HelpQueue {
			if !req.Pending {
				continue
			}
			help = append(help, orderedHelpQueue{
				dto: HelpQueue{
					FeatureID: f.ID,
					Question:  SafeDisplayText(req.Question, 300),
					Kind:      helpKindQuestion,
					Pending:   req.Pending,
					Time:      req.Time,
				},
				featureID: f.ID,
				index:     i,
			})
		}
		if f.Status == feature.StatusNeedUserInput && f.PendingNeedUserInputPath != "" {
			gates = append(gates, orderedNeedInputGate{
				dto:       needUserInputGateDTO(f.ID, entityFeature, "", f.CurrentIteration, f.InputNotifications, f.PendingNeedUserInputPath),
				featureID: f.ID,
				created:   f.Created,
				gateTime:  gateFileTime(f.PendingNeedUserInputPath),
			})
		}
	}
	if h.sessions != nil {
		for _, sess := range h.sessions.ActiveSessions() {
			if sess == nil || sess.Status() != ports.SessionWaitingHelp || sessionHasPendingAskUserControl(sess) {
				continue
			}
			// A chat session waiting between turns is waiting on the user —
			// the inbox is its only reply surface. A phase session in the same
			// state is mid-coordination and needs no human.
			kind := helpKindCoordinating
			if sess.Kind() == ports.KindChat {
				kind = helpKindInput
			}
			help = append(help, orderedHelpQueue{
				dto: HelpQueue{
					FeatureID: sess.FeatureID(),
					Question:  agentQuestionPrompt,
					Kind:      kind,
					Pending:   true,
					Time:      sess.WaitingSince(),
				},
				featureID: sess.FeatureID(),
				index:     len(help),
			})
		}
	}
	sortOrderedHelpQueue(help)
	sortOrderedNeedInputGates(gates)
	if err := boundNeedUserInputGateCollection(gates); err != nil {
		return nil, nil, err
	}
	return dtosOf(help, func(w orderedHelpQueue) HelpQueue { return w.dto }),
		dtosOf(gates, func(w orderedNeedInputGate) NeedUserInputGate { return w.dto }), nil
}

func sessionHasPendingAskUserControl(sess ports.SessionView) bool {
	if sess == nil {
		return false
	}
	for _, req := range sess.PendingControlRequests() {
		if req != nil && req.Request.ToolName == toolNameAskUserQuestion {
			return true
		}
	}
	return false
}

func needUserInputGateDTO(featureID, scope, repoName string, iteration int, inputNotifications feature.InputNotificationsMode, gatePath string) NeedUserInputGate {
	dto := NeedUserInputGate{
		FeatureID:          featureID,
		Open:               true,
		Scope:              scope,
		RepoName:           repoName,
		InputNotifications: string(feature.NormalizeInputNotificationsMode(inputNotifications)),
		Iteration:          iteration,
		WaitingSince:       gateFileTime(gatePath),
	}
	rec, err := agent.ReadNeedUserInputRecord(gatePath)
	if err != nil {
		return dto
	}
	if dto.Iteration == 0 {
		dto.Iteration = rec.Iteration
	}
	if !rec.WaitingSince.IsZero() {
		dto.WaitingSince = rec.WaitingSince
	}
	dto.Summary = agent.BoundNeedUserInputVerificationString(
		strings.TrimSpace(rec.Summary),
		agent.NeedUserInputVerificationContextTextMaxLength,
	)
	dto.Questions = make(
		[]NeedUserInputQuestion,
		0,
		min(len(rec.Questions), agent.NeedUserInputGateMaxQuestions),
	)
	for _, q := range rec.Questions {
		if len(dto.Questions) == agent.NeedUserInputGateMaxQuestions {
			break
		}
		prompt := strings.TrimSpace(q.Prompt)
		if prompt == "" {
			continue
		}
		questionIndex := q.Index
		if questionIndex <= 0 {
			questionIndex = len(dto.Questions) + 1
		}
		dto.Questions = append(dto.Questions, NeedUserInputQuestion{
			Index: questionIndex,
			Prompt: agent.BoundNeedUserInputVerificationString(
				prompt,
				agent.NeedUserInputVerificationContextTextMaxLength,
			),
			Answer: agent.BoundNeedUserInputVerificationString(
				strings.TrimSpace(q.Answer),
				agent.NeedUserInputVerificationContextTextMaxLength,
			),
		})
	}
	if rec.Verification != nil && rec.VerificationDecision != nil && len(rec.Verification.Blockers) > 0 {
		verification := NeedUserInputVerification{
			Blockers: make(
				[]NeedUserInputVerificationBlocker,
				0,
				min(len(rec.Verification.Blockers), agent.NeedUserInputVerificationMaxBlockers),
			),
			AllowedActions: []NeedUserInputVerificationAction{},
		}
		for _, blocker := range rec.Verification.Blockers {
			if len(verification.Blockers) == agent.NeedUserInputVerificationMaxBlockers {
				break
			}
			itemID := strings.TrimSpace(blocker.ItemID)
			if itemID == "" {
				continue
			}
			capabilities := make(
				[]string,
				0,
				min(
					len(blocker.Capabilities),
					agent.NeedUserInputVerificationMaxCapabilities,
				),
			)
			for _, capability := range blocker.Capabilities {
				if capability = strings.TrimSpace(capability); capability != "" {
					capabilities = append(
						capabilities,
						agent.BoundNeedUserInputVerificationString(
							capability,
							agent.NeedUserInputVerificationContextTextMaxLength,
						),
					)
					if len(capabilities) == agent.NeedUserInputVerificationMaxCapabilities {
						break
					}
				}
			}
			verification.Blockers = append(verification.Blockers, NeedUserInputVerificationBlocker{
				ItemID: agent.BoundNeedUserInputVerificationString(
					itemID,
					agent.NeedUserInputVerificationItemIDMaxLength,
				),
				Name: agent.BoundNeedUserInputVerificationString(
					strings.TrimSpace(blocker.Name),
					agent.NeedUserInputVerificationContextTextMaxLength,
				),
				RepoName: agent.BoundNeedUserInputVerificationString(
					strings.TrimSpace(blocker.RepoName),
					agent.NeedUserInputVerificationRepoNameMaxLength,
				),
				Command: agent.BoundNeedUserInputVerificationString(
					strings.TrimSpace(blocker.Command),
					agent.NeedUserInputVerificationContextTextMaxLength,
				),
				Reason: agent.BoundNeedUserInputVerificationString(
					strings.TrimSpace(blocker.Reason),
					agent.NeedUserInputVerificationContextTextMaxLength,
				),
				Capabilities: capabilities,
				Remediation: agent.BoundNeedUserInputVerificationString(
					strings.TrimSpace(blocker.Remediation),
					agent.NeedUserInputVerificationContextTextMaxLength,
				),
			})
		}
		seenActions := make(map[NeedUserInputVerificationAction]struct{}, 2)
		for _, action := range rec.VerificationDecision.AllowedActions {
			normalized := strings.ToUpper(strings.TrimSpace(action))
			switch normalized {
			case agent.NeedUserVerificationWaive, agent.NeedUserVerificationRetryAfterAuth:
				candidate := NeedUserInputVerificationAction(normalized)
				if _, exists := seenActions[candidate]; !exists {
					seenActions[candidate] = struct{}{}
					verification.AllowedActions = append(verification.AllowedActions, candidate)
				}
			}
		}
		dto.Verification = &verification
	}
	boundNeedUserInputGateDisplay(&dto)
	return dto
}

// A JSON string can expand to six bytes per UTF-16 code unit when control
// characters are escaped. Reserve three quarters of the gate budget for all
// questionnaire fields so every projected question remains answerable.
const needUserInputQuestionJSONExpansion = 6

func boundNeedUserInputGateDisplay(dto *NeedUserInputGate) {
	if len(dto.Questions) > 0 {
		questionTextLimit := (agent.NeedUserInputGateDisplayMaxBytes * 3 / 4) /
			(len(dto.Questions) * 2 * needUserInputQuestionJSONExpansion)
		questionTextLimit = max(1, min(
			questionTextLimit,
			agent.NeedUserInputVerificationContextTextMaxLength,
		))
		for i := range dto.Questions {
			dto.Questions[i].Prompt = agent.BoundNeedUserInputVerificationString(
				dto.Questions[i].Prompt,
				questionTextLimit,
			)
			dto.Questions[i].Answer = agent.BoundNeedUserInputVerificationString(
				dto.Questions[i].Answer,
				questionTextLimit,
			)
		}
	}
	if needUserInputGateFitsDisplayBudget(*dto) {
		return
	}
	if dto.Verification != nil {
		for i := range dto.Verification.Blockers {
			dto.Verification.Blockers[i].Capabilities = []string{}
		}
		if needUserInputGateFitsDisplayBudget(*dto) {
			return
		}
	}
	dto.Summary = ""
	if needUserInputGateFitsDisplayBudget(*dto) {
		return
	}
	if dto.Verification == nil {
		return
	}

	blockers := dto.Verification.Blockers
	low, high, best := 1, len(blockers), 0
	for low <= high {
		mid := low + (high-low)/2
		dto.Verification.Blockers = blockers[:mid]
		if needUserInputGateFitsDisplayBudget(*dto) {
			best = mid
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	if best > 0 {
		dto.Verification.Blockers = blockers[:best]
		return
	}
	dto.Verification = nil
}

func needUserInputGateFitsDisplayBudget(dto NeedUserInputGate) bool {
	data, err := json.Marshal(dto)
	return err == nil && len(data) <= agent.NeedUserInputGateDisplayMaxBytes
}

func boundNeedUserInputGateCollection(gates []orderedNeedInputGate) error {
	questionCount := 0
	for i := range gates {
		questionCount += len(gates[i].dto.Questions)
	}
	if questionCount > 0 {
		questionTextLimit := (agent.NeedUserInputGateCollectionMaxBytes * 3 / 4) /
			(questionCount * 2 * needUserInputQuestionJSONExpansion)
		questionTextLimit = max(1, min(
			questionTextLimit,
			agent.NeedUserInputVerificationContextTextMaxLength,
		))
		for i := range gates {
			for question := range gates[i].dto.Questions {
				gates[i].dto.Questions[question].Prompt =
					agent.BoundNeedUserInputVerificationString(
						gates[i].dto.Questions[question].Prompt,
						questionTextLimit,
					)
				gates[i].dto.Questions[question].Answer =
					agent.BoundNeedUserInputVerificationString(
						gates[i].dto.Questions[question].Answer,
						questionTextLimit,
					)
			}
		}
	}
	if needUserInputGateCollectionFitsBudget(gates) {
		return nil
	}
	for i := range gates {
		if gates[i].dto.Verification == nil {
			continue
		}
		for blocker := range gates[i].dto.Verification.Blockers {
			gates[i].dto.Verification.Blockers[blocker].Capabilities = []string{}
		}
	}
	if needUserInputGateCollectionFitsBudget(gates) {
		return nil
	}
	for i := range gates {
		gates[i].dto.Summary = ""
	}
	if needUserInputGateCollectionFitsBudget(gates) {
		return nil
	}
	for i := len(gates) - 1; i >= 0; i-- {
		if gates[i].dto.Verification == nil {
			continue
		}
		gates[i].dto.Verification = nil
		if needUserInputGateCollectionFitsBudget(gates) {
			return nil
		}
	}
	return errNeedUserInputGateCollectionTooLarge
}

func needUserInputGateCollectionFitsBudget(gates []orderedNeedInputGate) bool {
	dtos := make([]NeedUserInputGate, len(gates))
	for i := range gates {
		dtos[i] = gates[i].dto
	}
	data, err := json.Marshal(dtos)
	return err == nil && len(data) <= agent.NeedUserInputGateCollectionMaxBytes
}

type orderedControlRequest struct {
	dto       ControlRequest
	startedAt time.Time
	sessionID string
	index     int
}

func sortOrderedControlRequests(items []orderedControlRequest) {
	sort.SliceStable(items, func(i, j int) bool {
		if beforeByKnownTime(items[i].startedAt, items[j].startedAt) {
			return true
		}
		if beforeByKnownTime(items[j].startedAt, items[i].startedAt) {
			return false
		}
		if items[i].sessionID != items[j].sessionID {
			return items[i].sessionID < items[j].sessionID
		}
		if items[i].index != items[j].index {
			return items[i].index < items[j].index
		}
		return items[i].dto.RequestID < items[j].dto.RequestID
	})
}

type orderedHelpQueue struct {
	dto       HelpQueue
	featureID string
	index     int
}

func sortOrderedHelpQueue(items []orderedHelpQueue) {
	sort.SliceStable(items, func(i, j int) bool {
		if beforeByKnownTime(items[i].dto.Time, items[j].dto.Time) {
			return true
		}
		if beforeByKnownTime(items[j].dto.Time, items[i].dto.Time) {
			return false
		}
		if items[i].featureID != items[j].featureID {
			return items[i].featureID < items[j].featureID
		}
		if items[i].index != items[j].index {
			return items[i].index < items[j].index
		}
		return items[i].dto.Question < items[j].dto.Question
	})
}

type orderedNeedInputGate struct {
	dto       NeedUserInputGate
	featureID string
	created   time.Time
	gateTime  time.Time
}

func sortOrderedNeedInputGates(items []orderedNeedInputGate) {
	sort.SliceStable(items, func(i, j int) bool {
		if beforeByKnownTime(items[i].gateTime, items[j].gateTime) {
			return true
		}
		if beforeByKnownTime(items[j].gateTime, items[i].gateTime) {
			return false
		}
		if beforeByKnownTime(items[i].created, items[j].created) {
			return true
		}
		if beforeByKnownTime(items[j].created, items[i].created) {
			return false
		}
		if items[i].featureID != items[j].featureID {
			return items[i].featureID < items[j].featureID
		}
		return items[i].dto.Iteration < items[j].dto.Iteration
	})
}

func dtosOf[W any, D any](items []W, get func(W) D) []D {
	out := make([]D, 0, len(items))
	for _, item := range items {
		out = append(out, get(item))
	}
	return out
}

func gateFileTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime().UTC()
}

func beforeByKnownTime(a, b time.Time) bool {
	if a.IsZero() {
		return false
	}
	if b.IsZero() {
		return true
	}
	return a.Before(b)
}

func featureConfigDTO(f *feature.Feature) FeatureConfig {
	pipeline := f.Pipeline
	return FeatureConfig{
		Models:              f.Models,
		Effort:              f.Effort,
		Inquireness:         FeatureConfigInquireness(f.Inquireness),
		Checkpoints:         checkpointsDTO(pipeline.NormalizeCheckpoints(f.Checkpoints, f.IsPublishable())),
		Pipeline:            string(pipeline),
		InputNotifications:  FeatureConfigInputNotifications(feature.NormalizeInputNotificationsMode(f.InputNotifications)),
		AutomaticReviewMode: FeatureConfigAutomaticReviewMode(feature.NormalizeAutomaticReviewMode(f.AutomaticReviewMode)),
	}
}

func checkpointsDTO(c feature.Checkpoints) Checkpoints {
	return Checkpoints{
		InquiryReview:   c.InquiryReview,
		ResearchReview:  c.ResearchReview,
		DesignReview:    c.DesignReview,
		RoadmapReview:   c.RoadmapReview,
		PhasePlanReview: c.PhasePlanReview,
		ManualPublish:   c.ManualPublish,
		DraftPublish:    c.DraftPublish,
	}
}

func controlRequestDTO(sess ports.SessionView, req *llm.ControlRequestMessage) ControlRequest {
	if req == nil {
		return ControlRequest{}
	}
	dto := ControlRequest{
		RequestID:    req.RequestID,
		SessionID:    sess.ID(),
		FeatureID:    sess.FeatureID(),
		Phase:        sess.Phase().String(),
		ToolName:     req.Request.ToolName,
		Status:       controlRequestStatusPending,
		WaitingSince: req.WaitingSince,
		Summary:      safeControlSummary(req),
	}
	if req.Request.ToolName == toolNameAskUserQuestion {
		dto.Questions = safeAskUserQuestions(sess, req)
	} else {
		dto.Input = safeControlInput(req)
		scope := sess.PermCacheScope()
		dto.Remember = &PermissionRememberPreview{
			Pattern:      safeRememberPattern(req),
			Scope:        scope,
			ScopeDisplay: permissionScopeDisplay(scope),
		}
	}
	return dto
}

func safeRememberPattern(req *llm.ControlRequestMessage) string {
	if req == nil {
		return ""
	}
	// The snapshot exposes a client-submitted default policy preview, so infer
	// from sanitized input when possible rather than leaking raw tool arguments.
	if safeInput := safeControlInput(req); safeInput != nil {
		if data, err := json.Marshal(safeInput); err == nil {
			return permission.InferBashPattern(req.Request.ToolName, string(data))
		}
	}
	return permission.InferBashPattern(req.Request.ToolName, safeRememberFallbackInput(req.Request.ToolName))
}

func safeRememberFallbackInput(toolName string) string {
	if toolName == toolNameBash {
		return ""
	}
	return "*"
}

func permissionScopeDisplay(scope string) string {
	if scope == "" {
		return "global"
	}
	return "repo: " + scope
}

func safeControlSummary(req *llm.ControlRequestMessage) string {
	if req == nil {
		return ""
	}
	if req.Request.ToolName == toolNameAskUserQuestion {
		var envelope struct {
			Questions []struct {
				Question string `json:"question"`
				Header   string `json:"header"`
			} `json:"questions"`
		}
		if json.Unmarshal(req.Request.Input, &envelope) == nil && len(envelope.Questions) > 0 {
			q := envelope.Questions[0].Question
			if q == "" {
				q = envelope.Questions[0].Header
			}
			return SafeDisplayText(q, 180)
		}
		return "user input requested"
	}
	if detail := safeControlInputDetail(req.Request.ToolName, req.Request.Input); detail != "" {
		return SafeDisplayText(detail, 180)
	}
	return SafeDisplayText(req.Request.ToolName+" requested", 180)
}

func safeControlInput(req *llm.ControlRequestMessage) map[string]any {
	if req == nil || len(req.Request.Input) == 0 {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(req.Request.Input, &parsed); err != nil {
		return nil
	}
	return safeControlInputValue(parsed).(map[string]any)
}

func safeControlInputValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = safeControlInputValue(item)
		}
		return out
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, safeControlInputValue(item))
		}
		return out
	case string:
		return SafeDisplayText(v, 2000)
	default:
		return v
	}
}

func safeControlInputDetail(toolName string, input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var fields map[string]any
	if err := json.Unmarshal(input, &fields); err != nil {
		return ""
	}
	switch toolName {
	case toolNameBash:
		return safeStringField(fields, "command")
	case toolNameWrite, toolNameEdit:
		return firstSafeStringField(fields, "file_path", "path")
	default:
		return firstSafeStringField(fields, "description", "summary", "title", "command", "file_path", "path")
	}
}

func firstSafeStringField(fields map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := safeStringField(fields, key); value != "" {
			return value
		}
	}
	return ""
}

func safeStringField(fields map[string]any, key string) string {
	value, ok := fields[key].(string)
	if !ok {
		return ""
	}
	return SafeDisplayText(value, 2000)
}

const (
	askUserQuestionDisplayLimit          = 4000
	askUserHeaderDisplayLimit            = 1000
	askUserOptionLabelDisplayLimit       = 1000
	askUserOptionDescriptionDisplayLimit = 4000
)

func safeAskUserQuestions(sess ports.SessionView, req *llm.ControlRequestMessage) []AskUserQuestion {
	if req == nil || req.Request.ToolName != toolNameAskUserQuestion {
		return nil
	}
	questions := safeAskUserQuestionsFromInput(req.Request.Input)
	if !askUserQuestionDTOsNeedConfidence(questions) || sess == nil || sess.MessageLog() == nil {
		return questions
	}
	return enrichAskUserQuestionDTOConfidence(questions, sess.MessageLog())
}

func safeAskUserQuestionsFromInput(input json.RawMessage) []AskUserQuestion {
	if len(input) == 0 {
		return nil
	}
	var envelope struct {
		Questions []struct {
			Question         string `json:"question"`
			Header           string `json:"header"`
			MultiSelect      bool   `json:"multiSelect"`
			MultiSelectSnake bool   `json:"multi_select"`
			Options          []struct {
				Label       string   `json:"label"`
				Description string   `json:"description"`
				Confidence  *float64 `json:"confidence"`
			} `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(input, &envelope); err != nil || len(envelope.Questions) == 0 {
		return nil
	}
	questions := make([]AskUserQuestion, 0, len(envelope.Questions))
	for _, rawQuestion := range envelope.Questions {
		question := AskUserQuestion{
			Question:    SafeDisplayText(rawQuestion.Question, askUserQuestionDisplayLimit),
			Header:      SafeDisplayText(rawQuestion.Header, askUserHeaderDisplayLimit),
			MultiSelect: rawQuestion.MultiSelect || rawQuestion.MultiSelectSnake,
		}
		for _, rawOption := range rawQuestion.Options {
			option := AskUserOption{
				Label:       SafeDisplayText(rawOption.Label, askUserOptionLabelDisplayLimit),
				Description: SafeDisplayText(rawOption.Description, askUserOptionDescriptionDisplayLimit),
				Confidence:  rawOption.Confidence,
			}
			if option.Label == "" && option.Description == "" && option.Confidence == nil {
				continue
			}
			question.Options = append(question.Options, option)
		}
		if question.Question == "" && question.Header == "" && len(question.Options) == 0 {
			continue
		}
		questions = append(questions, question)
	}
	return questions
}

func askUserQuestionDTOsNeedConfidence(questions []AskUserQuestion) bool {
	for _, q := range questions {
		for _, opt := range q.Options {
			if opt.Confidence == nil {
				return true
			}
		}
	}
	return false
}

func enrichAskUserQuestionDTOConfidence(questions []AskUserQuestion, log ports.MessageLog) []AskUserQuestion {
	if log == nil {
		return questions
	}
	blocks := log.ToolUseBlocks()
	for i := len(blocks) - 1; i >= 0; i-- {
		block := blocks[i]
		if block.Name != toolNameAskUserQuestion || len(block.Input) == 0 {
			continue
		}
		source := safeAskUserQuestionsFromInput(block.Input)
		if !askUserQuestionDTOBundlesMatch(questions, source) {
			continue
		}
		return copyAskUserQuestionDTOConfidence(questions, source)
	}
	return questions
}

func askUserQuestionDTOBundlesMatch(a, b []AskUserQuestion) bool {
	if len(a) == 0 || len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.TrimSpace(a[i].Question) != strings.TrimSpace(b[i].Question) ||
			strings.TrimSpace(a[i].Header) != strings.TrimSpace(b[i].Header) ||
			a[i].MultiSelect != b[i].MultiSelect ||
			len(a[i].Options) != len(b[i].Options) {
			return false
		}
		for j := range a[i].Options {
			if strings.TrimSpace(a[i].Options[j].Label) != strings.TrimSpace(b[i].Options[j].Label) ||
				strings.TrimSpace(a[i].Options[j].Description) != strings.TrimSpace(b[i].Options[j].Description) {
				return false
			}
		}
	}
	return true
}

func copyAskUserQuestionDTOConfidence(questions, source []AskUserQuestion) []AskUserQuestion {
	enriched := make([]AskUserQuestion, len(questions))
	for i := range questions {
		enriched[i] = questions[i]
		enriched[i].Options = append([]AskUserOption(nil), questions[i].Options...)
		for j := range enriched[i].Options {
			if enriched[i].Options[j].Confidence == nil {
				enriched[i].Options[j].Confidence = source[i].Options[j].Confidence
			}
		}
	}
	return enriched
}

func SafeDisplayText(s string, limit int) string {
	s = strings.ReplaceAll(s, "private-token", "[redacted]")
	s = strings.ReplaceAll(s, "raw initial prompt", "[redacted prompt]")
	s = strings.TrimSpace(s)
	if limit > 0 && len(s) > limit {
		return s[:limit] + "..."
	}
	return s
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
