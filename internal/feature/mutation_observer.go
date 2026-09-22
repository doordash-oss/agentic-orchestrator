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
	"reflect"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
)

type MutationKind string

const (
	MutationSaved   MutationKind = "saved"
	MutationDeleted MutationKind = "deleted"
	MutationRewound MutationKind = "rewound"
)

// MutationSnapshot is an immutable view captured at the persistence boundary.
// OutputRepos paths are for local Git inspection only and must not be exported.
type MutationSnapshot struct {
	ID                string
	Name              string
	Created           time.Time
	Status            Status
	Phase             Phase
	ActiveRun         int
	RunCount          int
	Pipeline          PipelineProfile
	Risk              RiskLevel
	FeatureKind       string
	RepositoryNames   []string
	TraceID           string
	FeatureSpanID     string
	ErrorCode         string
	ErrorClass        string
	VerificationItems []VerificationItemStatus
	OutputRepos       []MutationOutputRepo
	StartedAt         *time.Time
	OutputReadyAt     *time.Time
	DeliveredAt       *time.Time
	PhaseTimings      map[string]time.Duration
}

type MutationOutputRepo struct{ Name, Path, WorktreePath, BaseBranch string }

type Mutation struct {
	Kind   MutationKind
	Before *MutationSnapshot
	After  *MutationSnapshot
	At     time.Time
}

type MutationObserver interface{ FeatureMutated(Mutation) }

func mutationSnapshot(f *Feature) *MutationSnapshot {
	if f == nil {
		return nil
	}
	repos := make([]string, 0, len(f.Repos))
	outputRepos := make([]MutationOutputRepo, 0, len(f.Repos))
	for _, repo := range f.Repos {
		repos = append(repos, repo.Name)
		outputRepos = append(outputRepos, MutationOutputRepo{Name: repo.Name, Path: repo.Path, WorktreePath: repo.WorktreePath, BaseBranch: repo.BaseBranch})
	}
	kind := "parent"
	if f.IsChild() {
		kind = "child"
	}
	entry, _ := errcat.Lookup(f.FailureCode())
	return &MutationSnapshot{ID: f.ID, Name: f.Name, Created: f.Created, Status: f.Status, Phase: f.CurrentPhase,
		ActiveRun: f.ActiveRun, RunCount: f.RunCount, Pipeline: f.Pipeline, Risk: f.RiskLevel, FeatureKind: kind,
		RepositoryNames: repos, TraceID: f.TraceID, FeatureSpanID: f.FeatureSpanID, ErrorCode: string(f.FailureCode()), ErrorClass: string(entry.Class),
		VerificationItems: append([]VerificationItemStatus(nil), f.VerificationItems...), OutputRepos: outputRepos, StartedAt: cloneTime(f.StartedAt), OutputReadyAt: cloneTime(f.Run().OutputReadyAt), DeliveredAt: cloneTime(f.Run().DeliveredAt), PhaseTimings: cloneDurations(f.PhaseTimings)}
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func cloneDurations(in map[string]time.Duration) map[string]time.Duration {
	out := make(map[string]time.Duration, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func mutationSnapshotsEqual(a, b *MutationSnapshot) bool { return reflect.DeepEqual(a, b) }

func (s *Store) SetMutationObserver(observer MutationObserver) {
	s.mu.Lock()
	s.mutationObserver = observer
	s.mu.Unlock()
}

func (s *Store) notifyMutation(m Mutation) {
	s.mu.RLock()
	observer := s.mutationObserver
	s.mu.RUnlock()
	if observer != nil {
		observer.FeatureMutated(m)
	}
}

// recordRunMilestones stamps only a first transition into a milestone. Run
// forks bypass this helper: the old feature status still exists briefly while
// the fresh run is persisted, and must not mark the new attempt delivered.
func recordRunMilestones(f *Feature, before *MutationSnapshot) {
	r := f.Run()
	if r.IsSealed() {
		return
	}
	now := time.Now().UTC()
	if f.Status == StatusCodeReady && r.OutputReadyAt == nil && (before == nil || before.Status != StatusCodeReady) {
		r.OutputReadyAt = &now
	}
	if (f.Status == StatusPublished || f.Status == StatusDone) && r.DeliveredAt == nil && (before == nil || (before.Status != StatusPublished && before.Status != StatusDone)) {
		r.DeliveredAt = &now
	}
}
