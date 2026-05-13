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

package session

import (
	"context"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// RecoveryAdapter binds the featuresDir + *feature.Manager context that
// ScanForRecovery / ExecuteRecovery need, and exposes them as the
// ports.RecoveryOperator interface. Context is accepted for future
// cancellation but is not consulted by the underlying package-level
// functions today.
type RecoveryAdapter struct {
	StateDir       string
	FeatureManager *feature.Manager
}

// NewRecoveryAdapter constructs a RecoveryAdapter bound to the given state
// directory and feature manager.
func NewRecoveryAdapter(stateDir string, fm *feature.Manager) *RecoveryAdapter {
	return &RecoveryAdapter{StateDir: stateDir, FeatureManager: fm}
}

// ScanForRecovery delegates to the package-level ScanForRecovery using the
// adapter's stored featuresDir + feature manager.
func (a *RecoveryAdapter) ScanForRecovery(_ context.Context) ([]RecoveryItem, error) {
	return ScanForRecovery(a.StateDir, a.FeatureManager)
}

// ExecuteRecovery delegates to the package-level ExecuteRecovery using the
// adapter's stored feature manager.
func (a *RecoveryAdapter) ExecuteRecovery(_ context.Context, items []RecoveryItem, actions map[string]RecoveryAction) error {
	return ExecuteRecovery(items, actions, a.FeatureManager)
}
