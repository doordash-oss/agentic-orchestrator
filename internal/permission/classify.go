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
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/clirun"
)

// ClassifyFunc is the signature of a function that decides whether a tool
// request may be auto-approved. Returns (true, nil) for allow, (false, nil)
// for defer, and (false, err) when the classification itself failed.
type ClassifyFunc func(toolName, toolInput string) (bool, error)

const (
	classifyModel        = "claude-haiku-4-5-20251001"
	classifyMaxBudgetUSD = "0.05"
	classifyTimeout      = 5 * time.Second
)

const classifySystemPrompt = `You are an automated safety classifier for a CI pipeline.
Your job is to review Bash tool commands and decide whether they should be auto-approved without human intervention.

ALLOW commands that are:
- Safe and reversible
- Local in scope (do not transmit data over the network)
- Read-only or produce only local, recoverable changes
- Common development tasks like testing, linting, building, or listing files

DEFER commands that are:
- Destructive or irreversible (e.g., deletion, force pushes)
- Network-transmitting (e.g., curl with data exfiltration)
- Credential-touching or secret-reading
- System-modifying (e.g., permission changes, installing services)

Reply with exactly the literal token ALLOW or DEFER and nothing else.`

// NewClassify creates a production ClassifyFunc that shells out to the
// claude CLI via the provided CommandRunner. A nil runner produces a no-op
// classify that always returns defer (false, nil).
func NewClassify(runner clirun.CommandRunner) ClassifyFunc {
	if runner == nil {
		return func(_, _ string) (bool, error) { return false, nil }
	}
	return func(toolName, toolInput string) (bool, error) {
		return classify(toolName, toolInput, runner)
	}
}

func classify(toolName, toolInput string, runner clirun.CommandRunner) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), classifyTimeout)
	defer cancel()

	prompt := classifySystemPrompt + "\n\nCommand to classify:\nTool: " + toolName + "\nInput: " + toolInput
	args := []string{
		"--model", classifyModel,
		"--output-format", "stream-json",
		"--tools", "",
		"--no-session-persistence",
		"--max-budget-usd", classifyMaxBudgetUSD,
		"-p", prompt,
	}

	out, err := runner(ctx, "claude", args, nil)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, err
	}

	return parseClassifyOutput(out)
}

func parseClassifyOutput(out []byte) (bool, error) {
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var msg llm.SDKMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if msg.Assistant == nil {
			continue
		}
		for _, block := range msg.Assistant.Message.Content {
			if !block.IsText() {
				continue
			}
			text := strings.TrimSpace(block.Text)
			if text == "ALLOW" {
				return true, nil
			}
			// Anything else — DEFER, empty, extra text — is a defer.
			return false, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("scan classify output: %w", err)
	}
	// No assistant text found — defer safely.
	return false, nil
}
