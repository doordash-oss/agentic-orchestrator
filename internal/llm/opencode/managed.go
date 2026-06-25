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

package opencode

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// Managed OpenCode session configuration (Phase 4).
//
// OpenCode merges config sources (global, project, OPENCODE_CONFIG file, and the
// inline OPENCODE_CONFIG_CONTENT) rather than replacing them, and its ACP surface
// exposes no method to set a system prompt, additional read roots, or agents at
// session time. So Agentico reaches parity by generating a deterministic,
// Agentico-owned config file plus a role-instructions file under the
// provider-managed state directory, pointing the launched process at them, and
// scrubbing inherited compatibility/config sources through environment flags. The
// generated config never writes into the user's global OpenCode directories.

const (
	// openCodeConfigSchema is OpenCode's published config JSON schema URL. It is
	// purely advisory metadata in the generated file.
	openCodeConfigSchema = "https://opencode.ai/config.json"

	// configFileEnvVar points OpenCode at a managed config file. It is an
	// additional config source merged over the user's global config (so backend
	// provider credentials still load) while the inline configContentEnvVar
	// carries the same content at the highest precedence.
	configFileEnvVar = "OPENCODE_CONFIG"

	// managedRootDirName is the Agentico-owned subdirectory of the provider state
	// directory that holds every generated OpenCode artifact. Only files beneath
	// it are written or refreshed; nothing outside it (least of all the user's
	// global OpenCode config) is ever touched.
	managedRootDirName = "opencode"

	// configFileName is the managed OpenCode config file name within a session's
	// fingerprinted managed directory.
	configFileName = "opencode.json"

	// instructionsFileName is the managed role-instructions file (relative to a
	// session's managed directory) referenced by the config "instructions" key.
	instructionsFileName = "agentico-role.md"

	// instructionsDirName groups generated instruction files.
	instructionsDirName = "instructions"

	// managedFilePerm / managedDirPerm are the permissions for generated managed
	// artifacts: owner-writable, group/other readable, matching other Agentico
	// state files.
	managedFilePerm = 0o644
	managedDirPerm  = 0o755
)

// managedConfig is the Agentico-owned OpenCode configuration generated for one
// session. It is the single source for the backend model, permission defaults,
// role instructions, converted agents, and noninteractive runtime settings that
// must not be inherited from user preferences. It is marshaled once and written
// both to the managed config file (referenced by OPENCODE_CONFIG) and inline into
// OPENCODE_CONFIG_CONTENT (highest precedence).
type managedConfig struct {
	Schema       string                  `json:"$schema,omitempty"`
	Model        string                  `json:"model"`
	Instructions []string                `json:"instructions,omitempty"`
	Permission   map[string]any          `json:"permission"`
	Agent        map[string]managedAgent `json:"agent,omitempty"`
	Provider     map[string]any          `json:"provider,omitempty"`
	// Share and Autoupdate pin noninteractive runtime behavior so a managed
	// session never shares transcripts or self-updates regardless of user config.
	Share      string `json:"share,omitempty"`
	Autoupdate *bool  `json:"autoupdate,omitempty"`
}

// managedAgent is an OpenCode agent definition converted from an Agentico
// embedded agent. Only the fields with a safe, documented OpenCode meaning are
// emitted; the model is included only when it is already a valid OpenCode backend
// id so an Agentico-internal model name can never reach OpenCode as a bad value.
type managedAgent struct {
	Description string         `json:"description,omitempty"`
	Mode        string         `json:"mode,omitempty"`
	Prompt      string         `json:"prompt,omitempty"`
	Model       string         `json:"model,omitempty"`
	Permission  map[string]any `json:"permission,omitempty"`
}

// buildManagedSession produces the launch args and environment for a managed
// OpenCode session. It validates the backend model, generates the Agentico-owned
// config + role-instructions artifacts under the provider-managed state directory
// when one is available, points the launched process at them, mirrors the config
// into the highest-precedence inline channel, and appends the inherited-surface
// isolation environment. Any failure aborts before a launchable command exists
// and returns a redacted, actionable error.
func buildManagedSession(opts llm.CommandBuildOpts) (args, env []string, err error) {
	backend := BackendModel(opts.Model)
	if err := validateBackendModel(backend); err != nil {
		return nil, nil, err
	}

	cfg := managedConfig{
		Schema:     openCodeConfigSchema,
		Model:      backend,
		Permission: permissionConfig(opts.DangerouslySkipPerms, opts.WritableRoots, opts.ReadRoots),
		Share:      "disabled",
		Autoupdate: boolPtr(false),
	}

	agents, err := convertAgents(opts.AgentsJSON, opts.DangerouslySkipPerms, opts.WritableRoots)
	if err != nil {
		// The agent JSON is Agentico-internal; name the operation without echoing
		// its contents so a malformed definition cannot leak into diagnostics.
		return nil, nil, fmt.Errorf("converting OpenCode managed agents: %s", sanitizeDiagnostic(err.Error()))
	}
	if len(agents) > 0 {
		cfg.Agent = agents
	}
	applyEffort(&cfg, backend, opts.EffortLevel)

	args = []string{"opencode", "acp"}

	systemPrompt := opts.SystemPrompt
	dir := managedSessionDir(opts.StateDir, sessionFingerprint(opts, backend))

	if dir != "" {
		if strings.TrimSpace(systemPrompt) != "" {
			instrPath := filepath.Join(dir, instructionsDirName, instructionsFileName)
			if werr := writeManagedFileIfChanged(instrPath, []byte(systemPrompt)); werr != nil {
				return nil, nil, fmt.Errorf("writing OpenCode managed role instructions: %s", sanitizeDiagnostic(werr.Error()))
			}
			cfg.Instructions = []string{instrPath}
		}
		content, merr := marshalManagedConfig(cfg)
		if merr != nil {
			return nil, nil, merr
		}
		cfgPath := filepath.Join(dir, configFileName)
		if werr := writeManagedFileIfChanged(cfgPath, content); werr != nil {
			return nil, nil, fmt.Errorf("writing OpenCode managed config: %s", sanitizeDiagnostic(werr.Error()))
		}
		env = append(env, configFileEnvVar+"="+cfgPath)
	} else if strings.TrimSpace(systemPrompt) != "" {
		// Role instructions can only be delivered to OpenCode through a managed
		// instructions file, which requires a provider-managed state directory.
		// Fail closed rather than silently dropping the role prompt.
		return nil, nil, fmt.Errorf("OpenCode managed role instructions require a provider-managed state directory")
	}

	content, merr := marshalManagedConfig(cfg)
	if merr != nil {
		return nil, nil, merr
	}
	env = append(env, configContentEnvVar+"="+string(content))

	isoEnv, _ := buildIsolationEnv(minSupportedVersion())
	env = append(env, isoEnv...)

	return args, env, nil
}

// marshalManagedConfig renders the managed config deterministically. Go marshals
// struct fields in declaration order and map keys sorted, so identical inputs
// always yield identical bytes.
func marshalManagedConfig(cfg managedConfig) ([]byte, error) {
	content, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		// The config can carry instruction paths and converted agent prompts;
		// never surface its contents in the error.
		return nil, fmt.Errorf("marshaling OpenCode managed config: marshal failed")
	}
	return content, nil
}

// managedSessionDir returns the absolute Agentico-owned directory for a session's
// generated artifacts, namespaced by a content fingerprint so concurrent sessions
// with different inputs never collide and identical inputs deterministically
// reuse (and safely overwrite) the same directory. It returns "" when no provider
// state directory is available.
func managedSessionDir(stateDir, fingerprint string) string {
	stateDir = strings.TrimSpace(stateDir)
	if stateDir == "" {
		return ""
	}
	dir := filepath.Join(stateDir, managedRootDirName, fingerprint)
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

// sessionFingerprint is a short, stable hash of the inputs that determine a
// managed session's config and instructions. It excludes derived artifact paths
// so the directory it names does not depend on itself, and it is independent of
// time/randomness so the same inputs always map to the same directory.
func sessionFingerprint(opts llm.CommandBuildOpts, backend string) string {
	roots := append([]string(nil), opts.WritableRoots...)
	sort.Strings(roots)
	readRoots := append([]string(nil), opts.ReadRoots...)
	sort.Strings(readRoots)
	parts := []string{
		backend,
		fmt.Sprintf("dsp=%t", opts.DangerouslySkipPerms),
		"roots=" + strings.Join(roots, "|"),
		"readRoots=" + strings.Join(readRoots, "|"),
		"agents=" + opts.AgentsJSON,
		"effort=" + string(opts.EffortLevel),
		"prompt=" + opts.SystemPrompt,
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])[:16]
}

// writeManagedFileIfChanged writes content to path (creating parent dirs) only
// when the current contents differ, mirroring the deterministic, safe-overwrite
// behavior used for other Agentico-managed provider artifacts. An unchanged file
// is left untouched so repeated session construction is idempotent.
func writeManagedFileIfChanged(path string, content []byte) error {
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, content) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), managedDirPerm); err != nil {
		return fmt.Errorf("creating managed directory: %w", err)
	}
	return os.WriteFile(path, content, managedFilePerm)
}

// boolPtr returns a pointer to b for optional JSON bool fields.
func boolPtr(b bool) *bool { return &b }
