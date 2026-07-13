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

package ports

import (
	"context"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// RecoveryAction enumerates user choices for an orphaned session.
type RecoveryAction int

const (
	// RecoveryResume — continue the session via --resume after killing the
	// orphaned process group.
	RecoveryResume RecoveryAction = iota
	// RecoveryKill — terminate the process group and mark the feature
	// interrupted without resuming.
	RecoveryKill
	// RecoverySkip — leave the feature untouched; cleanup stale PID files
	// only when the process is already dead.
	RecoverySkip
)

// PIDFile describes an on-disk session PID file discovered during recovery
// scanning. Defined in ports so domain code can manipulate recovery items
// without importing internal/session.
type PIDFile struct {
	PID         int       `yaml:"pid"`
	StartedAt   time.Time `yaml:"started"`
	FeatureID   string    `yaml:"feature"`
	Phase       string    `yaml:"phase"`
	Iteration   int       `yaml:"iteration"`
	WorktreeDir string    `yaml:"worktree"`
	SessionID   string    `yaml:"session_id,omitempty"`
	RepoName    string    `yaml:"repo_name,omitempty"`
	Dir         string    `yaml:"-"`
}

// RecoveryItem packages everything the orchestrator needs to make a
// recovery decision about a single orphan session.
type RecoveryItem struct {
	PIDFile      PIDFile
	ProcessAlive bool
	Feature      *feature.Feature
	RepoName     string
}

// RecoveryActionKey returns the map key used to look up a per-item action.
// Returns featureID for feature-scoped items and "featureID:repoName" for
// per-repo items.
func RecoveryActionKey(featureID, repoName string) string {
	if repoName == "" {
		return featureID
	}
	return featureID + ":" + repoName
}

// RecoveryOperator abstracts orphan-session discovery and dispatch. The
// interface is intentionally narrow: domain code never imports internal/session
// to obtain recovery context.
type RecoveryOperator interface {
	ScanForRecovery(ctx context.Context) ([]RecoveryItem, error)
	ExecuteRecovery(ctx context.Context, items []RecoveryItem, actions map[string]RecoveryAction) error
}
