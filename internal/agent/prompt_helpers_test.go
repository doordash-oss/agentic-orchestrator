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

package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
)

// fakeSubagentSession implements just enough of the session interface to test
// SubagentProgressTracker.Install without pulling in a real PTY session.
type fakeSubagentSession struct {
	cb func(llm.SDKMessage)
}

func (f *fakeSubagentSession) SetOnSubagentEvent(fn func(llm.SDKMessage)) { f.cb = fn }

type fakeContextReadSession struct {
	toolCB func(string, json.RawMessage)
	fileCB func(llm.FileReadEvent)
}

func (f *fakeContextReadSession) SetOnToolAllowed(fn func(string, json.RawMessage)) {
	f.toolCB = fn
}

func (f *fakeContextReadSession) SetOnFileRead(fn func(llm.FileReadEvent)) {
	f.fileCB = fn
}

func TestBuildPreflightInput(t *testing.T) {
	// skillsDir="" suppresses the Additional Skills subsection regardless
	// of phase, which lets these unit tests exercise the KB / Guidelines
	// branches in isolation without depending on the embedded skill catalog.
	// The full skill-rendering path is covered by system prompt goldens.
	//
	// guidelinesDir uses a dummy path; resolveGuidelineViews inspects only
	// the embedded guideline definitions, so the directory does not have
	// to exist on disk for these tests.
	const guidelinesDir = "/state/guidelines"

	t.Run("no context", func(t *testing.T) {
		got := buildPreflightInput(feature.PhaseImplement, "", nil, "")
		if got.HasKB || got.HasGuidelines || got.HasSkills {
			t.Errorf("buildPreflightInput() = %+v, want no enabled sections", got)
		}
	})

	t.Run("kb paths included", func(t *testing.T) {
		got := buildPreflightInput(feature.PhaseImplement, "", []KBInfo{
			{Name: "repo-a", IndexPath: "/state/repo-a/kb/index.md", RootDir: "/state/repo-a/kb"},
		}, "")
		if !got.HasKB || len(got.KBInfos) != 1 {
			t.Fatalf("buildPreflightInput().KBInfos = %+v, want one enabled KB", got.KBInfos)
		}
		kb := got.KBInfos[0]
		if kb.Name != "repo-a" || kb.IndexPath != "/state/repo-a/kb/index.md" || kb.RootDir != "/state/repo-a/kb" {
			t.Errorf("buildPreflightInput().KBInfos[0] = %+v", kb)
		}
	})

	t.Run("multiple kb entries", func(t *testing.T) {
		got := buildPreflightInput(feature.PhaseImplement, "", []KBInfo{
			{Name: "repo-a", IndexPath: "/a/index.md", RootDir: "/a"},
			{Name: "repo-b", IndexPath: "/b/index.md", RootDir: "/b"},
		}, "")
		if len(got.KBInfos) != 2 {
			t.Fatalf("buildPreflightInput().KBInfos length = %d, want 2", len(got.KBInfos))
		}
		if got.KBInfos[0].Name != "repo-a" || got.KBInfos[1].Name != "repo-b" {
			t.Errorf("buildPreflightInput().KBInfos = %+v, want stable input order", got.KBInfos)
		}
	})

	t.Run("kb and guidelines", func(t *testing.T) {
		got := buildPreflightInput(feature.PhaseImplement, "", []KBInfo{
			{Name: "repo-a", IndexPath: "/idx", RootDir: "/root"},
		}, guidelinesDir)
		if !got.HasKB {
			t.Error("HasKB = false, want true")
		}
		if !got.HasGuidelines {
			t.Error("HasGuidelines = false, want true")
		}
		if len(got.Guidelines) == 0 {
			t.Fatal("Guidelines length = 0, want embedded language rows")
		}
		for _, guideline := range got.Guidelines {
			if !strings.HasPrefix(guideline.IndexPath, guidelinesDir+"/") {
				t.Errorf("Guideline path %q does not live under %q", guideline.IndexPath, guidelinesDir)
			}
		}
	})

	t.Run("guidelines only", func(t *testing.T) {
		got := buildPreflightInput(feature.PhaseImplement, "", nil, guidelinesDir)
		if got.HasKB {
			t.Error("HasKB = true, want false")
		}
		if !got.HasGuidelines {
			t.Error("HasGuidelines = false, want true")
		}
		for _, guideline := range got.Guidelines {
			if !strings.HasSuffix(guideline.IndexPath, "/index.md") {
				t.Errorf("Guideline path %q does not end in /index.md", guideline.IndexPath)
			}
		}
	})
}

func TestContextReadTracker_EmitsProviderFileRead(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "contextread_feat"
	if err := os.MkdirAll(filepath.Join(stateDir, featureID), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	guidelinesDir := filepath.Join(stateDir, "guidelines")
	obs := observe.New(true, stateDir, false, "", false, "agentic")
	sc := observe.SpanContextForFeature(featureID, "", "", "").Child()
	tracker := &ContextReadTracker{
		KBBaseDir:     filepath.Join(stateDir, "knowledge-base"),
		SkillsDir:     filepath.Join(stateDir, "skills"),
		GuidelinesDir: guidelinesDir,
		Observer:      obs,
	}
	fake := &fakeContextReadSession{}
	tracker.Install(fake, sc, "implement", "sess-codex")
	if fake.fileCB == nil {
		t.Fatal("tracker did not register provider file-read callback")
	}

	exitCode := 0
	fake.fileCB(llm.FileReadEvent{
		FilePath:       filepath.Join(guidelinesDir, "go", "index.md"),
		Source:         "codex.command_action",
		ProviderItemID: "call_read",
		ExitCode:       &exitCode,
	})

	data, err := os.ReadFile(filepath.Join(stateDir, featureID, "events.jsonl"))
	if err != nil {
		t.Fatalf("reading events.jsonl: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		`"event_type":"context.file_read"`,
		`"category":"guideline"`,
		`"source":"codex.command_action"`,
		`"provider_item_id":"call_read"`,
		`"exit_code":0`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("events.jsonl missing %s in %s", want, got)
		}
	}
}

func TestSubagentProgressTracker_ForwardsProgress(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "subtrack_feat"
	if err := os.MkdirAll(filepath.Join(stateDir, featureID), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	obs := observe.New(true, stateDir, false, "", false, "agentic")
	sc := observe.SpanContextForFeature(featureID, "", "", "").Child()

	tracker := &SubagentProgressTracker{
		Observer: obs, SC: sc, Phase: "Knowledge Base", SessionID: "sess-kb",
	}
	fake := &fakeSubagentSession{}
	tracker.Install(fake)
	if fake.cb == nil {
		t.Fatal("tracker did not register a callback")
	}

	// Lifecycle start
	fake.cb(llm.SDKMessage{
		Type: "system", Subtype: "task_started",
		TaskStarted: &llm.TaskStartedMessage{
			TaskID: "t1", ToolUseID: "tu1", Description: "feature state",
			TaskType: "local_agent", Prompt: "long prompt body",
		},
	})
	// Progress: full payload
	fake.cb(llm.SDKMessage{
		Type: "system", Subtype: "task_progress",
		TaskProgress: &llm.TaskProgressMessage{
			TaskID: "t1", ToolUseID: "tu1", Description: "reading", LastToolName: "Read",
			Usage: &llm.TaskUsage{TotalTokens: 100, ToolUses: 3, DurationMs: 500},
		},
	})
	// Progress with nil Usage should not panic
	fake.cb(llm.SDKMessage{
		Type: "system", Subtype: "task_progress",
		TaskProgress: &llm.TaskProgressMessage{TaskID: "t2"},
	})
	// Notification terminal event
	fake.cb(llm.SDKMessage{
		Type: "system", Subtype: "task_notification",
		TaskNotification: &llm.TaskNotificationMessage{
			TaskID: "t1", Status: "completed", Summary: "done",
			Usage: &llm.TaskUsage{TotalTokens: 200, ToolUses: 5, DurationMs: 1500},
		},
	})

	// Read back events.jsonl and assert the three expected events were written.
	path := filepath.Join(stateDir, featureID, "events.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading events.jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 events, got %d: %s", len(lines), string(data))
	}
	if !strings.Contains(lines[0], `"event_type":"agent.task_started"`) ||
		!strings.Contains(lines[0], `"task_id":"t1"`) ||
		!strings.Contains(lines[0], `"task_type":"local_agent"`) {
		t.Errorf("first event unexpected: %s", lines[0])
	}
	// The task prompt is intentionally large and MUST NOT be persisted.
	if strings.Contains(lines[0], "long prompt body") {
		t.Errorf("task_started event leaked prompt body to events.jsonl: %s", lines[0])
	}
	if !strings.Contains(lines[1], `"event_type":"agent.task_progress"`) ||
		!strings.Contains(lines[1], `"task_id":"t1"`) ||
		!strings.Contains(lines[1], `"duration_ms":500`) {
		t.Errorf("second event unexpected: %s", lines[1])
	}
	if !strings.Contains(lines[2], `"event_type":"agent.task_progress"`) ||
		!strings.Contains(lines[2], `"task_id":"t2"`) {
		t.Errorf("third event unexpected: %s", lines[2])
	}
	if !strings.Contains(lines[3], `"event_type":"agent.task_ended"`) ||
		!strings.Contains(lines[3], `"status":"completed"`) ||
		!strings.Contains(lines[3], `"duration_ms":1500`) {
		t.Errorf("fourth event unexpected: %s", lines[3])
	}
}

func TestSubagentProgressTracker_NilObserverNoOp(t *testing.T) {
	// Must not panic and must not install a callback when Observer is nil.
	tracker := &SubagentProgressTracker{Observer: nil}
	fake := &fakeSubagentSession{}
	tracker.Install(fake)
	if fake.cb != nil {
		t.Error("expected no callback when Observer is nil")
	}
}
