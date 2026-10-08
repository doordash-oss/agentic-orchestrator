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

package codex

import (
	"encoding/json"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// interactiveToolBlock mirrors root-thread command and file-change items of
// an interactive session as Claude-shaped tool history: an assistant
// tool_use when the item starts and a user tool_result when it completes,
// alongside the progress messages every session gets. Orchestrated sessions
// keep the progress-only stream.
func (p *Protocol) interactiveToolBlock(method string, params json.RawMessage) (llm.SDKMessage, bool) {
	if !p.opts.Interactive || p.opts.NativeToollessReview {
		return llm.SDKMessage{}, false
	}
	if method != "item/started" && method != "item/completed" {
		return llm.SDKMessage{}, false
	}
	var ev ItemStartedParams
	if err := json.Unmarshal(params, &ev); err != nil || ev.Item.ID == "" {
		return llm.SDKMessage{}, false
	}
	if ev.Item.Type != "commandExecution" && ev.Item.Type != codexItemTypeFileChange {
		return llm.SDKMessage{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.isMainThread(ev.ThreadID) {
		return llm.SDKMessage{}, false
	}
	if p.toolPaths == nil {
		p.toolPaths = map[string][]string{}
	}
	if method == "item/started" {
		if _, seen := p.toolPaths[ev.Item.ID]; seen {
			return llm.SDKMessage{}, false
		}
		paths := changedPaths(ev.Item)
		p.toolPaths[ev.Item.ID] = paths
		return toolUseMessage(ev.Item, paths), true
	}
	paths, started := p.toolPaths[ev.Item.ID]
	if !started {
		// A result without its call would be dropped by every consumer.
		return llm.SDKMessage{}, false
	}
	delete(p.toolPaths, ev.Item.ID)
	if more := changedPaths(ev.Item); len(more) > 0 {
		paths = more
	}
	return toolResultMessage(ev.Item, paths), true
}

func changedPaths(item ItemUnion) []string {
	var paths []string
	for _, change := range item.Changes {
		if path := strings.TrimSpace(change.Path); path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

func toolUseMessage(item ItemUnion, paths []string) llm.SDKMessage {
	name := "Bash"
	input := map[string]any{"command": item.Command}
	if item.Type == codexItemTypeFileChange {
		name = codexToolNameWrite
		input = map[string]any{}
		if len(paths) > 0 {
			input["file_path"] = paths[0]
			input["paths"] = paths
		}
	}
	raw, _ := json.Marshal(input)
	return llm.SDKMessage{
		Type: codexRoleAssistant,
		Assistant: &llm.AssistantMessage{
			Type: codexRoleAssistant,
			Message: llm.ConversationMsg{
				Role:    codexRoleAssistant,
				Content: []llm.ContentBlock{{Type: "tool_use", ID: item.ID, Name: name, Input: raw}},
			},
		},
	}
}

func toolResultMessage(item ItemUnion, paths []string) llm.SDKMessage {
	status := strings.ToLower(strings.TrimSpace(item.Status))
	failed := status == "declined" || status == "failed"
	var text string
	if item.Type == codexItemTypeFileChange {
		var lines []string
		for _, change := range item.Changes {
			if path := strings.TrimSpace(change.Path); path != "" {
				lines = append(lines, normalizeFileChangeOperation(change.Kind.Type)+" "+path)
			}
		}
		if len(lines) == 0 {
			lines = paths
		}
		text = strings.Join(lines, "\n")
		if failed {
			text = "File change " + status
		}
	} else {
		text = item.AggregatedOutput
		if item.ExitCode != nil && *item.ExitCode != 0 {
			failed = true
		}
		if text == "" && failed {
			text = "Command " + status
		}
	}
	content, _ := json.Marshal(text)
	var changes []llm.FileChangeEvent
	if item.Type == codexItemTypeFileChange && !failed {
		changes = fileChangeEventsForItem(item)
	}
	return llm.SDKMessage{
		FileChanges: changes,
		Type:        "user",
		User: &llm.UserMessage{
			Type: "user",
			Message: llm.ConversationMsg{
				Role:    "user",
				Content: []llm.ContentBlock{{Type: "tool_result", ToolUseID: item.ID, Content: content, IsError: failed}},
			},
		},
	}
}
