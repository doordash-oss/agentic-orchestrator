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

package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/claude"
)

func runTranscriptFixture(t *testing.T, m *Manager, dir, id string, run int, text string) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "session.sh")
	body := fmt.Sprintf(`#!/bin/bash
echo '{"type":"system","subtype":"init","session_id":"p","model":"test"}'
echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"%s"}]}}'
echo '{"type":"result","subtype":"success","session_id":"p","total_cost_usd":0}'
`, text)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("writing script: %v", err)
	}
	sess, err := m.StartSession(id, "feature-1", feature.PhaseImplement, []string{"bash", script}, dir, nil,
		&SessionOpts{PIDDir: dir, RunNumber: run, Protocol: claude.NewProtocol(llm.ProtocolOpts{})})
	if err != nil {
		t.Fatalf("starting session: %v", err)
	}
	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not finish")
	}
}

func segmentTexts(t *testing.T, dir, id string, run int) []string {
	t.Helper()
	paths, err := TranscriptSegments(dir, id, run)
	if err != nil {
		t.Fatalf("listing segments: %v", err)
	}
	texts := make([]string, 0, len(paths))
	for _, path := range paths {
		rows, err := ReadPersistedTranscript(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		log := NewMessageLog()
		for _, row := range rows {
			log.Append(row)
		}
		texts = append(texts, strings.TrimSpace(log.AssistantText()))
	}
	return texts
}

func TestReusedSessionIDKeepsEveryTranscriptSegment(t *testing.T) {
	const id = "feature-1-phase-01-impl-01"
	dir := t.TempDir()
	legacy := LegacyTranscriptPath(dir, id)
	if err := os.WriteFile(legacy, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("writing legacy transcript: %v", err)
	}
	m := NewManager(nil)
	t.Cleanup(m.Shutdown)

	runTranscriptFixture(t, m, dir, id, 1, "before pause")
	runTranscriptFixture(t, m, dir, id, 1, "after resume")
	runTranscriptFixture(t, m, dir, id, 2, "rerun after rewind")

	tests := []struct {
		run  int
		want []string
	}{
		{run: 1, want: []string{"before pause", "after resume"}},
		{run: 2, want: []string{"rerun after rewind"}},
		{run: 3, want: []string{}},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("run %d", tt.run), func(t *testing.T) {
			got := segmentTexts(t, dir, id, tt.run)
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Fatalf("segments = %q, want %q", got, tt.want)
			}
		})
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("legacy transcript removed: %v", err)
	}
}

func TestLocalStatusRowsKeepPersistedTranscriptContiguous(t *testing.T) {
	dir := t.TempDir()
	path, err := createTranscriptSegment(dir, "s1", 1, time.Now())
	if err != nil {
		t.Fatalf("creating segment: %v", err)
	}
	s := NewSession("s1", "feature-1", feature.PhaseImplement)
	s.protocol = restoredProtocol{}
	s.transcriptPath = path
	if err := s.appendLocalStatus("notice"); err != nil {
		t.Fatalf("appending status: %v", err)
	}
	msg := llm.SDKMessage{Type: "assistant", Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{
		Role: "assistant", Content: []llm.ContentBlock{{Type: "text", Text: "reply"}},
	}}}
	if err := s.persistTranscriptRow(s.messageLog.Append(msg), msg); err != nil {
		t.Fatalf("persisting row: %v", err)
	}
	rows, err := ReadPersistedTranscript(path)
	if err != nil {
		t.Fatalf("reading transcript: %v", err)
	}
	if len(rows) != 2 || rows[0].Status == nil || rows[1].Assistant == nil {
		t.Fatalf("persisted rows = %+v, want status then assistant", rows)
	}
}
