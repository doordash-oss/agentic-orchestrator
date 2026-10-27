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

package supervisor

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func trimUser(seq int64, turn string) Record {
	data, _ := json.Marshal(UserData{Text: "question " + turn})
	return Record{Seq: seq, TurnID: turn, Kind: KindUser, Visibility: VisibilityContent, Data: data}
}

func trimResult(t *testing.T, seq int64, turn string, content json.RawMessage) Record {
	t.Helper()
	data, err := json.Marshal(ContentData{Content: []llm.ContentBlock{{Type: "tool_result", ToolUseID: "call-" + strconv.FormatInt(seq, 10), Content: content}}})
	if err != nil {
		t.Fatal(err)
	}
	return Record{Seq: seq, TurnID: turn, Kind: KindToolResult, Visibility: VisibilityContent, Data: data}
}

func trimString(s string) json.RawMessage {
	raw, _ := json.Marshal(s)
	return raw
}

// trimTurns builds n turns, each a user message and one tool result.
func trimTurns(t *testing.T, n int, content func(turn int) json.RawMessage) []Record {
	t.Helper()
	var recs []Record
	for i := range n {
		turn := "t" + strconv.Itoa(i)
		recs = append(recs, trimUser(int64(2*i+1), turn), trimResult(t, int64(2*i+2), turn, content(i)))
	}
	return recs
}

func resultText(t *testing.T, rec Record) string {
	t.Helper()
	var cd ContentData
	if err := json.Unmarshal(rec.Data, &cd); err != nil {
		t.Fatal(err)
	}
	return toolResultsText(t, cd.Content[0].Content)
}

func toolResultsText(t *testing.T, content json.RawMessage) string {
	t.Helper()
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s
	}
	var items []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &items); err != nil {
		t.Fatal(err)
	}
	var parts []string
	for _, it := range items {
		parts = append(parts, it.Type+":"+it.Text)
	}
	return strings.Join(parts, "|")
}

func TestTrimToolResults(t *testing.T) {
	big := strings.Repeat("a", 1500) + strings.Repeat("m", 3000) + strings.Repeat("z", 1500)
	tests := []struct {
		name    string
		records func(t *testing.T) []Record
		check   func(t *testing.T, out []Record, stats TrimStats)
	}{
		{
			name: "keeps the last turns whole",
			records: func(t *testing.T) []Record {
				return trimTurns(t, trimKeepTurns, func(int) json.RawMessage { return trimString(big) })
			},
			check: func(t *testing.T, out []Record, stats TrimStats) {
				if stats.Trimmed != 0 || stats.Cleared != 0 {
					t.Fatalf("stats = %+v, want nothing trimmed", stats)
				}
				for i := 1; i < len(out); i += 2 {
					if got := resultText(t, out[i]); got != big {
						t.Fatalf("record %d trimmed to %d characters", out[i].Seq, len(got))
					}
				}
			},
		},
		{
			name: "trims an old string result to its head and tail",
			records: func(t *testing.T) []Record {
				return trimTurns(t, trimKeepTurns+1, func(int) json.RawMessage { return trimString(big) })
			},
			check: func(t *testing.T, out []Record, stats TrimStats) {
				if stats.Trimmed != 1 || stats.Cleared != 0 {
					t.Fatalf("stats = %+v, want one trimmed", stats)
				}
				got := resultText(t, out[1])
				want := strings.Repeat("a", 1500) + "\n[... 3000 characters trimmed when Agentico restored this session ...]\n" + strings.Repeat("z", 1500)
				if got != want {
					t.Fatalf("trimmed result = %q", got)
				}
				if got := resultText(t, out[3]); got != big {
					t.Fatalf("protected result trimmed to %d characters", len(got))
				}
			},
		},
		{
			name: "trims text items of a block result and keeps images",
			records: func(t *testing.T) []Record {
				content := json.RawMessage(`[{"type":"text","text":` + string(trimString(big)) + `},{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}]`)
				return trimTurns(t, trimKeepTurns+1, func(int) json.RawMessage { return content })
			},
			check: func(t *testing.T, out []Record, stats TrimStats) {
				if stats.Trimmed != 1 {
					t.Fatalf("stats = %+v, want one trimmed", stats)
				}
				got := resultText(t, out[1])
				if !strings.Contains(got, "3000 characters trimmed") || !strings.HasSuffix(got, "|image:") {
					t.Fatalf("block result = %q", got)
				}
			},
		},
		{
			name: "clears the oldest results over the budget",
			records: func(t *testing.T) []Record {
				chunk := strings.Repeat("x", trimMaxResultChars)
				n := trimBudgetChars/trimMaxResultChars + 2
				return trimTurns(t, n+trimKeepTurns, func(int) json.RawMessage { return trimString(chunk) })
			},
			check: func(t *testing.T, out []Record, stats TrimStats) {
				if stats.Trimmed != 0 || stats.Cleared == 0 || stats.CharsAfter > trimBudgetChars {
					t.Fatalf("stats = %+v, want results cleared to fit %d characters", stats, trimBudgetChars)
				}
				for i := range stats.Cleared {
					if got := resultText(t, out[2*i+1]); got != ClearedToolResult {
						t.Fatalf("result %d = %.40q, want it cleared", i, got)
					}
				}
				if got := resultText(t, out[2*stats.Cleared+1]); got != strings.Repeat("x", trimMaxResultChars) {
					t.Fatalf("first kept result = %.40q, want it whole", got)
				}
			},
		},
		{
			name: "leaves small results and the input untouched",
			records: func(t *testing.T) []Record {
				return trimTurns(t, trimKeepTurns+2, func(int) json.RawMessage { return trimString("ok") })
			},
			check: func(t *testing.T, out []Record, stats TrimStats) {
				if stats.Trimmed != 0 || stats.Cleared != 0 || stats.CharsBefore != 4 {
					t.Fatalf("stats = %+v", stats)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := tt.records(t)
			snapshot := make([][]byte, len(in))
			for i := range in {
				snapshot[i] = bytes.Clone(in[i].Data)
			}
			out, stats := TrimToolResults(in)
			for i := range in {
				if !bytes.Equal(in[i].Data, snapshot[i]) {
					t.Fatalf("input record %d was modified", in[i].Seq)
				}
			}
			again, _ := TrimToolResults(in)
			for i := range out {
				if !bytes.Equal(out[i].Data, again[i].Data) {
					t.Fatalf("record %d differs between runs", out[i].Seq)
				}
			}
			tt.check(t, out, stats)
		})
	}
}

func TestClipMiddle(t *testing.T) {
	t.Parallel()
	if got := ClipMiddle("short", 3, 3, "omitted"); got != "short" {
		t.Errorf("ClipMiddle(short) = %q", got)
	}
	want := "ab\n[... 8 characters omitted ...]\nkl"
	if got := ClipMiddle("abcdefghijkl", 2, 2, "omitted"); got != want {
		t.Errorf("ClipMiddle = %q, want %q", got, want)
	}
}

func TestToolResultItemPlaceholder(t *testing.T) {
	t.Parallel()
	cases := []struct{ typ, item, want string }{
		{"tool_reference", `{"type":"tool_reference","tool_name":"TaskStop"}`, "[tool reference: TaskStop]"},
		{"tool_reference", `{"type":"tool_reference"}`, "[tool reference]"},
		{"document", `{"type":"document"}`, "[document content omitted: could not be restored]"},
		{"", `{}`, "[unknown content omitted: could not be restored]"},
	}
	for _, tc := range cases {
		if got := ToolResultItemPlaceholder(tc.typ, json.RawMessage(tc.item)); got != tc.want {
			t.Errorf("ToolResultItemPlaceholder(%q, %s) = %q, want %q", tc.typ, tc.item, got, tc.want)
		}
	}
}
