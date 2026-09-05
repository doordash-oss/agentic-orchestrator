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
	"encoding/json"
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

	// The reservation critical section mirrors a clone start: it covers
	// idempotency, reservation, staging and durable acceptance, then ends
	// before any git execution. The durable accepted record holds the
	// destination reservation from here on.
	s.startMu.Lock()

	if existing, ok := s.store.getByIdempotencyKey(input.IdempotencyKey); ok {
		s.startMu.Unlock()
		if recordKind(&existing) != KindCreate || existing.InputFingerprint != fp {
			return Record{}, serviceError(CodeIdempotencyConflict, "idempotency key was already used with different input")
		}
		// Replay returns the retained record whatever its state: a
		// succeeded record replays its published result; anything else is
		// mapped by the server layer onto a truthful unavailable error.
		return existing, nil
	}
	if holder, ok := s.store.reservation(destinationPath); ok {
		s.startMu.Unlock()
		return Record{}, serviceError(CodeDestinationReserved, "destination is reserved by operation "+holder.ID)
	}
	if _, err := os.Lstat(destinationPath); err == nil {
		s.startMu.Unlock()
		return Record{}, serviceError(CodeDestinationExists, "destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		s.startMu.Unlock()
		return Record{}, serviceError(CodeInternal, "inspect destination")
	}
	for name := range s.config().Repos {
		if name == input.Destination {
			s.startMu.Unlock()
			return Record{}, serviceError(CodeDestinationShadowed, "an explicit repository registration already uses this name")
		}
	}

	id := newID(KindCreate)
	nonce := newNonce()
	handle, err := openRootHandle(root.Resolved)
	if err != nil {
		s.startMu.Unlock()
		return Record{}, serviceError(CodeInternal, "open root")
	}
	staging := stagingNameFor(KindCreate, id)
	if _, err := handle.root.Lstat(staging); err == nil {
		handle.Close()
		s.startMu.Unlock()
		return Record{}, serviceError(CodeInternal, "staging path collision")
	} else if !errors.Is(err, os.ErrNotExist) {
		handle.Close()
		s.startMu.Unlock()
		return Record{}, serviceError(CodeInternal, "inspect staging path")
	}
	if err := handle.root.Mkdir(staging, 0o700); err != nil {
		handle.Close()
		s.startMu.Unlock()
		return Record{}, serviceError(CodeInternal, "create staging")
	}
	marker, err := json.Marshal(ownershipMarker{
		OperationID: id, Nonce: nonce,
		RootDevice: root.Identity.Device, RootInode: root.Identity.Inode,
		CreatedAt: s.now().UTC(),
	})
	if err == nil {
		err = handle.root.WriteFile(filepath.Join(staging, ownershipMarkerName), marker, 0o600)
	}
	if err != nil {
		// Persistence failure of the ownership marker starts no git and
		// releases only the staging this process just created and owns.
		_ = handle.root.RemoveAll(staging)
		handle.Close()
		s.startMu.Unlock()
		return Record{}, serviceError(CodeInternal, "write staging ownership marker")
	}
	handle.Close()

	now := s.now().UTC()
	rec := Record{
		ID:               id,
		Kind:             KindCreate,
		IdempotencyKey:   input.IdempotencyKey,
		InputFingerprint: fp,
		RootPath:         root.Configured,
		RootResolved:     root.Resolved,
		RootDevice:       root.Identity.Device,
		RootInode:        root.Identity.Inode,
		Destination:      input.Destination,
		DestinationPath:  destinationPath,
		StagingPath:      filepath.Join(root.Resolved, staging),
		OwnershipNonce:   nonce,
		State:            StateAccepted,
		Stage:            StagePreparing,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	// Durable acceptance precedes any git execution, exactly like a clone
	// start: a crash between here and publication is recoverable.
	if err := s.save(&rec); err != nil {
		s.releaseFreshStaging(&rec)
		s.startMu.Unlock()
		return Record{}, serviceError(CodeInternal, "persist accepted operation")
	}
	s.startMu.Unlock()
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
