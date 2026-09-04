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
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Recover reconciles durable records at startup. It restores unresolved
// destination reservations (records persist, so reservations derive from
// them), recognizes already-published success from durable evidence,
// recovers unfinished attempts without launching another git clone, and is
// idempotent: repeated runs converge on the same terminal states.
// A failure to persist a transition leaves the truthful unresolved record
// for the next reconciliation instead of claiming success or cleanup.
func (s *Service) Recover() error {
	var errs []error
	for _, rec := range s.store.all() {
		if !ActiveStates[rec.State] && rec.State != StateCleanupPending {
			continue
		}
		op := s.op(rec.ID)
		op.mu.Lock()
		cur, ok := s.store.get(rec.ID)
		if !ok {
			op.mu.Unlock()
			continue
		}
		switch {
		case ResolvedStates[cur.State]:
			// Already reconciled by a concurrent pass.
		case cur.State == StateCleanupPending:
			s.reconcileCleanupPendingLocked(op, &cur)
		default:
			s.reconcileActiveLocked(op, &cur)
		}
		op.mu.Unlock()
	}
	return errors.Join(errs...)
}

// removeStagingWrapper removes the (marker-only) staging wrapper left
// behind when a crash happened between the atomic publication and the
// wrapper removal. It is best-effort and only ever touches verified owned
// staging.
func (s *Service) removeStagingWrapper(cur *Record) {
	handle, err := openRootHandle(cur.RootResolved)
	if err != nil {
		return
	}
	defer handle.Close()
	if handle.id != (DirIdentity{Device: cur.RootDevice, Inode: cur.RootInode}) {
		return
	}
	staging := stagingName(cur.ID)
	data, err := handle.root.ReadFile(filepath.Join(staging, ownershipMarkerName))
	if err != nil {
		return
	}
	var marker ownershipMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return
	}
	if marker.OperationID != cur.ID || marker.Nonce != cur.OwnershipNonce {
		return
	}
	_ = handle.root.RemoveAll(staging)
}

// reconcileActiveLocked recovers one unfinished attempt (accepted,
// running, finalizing or cancelling) after a restart or crash. The caller
// holds the operation lock. Publication evidence is honored first; every
// other path terminates a verifiably-owned surviving process, then
// resolves through verified cleanup.
func (s *Service) reconcileActiveLocked(op *opState, cur *Record) {
	if ResolvedStates[cur.State] || cur.State == StateCleanupPending {
		return
	}
	// A crash between atomic publication and the success write still
	// counts as success: the evidence is durable in the repository.
	if s.hasPublicationEvidence(cur) {
		s.removeStagingWrapper(cur)
		s.recordSuccessLocked(cur)
		return
	}
	// A finalizing record without publication evidence is an unfinished
	// attempt: fall through to interruption handling.
	stopped, uncertain := s.terminateRecordedProcess(cur)
	if uncertain {
		s.toCleanupPendingLocked(cur, pendingOutcomeFor(cur), "surviving process identity could not be verified")
		return
	}
	_ = stopped
	s.finalizeOutcomeLocked(op, cur, pendingOutcomeFor(cur), "", "", nil)
}

// pendingOutcomeFor maps an interrupted attempt to its terminal outcome,
// keeping a prior explicit cancellation distinguishable from server
// interruption.
func pendingOutcomeFor(cur *Record) State {
	if cur.CancelRequested {
		return StateCancelled
	}
	return StateInterrupted
}

// reconcileCleanupPendingLocked retries the cleanup boundary for one
// unresolved attempt: process liveness/identity first, then late
// publication evidence, then root identity and staging ownership. The
// caller holds the operation lock.
func (s *Service) reconcileCleanupPendingLocked(op *opState, cur *Record) {
	if cur.State != StateCleanupPending {
		return
	}
	if cur.Process != nil && (IsProcessAlive(cur.Process.Pid) || GroupAlive(cur.Process.Pgid)) {
		if !VerifyProcessIdentity(cur.Process.Pid, cur.Process.StartIdentity) {
			// PID reuse or unreadable identity: never signal or delete
			// based on the pid alone.
			cur.CleanupIssue = "surviving process identity could not be verified"
			_ = s.save(cur)
			return
		}
		stopped, uncertain := s.terminateRecordedProcess(cur)
		if uncertain || !stopped {
			cur.CleanupIssue = "surviving process could not be stopped"
			_ = s.save(cur)
			return
		}
	}
	if s.hasPublicationEvidence(cur) {
		// Late success: the attempt actually published before the
		// failure was recorded.
		s.removeStagingWrapper(cur)
		s.recordSuccessLocked(cur)
		return
	}
	cleaned, reason := s.attemptCleanup(cur)
	if !cleaned {
		cur.CleanupIssue = reason
		_ = s.save(cur)
		return
	}
	now := s.now().UTC()
	outcome := cur.PendingOutcome
	if outcome == "" {
		outcome = StateFailed
	}
	cur.State = outcome
	cur.PendingOutcome = ""
	cur.CleanupIssue = ""
	cur.Process = nil
	cur.ResolvedAt = now
	if cur.TerminalAt.IsZero() {
		cur.TerminalAt = now
	}
	_ = s.save(cur)
}

// toCleanupPendingLocked records an unresolved attempt whose cleanup
// cannot currently be proved safe, retaining its reservation.
func (s *Service) toCleanupPendingLocked(cur *Record, outcome State, reason string) {
	now := s.now().UTC()
	cur.State = StateCleanupPending
	cur.PendingOutcome = outcome
	cur.CleanupIssue = reason
	if cur.TerminalAt.IsZero() {
		cur.TerminalAt = now
	}
	_ = s.save(cur)
}

// terminateRecordedProcess stops a surviving worker process tree recorded
// in a recovered record. It signals only after the process start identity
// is re-verified against the record (PID alone is never proof), and never
// waits unbounded. stopped reports the tree is gone; uncertain means the
// identity could not be proved and nothing was signaled.
func (s *Service) terminateRecordedProcess(cur *Record) (stopped, uncertain bool) {
	if cur.Process == nil {
		return true, false
	}
	pid, pgid := cur.Process.Pid, cur.Process.Pgid
	if pid <= 0 {
		return true, false
	}
	if !IsProcessAlive(pid) && !GroupAlive(pgid) {
		return true, false
	}
	if !VerifyProcessIdentity(pid, cur.Process.StartIdentity) {
		return false, true
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !IsProcessAlive(pid) && !GroupAlive(pgid) {
			return true, false
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !IsProcessAlive(pid) && !GroupAlive(pgid) {
			return true, false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false, true
}

// Shutdown drains gracefully: stop accepting clone work, signal and reap
// worker process trees, drain pipes within their bound, then delete only
// owned staging and persist truthful interrupted outcomes (a prior
// explicit cancellation stays distinguishable as cancelled). It is
// bounded by ctx.
func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.draining {
		s.mu.Unlock()
		return nil
	}
	s.draining = true
	workers := make([]*opState, 0, len(s.workers))
	for _, op := range s.workers {
		workers = append(workers, op)
	}
	s.mu.Unlock()

	for _, op := range workers {
		op.signalCancel()
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}

	var errs []error
	for _, rec := range s.store.all() {
		if !ActiveStates[rec.State] {
			continue
		}
		op := s.op(rec.ID)
		op.mu.Lock()
		cur, ok := s.store.get(rec.ID)
		if !ok {
			op.mu.Unlock()
			continue
		}
		if ActiveStates[cur.State] {
			s.reconcileActiveLocked(op, &cur)
		}
		op.mu.Unlock()
	}
	return errors.Join(errs...)
}

var _ = strings.TrimSpace
