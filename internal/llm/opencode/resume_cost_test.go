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

package opencode

import (
	"context"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// sessionDBRunner answers the parent and child cost queries from a mutable
// snapshot of opencode.db.
type sessionDBRunner struct {
	mu         sync.Mutex
	parentCost string
	children   string
}

func (r *sessionDBRunner) set(parentCost, children string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.parentCost, r.children = parentCost, children
}

func (r *sessionDBRunner) RunSQLite(_ context.Context, _, query string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if strings.Contains(query, "WITH RECURSIVE descendants") {
		return []byte(r.children), nil
	}
	return []byte(r.parentCost), nil
}

func TestResumedSessionReportsOnlyItsOwnSpend(t *testing.T) {
	tests := []struct {
		name            string
		resumeID        string
		dbAvailable     bool
		replayedCost    float64
		wantCost        float64
		wantChildCost   float64
		wantChildIDs    string
		wantChildTokens int
	}{
		{
			name:     "fresh session keeps the cumulative cost",
			wantCost: 1.25, wantChildCost: 0.8, wantChildIDs: "child-a,child-b", wantChildTokens: 80,
		},
		{
			name: "resume subtracts the database baseline", resumeID: "ses_x", dbAvailable: true,
			wantCost: 0.25, wantChildCost: 0.3, wantChildIDs: "child-b", wantChildTokens: 30,
		},
		{
			name: "resume without a database subtracts the replayed cost", resumeID: "ses_x", replayedCost: 1.0,
			wantCost: 0.25,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHandshakeHarness(t, llm.ProtocolOpts{
				Model:           "opencode:vendor/model",
				WorkDir:         "/work/dir",
				InitialPrompt:   "continue",
				ResumeSessionID: tt.resumeID,
			})
			runner := &sessionDBRunner{}
			runner.set("1.0\n", "0.5\t50\t0\t0\t0\tchild-a\n")
			if tt.dbAvailable || tt.resumeID == "" {
				h.p.costLookup = openCodeChildSessionCostLookup{dbPath: "/tmp/opencode.db", runner: runner}
			} else {
				h.p.costLookup = openCodeChildSessionCostLookup{dbPath: "/nonexistent/opencode.db"}
			}

			errCh := make(chan error, 1)
			go func() { errCh <- h.p.Handshake(context.Background()) }()
			initReq := h.nextRequest(t)
			h.feed(t, responseLine(t, mustID(t, initReq.ID), initResultWithLoadSession(true)))
			sessReq := h.nextRequest(t)
			if tt.replayedCost > 0 {
				h.feed(t, usageUpdateLine(t, map[string]any{"cost": map[string]any{"amount": tt.replayedCost, "currency": "USD"}}))
			}
			sessionResult := map[string]any{}
			if tt.resumeID == "" {
				sessionResult["sessionId"] = "ses_x"
			}
			h.feed(t, responseLine(t, mustID(t, sessReq.ID), sessionResult))
			promptReq := h.nextRequest(t)
			if err := <-errCh; err != nil {
				t.Fatalf("Handshake: %v", err)
			}

			runner.set("1.25\n", "0.8\t80\t0\t0\t0\tchild-a,child-b\n")
			u := lastUsageUpdate(t, h.feed(t, usageUpdateLine(t, map[string]any{"cost": map[string]any{"amount": 1.25, "currency": "USD"}})))
			if math.Abs(u.CostUSD-tt.wantCost) > 1e-9 {
				t.Errorf("running cost = %v, want %v", u.CostUSD, tt.wantCost)
			}
			r := terminalResult(t, h.feed(t, promptResultLine(t, mustID(t, promptReq.ID), "end_turn", nil)))
			if math.Abs(r.TotalCostUSD-tt.wantCost) > 1e-9 {
				t.Errorf("terminal cost = %v, want %v", r.TotalCostUSD, tt.wantCost)
			}

			adj, err := h.p.AdditionalSessionCost(context.Background())
			if err != nil {
				t.Fatalf("AdditionalSessionCost: %v", err)
			}
			if math.Abs(adj.TotalCostUSD-tt.wantChildCost) > 1e-9 || adj.Usage.InputTokens != tt.wantChildTokens || strings.Join(adj.SessionIDs, ",") != tt.wantChildIDs {
				t.Errorf("child adjustment = %+v, want cost %v, %d input tokens, ids %q", adj, tt.wantChildCost, tt.wantChildTokens, tt.wantChildIDs)
			}
		})
	}
}
