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
	"sync"
	"time"
)

// ProbeCacheTTL is how long a probe result is served before a background
// refresh is scheduled. Read paths poll far more often than a worktree
// changes, so anything shorter buys accuracy nobody can perceive at the cost
// of a subprocess per request.
const ProbeCacheTTL = 5 * time.Second

// ProbeCacheMaxEntries bounds how many worktrees a cache tracks. Keys are
// worktree paths, and children come and go, so an unbounded map would grow
// for the lifetime of the process.
const ProbeCacheMaxEntries = 512

// ProbeCache serves the result of an expensive git probe from memory. A fresh
// key is served from the map; a stale key is served from the map too and a
// single background refresh is scheduled, so no caller after the first ever
// waits on git. Concurrent callers for the same key collapse onto that one
// probe, so a burst of reads costs one git invocation rather than one per read.
// Only a cold key — nothing cached at all — waits, joining the same shared
// probe (itself bounded by ProbeTimeout) rather than inventing an answer.
type ProbeCache[V any] struct {
	ttl   time.Duration
	max   int
	probe func(key string) V

	mu      sync.Mutex
	entries map[string]*probeCacheEntry[V]

	// Test hooks: a clock and a post-refresh notification.
	clock     func() time.Time
	onRefresh func(key string)
}

type probeCacheEntry[V any] struct {
	value      V
	present    bool
	expires    time.Time
	lastAccess time.Time
	// done is non-nil while a probe for this key is in flight and is closed
	// when it lands, so cold callers can join instead of spawning their own.
	done chan struct{}
}

// NewProbeCache builds a cache over probe. A ttl or max of zero or less
// applies ProbeCacheTTL / ProbeCacheMaxEntries.
func NewProbeCache[V any](ttl time.Duration, max int, probe func(key string) V) *ProbeCache[V] {
	if ttl <= 0 {
		ttl = ProbeCacheTTL
	}
	if max <= 0 {
		max = ProbeCacheMaxEntries
	}
	return &ProbeCache[V]{
		ttl:     ttl,
		max:     max,
		probe:   probe,
		entries: make(map[string]*probeCacheEntry[V]),
		clock:   time.Now,
	}
}

// Get returns the value for key, serving a stale value immediately while a
// refresh runs in the background. Only the first call for a key waits.
func (c *ProbeCache[V]) Get(key string) V {
	c.mu.Lock()
	now := c.clock()
	entry := c.entries[key]
	if entry == nil {
		c.evictLocked()
		entry = &probeCacheEntry[V]{}
		c.entries[key] = entry
	}
	entry.lastAccess = now
	if (!entry.present || now.After(entry.expires)) && entry.done == nil {
		entry.done = make(chan struct{})
		go c.refresh(key, entry)
	}
	if entry.present {
		value := entry.value
		c.mu.Unlock()
		return value
	}
	done := entry.done
	c.mu.Unlock()

	<-done
	c.mu.Lock()
	value := entry.value
	c.mu.Unlock()
	return value
}

func (c *ProbeCache[V]) refresh(key string, entry *probeCacheEntry[V]) {
	value := c.probe(key)
	c.mu.Lock()
	entry.value = value
	entry.present = true
	entry.expires = c.clock().Add(c.ttl)
	done := entry.done
	entry.done = nil
	c.mu.Unlock()
	close(done)
	if c.onRefresh != nil {
		c.onRefresh(key)
	}
}

// evictLocked drops the least recently accessed entry once the cache is full.
// An in-flight refresh for an evicted key simply lands on an orphaned entry
// and is recomputed on the next Get.
func (c *ProbeCache[V]) evictLocked() {
	if len(c.entries) < c.max {
		return
	}
	oldestKey := ""
	var oldest time.Time
	for key, entry := range c.entries {
		if oldestKey == "" || entry.lastAccess.Before(oldest) {
			oldestKey, oldest = key, entry.lastAccess
		}
	}
	delete(c.entries, oldestKey)
}
