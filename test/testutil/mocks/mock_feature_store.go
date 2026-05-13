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

package mocks

import (
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// MockFeatureStore implements ports.FeatureStore with configurable function
// overrides and call tracking. Each method delegates to its XxxFn field when
// non-nil; otherwise it returns DefaultError (and nil for value returns).
type MockFeatureStore struct {
	// Function overrides — set these to control method behavior.
	SaveFn           func(f *feature.Feature) error
	LoadFn           func(id string) (*feature.Feature, error)
	ModifyFn         func(id string, fn func(f *feature.Feature) error) error
	ListFn           func() ([]*feature.Feature, error)
	DeleteFn         func(id string) error
	CreateRunFn      func(featureID string, r *feature.Run) error
	LoadRunFn        func(featureID string, runNumber int) (*feature.Run, error)
	SaveRunFn        func(featureID string, r *feature.Run) error
	SealAndForkRunFn func(
		featureID string,
		seal func(*feature.Run) error,
		fork func(*feature.Run) (*feature.Run, error),
		populate func(*feature.Run, *feature.Run) error,
	) (*feature.Feature, error)
	CleanupOrphanRunsFn func(id string) ([]int, error)

	// DefaultError is returned by methods whose Fn override is nil.
	DefaultError error

	// Call tracking — each method appends its arguments before delegating.
	SaveCalls              []*feature.Feature
	LoadCalls              []string
	ModifyCalls            []string
	DeleteCalls            []string
	CreateRunCalls         []string
	LoadRunCalls           []string
	SaveRunCalls           []string
	SealAndForkRunCalls    []string
	CleanupOrphanRunsCalls []string
}

// NewMockFeatureStore returns a MockFeatureStore with zero-value defaults
// (all Fn overrides nil, DefaultError nil, empty call slices).
func NewMockFeatureStore() *MockFeatureStore {
	return &MockFeatureStore{}
}

func (m *MockFeatureStore) Save(f *feature.Feature) error {
	m.SaveCalls = append(m.SaveCalls, f)
	if m.SaveFn != nil {
		return m.SaveFn(f)
	}
	return m.DefaultError
}

func (m *MockFeatureStore) Load(id string) (*feature.Feature, error) {
	m.LoadCalls = append(m.LoadCalls, id)
	if m.LoadFn != nil {
		return m.LoadFn(id)
	}
	return nil, m.DefaultError
}

func (m *MockFeatureStore) Modify(id string, fn func(f *feature.Feature) error) error {
	m.ModifyCalls = append(m.ModifyCalls, id)
	if m.ModifyFn != nil {
		return m.ModifyFn(id, fn)
	}
	return m.DefaultError
}

func (m *MockFeatureStore) List() ([]*feature.Feature, error) {
	if m.ListFn != nil {
		return m.ListFn()
	}
	return nil, m.DefaultError
}

func (m *MockFeatureStore) Delete(id string) error {
	m.DeleteCalls = append(m.DeleteCalls, id)
	if m.DeleteFn != nil {
		return m.DeleteFn(id)
	}
	return m.DefaultError
}

func (m *MockFeatureStore) CreateRun(featureID string, r *feature.Run) error {
	m.CreateRunCalls = append(m.CreateRunCalls, featureID)
	if m.CreateRunFn != nil {
		return m.CreateRunFn(featureID, r)
	}
	return m.DefaultError
}

func (m *MockFeatureStore) LoadRun(featureID string, runNumber int) (*feature.Run, error) {
	m.LoadRunCalls = append(m.LoadRunCalls, featureID)
	if m.LoadRunFn != nil {
		return m.LoadRunFn(featureID, runNumber)
	}
	return nil, m.DefaultError
}

func (m *MockFeatureStore) SaveRun(featureID string, r *feature.Run) error {
	m.SaveRunCalls = append(m.SaveRunCalls, featureID)
	if m.SaveRunFn != nil {
		return m.SaveRunFn(featureID, r)
	}
	return m.DefaultError
}

func (m *MockFeatureStore) SealAndForkRun(
	featureID string,
	seal func(*feature.Run) error,
	fork func(*feature.Run) (*feature.Run, error),
	populate func(*feature.Run, *feature.Run) error,
) (*feature.Feature, error) {
	m.SealAndForkRunCalls = append(m.SealAndForkRunCalls, featureID)
	if m.SealAndForkRunFn != nil {
		return m.SealAndForkRunFn(featureID, seal, fork, populate)
	}
	return nil, m.DefaultError
}

func (m *MockFeatureStore) CleanupOrphanRuns(id string) ([]int, error) {
	m.CleanupOrphanRunsCalls = append(m.CleanupOrphanRunsCalls, id)
	if m.CleanupOrphanRunsFn != nil {
		return m.CleanupOrphanRunsFn(id)
	}
	return nil, m.DefaultError
}
