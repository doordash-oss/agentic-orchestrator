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
	"strconv"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// spawnWorker starts the worker goroutine for an accepted operation.
func (s *Service) spawnWorker(rec Record) {
	op := s.op(rec.ID)
	op.mu.Lock()
	if op.hasWorker {
		op.mu.Unlock()
		return
	}
	op.hasWorker = true
	op.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runWorker(op, rec.ID)
	}()
}

// progressThrottle bounds how often progress updates persist.
type progressThrottle struct {
	mu   sync.Mutex
	last time.Time
}

func (p *progressThrottle) allow(now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if now.Sub(p.last) < progressIntervalMin {
		return false
	}
	p.last = now
	return true
}

// runWorker drives one accepted operation to a terminal outcome. All
// state transitions re-read the store under the operation lock so
// cancellation, cleanup and recovery serialize correctly with
// publication.
func (s *Service) runWorker(op *opState, id string) {
	throttle := &progressThrottle{}

	// Spawn phase: honor a cancellation that arrived before spawn without
	// holding the operation lock across the runner call.
	op.mu.Lock()
	cur, ok := s.store.get(id)
	if !ok || !ActiveStates[cur.State] || cur.CancelRequested {
		op.mu.Unlock()
		if ok {
			s.finalizeOutcome(op, &cur, StateCancelled, "", "", nil)
		}
		return
	}
	op.mu.Unlock()

	handle, err := s.runner.Start(RunSpec{
		Remote:   cur.RemoteURL,
		Staging:  filepath.Join(cur.StagingPath, stagingWorkDir),
		Deadline: s.deadline,
		OnStage: func(stage, progress string) {
			op.mu.Lock()
			defer op.mu.Unlock()
			fresh, ok := s.store.get(id)
			if !ok || !ActiveStates[fresh.State] || fresh.CancelRequested {
				return
			}
			if fresh.Stage == stage && fresh.Progress == progress {
				return
			}
			if fresh.Stage == stage && !throttle.allow(s.now()) {
				return
			}
			fresh.Stage = stage
			fresh.Progress = progress
			_ = s.save(&fresh)
		},
	})

	op.mu.Lock()
	cur, ok = s.store.get(id)
	if !ok {
		op.mu.Unlock()
		if handle != nil {
			handle.Terminate()
			_ = handle.Wait()
		}
		return
	}
	if err != nil {
		op.mu.Unlock()
		s.finalizeOutcome(op, &cur, StateFailed, FailureExecution, "start git clone: "+err.Error(), nil)
		return
	}
	if cur.CancelRequested || !ActiveStates[cur.State] {
		// Cancellation won before the worker could record the process.
		op.mu.Unlock()
		handle.Terminate()
		res := handle.Wait()
		outcome := StateCancelled
		code := ""
		if !cur.CancelRequested {
			outcome = StateInterrupted
		}
		if res.ExitCode != 0 && !cur.CancelRequested {
			code = ClassifyFailure(res)
			outcome = StateFailed
		}
		s.finalizeOutcome(op, &cur, outcome, code, res.OutputTail, nil)
		return
	}
	cur.Process = &ProcessInfo{
		Pid:           handle.Pid(),
		Pgid:          handle.Pgid(),
		StartIdentity: handle.StartIdentity(),
		StartedAt:     s.now().UTC(),
	}
	cur.State = StateRunning
	if cur.Stage == "" {
		cur.Stage = StageTransferring
	}
	if err := s.save(&cur); err != nil {
		// Persistence failure mid-flight: stop the tree and keep a
		// truthful unresolved result rather than running unrecorded work.
		op.mu.Unlock()
		handle.Terminate()
		_ = handle.Wait()
		s.finalizeOutcome(op, &cur, StateCleanupPending, FailureCleanupPending, "persist running state", nil)
		return
	}
	op.mu.Unlock()

	waitCh := make(chan RunResult, 1)
	go func() { waitCh <- handle.Wait() }()

	var res RunResult
	select {
	case res = <-waitCh:
	case <-op.cancelCh:
		handle.Terminate()
		res = <-waitCh
	}

	op.mu.Lock()
	defer op.mu.Unlock()
	fresh, ok := s.store.get(id)
	if !ok {
		return
	}
	if fresh.CancelRequested || op.cancelSignaled() {
		outcome := StateCancelled
		if !fresh.CancelRequested {
			// The channel fired without a durable explicit request:
			// server shutdown, not user cancellation.
			outcome = StateInterrupted
		}
		s.finalizeOutcomeLocked(op, &fresh, outcome, "", res.OutputTail, nil)
		return
	}
	if res.TimedOut || res.ExitCode != 0 || res.Err != nil {
		s.finalizeOutcomeLocked(op, &fresh, StateFailed, ClassifyFailure(res), res.OutputTail, nil)
		return
	}
	s.publishLocked(op, &fresh)
}

// finalizeOutcome acquires the operation lock and delegates.
func (s *Service) finalizeOutcome(op *opState, cur *Record, outcome State, code, detail string, handle RunHandle) {
	op.mu.Lock()
	defer op.mu.Unlock()
	s.finalizeOutcomeLocked(op, cur, outcome, code, detail, handle)
}

// finalizeOutcomeLocked resolves an operation to a terminal outcome after
// its process is confirmed stopped. The caller holds the operation lock.
// handle may be nil when the process is already known stopped. Failure
// becomes terminal only after owned cleanup completes; otherwise the
// operation stays cleanup-pending with its reservation retained.
func (s *Service) finalizeOutcomeLocked(op *opState, cur *Record, outcome State, code, detail string, handle RunHandle) {
	if handle != nil {
		handle.Terminate()
		_ = handle.Wait()
	}
	if ResolvedStates[cur.State] {
		return
	}
	detail = RedactDiagnostics(boundDetail(detail))
	cleaned, reason := s.attemptCleanup(cur)
	now := s.now().UTC()
	cur.TerminalAt = now
	cur.Progress = ""
	if code != "" && code != FailureCleanupPending && code != FailureCancelled && code != FailureInterrupted {
		cur.Error = &OpError{Code: code, Diagnostics: detail}
	}
	if cleaned {
		cur.State = outcome
		cur.PendingOutcome = ""
		cur.CleanupIssue = ""
		cur.Process = nil
		cur.ResolvedAt = now
		if outcome == StateSucceeded {
			cur.Stage = StageDone
		} else if outcome == StateCancelled || outcome == StateInterrupted {
			cur.Stage = ""
		}
	} else {
		cur.State = StateCleanupPending
		cur.PendingOutcome = outcome
		cur.CleanupIssue = reason
		if cur.Error == nil {
			cur.Error = &OpError{Code: code}
		}
	}
	if err := s.save(cur); err != nil {
		// Keep the truthful unresolved record on disk; the next
		// reconciliation retries the transition.
		return
	}
}

func boundDetail(detail string) string {
	if len(detail) > MaxDiagnosticsLength {
		detail = detail[:MaxDiagnosticsLength]
	}
	return detail
}

// publishLocked performs the irreversible success boundary: verify the
// world still matches the reservation, write publication evidence, then
// atomically publish staging into the destination without replacement.
// The caller holds the operation lock and the process has exited zero.
func (s *Service) publishLocked(op *opState, cur *Record) {
	if cur.CancelRequested || !ActiveStates[cur.State] {
		return
	}
	cur.State = StateFinalizing
	cur.Stage = StagePublishing
	cur.Progress = ""
	if err := s.save(cur); err != nil {
		return
	}

	root, err := s.resolveRoot(cur.RootPath)
	if err != nil {
		s.finalizeOutcomeLocked(op, cur, StateFailed, FailureCleanupPending, "root no longer clone-eligible at publication", nil)
		return
	}
	handle, err := openRootHandle(root.Resolved)
	if err != nil {
		s.finalizeOutcomeLocked(op, cur, StateFailed, FailureCleanupPending, "root could not be opened at publication", nil)
		return
	}
	defer func() { handle.Close() }()
	if handle.id != (DirIdentity{Device: cur.RootDevice, Inode: cur.RootInode}) {
		s.finalizeOutcomeLocked(op, cur, StateFailed, FailureCleanupPending, "root identity changed at publication", nil)
		return
	}
	staging := stagingNameForRecord(cur)
	info, err := handle.root.Lstat(staging)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		s.finalizeOutcomeLocked(op, cur, StateFailed, FailureCleanupPending, "staging is not an owned directory", nil)
		return
	}
	workName := staging + "/" + stagingWorkDir
	workInfo, err := handle.root.Lstat(workName)
	if err != nil || !workInfo.IsDir() || workInfo.Mode()&os.ModeSymlink != 0 {
		s.finalizeOutcomeLocked(op, cur, StateFailed, FailureCleanupPending, "staged clone is missing", nil)
		return
	}
	data, err := handle.root.ReadFile(filepath.Join(staging, ownershipMarkerName))
	if err != nil {
		s.finalizeOutcomeLocked(op, cur, StateFailed, FailureCleanupPending, "staging ownership marker unreadable", nil)
		return
	}
	var marker ownershipMarker
	if err := json.Unmarshal(data, &marker); err != nil ||
		marker.OperationID != cur.ID || marker.Nonce != cur.OwnershipNonce ||
		marker.RootDevice != cur.RootDevice || marker.RootInode != cur.RootInode {
		s.finalizeOutcomeLocked(op, cur, StateFailed, FailureCleanupPending, "staging ownership could not be verified", nil)
		return
	}
	if _, err := handle.root.Lstat(cur.Destination); err == nil {
		// Another actor won the destination race; their data stays intact.
		s.finalizeOutcomeLocked(op, cur, StateFailed, FailureDestinationConflict, "destination occupied at publication", nil)
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		s.finalizeOutcomeLocked(op, cur, StateFailed, FailureCleanupPending, "destination could not be inspected", nil)
		return
	}
	pubMarker, err := s.marshalPublicationMarker(cur)
	if err != nil {
		s.finalizeOutcomeLocked(op, cur, StateFailed, FailureCleanupPending, "publication marker", nil)
		return
	}
	if err := handle.root.WriteFile(filepath.Join(workName, ".git", publicationMarkerName), pubMarker, 0o600); err != nil {
		s.finalizeOutcomeLocked(op, cur, StateFailed, FailureCleanupPending, "publication marker write failed", nil)
		return
	}
	err = RenameNoReplaceDir(handle.file, workName, cur.Destination)
	switch {
	case err == nil:
	case errors.Is(err, ErrDestinationExists):
		s.finalizeOutcomeLocked(op, cur, StateFailed, FailureDestinationConflict, "destination occupied at publication", nil)
		return
	case errors.Is(err, ErrNoReplaceUnsupported):
		s.finalizeOutcomeLocked(op, cur, StateFailed, FailurePublicationUnsupported, "filesystem lacks atomic no-replace rename", nil)
		return
	default:
		s.finalizeOutcomeLocked(op, cur, StateFailed, FailureCleanupPending, "atomic publication failed", nil)
		return
	}

	// The repository moved out of the staging wrapper; the wrapper (now
	// holding only the ownership marker) is removed through the same
	// verified root handle. A crash here leaves a harmless empty wrapper
	// that recovery removes alongside the publication evidence.
	_ = handle.root.RemoveAll(staging)

	// The rename succeeded: publication is the irreversible success
	// boundary. Retain published data whatever happens next.
	s.recordSuccessLocked(cur)
}

// recordSuccessLocked durably records success from publication evidence.
// A persistence failure keeps the truthful finalizing state; the
// repository stays published and a later reconciliation records success.
func (s *Service) recordSuccessLocked(cur *Record) {
	pub := s.publicationIdentity(cur)
	now := s.now().UTC()
	cur.State = StateSucceeded
	cur.Stage = StageDone
	cur.Progress = ""
	cur.Error = nil
	cur.CleanupIssue = ""
	cur.PendingOutcome = ""
	cur.Process = nil
	cur.Published = &pub
	cur.TerminalAt = now
	cur.ResolvedAt = now
	if err := s.save(cur); err != nil {
		return
	}
	if s.opts.Hooks.WorkspaceChanged != nil {
		s.opts.Hooks.WorkspaceChanged()
	}
}

// Cancel requests explicit cancellation. It returns the authoritative
// snapshot with cancellation requested — never an immediate claim of
// cancelled — and is idempotent. A terminal record is returned unchanged;
// a delayed cancel never removes or reclassifies a published success.
func (s *Service) Cancel(id string) (Record, error) {
	rec, ok := s.store.get(id)
	if !ok {
		return Record{}, serviceError(CodeNotFound, "operation not found")
	}
	op := s.op(id)
	op.mu.Lock()
	defer op.mu.Unlock()
	fresh, ok := s.store.get(id)
	if !ok {
		return Record{}, serviceError(CodeNotFound, "operation not found")
	}
	if TerminalStates[fresh.State] {
		return fresh, nil
	}
	if fresh.CancelRequested {
		return fresh, nil
	}
	fresh.CancelRequested = true
	fresh.CancelRequestedAt = s.now().UTC()
	fresh.State = StateCancelling
	fresh.Stage = StageCancelling
	if err := s.save(&fresh); err != nil {
		return rec, serviceError(CodeInternal, "persist cancellation request")
	}
	op.signalCancel()
	if !op.hasWorker {
		// A recovered operation has no live worker: finish the
		// cancellation asynchronously through the same reconciliation
		// boundary.
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			op.mu.Lock()
			defer op.mu.Unlock()
			cur, ok := s.store.get(id)
			if !ok || !ActiveStates[cur.State] {
				return
			}
			s.reconcileActiveLocked(op, &cur)
		}()
	}
	return fresh, nil
}

// Cleanup resolves a cleanup-pending attempt by re-running the same
// reconciliation boundary as startup: recheck process liveness and
// identity, publication evidence, root identity and staging ownership
// before deleting anything. Already-completed cleanup is a harmless
// no-op; cleanup against active or succeeded work returns the snapshot
// without deleting data or cancelling anything.
func (s *Service) Cleanup(id string) (Record, error) {
	_, ok := s.store.get(id)
	if !ok {
		return Record{}, serviceError(CodeNotFound, "operation not found")
	}
	op := s.op(id)
	op.mu.Lock()
	defer op.mu.Unlock()
	fresh, ok := s.store.get(id)
	if !ok {
		return Record{}, serviceError(CodeNotFound, "operation not found")
	}
	if fresh.State != StateCleanupPending {
		return fresh, nil
	}
	fresh.CleanupAttempts++
	s.reconcileCleanupPendingLocked(op, &fresh)
	out, _ := s.store.get(id)
	return out, nil
}

// Retry starts a deliberate fresh attempt for a terminal failed,
// cancelled or interrupted operation whose cleanup completed. It creates a
// new operation with a new idempotency key, retains the predecessor's
// history, and revalidates current authorization and destination
// conditions through the normal start boundary.
func (s *Service) Retry(id string) (Record, error) {
	rec, ok := s.store.get(id)
	if !ok {
		return Record{}, serviceError(CodeHistoryUnavailable, "operation history is unavailable")
	}
	if recordKind(&rec) == KindCreate {
		// Create attempts are short and synchronous; a deliberate retry is
		// a fresh Create request through the normal boundary.
		return Record{}, serviceError(CodeNotRetryable, "create operations do not use clone retry")
	}
	switch rec.State {
	case StateFailed, StateCancelled, StateInterrupted:
	default:
		detail := "retry is unavailable while the operation is " + string(rec.State)
		if rec.State == StateCleanupPending {
			detail = "retry is blocked until cleanup is resolved"
		}
		return Record{}, serviceError(CodeNotRetryable, detail)
	}
	input := StartInput{
		Remote:         rec.RemoteURL,
		Root:           rec.RootPath,
		Destination:    rec.Destination,
		IdempotencyKey: "retry-" + newNonce(),
	}
	return s.Start(context.Background(), input)
}

func (s *Service) marshalPublicationMarker(cur *Record) ([]byte, error) {
	return json.Marshal(publicationMarker{
		OperationID: cur.ID,
		Nonce:       cur.OwnershipNonce,
		PublishedAt: s.now().UTC(),
		Identity:    stagedPublicationIdentity(cur),
	})
}

// stagedPublicationIdentity pins, before the atomic rename, the identity the
// published repository will carry at its destination: the canonical
// destination path and its .git directory, with the filesystem identity of
// the staged .git directory (the rename preserves it). A nil result leaves
// the publication without provable identity rather than guessing.
func stagedPublicationIdentity(cur *Record) *PublicationIdentity {
	workGit := filepath.Join(cur.StagingPath, stagingWorkDir, ".git")
	var stat unix.Stat_t
	if err := unix.Stat(workGit, &stat); err != nil {
		return nil
	}
	dest := filepath.Clean(cur.DestinationPath)
	return &PublicationIdentity{
		Path:      dest,
		CommonDir: filepath.Join(dest, ".git"),
		Device:    strconv.FormatUint(uint64(stat.Dev), 10),
		Inode:     strconv.FormatUint(uint64(stat.Ino), 10),
	}
}
