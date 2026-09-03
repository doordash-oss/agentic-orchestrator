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

import "github.com/doordash-oss/agentic-orchestrator/internal/errcat"

// ErrChildExecutionClosed rejects execution of a child whose relationship
// has settled. It aliases the relationship mutation sentinel so execution
// and serialized direct mutations expose the same stable closed error.
var ErrChildExecutionClosed = ErrChildRelationshipClosed

// ChildSetupComplete reports whether the child's durable setup finished
// successfully — the child is parked at Created (or any later non-failed
// status). Queued/setting-up and failed-setup children report false and stay
// reachable only through the setup and setup-retry entrypoints.
func (f *Feature) ChildSetupComplete() bool {
	if f == nil || !f.IsChild() {
		return false
	}
	if f.Status == StatusSettingUpWorktrees {
		return false
	}
	if f.Status == StatusFailed && errcat.IsSetupFailure(f.FailureCode()) {
		return false
	}
	return true
}

// ChildExecutionCapability is a deliberate extension seam for enforcing the
// supported child execution shape. All pipeline profiles (Medium, Large, and
// Moonshot) may execute. Large and Moonshot children use disposable KB
// workspaces seeded from the parent overlay or canonical KB, so the temporary
// profile restriction is retired. The check is deterministic from the
// durable record and is enforced identically by start, resume, retry, and
// restart paths. It currently always returns nil; callers still invoke it
// so future capability gates can be added without touching every call site.
func (f *Feature) ChildExecutionCapability() error {
	if f == nil || !f.IsChild() {
		return nil
	}
	return nil
}

// Close outcomes recorded on ChildRelationship.CloseOutcome.
const (
	// ChildCloseOutcomeCompleted: the child's work was integrated into the
	// parent and the relationship closed successfully.
	ChildCloseOutcomeCompleted = "completed"
	// ChildCloseOutcomeDiscarded: the child was discarded without integrating
	// its work into the parent. The relationship is closed and the child
	// record is retained for inspection.
	ChildCloseOutcomeDiscarded = "discarded"
)
