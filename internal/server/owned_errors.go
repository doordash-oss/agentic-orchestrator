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
	"sort"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// ownedErrorScopeRank orders the projected scopes deterministically within
// one severity class: the feature's own run failure first, then its setup
// task, its repositories, and finally the active child's records. Recovery
// never appears on the projection (the Recovery workspace already renders
// catalog titles) but keeps a rank so the map is total.
var ownedErrorScopeRank = map[ErrorScope]int{
	errorScopeRun:         0,
	errorScopeSetup:       1,
	errorScopeRepository:  2,
	errorScopeTransaction: 3,
	errorScopeRecovery:    4,
}

// ownedErrorsDTO projects a feature's current non-warning errors onto the
// summary's owned-error list: the feature's run failure record (or, when its
// context names a setup task, that task's record as a single setup entry),
// one entry per repository publish record, and — for a parent — the active
// child's integration attention record and run failure record keyed by the
// child id. A child feature's own detail projects the same child-keyed homes
// against itself. Every entry renders through the catalog without
// diagnostics, carries the reference to its durable home, and is ordered
// blocking first, then needs_action, stable by scope and key. Closed
// children and warning-class records never appear; nil means the field is
// omitted from the wire.
func ownedErrorsDTO(f *feature.Feature, activeChild *feature.Feature) []OwnedError {
	if f == nil {
		return nil
	}
	entries := make([]OwnedError, 0, 4)
	entries = appendOwnedRunFailure(entries, f)
	entries = appendOwnedRepoErrors(entries, f)
	if activeChild != nil {
		entries = appendOwnedAttention(entries, activeChild)
		entries = appendOwnedRunFailure(entries, activeChild)
	} else if f.IsChild() {
		entries = appendOwnedAttention(entries, f)
	}
	if len(entries) == 0 {
		return nil
	}
	sort.Slice(entries, func(i, j int) bool {
		left, right := entries[i], entries[j]
		leftBlocking := left.Error.Class == ErrorClassBlocking
		rightBlocking := right.Error.Class == ErrorClassBlocking
		if leftBlocking != rightBlocking {
			return leftBlocking
		}
		if left.Ref.Scope != right.Ref.Scope {
			return ownedErrorScopeRank[left.Ref.Scope] < ownedErrorScopeRank[right.Ref.Scope]
		}
		return ownedErrorSortKey(left.Ref) < ownedErrorSortKey(right.Ref)
	})
	return entries
}

// ownedErrorSortKey builds the stable secondary key within one scope: the
// owning feature id, then the scope's own key (repository name or task key).
func ownedErrorSortKey(ref ErrorReference) string {
	key := ref.FeatureID
	if ref.Repository != "" {
		key += "\x00" + ref.Repository
	}
	if ref.TaskKey != "" {
		key += "\x00" + ref.TaskKey
	}
	return key
}

// appendOwnedRunFailure appends the feature's run failure record as a run
// entry keyed by its feature id — except when the record's context names a
// setup task, in which case that task's own record becomes the single setup
// entry carrying the task key. A named task without a stored record falls
// back to the run record so exactly one entry is projected either way.
func appendOwnedRunFailure(entries []OwnedError, f *feature.Feature) []OwnedError {
	rec := f.FailureRecord()
	if rec == nil {
		return entries
	}
	if task := f.FailedSetupTask(); task != nil && task.Error != nil {
		return appendOwnedError(entries, ErrorReference{
			Scope:     errorScopeSetup,
			Code:      string(task.Error.Code),
			FeatureID: f.ID,
			TaskKey:   task.Key,
		}, *task.Error)
	}
	return appendOwnedError(entries, ErrorReference{
		Scope:     errorScopeRun,
		Code:      string(rec.Code),
		FeatureID: f.ID,
	}, *rec)
}

// appendOwnedRepoErrors appends one repository entry per stored publish
// failure record, in the feature's declared repo order.
func appendOwnedRepoErrors(entries []OwnedError, f *feature.Feature) []OwnedError {
	for _, repo := range f.Repos {
		state := f.RepoStates[repo.Name]
		if state == nil || state.Error == nil {
			continue
		}
		entries = appendOwnedError(entries, ErrorReference{
			Scope:      errorScopeRepository,
			Code:       string(state.Error.Code),
			FeatureID:  f.ID,
			Repository: repo.Name,
		}, *state.Error)
	}
	return entries
}

// appendOwnedAttention appends the child's integration attention record as a
// transaction entry keyed by the child id.
func appendOwnedAttention(entries []OwnedError, child *feature.Feature) []OwnedError {
	rec := child.IntegrationAttentionRecord()
	if rec == nil {
		return entries
	}
	return appendOwnedError(entries, ErrorReference{
		Scope:     errorScopeTransaction,
		Code:      string(rec.Code),
		FeatureID: child.ID,
	}, *rec)
}

// appendOwnedError renders one stored record through the catalog and appends
// it when it is a presence-class error. Warning-class records never reach
// the projection; diagnostics never cross into it.
func appendOwnedError(entries []OwnedError, ref ErrorReference, record errcat.FailureRecord) []OwnedError {
	rendered := errcat.RenderRecord(record)
	switch rendered.Class {
	case errcat.ClassBlocking, errcat.ClassNeedsAction:
	default:
		return entries
	}
	wire := wireError(rendered)
	wire.Diagnostics = ""
	return append(entries, OwnedError{Ref: ref, Error: wire})
}
