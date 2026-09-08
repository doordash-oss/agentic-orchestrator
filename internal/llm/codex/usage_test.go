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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func parseUsageLine(t *testing.T, p *Protocol, line string) []llm.SDKMessage {
	t.Helper()
	messages, err := p.ParseLine([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	return messages
}

func sendUsage(t *testing.T, p *Protocol, thread string, input, cached, writes, output int) llm.SDKMessage {
	t.Helper()
	breakdown := TokenUsageBreakdown{InputTokens: input, CachedInputTokens: cached, CacheWriteInputTokens: writes, OutputTokens: output, TotalTokens: input + output}
	raw, err := json.Marshal(TokenUsageUpdatedParams{ThreadID: thread, TurnID: "turn-1", TokenUsage: ThreadTokenUsage{Total: breakdown, Last: breakdown}})
	if err != nil {
		t.Fatal(err)
	}
	messages := parseUsageLine(t, p, `{"method":"thread/tokenUsage/updated","params":`+string(raw)+`}`)
	if len(messages) == 0 {
		return llm.SDKMessage{}
	}
	return messages[0]
}

func completeForUsage(t *testing.T, p *Protocol, out *bytes.Buffer, status string) int {
	t.Helper()
	out.Reset()
	messages := parseUsageLine(t, p, fmt.Sprintf(`{"method":"turn/completed","params":{"threadId":"root","turn":{"id":"turn-1","status":%q}}}`, status))
	if len(messages) != 1 || messages[0].Result == nil {
		t.Fatalf("expected one terminal result: %+v", messages)
	}
	var request struct {
		ID     int
		Method string
		Params struct {
			ThreadID string `json:"threadId"`
		}
	}
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &request); err != nil {
		t.Fatal(err)
	}
	if request.Method != "account/usage/read" || request.Params.ThreadID != "root" {
		t.Fatalf("wrong lookup: %+v", request)
	}
	return request.ID
}

func replyUsage(t *testing.T, p *Protocol, id int, body string) []llm.SDKMessage {
	t.Helper()
	return parseUsageLine(t, p, fmt.Sprintf(`{"id":%d,"result":%s}`, id, body))
}

func assertUsageCost(t *testing.T, p *Protocol, want float64) *llm.Usage {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Read current fallback even if a request remains outstanding.
	usage := p.ReconciledUsage(ctx)
	if usage == nil || math.Abs(usage.CostUSD-want) > 0.00000001 {
		t.Fatalf("usage = %+v, want cost %.8f", usage, want)
	}
	return usage
}

func TestUsageReconciliationReplacesEstimateAndRetainsCredits(t *testing.T) {
	for _, status := range []string{"completed", "failed", "interrupted"} {
		t.Run(status, func(t *testing.T) {
			p := NewProtocol(llm.ProtocolOpts{Model: "gpt-6-astra", Interactive: true})
			var out bytes.Buffer
			p.SetStdin(&out)
			p.SetThreadIDForTest("root")
			// 100K regular + 50K cached + 50K writes + 10K output.
			sendUsage(t, p, "root", 200_000, 50_000, 50_000, 10_000)
			assertUsageCost(t, p, 2.175)
			id := completeForUsage(t, p, &out, status)
			messages := replyUsage(t, p, id, `{"threadUsage":{"threadId":"root","estimatedUsageUsdMicros":1234567,"estimatedUsageCreditsMicros":9876543}}`)
			if len(messages) != 1 || messages[0].Type != "usage_update" || messages[0].Result != nil {
				t.Fatalf("billing created a terminal event: %+v", messages)
			}
			usage := assertUsageCost(t, p, 1.234567)
			if usage.CacheCreationInputTokens != 50_000 || usage.CostSource != "provider_estimate" || usage.CostCreditsMicros == nil || *usage.CostCreditsMicros != 9876543 {
				t.Fatalf("lost billing metadata: %+v", usage)
			}
			// Replay cannot apply a cumulative estimate twice.
			replyUsage(t, p, id, `{"threadUsage":{"threadId":"root","estimatedUsageUsdMicros":1234567}}`)
			assertUsageCost(t, p, 1.234567)
			// Same token snapshot must not undo the reconciled cost.
			sendUsage(t, p, "root", 200_000, 50_000, 50_000, 10_000)
			assertUsageCost(t, p, 1.234567)
			// New inference adds only its incremental cost to the reconciled base.
			sendUsage(t, p, "root", 210_000, 50_000, 50_000, 11_000)
			assertUsageCost(t, p, 1.384567)
		})
	}
}

func TestUsageReconciliationMissingMalformedAndZeroUSD(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       float64
		credits    bool
	}{
		{"missing", `{}`, 1, false},
		{"unavailable", `{"threadUsage":null}`, 1, false},
		{"credits only", `{"threadUsage":{"threadId":"root","estimatedUsageUsdMicros":null,"estimatedUsageCreditsMicros":42}}`, 1, true},
		{"zero", `{"threadUsage":{"threadId":"root","estimatedUsageUsdMicros":0,"estimatedUsageCreditsMicros":0}}`, 0, true},
		{"wrong thread", `{"threadUsage":{"threadId":"child","estimatedUsageUsdMicros":10}}`, 1, false},
		{"malformed", `{"threadUsage":{"threadId":"root","estimatedUsageUsdMicros":"1.5"}}`, 1, false},
		{"negative", `{"threadUsage":{"threadId":"root","estimatedUsageUsdMicros":-1}}`, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewProtocol(llm.ProtocolOpts{Model: "gpt-6-astra", Interactive: true})
			var out bytes.Buffer
			p.SetStdin(&out)
			p.SetThreadIDForTest("root")
			sendUsage(t, p, "root", 100_000, 0, 0, 0)
			id := completeForUsage(t, p, &out, "completed")
			replyUsage(t, p, id, tc.body)
			usage := assertUsageCost(t, p, tc.want)
			if (usage.CostCreditsMicros != nil) != tc.credits {
				t.Fatalf("credits: %+v", usage)
			}
		})
	}
}

func TestUsageStaleRepliesAndChildCountersDoNotCorruptRoot(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{Model: "gpt-6-astra", Interactive: true})
	var out bytes.Buffer
	p.SetStdin(&out)
	p.SetThreadIDForTest("root")
	sendUsage(t, p, "root", 100_000, 0, 0, 0)
	first := completeForUsage(t, p, &out, "completed")
	sendUsage(t, p, "child", 900_000, 0, 0, 0)
	assertUsageCost(t, p, 1)
	sendUsage(t, p, "root", 110_000, 0, 0, 0)
	replyUsage(t, p, first, `{"threadUsage":{"threadId":"root","estimatedUsageUsdMicros":99}}`)
	assertUsageCost(t, p, 1.1)
	// Regression must not lower the token baseline then charge it again.
	sendUsage(t, p, "root", 100_000, 0, 0, 0)
	sendUsage(t, p, "root", 110_000, 0, 0, 0)
	assertUsageCost(t, p, 1.1)
	old := completeForUsage(t, p, &out, "completed")
	latest := completeForUsage(t, p, &out, "completed")
	replyUsage(t, p, old, `{"threadUsage":{"threadId":"root","estimatedUsageUsdMicros":99}}`)
	assertUsageCost(t, p, 1.1)
	replyUsage(t, p, latest, `{"threadUsage":{"threadId":"root","estimatedUsageUsdMicros":500000}}`)
	assertUsageCost(t, p, 0.5)
}

func TestUsageLookupUnsupportedAndClosedNeverFailTurn(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{Model: "gpt-6-astra", Interactive: true})
	var out bytes.Buffer
	p.SetStdin(&out)
	p.SetThreadIDForTest("root")
	sendUsage(t, p, "root", 100_000, 0, 0, 0)
	id := completeForUsage(t, p, &out, "completed")
	messages := parseUsageLine(t, p, fmt.Sprintf(`{"id":%d,"error":{"code":-32601,"message":"Method not found"}}`, id))
	if len(messages) != 0 {
		t.Fatalf("optional lookup became agent error: %+v", messages)
	}
	assertUsageCost(t, p, 1)
	out.Reset()
	p.requestUsageRead()
	if out.Len() != 0 {
		t.Fatal("retried unsupported billing method")
	}
	p.Close()
	assertUsageCost(t, p, 1)
}

func TestUsageLookupWaitIsBoundedAndCloseUnblocks(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{Model: "gpt-6-astra", Interactive: true})
	var out bytes.Buffer
	p.SetStdin(&out)
	p.SetThreadIDForTest("root")
	sendUsage(t, p, "root", 100_000, 0, 0, 0)
	completeForUsage(t, p, &out, "completed")
	assertUsageCost(t, p, 1) // Canceled context returns fallback immediately.
	result := make(chan *llm.Usage, 1)
	go func() { result <- p.ReconciledUsage(context.Background()) }()
	p.Close()
	usage := <-result
	if usage.CostUSD != 1 {
		t.Fatalf("lost fallback on close: %+v", usage)
	}
}

func TestResumeUsageDoesNotChargeHistoricalTokensAgain(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{Model: "gpt-6-astra", ResumeSessionID: "root", Interactive: true})
	var out bytes.Buffer
	p.SetStdin(&out)
	parseUsageLine(t, p, `{"id":1,"result":{"thread":{"id":"root"},"model":"gpt-6-astra","serviceTier":"fast"}}`)
	var request Request
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &request); err != nil {
		t.Fatal(err)
	}
	replyUsage(t, p, request.ID, `{"threadUsage":{"threadId":"root","estimatedUsageUsdMicros":500000}}`)
	sendUsage(t, p, "root", 200_000, 0, 0, 0)
	assertUsageCost(t, p, 0.5)
	sendUsage(t, p, "root", 210_000, 0, 0, 0)
	assertUsageCost(t, p, 0.7) // Only 10K new tokens at fast rates.
}

func TestAstraReroutingAndTierAffectLiveEstimate(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{Model: "gpt-6-astra"})
	parseUsageLine(t, p, `{"id":1,"result":{"thread":{"id":"root"},"model":"gpt-6-astra","serviceTier":"priority"}}`)
	sendUsage(t, p, "root", 100_000, 0, 0, 0)
	assertUsageCost(t, p, 2)
	parseUsageLine(t, p, `{"method":"model/rerouted","params":{"threadId":"root","turnId":"turn-1","toModel":"gpt-5.6-luna"}}`)
	sendUsage(t, p, "root", 200_000, 0, 0, 0)
	assertUsageCost(t, p, 2.04)
}

func TestUnknownModelHasNoInventedPrice(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{Model: "gpt-future"})
	sendUsage(t, p, "root", 100_000, 0, 0, 0)
	if usage := assertUsageCost(t, p, 0); usage.CostSource != "unavailable" {
		t.Fatalf("unknown rate: %+v", usage)
	}
}
