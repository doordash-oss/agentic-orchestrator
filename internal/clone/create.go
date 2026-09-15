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

package clone

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// CreateStartInput is one repository-creation request. The server layer
// enforces the explicit consent flag before the request reaches this
// boundary; everything else (root eligibility, child-name validity,
// destination reservation, owned staging, atomic no-replace publication)
// is shared with the clone start boundary.
type CreateStartInput struct {
	Root           string
	Destination    string
	IdempotencyKey string
}

// Create durably accepts a repository-creation request and drives it to a
// terminal outcome synchronously: the operation is short (two bounded local
// git commands), so the caller observes the authoritative final record
// rather than polling. The whole sequence reuses the clone machinery —
// idempotency replay, the shared destination reservation (a clone or
// another create holding the destination refuses this one, and vice
// versa), hidden owned staging under the destination root, identity-bound
// verification and atomic no-replace publication. A failure removes only
// provably owned staging and releases the reservation; an uncertain
// cleanup stays truthfully cleanup-pending.
func (s *Service) Create(ctx context.Context, input CreateStartInput) (Record, error) {
	if s.drainingNow() {
		return Record{}, serviceError(CodeUnavailable, "server is shutting down")
	}
	if err := ValidateIdempotencyKey(input.IdempotencyKey); err != nil {
		var verr *ValidationError
		detail := ""
		if errors.As(err, &verr) {
			detail = verr.Detail
		}
		return Record{}, serviceError(CodeIdempotencyConflict, detail)
	}
	var verr *ValidationError
	if err := ValidateDestination(input.Destination); err != nil {
		detail := ""
		if errors.As(err, &verr) {
			detail = verr.Detail
		}
		return Record{}, serviceError(ValidationDestinationInvalid, detail)
	}
	root, err := s.resolveRoot(input.Root)
	if err != nil {
		return Record{}, err
	}
	destinationPath := filepath.Join(root.Resolved, input.Destination)
	// The empty remote keeps a create fingerprint disjoint from every clone
	// fingerprint: clone remotes are validated non-empty.
	fp := inputFingerprint("", root.Resolved, input.Destination)

	rec, replayed, err := s.acceptOperation(acceptRequest{
		kind:             KindCreate,
		root:             root,
		destination:      input.Destination,
		destinationPath:  destinationPath,
		idempotencyKey:   input.IdempotencyKey,
		inputFingerprint: fp,
		// A create replay must not be satisfied by a clone record.
		replayRequiresSameKind: true,
	})
	if err != nil {
		return Record{}, err
	}
	if replayed {
		// Replay returns the retained record whatever its state: a
		// succeeded record replays its published result; anything else is
		// mapped by the server layer onto a truthful unavailable error.
		return rec, nil
	}
	s.runCreateOperation(ctx, &rec)
	out, ok := s.store.get(rec.ID)
	if !ok {
		return Record{}, serviceError(CodeInternal, "read create outcome")
	}
	return out, nil
}

// runCreateOperation drives one accepted create record to a terminal
// outcome while holding the operation lock, so cancellation, recovery and
// competing mutations serialize exactly as they do for clones.
func (s *Service) runCreateOperation(ctx context.Context, rec *Record) {
	op := s.op(rec.ID)
	op.mu.Lock()
	defer op.mu.Unlock()

	cur, ok := s.store.get(rec.ID)
	if !ok || !ActiveStates[cur.State] {
		return
	}
	if cur.CancelRequested {
		// A cancellation won before any git execution: resolve through the
		// same verified-cleanup boundary as every other outcome.
		s.finalizeOutcomeLocked(op, &cur, StateCancelled, "", "", nil)
		return
	}
	cur.State = StateRunning
	if err := s.save(&cur); err != nil {
		// A truthful unresolved record stays on disk; reconciliation
		// retries the transition and cleanup.
		s.finalizeOutcomeLocked(op, &cur, StateFailed, FailureCleanupPending, "persist running state", nil)
		return
	}
	workDir := filepath.Join(cur.StagingPath, stagingWorkDir)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		s.finalizeOutcomeLocked(op, &cur, StateFailed, FailureExecution, "create work directory: "+err.Error(), nil)
		return
	}
	createGit := s.opts.CreateGit
	if createGit == nil {
		createGit = git.CreateRepository
	}
	if err := createGit(ctx, workDir); err != nil {
		s.finalizeOutcomeLocked(op, &cur, StateFailed, FailureExecution, "create repository: "+err.Error(), nil)
		return
	}
	fresh, ok := s.store.get(rec.ID)
	if !ok || !ActiveStates[fresh.State] {
		return
	}
	if fresh.CancelRequested {
		s.finalizeOutcomeLocked(op, &fresh, StateCancelled, "", "", nil)
		return
	}
	// The irreversible success boundary is shared with clones: verify the
	// world still matches the reservation, pin publication evidence, then
	// atomically publish without replacement.
	s.publishLocked(op, &fresh)
}
