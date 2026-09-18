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
	"fmt"
	"sort"
)

// RestackRefUpdate is one ref update recorded in a restack journal entry.
type RestackRefUpdate struct {
	Ref    string `yaml:"ref"`
	OldSHA string `yaml:"old_sha,omitempty"`
	NewSHA string `yaml:"new_sha,omitempty"`
}

// RestackRemap records the roadmap-phase anchors and layer tips a journal
// entry will persist once its ref transaction has landed.
type RestackRemap struct {
	Anchors map[int]string `yaml:"anchors,omitempty"` // roadmap phase → new SHA
	Tips    map[int]string `yaml:"tips,omitempty"`    // layer position → new SHA
}

// RestackJournalEntry is the durable record of one repository's in-flight
// restack landing.
type RestackJournalEntry struct {
	Repository  string              `yaml:"repository"`
	Updates     []RestackRefUpdate  `yaml:"updates,omitempty"`
	Remap       RestackRemap        `yaml:"remap,omitempty"`
	State       RestackJournalState `yaml:"state,omitempty"`
	PendingSync bool                `yaml:"pending_sync,omitempty"`
	// RequestedLayer is the layer position the fix round asked to relocate
	// into, for warning rendering during recovery.
	RequestedLayer int `yaml:"requested_layer,omitempty"`
	// NewTopSHA is the rewritten chain's top for this repository; recovery
	// hard-resets the worktree to it.
	NewTopSHA string `yaml:"new_top_sha,omitempty"`
}

// RestackJournalState is the durable landing state of one restack journal
// entry: prepared before its ref transaction is observed landing, applied
// once its remap is persisted, and synced once the worktree reset has
// landed.
type RestackJournalState string

const (
	// RestackJournalPrepared marks an entry whose ref transaction has not
	// yet been observed landing.
	RestackJournalPrepared RestackJournalState = "prepared"
	// RestackJournalApplied marks an entry whose remap was persisted; the
	// worktree reset may still be pending.
	RestackJournalApplied RestackJournalState = "applied"
	// RestackJournalSynced marks an entry whose worktree reset landed.
	RestackJournalSynced RestackJournalState = "synced"
)

// SetRestackJournalEntry writes or replaces the active run's restack journal
// entry for one repository. A repository holds at most one entry; a second
// write for the same repository replaces the first in place.
func (m *Manager) SetRestackJournalEntry(featureID, repository string, entry RestackJournalEntry) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		entry.Repository = repository
		r := f.Run()
		for i := range r.RestackJournal {
			if r.RestackJournal[i].Repository == repository {
				r.RestackJournal[i] = entry
				return nil
			}
		}
		r.RestackJournal = append(r.RestackJournal, entry)
		return nil
	})
}

// ApplyRestackJournalEntry persists one repository's remap onto the active
// run and removes its journal entry in the same write: every remapped
// roadmap-phase anchor rewrites that repository's SHA in
// RoadmapPhaseCommitAnchors (creating the phase's inner map when absent),
// and every remapped layer tip rewrites that repository's TipSHA on the
// named stack layer (creating the Repos entry when absent and preserving
// every other field an earlier boundary or round hook recorded). Phases,
// layers, and repositories the remap does not name are untouched. Fails
// when the feature or the repository's entry is missing.
func (m *Manager) ApplyRestackJournalEntry(featureID, repository string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		r := f.Run()
		idx := -1
		for i := range r.RestackJournal {
			if r.RestackJournal[i].Repository == repository {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("no restack journal entry for repository %q", repository)
		}
		remap := r.RestackJournal[idx].Remap
		applyRestackRemap(f, remap, repository)
		r.RestackJournal = append(r.RestackJournal[:idx], r.RestackJournal[idx+1:]...)
		return nil
	})
}

// SetRestackJournalPendingSync marks or clears one repository's journal
// entry pending-sync flag. Fails when the feature or the entry is missing.
func (m *Manager) SetRestackJournalPendingSync(featureID, repository string, pending bool) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		r := f.Run()
		for i := range r.RestackJournal {
			if r.RestackJournal[i].Repository == repository {
				r.RestackJournal[i].PendingSync = pending
				return nil
			}
		}
		return fmt.Errorf("no restack journal entry for repository %q", repository)
	})
}

// DropRestackJournalEntry removes one repository's journal entry without
// applying anything. Removing an absent entry is a no-op so recovery can
// drop unconditionally after a landing; a missing feature still fails.
func (m *Manager) DropRestackJournalEntry(featureID, repository string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		r := f.Run()
		for i := range r.RestackJournal {
			if r.RestackJournal[i].Repository == repository {
				r.RestackJournal = append(r.RestackJournal[:i], r.RestackJournal[i+1:]...)
				return nil
			}
		}
		return nil
	})
}

// applyRestackRemap writes one repository's remapped roadmap-phase anchors
// and layer tips onto the feature, inside the caller's single Store.Modify
// write. Phases, layers, and repositories the remap does not name are
// untouched.
func applyRestackRemap(f *Feature, remap RestackRemap, repository string) {
	r := f.Run()
	phases := make([]int, 0, len(remap.Anchors))
	for phase := range remap.Anchors {
		phases = append(phases, phase)
	}
	sort.Ints(phases)
	for _, phase := range phases {
		if r.RoadmapPhaseCommitAnchors == nil {
			r.RoadmapPhaseCommitAnchors = make(map[int]map[string]string)
		}
		anchors, ok := r.RoadmapPhaseCommitAnchors[phase]
		if !ok || anchors == nil {
			anchors = make(map[string]string)
			r.RoadmapPhaseCommitAnchors[phase] = anchors
		}
		anchors[repository] = remap.Anchors[phase]
	}
	tips := make([]int, 0, len(remap.Tips))
	for position := range remap.Tips {
		tips = append(tips, position)
	}
	sort.Ints(tips)
	for _, position := range tips {
		for i := range f.Stack {
			if f.Stack[i].Position != position {
				continue
			}
			if f.Stack[i].Repos == nil {
				f.Stack[i].Repos = make(map[string]StackRepoEntry)
			}
			entry := f.Stack[i].Repos[repository]
			entry.TipSHA = remap.Tips[position]
			f.Stack[i].Repos[repository] = entry
		}
	}
}

// ApplyTransactionRemap writes one repository's transaction-journal remap —
// the anchor and tip remap a child transaction records on its journal entry —
// onto the parent feature, inside the caller's single Store.Modify write.
// The remap carries absolute SHAs keyed by roadmap phase and layer position,
// so applying it is idempotent: re-applying after a crash writes the same
// values. Phases, layers, and repositories the remap does not name are
// untouched.
func ApplyTransactionRemap(f *Feature, remap RestackRemap, repository string) {
	if f == nil {
		return
	}
	applyRestackRemap(f, remap, repository)
}

// MarkStackLayerMergedForRepo settles one repository's entry on the stack
// layer at the given position after a transaction deleted that layer's ref:
// the pull request state becomes merged, the tip and last-pushed SHA are
// cleared (the ref no longer exists), and the pull request URL is kept as
// the durable record of the delivered layer. Called from the closure's
// single parent write, so it must stay a pure function of the feature;
// idempotent — a layer already marked merged is left alone.
func MarkStackLayerMergedForRepo(f *Feature, repository string, layerPosition int) {
	if f == nil {
		return
	}
	for i := range f.Stack {
		if f.Stack[i].Position != layerPosition {
			continue
		}
		if f.Stack[i].Repos == nil {
			f.Stack[i].Repos = make(map[string]StackRepoEntry)
		}
		entry := f.Stack[i].Repos[repository]
		entry.TipSHA = ""
		entry.LastPushedSHA = ""
		entry.PRState = StackPRStateMerged
		f.Stack[i].Repos[repository] = entry
	}
}

// RestackRemapForRepository computes the anchor and tip remap for one
// repository given an old-to-new SHA map: every roadmap-phase anchor and
// every layer tip of the repository whose current SHA appears in the map is
// rewritten to the mapped SHA; all others are left untouched (and omitted
// from the result).
func RestackRemapForRepository(anchors map[int]map[string]string, stack []StackLayer, repository string, commitMap map[string]string) RestackRemap {
	var remap RestackRemap
	if len(commitMap) == 0 {
		return remap
	}
	phases := make([]int, 0, len(anchors))
	for phase := range anchors {
		phases = append(phases, phase)
	}
	sort.Ints(phases)
	for _, phase := range phases {
		sha, ok := anchors[phase][repository]
		if !ok {
			continue
		}
		newSHA, ok := commitMap[sha]
		if !ok {
			continue
		}
		if remap.Anchors == nil {
			remap.Anchors = make(map[int]string)
		}
		remap.Anchors[phase] = newSHA
	}
	layers := append([]StackLayer(nil), stack...)
	sort.Slice(layers, func(i, j int) bool { return layers[i].Position < layers[j].Position })
	for _, layer := range layers {
		entry, ok := layer.Repos[repository]
		if !ok {
			continue
		}
		newSHA, ok := commitMap[entry.TipSHA]
		if !ok {
			continue
		}
		if remap.Tips == nil {
			remap.Tips = make(map[int]string)
		}
		remap.Tips[layer.Position] = newSHA
	}
	return remap
}
