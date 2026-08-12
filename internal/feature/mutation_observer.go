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
)

type MutationKind string

const (
	MutationSaved   MutationKind = "saved"
	MutationDeleted MutationKind = "deleted"
	MutationRewound MutationKind = "rewound"
)

// MutationSnapshot is an immutable, telemetry-safe view captured at the
// persistence boundary. It intentionally excludes paths, descriptions,
// prompts, answers, and other rich content.
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
	FailureType       string
	VerificationItems []VerificationItemStatus
	OutputRepos       []MutationOutputRepo
	StartedAt         *time.Time
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
	return &MutationSnapshot{ID: f.ID, Name: f.Name, Created: f.Created, Status: f.Status, Phase: f.CurrentPhase,
		ActiveRun: f.ActiveRun, RunCount: f.RunCount, Pipeline: f.Pipeline, Risk: f.RiskLevel, FeatureKind: kind,
		RepositoryNames: repos, TraceID: f.TraceID, FeatureSpanID: f.FeatureSpanID, FailureType: f.FailureType,
		VerificationItems: append([]VerificationItemStatus(nil), f.VerificationItems...), OutputRepos: outputRepos, StartedAt: cloneTime(f.StartedAt), PhaseTimings: cloneDurations(f.PhaseTimings)}
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
