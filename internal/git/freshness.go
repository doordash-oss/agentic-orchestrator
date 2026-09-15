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

package git

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// TrackingRefreshInterval bounds how often RepoFreshness fetches a branch the
// clone's fetch refspec does not map. TrackingRefreshTimeout bounds one fetch.
var (
	TrackingRefreshInterval = time.Minute
	TrackingRefreshTimeout  = 10 * time.Second
)

var trackingRefreshes sync.Map // worktree + branch → time.Time of last fetch

// Freshness status values returned by RepoFreshness. Exported since callers
// outside this package (e.g. cmd/agentico) switch on these literal strings.
const (
	FreshnessUnknown      = "unknown"
	FreshnessLocalChanges = "local changes"
)

func RepoFreshness(worktreePath string) string {
	if worktreePath == "" {
		return FreshnessUnknown
	}
	if worktreeMutationInProgress(worktreePath) {
		return FreshnessUnknown
	}
	if out, _, err := runProbe("-C", worktreePath, "status", "--porcelain"); err != nil {
		return FreshnessUnknown
	} else if strings.TrimSpace(string(out)) != "" {
		return FreshnessLocalChanges
	}
	remote := "@{upstream}"
	if _, timedOut, err := runProbe("-C", worktreePath, "rev-parse", "--abbrev-ref", "--symbolic-full-name", remote); timedOut {
		return FreshnessUnknown
	} else if err != nil {
		// git only resolves @{upstream} through the fetch refspec. A
		// single-branch clone never maps feature branches, so fall back to
		// the remote-tracking ref by name.
		remote = unmappedTrackingRef(worktreePath)
		if remote == "" {
			return "local only"
		}
	}
	out, _, err := runProbe("-C", worktreePath, "rev-list", "--left-right", "--count", "HEAD..."+remote)
	if err != nil {
		return FreshnessUnknown
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 {
		return FreshnessUnknown
	}
	ahead, errA := strconv.Atoi(fields[0])
	behind, errB := strconv.Atoi(fields[1])
	if errA != nil || errB != nil {
		return FreshnessUnknown
	}
	if ahead == 0 && behind == 0 {
		return "in sync"
	}
	return FreshnessLocalChanges
}

// unmappedTrackingRef returns refs/remotes/origin/<branch> for the checked-out
// branch when the clone's fetch refspec does not map it, refreshing that ref
// from origin at most once per TrackingRefreshInterval so the comparison
// reflects the remote rather than the last time something fetched the
// branch by hand. It returns "" when HEAD is detached, the refspec already
// maps the branch (git would have resolved @{upstream}), or the remote
// branch does not exist.
func unmappedTrackingRef(worktreePath string) string {
	out, _, err := runProbe("-C", worktreePath, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil {
		return ""
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		return ""
	}
	if covered, err := FetchRefspecCoversBranch(worktreePath, branch); err != nil || covered {
		return ""
	}
	ref := "refs/remotes/origin/" + branch
	refreshTrackingRef(worktreePath, branch, ref)
	if _, _, err := runProbe("-C", worktreePath, "rev-parse", "--verify", "-q", ref+"^{commit}"); err != nil {
		return ""
	}
	return ref
}

func refreshTrackingRef(worktreePath, branch, ref string) {
	key := worktreePath + "\x00" + branch
	now := time.Now()
	if last, ok := trackingRefreshes.Load(key); ok && now.Sub(last.(time.Time)) < TrackingRefreshInterval {
		return
	}
	trackingRefreshes.Store(key, now)
	// Failures leave the previous ref in place; the label then reflects the
	// last successful refresh rather than failing the probe.
	_, _, _ = runProbeBounded(TrackingRefreshTimeout, "-C", worktreePath, "fetch", "--no-tags", "origin", "+refs/heads/"+branch+":"+ref)
}

// FetchRefspecCoversBranch reports whether any configured origin fetch
// refspec maps refs/heads/<branch> to a remote-tracking ref. Single-branch
// clones map only their default branch, so git maintains no tracking ref
// for other branches and cannot derive an implicit force-with-lease.
func FetchRefspecCoversBranch(worktreePath, branch string) (bool, error) {
	out, _, err := runProbe("-C", worktreePath, "config", "--get-all", "remote.origin.fetch")
	if err != nil {
		return false, err
	}
	want := "refs/heads/" + branch
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		src := strings.TrimPrefix(strings.TrimSpace(line), "+")
		if i := strings.Index(src, ":"); i >= 0 {
			src = src[:i]
		}
		if refspecSourceMatches(src, want) {
			return true, nil
		}
	}
	return false, nil
}

// refspecSourceMatches implements git's refspec source matching: an exact
// ref name, or a pattern with a single "*" standing for any non-empty
// path segment sequence.
func refspecSourceMatches(pattern, ref string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == ref
	}
	prefix, suffix, _ := strings.Cut(pattern, "*")
	return len(ref) > len(prefix)+len(suffix) && strings.HasPrefix(ref, prefix) && strings.HasSuffix(ref, suffix)
}

// FreshnessCache decorates RepoFreshness with a bounded, deduplicated,
// never-blocking cache so repeated read-model requests for the same worktree
// cost one background git probe rather than up to three subprocesses each.
type FreshnessCache struct {
	cache *ProbeCache[string]
}

// NewFreshnessCache caches RepoFreshness with the default TTL and bound.
func NewFreshnessCache() *FreshnessCache {
	return NewFreshnessCacheWithProbe(RepoFreshness)
}

// NewFreshnessCacheWithProbe caches an arbitrary freshness probe.
func NewFreshnessCacheWithProbe(probe func(worktreePath string) string) *FreshnessCache {
	return &FreshnessCache{cache: NewProbeCache(0, 0, probe)}
}

// Freshness reports the freshness of worktreePath from cache, refreshing in
// the background once the entry goes stale.
func (c *FreshnessCache) Freshness(worktreePath string) string {
	if worktreePath == "" {
		return FreshnessUnknown
	}
	return c.cache.Get(worktreePath)
}
