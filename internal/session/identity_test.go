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
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// TestAssertEmissionIdentity_WarnsOnEmptyFeatureID exercises the
// precondition helper used at every production SDKEventMsg /
// SessionDoneMsg construction site. A missing FeatureID must produce a
// log line so regressions surface — the helper never panics.
func TestAssertEmissionIdentity_WarnsOnEmptyFeatureID(t *testing.T) {
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)

	assertEmissionIdentity("sess-xyz", "", feature.PhaseImplement)

	got := buf.String()
	if !strings.Contains(got, "session-manager: emit with empty identity") {
		t.Errorf("expected warn log for empty featureID; got %q", got)
	}
	if !strings.Contains(got, `sessionID="sess-xyz"`) {
		t.Errorf("expected sessionID in warn log; got %q", got)
	}
}

// TestAssertEmissionIdentity_QuietOnFullIdentity verifies that a
// correctly-populated emission produces no log output.
func TestAssertEmissionIdentity_QuietOnFullIdentity(t *testing.T) {
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)

	assertEmissionIdentity("sess-abc", "feat-1", feature.PhaseReview)

	if got := buf.String(); got != "" {
		t.Errorf("expected no log output for full identity; got %q", got)
	}
}

// TestEmittedEventsCarryIdentity_TableDriven spins up a session for a
// variety of role-suffixed session IDs and asserts the manager populates
// FeatureID/Phase on both SDKEventMsg and SessionDoneMsg. This is the
// forcing function for F4: every new role (fix, tweak, rebase, kb,
// inquire, brainstorm, …) the manager emits for must carry identity
// directly on the struct, so the TUI never has to parse SessionID.
func TestEmittedEventsCarryIdentity_TableDriven(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	cases := []struct {
		name      string
		sessionID string
		phase     feature.Phase
	}{
		{"implement", "feat-abc-impl-01", feature.PhaseImplement},
		{"plan", "feat-abc-plan", feature.PhasePlan},
		{"research", "feat-abc-research", feature.PhaseResearch},
		{"review", "feat-abc-review-01", feature.PhaseReview},
		{"fix-with-repo", "feat-abc-fix-dev-console-04", feature.PhaseReview},
		{"rebase", "feat-abc-rebase-01", feature.PhaseImplement},
		{"kb", "feat-abc-kb", feature.PhaseKnowledgeBase},
		{"inquire", "feat-abc-inquire", feature.PhaseInquire},
		{"brainstorm", "feat-abc-brainstorm", feature.PhaseBrainstorm},
	}

	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "mock.sh")
	// Emit minimal well-formed JSONL so the session completes quickly.
	script := `#!/bin/bash
read -r -t 5 _ || true
echo '{"type":"system","subtype":"init","session_id":"s1","model":"test"}'
echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}'
echo '{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0.01}'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("writing script: %v", err)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Helper()
			eventCh := make(chan interface{}, 64)
			sm := NewManager(eventCh)
			defer sm.Shutdown()

			featureID := "feat-abc"

			sess, err := sm.StartSession(
				tc.sessionID, featureID, tc.phase,
				[]string{"bash", scriptPath}, tmpDir, nil,
			)
			if err != nil {
				t.Fatalf("starting session: %v", err)
			}

			var sdkSeen, doneSeen atomic.Int64
			var badIdentity atomic.Int64
			doneCh := make(chan struct{})
			go func() {
				defer close(doneCh)
				timeout := time.After(10 * time.Second)
				var settle <-chan time.Time
				for {
					select {
					case evt, ok := <-eventCh:
						if !ok {
							return
						}
						switch v := evt.(type) {
						case SDKEventMsg:
							sdkSeen.Add(1)
							if v.FeatureID != featureID || v.Phase != tc.phase {
								badIdentity.Add(1)
							}
						case SessionDoneMsg:
							doneSeen.Add(1)
							if v.FeatureID != featureID || v.Phase != tc.phase {
								badIdentity.Add(1)
							}
							settle = time.After(200 * time.Millisecond)
						}
					case <-settle:
						return
					case <-timeout:
						return
					}
				}
			}()

			sess.Wait()
			select {
			case <-doneCh:
			case <-time.After(5 * time.Second):
			}

			if sdkSeen.Load() == 0 {
				t.Errorf("no SDKEventMsg received for %q", tc.sessionID)
			}
			if doneSeen.Load() == 0 {
				t.Errorf("no SessionDoneMsg received for %q", tc.sessionID)
			}
			if n := badIdentity.Load(); n > 0 {
				t.Errorf("%d events had missing/wrong identity for %q", n, tc.sessionID)
			}
		})
	}
}

// sanity-check — keeps fmt used if additional role cases are added.
var _ = fmt.Sprintf
