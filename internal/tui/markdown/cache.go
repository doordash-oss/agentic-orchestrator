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

package markdown

import (
	"hash/fnv"
	"sync"
)

// cacheCap bounds the memo so that a long session can't grow it unboundedly.
// On overflow the cache is cleared wholesale — cheap to rebuild on demand,
// and avoids pulling in an LRU dependency.
const cacheCap = 512

type cacheKey struct {
	hash  uint64
	width int
}

type renderCache struct {
	mu      sync.RWMutex
	entries map[cacheKey]string
}

func newRenderCache() *renderCache {
	return &renderCache{entries: make(map[cacheKey]string, 64)}
}

func (c *renderCache) get(text string, width int) (string, bool) {
	k := keyFor(text, width)
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.entries[k]
	return v, ok
}

func (c *renderCache) put(text string, width int, rendered string) {
	k := keyFor(text, width)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= cacheCap {
		c.entries = make(map[cacheKey]string, 64)
	}
	c.entries[k] = rendered
}

func keyFor(text string, width int) cacheKey {
	h := fnv.New64a()
	_, _ = h.Write([]byte(text))
	return cacheKey{hash: h.Sum64(), width: width}
}
