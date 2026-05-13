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

package ports_test

import (
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// TestRecoveryActionKey_Behavior verifies the canonical helper used across
// the domain for mapping (featureID, repoName) → action-map key.
func TestRecoveryActionKey_Behavior(t *testing.T) {
	cases := []struct {
		name, featureID, repoName, want string
	}{
		{"feature only", "feat-a", "", "feat-a"},
		{"feature and repo", "feat-b", "repo-x", "feat-b:repo-x"},
		{"empty", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ports.RecoveryActionKey(tc.featureID, tc.repoName)
			if got != tc.want {
				t.Errorf("ports.RecoveryActionKey(%q,%q) = %q, want %q",
					tc.featureID, tc.repoName, got, tc.want)
			}
		})
	}
}

// TestRecoveryActionConstants asserts the distinct values domain code relies
// on when routing actions.
func TestRecoveryActionConstants(t *testing.T) {
	if ports.RecoveryResume == ports.RecoveryKill {
		t.Error("RecoveryResume and RecoveryKill should be distinct")
	}
	if ports.RecoveryKill == ports.RecoverySkip {
		t.Error("RecoveryKill and RecoverySkip should be distinct")
	}
}
