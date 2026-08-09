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
)

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
	if out, _, err := runProbe("--no-optional-locks", "-C", worktreePath, "status", "--porcelain"); err != nil {
		return FreshnessUnknown
	} else if strings.TrimSpace(string(out)) != "" {
		return FreshnessLocalChanges
	}
	if _, timedOut, err := runProbe("-C", worktreePath, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); timedOut {
		return FreshnessUnknown
	} else if err != nil {
		return "local only"
	}
	out, _, err := runProbe("-C", worktreePath, "rev-list", "--left-right", "--count", "HEAD...@{upstream}")
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
