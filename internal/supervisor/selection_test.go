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
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// selPart and selEntry are a synthetic part and entry type: enough shape to
// exercise every rule of the shared selection over one small payload.
type selPart struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	Text string `json:"text,omitempty"`
}

type selEntry struct {
	role   string
	prompt *string
	parts  []selPart
}

type selPayload struct {
	Role   string    `json:"role"`
	Prompt *string   `json:"prompt,omitempty"`
	Parts  []selPart `json:"parts"`
}

const (
	selKindUse    = "tool_use"
	selKindResult = "tool_result"
)

// selRules builds rules over the synthetic types. promptCounts emulates the
// converters' split: Claude counts a prompt-only entry as non-empty, Codex
// and OpenCode do not.
func selRules(promptCounts bool) SelectionRules[selEntry, selPart] {
	return SelectionRules[selEntry, selPart]{
		Decode: func(rec *Record) (selEntry, error) {
			var p selPayload
			if err := DecodeRecordPayload(rec, &p); err != nil {
				return selEntry{}, err
			}
			return selEntry{role: p.Role, prompt: p.Prompt, parts: p.Parts}, nil
		},
		Parts:          func(e *selEntry) []selPart { return e.parts },
		SetParts:       func(e *selEntry, parts []selPart) { e.parts = parts },
		PartKind:       func(t selPart) string { return t.Kind },
		PartID:         func(t selPart) string { return t.ID },
		ToolUseKind:    selKindUse,
		ToolResultKind: selKindResult,
		EntryKey:       selKey,
		NonEmpty: func(e selEntry) bool {
			if promptCounts {
				return e.prompt != nil || len(e.parts) > 0
			}
			return len(e.parts) > 0
		},
	}
}

// selKey keys on role, prompt and part content, as the converters key on
// rendered content.
func selKey(e selEntry) string {
	var sb strings.Builder
	sb.WriteString(e.role)
	if e.prompt != nil {
		sb.WriteString("\x00prompt\x00")
		sb.WriteString(*e.prompt)
	}
	for _, p := range e.parts {
		sb.WriteByte(0)
		sb.WriteString(p.Kind)
		sb.WriteByte(':')
		sb.WriteString(p.ID)
		sb.WriteByte(':')
		sb.WriteString(p.Text)
	}
	return sb.String()
}

// selRec builds one selected-visibility record from a payload.
func selRec(turn, payload string) Record {
	return Record{Seq: 1, TurnID: turn, Kind: KindAssistant, Visibility: VisibilityContent, Data: json.RawMessage(payload)}
}

func selMarker(turn string) Record {
	return Record{Seq: 1, TurnID: turn, Kind: KindMarker, Visibility: VisibilityDisplayOnly,
		Data: json.RawMessage(`{"marker":"` + MarkerInterrupted + `"}`)}
}

// renderSel reduces entries to a compact comparable form: "role|prompt"
// with each part appended as " kind:id".
func renderSel(entries []selEntry) []string {
	var out []string
	for _, e := range entries {
		s := e.role
		if e.prompt != nil {
			s += "|" + *e.prompt
		}
		for _, p := range e.parts {
			s += " " + p.Kind + ":" + p.ID
		}
		out = append(out, s)
	}
	return out
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestSelectEntriesInterruptedTurnDropsToolKindsOnlyThere(t *testing.T) {
	records := []Record{
		selMarker("t1"),
		selRec("t1", `{"role":"assistant","parts":[{"kind":"message","text":"partial"},{"kind":"tool_use","id":"a"},{"kind":"tool_result","id":"a"}]}`),
		selRec("t2", `{"role":"assistant","parts":[{"kind":"tool_use","id":"b"},{"kind":"tool_result","id":"b"}]}`),
	}
	got, err := SelectEntries(records, selRules(true))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"assistant message:", "assistant tool_use:b tool_result:b"}
	if ss := renderSel(got); !equalStrings(ss, want) {
		t.Fatalf("entries = %v, want %v", ss, want)
	}
}

func TestSelectEntriesToolPairing(t *testing.T) {
	cases := []struct {
		name    string
		records []Record
		want    []string
	}{
		{
			name: "orphan use dropped with its entry kept for its message",
			records: []Record{
				selRec("t1", `{"role":"assistant","parts":[{"kind":"message","text":"one"},{"kind":"tool_use","id":"x"}]}`),
			},
			want: []string{"assistant message:"},
		},
		{
			name: "orphan use empties its entry",
			records: []Record{
				selRec("t1", `{"role":"assistant","parts":[{"kind":"tool_use","id":"x"}]}`),
			},
			want: nil,
		},
		{
			name: "orphan result dropped",
			records: []Record{
				selRec("t1", `{"role":"user","parts":[{"kind":"tool_result","id":"y"}]}`),
			},
			want: nil,
		},
		{
			name: "duplicate ids keep the first use and the first result",
			records: []Record{
				selRec("t1", `{"role":"assistant","parts":[{"kind":"tool_use","id":"a"},{"kind":"tool_result","id":"a"}]}`),
				selRec("t2", `{"role":"assistant","parts":[{"kind":"tool_use","id":"a"},{"kind":"tool_result","id":"a"}]}`),
			},
			want: []string{"assistant tool_use:a tool_result:a"},
		},
		{
			name: "result before its use in the same entry is unpaired",
			records: []Record{
				selRec("t1", `{"role":"user","parts":[{"kind":"tool_result","id":"z"},{"kind":"tool_use","id":"z"}]}`),
			},
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SelectEntries(tc.records, selRules(true))
			if err != nil {
				t.Fatal(err)
			}
			if ss := renderSel(got); !equalStrings(ss, tc.want) {
				t.Fatalf("entries = %v, want %v", ss, tc.want)
			}
		})
	}
}

func TestSelectEntriesCollapseVariants(t *testing.T) {
	cases := []struct {
		name        string
		records     []Record
		want        []string
		promptEmpty bool // prompt-only entry counts as non-empty
	}{
		{
			name: "consecutive duplicates collapse",
			records: []Record{
				selRec("t1", `{"role":"assistant","parts":[{"kind":"message","text":"same"}]}`),
				selRec("t2", `{"role":"assistant","parts":[{"kind":"message","text":"same"}]}`),
				selRec("t3", `{"role":"assistant","parts":[{"kind":"message","text":"other"}]}`),
			},
			want: []string{"assistant message:", "assistant message:"},
		},
		{
			name: "non-consecutive repeats are kept",
			records: []Record{
				selRec("t1", `{"role":"assistant","parts":[{"kind":"message","text":"a"}]}`),
				selRec("t2", `{"role":"assistant","parts":[{"kind":"message","text":"b"}]}`),
				selRec("t3", `{"role":"assistant","parts":[{"kind":"message","text":"a"}]}`),
			},
			want: []string{"assistant message:", "assistant message:", "assistant message:"},
		},
		{
			name: "part-empty entries drop before the collapse",
			records: []Record{
				selRec("t1", `{"role":"user","parts":[]}`),
				selRec("t2", `{"role":"user","prompt":"hi","parts":[]}`),
				selRec("t3", `{"role":"user","parts":[]}`),
			},
			want:        nil,
			promptEmpty: false,
		},
		{
			name: "prompt-only entry survives under Claude-style emptiness",
			records: []Record{
				selRec("t1", `{"role":"user","prompt":"hi","parts":[]}`),
			},
			want:        []string{"user|hi"},
			promptEmpty: true,
		},
		{
			name: "prompt-only entry drops under Codex-style emptiness",
			records: []Record{
				selRec("t1", `{"role":"user","prompt":"hi","parts":[]}`),
			},
			want:        nil,
			promptEmpty: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SelectEntries(tc.records, selRules(tc.promptEmpty))
			if err != nil {
				t.Fatal(err)
			}
			if ss := renderSel(got); !equalStrings(ss, tc.want) {
				t.Fatalf("entries = %v, want %v", ss, tc.want)
			}
		})
	}
}

func TestSelectEntriesDecodeErrorsAndSelection(t *testing.T) {
	t.Run("decode failure is a ConversionError carrying the seq", func(t *testing.T) {
		bad := selRec("t1", `{not json`)
		bad.Seq = 42
		_, err := SelectEntries([]Record{bad}, selRules(true))
		var convErr *ConversionError
		if !errors.As(err, &convErr) || convErr.Seq != 42 {
			t.Fatalf("error = %v, want ConversionError seq 42", err)
		}
	})
	t.Run("marker decode failure aborts the selection", func(t *testing.T) {
		bad := selMarker("t1")
		bad.Data = json.RawMessage(`{"marker":`)
		bad.Seq = 7
		records := []Record{bad, selRec("t1", `{"role":"user","parts":[{"kind":"message","text":"x"}]}`)}
		_, err := SelectEntries(records, selRules(true))
		var convErr *ConversionError
		if !errors.As(err, &convErr) || convErr.Seq != 7 {
			t.Fatalf("error = %v, want ConversionError seq 7", err)
		}
	})
	t.Run("display-only and non-conversational records are skipped", func(t *testing.T) {
		records := []Record{
			{Kind: KindAssistant, Visibility: VisibilityDisplayOnly, Data: json.RawMessage(`{"role":"assistant","parts":[{"kind":"message","text":"no"}]}`)},
			{Kind: KindPermission, Visibility: VisibilityContent, Data: json.RawMessage(`{}`)},
			{Kind: KindNote, Visibility: VisibilityModelOnly, Data: json.RawMessage(`{"role":"user","parts":[{"kind":"message","text":"yes"}]}`)},
		}
		got, err := SelectEntries(records, selRules(true))
		if err != nil {
			t.Fatal(err)
		}
		if ss := renderSel(got); !equalStrings(ss, []string{"user message:"}) {
			t.Fatalf("entries = %v, want the note only", ss)
		}
	})
}

func TestPairToolPartsAndCollapseRunStandalone(t *testing.T) {
	// The OpenCode budget pass re-runs pairing and collapse on a subset; the
	// exported steps must behave there exactly as inside SelectEntries.
	rules := selRules(false)
	entries := []selEntry{
		{role: "assistant", parts: []selPart{{Kind: selKindUse, ID: "a"}, {Kind: selKindResult, ID: "a"}}},
		{role: "assistant", parts: []selPart{{Kind: selKindUse, ID: "b"}}},
		{role: "assistant", parts: []selPart{{Kind: selKindResult, ID: "a"}}},
	}
	paired := PairToolParts(entries, rules)
	if ss := renderSel(paired); !equalStrings(ss, []string{"assistant tool_use:a tool_result:a", "assistant", "assistant"}) {
		t.Fatalf("paired = %v", ss)
	}
	collapsed := CollapseDuplicateEntries(paired, rules)
	if ss := renderSel(collapsed); !equalStrings(ss, []string{"assistant tool_use:a tool_result:a"}) {
		t.Fatalf("collapsed = %v", ss)
	}
}
