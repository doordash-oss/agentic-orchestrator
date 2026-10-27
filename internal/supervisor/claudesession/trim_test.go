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

package claudesession

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor"
)

// toolTurns builds n turns, each a user message, a Bash call and a result of
// output.
func toolTurns(n int, output string) []supervisor.Record {
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	result, _ := json.Marshal(output)
	var recs []supervisor.Record
	for i := range n {
		turn, id := "turn-"+strconv.Itoa(i), "call-"+strconv.Itoa(i)
		seq := int64(3*i + 1)
		recs = append(recs,
			supervisor.Record{Seq: seq, TurnID: turn, Kind: supervisor.KindUser, Visibility: supervisor.VisibilityContent, CreatedAt: at,
				Data: json.RawMessage(`{"text":"question ` + strconv.Itoa(i) + `"}`)},
			supervisor.Record{Seq: seq + 1, TurnID: turn, Kind: supervisor.KindToolUse, Visibility: supervisor.VisibilityContent, CreatedAt: at,
				Data: json.RawMessage(`{"content":[{"type":"tool_use","id":"` + id + `","name":"Bash","input":{"command":"cat big.log"}}]}`)},
			supervisor.Record{Seq: seq + 2, TurnID: turn, Kind: supervisor.KindToolResult, Visibility: supervisor.VisibilityContent, CreatedAt: at,
				Data: json.RawMessage(`{"content":[{"type":"tool_result","tool_use_id":"` + id + `","content":` + string(result) + `}]}`)},
		)
	}
	return recs
}

func TestRebuildTrimsOldToolResults(t *testing.T) {
	output := strings.Repeat("a", 1500) + strings.Repeat("m", 10_000) + strings.Repeat("z", 1500)
	res, err := converterAt(t.TempDir()).Rebuild(context.Background(), input(toolTurns(4, output)))
	if err != nil {
		t.Fatal(err)
	}
	if res.Trim.Trimmed != 1 || res.Trim.Cleared != 0 {
		t.Fatalf("trim = %+v, want the oldest of four results trimmed", res.Trim)
	}
	data, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "10000 characters trimmed when Agentico restored this session"); got != 1 {
		t.Fatalf("trim notes = %d, want 1", got)
	}
	if got := strings.Count(string(data), strings.Repeat("m", 10_000)); got != 3 {
		t.Fatalf("whole results = %d, want the last 3", got)
	}
}
