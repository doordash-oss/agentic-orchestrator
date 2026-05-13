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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func TestWriteQAFileEmpty(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteQAFile(nil, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "" {
		t.Errorf("expected empty path for nil QALog, got %q", path)
	}
	// File should not exist
	if _, err := os.Stat(filepath.Join(dir, "qa-answers.md")); err == nil {
		t.Error("expected no file to be created for empty QALog")
	}
}

func TestWriteQAFileSinglePair(t *testing.T) {
	dir := t.TempDir()
	qaLog := []ports.QAPair{
		{Question: "What database?", Answer: "PostgreSQL"},
	}
	path, err := WriteQAFile(qaLog, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != filepath.Join(dir, "qa-answers.md") {
		t.Errorf("unexpected path: %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "## Q: What database?") {
		t.Error("expected question heading")
	}
	if !strings.Contains(content, "**A:** PostgreSQL") {
		t.Error("expected answer")
	}
}

func TestWriteQAFileAutoPickedPair(t *testing.T) {
	dir := t.TempDir()
	qaLog := []ports.QAPair{
		{Question: "Which scope?", Answer: "Repository-first (Recommended)", AutoPicked: true, Confidence: 0.81},
	}
	path, err := WriteQAFile(qaLog, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "**A:** Repository-first (Recommended)") {
		t.Error("expected answer")
	}
	if !strings.Contains(content, "_(auto-picked, confidence: 0.81)_") {
		t.Errorf("expected auto-pick annotation, got:\n%s", content)
	}
}

func TestWriteQAFileRendersUserNotes(t *testing.T) {
	dir := t.TempDir()
	qaLog := []ports.QAPair{
		{Question: "Which scope?", Answer: "Repository only", Notes: "Keep it narrow."},
	}
	path, err := WriteQAFile(qaLog, dir)
	if err != nil {
		t.Fatalf("WriteQAFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "**Notes:** Keep it narrow.") {
		t.Errorf("WriteQAFile() content missing notes:\n%s", content)
	}
	if strings.Contains(content, "auto-picked") {
		t.Errorf("WriteQAFile() rendered auto-pick annotation for user answer:\n%s", content)
	}
}

func TestWriteQAFileMultiplePairs(t *testing.T) {
	dir := t.TempDir()
	qaLog := []ports.QAPair{
		{Question: "Auth method?", Answer: "JWT"},
		{Question: "Cache layer?", Answer: "Redis"},
		{Question: "Deployment?", Answer: "Kubernetes"},
	}
	path, err := WriteQAFile(qaLog, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	content := string(data)
	for _, pair := range qaLog {
		if !strings.Contains(content, "## Q: "+pair.Question) {
			t.Errorf("missing question: %q", pair.Question)
		}
		if !strings.Contains(content, "**A:** "+pair.Answer) {
			t.Errorf("missing answer: %q", pair.Answer)
		}
	}
}

func TestWriteQAFileMarkdownFormat(t *testing.T) {
	dir := t.TempDir()
	qaLog := []ports.QAPair{
		{Question: "Which approach?", Answer: "Option B"},
	}
	path, err := WriteQAFile(qaLog, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	content := string(data)
	// Verify exact heading structure
	if !strings.HasPrefix(content, "# User Q&A — Phase Clarifications\n\n") {
		t.Error("expected file to start with top-level heading")
	}
}
