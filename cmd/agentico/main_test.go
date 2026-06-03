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

package main

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// stubProvider is a minimal LLMProvider for testing checkRequiredProviders.
type stubProvider struct {
	name         string
	models       []string
	hasCLI       bool
	installHint  string
	readiness    llm.ProviderReadiness
	hasReadiness bool
}

func (s *stubProvider) Name() string { return s.name }
func (s *stubProvider) MatchesModel(m string) bool {
	return slices.Contains(s.models, m)
}
func (s *stubProvider) DetectCLI() bool           { return s.hasCLI }
func (s *stubProvider) AvailableModels() []string { return s.models }
func (s *stubProvider) BuildCommand(_ llm.CommandBuildOpts) ([]string, []string, error) {
	return nil, nil, nil
}
func (s *stubProvider) NewProtocol(_ llm.ProtocolOpts) llm.Protocol { return nil }
func (s *stubProvider) InstallHint() string                         { return s.installHint }
func (s *stubProvider) VersionInfo() (string, error)                { return "1.0.0", nil }
func (s *stubProvider) MinVersion() [3]int                          { return [3]int{0, 0, 0} }
func (s *stubProvider) EnvVarsToExclude() []string                  { return nil }
func (s *stubProvider) CheckReadiness(context.Context) llm.ProviderReadiness {
	if s.hasReadiness {
		return s.readiness
	}
	return llm.ProviderReadiness{Ready: true}
}

func TestRunArgsLaunchesTUIByDefault(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var launched bool
	wantParent := pickRuntimeParent(os.Stat)
	code := runArgs(nil, &stdout, &stderr, func(configPath, stateDir string, dangerouslySkipPerms bool, enabledProviders []string) {
		launched = true
		if configPath != filepath.Join(wantParent, defaultConfigBasename) {
			t.Errorf("configPath = %q, want default", configPath)
		}
		if stateDir != filepath.Join(wantParent, defaultStateBasename) {
			t.Errorf("stateDir = %q, want default", stateDir)
		}
		if dangerouslySkipPerms {
			t.Error("dangerouslySkipPerms = true, want false")
		}
		if enabledProviders != nil {
			t.Errorf("enabledProviders = %v, want nil", enabledProviders)
		}
	}, failingUpdater(t))
	if code != 0 {
		t.Errorf("runArgs() code = %d, want 0", code)
	}
	if !launched {
		t.Fatal("launcher was not called")
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("stdout = %q stderr = %q, want both empty", stdout.String(), stderr.String())
	}
}

func TestRunArgsPassesRetainedLaunchFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var gotConfig, gotState string
	var gotDangerouslySkipPerms bool
	var gotProviders []string
	code := runArgs(
		[]string{"--config", "/tmp/agentic-config.yaml", "--state-dir", "/tmp/agentic-features", "--providers", "codex, claude", "--dangerously-skip-permissions"},
		&stdout,
		&stderr,
		func(configPath, stateDir string, dangerouslySkipPerms bool, enabledProviders []string) {
			gotConfig = configPath
			gotState = stateDir
			gotDangerouslySkipPerms = dangerouslySkipPerms
			gotProviders = enabledProviders
		},
		failingUpdater(t),
	)
	if code != 0 {
		t.Errorf("runArgs() code = %d, want 0", code)
	}
	if gotConfig != "/tmp/agentic-config.yaml" {
		t.Errorf("configPath = %q, want custom path", gotConfig)
	}
	if gotState != "/tmp/agentic-features" {
		t.Errorf("stateDir = %q, want custom path", gotState)
	}
	if !gotDangerouslySkipPerms {
		t.Error("dangerouslySkipPerms = false, want true")
	}
	if !slices.Equal(gotProviders, []string{"codex", " claude"}) {
		t.Errorf("enabledProviders = %v, want raw CSV split", gotProviders)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("stdout = %q stderr = %q, want both empty", stdout.String(), stderr.String())
	}
}

func TestParseLaunchArgsRejectsRemovedSurface(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "run alias", args: []string{"run"}, wantErr: "unknown command: run"},
		{name: "feature list", args: []string{"feature", "list"}, wantErr: "unknown command: feature"},
		{name: "feature create", args: []string{"feature", "create", "--name", "x"}, wantErr: "unknown command: feature"},
		{name: "refresh models", args: []string{"--refresh-models"}, wantErr: "unknown flag: --refresh-models"},
		{name: "feature create flag at top level", args: []string{"--name", "x"}, wantErr: "unknown flag: --name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseLaunchArgs(tt.args)
			if err == nil {
				t.Fatalf("parseLaunchArgs(%v) error = nil, want %q", tt.args, tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Fatalf("parseLaunchArgs(%v) error = %q, want %q", tt.args, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestProviderFxModules_NilReturnsAll(t *testing.T) {
	modules := providerFxModules(nil)
	if len(modules) != 2 {
		t.Errorf("expected 2 modules for nil input, got %d", len(modules))
	}
}

func TestProviderFxModules_SingleProvider(t *testing.T) {
	for _, name := range []string{"claude", "codex"} {
		t.Run(name, func(t *testing.T) {
			modules := providerFxModules([]string{name})
			if len(modules) != 1 {
				t.Errorf("expected 1 module for %q, got %d", name, len(modules))
			}
		})
	}
}

func TestProviderFxModules_BothProviders(t *testing.T) {
	modules := providerFxModules([]string{"claude", "codex"})
	if len(modules) != 2 {
		t.Errorf("expected 2 modules, got %d", len(modules))
	}
}

func TestProviderFxModules_TrimsWhitespace(t *testing.T) {
	modules := providerFxModules([]string{" claude ", " codex "})
	if len(modules) != 2 {
		t.Errorf("expected 2 modules after trimming, got %d", len(modules))
	}
}

func TestProviderFxModules_UnknownSkipped(t *testing.T) {
	modules := providerFxModules([]string{"claude", "bogus"})
	if len(modules) != 1 {
		t.Errorf("expected 1 module (bogus skipped), got %d", len(modules))
	}
}

func TestRemapUnresolvableModels(t *testing.T) {
	t.Run("replaces_unresolvable_with_fallback", func(t *testing.T) {
		r := llm.NewRegistry()
		r.Register(&stubProvider{name: "codex", models: []string{"gpt-5.4"}, hasCLI: true})

		cfg := config.NewDefault() // has claude models: opus, sonnet, etc.
		remapUnresolvableModels(cfg, r)

		// All fields should now be "gpt-5.4" since codex is the only provider
		m := cfg.Defaults.Models
		for _, field := range []string{m.Research, m.Planning, m.Implementation, m.Review, m.Utilities, m.KBBuild} {
			if field != "gpt-5.4" {
				t.Errorf("expected gpt-5.4, got %q", field)
			}
		}
	})

	t.Run("keeps_resolvable_models", func(t *testing.T) {
		r := llm.NewRegistry()
		r.Register(&stubProvider{name: "claude", models: []string{"opus", "sonnet"}, hasCLI: true})

		cfg := config.NewDefault()
		remapUnresolvableModels(cfg, r)

		// opus and sonnet should stay; gpt-5.4 (review) should become opus (most capable)
		if cfg.Defaults.Models.Research != "opus" {
			t.Errorf("research should stay opus, got %q", cfg.Defaults.Models.Research)
		}
		if cfg.Defaults.Models.Utilities != "sonnet" {
			t.Errorf("utilities should stay sonnet, got %q", cfg.Defaults.Models.Utilities)
		}
	})
}

func TestCheckRequiredProviders_AllDetected(t *testing.T) {
	r := llm.NewRegistry()
	r.Register(&stubProvider{name: "claude", models: []string{"opus"}, hasCLI: true, installHint: "install claude"})
	r.Register(&stubProvider{name: "codex", models: []string{"gpt-5.4"}, hasCLI: true, installHint: "install codex"})

	detected, warnings, notices, filtered, err := checkRequiredProviders(context.Background(), r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(notices) != 0 {
		t.Fatalf("expected no startup notices, got %v", notices)
	}
	if filtered {
		t.Fatal("filtered = true, want false")
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got %v", warnings)
	}
	if len(detected) != 2 {
		t.Errorf("expected 2 detected providers, got %d", len(detected))
	}
}

func TestCheckRequiredProviders_OneDetected(t *testing.T) {
	r := llm.NewRegistry()
	r.Register(&stubProvider{name: "claude", models: []string{"opus"}, hasCLI: true, installHint: "install claude"})
	r.Register(&stubProvider{name: "codex", models: []string{"gpt-5.4"}, hasCLI: false, installHint: "install codex"})

	detected, warnings, notices, filtered, err := checkRequiredProviders(context.Background(), r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !filtered {
		t.Fatal("filtered = false, want true when a registered provider is unavailable")
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no immediate warnings, got %d: %v", len(warnings), warnings)
	}
	if len(notices) != 1 {
		t.Fatalf("expected 1 startup notice, got %d: %v", len(notices), notices)
	}
	if !strings.Contains(notices[0], "Provider codex CLI was not found") || !strings.Contains(notices[0], "Starting with claude only") {
		t.Fatalf("startup notice = %q, want missing codex/start claude", notices[0])
	}
	if len(detected) != 1 {
		t.Errorf("expected 1 detected provider, got %d", len(detected))
	}
}

func TestPickRuntimeParent(t *testing.T) {
	newParent := config.ExpandHome(defaultRuntimeParent)
	legacyParent := config.ExpandHome(legacyRuntimeParent)

	makeStat := func(present map[string]bool) func(string) (os.FileInfo, error) {
		return func(p string) (os.FileInfo, error) {
			if present[p] {
				return fakeDirInfo{}, nil
			}
			return nil, &fs.PathError{Op: "stat", Path: p, Err: fs.ErrNotExist}
		}
	}

	tests := []struct {
		name    string
		present map[string]bool
		want    string
	}{
		{
			name:    "fresh install — neither parent exists, prefer new namespace",
			present: map[string]bool{},
			want:    newParent,
		},
		{
			name:    "new namespace exists, legacy absent",
			present: map[string]bool{newParent: true},
			want:    newParent,
		},
		{
			name:    "legacy parent exists, new absent — recover in place",
			present: map[string]bool{legacyParent: true},
			want:    legacyParent,
		},
		{
			name:    "both exist — new namespace wins",
			present: map[string]bool{newParent: true, legacyParent: true},
			want:    newParent,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pickRuntimeParent(makeStat(tt.present))
			if got != tt.want {
				t.Errorf("pickRuntimeParent = %q, want %q", got, tt.want)
			}
		})
	}
}

type fakeDirInfo struct{}

func (fakeDirInfo) Name() string       { return "" }
func (fakeDirInfo) Size() int64        { return 0 }
func (fakeDirInfo) Mode() fs.FileMode  { return fs.ModeDir | 0o755 }
func (fakeDirInfo) ModTime() time.Time { return time.Time{} }
func (fakeDirInfo) IsDir() bool        { return true }
func (fakeDirInfo) Sys() any           { return nil }

func TestPrintUsageAdvertisesRenamedDefaults(t *testing.T) {
	var b bytes.Buffer
	printUsage(&b)
	out := b.String()
	for _, want := range []string{
		"Agentic Orchestrator",
		"agentico [flags]",
		"~/.agentic-orchestrator/config.yaml",
		"~/.agentic-orchestrator/features",
		"~/.agentic-workflow/", // legacy-recovery callout retained
	} {
		if !strings.Contains(out, want) {
			t.Errorf("printUsage missing %q\nfull:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Agentic Workflow Orchestrator") {
		t.Errorf("printUsage still advertises legacy product title:\n%s", out)
	}
	if strings.Contains(out, "Commands:") {
		t.Errorf("printUsage must not introduce a Commands section:\n%s", out)
	}
}

func TestCheckRequiredProviders_NoneDetected(t *testing.T) {
	r := llm.NewRegistry()
	r.Register(&stubProvider{name: "claude", models: []string{"opus"}, hasCLI: false, installHint: "install claude"})
	r.Register(&stubProvider{name: "codex", models: []string{"gpt-5.4"}, hasCLI: false, installHint: "install codex"})

	_, _, _, _, err := checkRequiredProviders(context.Background(), r)
	if err == nil {
		t.Fatal("expected error when no providers detected")
	}
}

func TestCheckRequiredProviders_UnreadyProviderWarnsAndFiltersRegistry(t *testing.T) {
	r := llm.NewRegistry()
	r.Register(&stubProvider{name: "claude", models: []string{"opus"}, hasCLI: true, installHint: "install claude", hasReadiness: true, readiness: llm.ProviderReadiness{Ready: true}})
	r.Register(&stubProvider{name: "codex", models: []string{"gpt-5.4"}, hasCLI: true, installHint: "install codex", readiness: llm.ProviderReadiness{
		Ready:  false,
		Detail: "not logged in",
		Remedy: "run 'codex login'",
	}, hasReadiness: true})

	detected, warnings, notices, filtered, err := checkRequiredProviders(context.Background(), r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !filtered {
		t.Fatal("filtered = false, want true")
	}
	if len(detected) != 1 || detected[0].Name() != "claude" {
		t.Fatalf("detected = %v, want only claude", providerNames(detected))
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want no immediate warnings", warnings)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "Provider codex is not configured") || !strings.Contains(notices[0], "codex login") || !strings.Contains(notices[0], "Starting with claude only") {
		t.Fatalf("notices = %v, want codex startup notice", notices)
	}
	if _, _, err := r.ResolveModel("codex:gpt-5.4"); err == nil {
		t.Fatal("ResolveModel(codex:gpt-5.4) succeeded after codex was filtered")
	}
	if got := r.AvailableModels(); !slices.Equal(got, []string{"opus"}) {
		t.Fatalf("AvailableModels() = %v, want [opus]", got)
	}
}

func TestCheckRequiredProviders_NoneReadyReportsProviderSpecificFix(t *testing.T) {
	r := llm.NewRegistry()
	r.Register(&stubProvider{name: "claude", models: []string{"opus"}, hasCLI: true, installHint: "install claude", readiness: llm.ProviderReadiness{
		Ready:  false,
		Detail: "not authenticated",
		Remedy: "run 'claude auth login'",
	}, hasReadiness: true})
	r.Register(&stubProvider{name: "codex", models: []string{"gpt-5.4"}, hasCLI: false, installHint: "install codex"})

	_, _, _, filtered, err := checkRequiredProviders(context.Background(), r)
	if err == nil {
		t.Fatal("expected error when no providers are ready")
	}
	if !filtered {
		t.Fatal("filtered = false, want true")
	}
	msg := err.Error()
	for _, want := range []string{
		"No ready AI coding assistant providers detected",
		"claude",
		"not authenticated",
		"claude auth login",
		"Missing provider CLIs",
		"codex",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error missing %q:\n%s", want, msg)
		}
	}
}

func providerNames(providers []llm.LLMProvider) []string {
	names := make([]string, 0, len(providers))
	for _, p := range providers {
		names = append(names, p.Name())
	}
	return names
}

func TestShowProviderStartupNoticesWritesBeforeTUIWithoutForcedDelayInTests(t *testing.T) {
	var out bytes.Buffer
	showProviderStartupNotices(&out, []string{"Provider codex is not configured: not logged in. Starting with claude only."}, 0)
	if got := out.String(); got != "Provider codex is not configured: not logged in. Starting with claude only.\n" {
		t.Fatalf("startup notice output = %q", got)
	}
}

// TestOverrideColorProfileForcesANSI256InAppleTerminal guards the workaround
// for macOS Terminal.app: when TERM_PROGRAM=Apple_Terminal we force the
// bubbletea renderer to ANSI256, overriding the user's COLORTERM env var
// (commonly set to "truecolor" by Neovim/tmux tutorials). Terminal.app
// silently drops 24-bit escapes, so without the override the TUI renders as
// default foreground.
func TestOverrideColorProfileForcesANSI256InAppleTerminal(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "Apple_Terminal")
	t.Setenv("COLORTERM", "truecolor") // user lie that we're overriding

	profile, ok := overrideColorProfile()
	if !ok {
		t.Fatal("expected override to be applied for Apple_Terminal")
	}
	if profile != colorprofile.ANSI256 {
		t.Errorf("profile = %v, want ANSI256", profile)
	}
}

// TestOverrideColorProfileLeavesOtherTerminalsAlone ensures iTerm2 / WezTerm /
// Ghostty / etc. continue receiving full 24-bit color via bubbletea's normal
// auto-detection.
func TestOverrideColorProfileLeavesOtherTerminalsAlone(t *testing.T) {
	for _, term := range []string{"iTerm.app", "WezTerm", "ghostty", "vscode", ""} {
		t.Run(term, func(t *testing.T) {
			t.Setenv("TERM_PROGRAM", term)
			if _, ok := overrideColorProfile(); ok {
				t.Errorf("TERM_PROGRAM=%q should not trigger an override", term)
			}
		})
	}
}
