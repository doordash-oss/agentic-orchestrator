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
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func backgroundTool(t *testing.T, sess *fakeSession, id, name, input, result string, failed bool) {
	t.Helper()
	sess.emit(llm.SDKMessage{Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{
		ID: id, Content: []llm.ContentBlock{{Type: "tool_use", ID: id, Name: name, Input: json.RawMessage(input)}},
	}}})
	content, _ := json.Marshal(result)
	sess.emit(llm.SDKMessage{User: &llm.UserMessage{Message: llm.ConversationMsg{
		Content: []llm.ContentBlock{{Type: "tool_result", ToolUseID: id, Content: content, IsError: failed}},
	}}})
}

func TestBackgroundWorkSurvivesTurnsAndReloadButNotProcessLoss(t *testing.T) {
	dir := t.TempDir()
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, dir, launcher)
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "watch", "", "start"); err != nil {
		t.Fatal(err)
	}
	sess := launcher.session(0)
	sub, err := c.Subscribe(0, false, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Unsubscribe(sub)
	backgroundTool(t, sess, "create", "CronCreate", `{"cron":"* * * * *","prompt":"Watch translation questions","recurring":true}`, "Scheduled recurring job b95f1124 (every minute). Session-only (not written to disk, dies when Claude exits). Auto-expires after 3 days. Use CronDelete to cancel sooner.", false)
	sess.emit(llm.SDKMessage{TaskStarted: &llm.TaskStartedMessage{TaskID: "child", Description: "Run tests"}})
	sess.emit(successResult())
	if got := c.State(); got.Lifecycle != LifecycleIdle || len(got.BackgroundTasks) != 2 {
		t.Fatalf("idle background state = %+v", got)
	}
	// The state stream itself announces tasks, so an off-page sidebar updates.
	found := false
	for !found {
		select {
		case ev := <-sub.Events():
			found = ev.State != nil && len(ev.State.BackgroundTasks) == 2
		case <-time.After(time.Second):
			t.Fatal("background work was not published")
		}
	}
	if _, err := c.Send(context.Background(), "another turn", "", "next"); err != nil {
		t.Fatal(err)
	}
	sess.emit(llm.SDKMessage{TaskProgress: &llm.TaskProgressMessage{TaskID: "child", LastToolName: "Bash"}})
	sess.emit(successResult())
	got := c.State().BackgroundTasks
	for _, task := range got {
		if !backgroundActive(task.State) {
			t.Fatalf("turn completion retired task: %+v", task)
		}
	}
	// Paging away from the create record cannot remove the task from state.
	for i := 0; i < 8; i++ {
		sess.emit(assistantText(fmt.Sprint("extra-", i), "Other activity"))
	}
	page, err := c.Transcript(PageQuery{Limit: 1})
	if err != nil || len(page.Items) != 1 || len(c.State().BackgroundTasks) != 2 {
		t.Fatalf("paging: %+v %v", page, err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	restored := newTestCoordinator(t, dir, &fakeLauncher{})
	tasks := restored.State().BackgroundTasks
	if len(tasks) != 2 {
		t.Fatalf("reload lost tasks: %+v", tasks)
	}
	for _, task := range tasks {
		if task.State != "interrupted" {
			t.Fatalf("dead process advertised live task: %+v", task)
		}
	}
}

func TestBackgroundWorkRequiresSuccessfulCorrelatedResultsAndConfirmedStops(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "watch", "", "start"); err != nil {
		t.Fatal(err)
	}
	sess := launcher.session(0)
	input := `{"cron":"* * * * *","prompt":"Watch tests"}`
	result := "Scheduled recurring job job1 (every minute). Session-only."
	backgroundTool(t, sess, "fail", "CronCreate", input, result, true)
	backgroundTool(t, sess, "unknown", "CronCreate", input, "Tool completed", false)
	backgroundTool(t, sess, "unrelated", "Bash", input, result, false)
	sess.emit(assistantText("promise", "I have armed a monitor job1"))
	if tasks := c.State().BackgroundTasks; len(tasks) != 0 {
		t.Fatalf("unconfirmed tasks: %+v", tasks)
	}
	backgroundTool(t, sess, "ok", "CronCreate", input, result, false)
	backgroundTool(t, sess, "ok", "CronCreate", input, result, false) // duplicate provider records
	if tasks := c.State().BackgroundTasks; len(tasks) != 1 {
		t.Fatalf("duplicate tasks: %+v", tasks)
	}
	backgroundTool(t, sess, "deny-delete", "CronDelete", `{"id":"job1"}`, "Cancelled job job1.", true)
	if got := c.State().BackgroundTasks[0].State; got != "watching" {
		t.Fatalf("failed stop changed state to %s", got)
	}
	backgroundTool(t, sess, "delete", "CronDelete", `{"id":"job1"}`, "Cancelled job job1.", false)
	if got := c.State().BackgroundTasks[0].State; got != "stopped" {
		t.Fatalf("stop = %s", got)
	}
	backgroundTool(t, sess, "monitor", "Monitor", `{"description":"Watch build events"}`, "Monitor started (task mon1, timeout 60000ms). You will be notified on each event.", false)
	sess.emit(successResult())
	sess.emit(llm.SDKMessage{TaskNotification: &llm.TaskNotificationMessage{TaskID: "mon1", Status: "completed"}})
	sess.emit(llm.SDKMessage{TaskProgress: &llm.TaskProgressMessage{TaskID: "mon1", LastToolName: "Read"}})
	tasks := c.State().BackgroundTasks
	for _, task := range tasks {
		if task.ProviderID == "mon1" && (task.Kind != "monitor" || task.State != "completed") {
			t.Fatalf("monitor completion = %+v", task)
		}
	}
}

func TestBackgroundSchedulesReconcileAfterRestartAndExpireHonestly(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher, func(o *Options) { o.Now = func() time.Time { return now } })
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "watch", "", "start"); err != nil {
		t.Fatal(err)
	}
	sess := launcher.session(0)
	backgroundTool(t, sess, "create", "CronCreate", `{"prompt":"Watch tests"}`, "Scheduled recurring job job1 (every minute). Auto-expires after 3 days.", false)
	// Project with a future clock without changing the live coordinator clock.
	future := c.store.backgroundTasks(c.State().Generation, true, now.Add(4*24*time.Hour))
	if future[0].State != "interrupted" {
		t.Fatalf("expired schedule = %+v", future)
	}
	sess.emit(successResult())
	c.End()
	if _, err := c.Send(context.Background(), "verify schedules", "", "restart"); err != nil {
		t.Fatal(err)
	}
	sess = launcher.session(1)
	backgroundTool(t, sess, "list", "CronList", `{}`, "job1 — every minute (recurring): Watch tests", false)
	tasks := c.State().BackgroundTasks
	if len(tasks) != 2 || tasks[0].State != "watching" || tasks[0].Generation != c.State().Generation || tasks[1].State != "completed" {
		t.Fatalf("reconciled schedules = %+v", tasks)
	}
	backgroundTool(t, sess, "bad-list", "CronList", `{}`, "Unrecognized output", false)
	if c.State().BackgroundTasks[0].State != "watching" {
		t.Fatal("unknown list removed schedule")
	}
	backgroundTool(t, sess, "empty-list", "CronList", `{}`, "No scheduled jobs.", false)
	for _, task := range c.State().BackgroundTasks {
		if backgroundActive(task.State) {
			t.Fatalf("missing schedule still active: %+v", task)
		}
	}
}

func TestBackgroundActivityIsBoundedAndSnapshotsAreIndependent(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "work", "", "start"); err != nil {
		t.Fatal(err)
	}
	sess := launcher.session(0)
	sess.emit(llm.SDKMessage{TaskStarted: &llm.TaskStartedMessage{TaskID: "child", Description: "Run tests"}})
	before := c.State().BackgroundTasks[0]
	for i := 0; i < 12; i++ {
		sess.emit(llm.SDKMessage{TaskProgress: &llm.TaskProgressMessage{TaskID: "child", LastToolName: fmt.Sprint("tool-", i)}})
	}
	task := c.State().BackgroundTasks[0]
	if len(task.Activity) != 8 || len(before.Activity) != 1 || before.Activity[0].Detail != "Started" {
		t.Fatalf("activity bounds: before=%+v after=%+v", before.Activity, task.Activity)
	}
	sess.emit(llm.SDKMessage{TaskProgress: &llm.TaskProgressMessage{TaskID: "child", LastToolName: "tool-11"}})
	if len(c.State().BackgroundTasks[0].Activity) != 8 {
		t.Fatal("unchanged progress duplicated activity")
	}
	task.Activity[0].Detail = "mutated snapshot"
	if c.State().BackgroundTasks[0].Activity[0].Detail == "mutated snapshot" {
		t.Fatal("state exposed mutable registry")
	}
}
