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
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestProbeCacheCollapsesConcurrentColdReads proves the singleflight: many
// simultaneous reads of one worktree cost a single probe, not one each.
func TestProbeCacheCollapsesConcurrentColdReads(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	cache := NewProbeCache(0, 0, func(key string) string {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		return "probed " + key
	})

	const readers = 32
	results := make([]string, readers)
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = cache.Get("/tmp/worktree")
		}(i)
	}
	<-entered
	close(release)
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("probe ran %d times for %d concurrent reads, want 1", got, readers)
	}
	for i, got := range results {
		if got != "probed /tmp/worktree" {
			t.Fatalf("reader %d value = %q, want the shared probe result", i, got)
		}
	}
}

// TestProbeCacheServesEntryWithoutProbing pins that a fresh entry is answered
// from memory: no second probe runs.
func TestProbeCacheServesEntryWithoutProbing(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	cache := NewProbeCache(time.Minute, 0, func(string) string {
		calls.Add(1)
		return "in sync"
	})
	for i := 0; i < 5; i++ {
		if got := cache.Get("/tmp/worktree"); got != "in sync" {
			t.Fatalf("read %d = %q, want in sync", i, got)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("probe ran %d times across 5 reads, want 1", got)
	}
}

// TestProbeCacheServesStaleWhileRevalidating proves an expired entry is served
// immediately from memory while the refresh runs in the background, so no read
// path ever waits on git once a worktree is known.
func TestProbeCacheServesStaleWhileRevalidating(t *testing.T) {
	t.Parallel()

	values := make(chan string, 2)
	values <- "first"
	values <- "second"
	refreshed := make(chan string, 2)
	blocked := make(chan struct{})
	var probes atomic.Int64
	cache := NewProbeCache(time.Minute, 0, func(string) string {
		if probes.Add(1) == 2 {
			<-blocked
		}
		return <-values
	})
	cache.onRefresh = func(key string) { refreshed <- key }

	now := time.Now()
	cache.clock = func() time.Time { return now }
	if got := cache.Get("/tmp/worktree"); got != "first" {
		t.Fatalf("cold read = %q, want first", got)
	}
	<-refreshed

	// Past the TTL the stale value is returned without waiting on the probe,
	// which is still blocked.
	now = now.Add(2 * time.Minute)
	if got := cache.Get("/tmp/worktree"); got != "first" {
		t.Fatalf("stale read = %q, want the stale value served immediately", got)
	}
	close(blocked)
	<-refreshed
	if got := cache.Get("/tmp/worktree"); got != "second" {
		t.Fatalf("post-refresh read = %q, want the refreshed value", got)
	}
	if got := probes.Load(); got != 2 {
		t.Fatalf("probe ran %d times, want 2 (one cold, one background refresh)", got)
	}
}

// TestProbeCacheEvictsLeastRecentlyUsed pins the bound: the map cannot grow
// one entry per worktree forever.
func TestProbeCacheEvictsLeastRecentlyUsed(t *testing.T) {
	t.Parallel()

	cache := NewProbeCache(time.Minute, 2, func(key string) string { return key })
	cache.Get("a")
	cache.Get("b")
	cache.Get("a") // keeps "a" hot so "b" is the eviction victim
	cache.Get("c")

	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.entries) != 2 {
		t.Fatalf("cache holds %d entries, want the bound of 2", len(cache.entries))
	}
	if _, ok := cache.entries["b"]; ok {
		t.Fatalf("cache kept least recently used entry %q", "b")
	}
	for _, key := range []string{"a", "c"} {
		if _, ok := cache.entries[key]; !ok {
			t.Fatalf("cache evicted recently used entry %q", key)
		}
	}
}

// TestCleanlinessCacheNeverReportsIndeterminateAsClean pins the fail-closed
// contract for the gate that guards destructive and refactor work.
func TestCleanlinessCacheNeverReportsIndeterminateAsClean(t *testing.T) {
	t.Parallel()

	for name, inspector := range map[string]*stubCleanlinessInspector{
		"timeout":     {err: fmt.Errorf("checking git status: %w", ErrProbeTimeout)},
		"probe error": {err: errors.New("git exploded")},
		"nil report":  {},
	} {
		inspector := inspector
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cache := NewCleanlinessCache(inspector)
			report, err := cache.InspectCleanliness("/tmp/worktree", 0)
			if err == nil && !report.Dirty() {
				t.Fatalf("InspectCleanliness reported a clean worktree from an indeterminate probe (report %+v)", report)
			}
			if name == "timeout" && !errors.Is(err, ErrProbeTimeout) {
				t.Fatalf("InspectCleanliness error = %v, want the probe timeout preserved", err)
			}
			if name == "nil report" && !errors.Is(err, ErrCleanlinessUnknown) {
				t.Fatalf("InspectCleanliness error = %v, want ErrCleanlinessUnknown", err)
			}
		})
	}
}

// TestCleanlinessCacheServesEntryWithoutProbing proves repeated read-path
// inspections of one worktree run `git status` once.
func TestCleanlinessCacheServesEntryWithoutProbing(t *testing.T) {
	t.Parallel()

	inspector := &stubCleanlinessInspector{
		report: &CleanlinessReport{Untracked: []string{"scratch.txt"}, UntrackedTotal: 1},
	}
	cache := NewCleanlinessCache(inspector)
	for i := 0; i < 4; i++ {
		report, err := cache.InspectCleanliness("/tmp/worktree", DefaultCleanlinessPathLimit)
		if err != nil {
			t.Fatalf("InspectCleanliness error = %v", err)
		}
		if !report.Dirty() {
			t.Fatalf("read %d reported a clean worktree, want dirty", i)
		}
	}
	if got := inspector.calls.Load(); got != 1 {
		t.Fatalf("inspector ran %d times across 4 reads, want 1", got)
	}
}

// TestCleanlinessCacheCollapsesConcurrentReads pins the singleflight for the
// cleanliness probe as well: a burst of parent detail reads costs one
// `git status`.
func TestCleanlinessCacheCollapsesConcurrentReads(t *testing.T) {
	t.Parallel()

	inspector := &stubCleanlinessInspector{
		report:  &CleanlinessReport{},
		block:   make(chan struct{}),
		entered: make(chan struct{}, 1),
	}
	cache := NewCleanlinessCache(inspector)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := cache.InspectCleanliness("/tmp/worktree", 0); err != nil {
				t.Errorf("InspectCleanliness error = %v", err)
			}
		}()
	}
	<-inspector.entered
	close(inspector.block)
	wg.Wait()

	if got := inspector.calls.Load(); got != 1 {
		t.Fatalf("inspector ran %d times for 16 concurrent reads, want 1", got)
	}
}

// TestFreshnessCacheServesEntryWithoutProbing covers the freshness decorator's
// deduplication over the same cache.
func TestFreshnessCacheServesEntryWithoutProbing(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	cache := NewFreshnessCacheWithProbe(func(string) string {
		calls.Add(1)
		return "in sync"
	})
	for i := 0; i < 3; i++ {
		if got := cache.Freshness("/tmp/worktree"); got != "in sync" {
			t.Fatalf("read %d = %q, want in sync", i, got)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("probe ran %d times across 3 reads, want 1", got)
	}
	if got := cache.Freshness(""); got != FreshnessUnknown {
		t.Fatalf("empty worktree freshness = %q, want unknown", got)
	}
}

type stubCleanlinessInspector struct {
	report  *CleanlinessReport
	err     error
	calls   atomic.Int64
	block   chan struct{}
	entered chan struct{}
}

func (s *stubCleanlinessInspector) InspectCleanliness(string, int) (*CleanlinessReport, error) {
	s.calls.Add(1)
	if s.entered != nil {
		s.entered <- struct{}{}
	}
	if s.block != nil {
		<-s.block
	}
	return s.report, s.err
}
