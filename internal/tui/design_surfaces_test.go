// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
package tui

import (
	"strings"
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
		feature.GateDesignReview:    "Pause after design before planning",
	}
	for _, cf := range checkpointFields {
		exp, ok := want[cf.Gate]
		if !ok {
			continue
		}
		if cf.Desc != exp {
			t.Errorf("checkpoint %v desc = %q, want %q", cf.Gate, cf.Desc, exp)
		}
		if strings.Contains(strings.ToLower(cf.Desc), "brainstorm") {
			t.Errorf("checkpoint %v desc still references legacy brainstorm: %q", cf.Gate, cf.Desc)
		}
	}
}

// TestSessionIDParsingCanonicalDesignSuffix ensures the canonical "-design"
// suffix routes to PhaseDesign. Legacy "-brainstorm" handling is covered
// alongside the other suffix routes in TestPhaseFromSessionID.
func TestSessionIDParsingCanonicalDesignSuffix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		sessionID string
		want      feature.Phase
	}{
		{"abc123-design", feature.PhaseDesign},
		{"longid1234567890-design", feature.PhaseDesign},
		// Legacy alias still resolves to the same logical phase.
		{"abc123-brainstorm", feature.PhaseDesign},
	}
	for _, tt := range tests {
		t.Run(tt.sessionID, func(t *testing.T) {
			got := phaseFromSessionID(tt.sessionID)
			if got != tt.want {
				t.Errorf("phaseFromSessionID(%q) = %v, want %v", tt.sessionID, got, tt.want)
			}
		})
	}
}
