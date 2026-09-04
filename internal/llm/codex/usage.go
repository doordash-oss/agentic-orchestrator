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

package codex

import (
	"context"
	"encoding/json"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// usageState is protected by Protocol.mu. Only the latest lookup is retained;
// negative request IDs let us harmlessly discard superseded replies and errors.
type usageState struct {
	resumeBaselinePending bool
	revision              uint64
	pending               *usageRead
	disabled              bool
	closed                bool
	seen                  bool
	source                string
	creditsMicros         *int64
	last                  TokenUsageBreakdown
}

type usageRead struct {
	id       int
	revision uint64
	done     chan struct{}
}

type accountUsageResult struct {
	ThreadUsage *struct {
		ThreadID      string `json:"threadId"`
		USDMicros     *int64 `json:"estimatedUsageUsdMicros"`
		CreditsMicros *int64 `json:"estimatedUsageCreditsMicros"`
	} `json:"threadUsage"`
}

// requestUsageRead sends one optional request on the existing RPC connection.
// It never waits for the server, starts another process, or polls account data.
func (p *Protocol) requestUsageRead() {
	p.mu.Lock()
	if p.opts.NativeToollessReview || p.usageState.disabled || p.usageState.closed || p.threadID == "" || p.stdin == nil {
		p.mu.Unlock()
		return
	}
	p.finishUsageReadLocked()
	request := &usageRead{id: -int(nextID.Add(1)), revision: p.usageState.revision, done: make(chan struct{})}
	p.usageState.pending = request
	threadID := p.threadID
	p.mu.Unlock()
	err := p.writeJSON(Request{
		JSONRPC: "2.0", ID: request.id, Method: "account/usage/read",
		Params: struct {
			ThreadID string `json:"threadId"`
		}{threadID},
	})
	if err != nil {
		p.mu.Lock()
		if p.usageState.pending == request {
			p.finishUsageReadLocked()
		}
		p.mu.Unlock()
		p.logDebug("[codex] optional usage lookup unavailable: %v", err)
	}
}

func (p *Protocol) finishUsageReadLocked() {
	if pending := p.usageState.pending; pending != nil {
		close(pending.done)
		p.usageState.pending = nil
	}
}

func (p *Protocol) handleUsageResponse(id int, result, errData json.RawMessage) (llm.SDKMessage, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	request := p.usageState.pending
	if request == nil || request.id != id {
		return llm.SDKMessage{}, false
	}
	defer p.finishUsageReadLocked()
	if len(errData) > 0 && string(errData) != "null" {
		var rpcError struct {
			Code int `json:"code"`
		}
		if json.Unmarshal(errData, &rpcError) == nil && rpcError.Code == -32601 {
			// Older Codex versions do not implement account/usage/read.
			p.usageState.disabled = true
		}
		return llm.SDKMessage{}, false
	}
	// A response crossing new inference activity cannot safely replace its
	// cost: the API has no usage watermark. Keep the live estimate until the
	// next turn's lookup instead of double-counting or losing newer tokens.
	if request.revision != p.usageState.revision {
		return llm.SDKMessage{}, false
	}
	var response accountUsageResult
	if json.Unmarshal(result, &response) != nil || response.ThreadUsage == nil {
		return llm.SDKMessage{}, false
	}
	usage := response.ThreadUsage
	if usage.ThreadID != p.threadID {
		return llm.SDKMessage{}, false
	}
	if usage.USDMicros != nil && *usage.USDMicros < 0 || usage.CreditsMicros != nil && *usage.CreditsMicros < 0 {
		return llm.SDKMessage{}, false
	}
	if usage.USDMicros == nil && usage.CreditsMicros == nil {
		return llm.SDKMessage{}, false
	}
	p.usageState.seen = true
	p.usageState.creditsMicros = usage.CreditsMicros
	if usage.USDMicros != nil {
		// Zero is a valid provider estimate. Never add a cumulative cost twice.
		p.totalCostUSD = float64(*usage.USDMicros) / 1_000_000
		p.usageState.source = "provider_estimate"
	}
	return p.usageMessageLocked(), true
}

// ReconciledUsage lets final accounting wait for an already-issued lookup.
// Cancellation, unsupported billing, and process closure retain the estimate.
func (p *Protocol) ReconciledUsage(ctx context.Context) *llm.Usage {
	p.mu.Lock()
	pending := p.usageState.pending
	p.mu.Unlock()
	if pending != nil {
		select {
		case <-pending.done:
		case <-ctx.Done():
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.usageState.seen {
		return nil
	}
	usage := p.usageSnapshotLocked()
	return &usage
}

func (p *Protocol) updateTokenUsageLocked(usage ThreadTokenUsage) {
	total := usage.Total
	inputDelta := total.InputTokens - p.inputTokens
	cachedDelta := total.CachedInputTokens - p.cachedInputTokens
	writeDelta := total.CacheWriteInputTokens - p.cacheWriteInputTokens
	outputDelta := total.OutputTokens - p.outputTokens
	// Replayed or regressing snapshots must not reset the baseline: the next
	// ordinary update would otherwise charge those same tokens again.
	if inputDelta < 0 || cachedDelta < 0 || writeDelta < 0 || outputDelta < 0 {
		return
	}
	historical := p.usageState.resumeBaselinePending
	p.usageState.resumeBaselinePending = false
	if !historical && (inputDelta > 0 || cachedDelta > 0 || writeDelta > 0 || outputDelta > 0) {
		p.usageState.revision++
		model := p.pricingModel
		if model == "" {
			model = p.model
		}
		p.totalCostUSD += computeCostForServiceTier(model, p.serviceTier, inputDelta, cachedDelta, writeDelta, outputDelta, usage.Last.InputTokens)
		if _, ok := lookupRate(model); ok {
			p.usageState.source = "token_estimate"
		} else {
			p.usageState.source = "unavailable"
		}
	}
	p.inputTokens = total.InputTokens
	p.cachedInputTokens = total.CachedInputTokens
	p.cacheWriteInputTokens = total.CacheWriteInputTokens
	p.outputTokens = total.OutputTokens
	p.usageState.last = usage.Last
	p.usageState.seen = true
	if usage.ModelContextWindow != nil {
		p.modelContextWindow = *usage.ModelContextWindow
	}
}

func (p *Protocol) usageSnapshotLocked() llm.Usage {
	source := p.usageState.source
	if source == "" {
		source = "unavailable"
	}
	return llm.Usage{
		InputTokens:              p.inputTokens,
		CacheReadInputTokens:     p.cachedInputTokens,
		CacheCreationInputTokens: p.cacheWriteInputTokens,
		OutputTokens:             p.outputTokens,
		ContextInputTokens:       p.usageState.last.InputTokens,
		ContextTotalTokens:       p.usageState.last.TotalTokens,
		ContextBaseline:          codexContextBaselineTokens,
		ContextWindow:            p.modelContextWindow,
		CostUSD:                  p.totalCostUSD,
		CostSource:               source,
		CostCreditsMicros:        p.usageState.creditsMicros,
	}
}

func (p *Protocol) usageMessageLocked() llm.SDKMessage {
	usage := p.usageSnapshotLocked()
	return llm.SDKMessage{Type: "usage_update", UsageUpdate: &usage}
}

func (p *Protocol) withUsage(msg llm.SDKMessage) llm.SDKMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	if msg.Result != nil && p.usageState.seen {
		usage := p.usageSnapshotLocked()
		msg.Result.Usage = &usage
		msg.Result.TotalCostUSD = usage.CostUSD
	}
	return msg
}
