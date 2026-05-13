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

package session

import (
	"sync"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// streamRingCap bounds the drop-oldest ring that buffers high-volume
// stream_event (and codex stderr synthetic) SDK messages before they
// reach the attachCh consumer. Older entries are evicted on overflow:
// stream deltas that the UI could not display in real time are
// superseded by newer ones anyway.
const streamRingCap = 50

// attachStreamReserve is the free-slot headroom that must remain in
// attachCh before the ring drainer will push another stream event.
// This reserves capacity for critical messages (Result /
// ControlRequest) so they never time out behind a backlog of stream
// deltas — the central backpressure invariant.
const attachStreamReserve = 25

// streamRing is a drop-oldest bounded ring buffer decoupling a
// high-rate stream_event producer from the attachCh consumer. Safe
// for concurrent Push / Pop / Close callers. The Notify channel
// lets a dedicated drainer goroutine sleep until work arrives.
type streamRing struct {
	mu     sync.Mutex
	buf    []llm.SDKMessage
	head   int
	size   int
	notify chan struct{}
	closed bool
}

func newStreamRing(capacity int) *streamRing {
	return &streamRing{
		buf:    make([]llm.SDKMessage, capacity),
		notify: make(chan struct{}, 1),
	}
}

// Push appends msg to the ring, evicting the oldest entry if full.
// A no-op if the ring has been Closed. Fires the notify signal so a
// blocked drainer wakes up.
func (r *streamRing) Push(msg llm.SDKMessage) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	if r.size == len(r.buf) {
		r.head = (r.head + 1) % len(r.buf)
		r.size--
	}
	idx := (r.head + r.size) % len(r.buf)
	r.buf[idx] = msg
	r.size++
	select {
	case r.notify <- struct{}{}:
	default:
	}
	r.mu.Unlock()
}

// Pop removes and returns the oldest entry. ok=false when empty.
func (r *streamRing) Pop() (llm.SDKMessage, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size == 0 {
		return llm.SDKMessage{}, false
	}
	msg := r.buf[r.head]
	r.buf[r.head] = llm.SDKMessage{} // help GC
	r.head = (r.head + 1) % len(r.buf)
	r.size--
	return msg, true
}

// Len returns the current number of buffered items.
func (r *streamRing) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.size
}

// Close rejects further Pushes. Already-buffered items remain
// drainable via Pop. Fires notify so a blocked drainer wakes up and
// can observe the closed-and-empty state.
func (r *streamRing) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

// isClosedAndEmpty reports whether the drainer can safely exit.
func (r *streamRing) isClosedAndEmpty() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed && r.size == 0
}

// isClosed reports whether Close has been called. The drainer reads
// this to decide whether the attach-slot reserve still needs honoring
// (false → reserve applies, true → no critical traffic will arrive, so
// drain unconditionally and exit).
func (r *streamRing) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// Notify returns the wake-up channel for the drainer.
func (r *streamRing) Notify() <-chan struct{} {
	return r.notify
}
