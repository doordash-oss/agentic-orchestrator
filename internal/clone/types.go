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

// Package clone owns the server-side repository clone lifecycle: durable
// operation records, hidden per-attempt staging under the destination root,
// bounded git execution, atomic no-replace publication, cancellation,
// startup recovery, cleanup resolution and explicit retry. Every state
// transition is persisted before it is observable, so snapshots are always
// authoritative and never inferred from transport or git text.
package clone

import "time"

// State is the authoritative lifecycle state of a clone operation. States
// are persisted; snapshots expose them verbatim. Success is only ever
// reported from a durable succeeded record — never from stream completion,
// transport completion or git output.
type State string

const (
	// StateAccepted means the request was durably recorded (with its
	// reservation and staging) before any git process was spawned.
	StateAccepted State = "accepted"
	// StateRunning means a git clone process is executing against staging.
	StateRunning State = "running"
	// StateFinalizing means the transfer finished and publication is in
	// progress. The record may stay in this state across a crash between
	// the atomic rename and the success write.
	StateFinalizing State = "finalizing"
	// StateCancelling means explicit cancellation was requested; the
	// operation stays in this state until the process tree is confirmed
	// stopped and cleanup completes.
	StateCancelling State = "cancelling"
	// StateSucceeded is the terminal success state, set only after the
	// atomic publication and its durable publication evidence.
	StateSucceeded State = "succeeded"
	// StateFailed is a terminal failure with verified completed cleanup.
	StateFailed State = "failed"
	// StateCancelled is a terminal explicit cancellation with verified
	// completed cleanup.
	StateCancelled State = "cancelled"
	// StateInterrupted is a terminal server interruption (shutdown or
	// crash) with verified completed cleanup.
	StateInterrupted State = "interrupted"
	// StateCleanupPending means the attempt reached a failure/cancellation/
	// interruption outcome but cleanup could not yet be proved safe. The
	// destination reservation is retained until cleanup resolves.
	StateCleanupPending State = "cleanup_pending"
)

// TerminalStates are the states that describe an attempt outcome (including
// the unresolved cleanup_pending outcome holder).
var TerminalStates = map[State]bool{
	StateSucceeded:      true,
	StateFailed:         true,
	StateCancelled:      true,
	StateInterrupted:    true,
	StateCleanupPending: true,
}

// UnresolvedStates hold a destination reservation.
var UnresolvedStates = map[State]bool{
	StateAccepted:       true,
	StateRunning:        true,
	StateFinalizing:     true,
	StateCancelling:     true,
	StateCleanupPending: true,
}

// ActiveStates describe work that may still have a live process.
var ActiveStates = map[State]bool{
	StateAccepted:   true,
	StateRunning:    true,
	StateFinalizing: true,
	StateCancelling: true,
}

// ResolvedStates are terminal states whose cleanup (if any) completed and
// whose retention window (from ResolvedAt) applies.
var ResolvedStates = map[State]bool{
	StateSucceeded:   true,
	StateFailed:      true,
	StateCancelled:   true,
	StateInterrupted: true,
}

// Input bounds. Every submitted string is bounded before it is recorded or
// acted on.
const (
	MaxRemoteURLLength   = 2048
	MaxDestinationLength = 128
	MaxIdempotencyLength = 128
	MaxRootPathLength    = 1024
	MaxStageLength       = 64
	MaxProgressLength    = 256
	MaxDiagnosticsLength = 2000
	maxOutputRingBytes   = 32 * 1024
	progressIntervalMin  = 500 * time.Millisecond
	DefaultDeadline      = 30 * time.Minute
	// RetentionWindow is how long resolved terminal records are kept after
	// resolution. Active and cleanup-pending records are always retained.
	RetentionWindow = 7 * 24 * time.Hour
	// DefaultListLimit bounds one list page.
	DefaultListLimit = 50
	MaxListLimit     = 200
)

// Operation kinds. The clone lifecycle machinery (records, reservations,
// staging, publication, cleanup, recovery) is shared by both kinds; only
// the worker differs. KindClone is the default so records written before
// kinds existed keep their original meaning.
const (
	KindClone  = "clone"
	KindCreate = "create"
)

// OpError is the bounded, redacted failure detail carried on a record. It
// never contains raw git output or secret-bearing input.
type OpError struct {
	Code        string `json:"code"`
	Diagnostics string `json:"diagnostics,omitempty"`
}

// PublicationIdentity is the server-resolved repository identity of the
// repository actually published, pinned by the publication marker before the
// atomic rename. Device and Inode are decimal text so the comparison stays
// exact in every client language. It is absent when the identity could not
// be proved (older records, or a destination whose repository no longer
// matches the marker), and clients must never fall back to key or path.
type PublicationIdentity struct {
	Path      string `json:"path"`
	CommonDir string `json:"common_dir"`
	Device    string `json:"device"`
	Inode     string `json:"inode"`
	BirthTime string `json:"birth_time,omitempty"`
}

// Publication is the durable evidence of a successful atomic publication.
// RepoKey is the actual collision-safe catalog key computed from discovery
// after publication, never the requested folder name. Identity binds the
// publication to the repository that was actually published.
type Publication struct {
	RepoKey     string               `json:"repo_key"`
	Path        string               `json:"path"`
	HasHead     bool                 `json:"has_head"`
	PublishedAt time.Time            `json:"published_at"`
	Identity    *PublicationIdentity `json:"identity,omitempty"`
}

// ProcessInfo records the spawned worker identity. PID alone is never
// treated as proof of ownership; StartIdentity (the OS process start
// identity) is compared before any recovery-time signaling or cleanup.
type ProcessInfo struct {
	Pid           int       `json:"pid"`
	Pgid          int       `json:"pgid"`
	StartIdentity string    `json:"start_identity,omitempty"`
	StartedAt     time.Time `json:"started_at"`
}

// Record is the durable clone operation record. It is the single authority
// for snapshots, reservations, idempotency and recovery.
type Record struct {
	ID string `json:"id"`
	// Kind separates clone operations from create operations; both share
	// this record shape, reservation space and publication boundary. Empty
	// means a legacy clone record.
	Kind             string `json:"kind,omitempty"`
	IdempotencyKey   string `json:"idempotency_key"`
	InputFingerprint string `json:"input_fingerprint"`
	RemoteURL        string `json:"remote_url"`
	RootPath         string `json:"root_path"`
	RootResolved     string `json:"root_resolved"`
	// RootDevice and RootInode pin the canonical root directory identity
	// captured at reservation; identity-bound publication and cleanup
	// re-verify it so a path or symlink swap cannot redirect them.
	RootDevice      uint64 `json:"root_device"`
	RootInode       uint64 `json:"root_inode"`
	Destination     string `json:"destination"`
	DestinationPath string `json:"destination_path"`
	StagingPath     string `json:"staging_path"`
	OwnershipNonce  string `json:"ownership_nonce"`
	State           State  `json:"state"`
	Stage           string `json:"stage,omitempty"`
	Progress        string `json:"progress,omitempty"`
	// PendingOutcome is the failure/cancel/interrupt outcome a
	// cleanup-pending attempt resolves to once cleanup completes.
	PendingOutcome    State     `json:"pending_outcome,omitempty"`
	CancelRequested   bool      `json:"cancel_requested,omitempty"`
	CancelRequestedAt time.Time `json:"cancel_requested_at,omitempty"`
	Error             *OpError  `json:"error,omitempty"`
	// CleanupIssue is the bounded canonical reason cleanup could not yet
	// be proved safe while the operation is cleanup_pending.
	CleanupIssue    string       `json:"cleanup_issue,omitempty"`
	Process         *ProcessInfo `json:"process,omitempty"`
	Published       *Publication `json:"published,omitempty"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
	TerminalAt      time.Time    `json:"terminal_at,omitempty"`
	ResolvedAt      time.Time    `json:"resolved_at,omitempty"`
	CleanupAttempts int          `json:"cleanup_attempts,omitempty"`
}

// Snapshot returns a copy safe to hand to API layers.
func (r *Record) Snapshot() Record {
	if r == nil {
		return Record{}
	}
	copied := *r
	return copied
}

// Stage labels are authored status markers; the renderer shows them
// verbatim. Git text never becomes a stage.
const (
	StagePreparing    = "preparing"
	StageTransferring = "transferring"
	StagePublishing   = "publishing"
	StageFinalizing   = "finalizing"
	StageDone         = "done"
	StageCancelling   = "cancelling"
)
