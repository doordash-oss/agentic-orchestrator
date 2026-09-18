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

// StackPullRequestEntry is the read-model projection of one repository's
// delivery state on one stack layer: the layer's position, title, and
// branch; the pull request URL and lifecycle state when one exists; the
// no-commits marker for a layer that delivered nothing in the repository;
// and the pushed-up-to-date flag recording that the layer's recorded tip
// equals its last-pushed SHA. State is "none" before any pull request
// exists; the read model reports recorded state only and never inspects
// git.
type StackPullRequestEntry struct {
	Position       int
	Title          string
	Branch         string
	URL            string
	State          StackPRState
	NoCommits      bool
	PushedUpToDate bool
}

// stackLayerPRState normalizes a recorded entry's pull request state for
// the read model: a layer without a pull request URL is always "none"
// regardless of any stale recorded state, and a URL recorded without a
// state (impossible through the publish writes, tolerated defensively)
// reads as open — the same posture the publish walk takes toward an
// indeterminate remote answer.
func stackLayerPRState(entry StackRepoEntry) StackPRState {
	if entry.PRURL == "" {
		return StackPRStateNone
	}
	switch entry.PRState {
	case StackPRStateOpen, StackPRStateMerged, StackPRStateClosed:
		return entry.PRState
	default:
		return StackPRStateOpen
	}
}

// StackRepoPullRequestEntries returns one repository's ordered per-layer
// entries: one entry for every layer of the stack, ascending by position,
// never skipping positions. A layer with no recorded entry for the
// repository yields state "none", no URL, and both flags false — a
// not-yet-published layer and an empty one are distinguished by the
// no-commits marker, which the recorded state alone cannot do. Nil for a
// run without a stack.
func (f *Feature) StackRepoPullRequestEntries(repoName string) []StackPullRequestEntry {
	if f == nil || len(f.Stack) == 0 {
		return nil
	}
	layers := append([]StackLayer(nil), f.Stack...)
	sort.SliceStable(layers, func(i, j int) bool { return layers[i].Position < layers[j].Position })
	entries := make([]StackPullRequestEntry, 0, len(layers))
	for _, layer := range layers {
		entry := layer.Repos[repoName]
		entries = append(entries, StackPullRequestEntry{
			Position:       layer.Position,
			Title:          layer.Title,
			Branch:         layer.Branch,
			URL:            entry.PRURL,
			State:          stackLayerPRState(entry),
			NoCommits:      entry.NoCommits,
			PushedUpToDate: entry.PRURL != "" && entry.TipSHA == entry.LastPushedSHA,
		})
	}
	return entries
}

// TopStackLayerPRURL returns the pull request URL of the highest positioned
// stack layer whose entry for repoName carries a pull request — the
// repository's primary reviewable artifact under a stacked delivery. Empty
// when no layer entry for the repository has a pull request.
func (f *Feature) TopStackLayerPRURL(repoName string) string {
	if f == nil {
		return ""
	}
	var url string
	best := 0
	for _, layer := range f.Stack {
		if layer.Position <= best {
			continue
		}
		if entry, ok := layer.Repos[repoName]; ok && entry.PRURL != "" {
			url = entry.PRURL
			best = layer.Position
		}
	}
	return url
}

// LowestStackLayerPRURL returns the pull request URL of the lowest
// positioned stack layer whose entry for repoName carries a pull request.
// The lowest layer's pull request bases on a real target branch (the
// repository's base or a merged lower layer), which is why rebase target
// resolution reads it instead of the top: an upper layer's pull request
// bases on the layer branch below it. Empty when no layer entry for the
// repository has a pull request.
func (f *Feature) LowestStackLayerPRURL(repoName string) string {
	if f == nil {
		return ""
	}
	for _, layer := range orderedStackLayersForRead(f.Stack) {
		if entry, ok := layer.Repos[repoName]; ok && entry.PRURL != "" {
			return entry.PRURL
		}
	}
	return ""
}

// StackRepoPRURLList returns every pull request URL recorded for repoName
// across the stack's layers, ascending by layer position.
func (f *Feature) StackRepoPRURLList(repoName string) []string {
	if f == nil {
		return nil
	}
	var urls []string
	for _, layer := range orderedStackLayersForRead(f.Stack) {
		if entry, ok := layer.Repos[repoName]; ok && entry.PRURL != "" {
			urls = append(urls, entry.PRURL)
		}
	}
	return urls
}

// AnyStackLayerHasPullRequest reports whether any layer of any repository
// of the stack carries a pull request. False for a run without a stack —
// the single-URL projection that used to answer this question for
// stackless runs is gone, and a publishable stackless run fails closed at
// publish instead.
func (f *Feature) AnyStackLayerHasPullRequest() bool {
	if f == nil {
		return false
	}
	for _, layer := range f.Stack {
		for _, entry := range layer.Repos {
			if entry.PRURL != "" {
				return true
			}
		}
	}
	return false
}

// StackRepoHasPullRequest reports whether any layer of the stack carries a
// pull request for repoName.
func (f *Feature) StackRepoHasPullRequest(repoName string) bool {
	if f == nil {
		return false
	}
	for _, layer := range f.Stack {
		if entry, ok := layer.Repos[repoName]; ok && entry.PRURL != "" {
			return true
		}
	}
	return false
}

// OrderedStackLayers returns the stack's layers sorted ascending by position.
// A copy, so callers never mutate the run's slice through it. The rebase
// preflight walks it to classify every layer and refresh layer branches.
func (f *Feature) OrderedStackLayers() []StackLayer {
	if f == nil {
		return nil
	}
	return orderedStackLayersForRead(f.Stack)
}

// orderedStackLayersForRead returns the stack's layers sorted ascending by
// position. A copy, so callers never mutate the run's slice through a read
// helper.
func orderedStackLayersForRead(stack []StackLayer) []StackLayer {
	layers := append([]StackLayer(nil), stack...)
	sort.SliceStable(layers, func(i, j int) bool { return layers[i].Position < layers[j].Position })
	return layers
}
