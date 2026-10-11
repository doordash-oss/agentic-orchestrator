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
	"encoding/json"
	"fmt"
)

// This file exposes a handful of unexported helpers to the external
// feature_test package. It is compiled only during testing.

// GenerateIDForTest returns a fresh random feature ID. Exported here solely
// for tests that need to fabricate Feature values directly in the store.
func GenerateIDForTest() string { return generateID() }

// StoreSaveHook is a test-only hook that, when set, causes saveUnlocked to
// fail on the Nth call (optionally only for a specific feature ID). This
// lets external tests exercise error paths in paired config updates without
// modifying the file system.
type StoreSaveHook struct {
	FailOnFeatureID string
	FailOnCall      int
	currentCall     int
}

// SetSaveHook installs a test-only save interceptor on the Store.
func (s *Store) SetSaveHook(h *StoreSaveHook) {
	s.SetSaveInterceptor(func(f *Feature) error {
		h.currentCall++
		if h.FailOnCall > 0 && h.currentCall == h.FailOnCall {
			if h.FailOnFeatureID == "" || h.FailOnFeatureID == f.ID {
				return fmt.Errorf("test-injected save failure on call %d for %s", h.currentCall, f.ID)
			}
		}
		return nil
	})
}

// ResetSaveHook removes any test-only save interceptor.
func (s *Store) ResetSaveHook() {
	s.SetSaveInterceptor(nil)
}

// RebaseDescriptionForTest exposes the machine-generated rebase child
// description generator to the external feature_test package.
var RebaseDescriptionForTest = rebaseDescription

// RebaseExitCriteriaForTest exposes the machine-generated rebase child exit
// criteria generator to the external feature_test package.
var RebaseExitCriteriaForTest = rebaseExitCriteria

// SwapFetchPRCommentsForTest replaces the GitHub comment resolver used by
// draft-based review-feedback launch and returns a restore function. Fixture
// comments are supplied as GitHub REST comment wire JSON and decoded into the
// resolver's comment shape here, so the external test package never names the
// internal comment type.
func SwapFetchPRCommentsForTest(fn func(repoPath, prURL string) ([][]byte, error)) func() {
	prior := fetchPRCommentsFunc
	fetchPRCommentsFunc = decodeFetchComments(prior, fn)
	return func() { fetchPRCommentsFunc = prior }
}

// decodeFetchComments adapts a wire-JSON fixture resolver to the prior
// resolver's comment type, which stays unnamed here.
func decodeFetchComments[T any](prior func(repoPath, prURL string) ([]T, error), fn func(repoPath, prURL string) ([][]byte, error)) func(repoPath, prURL string) ([]T, error) {
	if fn == nil {
		return prior
	}
	return func(repoPath, prURL string) ([]T, error) {
		raw, err := fn(repoPath, prURL)
		if err != nil {
			return nil, err
		}
		comments := make([]T, 0, len(raw))
		for _, data := range raw {
			var c T
			if err := json.Unmarshal(data, &c); err != nil {
				return nil, err
			}
			comments = append(comments, c)
		}
		return comments, nil
	}
}
