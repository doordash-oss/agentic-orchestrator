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
	"sync"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

const (
	// defaultOriginCheckDeadline bounds one origin-check attempt, covering
	// semaphore admission, common-directory coordination, and Git work.
	defaultOriginCheckDeadline = 60 * time.Second
	// maxConcurrentOriginChecks caps in-flight origin checks server-wide so
	// many selected repositories cannot saturate the host with fetches.
	maxConcurrentOriginChecks = 4
	// maxRetainedOriginChecks bounds completed-snapshot memory.
	maxRetainedOriginChecks = 512
)

// originAttemptOutcome is one fetch-and-compare attempt's result. A
// comparison is evidence about exactly its recorded SHAs; Absent means the
// current attempt proved the mapped branch missing on the remote.
type originAttemptOutcome struct {
	Comparison  *git.OriginComparison
	Absent      bool
	Unavailable bool
	Diagnostics string
}

// originCheckExecutor performs the network work for one resolved plan. It is
// a field so deterministic tests inject barriers and failures.
type originCheckExecutor func(ctx context.Context, repoPath string, plan git.OriginCheckPlan) originAttemptOutcome

func defaultOriginCheckExecutor(ctx context.Context, repoPath string, plan git.OriginCheckPlan) originAttemptOutcome {
	fetch := git.FetchOriginBranch(ctx, repoPath, *plan.Mapping, git.OriginCheckOptions{})
	switch fetch.State {
	case git.FetchOriginAbsent:
		return originAttemptOutcome{Absent: true}
	case git.FetchOriginUnavailable:
		return originAttemptOutcome{Unavailable: true, Diagnostics: fetch.Diagnostics}
	}
	comparison, err := git.CompareOriginSource(ctx, repoPath, plan.Source.Commit, fetch.SHA, *plan.Mapping, git.OriginCheckOptions{})
	if err != nil {
		return originAttemptOutcome{Unavailable: true, Diagnostics: err.Error()}
	}
	return originAttemptOutcome{Comparison: &comparison}
}

// originCheckKey identifies one comparable source: the repository's resolved
// identity, the shared mode, the selected source, and the mapping. Results
// are stored and served only under the key that produced them, so a mode
// switch, source change, or mapping change can never inherit another key's
// comparison.
type originCheckKey struct {
	commonDir     string
	device        uint64
	inode         uint64
	mode          git.LocalSourceMode
	kind          string
	branch        string
	localSHA      string
	mappingBranch string
}

func originCheckKeyFor(identity git.RepoIdentity, plan git.OriginCheckPlan) originCheckKey {
	key := originCheckKey{
		commonDir: identity.CommonDir,
		device:    identity.Device,
		inode:     identity.Inode,
		mode:      plan.Source.Mode,
		kind:      plan.Source.Kind,
		branch:    plan.Source.Branch,
		localSHA:  plan.Source.Commit,
	}
	if plan.Mapping != nil {
		key.mappingBranch = plan.Mapping.Branch
	}
	return key
}

// originCompleted is one completed attempt's durable snapshot.
type originCompleted struct {
	comparison     *git.OriginComparison
	absent         bool
	unavailable    bool
	diagnostics    string
	checkedAt      time.Time
	stale          *git.OriginComparison
	updateEligible *bool
	updateBlockers []git.UpdateBlocker
}

type originFlight struct {
	key  originCheckKey
	done chan struct{}
}

// originCheckCoordinator owns the server's origin-check state: coalesced
// in-flight attempts, completed snapshots, the global concurrency cap, and
// the per-attempt deadline. Local-only outcomes never enter the coordinator;
// they are resolved inline by each request.
type originCheckCoordinator struct {
	executor originCheckExecutor
	deadline time.Duration
	now      func() time.Time

	baseCtx context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	sem     chan struct{}

	mu             sync.Mutex
	inflight       map[originCheckKey]*originFlight
	completed      map[originCheckKey]*originCompleted
	completedOrder []originCheckKey
}

func newOriginCheckCoordinator() *originCheckCoordinator {
	ctx, cancel := context.WithCancel(context.Background())
	return &originCheckCoordinator{
		executor:  defaultOriginCheckExecutor,
		deadline:  defaultOriginCheckDeadline,
		now:       time.Now,
		baseCtx:   ctx,
		cancel:    cancel,
		sem:       make(chan struct{}, maxConcurrentOriginChecks),
		inflight:  make(map[originCheckKey]*originFlight),
		completed: make(map[originCheckKey]*originCompleted),
	}
}

// Shutdown cancels every in-flight attempt's context and waits for the
// attempt goroutines to release their resources.
func (c *originCheckCoordinator) Shutdown() {
	c.cancel()
	c.wg.Wait()
}

// ensure registers or joins the attempt for one resolved selection. It
// returns the completed snapshot when one exists and is not being refreshed,
// otherwise the attempt's done channel, which closes when the attempt's
// result is stored.
func (c *originCheckCoordinator) ensure(identity git.RepoIdentity, plan git.OriginCheckPlan, repoPath string, refresh bool) (*originCompleted, bool, <-chan struct{}) {
	key := originCheckKeyFor(identity, plan)
	c.mu.Lock()
	defer c.mu.Unlock()
	if !refresh {
		if completed, ok := c.completed[key]; ok {
			return completed, true, nil
		}
	}
	if flight, ok := c.inflight[key]; ok {
		// An in-flight attempt for the identical resolved source is itself a
		// fresh attempt; the caller joins it instead of scheduling another.
		return nil, false, flight.done
	}
	flight := &originFlight{key: key, done: make(chan struct{})}
	c.inflight[key] = flight
	var priorSuccess *git.OriginComparison
	var priorStale *git.OriginComparison
	if completed, ok := c.completed[key]; ok {
		priorSuccess = completed.comparison
		priorStale = completed.stale
		// A refreshed attempt invalidates the completed result immediately:
		// while the fresh attempt runs, polls must report checking, never
		// serve the superseded result as current.
		delete(c.completed, key)
	}
	c.wg.Add(1)
	go c.runAttempt(flight, identity, plan, repoPath, priorSuccess, priorStale)
	return nil, false, flight.done
}

// runAttempt executes one attempt: admission, coordination, re-resolution,
// the bounded fetch and compare, and eligibility. The result is stored under
// the recorded key regardless of what changed during the attempt.
func (c *originCheckCoordinator) runAttempt(flight *originFlight, identity git.RepoIdentity, plan git.OriginCheckPlan, repoPath string, priorSuccess, priorStale *git.OriginComparison) {
	defer c.wg.Done()
	key := flight.key
	ctx, cancel := context.WithTimeout(c.baseCtx, c.deadline)
	defer cancel()

	outcome := originAttemptOutcome{Unavailable: true, Diagnostics: "origin check did not run"}
	timedOut := false
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		outcome = originAttemptOutcome{Unavailable: true, Diagnostics: "origin check timed out before running"}
		timedOut = true
	}

	var updateEligible *bool
	var updateBlockers []git.UpdateBlocker
	if !timedOut {
		unlock, locked := git.LockRepositoryUntil(ctx, repoPath)
		if !locked {
			outcome = originAttemptOutcome{Unavailable: true, Diagnostics: "origin check timed out waiting for repository coordination"}
		} else {
			outcome = c.attemptUnderLock(ctx, identity, plan, repoPath, key)
			unlock()
		}
	}

	if outcome.Comparison != nil && plan.Source.Kind == git.LocalSourceBranch {
		// The fresh comparison supplies the advisory ignored-collision range
		// for an original-checkout holder; eligibility is advisory and the
		// update revalidates everything at execution.
		eligible, blockers := c.updateEligibility(repoPath, plan.Source.Branch, outcome.Comparison)
		updateEligible = &eligible
		updateBlockers = blockers
	}

	completed := &originCompleted{checkedAt: c.now()}
	switch {
	case outcome.Comparison != nil:
		completed.comparison = outcome.Comparison
		if outcome.Comparison.Status != git.OriginCheckBehind {
			notBehind := false
			updateEligible = &notBehind
			if !containsUpdateBlocker(updateBlockers, git.UpdateBlockerLocalNotBehind) {
				updateBlockers = append(updateBlockers, git.UpdateBlockerLocalNotBehind)
			}
		}
	case outcome.Absent:
		completed.absent = true
	default:
		completed.unavailable = true
		completed.diagnostics = outcome.Diagnostics
		// A failed retry preserves the last successful comparison as
		// explicitly stale, tied to its own SHAs, counts, and timestamp.
		if priorSuccess != nil {
			completed.stale = priorSuccess
		} else if priorStale != nil {
			completed.stale = priorStale
		}
		notEligible := false
		updateEligible = &notEligible
		updateBlockers = []git.UpdateBlocker{git.UpdateBlockerComparisonUnavailable}
	}
	completed.updateEligible = updateEligible
	completed.updateBlockers = updateBlockers

	c.mu.Lock()
	delete(c.inflight, key)
	c.storeCompletedLocked(key, completed)
	c.mu.Unlock()
	close(flight.done)
}

// attemptUnderLock re-resolves the repository identity and selected source
// after coordination and performs the fetch and compare. When the local
// branch advanced while waiting, the comparison still reports the recorded
// SHAs: it is evidence about them, never about the new selection.
func (c *originCheckCoordinator) attemptUnderLock(ctx context.Context, identity git.RepoIdentity, plan git.OriginCheckPlan, repoPath string, key originCheckKey) originAttemptOutcome {
	resolved, ok := git.ResolveRepoIdentity(repoPath)
	if !ok || !resolved.Equal(identity) {
		return originAttemptOutcome{Unavailable: true, Diagnostics: "repository identity changed during the origin check"}
	}
	current := git.PlanOriginCheck(ctx, repoPath, plan.Source.Mode, git.OriginCheckOptions{})
	if current.Status != "" || current.Mapping == nil || current.Mapping.Branch != key.mappingBranch || current.Source.Kind != key.kind || current.Source.Branch != key.branch {
		return originAttemptOutcome{Unavailable: true, Diagnostics: "selected source changed during the origin check"}
	}
	outcome := c.executor(ctx, repoPath, plan)
	if ctx.Err() != nil {
		return originAttemptOutcome{Unavailable: true, Diagnostics: "origin check timed out"}
	}
	return outcome
}

func (c *originCheckCoordinator) updateEligibility(repoPath, branch string, comparison *git.OriginComparison) (bool, []git.UpdateBlocker) {
	ctx, cancel := context.WithTimeout(c.baseCtx, 10*time.Second)
	defer cancel()
	return git.ProbeUpdateEligibility(ctx, repoPath, branch, comparison, git.OriginCheckOptions{})
}

func (c *originCheckCoordinator) storeCompletedLocked(key originCheckKey, completed *originCompleted) {
	if _, exists := c.completed[key]; !exists {
		c.completedOrder = append(c.completedOrder, key)
	}
	c.completed[key] = completed
	for len(c.completedOrder) > maxRetainedOriginChecks {
		oldest := c.completedOrder[0]
		c.completedOrder = c.completedOrder[1:]
		delete(c.completed, oldest)
	}
}

func containsUpdateBlocker(blockers []git.UpdateBlocker, want git.UpdateBlocker) bool {
	for _, blocker := range blockers {
		if blocker == want {
			return true
		}
	}
	return false
}
