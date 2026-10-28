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

package opencodesession

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor"
)

func TestRenderTrimsOldToolResultsBeforeBudget(t *testing.T) {
	t.Parallel()
	output, _ := json.Marshal(strings.Repeat("a", 1500) + strings.Repeat("m", 10_000) + "END")
	var recs []supervisor.Record
	for i := range 4 {
		turn, id := "turn-"+strconv.Itoa(i), "call-"+strconv.Itoa(i)
		seq := int64(3*i + 1)
		recs = append(recs,
			user(seq, turn, "question "+strconv.Itoa(i)),
			supervisor.Record{Seq: seq + 1, TurnID: turn, Kind: supervisor.KindToolUse, Visibility: supervisor.VisibilityContent,
				Data: json.RawMessage(`{"content":[{"type":"tool_use","id":"` + id + `","name":"Bash","input":{"command":"cat big.log"}}]}`)},
			supervisor.Record{Seq: seq + 2, TurnID: turn, Kind: supervisor.KindToolResult, Visibility: supervisor.VisibilityContent,
				Data: json.RawMessage(`{"content":[{"type":"tool_result","tool_use_id":"` + id + `","content":` + string(output) + `}]}`)},
		)
	}
	data, err := Render(input("", recs), Options{})
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	// The oldest result is trimmed to 3000 characters before clipping to 2000.
	if got := strings.Count(text, "  [... 1"); got != 1 {
		t.Errorf("clip markers of the trimmed result = %d, want 1", got)
	}
	if got := strings.Count(text, "  [... 9"); got != 3 {
		t.Errorf("clip markers of whole results = %d, want 3", got)
	}
}
