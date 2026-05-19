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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const ReadOnlyApplyPatchHookFlag = "--codex-readonly-apply-patch-hook"

const readOnlyApplyPatchRootsEnv = "AGENTIC_CODEX_WRITABLE_ROOTS_JSON"

var applyPatchPathPrefixes = []string{
	"*** Add File: ",
	"*** Update File: ",
	"*** Delete File: ",
	"*** Move to: ",
}

type readOnlyHookPayload struct {
	HookEventName string          `json:"hook_event_name"`
	ToolName      string          `json:"tool_name"`
	CWD           string          `json:"cwd"`
	ToolInput     json.RawMessage `json:"tool_input"`
}

// RunReadOnlyApplyPatchHook is the Codex hook entry point used by read-only
// planning roles. It exits successfully even when denying a tool call because
// Codex reads the decision from stdout.
func RunReadOnlyApplyPatchHook(stdin io.Reader, stdout io.Writer) int {
	var payload readOnlyHookPayload
	if err := json.NewDecoder(stdin).Decode(&payload); err != nil {
		return 0
	}
	if payload.ToolName != "apply_patch" {
		return 0
	}

	command := readOnlyHookCommand(payload.ToolInput)
	paths := readOnlyHookPatchPaths(command)
	if len(paths) == 0 {
		return 0
	}

	roots, err := readOnlyHookWritableRoots(os.Getenv(readOnlyApplyPatchRootsEnv))
	if err != nil {
		writeReadOnlyHookDeny(stdout, payload.HookEventName, err.Error())
		return 0
	}

	cwd := payload.CWD
	if cwd == "" {
		if wd, err := os.Getwd(); err == nil {
			cwd = wd
		}
	}

	for _, path := range paths {
		resolved := resolveReadOnlyHookPath(path, cwd)
		if !readOnlyHookInsideAnyRoot(resolved, roots) {
			writeReadOnlyHookDeny(stdout, payload.HookEventName, "apply_patch attempted to edit outside Agentic output roots: "+resolved)
			return 0
		}
	}
	return 0
}

func readOnlyHookCommand(raw json.RawMessage) string {
	var input struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(raw, &input); err == nil && input.Command != "" {
		return input.Command
	}
	var command string
	if err := json.Unmarshal(raw, &command); err == nil {
		return command
	}
	return ""
}

func readOnlyHookPatchPaths(command string) []string {
	var paths []string
	for _, rawLine := range strings.Split(command, "\n") {
		line := strings.TrimSpace(rawLine)
		for _, prefix := range applyPatchPathPrefixes {
			if strings.HasPrefix(line, prefix) {
				path := strings.TrimSpace(strings.TrimPrefix(line, prefix))
				if path != "" {
					paths = append(paths, path)
				}
				break
			}
		}
	}
	return paths
}

func readOnlyHookWritableRoots(raw string) ([]string, error) {
	var roots []string
	if err := json.Unmarshal([]byte(raw), &roots); err != nil {
		return nil, fmt.Errorf("Agentic read-only apply_patch hook could not parse writable roots")
	}
	var normalized []string
	for _, root := range roots {
		if root == "" {
			continue
		}
		normalized = append(normalized, resolveReadOnlyHookPath(root, ""))
	}
	if len(normalized) == 0 {
		return nil, fmt.Errorf("Agentic read-only apply_patch hook has no writable roots")
	}
	return normalized, nil
}

func resolveReadOnlyHookPath(path, cwd string) string {
	if !filepath.IsAbs(path) && cwd != "" {
		path = filepath.Join(cwd, path)
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return resolveReadOnlyHookExistingPrefix(filepath.Clean(path))
}

func resolveReadOnlyHookExistingPrefix(path string) string {
	current := path
	var missing []string
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return path
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func readOnlyHookInsideAnyRoot(path string, roots []string) bool {
	for _, root := range roots {
		if readOnlyHookPathInside(path, root) {
			return true
		}
	}
	return false
}

func readOnlyHookPathInside(path, root string) bool {
	if root == string(filepath.Separator) {
		return true
	}
	root = strings.TrimRight(root, string(filepath.Separator))
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

func writeReadOnlyHookDeny(w io.Writer, event, reason string) {
	var output map[string]any
	if readOnlyHookEventIsPermissionRequest(event) {
		output = map[string]any{
			"hookSpecificOutput": map[string]any{
				"hookEventName": "PermissionRequest",
				"decision": map[string]any{
					"behavior": "deny",
					"message":  reason,
				},
			},
		}
	} else {
		output = map[string]any{
			"hookSpecificOutput": map[string]any{
				"hookEventName":            "PreToolUse",
				"permissionDecision":       "deny",
				"permissionDecisionReason": reason,
			},
		}
	}
	_ = json.NewEncoder(w).Encode(output)
}

func readOnlyHookEventIsPermissionRequest(event string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(event, "_", ""))
	return normalized == "permissionrequest"
}
