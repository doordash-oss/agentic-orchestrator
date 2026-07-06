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
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

func TestCodexBuildSessionEnvContract(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("CODEX_HOME", "~/resolved-codex-home")

	eventCh := make(chan interface{}, 8)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)
	pr.Registry = newRegistryWithProviders()

	cmd, env, sessOpts, err := pr.BuildSession(BuildSessionOpts{
		Model:        "gpt-5.4",
		Prompt:       "test prompt",
		SystemPrompt: "system prompt",
		AgentNames:   []string{"codebase-locator", "web-search-researcher"},
		PIDDir:       filepath.Join(dir, "pid"),
		PermHandler:  permHandlerFor(false, nil, ""),
		WorkDir:      dir,
	})
	if err != nil {
		t.Fatalf("BuildSession() error: %v", err)
	}
	requireOnlyAgenticoBinEnv(t, env)
	wantCmdPrefix := []string{"codex", "app-server"}
	if len(cmd) < len(wantCmdPrefix) {
		t.Fatalf("BuildSession() cmd = %v, want prefix %v", cmd, wantCmdPrefix)
	}
	for i, want := range wantCmdPrefix {
		if cmd[i] != want {
			t.Fatalf("BuildSession() cmd[%d] = %q, want %q (full cmd %v)", i, cmd[i], want, cmd)
		}
	}
	for i := 0; i < len(cmd); i++ {
		if cmd[i] == "--agents" {
			t.Fatalf("BuildSession() unexpectedly emitted --agents for codex command %v", cmd)
		}
	}
	if sessOpts == nil {
		t.Fatal("BuildSession() session opts = nil, want non-nil")
	}
	if sessOpts.ProviderName != "codex" {
		t.Fatalf("BuildSession() ProviderName = %q, want codex", sessOpts.ProviderName)
	}
	if sessOpts.Protocol == nil {
		t.Fatal("BuildSession() Protocol = nil, want non-nil")
	}
}

func TestCodexBuildSessionAgentNamesDoNotChangeReconciliation(t *testing.T) {
	dir := t.TempDir()
	homeDir := filepath.Join(dir, "home")
	t.Setenv("HOME", homeDir)
	codexHome := filepath.Join(dir, "codex-home")
	t.Setenv("CODEX_HOME", codexHome)

	eventCh := make(chan interface{}, 8)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)
	pr.Registry = newRegistryWithProviders()

	baseOpts := BuildSessionOpts{
		Model:        "gpt-5.4",
		Prompt:       "test prompt",
		SystemPrompt: "system prompt",
		PIDDir:       filepath.Join(dir, "pid"),
		PermHandler:  permHandlerFor(false, nil, ""),
		WorkDir:      dir,
	}

	cmdEmpty, envEmpty, sessOptsEmpty, err := pr.BuildSession(BuildSessionOpts{
		Model:        baseOpts.Model,
		Prompt:       baseOpts.Prompt,
		SystemPrompt: baseOpts.SystemPrompt,
		PIDDir:       baseOpts.PIDDir,
		PermHandler:  baseOpts.PermHandler,
		WorkDir:      baseOpts.WorkDir,
		AgentNames:   []string{},
	})
	if err != nil {
		t.Fatalf("BuildSession() with explicit empty AgentNames error: %v", err)
	}
	snapshotEmpty := snapshotCodexAgentHome(t, codexHome)

	cmdNamed, envNamed, sessOptsNamed, err := pr.BuildSession(BuildSessionOpts{
		Model:        baseOpts.Model,
		Prompt:       baseOpts.Prompt,
		SystemPrompt: baseOpts.SystemPrompt,
		PIDDir:       baseOpts.PIDDir,
		PermHandler:  baseOpts.PermHandler,
		WorkDir:      baseOpts.WorkDir,
		AgentNames:   []string{"codebase-locator", "web-search-researcher"},
	})
	if err != nil {
		t.Fatalf("BuildSession() with named AgentNames error: %v", err)
	}
	snapshotNamed := snapshotCodexAgentHome(t, codexHome)

	requireOnlyAgenticoBinEnv(t, envEmpty)
	requireOnlyAgenticoBinEnv(t, envNamed)
	if !reflect.DeepEqual(cmdEmpty, cmdNamed) {
		t.Fatalf("BuildSession() command changed with AgentNames: empty=%v named=%v", cmdEmpty, cmdNamed)
	}
	if strings.Contains(strings.Join(cmdEmpty, " "), "--agents") {
		t.Fatalf("BuildSession() unexpectedly emitted --agents for codex command %v", cmdEmpty)
	}
	if !reflect.DeepEqual(snapshotEmpty, snapshotNamed) {
		t.Fatalf("Codex home reconciliation changed with AgentNames: empty=%v named=%v", snapshotEmpty, snapshotNamed)
	}
	if sessOptsEmpty == nil || sessOptsNamed == nil {
		t.Fatal("BuildSession() session opts = nil")
	}
	if sessOptsEmpty.ProviderName != "codex" || sessOptsNamed.ProviderName != "codex" {
		t.Fatalf("BuildSession() ProviderName = %q / %q, want codex / codex", sessOptsEmpty.ProviderName, sessOptsNamed.ProviderName)
	}
}

func TestCodexBuildSessionFailuresAbortBeforeLaunch(t *testing.T) {
	dir := t.TempDir()
	blockingFile := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blockingFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", blockingFile, err)
	}

	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("CODEX_HOME", blockingFile)

	eventCh := make(chan interface{}, 8)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)
	pr.Registry = newRegistryWithProviders()

	cmd, env, sessOpts, err := pr.BuildSession(BuildSessionOpts{
		Model:       "gpt-5.4",
		Prompt:      "test prompt",
		PIDDir:      filepath.Join(dir, "pid"),
		PermHandler: permHandlerFor(false, nil, ""),
		WorkDir:     dir,
	})
	if err == nil {
		t.Fatal("BuildSession() error = nil, want failure")
	}
	if cmd != nil {
		t.Fatalf("BuildSession() cmd = %v, want nil on error", cmd)
	}
	if env != nil {
		t.Fatalf("BuildSession() env = %v, want nil on error", env)
	}
	if sessOpts != nil {
		t.Fatalf("BuildSession() session opts = %#v, want nil on error", sessOpts)
	}
	wantErrHas := "building command for codex: preparing codex home:"
	if !strings.Contains(err.Error(), wantErrHas) {
		t.Fatalf("BuildSession() error = %q, want substring %q", err, wantErrHas)
	}
}

func TestAgentLayerDoesNotReferenceRetiredCodexHomeHelpers(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob *.go: %v", err)
	}

	bannedSnippets := []string{
		"CodexSessionEnv(",
		"SetupCodexHome(",
		"CODEX_HOME=",
	}

	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		content := string(data)
		for _, snippet := range bannedSnippets {
			if strings.Contains(content, snippet) {
				t.Errorf("%s unexpectedly contains retired Codex home wiring %q", path, snippet)
			}
		}
	}
}

func snapshotCodexAgentHome(t *testing.T, codexHome string) []string {
	t.Helper()

	agentsDir := filepath.Join(codexHome, "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", agentsDir, err)
	}

	snapshot := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(agentsDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", path, err)
		}
		snapshot = append(snapshot, entry.Name()+":"+string(data))
	}
	sort.Strings(snapshot)
	return snapshot
}

func TestAgentSessionEntryPointsWireEnvVariable(t *testing.T) {
	files := []string{
		"phase.go",
		"implement.go",
		"plan_validation.go",
	}

	for _, filename := range files {
		t.Run(filename, func(t *testing.T) {
			path := filepath.Join(".", filename)
			fset := token.NewFileSet()
			node, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parsing %s: %v", filename, err)
			}

			var sessionEntryPoints int
			var nilEnvCalls []string

			ast.Inspect(node, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}

				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}

				switch sel.Sel.Name {
				case "StartSession":
					sessionEntryPoints++
					if len(call.Args) < 7 {
						nilEnvCalls = append(nilEnvCalls, fset.Position(call.Pos()).String())
						return true
					}

					envArg := call.Args[5]
					ident, ok := envArg.(*ast.Ident)
					if ok && ident.Name == "nil" {
						nilEnvCalls = append(nilEnvCalls,
							fset.Position(call.Pos()).String()+": passes nil instead of env")
					}
				case "RunReadOnlyReviewHelper", "RunBoundedHelper":
					sessionEntryPoints++
				}

				return true
			})

			if sessionEntryPoints == 0 {
				t.Errorf("found 0 session entry points in %s, want at least 1", filename)
			}
			if len(nilEnvCalls) > 0 {
				t.Errorf("found %d StartSession call(s) passing nil for env in %s:\n  %s",
					len(nilEnvCalls), filename, strings.Join(nilEnvCalls, "\n  "))
			}
		})
	}
}
