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

package feature

import (
	"crypto/sha1"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

// DeferralStatus is the lifecycle state of a cross-phase deferral.
type DeferralStatus string

const (
	// DeferralOpen — work has been declared as deferred to a future phase
	// and has not yet landed. The ledger will carry this forward into the
	// plan/implement prompts of DueByPhase and its integrity gate will
	// refuse SUCCESS while the status remains Open.
	DeferralOpen DeferralStatus = "open"

	// DeferralClosed — the deferred work landed; the implementer cited
	// this ID in closed_deferrals: on the iteration that delivered it.
	// Implementation review is expected to verify the diff actually implements
	// the described work.
	DeferralClosed DeferralStatus = "closed"

	// DeferralRedeferred — the deferral was punted to a later phase. A
	// new event is appended to History; DueByPhase is updated to the new
	// target. Status remains Open logically (carry-forward still applies
	// at the new phase); Redeferred is a marker used by TUI surfaces
	// and by the phase-plan validator to flag chronic re-deferrals.
	DeferralRedeferred DeferralStatus = "open_redeferred"
)

// DeferralEventKind enumerates the entries in Deferral.History.
type DeferralEventKind string

const (
	DeferralEventCreated    DeferralEventKind = "created"
	DeferralEventRedeferred DeferralEventKind = "redeferred"
	DeferralEventClosed     DeferralEventKind = "closed"
)

// Deferral records a single cross-phase commitment: "work W was declared
// in phase N and is due by phase M." The ledger lives on Run.Deferrals and
// is carried forward into downstream phase prompts so the agent cannot
// silently drop work across phase boundaries.
//
// The failure mode this closes: a phase's implement output contains a
// prose hedge like "Tailwind will land in Phase 3", no structured record
// is made, Phase 3's plan never sees the commitment, and the deferred
// work cascades through every remaining phase unexecuted.
type Deferral struct {
	ID             string          `yaml:"id"`                        // stable hash of (CreatedInPhase, normalized Description)
	Description    string          `yaml:"description"`               // what was deferred, in a single sentence
	CreatedAt      time.Time       `yaml:"created_at"`                // when the deferral first entered the ledger
	CreatedInPhase int             `yaml:"created_in_phase"`          // phase number where the deferral was declared; 0 = roadmap-level
	CreatedInKind  string          `yaml:"created_in_kind"`           // "roadmap" | "plan" | "implement" | "design"
	DueByPhase     int             `yaml:"due_by_phase"`              // target phase (current value; History preserves prior values)
	Reason         string          `yaml:"reason"`                    // why deferred — required; integrity gate rejects empty reasons
	Status         DeferralStatus  `yaml:"status"`                    // open | closed | open_redeferred
	ClosedAt       *time.Time      `yaml:"closed_at,omitempty"`       // set when Status==closed
	ClosedInPhase  int             `yaml:"closed_in_phase,omitempty"` // phase where the work actually landed
	ClosedInKind   string          `yaml:"closed_in_kind,omitempty"`  // "plan" | "implement" (which artifact declared closure)
	History        []DeferralEvent `yaml:"history,omitempty"`         // append-only audit trail
	// RepoScope optionally restricts the deferral to a subset of the
	// feature's repos. nil/empty means feature-wide (the default).
	// Non-empty means only the listed repos see this entry in their
	// per-repo prompts and only their Report Integrity Gate is blocked
	// by it.
	RepoScope []string `yaml:"repo_scope,omitempty"`
}

// DeferralEvent is a single transition in a deferral's lifecycle.
type DeferralEvent struct {
	At        time.Time         `yaml:"at"`
	Kind      DeferralEventKind `yaml:"kind"`
	FromPhase int               `yaml:"from_phase,omitempty"` // for redeferred: prior DueByPhase
	ToPhase   int               `yaml:"to_phase,omitempty"`   // for redeferred: new DueByPhase; for closed: the phase that landed it
	Reason    string            `yaml:"reason,omitempty"`     // cited reason for this transition (required for redeferred)
}

// DeferralID computes a stable identifier for a deferral based on the phase
// it was created in and a normalized form of its description. The same
// (phase, description) pair across restarts/reparses resolves to the same
// ID, which means ingestion is idempotent — an agent re-emitting an
// already-ingested deferral collapses into the existing entry rather than
// creating a duplicate.
//
// The ID shape is "D-<6-hex-chars>" — compact enough to cite in prompts
// ("address D-a3f1c0") without collision risk at realistic feature sizes.
func DeferralID(createdInPhase int, description string) string {
	normalized := normalizeDeferralDescription(description)
	h := sha1.New()
	_, _ = h.Write([]byte(string(rune(createdInPhase)) + "|" + normalized))
	return "D-" + hex.EncodeToString(h.Sum(nil))[:6]
}

// normalizeDeferralDescription lowercases, trims, and collapses whitespace
// so minor wording drift across iterations doesn't split the same logical
// deferral into two entries.
func normalizeDeferralDescription(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return b.String()
}

// DueForPhase returns the subset of deferrals whose DueByPhase matches the
// given phase and whose status is still open (including open_redeferred).
// Sorted by ID for deterministic rendering in prompts.
func DueForPhase(deferrals []Deferral, phase int) []Deferral {
	var out []Deferral
	for _, d := range deferrals {
		if d.DueByPhase != phase {
			continue
		}
		if d.Status == DeferralClosed {
			continue
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// DueForPhaseScopedTo returns the subset of DueForPhase(deferrals, phase)
// whose RepoScope includes repoName. An empty/nil RepoScope is feature-wide
// (always included). An empty repoName is permissive (no entries filtered
// out) — preserves callers that pre-date per-repo prompt threading.
//
// Sorted by ID for deterministic rendering, same as DueForPhase.
func DueForPhaseScopedTo(deferrals []Deferral, phase int, repoName string) []Deferral {
	due := DueForPhase(deferrals, phase)
	if repoName == "" {
		return due
	}
	out := make([]Deferral, 0, len(due))
	for _, d := range due {
		if len(d.RepoScope) == 0 || containsString(d.RepoScope, repoName) {
			out = append(out, d)
		}
	}
	return out
}

// containsString reports whether s is in ss. Inlined to avoid pulling in
// slices.Contains and to keep the helper available pre-Go-1.21 if the
// toolchain ever drifts.
func containsString(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// sameScope reports whether two RepoScope slices represent the same set.
// Order- and duplicate-insensitive. Empty vs. nil compare equal.
//
// Compares as sets — `["repo-a"]` and `["repo-a","repo-a"]` are equal —
// because RepoScope is conceptually a membership predicate. Storing the
// same logical scope with an extra duplicate must not look like a drift
// to MergeDeferrals (which would then spuriously append a Redeferred
// history event for a no-op re-emit).
func sameScope(a, b []string) bool {
	setA := make(map[string]struct{}, len(a))
	for _, v := range a {
		setA[v] = struct{}{}
	}
	setB := make(map[string]struct{}, len(b))
	for _, v := range b {
		setB[v] = struct{}{}
	}
	if len(setA) != len(setB) {
		return false
	}
	for k := range setA {
		if _, ok := setB[k]; !ok {
			return false
		}
	}
	return true
}

// RedeferralCount reports how many times a deferral has been re-deferred
// (DeferralEventRedeferred entries in History). A count ≥ 2 is a signal
// the deferral keeps slipping and may need human attention; phase-plan
// validators can surface this without blocking.
func (d Deferral) RedeferralCount() int {
	n := 0
	for _, e := range d.History {
		if e.Kind == DeferralEventRedeferred {
			n++
		}
	}
	return n
}

// IncomingDeferral is the shape an agent emits when declaring new or
// updated deferrals — a lighter-weight struct than Deferral, lacking the
// fields the ingester fills in (CreatedAt, History). Parsers in
// internal/agent produce these from progress.md's `## Deferrals` fenced
// YAML block (during implement) and from plan markdown (during plan), and
// hand them to MergeDeferrals for idempotent ingestion.
//
// ID is optional and should be populated ONLY when the agent is explicitly
// re-deferring an existing ledger entry. Cite the ledger entry's `D-xxxxxx`
// ID verbatim (from the `## Deferrals Due This Phase` carry-forward block)
// so MergeDeferrals + ValidateDeferralLedger can match the re-defer even
// when the description drifts across phases. Fresh deferrals leave ID
// empty; the ingester will compute a stable ID from (CreatedInPhase,
// normalized Description).
type IncomingDeferral struct {
	ID            string `yaml:"id,omitempty"`
	Description   string `yaml:"description"`
	DueByPhase    int    `yaml:"due_by_phase"`
	Reason        string `yaml:"reason"`
	CreatedInKind string `yaml:"-"` // set by the parser, not by the agent
	// RepoScope mirrors Deferral.RepoScope. Optional — agents emit it
	// when the deferred work is repo-local; omit for cross-cutting debt.
	RepoScope []string `yaml:"repo_scope,omitempty"`
}

// MergeDeferrals ingests a batch of IncomingDeferral entries into an
// existing ledger. Semantics:
//
//   - If the incoming entry carries a non-empty ID and it matches an
//     existing entry, that entry is the match target (the strong path
//     for re-deferrals, since the agent cites the ledger ID directly
//     from the carry-forward prompt and description drift is tolerated).
//
//   - Otherwise the match is by description hash: DeferralID(createdInPhase,
//     Description). This handles fresh entries and re-emissions where the
//     agent wrote the description verbatim.
//
//   - If the match is Closed, it is a no-op (a re-emission of
//     already-completed work; treat as noise rather than re-opening).
//
//   - If the match is open AND DueByPhase is unchanged AND Description is
//     unchanged, it is a no-op (idempotent re-emission within the same
//     phase).
//
//   - If the match is open AND any of DueByPhase/Description has changed,
//     it is treated as a re-deferral: DueByPhase + Description are
//     updated, Status set to open_redeferred, History appends a
//     Redeferred event. Reason is required on incoming for this case.
//
//   - Otherwise it is a new deferral: a fresh Deferral is appended with
//     a History of [Created] and Status Open.
//
// The function mutates and returns existing. Fresh entries use now for
// timestamps.
//
// Lifecycle (re-entrancy + crash recovery): the function is called inside
// Store.Modify, which serializes the load-mutate-save cycle under the
// store mutex and persists feature.yaml via atomic temp-file rename.
// Re-entrancy: a second call with the same `incoming` against a ledger
// that already absorbed the first batch is a no-op for unchanged entries
// (matched by ID or content hash; closed entries skipped; idempotent
// when DueByPhase, Description, and RepoScope all match) — repeated
// invocations converge to the same fixed point. Crash recovery: because
// mutation is in-memory and persistence is atomic, a crash before
// saveUnlocked returns leaves the prior feature.yaml on disk; the next
// startup re-loads that prior state and the same incoming batch will
// re-converge to the same merged ledger on retry.
func MergeDeferrals(existing []Deferral, incoming []IncomingDeferral, createdInPhase int, now time.Time) []Deferral {
	index := map[string]int{}
	for i := range existing {
		index[existing[i].ID] = i
	}
	for _, in := range incoming {
		// Resolve match target. ID citation wins over description hash —
		// this is how the agent re-defers an existing ledger entry
		// across phases without having to re-type the description
		// verbatim. If the cited ID doesn't resolve (typo, stale), we
		// fall back to hash; the stale-cite case becomes a fresh entry,
		// which is safer than a silent drop.
		var idx int
		var matched bool
		if in.ID != "" {
			if i, ok := index[in.ID]; ok {
				idx, matched = i, true
			}
		}
		if !matched {
			fallbackID := DeferralID(createdInPhase, in.Description)
			if i, ok := index[fallbackID]; ok {
				idx, matched = i, true
			}
		}
		if matched {
			cur := &existing[idx]
			if cur.Status == DeferralClosed {
				continue
			}
			descChanged := normalizeDeferralDescription(cur.Description) != normalizeDeferralDescription(in.Description)
			scopeChanged := !sameScope(cur.RepoScope, in.RepoScope)
			if cur.DueByPhase == in.DueByPhase && !descChanged && !scopeChanged {
				continue
			}
			// Re-deferral: target phase, description, or repo scope shifted.
			cur.History = append(cur.History, DeferralEvent{
				At:        now,
				Kind:      DeferralEventRedeferred,
				FromPhase: cur.DueByPhase,
				ToPhase:   in.DueByPhase,
				Reason:    in.Reason,
			})
			cur.DueByPhase = in.DueByPhase
			cur.Status = DeferralRedeferred
			if descChanged {
				cur.Description = in.Description
			}
			if scopeChanged {
				if len(in.RepoScope) == 0 {
					cur.RepoScope = nil
				} else {
					cur.RepoScope = append([]string(nil), in.RepoScope...)
				}
			}
			if in.Reason != "" {
				cur.Reason = in.Reason
			}
			continue
		}
		// Fresh entry.
		kind := in.CreatedInKind
		if kind == "" {
			kind = "implement" // default source when caller didn't specify
		}
		// Fresh entries get a content-derived ID regardless of what the
		// agent cited. An unresolved `in.ID` is either stale (the cited
		// entry was already closed or never existed) or a typo; honoring
		// it would let an agent forge ledger IDs that collide with later
		// content-derived IDs. Always mint from (phase, description).
		freshID := DeferralID(createdInPhase, in.Description)
		var freshScope []string
		if len(in.RepoScope) > 0 {
			freshScope = append([]string(nil), in.RepoScope...)
		}
		existing = append(existing, Deferral{
			ID:             freshID,
			Description:    in.Description,
			CreatedAt:      now,
			CreatedInPhase: createdInPhase,
			CreatedInKind:  kind,
			DueByPhase:     in.DueByPhase,
			Reason:         in.Reason,
			Status:         DeferralOpen,
			RepoScope:      freshScope,
			History: []DeferralEvent{{
				At:      now,
				Kind:    DeferralEventCreated,
				ToPhase: in.DueByPhase,
				Reason:  in.Reason,
			}},
		})
		index[freshID] = len(existing) - 1
	}
	return existing
}

// CloseDeferrals marks the deferrals with the given IDs as closed by the
// named phase and artifact kind (e.g. phase=3, kind="implement"). IDs not
// present in existing are silently ignored (the reviewer flags orphan
// close-IDs in the verification step, so this path doesn't need to error).
// Returns the number of entries actually transitioned.
func CloseDeferrals(existing []Deferral, ids []string, closedInPhase int, closedInKind string, now time.Time) int {
	if len(ids) == 0 {
		return 0
	}
	want := map[string]struct{}{}
	for _, id := range ids {
		want[id] = struct{}{}
	}
	closed := 0
	for i := range existing {
		if _, ok := want[existing[i].ID]; !ok {
			continue
		}
		if existing[i].Status == DeferralClosed {
			continue
		}
		existing[i].Status = DeferralClosed
		t := now
		existing[i].ClosedAt = &t
		existing[i].ClosedInPhase = closedInPhase
		existing[i].ClosedInKind = closedInKind
		existing[i].History = append(existing[i].History, DeferralEvent{
			At:      now,
			Kind:    DeferralEventClosed,
			ToPhase: closedInPhase,
		})
		closed++
	}
	return closed
}
