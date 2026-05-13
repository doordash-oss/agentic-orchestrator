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
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func streamMsg(tag string) llm.SDKMessage {
	return llm.SDKMessage{
		Type:            "stream_event",
		StreamDeltaType: tag,
	}
}

func TestStreamRing_DropOldestOnOverflow(t *testing.T) {
	r := newStreamRing(3)
	for i, tag := range []string{"a", "b", "c", "d", "e"} {
		r.Push(streamMsg(tag))
		if i < 3 && r.Len() != i+1 {
			t.Fatalf("Len after %d pushes = %d, want %d", i+1, r.Len(), i+1)
		}
	}
	if got := r.Len(); got != 3 {
		t.Fatalf("Len after overflow = %d, want 3", got)
	}
	var tags []string
	for {
		msg, ok := r.Pop()
		if !ok {
			break
		}
		tags = append(tags, msg.StreamDeltaType)
	}
	// Expect the 3 newest: "c", "d", "e".
	want := []string{"c", "d", "e"}
	if len(tags) != len(want) {
		t.Fatalf("popped %v, want %v", tags, want)
	}
	for i := range tags {
		if tags[i] != want[i] {
			t.Fatalf("pop[%d] = %q, want %q", i, tags[i], want[i])
		}
	}
}

func TestStreamRing_PopEmpty(t *testing.T) {
	r := newStreamRing(2)
	if _, ok := r.Pop(); ok {
		t.Fatal("Pop on empty ring returned ok=true")
	}
}

func TestStreamRing_NotifyFiresOnPush(t *testing.T) {
	r := newStreamRing(2)
	select {
	case <-r.Notify():
		t.Fatal("Notify fired without a push")
	default:
	}
	r.Push(streamMsg("x"))
	select {
	case <-r.Notify():
	default:
		t.Fatal("Notify did not fire after push")
	}
}

func TestStreamRing_NotifyFiresOnClose(t *testing.T) {
	r := newStreamRing(2)
	// Drain any noise so we can observe the close signal.
	select {
	case <-r.Notify():
	default:
	}
	r.Close()
	select {
	case <-r.Notify():
	default:
		t.Fatal("Close did not wake the drainer")
	}
	if !r.isClosedAndEmpty() {
		t.Fatal("isClosedAndEmpty = false after Close on empty ring")
	}
}

func TestStreamRing_CloseRejectsFurtherPush(t *testing.T) {
	r := newStreamRing(2)
	r.Push(streamMsg("a"))
	r.Close()
	r.Push(streamMsg("b"))
	if r.Len() != 1 {
		t.Fatalf("Len after post-close push = %d, want 1", r.Len())
	}
}

func TestStreamRing_ClosedNotEmptyUntilDrained(t *testing.T) {
	r := newStreamRing(4)
	r.Push(streamMsg("a"))
	r.Push(streamMsg("b"))
	r.Close()
	if r.isClosedAndEmpty() {
		t.Fatal("isClosedAndEmpty = true before draining")
	}
	if _, ok := r.Pop(); !ok {
		t.Fatal("Pop returned ok=false while ring still held items")
	}
	if _, ok := r.Pop(); !ok {
		t.Fatal("Pop returned ok=false while ring still held items")
	}
	if !r.isClosedAndEmpty() {
		t.Fatal("isClosedAndEmpty = false after draining")
	}
}

// TestStreamRing_ConcurrentProducers exercises the drop-oldest invariant
// under concurrent Pushers. We assert that Len stays bounded and Pops
// yield well-formed messages — the absence of data races is covered by
// `-race`.
func TestStreamRing_IsClosedTracksState(t *testing.T) {
	r := newStreamRing(2)
	if r.isClosed() {
		t.Fatal("fresh ring reports isClosed = true")
	}
	r.Close()
	if !r.isClosed() {
		t.Fatal("ring reports isClosed = false after Close")
	}
}

func TestStreamRing_ConcurrentProducers(t *testing.T) {
	const cap = 32
	r := newStreamRing(cap)
	var wg sync.WaitGroup
	for p := 0; p < 8; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				r.Push(streamMsg("p"))
			}
		}(p)
	}
	wg.Wait()
	if got := r.Len(); got > cap {
		t.Fatalf("Len = %d, want <= %d", got, cap)
	}
	for i := 0; i < cap; i++ {
		msg, ok := r.Pop()
		if !ok {
			break
		}
		if msg.StreamDeltaType != "p" {
			t.Fatalf("popped message with unexpected tag %q", msg.StreamDeltaType)
		}
	}
}
