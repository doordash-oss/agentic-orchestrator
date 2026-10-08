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

package testutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

// SeededSupervisorQuestion and SeededSupervisorAnswer are the texts of the
// n-th seeded user and assistant records.
func SeededSupervisorQuestion(n int) string { return fmt.Sprintf("Seeded question %d", n) }
func SeededSupervisorAnswer(n int) string   { return fmt.Sprintf("Seeded answer %d", n) }

// SeedSupervisorConversation writes a generation-0 supervisor conversation
// with records alternating user and assistant, records long, directly in
// the durable format under stateDir, as a long-lived server would leave
// it. No sidecar index is written; the store rebuilds it on open. It
// returns the conversation id.
func SeedSupervisorConversation(t *testing.T, stateDir string, records int) string {
	t.Helper()
	conversationID := uuid.NewString()
	dir := filepath.Join(stateDir, "supervisor")
	convDir := filepath.Join(dir, "conversations", conversationID)
	if err := os.MkdirAll(convDir, 0o755); err != nil {
		t.Fatal(err)
	}
	conv, _ := json.Marshal(map[string]any{
		"format":          1,
		"conversation_id": conversationID,
		"generation":      0,
		"stream_epoch":    "seeded-epoch",
	})
	if err := os.WriteFile(filepath.Join(dir, "conversation.json"), conv, 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for seq := 1; seq <= records; seq++ {
		turn := (seq + 1) / 2
		rec := map[string]any{
			"seq":             seq,
			"id":              fmt.Sprintf("seed-%d", seq),
			"conversation_id": conversationID,
			"generation":      0,
			"turn_id":         fmt.Sprintf("g0.t%d", turn),
			"visibility":      "content",
			"created_at":      created.Add(time.Duration(seq) * time.Second),
		}
		if seq%2 == 1 {
			rec["kind"] = "user"
			rec["client_message_id"] = fmt.Sprintf("seed-cm-%d", turn)
			rec["data"] = map[string]any{"text": SeededSupervisorQuestion(turn)}
		} else {
			rec["kind"] = "assistant"
			rec["data"] = map[string]any{"content": []map[string]any{{"type": "text", "text": SeededSupervisorAnswer(turn)}}}
		}
		line, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(convDir, "transcript.jsonl"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return conversationID
}
