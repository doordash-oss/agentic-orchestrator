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

package permission

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

)

func makeStreamJSONLine(msg string) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`+"\n", msg)
}

func makeFakeRunner(output string, err error) func(context.Context, string, []string, []string) ([]byte, error) {
	return func(_ context.Context, _ string, _ []string, _ []string) ([]byte, error) {
		return []byte(output), err
	}
}

func TestNewClassify_NilRunner(t *testing.T) {
	cf := NewClassify(nil)
	allow, err := cf("Bash", "go test ./...")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allow {
		t.Error("nil runner should always defer")
	}
}

func TestClassify_ALLOW(t *testing.T) {
	out := makeStreamJSONLine("ALLOW")
	cf := NewClassify(makeFakeRunner(out, nil))
	allow, err := cf("Bash", "go test ./...")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allow {
		t.Error("expected allow for ALLOW output")
	}
}

func TestClassify_DEFER(t *testing.T) {
	out := makeStreamJSONLine("DEFER")
	cf := NewClassify(makeFakeRunner(out, nil))
	allow, err := cf("Bash", "rm -rf /")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allow {
		t.Error("expected defer for DEFER output")
	}
}

func TestClassify_EmptyOutput(t *testing.T) {
	cf := NewClassify(makeFakeRunner("", nil))
	allow, err := cf("Bash", "go test ./...")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allow {
		t.Error("expected defer for empty output")
	}
}

func TestClassify_MalformedOutput(t *testing.T) {
	cf := NewClassify(makeFakeRunner("not valid json\n", nil))
	allow, err := cf("Bash", "go test ./...")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allow {
		t.Error("expected defer for malformed output")
	}
}

func TestClassify_ErrorFromRunner(t *testing.T) {
	boom := errors.New("subprocess error")
	cf := NewClassify(makeFakeRunner("", boom))
	allow, err := cf("Bash", "go test ./...")
	if err == nil {
		t.Fatal("expected error from runner")
	}
	if allow {
		t.Error("expected defer on runner error")
	}
}

func TestClassify_ArgsContainExpectedFlags(t *testing.T) {
	var capturedArgs []string
	cf := NewClassify(func(_ context.Context, _ string, args []string, _ []string) ([]byte, error) {
		capturedArgs = args
		return []byte(makeStreamJSONLine("ALLOW")), nil
	})
	_, _ = cf("Bash", "go test ./...")

	required := []string{
		"--model", classifyModel,
		"--output-format", "stream-json",
		"--tools", "",
		"--no-session-persistence",
		"--max-budget-usd", classifyMaxBudgetUSD,
		"-p",
	}
	for i, want := range required {
		if i >= len(capturedArgs) {
			t.Fatalf("expected arg %d %q, got no more args", i, want)
		}
		if capturedArgs[i] != want {
			t.Errorf("arg[%d] = %q, want %q", i, capturedArgs[i], want)
		}
	}
}

func TestClassify_UserMessageOnlyContainsToolNameAndInput(t *testing.T) {
	var capturedPrompt string
	cf := NewClassify(func(_ context.Context, _ string, args []string, _ []string) ([]byte, error) {
		for i, a := range args {
			if a == "-p" && i+1 < len(args) {
				capturedPrompt = args[i+1]
				break
			}
		}
		return []byte(makeStreamJSONLine("ALLOW")), nil
	})
	_, _ = cf("Bash", "go test ./...")

	if capturedPrompt == "" {
		t.Fatal("did not capture prompt")
	}
	if !strings.Contains(capturedPrompt, "Tool: Bash") {
		t.Errorf("prompt missing Tool: Bash")
	}
	if !strings.Contains(capturedPrompt, "Input: go test ./...") {
		t.Errorf("prompt missing Input: go test ./...")
	}
}

func TestClassify_ParseIgnoresNonAssistantMessages(t *testing.T) {
	out := `{"type":"system","subtype":"init","session_id":"s1","model":"claude-haiku-4-5-20251001"}
` + makeStreamJSONLine("ALLOW")
	cf := NewClassify(makeFakeRunner(out, nil))
	allow, err := cf("Bash", "go test ./...")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allow {
		t.Error("expected allow after ignoring init message")
	}
}

func TestClassify_ParseIgnoresThinkingBlocks(t *testing.T) {
	// Assistant message with a thinking block followed by a text block.
	out := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"let me think"},{"type":"text","text":"ALLOW"}]}}` + "\n"
	cf := NewClassify(makeFakeRunner(out, nil))
	allow, err := cf("Bash", "go test ./...")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allow {
		t.Error("expected allow when text block follows thinking block")
	}
}

func TestClassify_ParseMultipleAssistantLines(t *testing.T) {
	out := makeStreamJSONLine("DEFER") + makeStreamJSONLine("ALLOW")
	cf := NewClassify(makeFakeRunner(out, nil))
	allow, err := cf("Bash", "go test ./...")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allow {
		t.Error("expected defer from first assistant line")
	}
}

func TestClassify_ParseUsesSDKMessageUnmarshal(t *testing.T) {
	// Build the raw JSON that llm.SDKMessage.UnmarshalJSON expects.
	out := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ALLOW"}]}}` + "\n"
	cf := NewClassify(makeFakeRunner(out, nil))
	allow, err := cf("Bash", "go test ./...")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allow {
		t.Error("expected allow for real SDKMessage shape")
	}
}
