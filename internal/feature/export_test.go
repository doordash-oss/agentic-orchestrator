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
	"fmt"

	gitadapter "github.com/doordash-oss/agentic-orchestrator/internal/git"
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
	s.testSaveInterceptor = func(f *Feature) error {
		h.currentCall++
		if h.FailOnCall > 0 && h.currentCall == h.FailOnCall {
			if h.FailOnFeatureID == "" || h.FailOnFeatureID == f.ID {
				return fmt.Errorf("test-injected save failure on call %d for %s", h.currentCall, f.ID)
			}
		}
		return nil
	}
}

// ResetSaveHook removes any test-only save interceptor.
func (s *Store) ResetSaveHook() {
	s.testSaveInterceptor = nil
}

// SwapFetchPRCommentsForTest replaces the GitHub comment resolver used by
// draft-based review-feedback launch and returns a restore function.
func SwapFetchPRCommentsForTest(fn func(repoPath, prURL string) ([]gitadapter.ReviewComment, error)) func() {
	prior := fetchPRCommentsFunc
	fetchPRCommentsFunc = fn
	return func() { fetchPRCommentsFunc = prior }
}
