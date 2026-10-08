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

package e2e

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// newSeededSupervisorHarness boots the Claude journey harness over a
// conversation seeded with records records in the durable format.
func newSeededSupervisorHarness(t *testing.T, records int) (*supervisorHarness, string) {
	t.Helper()
	h := newHarnessBase(t, "claude")
	conversation := testutil.SeedSupervisorConversation(t, h.stateDir, records)
	h.script = testutil.WriteFakeClaudeScript(t, testutil.FakeClaudeInteractiveScriptBody())
	h.registry = testutil.NewFakeClaudeRegistry(t, h.script)
	h.init()
	return h, conversation
}

func recordSeqs(events []sseEvent) []int64 {
	var seqs []int64
	for _, ev := range events {
		if ev.kind == string(server.SupervisorEventRecord) {
			seqs = append(seqs, ev.data.Seq)
		}
	}
	return seqs
}

func (h *supervisorHarness) conversationDir(conversation string) string {
	return filepath.Join(h.stateDir, "supervisor", "conversations", conversation)
}

// TestSupervisorStreamReconnectMidTurnDeliversEachRecordOnce drops a stream
// client while the fake holds its reply on a permission, completes the turn
// with the client away, and resumes with the client's cursor.
func TestSupervisorStreamReconnectMidTurnDeliversEachRecordOnce(t *testing.T) {
	h := newSupervisorHarness(t, testutil.FakeClaudeInteractiveScriptBody())
	h.chooseSettings()
	h.send("warm up", "c0")
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	epoch := h.state().StreamEpoch

	stream := h.openStream("?after=2&epoch=" + epoch)
	h.send("run it "+testutil.FakeSupervisorPermBash, "c1")
	seen := recordSeqs(stream.until("permission waiting", isState(server.SupervisorLifecycleWaitingPermission)))
	if len(seen) == 0 {
		t.Fatal("no records before the client dropped")
	}
	cursor := seen[len(seen)-1]
	stream.cancel()

	st := h.state()
	h.do(http.MethodPost, "/api/v1/permissions/answer", map[string]string{
		"request_id": st.PendingRequests[0].RequestID, "session_id": st.SessionID, "decision": "allow_once",
	}, http.StatusOK, nil)
	st = h.waitLifecycle(server.SupervisorLifecycleIdle)

	resumed := h.openStream(fmt.Sprintf("?after=%d&epoch=%s", cursor, epoch))
	events := resumed.until("replayed head", func(ev sseEvent) bool {
		return ev.kind == string(server.SupervisorEventRecord) && ev.data.Seq == st.HeadSeq
	})
	events = append(events, resumed.until("live heartbeat", func(ev sseEvent) bool { return ev.kind == string(server.SupervisorEventHeartbeat) })...)
	for _, ev := range events {
		if ev.kind == string(server.SupervisorEventStreamReset) {
			t.Fatal("a resume within the replay bound reset the stream")
		}
	}
	all := append(append([]int64{}, seen...), recordSeqs(events)...)
	var want []int64
	for seq := int64(3); seq <= st.HeadSeq; seq++ {
		want = append(want, seq)
	}
	if fmt.Sprint(all) != fmt.Sprint(want) {
		t.Fatalf("records across the reconnect = %v, want each of %v exactly once in order", all, want)
	}
	page := h.transcript("?after=2")
	if kinds := recordKinds(page.Items); kinds != "user,permission,permission,assistant" {
		t.Fatalf("turn records = %s", kinds)
	}
}

// TestSupervisorStreamReplayIsBoundedAndPagingServesTheGap seeds a long
// conversation, resumes the stream from cursors at and beyond the replay
// bound, and pages the whole history newest first.
func TestSupervisorStreamReplayIsBoundedAndPagingServesTheGap(t *testing.T) {
	const seeded = 640
	h, _ := newSeededSupervisorHarness(t, seeded)
	st := h.state()
	if st.HeadSeq != seeded || st.Lifecycle != server.SupervisorLifecycleStopped {
		t.Fatalf("seeded boot state = %+v", st)
	}
	epoch := st.StreamEpoch

	within := h.openStream(fmt.Sprintf("?after=%d&epoch=%s", seeded-500, epoch))
	events := within.until("live heartbeat", func(ev sseEvent) bool { return ev.kind == string(server.SupervisorEventHeartbeat) })
	if seqs := recordSeqs(events); len(seqs) != 500 || seqs[0] != seeded-499 || seqs[len(seqs)-1] != seeded {
		t.Fatalf("replay exactly 500 behind = %d records", len(seqs))
	}
	for _, ev := range events {
		if ev.kind == string(server.SupervisorEventStreamReset) {
			t.Fatal("a cursor exactly 500 behind reset the stream")
		}
	}

	gap := seeded - 501
	beyond := h.openStream(fmt.Sprintf("?after=%d&epoch=%s", gap, epoch))
	events = beyond.until("stream.reset", func(ev sseEvent) bool { return ev.kind == string(server.SupervisorEventStreamReset) })
	reset := events[len(events)-1]
	if !reset.data.SnapshotRequired || reset.data.Seq != seeded || len(recordSeqs(events)) != 0 {
		t.Fatalf("reset = %+v after %d records", reset.data, len(recordSeqs(events)))
	}
	beyond.until("heartbeat after reset", func(ev sseEvent) bool { return ev.kind == string(server.SupervisorEventHeartbeat) })
	// The paged endpoint serves the gap the stream refused to replay.
	if page := h.transcript(fmt.Sprintf("?after=%d&limit=500", gap)); page.FirstSeq != int64(gap+1) || len(page.Items) != 500 || !page.HasMoreAfter {
		t.Fatalf("gap page = first %d n %d more-after %v", page.FirstSeq, len(page.Items), page.HasMoreAfter)
	}

	// Newest first through before cursors to seq 1.
	page := h.transcript("")
	if page.FirstSeq != seeded-99 || page.LastSeq != seeded || !page.HasMoreBefore || page.HasMoreAfter {
		t.Fatalf("newest page = first %d last %d", page.FirstSeq, page.LastSeq)
	}
	seen := map[int64]bool{}
	pages := 0
	for {
		pages++
		for i, rec := range page.Items {
			if seen[rec.Seq] || (i > 0 && rec.Seq != page.Items[i-1].Seq+1) {
				t.Fatalf("page %d repeats or reorders seq %d", pages, rec.Seq)
			}
			seen[rec.Seq] = true
		}
		if !page.HasMoreBefore {
			break
		}
		page = h.transcript(fmt.Sprintf("?before=%d", page.FirstSeq))
	}
	if len(seen) != seeded || page.FirstSeq != 1 || pages != 7 {
		t.Fatalf("paged %d records in %d pages ending at %d", len(seen), pages, page.FirstSeq)
	}
	if empty := h.transcript("?before=1"); len(empty.Items) != 0 || empty.HasMoreBefore {
		t.Fatalf("before=1 = %+v", empty)
	}
	if empty := h.transcript(fmt.Sprintf("?after=%d", seeded)); len(empty.Items) != 0 || empty.HasMoreAfter {
		t.Fatalf("after=head = %+v", empty)
	}
	for _, q := range []string{fmt.Sprintf("?after=%d", seeded+1), fmt.Sprintf("?before=%d", seeded+2)} {
		var refused server.ErrorResponse
		h.do(http.MethodGet, "/api/v1/supervisor/transcript"+q, nil, http.StatusConflict, &refused)
		if refused.Error.Code != "cursor_out_of_range" || refused.Error.Diagnostics != "head_seq="+strconv.Itoa(seeded) {
			t.Fatalf("%s = %+v", q, refused.Error)
		}
	}
	h.do(http.MethodGet, "/api/v1/supervisor/transcript?before=0", nil, http.StatusBadRequest, nil)

	// The first turn after seeding launches and resumes with the history.
	h.chooseSettings()
	if sent := h.send("what came first?", "c1"); !sent.Launched || sent.Record.Seq != seeded+1 {
		t.Fatalf("first send after seeding = %+v", sent)
	}
	st = h.waitLifecycle(server.SupervisorLifecycleIdle)
	if h.resumeID() == "" {
		t.Fatalf("the seeded conversation did not resume: argv %v", h.argv())
	}
	reply := h.transcript("?limit=1").Items[0]
	// The fake counts the prior user prompts in the rebuilt session.
	want := fmt.Sprintf("Resumed with %d prior messages: %s", seeded/2, testutil.SeededSupervisorQuestion(1))
	if reply.Kind != server.SupervisorRecordKindAssistant || recordText(reply) != want {
		t.Fatalf("reply after seeding = %q, want %q", recordText(reply), want)
	}
}

// TestSupervisorRestartFencesRetiredGeneration crashes the server while a
// turn holds, then runs the next generation's turn on a fresh stream: no
// event or record the stream carries belongs to the retired generation.
func TestSupervisorRestartFencesRetiredGeneration(t *testing.T) {
	h := newSupervisorHarness(t, testutil.FakeClaudeInteractiveScriptBody())
	h.chooseSettings()
	h.send("start "+testutil.FakeSupervisorPartial, "c1")
	h.waitRecord("partial tool call", func(rec server.SupervisorRecord) bool { return rec.Kind == server.SupervisorRecordKindToolUse })
	retired := h.state().Generation

	h.crash()
	boot := h.state()
	stream := h.openStream("?after=" + strconv.FormatInt(boot.HeadSeq, 10) + "&epoch=" + boot.StreamEpoch)
	h.send("continue", "c2")
	events := stream.until("idle in the new generation", isState(server.SupervisorLifecycleIdle))
	sawRecord := false
	for _, ev := range events {
		switch ev.kind {
		case string(server.SupervisorEventRecord), string(server.SupervisorEventDelta), string(server.SupervisorEventRequest):
			if ev.data.Generation <= retired {
				t.Fatalf("%s event from retired generation %d: %+v", ev.kind, ev.data.Generation, ev.data)
			}
			if ev.kind == string(server.SupervisorEventRecord) {
				sawRecord = true
				if ev.data.Record.Generation <= retired {
					t.Fatalf("record %d committed under retired generation %d", ev.data.Seq, ev.data.Record.Generation)
				}
			}
		}
	}
	if !sawRecord {
		t.Fatal("the new generation committed nothing")
	}
	for _, rec := range h.transcript(fmt.Sprintf("?after=%d", boot.HeadSeq)).Items {
		if rec.Generation <= retired {
			t.Fatalf("record %d after restart carries retired generation %d", rec.Seq, rec.Generation)
		}
	}
}

// TestSupervisorClientMessageResendDeduplicatesAndConflicts resends a
// committed client message id identically and with changed text.
func TestSupervisorClientMessageResendDeduplicatesAndConflicts(t *testing.T) {
	h := newSupervisorHarness(t, testutil.FakeClaudeInteractiveScriptBody())
	h.chooseSettings()
	first := h.send("hello", "c1")
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	if first.Deduplicated {
		t.Fatalf("first send = %+v", first)
	}
	head := h.state().HeadSeq
	again := h.send("hello", "c1")
	if !again.Deduplicated || again.Launched || again.Record.Seq != first.Record.Seq || again.Record.ID != first.Record.ID {
		t.Fatalf("identical resend = %+v", again)
	}
	var refused server.ErrorResponse
	h.do(http.MethodPost, "/api/v1/supervisor/messages", map[string]string{"text": "goodbye", "client_message_id": "c1"}, http.StatusConflict, &refused)
	if refused.Error.Code != "client_message_conflict" || refused.Error.Diagnostics != fmt.Sprintf("committed_seq=%d", first.Record.Seq) {
		t.Fatalf("changed resend = %+v", refused.Error)
	}
	if st := h.state(); st.HeadSeq != head || st.Lifecycle != server.SupervisorLifecycleIdle {
		t.Fatalf("a resend appended or started a turn: %+v", st)
	}
	if inputs := h.userInputs(); fmt.Sprint(inputs) != "[hello]" {
		t.Fatalf("harness inputs = %q, want the first send only", inputs)
	}
}

// TestSupervisorCorruptTranscriptRecoversAcrossRestart damages a complete
// line mid-file while the server is down; the next boot keeps the valid
// prefix, preserves the original and says so in the transcript.
func TestSupervisorCorruptTranscriptRecoversAcrossRestart(t *testing.T) {
	h := newSupervisorHarness(t, testutil.FakeClaudeInteractiveScriptBody())
	h.chooseSettings()
	for i := range 3 {
		h.send(fmt.Sprintf("message %d", i+1), fmt.Sprintf("c%d", i+1))
		h.waitLifecycle(server.SupervisorLifecycleIdle)
	}
	conversation := h.state().ConversationID
	h.stopServer()
	_ = h.coord.Close()

	path := filepath.Join(h.conversationDir(conversation), "transcript.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(data, []byte("\n"))
	lines[2] = []byte("{\"seq\":3,\"kind\":\"user\",\"data\":{\"text\":\n") // seq 3 of 6
	original := bytes.Join(lines, nil)
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	h.start()

	st := h.state()
	if st.Lifecycle != server.SupervisorLifecycleStopped || st.HeadSeq != 3 {
		t.Fatalf("recovered state = %+v, want stopped with the 2-record prefix plus the marker", st)
	}
	page := h.transcript("")
	markers := markerRecords(page, server.SupervisorMarkerTranscriptRecovered)
	if len(markers) != 1 || markers[0].Seq != 3 || markers[0].Visibility != server.SupervisorVisibilityDisplayOnly {
		t.Fatalf("recovered markers = %+v", markers)
	}
	copies, _ := filepath.Glob(path + ".corrupt-*")
	if len(copies) != 1 {
		t.Fatalf("preserved copies = %v", copies)
	}
	if preserved, _ := os.ReadFile(copies[0]); !bytes.Equal(preserved, original) {
		t.Fatal("the preserved copy is not the untouched original")
	}
	text := markers[0].Marker.Text
	if !strings.Contains(text, "4 transcript records after #2") || !strings.Contains(text, filepath.Base(copies[0])) {
		t.Fatalf("marker text = %q", text)
	}
	if sent := h.send("after recovery", "c9"); !sent.Launched || sent.Record.Seq != 4 {
		t.Fatalf("send after recovery = %+v", sent)
	}
	h.waitLifecycle(server.SupervisorLifecycleIdle)
}

// TestSupervisorProviderRecordsCarryDeterministicIDs checks the committed
// assistant ids through the real Codex and OpenCode adapters.
func TestSupervisorProviderRecordsCarryDeterministicIDs(t *testing.T) {
	t.Run("codex item id", func(t *testing.T) {
		h := newCodexSupervisorHarness(t, testutil.FakeCodexScript{})
		h.chooseSettings()
		h.send("hello codex", "c1")
		st := h.waitState("turn answered", func(st server.SupervisorState) bool {
			return st.Lifecycle == server.SupervisorLifecycleIdle && st.HeadSeq == 2
		})
		reply := h.transcript("").Items[1]
		if reply.Kind != server.SupervisorRecordKindAssistant {
			t.Fatalf("reply = %+v", reply)
		}
		matched := ""
		for n := 1; n <= 8; n++ {
			item := fmt.Sprintf("item-1-%d", n)
			if reply.ID == supervisor.ProviderRecordID(st.ConversationID, st.Generation, supervisor.KindAssistant, item) {
				matched = item
			}
		}
		if matched == "" {
			t.Fatalf("assistant id %q is not derived from a fake item id", reply.ID)
		}
	})
	t.Run("opencode turn ordinal", func(t *testing.T) {
		h := newOpenCodeSupervisorHarness(t, testutil.FakeOpenCodeScript{})
		h.chooseSettings()
		h.send("hello opencode", "c1")
		h.waitLifecycle(server.SupervisorLifecycleIdle)
		h.send("again", "c2")
		st := h.waitState("second reply", func(st server.SupervisorState) bool {
			return st.Lifecycle == server.SupervisorLifecycleIdle && st.HeadSeq == 4
		})
		items := h.transcript("").Items
		for _, i := range []int{1, 3} {
			rec := items[i]
			want := supervisor.ProviderRecordID(st.ConversationID, st.Generation, supervisor.KindAssistant, "turn:"+rec.TurnID+"#1")
			if rec.Kind != server.SupervisorRecordKindAssistant || rec.ID != want {
				t.Fatalf("record %d = %s id %q, want %q", rec.Seq, rec.Kind, rec.ID, want)
			}
		}
		if items[1].ID == items[3].ID {
			t.Fatal("assistant ids collided across turns")
		}
	})
}
