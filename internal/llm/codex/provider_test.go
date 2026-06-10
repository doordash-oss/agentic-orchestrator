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
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agentdef"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func embeddedAgentDefs(t *testing.T) map[string]agentdef.AgentDef {
	t.Helper()

	defs, err := agentdef.ParseEmbedded()
	if err != nil {
		t.Fatalf("ParseEmbedded() error: %v", err)
	}
	if len(defs) == 0 {
		t.Fatal("ParseEmbedded() returned no embedded agents")
	}
	return defs
}

func embeddedAgentNames(t *testing.T) []string {
	t.Helper()

	defs := embeddedAgentDefs(t)
	names := make([]string, 0, len(defs))
	for name := range defs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func assertAgentTOMLs(t *testing.T, codexHome string) {
	t.Helper()

	defs := embeddedAgentDefs(t)
	agentsDir := filepath.Join(codexHome, "agents")
	info, err := os.Stat(agentsDir)
	if err != nil || !info.IsDir() {
		t.Fatalf("agents directory not created at %s", agentsDir)
	}

	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		t.Fatalf("reading agents dir: %v", err)
	}
	if len(entries) != len(defs) {
		t.Fatalf("agents dir contains %d entries, want %d", len(entries), len(defs))
	}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".toml") {
			t.Fatalf("unexpected non-TOML entry in agents dir: %s", entry.Name())
		}
	}

	for name, def := range defs {
		path := filepath.Join(agentsDir, name+".toml")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		want := agentTOMLContent(name, def)
		if string(data) != want {
			t.Fatalf("%s content mismatch\nwant:\n%s\ngot:\n%s", path, want, string(data))
		}
	}
}

func assertReasoningOverride(t *testing.T, cmd []string, want string) {
	t.Helper()

	for i := 0; i < len(cmd)-1; i++ {
		if cmd[i] == "-c" && cmd[i+1] == want {
			return
		}
	}
	t.Fatalf("command %v missing -c %s", cmd, want)
}

func assertConfigOverride(t *testing.T, cmd []string, want string) {
	t.Helper()

	for i := 0; i < len(cmd)-1; i++ {
		if cmd[i] == "-c" && cmd[i+1] == want {
			return
		}
	}
	t.Fatalf("command %v missing -c %s", cmd, want)
}

func assertProviderCommand(t *testing.T, gotName string, gotArgs []string, wantName string, wantArgs []string) {
	t.Helper()
	if gotName != wantName {
		t.Fatalf("command name = %q, want %q", gotName, wantName)
	}
	if !slices.Equal(gotArgs, wantArgs) {
		t.Fatalf("command args = %v, want %v", gotArgs, wantArgs)
	}
}

func snapshotAgentModTimes(t *testing.T, codexHome string) map[string]time.Time {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(codexHome, "agents"))
	if err != nil {
		t.Fatalf("reading agents dir: %v", err)
	}
	modTimes := make(map[string]time.Time, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatalf("entry info for %s: %v", entry.Name(), err)
		}
		modTimes[entry.Name()] = info.ModTime()
	}
	return modTimes
}

func TestProviderResolveCodexHome_PrefersEnv(t *testing.T) {
	t.Run("expands CODEX_HOME", func(t *testing.T) {
		homeDir := t.TempDir()
		t.Setenv("HOME", homeDir)
		t.Setenv("CODEX_HOME", "~/custom-codex")

		p := &Provider{}
		got, err := p.resolveCodexHome()
		if err != nil {
			t.Fatalf("resolveCodexHome() error: %v", err)
		}
		want := filepath.Join(homeDir, "custom-codex")
		if got != want {
			t.Fatalf("resolveCodexHome() = %q, want %q", got, want)
		}
	})
}

func TestProviderResolveCodexHome_FallsBackToDefaultHome(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("CODEX_HOME", "")
	t.Setenv("HOME", homeDir)

	p := &Provider{}
	got, err := p.resolveCodexHome()
	if err != nil {
		t.Fatalf("resolveCodexHome() error: %v", err)
	}
	want := filepath.Join(homeDir, ".codex")
	if got != want {
		t.Fatalf("resolveCodexHome() = %q, want %q", got, want)
	}
}

func TestProviderCheckReadinessLoggedIn(t *testing.T) {
	homeDir := t.TempDir()
	codexHome := filepath.Join(homeDir, "codex-home")
	t.Setenv("HOME", homeDir)
	t.Setenv("CODEX_HOME", codexHome)

	p := &Provider{
		runner: func(ctx context.Context, name string, args []string, env []string) ([]byte, error) {
			assertProviderCommand(t, name, args, "codex", []string{"login", "status"})
			return []byte("Logged in using ChatGPT\n"), nil
		},
	}

	status := p.CheckReadiness(context.Background())
	if !status.Ready {
		t.Fatalf("CheckReadiness().Ready = false, detail=%q remedy=%q", status.Detail, status.Remedy)
	}
	if !strings.Contains(status.Detail, "Logged in") {
		t.Fatalf("CheckReadiness().Detail = %q, want login status text", status.Detail)
	}
	if info, err := os.Stat(codexHome); err != nil || !info.IsDir() {
		t.Fatalf("CheckReadiness() did not create CODEX_HOME: info=%v err=%v", info, err)
	}
}

func TestProviderCheckReadinessNotLoggedIn(t *testing.T) {
	homeDir := t.TempDir()
	codexHome := filepath.Join(homeDir, "codex-home")
	t.Setenv("HOME", homeDir)
	t.Setenv("CODEX_HOME", codexHome)

	p := &Provider{
		runner: func(ctx context.Context, name string, args []string, env []string) ([]byte, error) {
			return []byte("Not logged in\n"), errors.New("exit status 1")
		},
	}

	status := p.CheckReadiness(context.Background())
	if status.Ready {
		t.Fatal("CheckReadiness().Ready = true, want false")
	}
	if !strings.Contains(status.Detail, "not logged in") {
		t.Fatalf("CheckReadiness().Detail = %q, want not logged in", status.Detail)
	}
	if !strings.Contains(status.Remedy, "codex login") {
		t.Fatalf("CheckReadiness().Remedy = %q, want codex login", status.Remedy)
	}
}

func TestProviderCheckReadinessHomePreparationFailure(t *testing.T) {
	blockingFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockingFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", blockingFile, err)
	}
	t.Setenv("CODEX_HOME", blockingFile)

	p := &Provider{
		runner: func(ctx context.Context, name string, args []string, env []string) ([]byte, error) {
			t.Fatal("runner should not be called when Codex home cannot be prepared")
			return nil, nil
		},
	}

	status := p.CheckReadiness(context.Background())
	if status.Ready {
		t.Fatal("CheckReadiness().Ready = true, want false")
	}
	if !strings.Contains(status.Detail, "could not prepare Codex home") {
		t.Fatalf("CheckReadiness().Detail = %q, want home preparation failure", status.Detail)
	}
}

func TestProviderBuildCommand_ReturnsNilEnvAndInteractiveEffortOverride(t *testing.T) {
	tests := []struct {
		name    string
		effort  llm.EffortLevel
		wantArg string
	}{
		{"medium", llm.EffortMedium, "model_reasoning_effort=medium"},
		{"high", llm.EffortHigh, "model_reasoning_effort=high"},
		{"max maps to xhigh", llm.EffortMax, "model_reasoning_effort=xhigh"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Provider{}
			homeDir := t.TempDir()
			stateDir := t.TempDir()
			t.Setenv("CODEX_HOME", "")
			t.Setenv("HOME", homeDir)

			cmd, env, err := p.BuildCommand(llm.CommandBuildOpts{
				Model:       "gpt-5.4",
				StateDir:    stateDir,
				EffortLevel: tt.effort,
			})
			if err != nil {
				t.Fatalf("BuildCommand() error: %v", err)
			}
			if env != nil {
				t.Fatalf("BuildCommand() env = %v, want nil", env)
			}
			if len(cmd) < 2 || cmd[0] != "codex" || cmd[1] != "app-server" {
				t.Fatalf("BuildCommand() cmd = %v, want [codex app-server ...]", cmd)
			}
			assertReasoningOverride(t, cmd, tt.wantArg)
			assertConfigOverride(t, cmd, "web_search=live")

			codexHome := filepath.Join(homeDir, ".codex")
			assertAgentTOMLs(t, codexHome)
			if _, err := os.Stat(filepath.Join(stateDir, "codex-home")); !os.IsNotExist(err) {
				t.Fatalf("unexpected synthetic codex-home under state dir: %v", err)
			}
		})
	}
}

func TestProviderReconcileAgenticAgents_Idempotent(t *testing.T) {
	p := &Provider{}
	codexHome := filepath.Join(t.TempDir(), "codex-home")
	t.Setenv("CODEX_HOME", codexHome)

	if err := p.prepareRealHome(); err != nil {
		t.Fatalf("first prepareRealHome() error: %v", err)
	}
	before := snapshotAgentModTimes(t, codexHome)

	// Retained: creates a visible filesystem mtime boundary for idempotence.
	time.Sleep(20 * time.Millisecond)

	if err := p.prepareRealHome(); err != nil {
		t.Fatalf("second prepareRealHome() error: %v", err)
	}
	after := snapshotAgentModTimes(t, codexHome)

	if len(before) != len(after) {
		t.Fatalf("modtime snapshots differ in size: before=%d after=%d", len(before), len(after))
	}
	for name, beforeTime := range before {
		if after[name] != beforeTime {
			t.Fatalf("%s modtime changed across idempotent reconcile: before=%s after=%s", name, beforeTime, after[name])
		}
	}
	assertAgentTOMLs(t, codexHome)
}

func TestProviderReconcileAgenticAgents_ConcurrentSameHome(t *testing.T) {
	p := &Provider{}
	codexHome := filepath.Join(t.TempDir(), "codex-home")
	t.Setenv("CODEX_HOME", codexHome)

	const workers = 16
	start := make(chan struct{})
	errCh := make(chan error, workers)
	var wg sync.WaitGroup

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errCh <- p.prepareRealHome()
		}()
	}

	close(start)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent prepareRealHome() error: %v", err)
		}
	}
	assertAgentTOMLs(t, codexHome)
}

func TestProviderPrepareRealHome_PartialFailureThenRetrySucceeds(t *testing.T) {
	names := embeddedAgentNames(t)
	if len(names) < 2 {
		t.Fatal("need at least two embedded agents for partial failure test")
	}

	p := &Provider{}
	codexHome := filepath.Join(t.TempDir(), "codex-home")
	agentsDir := filepath.Join(codexHome, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", agentsDir, err)
	}
	blockedName := names[len(names)-1]
	blockedPath := filepath.Join(agentsDir, blockedName+".toml")
	if err := os.Mkdir(blockedPath, 0o755); err != nil {
		t.Fatalf("Mkdir(%s): %v", blockedPath, err)
	}
	t.Setenv("CODEX_HOME", codexHome)

	err := p.prepareRealHome()
	if err == nil {
		t.Fatal("prepareRealHome() succeeded unexpectedly with blocked TOML path")
	}
	if !strings.Contains(err.Error(), blockedName) {
		t.Fatalf("prepareRealHome() error = %v, want reference to %q", err, blockedName)
	}

	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", agentsDir, err)
	}
	if len(entries) < 2 {
		t.Fatalf("expected partial reconcile before failure, got %d entries", len(entries))
	}

	if err := os.RemoveAll(blockedPath); err != nil {
		t.Fatalf("RemoveAll(%s): %v", blockedPath, err)
	}
	if err := p.prepareRealHome(); err != nil {
		t.Fatalf("retry prepareRealHome() error: %v", err)
	}

	assertAgentTOMLs(t, codexHome)
}

func TestProviderBuildCommand_UnwritableResolvedHomeFailsFast(t *testing.T) {
	p := &Provider{}
	blockingFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockingFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", blockingFile, err)
	}
	t.Setenv("CODEX_HOME", blockingFile)

	_, env, err := p.BuildCommand(llm.CommandBuildOpts{
		Model:       "gpt-5.4",
		EffortLevel: llm.EffortHigh,
	})
	if err == nil {
		t.Fatal("BuildCommand() succeeded unexpectedly with blocked resolved home")
	}
	if env != nil {
		t.Fatalf("BuildCommand() env = %v, want nil on error", env)
	}
	if !strings.Contains(err.Error(), "preparing codex home: creating codex home") {
		t.Fatalf("BuildCommand() error = %v, want wrapped reconcile failure", err)
	}
}

func TestProviderBuildCommand_AppServerAcceptsReasoningOverride(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping runtime Codex compatibility probe in short mode")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI not installed")
	}

	p := &Provider{}
	codexHome := filepath.Join(t.TempDir(), "codex-home")
	t.Setenv("CODEX_HOME", codexHome)

	cmd, env, err := p.BuildCommand(llm.CommandBuildOpts{
		Model:       "gpt-5.4",
		EffortLevel: llm.EffortHigh,
	})
	if err != nil {
		t.Fatalf("BuildCommand() error: %v", err)
	}
	if env != nil {
		t.Fatalf("BuildCommand() env = %v, want nil", env)
	}
	assertReasoningOverride(t, cmd, "model_reasoning_effort=high")
	assertConfigOverride(t, cmd, "web_search=live")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	execCmd := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	var stdout, stderr bytes.Buffer
	execCmd.Stdout = &stdout
	execCmd.Stderr = &stderr
	stdin, err := execCmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe(): %v", err)
	}

	if err := execCmd.Start(); err != nil {
		t.Fatalf("starting %v: %v\nstderr:\n%s", cmd, err, stderr.String())
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- execCmd.Wait()
	}()

	select {
	case err := <-waitCh:
		_ = stdin.Close()
		t.Fatalf("codex app-server exited immediately: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	case <-time.After(500 * time.Millisecond):
		_ = stdin.Close()
		_ = execCmd.Process.Kill()
		<-waitCh
	}
}

func TestCodexProvider_EnvVarsToExclude(t *testing.T) {
	p := &Provider{}
	if got := p.EnvVarsToExclude(); got != nil {
		t.Fatalf("EnvVarsToExclude() = %v, want nil", got)
	}
}

func TestParseCodexModelCatalog_FiltersAndMapsVisibleAPIModels(t *testing.T) {
	catalogJSON := []byte(`{"models":[
		{"slug":"gpt-5.5","display_name":"GPT-5.5","visibility":"list","supported_in_api":true,"context_window":272000,"max_context_window":272000},
		{"slug":"codex-auto-review","display_name":"Codex Auto Review","visibility":"hide","supported_in_api":true,"context_window":272000},
		{"slug":"gpt-5.4","display_name":"GPT-5.4","visibility":"list","supported_in_api":true,"context_window":272000,"max_context_window":1000000},
		{"slug":"gpt-5.4-mini","display_name":"GPT-5.4-Mini","visibility":"list","supported_in_api":true,"context_window":272000},
		{"slug":"gpt-5.3-codex","display_name":"","visibility":"list","supported_in_api":true,"context_window":400000},
		{"slug":"private-model","display_name":"Private","visibility":"list","supported_in_api":false,"context_window":123000}
	]}`)

	models, err := parseCodexModelCatalog(catalogJSON)
	if err != nil {
		t.Fatalf("parseCodexModelCatalog() error: %v", err)
	}
	gotIDs := make([]string, 0, len(models))
	byID := make(map[string]llm.ModelInfo, len(models))
	for _, model := range models {
		gotIDs = append(gotIDs, model.ID)
		byID[model.ID] = model
	}
	wantIDs := []string{"gpt-5.5[272K]", "gpt-5.4[272K]", "gpt-5.4[1M]", "gpt-5.4-mini[272K]", "gpt-5.3-codex[400K]"}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("model IDs = %v, want %v", gotIDs, wantIDs)
	}
	if got := byID["gpt-5.5[272K]"].Category; got != "capable" {
		t.Errorf("gpt-5.5 category = %q, want capable", got)
	}
	if got := byID["gpt-5.4-mini[272K]"].Category; got != "balanced" {
		t.Errorf("gpt-5.4-mini category = %q, want balanced", got)
	}
	if !slices.Equal(byID["gpt-5.5[272K]"].Aliases, []string{"gpt-5.5"}) {
		t.Errorf("gpt-5.5[272K] aliases = %v, want [gpt-5.5]", byID["gpt-5.5[272K]"].Aliases)
	}
	if got := byID["gpt-5.4[272K]"].ContextWindow; got != 272_000 {
		t.Errorf("gpt-5.4[272K] context window = %d, want 272000", got)
	}
	if got := byID["gpt-5.4[1M]"].ContextWindow; got != 1_000_000 {
		t.Errorf("gpt-5.4[1M] context window = %d, want 1000000", got)
	}
	if !slices.Equal(byID["gpt-5.4[272K]"].Aliases, []string{"gpt-5.4"}) {
		t.Errorf("gpt-5.4[272K] aliases = %v, want [gpt-5.4]", byID["gpt-5.4[272K]"].Aliases)
	}
	codexModel := byID["gpt-5.3-codex[400K]"]
	if got := codexModel.DisplayName; got != "GPT-5.3 Codex (400K)" {
		t.Errorf("gpt-5.3-codex display name = %q, want generated display name", got)
	}
	if !slices.Equal(codexModel.Aliases, []string{"gpt-5.3-codex"}) {
		t.Errorf("gpt-5.3-codex aliases = %v, want [gpt-5.3-codex]", codexModel.Aliases)
	}
	if got := codexModel.ContextWindow; got != 400_000 {
		t.Errorf("gpt-5.3-codex context window = %d, want 400000", got)
	}
}

func TestProviderDiscoverModelCatalog_FallsBackToBundledCatalog(t *testing.T) {
	var calls [][]string
	p := &Provider{
		runner: func(ctx context.Context, name string, args []string, env []string) ([]byte, error) {
			if name != "codex" {
				t.Fatalf("command name = %q, want codex", name)
			}
			calls = append(calls, slices.Clone(args))
			switch len(calls) {
			case 1:
				return []byte("{not-json"), nil
			case 2:
				return []byte(`{"models":[{"slug":"gpt-5.4","display_name":"GPT-5.4","visibility":"list","supported_in_api":true,"context_window":272000}]}`), nil
			default:
				t.Fatalf("unexpected runner call %d: %v", len(calls), args)
				return nil, nil
			}
		},
	}

	models, err := p.DiscoverModelCatalog(context.Background())
	if err != nil {
		t.Fatalf("DiscoverModelCatalog() error: %v", err)
	}
	if len(models) != 1 || models[0].ID != "gpt-5.4[272K]" {
		t.Fatalf("DiscoverModelCatalog() = %+v, want gpt-5.4[272K]", models)
	}
	wantCalls := [][]string{
		{"debug", "models"},
		{"debug", "models", "--bundled"},
	}
	if len(calls) != len(wantCalls) {
		t.Fatalf("runner calls = %v, want %v", calls, wantCalls)
	}
	for i := range calls {
		if !slices.Equal(calls[i], wantCalls[i]) {
			t.Fatalf("runner call %d = %v, want %v", i, calls[i], wantCalls[i])
		}
	}
}

func TestProviderBuildCommand_AddsSelectedContextWindowOverride(t *testing.T) {
	p := &Provider{}
	homeDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("CODEX_HOME", "")
	t.Setenv("HOME", homeDir)

	cmd, env, err := p.BuildCommand(llm.CommandBuildOpts{
		Model:    "gpt-5.4[1M]",
		StateDir: stateDir,
	})
	if err != nil {
		t.Fatalf("BuildCommand() error: %v", err)
	}
	if env != nil {
		t.Fatalf("BuildCommand() env = %v, want nil", env)
	}
	assertConfigOverride(t, cmd, "model_context_window=1000000")
}

// TestCodexProvider_DefaultCatalog_Invariants locks in the hardcoded
// Codex catalog that replaced the former discovery pipeline.
func TestCodexProvider_DefaultCatalog_Invariants(t *testing.T) {
	p := &Provider{}
	cat := p.defaultModelInfos()

	byID := make(map[string]llm.ModelInfo, len(cat))
	for _, m := range cat {
		byID[m.ID] = m
	}

	wantWindows := map[string]int{
		"gpt-5.5[272K]":       272_000,
		"gpt-5.4[272K]":       272_000,
		"gpt-5.4[1M]":         1_000_000,
		"gpt-5.4-mini[400K]":  400_000,
		"gpt-5.3-codex[400K]": 400_000,
		"gpt-5.2[400K]":       400_000,
	}
	for id, want := range wantWindows {
		m, ok := byID[id]
		if !ok {
			t.Errorf("missing catalog entry for %q", id)
			continue
		}
		if m.ContextWindow != want {
			t.Errorf("%s.ContextWindow = %d, want %d", id, m.ContextWindow, want)
		}
	}
}

// TestCodexProvider_ContextWindowForModel_ReturnsHardcodedWithoutSeed
// guards the fallback path that keeps ContextPercentage() honest before
// the first usage event arrives.
func TestCodexProvider_ContextWindowForModel_ReturnsHardcodedWithoutSeed(t *testing.T) {
	p := &Provider{}
	tests := map[string]int{
		"gpt-5.5":             272_000,
		"gpt-5.5[272K]":       272_000,
		"gpt-5.4":             272_000,
		"gpt-5.4[272K]":       272_000,
		"gpt-5.4[1M]":         1_000_000,
		"gpt-5.4-mini":        400_000,
		"gpt-5.4-mini[400K]":  400_000,
		"gpt-5.3-codex":       400_000,
		"gpt-5.3-codex[400K]": 400_000,
		"gpt-5.2":             400_000,
		"gpt-5.2[400K]":       400_000,
	}
	for model, want := range tests {
		t.Run(model, func(t *testing.T) {
			if got := p.ContextWindowForModel(model); got != want {
				t.Errorf("ContextWindowForModel(%q) = %d, want %d", model, got, want)
			}
		})
	}
}
