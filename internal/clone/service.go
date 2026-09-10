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
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/workspace"
)

// Service error codes surfaced to the server layer, which maps them onto
// canonical errors. Codes are stable API vocabulary.
const (
	CodeNotFound            = "clone_not_found"
	CodeHistoryUnavailable  = "clone_history_unavailable"
	CodeIdempotencyConflict = "clone_idempotency_conflict"
	CodeDestinationReserved = "clone_destination_reserved"
	CodeDestinationExists   = "clone_destination_exists"
	CodeDestinationShadowed = "clone_destination_shadowed"
	CodeRootIneligible      = "clone_root_ineligible"
	CodeNotRetryable        = "clone_not_retryable"
	CodeUnavailable         = "clone_unavailable"
	CodeInternal            = "clone_internal"
)

// ServiceError is a typed service failure the server layer maps onto a
// canonical error code with an HTTP status.
type ServiceError struct {
	Code   string
	Detail string
}

func (e *ServiceError) Error() string {
	return fmt.Sprintf("clone service error: %s", e.Code)
}

func serviceError(code, detail string) error {
	return &ServiceError{Code: code, Detail: detail}
}

// Hooks let the server publish SSE invalidations when operation or
// workspace state changes.
type Hooks struct {
	// OperationChanged fires on every persisted operation change, with the
	// operation's kind so clone and create changes can be routed apart.
	OperationChanged func(kind, operationID string)
	// WorkspaceChanged fires when discovery-visible state changed (a
	// successful publication).
	WorkspaceChanged func()
}

// Options configures a Service.
type Options struct {
	// StateDir is the runtime state dir; records live beneath it in
	// StoreDirName.
	StateDir string
	// Config returns the authoritative runtime config (workspace roots,
	// explicit repository registrations).
	Config func() *config.Config
	// Runner overrides the git clone runner (tests inject fakes).
	Runner Runner
	// CreateGit overrides the repository-creation executor used by create
	// operations (tests inject fakes). It receives the staged work
	// directory and must leave a fresh repository with one empty initial
	// commit on main inside it.
	CreateGit func(ctx context.Context, dir string) error
	// Deadline bounds one clone execution. Zero means DefaultDeadline.
	Deadline time.Duration
	// Now overrides the clock (tests inject deterministic time).
	Now func() time.Time
	// Hooks receive SSE invalidation callbacks.
	Hooks Hooks
	// GitBin overrides the git binary (rarely needed).
	GitBin string
}

// StartInput is one clone start request.
type StartInput struct {
	Remote         string
	Root           string
	Destination    string
	IdempotencyKey string
}

// Service owns the clone lifecycle for one server process.
type Service struct {
	opts     Options
	store    *store
	runner   Runner
	deadline time.Duration
	now      func() time.Time

	// startMu serializes the reservation critical section (idempotency,
	// destination reservation, staging creation, durable acceptance) so
	// concurrent starts cannot double-reserve.
	startMu sync.Mutex

	mu       sync.Mutex
	workers  map[string]*opState
	draining bool
	wg       sync.WaitGroup
}

// opState coordinates one operation's worker, cancellation and
// reconciliation. mu serializes state transitions; cancelCh signals
// workers; hasWorker distinguishes live workers from recovered records.
type opState struct {
	mu         sync.Mutex
	cancelCh   chan struct{}
	cancelOnce sync.Once
	hasWorker  bool
}

func (o *opState) signalCancel() {
	o.cancelOnce.Do(func() { close(o.cancelCh) })
}

func (o *opState) cancelSignaled() bool {
	select {
	case <-o.cancelCh:
		return true
	default:
		return false
	}
}

// New builds a Service, loading durable records from the state dir.
func New(opts Options) (*Service, error) {
	if strings.TrimSpace(opts.StateDir) == "" {
		return nil, errors.New("clone service requires a state dir")
	}
	if opts.Config == nil {
		opts.Config = func() *config.Config { return &config.Config{} }
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	runner := opts.Runner
	if runner == nil {
		runner = NewRealRunner(opts.GitBin)
	}
	deadline := opts.Deadline
	if deadline <= 0 {
		deadline = DefaultDeadline
	}
	st, err := newStore(filepath.Join(opts.StateDir, StoreDirName), opts.Now)
	if err != nil {
		return nil, err
	}
	return &Service{
		opts:     opts,
		store:    st,
		runner:   runner,
		deadline: deadline,
		now:      opts.Now,
		workers:  map[string]*opState{},
	}, nil
}

// op returns (creating if needed) the coordination state for an operation.
func (s *Service) op(id string) *opState {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.workers[id]
	if !ok {
		o = &opState{cancelCh: make(chan struct{})}
		s.workers[id] = o
	}
	return o
}

// drainingNow reports whether the service stopped accepting clone work.
func (s *Service) drainingNow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.draining
}

// save persists the record and fires the operation-changed hook. The
// caller holds the operation lock (or is inside the start critical
// section).
func (s *Service) save(rec *Record) error {
	rec.UpdatedAt = s.now().UTC()
	if err := s.store.save(rec); err != nil {
		return err
	}
	if s.opts.Hooks.OperationChanged != nil {
		s.opts.Hooks.OperationChanged(rec.Kind, rec.ID)
	}
	return nil
}

// config returns the authoritative runtime config snapshot.
func (s *Service) config() *config.Config {
	return s.opts.Config()
}

// newID mints an unguessable operation ID prefixed by its kind.
func newID(kind string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return kind + "-" + fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return kind + "-" + hex.EncodeToString(b[:])
}

func newNonce() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return hex.EncodeToString([]byte("fallbacknonce"))
	}
	return hex.EncodeToString(b[:])
}

// inputFingerprint binds an idempotency key to its exact input.
func inputFingerprint(remote, rootResolved, destination string) string {
	sum := sha256.Sum256([]byte(remote + "\x00" + rootResolved + "\x00" + destination))
	return hex.EncodeToString(sum[:16])
}

// resolvedRoot is a canonical workspace root with its filesystem identity.
type resolvedRoot struct {
	Configured string
	Resolved   string
	Identity   DirIdentity
}

// resolveRoot resolves the requested root against the current config and
// verifies clone eligibility (existing writable non-repository directory).
func (s *Service) resolveRoot(requested string) (resolvedRoot, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return resolvedRoot{}, serviceError(CodeRootIneligible, "root is required")
	}
	if len(requested) > MaxRootPathLength {
		return resolvedRoot{}, serviceError(CodeRootIneligible, "root path is too long")
	}
	for _, configured := range s.config().WorkspaceRoots {
		expanded := workspace.ExpandHome(configured)
		abs, err := filepath.Abs(expanded)
		if err != nil {
			continue
		}
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			continue
		}
		if requested != configured && requested != expanded && requested != abs && requested != resolved {
			continue
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.IsDir() {
			continue
		}
		if workspace.IsGitRepo(resolved) {
			continue
		}
		dir, identity, err := OpenRootDir(resolved)
		if err != nil {
			continue
		}
		root := resolvedRoot{Configured: configured, Resolved: resolved, Identity: identity}
		if err := s.probeWritable(dir); err != nil {
			_ = dir.Close()
			continue
		}
		_ = dir.Close()
		return root, nil
	}
	return resolvedRoot{}, serviceError(CodeRootIneligible, "no matching clone-eligible workspace root")
}

// probeWritable verifies write access by creating and removing an
// exclusively-named temporary file through the pinned root handle.
func (s *Service) probeWritable(dir *os.File) error {
	name := ".agentico-probe-" + newNonce()
	fd, err := unixOpenat(int(dir.Fd()), name)
	if err != nil {
		return err
	}
	_ = os.NewFile(uintptr(fd), name).Close()
	return unixUnlinkat(int(dir.Fd()), name)
}

// rootHandle bundles the pinned directory file (for identity-bound
// no-replace rename) and an os.Root view (for identity-bound tree
// operations) of the same directory.
type rootHandle struct {
	file *os.File
	root *os.Root
	id   DirIdentity
}

func (h *rootHandle) Close() {
	if h.root != nil {
		_ = h.root.Close()
	}
	if h.file != nil {
		_ = h.file.Close()
	}
}

// openRootHandle opens the resolved root twice: a raw fd for renameat and
// an os.Root for tree operations. Both pin the directory inode.
func openRootHandle(resolved string) (*rootHandle, error) {
	file, id, err := OpenRootDir(resolved)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(resolved)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return &rootHandle{file: file, root: root, id: id}, nil
}

// ownershipMarker is the durable staging ownership evidence.
type ownershipMarker struct {
	OperationID string    `json:"operation_id"`
	Nonce       string    `json:"nonce"`
	RootDevice  uint64    `json:"root_device"`
	RootInode   uint64    `json:"root_inode"`
	CreatedAt   time.Time `json:"created_at"`
}

// publicationMarker is the durable publication evidence written into the
// staged repository's .git directory before the atomic rename, so a crash
// between rename and the success write can be reconciled later.
type publicationMarker struct {
	OperationID string               `json:"operation_id"`
	Nonce       string               `json:"nonce"`
	PublishedAt time.Time            `json:"published_at"`
	Identity    *PublicationIdentity `json:"identity,omitempty"`
}

const (
	ownershipMarkerName   = ".agentico-ownership.json"
	publicationMarkerName = "agentico-publication.json"
	stagingPrefix         = ".agentico-"
	// stagingWorkDir is the repository directory inside the staging
	// directory: git requires an empty target, so the ownership marker
	// lives at the staging root and the repository clones (or is created)
	// into this child.
	stagingWorkDir = "work"
)

// stagingNameFor is the hidden staging child name inside the root for an
// operation of the given kind. Kinds share the reservation space but never
// share staging directories.
func stagingNameFor(kind, id string) string {
	return stagingPrefix + kind + "-" + strings.TrimPrefix(strings.TrimPrefix(id, "clone-"), "create-")
}

// stagingName is the clone-kind staging name (legacy records have no kind).
func stagingName(id string) string {
	return stagingNameFor(KindClone, id)
}

// recordKind normalizes a record's kind: records written before kinds
// existed are clone records.
func recordKind(rec *Record) string {
	if rec.Kind == "" {
		return KindClone
	}
	return rec.Kind
}

// stagingNameForRecord is the record-driven staging name, tolerant of
// legacy records without a kind.
func stagingNameForRecord(rec *Record) string {
	return stagingNameFor(recordKind(rec), rec.ID)
}

// Start validates and durably accepts a clone request, then spawns the
// worker. The accepted snapshot returns promptly, independent of transfer
// duration; closing the calling context does not own or cancel the worker.
func (s *Service) Start(ctx context.Context, input StartInput) (Record, error) {
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
	if err := ValidateCloneRemote(input.Remote); err != nil {
		detail := ""
		if errors.As(err, &verr) {
			detail = verr.Detail
		}
		return Record{}, serviceError(ValidationRemoteInvalid, RedactRemote(input.Remote)+" "+detail)
	}
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
	fp := inputFingerprint(input.Remote, root.Resolved, input.Destination)

	rec, replayed, err := s.acceptOperation(acceptRequest{
		kind:             KindClone,
		root:             root,
		destination:      input.Destination,
		destinationPath:  destinationPath,
		idempotencyKey:   input.IdempotencyKey,
		inputFingerprint: fp,
		remoteURL:        RedactRemote(input.Remote),
	})
	if err != nil {
		return Record{}, err
	}
	if replayed {
		return rec, nil
	}
	s.spawnWorker(rec)
	return rec.Snapshot(), nil
}

// acceptRequest is one durable-acceptance request. Only the kind and the
// kind-specific remote fields differ between a clone start and a create;
// everything else about accepting an operation is identical, so both
// boundaries share acceptOperation.
type acceptRequest struct {
	kind             string
	root             resolvedRoot
	destination      string
	destinationPath  string
	idempotencyKey   string
	inputFingerprint string
	// remoteURL is the already-redacted clone remote, recorded verbatim.
	// Kinds without a remote leave it empty.
	remoteURL string
	// replayRequiresSameKind makes an idempotency-key replay against a
	// record of another kind a conflict rather than a replay. Fingerprint
	// spaces are disjoint across kinds today, so this only matters if that
	// ever stops holding.
	replayRequiresSameKind bool
}

// acceptOperation runs the reservation critical section shared by clone
// start and create: idempotency replay, destination reservation,
// destination-safety checks, owned staging creation and durable acceptance.
// It holds startMu for the whole section so concurrent acceptances of either
// kind cannot double-reserve a destination, and it returns before any git
// execution — from here on the durable accepted record holds the
// destination reservation.
//
// A true second result means the returned record is a retained prior attempt
// replayed for its idempotency key, whatever its state, not a fresh
// acceptance the caller should drive.
func (s *Service) acceptOperation(req acceptRequest) (Record, bool, error) {
	s.startMu.Lock()
	defer s.startMu.Unlock()

	if existing, ok := s.store.getByIdempotencyKey(req.idempotencyKey); ok {
		sameKind := !req.replayRequiresSameKind || recordKind(&existing) == req.kind
		if sameKind && existing.InputFingerprint == req.inputFingerprint {
			return existing, true, nil
		}
		return Record{}, false, serviceError(CodeIdempotencyConflict, "idempotency key was already used with different input")
	}
	if holder, ok := s.store.reservation(req.destinationPath); ok {
		return Record{}, false, serviceError(CodeDestinationReserved, "destination is reserved by operation "+holder.ID)
	}
	if _, err := os.Lstat(req.destinationPath); err == nil {
		return Record{}, false, serviceError(CodeDestinationExists, "destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Record{}, false, serviceError(CodeInternal, "inspect destination")
	}
	for name := range s.config().Repos {
		if name == req.destination {
			return Record{}, false, serviceError(CodeDestinationShadowed, "an explicit repository registration already uses this name")
		}
	}

	id := newID(req.kind)
	nonce := newNonce()
	handle, err := openRootHandle(req.root.Resolved)
	if err != nil {
		return Record{}, false, serviceError(CodeInternal, "open root")
	}
	staging := stagingNameFor(req.kind, id)
	if _, err := handle.root.Lstat(staging); err == nil {
		handle.Close()
		return Record{}, false, serviceError(CodeInternal, "staging path collision")
	} else if !errors.Is(err, os.ErrNotExist) {
		handle.Close()
		return Record{}, false, serviceError(CodeInternal, "inspect staging path")
	}
	if err := handle.root.Mkdir(staging, 0o700); err != nil {
		handle.Close()
		return Record{}, false, serviceError(CodeInternal, "create staging")
	}
	marker, err := json.Marshal(ownershipMarker{
		OperationID: id, Nonce: nonce,
		RootDevice: req.root.Identity.Device, RootInode: req.root.Identity.Inode,
		CreatedAt: s.now().UTC(),
	})
	if err == nil {
		err = handle.root.WriteFile(filepath.Join(staging, ownershipMarkerName), marker, 0o600)
	}
	if err != nil {
		// Persistence failure of the ownership marker starts no git and
		// releases only the staging we just created and still own.
		_ = handle.root.RemoveAll(staging)
		handle.Close()
		return Record{}, false, serviceError(CodeInternal, "write staging ownership marker")
	}
	handle.Close()

	now := s.now().UTC()
	rec := Record{
		ID:               id,
		Kind:             req.kind,
		IdempotencyKey:   req.idempotencyKey,
		InputFingerprint: req.inputFingerprint,
		RemoteURL:        req.remoteURL,
		RootPath:         req.root.Configured,
		RootResolved:     req.root.Resolved,
		RootDevice:       req.root.Identity.Device,
		RootInode:        req.root.Identity.Inode,
		Destination:      req.destination,
		DestinationPath:  req.destinationPath,
		StagingPath:      filepath.Join(req.root.Resolved, staging),
		OwnershipNonce:   nonce,
		State:            StateAccepted,
		Stage:            StagePreparing,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	// Durable acceptance precedes any git process: a crash between here and
	// publication is recoverable.
	if err := s.save(&rec); err != nil {
		s.releaseFreshStaging(&rec)
		return Record{}, false, serviceError(CodeInternal, "persist accepted operation")
	}
	return rec, false, nil
}

// releaseFreshStaging removes staging this process just created inside its
// own critical section (before any worker existed). It is only used when
// acceptance persistence fails.
func (s *Service) releaseFreshStaging(rec *Record) {
	handle, err := openRootHandle(rec.RootResolved)
	if err != nil {
		return
	}
	defer func() { handle.Close() }()
	if !SameDirIdentity(handle.file, DirIdentity{Device: rec.RootDevice, Inode: rec.RootInode}) {
		return
	}
	_ = handle.root.RemoveAll(stagingNameForRecord(rec))
}

// Snapshot returns the authoritative record.
func (s *Service) Snapshot(id string) (Record, error) {
	rec, ok := s.store.get(id)
	if !ok {
		return Record{}, serviceError(CodeNotFound, "operation not found")
	}
	return rec, nil
}

// List returns one bounded page of operations.
func (s *Service) List(q ListQuery) (ListResult, error) {
	return s.store.list(q), nil
}

// Sweep prunes resolved terminal records past the retention window. It
// removes only operation metadata, never repositories or unresolved
// staging.
func (s *Service) Sweep() []string {
	return s.store.prune(s.now())
}

// publicationIdentity computes the actual collision-safe catalog key for
// the published repository by re-running workspace discovery against the
// current config, plus whether the repository has commits.
func (s *Service) publicationIdentity(rec *Record) Publication {
	cfg := s.config()
	explicit := map[string]string{}
	for name, rc := range cfg.Repos {
		explicit[name] = rc.Path
	}
	key := rec.Destination
	for _, repo := range workspace.DiscoverReposFromRoots(cfg.WorkspaceRoots, explicit) {
		if sameResolvedPath(repo.Path, rec.DestinationPath) {
			key = repo.Name
			break
		}
	}
	pub := Publication{
		RepoKey:     key,
		Path:        rec.DestinationPath,
		HasHead:     git.HasHead(rec.DestinationPath),
		PublishedAt: s.now().UTC(),
	}
	// The publication identity is only provable when the repository now at
	// the destination is the one the marker pinned before the rename: a
	// replacement at the same path never inherits an old success.
	if markerIdentity := s.publicationMarkerIdentity(rec); markerIdentity != nil {
		if identity, ok := git.ResolveRepoIdentity(rec.DestinationPath); ok && publicationIdentityEqual(markerIdentity, identity) {
			pub.Identity = wirePublicationIdentity(identity)
		}
	}
	return pub
}

// publicationMarkerIdentity reads the durable publication marker at the
// destination and returns the identity it pinned, if any.
func (s *Service) publicationMarkerIdentity(rec *Record) *PublicationIdentity {
	data, err := os.ReadFile(filepath.Join(rec.DestinationPath, ".git", publicationMarkerName))
	if err != nil {
		return nil
	}
	var marker publicationMarker
	if json.Unmarshal(data, &marker) != nil {
		return nil
	}
	return marker.Identity
}

// publicationIdentityEqual compares a pinned marker identity with a fresh
// server-resolved identity.
func publicationIdentityEqual(pinned *PublicationIdentity, fresh git.RepoIdentity) bool {
	if pinned == nil {
		return false
	}
	return pinned.Path == fresh.Path &&
		pinned.CommonDir == fresh.CommonDir &&
		pinned.Device == strconv.FormatUint(fresh.Device, 10) &&
		pinned.Inode == strconv.FormatUint(fresh.Inode, 10) && pinned.BirthTime == fresh.BirthTime
}

func wirePublicationIdentity(identity git.RepoIdentity) *PublicationIdentity {
	return &PublicationIdentity{
		Path:      identity.Path,
		CommonDir: identity.CommonDir,
		Device:    strconv.FormatUint(identity.Device, 10),
		Inode:     strconv.FormatUint(identity.Inode, 10),
		BirthTime: identity.BirthTime,
	}
}

func sameResolvedPath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA == nil && errB == nil {
		return ra == rb
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// attemptCleanup deletes verified owned incomplete staging. It refuses to
// touch anything whose ownership, root identity or shape cannot be proved:
// a path, PID or marker alone is never sufficient. Returns whether staging
// is gone and, when not, the canonical reason.
func (s *Service) attemptCleanup(rec *Record) (bool, string) {
	root, err := s.resolveRoot(rec.RootPath)
	if err != nil {
		return false, "configured root is no longer clone-eligible"
	}
	handle, err := openRootHandle(root.Resolved)
	if err != nil {
		return false, "root directory could not be opened"
	}
	defer func() { handle.Close() }()
	if handle.id != (DirIdentity{Device: rec.RootDevice, Inode: rec.RootInode}) {
		return false, "root identity changed"
	}
	staging := stagingNameForRecord(rec)
	info, err := handle.root.Lstat(staging)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, ""
		}
		return false, "staging could not be inspected"
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, "staging is not an owned directory"
	}
	data, err := handle.root.ReadFile(filepath.Join(staging, ownershipMarkerName))
	if err != nil {
		return false, "staging ownership marker could not be read"
	}
	var marker ownershipMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return false, "staging ownership marker is malformed"
	}
	if marker.OperationID != rec.ID || marker.Nonce != rec.OwnershipNonce ||
		marker.RootDevice != rec.RootDevice || marker.RootInode != rec.RootInode {
		return false, "staging ownership could not be verified"
	}
	if err := handle.root.RemoveAll(staging); err != nil {
		return false, "staging removal failed"
	}
	return true, ""
}

// hasPublicationEvidence reports whether the destination currently holds
// this attempt's durable publication evidence: the repository at the
// destination with our publication marker. An unrelated repository merely
// occupying the destination is never reported as this attempt's success.
func (s *Service) hasPublicationEvidence(rec *Record) bool {
	info, err := os.Lstat(rec.DestinationPath)
	if err != nil || !info.IsDir() {
		return false
	}
	data, err := os.ReadFile(filepath.Join(rec.DestinationPath, ".git", publicationMarkerName))
	if err != nil {
		return false
	}
	var marker publicationMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return false
	}
	return marker.OperationID == rec.ID && marker.Nonce == rec.OwnershipNonce
}
