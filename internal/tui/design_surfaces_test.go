// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tui

import (
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// TestCheckpointDescriptionsUseDesignLanguage pins the configeditor checkpoint
// copy so Research Review describes "before design" and Design Review describes
// "after design" — the canonical phase-slot language users see in the wizard.
func TestCheckpointDescriptionsUseDesignLanguage(t *testing.T) {
	t.Parallel()
	want := map[feature.GateIndex]string{
		feature.GateResearchReview: "Pause after research before design",
		feature.GateDesignReview:   "Pause after design before planning",
	}
	for _, cf := range checkpointFields {
		exp, ok := want[cf.Gate]
		if !ok {
			continue
		}
		if cf.Desc != exp {
			t.Errorf("checkpoint %v desc = %q, want %q", cf.Gate, cf.Desc, exp)
		}
	}
}
