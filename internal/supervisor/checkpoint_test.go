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
	"testing"
)

func TestSelectCheckpointUsesLatestRepresentable(t *testing.T) {
	records := []Record{
		{Seq: 1, Kind: KindUser},
		{Seq: 2, Kind: KindCheckpoint, Data: json.RawMessage(`{"covers_through_seq":1,"summary":"readable","native_baseline":{"harness":"claude","payload":{}},"reason":"native_auto"}`)},
		{Seq: 3, Kind: KindUser},
		{Seq: 4, Kind: KindCheckpoint, Data: json.RawMessage(`{"covers_through_seq":3,"native_baseline":{"harness":"codex","payload":{}},"reason":"native_auto"}`)},
		{Seq: 5, Kind: KindUser},
	}
	claude, err := SelectCheckpoint(records, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if claude.Checkpoint == nil || claude.Checkpoint.Seq != 2 || len(claude.Records) != 2 || claude.Records[0].Seq != 3 {
		t.Fatalf("Claude selection: %#v", claude)
	}
	codex, err := SelectCheckpoint(records, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if codex.Checkpoint == nil || codex.Checkpoint.Seq != 4 || len(codex.Records) != 1 || codex.Records[0].Seq != 5 {
		t.Fatalf("Codex selection: %#v", codex)
	}
	opencode, err := SelectCheckpoint(records, "opencode")
	if err != nil {
		t.Fatal(err)
	}
	if opencode.Checkpoint == nil || opencode.Checkpoint.Seq != 2 {
		t.Fatalf("OpenCode selection: %#v", opencode)
	}
}
