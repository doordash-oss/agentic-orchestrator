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

package testutil_test

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestFakeClaudeCompactionAndUsage(t *testing.T) {
	path := testutil.WriteFakeClaudeScript(t, testutil.FakeClaudeInteractiveScriptBody())
	cmd := exec.Command("sh", path)
	cmd.Stdin = strings.NewReader("{\"subtype\":\"initialize\"}\n" +
		"{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"" + testutil.FakeSupervisorUsageHigh + "\"}}\n" +
		"{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"" + testutil.FakeSupervisorCompact + "\"}}\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	var high, low int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var msg llm.SDKMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		if msg.Compact != nil {
			kinds = append(kinds, "boundary")
		}
		if msg.User != nil && msg.User.IsCompactSummary {
			kinds = append(kinds, "summary")
		}
		if msg.Assistant != nil {
			kinds = append(kinds, "reply")
			if high == 0 {
				high = msg.Assistant.Message.Usage.InputTokens
			} else {
				low = msg.Assistant.Message.Usage.InputTokens
			}
		}
		if msg.Result != nil && msg.Result.ModelUsage["haiku"].ContextWindow != 200000 {
			t.Fatalf("result window = %+v", msg.Result.ModelUsage)
		}
	}
	if high < 160000 || low >= high || strings.Join(kinds, ",") != "reply,boundary,summary,reply" {
		t.Fatalf("high=%d low=%d kinds=%v", high, low, kinds)
	}
}
