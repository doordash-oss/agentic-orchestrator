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
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

func TestReadSessionOutput(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name        string
		fileContent string
		assistText  string
		resultMsg   string
		want        string
	}{
		{"file wins", "from file", "from assistant", "from result", "from file"},
		{"assistant fallback", "", "from assistant", "from result", "from assistant\n"},
		{"result fallback", "", "", "from result", "from result"},
		{"all empty", "", "", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			responsePath := filepath.Join(tmpDir, tt.name+".txt")
			if tt.fileContent != "" {
				if err := os.WriteFile(responsePath, []byte(tt.fileContent), 0o644); err != nil {
					t.Fatal(err)
				}
			} else {
				_ = os.Remove(responsePath)
			}

			sess := session.NewSession("test", "test-feat", feature.PhaseResearch)
			if tt.assistText != "" {
				sess.MessageLog().Append(llm.SDKMessage{
					Type: "assistant",
					Assistant: &llm.AssistantMessage{
						Message: llm.ConversationMsg{
							Role:    "assistant",
							Content: []llm.ContentBlock{{Type: "text", Text: tt.assistText}},
						},
					},
				})
			}
			if tt.resultMsg != "" {
				sess.MessageLog().Append(llm.SDKMessage{
					Type:   testResultMessageType,
					Result: &llm.ResultMessage{Subtype: testResultSuccessValue, Result: tt.resultMsg},
				})
			}

			got := readSessionOutput(responsePath, sess)
			if got != tt.want {
				t.Errorf("readSessionOutput() = %q, want %q", got, tt.want)
			}
		})
	}
}
