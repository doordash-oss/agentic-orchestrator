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
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// phaseExtraInstructions holds operator-provided per-phase instruction text,
// keyed by feature.Phase.InstructionKey(). It is process-global, immutable
// startup configuration: set once via SetPhaseExtraInstructions before any
// session launches and only read thereafter (during system-prompt
// composition in BuildRoleSystemPrompt). A nil map means no phase has extra
// instructions.
var phaseExtraInstructions map[string]string

// SetPhaseExtraInstructions installs the process-wide per-phase extra
// instruction map. Call once at startup with the result of
// LoadPhaseExtraInstructions. Passing nil clears it.
func SetPhaseExtraInstructions(m map[string]string) {
	phaseExtraInstructions = m
}

// PhaseExtraInstruction returns the operator instruction text configured for
// the given phase, or "" when none is set. Safe to call when no instructions
// were configured.
func PhaseExtraInstruction(p feature.Phase) string {
	if phaseExtraInstructions == nil {
		return ""
	}
	return phaseExtraInstructions[p.InstructionKey()]
}

// LoadPhaseExtraInstructions reads the operator-provided per-phase extra
// instruction files named in config.Defaults.PhaseExtraInstructions and
// returns a map keyed by feature.Phase.InstructionKey() -> file contents.
//
// configDir is the directory of the config file; relative paths are resolved
// against it. Paths may also be absolute or "~/"-prefixed.
//
// This is intentionally non-fatal at bootstrap: an unknown phase key, an
// unreadable/missing file, or an empty file produces a warning (returned in
// the second result) and is skipped so startup continues. Returns a nil map
// when nothing valid was loaded.
func LoadPhaseExtraInstructions(configured map[string]string, configDir string) (map[string]string, []string) {
	if len(configured) == 0 {
		return nil, nil
	}

	var warnings []string
	result := make(map[string]string, len(configured))

	for key, rawPath := range configured {
		phase, ok := feature.PhaseFromInstructionKey(key)
		if !ok {
			warnings = append(warnings, fmt.Sprintf(
				"Warning: phase_extra_instructions: unknown phase key %q; skipping", key))
			continue
		}

		path := strings.TrimSpace(rawPath)
		if path == "" {
			warnings = append(warnings, fmt.Sprintf(
				"Warning: phase_extra_instructions[%s]: empty path; skipping", key))
			continue
		}
		path = config.ExpandHome(path)
		if !filepath.IsAbs(path) && configDir != "" {
			path = filepath.Join(configDir, path)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf(
				"Warning: phase_extra_instructions[%s]: could not read %q: %v; skipping", key, path, err))
			continue
		}

		if looksBinary(data) {
			warnings = append(warnings, fmt.Sprintf(
				"Warning: phase_extra_instructions[%s]: file %q is not a UTF-8 text file (binary content); skipping", key, path))
			continue
		}

		content := strings.TrimSpace(string(data))
		if content == "" {
			warnings = append(warnings, fmt.Sprintf(
				"Warning: phase_extra_instructions[%s]: file %q is empty; skipping", key, path))
			continue
		}

		result[phase.InstructionKey()] = content
	}

	if len(result) == 0 {
		return nil, warnings
	}
	return result, warnings
}

// looksBinary reports whether data is unsuitable for injection into a text
// prompt. It flags any NUL byte (the classic binary marker, matching git's
// heuristic) and any content that is not valid UTF-8. Empty input is treated
// as text so the caller's dedicated empty-file check handles it.
func looksBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return true
	}
	return !utf8.Valid(data)
}
