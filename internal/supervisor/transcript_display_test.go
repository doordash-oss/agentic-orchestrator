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
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func legacyCreationRecords(t *testing.T) []Record {
	t.Helper()
	payloads := []ContentData{
		{Content: []llm.ContentBlock{{Type: "tool_use", ID: "write-1", Name: "Write", Input: json.RawMessage(`{"paths":["/work/new.txt"]}`)}}},
		{ObservedFiles: true, FileChanges: []llm.FileChangeEvent{{Path: "/work/new.txt", Operation: "add", Detail: "+complete observed content", HasDiffPatch: true}}},
		{Content: []llm.ContentBlock{{Type: "tool_result", ToolUseID: "write-1", Content: json.RawMessage(`"created"`)}}, FileChanges: []llm.FileChangeEvent{{Path: "/work/new.txt", Operation: "write", Detail: "+bounded native content", HasDiffPatch: true}}},
	}
	out := make([]Record, len(payloads))
	for i, data := range payloads {
		kind, visibility := KindToolResult, VisibilityContent
		if i == 0 {
			kind = KindToolUse
		}
		if data.ObservedFiles {
			visibility = VisibilityDisplayOnly
		}
		encoded, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = Record{Seq: int64(i + 1), Kind: kind, TurnID: "g0.t1", Visibility: visibility, Data: encoded}
	}
	return out
}

func creationData(t *testing.T, rec Record) ContentData {
	t.Helper()
	var data ContentData
	if err := json.Unmarshal(rec.Data, &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func changeCreationData(t *testing.T, rec *Record, edit func(*ContentData)) {
	t.Helper()
	data := creationData(t, *rec)
	edit(&data)
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	rec.Data = encoded
}

func TestCreationDisplayRequiresAdjacentCorrelatedCreation(t *testing.T) {
	cases := []struct {
		name string
		edit func([]Record) []Record
	}{
		{"different tool result", func(r []Record) []Record {
			changeCreationData(t, &r[2], func(d *ContentData) { d.Content[0].ToolUseID = "other" })
			return r
		}},
		{"failed tool result", func(r []Record) []Record {
			changeCreationData(t, &r[2], func(d *ContentData) { d.Content[0].IsError = true })
			return r
		}},
		{"unrelated observed path", func(r []Record) []Record {
			changeCreationData(t, &r[1], func(d *ContentData) { d.FileChanges[0].Path = "/work/elsewhere.txt" })
			return r
		}},
		{"relative Write target", func(r []Record) []Record {
			changeCreationData(t, &r[0], func(d *ContentData) { d.Content[0].Input = json.RawMessage(`{"paths":["new.txt"]}`) })
			return r
		}},
		{"different absolute spelling", func(r []Record) []Record {
			changeCreationData(t, &r[0], func(d *ContentData) { d.Content[0].Input = json.RawMessage(`{"file_path":"/work/./new.txt"}`) })
			return r
		}},
		{"different tool", func(r []Record) []Record {
			changeCreationData(t, &r[0], func(d *ContentData) { d.Content[0].Name = "Bash" })
			return r
		}},
		{"observed update", func(r []Record) []Record {
			changeCreationData(t, &r[1], func(d *ContentData) { d.FileChanges[0].Operation = "update" })
			return r
		}},
		{"native update", func(r []Record) []Record {
			changeCreationData(t, &r[2], func(d *ContentData) { d.FileChanges[0].Operation = "update" })
			return r
		}},
		{"native deletion", func(r []Record) []Record {
			changeCreationData(t, &r[2], func(d *ContentData) { d.FileChanges[0].Operation = "delete" })
			return r
		}},
		{"missing observer provenance", func(r []Record) []Record {
			changeCreationData(t, &r[1], func(d *ContentData) { d.ObservedFiles = false })
			return r
		}},
		{"different turn", func(r []Record) []Record { r[2].TurnID = "g0.t2"; return r }},
		{"different generation", func(r []Record) []Record { r[2].Generation = 1; return r }},
		{"intervening record", func(r []Record) []Record {
			return []Record{r[0], r[1], {Seq: 3, Kind: KindAssistant, TurnID: "g0.t1", Data: json.RawMessage(`{}`)}, r[2]}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			records := tc.edit(legacyCreationRecords(t))
			var index creationDisplayIndex
			for i := range records {
				records[i].Seq = int64(i + 1)
				index.observe(records[i])
			}
			native := records[len(records)-1]
			if got := index.project(native); !bytes.Equal(got.Data, native.Data) {
				t.Fatalf("unrelated record changed: %s", got.Data)
			}
		})
	}
}

func TestCreationDisplayPaginationReplayAndReopenPreserveRawHistory(t *testing.T) {
	for _, input := range []string{`{"paths":["/work/new.txt"]}`, `{"file_path":"/work/new.txt"}`} {
		t.Run(input, func(t *testing.T) {
			dir := t.TempDir()
			store := openTestStore(t, dir)
			records := legacyCreationRecords(t)
			changeCreationData(t, &records[0], func(d *ContentData) { d.Content[0].Input = json.RawMessage(input) })
			// Preserve a real update to the same path and an unrelated creation.
			changeCreationData(t, &records[2], func(d *ContentData) {
				d.FileChanges = append(d.FileChanges,
					llm.FileChangeEvent{Path: "/work/new.txt", Operation: "update", Detail: "-first\n+second"},
					llm.FileChangeEvent{Path: "/work/other.txt", Operation: "write", Detail: "+other"})
			})
			for i, rec := range records {
				got, _, err := store.appendRecord(rec)
				if err != nil {
					t.Fatal(err)
				}
				records[i] = got
			}
			before, err := os.ReadFile(filepath.Join(dir, transcriptFileName))
			if err != nil {
				t.Fatal(err)
			}
			for _, reopen := range []bool{false, true} {
				if reopen {
					_ = store.close()
					store = openTestStore(t, dir)
				}
				for _, query := range []PageQuery{{Limit: 1}, {HasBefore: true, Before: 4, Limit: 1}, {HasAfter: true, After: 2, Limit: 1}} {
					page, err := store.displayPage(query)
					if err != nil {
						t.Fatal(err)
					}
					if page.FirstSeq != 3 || page.LastSeq != 3 || page.HeadSeq != 3 || !page.HasMoreBefore || page.HasMoreAfter || len(page.Items) != 1 {
						t.Fatalf("cursor changed: %+v", page)
					}
					assertProjectedCreation(t, page.Items[0], records[2])
				}
				replay, ok, err := store.replayAfter(2)
				if err != nil || !ok || len(replay) != 1 {
					t.Fatalf("replay: %+v %v %v", replay, ok, err)
				}
				assertProjectedCreation(t, replay[0], records[2])
				assertProjectedCreation(t, store.displayRecord(records[2]), records[2])
				raw, err := store.after(0)
				if err != nil || !reflect.DeepEqual(raw, records) {
					t.Fatalf("raw model replay changed: %+v, %v", raw, err)
				}
				page, err := store.displayPage(PageQuery{HasBefore: true, Before: 3, Limit: 1})
				if err != nil || !bytes.Equal(page.Items[0].Data, records[1].Data) {
					t.Fatal("lost the richer observed creation")
				}
			}
			after, err := os.ReadFile(filepath.Join(dir, transcriptFileName))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("display projection changed durable JSONL")
			}
		})
	}
}

func assertProjectedCreation(t *testing.T, got, raw Record) {
	t.Helper()
	data := creationData(t, got)
	want := creationData(t, raw)
	if !reflect.DeepEqual(data.FileChanges, want.FileChanges[1:]) || !reflect.DeepEqual(data.Content, want.Content) {
		t.Fatalf("projection lost distinct changes or tool result: %+v", data)
	}
	got.Data = raw.Data
	if !reflect.DeepEqual(got, raw) {
		t.Fatalf("record identity changed: %+v", got)
	}
}

func TestCreationDisplayCoordinatorPublishesSameProjectionLiveAndOnResume(t *testing.T) {
	c := newTestCoordinator(t, t.TempDir(), &fakeLauncher{})
	sub, err := c.Subscribe(0, false, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Unsubscribe(sub)
	records := legacyCreationRecords(t)
	func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.turns = []string{"g0.t1"}
		for _, record := range records {
			if err := c.appendProviderLocked(c.conv.Generation, record.Kind, creationData(t, record), "", ""); err != nil {
				t.Fatal(err)
			}
		}
	}()
	var live Record
	for range records {
		select {
		case event := <-sub.Events():
			if event.Kind != EventRecord || event.Record == nil {
				t.Fatalf("unexpected event: %+v", event)
			}
			live = *event.Record
		default:
			t.Fatal("missing committed live record")
		}
	}
	if len(creationData(t, live).FileChanges) != 0 {
		t.Fatalf("live duplicate: %s", live.Data)
	}
	page, err := c.Transcript(PageQuery{Limit: 1})
	if err != nil || len(page.Items) != 1 || !reflect.DeepEqual(page.Items[0], live) {
		t.Fatalf("page disagrees with live record: %+v %v", page, err)
	}
	resumed, err := c.Subscribe(live.Seq-1, true, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Unsubscribe(resumed)
	if len(resumed.Replay) != 1 || !reflect.DeepEqual(resumed.Replay[0], live) {
		t.Fatalf("replay disagrees with live record: %+v", resumed.Replay)
	}
	raw, err := c.store.after(live.Seq - 1)
	if err != nil || len(raw) != 1 || len(creationData(t, raw[0]).FileChanges) != 1 {
		t.Fatalf("model replay lost its native metadata: %+v %v", raw, err)
	}
}
