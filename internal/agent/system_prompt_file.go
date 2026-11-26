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
	"fmt"
	"os"
	"path/filepath"
)

const (
	systemPromptsDirName   = "system-prompts"
	systemPromptFileName   = "system-prompt.md"
	systemPromptDirPerm    = 0o700
	systemPromptFilePerm   = 0o600
	systemPromptDirPattern = "agentico-system-prompt-*"
)

// writeSystemPromptFile writes prompt to a private file in a fresh directory
// under stateDir (the OS temp dir when empty). cleanup removes that directory.
func writeSystemPromptFile(stateDir, prompt string) (path string, cleanup func(), err error) {
	base := os.TempDir()
	if stateDir != "" {
		base = filepath.Join(stateDir, systemPromptsDirName)
		if err := os.MkdirAll(base, systemPromptDirPerm); err != nil {
			return "", nil, fmt.Errorf("creating system prompt directory: %w", err)
		}
		if err := os.Chmod(base, systemPromptDirPerm); err != nil {
			return "", nil, fmt.Errorf("restricting system prompt directory: %w", err)
		}
	}
	dir, err := os.MkdirTemp(base, systemPromptDirPattern)
	if err != nil {
		return "", nil, fmt.Errorf("creating system prompt directory: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	path = filepath.Join(dir, systemPromptFileName)
	if err := os.WriteFile(path, []byte(prompt), systemPromptFilePerm); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("writing system prompt file: %w", err)
	}
	return path, cleanup, nil
}
