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

package codexsession

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

// orphanedResultTurn is a turn whose tool results answer no call, so the
// selection drops them from the replay.
func orphanedResultTurn(seq int64, turn string, n int, output string) []supervisor.Record {
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	result, _ := json.Marshal(output)
	recs := []supervisor.Record{{Seq: seq, TurnID: turn, Kind: supervisor.KindUser, Visibility: supervisor.VisibilityContent, CreatedAt: at,
		Data: json.RawMessage(`{"text":"orphans"}`)}}
	for i := range n {
		recs = append(recs, supervisor.Record{Seq: seq + 1 + int64(i), TurnID: turn, Kind: supervisor.KindToolResult, Visibility: supervisor.VisibilityContent, CreatedAt: at,
			Data: json.RawMessage(`{"content":[{"type":"tool_result","tool_use_id":"orphan-` + strconv.Itoa(i) + `","content":` + string(result) + `}]}`)})
	}
	return recs
}

func TestRebuildExcludedResultsDoNotSpendTrimBudget(t *testing.T) {
	valid := "KEEPME" + strings.Repeat("v", 3000)
	recs := toolTurns(1, valid)
	recs = append(recs, orphanedResultTurn(10, "turn-orphans", 60, strings.Repeat("x", 3500))...)
	tail := toolTurns(3, "ok")
	for i := range tail {
		tail[i].Seq += 1000
		tail[i].TurnID = "tail-" + tail[i].TurnID
	}
	recs = append(recs, tail...)
	res, err := converterAt(t.TempDir()).Rebuild(context.Background(), input(recs))
	if err != nil {
		t.Fatal(err)
	}
	if res.Trim.Cleared != 0 || res.Trim.Trimmed != 0 {
		t.Fatalf("trim = %+v, want the replayed 3KB result left alone", res.Trim)
	}
	data, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), valid) || strings.Contains(string(data), supervisor.ClearedToolResult) {
		t.Fatal("the replayed result was cleared by results the replay does not contain")
	}
}
