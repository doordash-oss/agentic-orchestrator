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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func transcriptKinds(t *testing.T, c *Coordinator) []RecordKind {
	t.Helper()
	page, err := c.Transcript(PageQuery{Limit: maxPageLimit})
	if err != nil {
		t.Fatal(err)
	}
	kinds := make([]RecordKind, 0, len(page.Items))
	for _, rec := range page.Items {
		kinds = append(kinds, rec.Kind)
	}
	return kinds
}

func TestCoordinator_RepeatedProviderItemCommitsOnceUnderItsDeterministicID(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	res, err := c.Send(context.Background(), "hello", "", "cm-1")
	if err != nil {
		t.Fatal(err)
	}
	sess := launcher.session(0)
	sess.emit(assistantText("msg_1", "Hello!"))
	sess.emit(assistantText("msg_1", "Hello!"))
	tools := llm.SDKMessage{Type: "assistant", Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{
		ID: "msg_2", Role: "assistant", Content: []llm.ContentBlock{{Type: "tool_use", ID: "item-1-2", Name: "Bash"}},
	}}}
	results := llm.SDKMessage{Type: "user", User: &llm.UserMessage{Message: llm.ConversationMsg{
		Role: "user", Content: []llm.ContentBlock{{Type: "tool_result", ToolUseID: "item-1-2"}},
	}}}
	results.FileChanges = []llm.FileChangeEvent{{Path: "src/a.go", Detail: "-old\n+new", HasDiffPatch: true}}
	sess.emit(tools)
	sess.emit(results)
	sess.emit(tools)
	sess.emit(results)
	sess.emit(successResult())
	waitLifecycle(t, c, LifecycleIdle)

	if got := fmt.Sprint(transcriptKinds(t, c)); got != "[user assistant tool_use tool_result]" {
		t.Fatalf("kinds = %s", got)
	}
	page, _ := c.Transcript(PageQuery{})
	var saved ContentData
	if err := json.Unmarshal(page.Items[3].Data, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.FileChanges) != 1 || saved.FileChanges[0].Detail != "-old\n+new" {
		t.Fatalf("lost durable diff: %+v", saved)
	}
	st := c.State()
	want := map[RecordKind]string{
		KindAssistant:  ProviderRecordID(st.ConversationID, st.Generation, KindAssistant, "msg_1"),
		KindToolUse:    ProviderRecordID(st.ConversationID, st.Generation, KindToolUse, "item-1-2"),
		KindToolResult: ProviderRecordID(st.ConversationID, st.Generation, KindToolResult, "item-1-2"),
	}
	for _, rec := range page.Items[1:] {
		if rec.ID != want[rec.Kind] {
			t.Fatalf("%s id = %q, want %q", rec.Kind, rec.ID, want[rec.Kind])
		}
	}
	if page.Items[2].ID == page.Items[3].ID {
		t.Fatal("tool_use and tool_result sharing an item id collided")
	}
	if page.Items[0].ID == "" || page.Items[0].TurnID != res.Record.TurnID {
		t.Fatalf("user record = %+v", page.Items[0])
	}
}

func TestCoordinator_IDLessProviderOutputUsesTurnOrdinal(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	res, err := c.Send(context.Background(), "hello", "", "cm-1")
	if err != nil {
		t.Fatal(err)
	}
	sess := launcher.session(0)
	sess.emit(assistantText("", "first"))
	sess.emit(assistantText("", "second"))
	sess.emit(successResult())
	waitLifecycle(t, c, LifecycleIdle)
	page, _ := c.Transcript(PageQuery{})
	if len(page.Items) != 3 {
		t.Fatalf("records = %d, want user + two assistants", len(page.Items))
	}
	st := c.State()
	for i, rec := range page.Items[1:] {
		want := ProviderRecordID(st.ConversationID, st.Generation, KindAssistant, fmt.Sprintf("turn:%s#%d", res.Record.TurnID, i+1))
		if rec.ID != want {
			t.Fatalf("assistant %d id = %q, want %q", i+1, rec.ID, want)
		}
	}
}

func TestCoordinator_RequestAndVerdictForOneRequestCommitTwoRecords(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "hello", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	sess := launcher.session(0)
	req := llm.SDKMessage{Type: "control_request", ControlRequest: &llm.ControlRequestMessage{
		RequestID: "req-1", Request: llm.ControlRequest{ToolName: "Bash", Input: json.RawMessage(`{}`)},
	}}
	sess.emit(req)
	sess.emit(req)
	waitLifecycle(t, c, LifecycleWaitingPermission)
	sess.answer(ports.ControlAnswer{RequestID: "req-1", ToolName: "Bash", Allowed: true})
	sess.emit(successResult())
	waitLifecycle(t, c, LifecycleIdle)
	if got := fmt.Sprint(transcriptKinds(t, c)); got != "[user permission permission]" {
		t.Fatalf("kinds = %s, want the request and its verdict once each", got)
	}
}

func TestCoordinator_ClientMessageResendIsDeduplicatedAndConflictingReuseRefused(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	first, err := c.Send(context.Background(), "hello", "", "cm-1")
	if err != nil || first.Deduplicated {
		t.Fatalf("first send = %+v, %v", first, err)
	}
	again, err := c.Send(context.Background(), "hello", "", "cm-1")
	if err != nil || !again.Deduplicated || again.Record.Seq != first.Record.Seq || again.Record.ID != first.Record.ID {
		t.Fatalf("identical resend = %+v, %v", again, err)
	}
	if sent := launcher.session(0).Sent(); len(sent) != 1 {
		t.Fatalf("identical resend reached the harness: %v", sent)
	}
	for name, send := range map[string]func() error{
		"different text": func() error {
			_, err := c.Send(context.Background(), "goodbye", "", "cm-1")
			return err
		},
		"added hidden context": func() error {
			_, err := c.Send(context.Background(), "hello", "BUNDLE", "cm-1")
			return err
		},
	} {
		var conflict *ClientMessageConflictError
		if err := send(); !errors.As(err, &conflict) || conflict.CommittedSeq != first.Record.Seq {
			t.Fatalf("%s: err = %v, want conflict naming seq %d", name, err, first.Record.Seq)
		}
	}
	if head := c.State().HeadSeq; head != 1 {
		t.Fatalf("conflict appended: head %d", head)
	}

	// The committed record survives a relaunch: a reuse from the retired
	// generation is still told apart.
	launcher.session(0).emit(successResult())
	waitLifecycle(t, c, LifecycleIdle)
	c.End()
	if _, err := c.Send(context.Background(), "next", "", "cm-2"); err != nil {
		t.Fatal(err)
	}
	if c.State().Generation != 2 {
		t.Fatalf("generation = %d, want 2", c.State().Generation)
	}
	var conflict *ClientMessageConflictError
	if _, err := c.Send(context.Background(), "changed", "", "cm-1"); !errors.As(err, &conflict) || conflict.CommittedSeq != 1 {
		t.Fatalf("retired-generation reuse err = %v, want conflict", err)
	}
	if res, err := c.Send(context.Background(), "hello", "", "cm-1"); err != nil || !res.Deduplicated {
		t.Fatalf("retired-generation resend = %+v, %v", res, err)
	}
}

func TestCoordinator_CorruptTranscriptBootsWithRecoveredMarkerAndAcceptsSends(t *testing.T) {
	stateDir := t.TempDir()
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, stateDir, launcher)
	chooseSettings(t, c)
	for i := range 3 {
		if _, err := c.Send(context.Background(), fmt.Sprintf("m%d", i+1), "", fmt.Sprintf("cm-%d", i+1)); err != nil {
			t.Fatal(err)
		}
		launcher.session(0).emit(assistantText(fmt.Sprintf("msg_%d", i+1), "reply"))
		launcher.session(0).emit(successResult())
		waitLifecycle(t, c, LifecycleIdle)
	}
	st := c.State()
	_ = c.Close()

	dir := filepath.Join(stateDir, supervisorDirName, conversationsDir, st.ConversationID)
	path := filepath.Join(dir, transcriptFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(data, []byte("\n"))
	lines[3] = []byte("{not json\n") // seq 4 of 6
	original := bytes.Join(lines, nil)
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}

	reopened := newTestCoordinator(t, stateDir, &fakeLauncher{})
	got := reopened.State()
	if got.Lifecycle != LifecycleStopped {
		t.Fatalf("lifecycle after recovery = %s, want stopped", got.Lifecycle)
	}
	if got.HeadSeq != 4 {
		t.Fatalf("head = %d, want the 3-record prefix plus the marker", got.HeadSeq)
	}
	page, _ := reopened.Transcript(PageQuery{})
	marker := page.Items[3]
	var md MarkerData
	_ = json.Unmarshal(marker.Data, &md)
	if marker.Kind != KindMarker || marker.Visibility != VisibilityDisplayOnly || md.Marker != MarkerTranscriptRecovered {
		t.Fatalf("recovered record = %+v %+v", marker, md)
	}
	copies, _ := filepath.Glob(filepath.Join(dir, transcriptFileName+".corrupt-*"))
	if len(copies) != 1 {
		t.Fatalf("preserved copies = %v", copies)
	}
	if !strings.Contains(md.Text, "3 transcript records after #3") || !strings.Contains(md.Text, copies[0]) {
		t.Fatalf("marker text = %q", md.Text)
	}
	if preserved, _ := os.ReadFile(copies[0]); !bytes.Equal(preserved, original) {
		t.Fatal("preserved copy is not byte-identical to the original")
	}
	chooseSettings(t, reopened)
	if _, err := reopened.Send(context.Background(), "after recovery", "", "cm-9"); err != nil {
		t.Fatalf("send after recovery: %v", err)
	}
}

func TestCoordinator_SubscribeReplayIsBoundedAndWiderGapsReset(t *testing.T) {
	c := newTestCoordinator(t, t.TempDir(), &fakeLauncher{})
	for i := range maxReplay + 20 {
		data, _ := json.Marshal(UserData{Text: fmt.Sprintf("m%d", i+1)})
		if _, _, err := c.store.appendRecord(Record{Kind: KindNote, Visibility: VisibilityModelOnly, Data: data}); err != nil {
			t.Fatal(err)
		}
	}
	head := c.State().HeadSeq
	epoch := c.State().StreamEpoch
	sub, err := c.Subscribe(head-maxReplay, true, epoch)
	if err != nil || sub.Reset || len(sub.Replay) != maxReplay || sub.Replay[0].Seq != head-maxReplay+1 {
		t.Fatalf("cursor %d behind: reset=%v replay=%d err=%v", maxReplay, sub.Reset, len(sub.Replay), err)
	}
	c.Unsubscribe(sub)
	sub, err = c.Subscribe(head-maxReplay-1, true, epoch)
	if err != nil || !sub.Reset || len(sub.Replay) != 0 {
		t.Fatalf("cursor %d behind: reset=%v replay=%d err=%v", maxReplay+1, sub.Reset, len(sub.Replay), err)
	}
	sub, err = c.Subscribe(head, true, epoch)
	if err != nil || sub.Reset || len(sub.Replay) != 0 {
		t.Fatalf("cursor at head: reset=%v replay=%d err=%v", sub.Reset, len(sub.Replay), err)
	}
	c.Unsubscribe(sub)
}

func TestCoordinator_TranscriptRefusesOutOfRangeCursor(t *testing.T) {
	c := newTestCoordinator(t, t.TempDir(), &fakeLauncher{})
	var oor *CursorOutOfRangeError
	if _, err := c.Transcript(PageQuery{After: 1, HasAfter: true}); !errors.As(err, &oor) || oor.HeadSeq != 0 {
		t.Fatalf("after past empty head err = %v", err)
	}
	if page, err := c.Transcript(PageQuery{Before: 1, HasBefore: true}); err != nil || len(page.Items) != 0 {
		t.Fatalf("before=1 = %+v, %v", page, err)
	}
}

func TestCoordinatorPersistsTaskActivityWithoutPrompt(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "review", "", "cm-tasks"); err != nil {
		t.Fatal(err)
	}
	sess := launcher.session(0)
	sess.emit(llm.SDKMessage{Origin: childOrigin, TaskStarted: &llm.TaskStartedMessage{TaskID: "task-a", Description: "Review tests", Prompt: "private delegated instructions"}})
	sess.emit(llm.SDKMessage{Origin: childOrigin, TaskProgress: &llm.TaskProgressMessage{TaskID: "task-a", LastToolName: "Read"}})
	sess.emit(llm.SDKMessage{Origin: childOrigin, TaskNotification: &llm.TaskNotificationMessage{TaskID: "task-a", Status: "completed", Summary: "Tests reviewed"}})
	sess.emit(successResult())
	waitLifecycle(t, c, LifecycleIdle)
	page, err := c.Transcript(PageQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 4 {
		t.Fatalf("records = %d", len(page.Items))
	}
	for _, rec := range page.Items[1:] {
		if rec.Visibility != VisibilityDisplayOnly {
			t.Fatalf("task event entered model history: %+v", rec)
		}
		if bytes.Contains(rec.Data, []byte("private delegated instructions")) {
			t.Fatal("task prompt persisted")
		}
	}
}
