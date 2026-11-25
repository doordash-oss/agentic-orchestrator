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
	"errors"
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
	err        error
}

func (r *sessionDBRunner) set(parentCost, children string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.parentCost, r.children = parentCost, children
}

func (r *sessionDBRunner) RunSQLite(_ context.Context, _, query string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
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
			runner.set("1.0\t0\t0\t0\t0\tses_x\n", "0.5\t50\t0\t0\t0\tchild-a\n")
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

			runner.set("1.25\t0\t0\t0\t0\tses_x\n", "0.8\t80\t0\t0\t0\tchild-a,child-b\n")
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

func TestReconciledUsageReadsSessionDatabase(t *testing.T) {
	priced := &llm.ModelCost{Input: 1, Output: 1}
	tests := []struct {
		name       string
		resumeID   string
		dbAtStart  string
		dbAtEnd    string
		noDB       bool
		runnerErr  error
		price      *llm.ModelCost
		streamCost float64
		turnUsage  map[string]any
		want       *llm.Usage
	}{
		{
			name:    "interrupted mid-turn records database spend",
			dbAtEnd: "0.14521732\t53917\t452\t188092\t0\tses_x\n",
			want:    &llm.Usage{CostUSD: 0.14521732, InputTokens: 53917, OutputTokens: 452, CacheReadInputTokens: 188092},
		},
		{
			name:       "completed turn matching the database is unchanged",
			dbAtEnd:    "0.5\t100\t10\t0\t0\tses_x\n",
			streamCost: 0.5,
			turnUsage:  map[string]any{"totalTokens": 110, "inputTokens": 100, "outputTokens": 10},
		},
		{
			name:       "completed multi-step turn takes database token totals",
			dbAtEnd:    "0.51\t4000\t600\t30000\t0\tses_x\n",
			streamCost: 0.51,
			turnUsage:  map[string]any{"totalTokens": 361, "inputTokens": 275, "outputTokens": 86},
			want:       &llm.Usage{CostUSD: 0.51, InputTokens: 4000, OutputTokens: 600, CacheReadInputTokens: 30000},
		},
		{
			name:       "later turn interrupted adds its completed steps",
			dbAtEnd:    "0.35\t3000\t300\t0\t0\tses_x\n",
			streamCost: 0.2,
			turnUsage:  map[string]any{"totalTokens": 2200, "inputTokens": 2000, "outputTokens": 200},
			want:       &llm.Usage{CostUSD: 0.35, InputTokens: 3000, OutputTokens: 300},
		},
		{
			name:      "resumed session subtracts the baseline",
			resumeID:  "ses_x",
			dbAtStart: "1.0\t1000\t100\t5000\t0\tses_x\n",
			dbAtEnd:   "1.3\t1500\t150\t8000\t0\tses_x\n",
			want:      &llm.Usage{CostUSD: 0.3, InputTokens: 500, OutputTokens: 50, CacheReadInputTokens: 3000},
		},
		{
			name:     "resumed session without a database baseline is unchanged",
			resumeID: "ses_x",
			noDB:     true,
		},
		{
			name:    "unpriced provider estimates from catalog price",
			dbAtEnd: "0\t1000000\t0\t0\t0\tses_x\n",
			price:   priced,
			want:    &llm.Usage{CostUSD: 1, CostSource: costSourceTokenEstimate, InputTokens: 1000000},
		},
		{
			name:    "unpriced provider without catalog price is unavailable",
			dbAtEnd: "0\t1000\t0\t0\t0\tses_x\n",
			want:    &llm.Usage{CostSource: costSourceUnavailable, InputTokens: 1000},
		},
		{
			name: "missing database is unchanged",
			noDB: true,
		},
		{
			name:      "unreadable database is unchanged",
			runnerErr: errors.New("database is locked"),
		},
		{
			name:    "missing session row is unchanged",
			dbAtEnd: "0\t0\t0\t0\t0\t\n",
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
			runner := &sessionDBRunner{err: tt.runnerErr}
			runner.set(tt.dbAtStart, "")
			h.p.costLookup = openCodeChildSessionCostLookup{dbPath: "/tmp/opencode.db", runner: runner}
			if tt.noDB {
				h.p.costLookup = openCodeChildSessionCostLookup{dbPath: "/nonexistent/opencode.db"}
			}

			errCh := make(chan error, 1)
			go func() { errCh <- h.p.Handshake(context.Background()) }()
			initReq := h.nextRequest(t)
			h.feed(t, responseLine(t, mustID(t, initReq.ID), initResultWithLoadSession(true)))
			sessReq := h.nextRequest(t)
			sessionResult := map[string]any{}
			if tt.resumeID == "" {
				sessionResult["sessionId"] = "ses_x"
			}
			h.feed(t, responseLine(t, mustID(t, sessReq.ID), sessionResult))
			promptReq := h.nextRequest(t)
			if err := <-errCh; err != nil {
				t.Fatalf("Handshake: %v", err)
			}
			h.p.price = tt.price

			h.feed(t, usageUpdateLine(t, map[string]any{"used": 1000, "size": 200000, "cost": nil}))
			if tt.streamCost > 0 {
				h.feed(t, usageUpdateLine(t, map[string]any{"cost": map[string]any{"amount": tt.streamCost, "currency": "USD"}}))
			}
			if tt.turnUsage != nil {
				h.feed(t, promptResultLine(t, mustID(t, promptReq.ID), "end_turn", tt.turnUsage))
			}
			runner.set(tt.dbAtEnd, "")

			got := h.p.ReconciledUsage(context.Background())
			if tt.want == nil {
				if got != nil {
					t.Fatalf("ReconciledUsage = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("ReconciledUsage = nil, want database usage")
			}
			if math.Abs(got.CostUSD-tt.want.CostUSD) > 1e-9 || got.CostSource != tt.want.CostSource {
				t.Errorf("cost = %v (%q), want %v (%q)", got.CostUSD, got.CostSource, tt.want.CostUSD, tt.want.CostSource)
			}
			if got.InputTokens != tt.want.InputTokens || got.OutputTokens != tt.want.OutputTokens ||
				got.CacheReadInputTokens != tt.want.CacheReadInputTokens || got.CacheCreationInputTokens != tt.want.CacheCreationInputTokens {
				t.Errorf("tokens = %+v, want %+v", got, tt.want)
			}
			if got.ContextWindow != 200000 {
				t.Errorf("context window = %d, want streamed 200000", got.ContextWindow)
			}
		})
	}
}
