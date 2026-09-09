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
	"context"
	"net/http"
	"sync"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// sourceUpdateKey identifies one git store by the same canonical
// common-directory identity the mutation locks use, so catalog aliases of
// one repository (the same store under another entry) are tracked together.
type sourceUpdateKey struct {
	commonDir string
	device    uint64
	inode     uint64
}

func sourceUpdateKeyFor(identity git.RepoIdentity) sourceUpdateKey {
	return sourceUpdateKey{commonDir: identity.CommonDir, device: identity.Device, inode: identity.Inode}
}

// sourceUpdateTracker records the lifetime of admitted Update-from-origin
// attempts: an entry exists from the moment the request is authorized until
// its response is written, covering common-directory coordination waits,
// the mutation, and verification. Reconciliation reads and feature
// acceptance wait on it so they never conclude from a state an admitted
// update may still be mutating. It is advisory visibility on top of the git
// mutation locks, not the serialization itself, and owns no goroutines: the
// owning HTTP handler's context bounds every registered attempt.
type sourceUpdateTracker struct {
	mu     sync.Mutex
	counts map[sourceUpdateKey]int
	idle   map[sourceUpdateKey]chan struct{}
}

func newSourceUpdateTracker() *sourceUpdateTracker {
	return &sourceUpdateTracker{
		counts: make(map[sourceUpdateKey]int),
		idle:   make(map[sourceUpdateKey]chan struct{}),
	}
}

// begin registers one admitted attempt for the repository and returns its
// release. Registration precedes any coordination wait, so a waiter that
// holds the mutation lock can always observe an attempt queued behind it.
func (t *sourceUpdateTracker) begin(identity git.RepoIdentity) func() {
	key := sourceUpdateKeyFor(identity)
	t.mu.Lock()
	t.counts[key]++
	t.mu.Unlock()
	return func() {
		t.mu.Lock()
		t.counts[key]--
		if t.counts[key] <= 0 {
			delete(t.counts, key)
			if ch, ok := t.idle[key]; ok {
				delete(t.idle, key)
				close(ch)
			}
		}
		t.mu.Unlock()
	}
}

// quiescent reports whether no admitted attempt is registered for the
// repository right now.
func (t *sourceUpdateTracker) quiescent(identity git.RepoIdentity) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.counts[sourceUpdateKeyFor(identity)] == 0
}

// wait blocks until no admitted attempt is registered for the repository,
// or ctx ends. New admissions during the wait are observed: the read loop
// re-checks the count under the lock after every wakeup.
func (t *sourceUpdateTracker) wait(ctx context.Context, identity git.RepoIdentity) error {
	key := sourceUpdateKeyFor(identity)
	for {
		t.mu.Lock()
		if t.counts[key] == 0 {
			t.mu.Unlock()
			return nil
		}
		ch, ok := t.idle[key]
		if !ok {
			ch = make(chan struct{})
			t.idle[key] = ch
		}
		t.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// empty reports whether no admitted attempt is registered anywhere, so
// callers can skip settlement waiting entirely on the common path.
func (t *sourceUpdateTracker) empty() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.counts) == 0
}

// awaitSourceUpdateSettlement waits out every admitted Update-from-origin
// attempt on the request's selected repositories before feature acceptance
// reads and pins their sources. Unresolvable selectors are skipped: the
// acceptance path refuses them with its own typed stale error. A wait that
// cannot complete within the bounded deadline fails closed — acceptance
// never proceeds beside an attempt that may still mutate a selected source.
func (h *apiHandler) awaitSourceUpdateSettlement(w http.ResponseWriter, r *http.Request, sources []RepositorySource) bool {
	if len(sources) == 0 || h.sourceUpdates.empty() {
		return true
	}
	var identities []git.RepoIdentity
	for _, source := range sources {
		if _, identity, ok := h.resolveCatalogSourceSelector(RepositorySourceSelector{RepoKey: source.RepoKey, Identity: source.Identity}); ok {
			identities = append(identities, identity)
		}
	}
	if len(identities) == 0 {
		return true
	}
	deadline := h.reconcileSourceDeadline
	if deadline <= 0 {
		deadline = defaultReconcileSourceDeadline
	}
	ctx, cancel := context.WithTimeout(r.Context(), deadline)
	defer cancel()
	for _, identity := range identities {
		if err := h.sourceUpdates.wait(ctx, identity); err != nil {
			writeAPIError(w, http.StatusServiceUnavailable, errcat.Unavailable,
				errcat.WithDiagnostics("a source update for a selected repository has not settled; wait for it to finish, then submit again"))
			return false
		}
	}
	return true
}
