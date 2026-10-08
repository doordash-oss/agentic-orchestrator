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
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// BackgroundActivity is one meaningful provider-confirmed task update.
type BackgroundActivity struct {
	At     string
	Detail string
}

// BackgroundTask is confirmed provider work whose lifetime is independent of
// a conversation turn. The transcript is its durable source of truth.
type BackgroundTask struct {
	updatedSeq int64
	Activity   []BackgroundActivity
	ID         string
	ProviderID string
	Generation int64
	Kind       string
	Title      string
	State      string
	Schedule   string
	Detail     string
	StartedAt  string
	UpdatedAt  string
	ExpiresAt  string
}

func backgroundActive(state string) bool { return state == "running" || state == "watching" }

// backgroundIndex folds at append/startup, never by reading transcript pages.
// Provider IDs are scoped to a process generation. Nothing in assistant prose
// can arm a task, finish it, or restore it after a process restart.
type backgroundIndex struct {
	tasks      map[string]BackgroundTask
	pending    map[string]llm.ContentBlock
	generation int64
	revision   uint64
}

var scheduledJob = regexp.MustCompile(`^Scheduled (?:recurring job|one-shot task) ([A-Za-z0-9_-]+) \(([^\n]+?)\)\.`)
var monitorTask = regexp.MustCompile(`^Monitor started \(task ([A-Za-z0-9_-]+)[,)]`)
var cronExpiry = regexp.MustCompile(`Auto-expires after ([0-9]+) days\.`)
var listedJob = regexp.MustCompile(`^([A-Za-z0-9_-]+) — (.+?) \((recurring|one-shot)\)(?: \[session-only\])?: (.*)$`)

func backgroundKey(gen int64, kind, id string) string {
	namespace := "task"
	if kind == "scheduled" {
		namespace = "cron"
	}
	return fmt.Sprintf("%d:%s:%s", gen, namespace, id)
}

func (d *backgroundIndex) put(rec Record, kind, id, title, state, detail string) BackgroundTask {
	key := backgroundKey(rec.Generation, kind, id)
	task, exists := d.tasks[key]
	// Late progress or a delayed start acknowledgement must not resurrect a
	// provider task after its terminal report. A fresh CronList may confirm a
	// schedule again, so schedules deliberately use their own reconciliation.
	if exists && kind != "scheduled" && !backgroundActive(task.State) && backgroundActive(state) {
		return task
	}
	if !exists {
		task = BackgroundTask{ID: key, ProviderID: id, Generation: rec.Generation, Kind: kind, StartedAt: rec.CreatedAt.Format(time.RFC3339Nano)}
	}
	if title != "" {
		task.Title = backgroundText(title, 160)
	}
	if task.Title == "" {
		task.Title = "Background task"
	}
	if state != "" {
		task.State = state
	}
	if detail != "" {
		task.Detail = backgroundText(detail, 400)
	}
	task.UpdatedAt = rec.CreatedAt.Format(time.RFC3339Nano)
	task.updatedSeq = rec.Seq
	if task.Detail != "" && (len(task.Activity) == 0 || task.Activity[len(task.Activity)-1].Detail != task.Detail) {
		task.Activity = append(append([]BackgroundActivity(nil), task.Activity...), BackgroundActivity{At: task.UpdatedAt, Detail: task.Detail})
		if len(task.Activity) > 8 {
			task.Activity = task.Activity[len(task.Activity)-8:]
		}
	}
	if d.tasks == nil {
		d.tasks = make(map[string]BackgroundTask)
	}
	d.tasks[key] = task
	d.revision++
	return task
}

func backgroundText(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > limit {
		return string(r[:limit-1]) + "…"
	}
	return s
}

func backgroundResult(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []llm.ContentBlock
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.IsText() {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func (d *backgroundIndex) observe(rec Record) {
	if rec.Generation != d.generation {
		d.generation = rec.Generation
		d.pending = nil
	}
	if rec.Kind != KindToolUse && rec.Kind != KindToolResult {
		return
	}
	var data ContentData
	if json.Unmarshal(rec.Data, &data) != nil {
		return
	}
	if t := data.TaskStarted; t != nil && t.TaskID != "" {
		kind, state := "task", "running"
		if strings.Contains(t.TaskType, "monitor") {
			kind, state = "monitor", "watching"
		}
		d.put(rec, kind, t.TaskID, t.Description, state, "Started")
	}
	if t := data.TaskProgress; t != nil && t.TaskID != "" {
		detail := "Progress reported"
		if t.LastToolName != "" {
			detail = "Using " + t.LastToolName
		}
		// A monitor remains watching between its events.
		state := "running"
		if d.tasks[backgroundKey(rec.Generation, "task", t.TaskID)].Kind == "monitor" {
			state = "watching"
		}
		d.put(rec, "task", t.TaskID, t.Description, state, detail)
	}
	if t := data.TaskNotification; t != nil && t.TaskID != "" {
		state := "interrupted"
		switch t.Status {
		case "completed":
			state = "completed"
		case "failed", "error":
			state = "failed"
		case "stopped", "cancelled", "canceled":
			state = "stopped"
		}
		d.put(rec, "task", t.TaskID, "", state, "Provider reported "+t.Status)
	}
	for _, b := range data.Content {
		if b.IsToolUse() {
			switch b.Name {
			case "CronCreate", "CronDelete", "CronList", "Monitor", "TaskStop":
				if d.pending == nil {
					d.pending = make(map[string]llm.ContentBlock)
				}
				d.pending[b.ID] = b
			}
		}
		if !b.IsToolResult() {
			continue
		}
		use, ok := d.pending[b.ToolUseID]
		delete(d.pending, b.ToolUseID)
		if !ok || b.IsError {
			continue
		}
		var input struct {
			ID          string `json:"id"`
			TaskID      string `json:"task_id"`
			Cron        string `json:"cron"`
			Prompt      string `json:"prompt"`
			Description string `json:"description"`
		}
		if json.Unmarshal(use.Input, &input) != nil {
			continue
		}
		result := backgroundResult(b.Content)
		switch use.Name {
		case "CronCreate":
			match := scheduledJob.FindStringSubmatch(result)
			if match == nil {
				continue
			}
			task := d.put(rec, "scheduled", match[1], input.Prompt, "watching", "Schedule armed; individual checks are not reported")
			task.Schedule = backgroundText(match[2], 120)
			if task.Schedule == "" {
				task.Schedule = backgroundText(input.Cron, 120)
			}
			if expiry := cronExpiry.FindStringSubmatch(result); expiry != nil {
				days, _ := strconv.Atoi(expiry[1])
				if started, err := time.Parse(time.RFC3339Nano, rec.CreatedAt.Format(time.RFC3339Nano)); err == nil && days > 0 && days <= 365 {
					task.ExpiresAt = started.Add(time.Duration(days) * 24 * time.Hour).Format(time.RFC3339Nano)
				}
			}
			d.tasks[task.ID] = task
		case "Monitor":
			match := monitorTask.FindStringSubmatch(result)
			if match == nil {
				continue
			}
			title := input.Description
			if title == "" {
				title = "Watching for events"
			}
			task := d.put(rec, "monitor", match[1], title, "watching", "Monitor armed; waiting for events")
			task.Kind = "monitor"
			d.tasks[task.ID] = task
		case "CronDelete":
			if result == "Cancelled job "+input.ID+"." {
				d.finish(rec, "scheduled", input.ID, "stopped", "Schedule cancelled")
			}
		case "TaskStop":
			// A successful TaskStop result is the provider acknowledgement, not the
			// user's request to stop. Failed tool results were rejected above.
			d.finish(rec, "task", input.TaskID, "stopped", "Stop confirmed by provider")
		case "CronList":
			d.reconcileSchedules(rec, result)
		}
	}
}

func (d *backgroundIndex) finish(rec Record, kind, id, state, detail string) {
	if id == "" {
		return
	}
	if _, ok := d.tasks[backgroundKey(rec.Generation, kind, id)]; ok {
		d.put(rec, kind, id, "", state, detail)
	}
}

func (d *backgroundIndex) reconcileSchedules(rec Record, result string) {
	// Only a completely recognized list is authoritative; unknown provider
	// formats never falsely complete the tasks we already know about.
	rows := strings.Split(strings.TrimSpace(result), "\n")
	matches := [][]string{}
	if strings.TrimSpace(result) != "No scheduled jobs." {
		for _, row := range rows {
			match := listedJob.FindStringSubmatch(row)
			if match == nil {
				return
			}
			matches = append(matches, match)
		}
	}
	seen := map[string]bool{}
	for _, match := range matches {
		task := d.put(rec, "scheduled", match[1], match[4], "watching", "Schedule confirmed by provider")
		task.Schedule = backgroundText(match[2], 120)
		task.ExpiresAt = "" // A fresh list confirms existence, but supplies no expiry.
		d.tasks[task.ID] = task
		seen[task.ProviderID] = true
	}
	for _, task := range d.tasks {
		if task.Kind != "scheduled" || !backgroundActive(task.State) {
			continue
		}
		if task.Generation == rec.Generation && !seen[task.ProviderID] {
			d.finish(rec, "scheduled", task.ProviderID, "completed", "No longer scheduled")
		}
		// A provider list in a new process supersedes old, unverified schedules.
		if task.Generation != rec.Generation {
			task.State = "completed"
			task.Detail = "Previous schedule reconciled in the current session"
			d.tasks[task.ID] = task
			d.revision++
		}
	}
}

func (s *transcriptStore) backgroundRevision() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.background.revision
}

func (s *transcriptStore) backgroundTasks(generation int64, live bool, now time.Time) []BackgroundTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks := make([]BackgroundTask, 0, len(s.background.tasks))
	for _, task := range s.background.tasks {
		if backgroundActive(task.State) && (task.Generation != generation || !live) {
			task.State = "interrupted"
			task.Detail = "Session ended; execution is no longer confirmed"
		}
		if expiry, err := time.Parse(time.RFC3339Nano, task.ExpiresAt); err == nil && !now.Before(expiry) && backgroundActive(task.State) {
			task.State = "interrupted"
			task.Detail = "Schedule reached its expiry; needs confirmation"
		}
		task.Activity = append([]BackgroundActivity(nil), task.Activity...)
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool {
		a, b := backgroundActive(tasks[i].State), backgroundActive(tasks[j].State)
		if a != b {
			return a
		}
		if tasks[i].updatedSeq == tasks[j].updatedSeq {
			return tasks[i].ID < tasks[j].ID
		}
		return tasks[i].updatedSeq > tasks[j].updatedSeq
	})
	// Keep all active work, plus a bounded recent history in every state push.
	active := 0
	for active < len(tasks) && backgroundActive(tasks[active].State) {
		active++
	}
	if len(tasks) > active+20 {
		tasks = tasks[:active+20]
	}
	return tasks
}
