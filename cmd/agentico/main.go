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
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/guidelinedef"
	"github.com/doordash-oss/agentic-orchestrator/internal/instancelock"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	claude "github.com/doordash-oss/agentic-orchestrator/internal/llm/claude"
	codex "github.com/doordash-oss/agentic-orchestrator/internal/llm/codex"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/internal/skilldef"
	"github.com/doordash-oss/agentic-orchestrator/internal/tui"
	"github.com/doordash-oss/agentic-orchestrator/internal/tui/markdown"
	"go.uber.org/fx"
	"golang.org/x/term"
)

const (
	// Fresh installs default to the Agentic Orchestrator runtime parent.
	defaultRuntimeParent = "~/.agentic-orchestrator"
	// Existing default installs may still live under the legacy parent; we
	// fall back to it when no explicit paths are provided and the new
	// parent does not yet exist, so user data remains discoverable.
	legacyRuntimeParent = "~/.agentic-workflow"

	defaultConfigBasename = "config.yaml"
	defaultStateBasename  = "features"
	defaultLogBasename    = "agentico.log"
)

func main() {
	run()
}

type launchMode int

const (
	launchModeTUI launchMode = iota
	launchModeHelp
	launchModeVersion
)

type launchOptions struct {
	configPath           string
	stateDir             string
	dangerouslySkipPerms bool
	enabledProviders     []string
	mode                 launchMode
}

type tuiLauncher func(configPath, stateDir string, dangerouslySkipPerms bool, enabledProviders []string)

func defaultLaunchOptions() launchOptions {
	parent := pickRuntimeParent(os.Stat)
	return launchOptions{
		configPath: filepath.Join(parent, defaultConfigBasename),
		stateDir:   filepath.Join(parent, defaultStateBasename),
	}
}

// pickRuntimeParent returns the runtime parent directory used to derive
// default paths when the user has not passed --config or --state-dir.
// Fresh installs land under the Agentic Orchestrator namespace. When the
// new namespace is absent but the legacy default exists, recover by using
// the legacy parent in place — without copying or moving any user data.
func pickRuntimeParent(stat func(string) (os.FileInfo, error)) string {
	newParent := config.ExpandHome(defaultRuntimeParent)
	legacyParent := config.ExpandHome(legacyRuntimeParent)
	if _, err := stat(newParent); err == nil {
		return newParent
	}
	if _, err := stat(legacyParent); err == nil {
		return legacyParent
	}
	return newParent
}

func run() {
	if len(os.Args) > 1 && os.Args[1] == codex.ReadOnlyApplyPatchHookFlag {
		os.Exit(codex.RunReadOnlyApplyPatchHook(os.Stdin, os.Stdout))
	}
	os.Exit(runArgs(os.Args[1:], os.Stdout, os.Stderr, runTUI))
}

func runArgs(args []string, stdout, stderr io.Writer, launch tuiLauncher) int {
	opts, err := parseLaunchArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	switch opts.mode {
	case launchModeHelp:
		printUsage(stdout)
		return 0
	case launchModeVersion:
		fmt.Fprintf(stdout, "agentico v%s\n", tui.GetVersion())
		return 0
	default:
		launch(opts.configPath, opts.stateDir, opts.dangerouslySkipPerms, opts.enabledProviders)
		return 0
	}
}

func parseLaunchArgs(args []string) (launchOptions, error) {
	opts := defaultLaunchOptions()
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--config":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--config requires a value")
			}
			i++
			opts.configPath = args[i]
		case "--state-dir":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--state-dir requires a value")
			}
			i++
			opts.stateDir = args[i]
		case "--dangerously-skip-permissions":
			opts.dangerouslySkipPerms = true
		case "--providers":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--providers requires a value")
			}
			i++
			opts.enabledProviders = strings.Split(args[i], ",")
		case "--help", "-h":
			opts.mode = launchModeHelp
			return opts, nil
		case "--version", "-v":
			opts.mode = launchModeVersion
			return opts, nil
		case "--refresh-models":
			return opts, fmt.Errorf("unknown flag: --refresh-models")
		default:
			if strings.HasPrefix(arg, "-") {
				return opts, fmt.Errorf("unknown flag: %s", arg)
			}
			return opts, fmt.Errorf("unknown command: %s", arg)
		}
	}
	return opts, nil
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `Agentic Orchestrator

Usage: agentico [flags]

Launches the Bubble Tea TUI dashboard.

Flags:
  --config <path>                  Config file path (default: ~/.agentic-orchestrator/config.yaml)
  --state-dir <path>               State directory path (default: ~/.agentic-orchestrator/features)
  --providers <list>               Comma-separated provider list (default: all)
                                   Available: claude, codex
  --dangerously-skip-permissions   Skip all permission prompts (use with caution)
  --help, -h                       Show this help
  --version, -v                    Show version

When no explicit paths are passed, an existing ~/.agentic-workflow/
runtime parent is used in place so legacy installs remain discoverable.`)
}

// checkRequiredProviders uses the registry to verify provider CLIs are available.
// Returns (detected providers, warnings, error): errors when none are available.
func checkRequiredProviders(registry *llm.Registry) ([]llm.LLMProvider, []string, error) {
	detected := registry.DetectedProviders()
	all := registry.All()
	if len(detected) == 0 {
		return nil, nil, fmt.Errorf("%s", agent.FormatNoCLIMessage(all))
	}
	var warnings []string
	if len(detected) < len(all) {
		for _, p := range all {
			if !p.DetectCLI() {
				warnings = append(warnings, fmt.Sprintf("Warning: %s CLI not found. Install with: %s", p.Name(), p.InstallHint()))
			}
		}
	}
	return detected, warnings, nil
}

func runTUI(configPath, stateDir string, dangerouslySkipPerms bool, enabledProviders []string) {
	lock, acquired, owner, err := instancelock.Acquire(filepath.Dir(stateDir), stateDir, configPath, tui.GetVersion())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error acquiring instance lock: %v\n", err)
		os.Exit(1)
	}
	if !acquired {
		fmt.Fprint(os.Stderr, formatInstanceLockBusyMessage(stateDir, owner))
		os.Exit(1)
	}
	defer func() {
		if err := lock.Close(); err != nil {
			log.Printf("release instance lock: %v", err)
		}
	}()

	// Detect first-run before config is loaded (LoadOrCreate will create it).
	configIsNew := !fileExists(configPath)

	workspaceDir, _ := os.Getwd()
	tui.SetMarkdownRenderer(markdown.Render)

	// Use fx for dependency injection only — not lifecycle management.
	// The TUI's p.Run() is the main event loop; fx.App.Run() would block
	// forever after it returns waiting for SIGINT/SIGTERM.
	var app tui.AppModel
	var fm *feature.Manager
	var sm *session.Manager
	var orch *orchestrator.Orchestrator
	var registry *llm.Registry
	var cfg *config.Config
	var phaseRunner *agent.PhaseRunner
	fxApp := fx.New(
		// Infrastructure
		fx.Supply(
			fx.Annotate(configPath, fx.ResultTags(`name:"configPath"`)),
			fx.Annotate(stateDir, fx.ResultTags(`name:"stateDir"`)),
			fx.Annotate(dangerouslySkipPerms, fx.ResultTags(`name:"dsp"`)),
			fx.Annotate(workspaceDir, fx.ResultTags(`name:"workspaceDir"`)),
		),
		fx.Provide(fx.Annotate(
			func() chan interface{} { return make(chan interface{}, 1000) },
			fx.ResultTags(`name:"eventCh"`),
		)),

		// Modules
		config.Module,
		feature.Module,
		git.Module,
		session.Module,
		observe.Module,
		permission.Module,
		llm.Module,
		fx.Options(providerFxModules(enabledProviders)...),
		agent.Module,
		orchestrator.Module,
		tuiModule,

		// Apply notification settings after config is loaded
		fx.Invoke(func(c *config.Config) {
			if c.Notifications.TerminalBundleID != "" {
				tui.SetTerminalBundleID(c.Notifications.TerminalBundleID)
			}
		}),

		// Extract components for use after fx.Start
		fx.Populate(&app, &fm, &sm, &orch, &registry, &cfg, &phaseRunner),

		// Silence fx startup/shutdown logs
		fx.NopLogger,
	)

	if err := fxApp.Start(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing: %v\n", err)
		os.Exit(1)
	}

	// Check provider CLIs after fx initialization (registry is now populated)
	detected, warnings, err := checkRequiredProviders(registry)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, w)
	}

	// Check required external tools (git, gh).
	toolErrors, toolWarnings := agent.CheckRequiredTools()
	for _, w := range toolWarnings {
		fmt.Fprintln(os.Stderr, w)
	}
	if len(toolErrors) > 0 {
		for _, e := range toolErrors {
			fmt.Fprintln(os.Stderr, e)
		}
		os.Exit(1)
	}

	// Reconcile embedded skills and guidelines to disk (non-fatal). When
	// either side has work to do (stamp missing or mismatched), surface a
	// spinner so the user knows the pause is intentional.
	skillsDir := filepath.Join(filepath.Dir(stateDir), "skills")
	guidelinesDir := filepath.Join(filepath.Dir(stateDir), "guidelines")
	stop := func() {}
	if skilldef.NeedsReconcile(skillsDir) || guidelinedef.NeedsReconcile(guidelinesDir) {
		stop = startSyncSpinner(os.Stderr, "Syncing skills and guidelines")
	}
	if err := skilldef.ReconcileSkills(skillsDir); err != nil {
		stop()
		stop = func() {}
		fmt.Fprintf(os.Stderr, "Warning: could not reconcile skills: %v\n", err)
	} else {
		phaseRunner.SkillsDir = skillsDir
	}
	if err := guidelinedef.ReconcileGuidelines(guidelinesDir); err != nil {
		stop()
		stop = func() {}
		fmt.Fprintf(os.Stderr, "Warning: could not reconcile guidelines: %v\n", err)
	} else {
		phaseRunner.GuidelinesDir = guidelinesDir
	}
	stop()

	// Provider-generic version check (replaces old CheckCLIVersion)
	for _, vr := range agent.CheckProviderVersions(detected) {
		if vr.Err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not check %s CLI version: %v\n", vr.Provider, vr.Err)
		} else if vr.Warning != "" {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", vr.Warning)
		} else {
			fmt.Fprintf(os.Stderr, "%s CLI version: %s\n", vr.Provider, vr.Version)
		}
	}

	// First-run setup: when both CLIs are detected and config is new,
	// prompt user to choose a preferred provider.
	var preferredProvider string
	if agent.ShouldRunFirstSetup(configIsNew, len(detected)) {
		preferredProvider = agent.RunFirstSetup(detected, os.Stdin, os.Stderr)
	}

	// Apply catalog-driven defaults after discovery. For a brand-new config,
	// replace the bootstrap defaults entirely so persisted config reflects the
	// discovered catalogs rather than built-in placeholders.
	changed := applyCatalogModelDefaultsToConfig(cfg, registry, configIsNew, preferredProvider)

	// When --providers limits the registry, remap model defaults that reference
	// unavailable providers to valid models. This is a runtime adjustment only —
	// don't persist provider-filtered defaults to the config file.
	if enabledProviders != nil {
		remapUnresolvableModels(cfg, registry)
	} else if changed {
		_ = config.Save(configPath, cfg)
	}

	// Redirect stderr and Go's default logger to a file before the TUI takes
	// over the terminal. Any write to stderr (from OTEL, gRPC, net/http, etc.)
	// would corrupt bubbletea's alternate screen rendering.
	logPath := filepath.Join(filepath.Dir(stateDir), defaultLogBasename)
	if logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
		origStderr := os.Stderr
		os.Stderr = logFile
		log.SetOutput(logFile)
		defer func() {
			os.Stderr = origStderr
			log.SetOutput(origStderr)
			logFile.Close()
		}()
	}

	// Run the TUI — this blocks until the user quits.
	// WithFilter drops excess scroll events (trackpad kinetic bursts) before
	// Update/View runs, so typing and Esc aren't queued behind them.
	opts := []tea.ProgramOption{tea.WithFilter(tui.NewScrollRateLimiter())}
	if profile, ok := overrideColorProfile(); ok {
		opts = append(opts, tea.WithColorProfile(profile))
	}
	p := tea.NewProgram(app, opts...)
	app.SetProgram(p)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	app.Close()
	shutdownFeatures(orch, sm)
	_ = fxApp.Stop(context.Background())
}

func overrideColorProfile() (colorprofile.Profile, bool) {
	if os.Getenv("TERM_PROGRAM") == "Apple_Terminal" {
		return colorprofile.ANSI256, true
	}
	return 0, false
}

// startSyncSpinner shows a "<msg>..." indicator on w while the embedded
// skills/guidelines reconcile runs. On a TTY the trailing dots animate so
// the user can tell the process is still doing work; on a non-TTY (CI,
// piped output) a single static line is printed instead. The returned
// stop() blocks until the animation goroutine has cleared its line, so
// subsequent writes to w don't collide with the spinner.
func startSyncSpinner(w io.Writer, msg string) func() {
	f, _ := w.(*os.File)
	isTTY := f != nil && term.IsTerminal(int(f.Fd()))
	if !isTTY {
		fmt.Fprintf(w, "%s...\n", msg)
		return func() {}
	}

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		frames := []string{".  ", ".. ", "..."}
		i := 0
		fmt.Fprintf(w, "\r%s%s", msg, frames[i])
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				clear := strings.Repeat(" ", len(msg)+len(frames[0]))
				fmt.Fprintf(w, "\r%s\r%s... done\n", clear, msg)
				return
			case <-ticker.C:
				i = (i + 1) % len(frames)
				fmt.Fprintf(w, "\r%s%s", msg, frames[i])
			}
		}
	})
	return func() {
		close(done)
		wg.Wait()
	}
}

func formatInstanceLockBusyMessage(stateDir string, owner instancelock.Owner) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Another Agentic instance is already running for state dir %s.\n", stateDir)
	if owner.PID != 0 {
		fmt.Fprintf(&b, "Owner: pid %d", owner.PID)
		if owner.PGID != 0 {
			fmt.Fprintf(&b, ", process group %d", owner.PGID)
		}
		if !owner.StartedAt.IsZero() {
			fmt.Fprintf(&b, ", started %s", owner.StartedAt.Format(time.RFC3339))
		}
		b.WriteString("\n")
		if owner.Config != "" {
			fmt.Fprintf(&b, "Config: %s\n", owner.Config)
		}
	} else {
		b.WriteString("Owner metadata was not available, but the OS lock is still held.\n")
	}
	b.WriteString("Use the existing instance, or start an isolated instance with both --config and --state-dir.\n")
	return b.String()
}

// fileExists reports whether path exists as a regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// remapUnresolvableModels replaces model config fields that can't be resolved
// by the current registry with a fallback model from available providers.
// Used when --providers limits which providers are registered.
func remapUnresolvableModels(cfg *config.Config, registry *llm.Registry) {
	m := &cfg.Defaults.Models
	fallback := registry.MostCapableModel("")

	remap := func(field *string) {
		if *field == "" || fallback == "" {
			if *field == "" && fallback != "" {
				*field = fallback
			}
			return
		}
		if _, _, err := registry.ResolveModel(*field); err != nil {
			*field = fallback
		}
	}

	remap(&m.Research)
	remap(&m.Planning)
	remap(&m.Implementation)
	remap(&m.Review)
	remap(&m.Utilities)
	remap(&m.KBBuild)
}

// providerFxModules returns the fx modules for the requested providers.
// When enabled is nil, all providers are included (default behavior).
func providerFxModules(enabled []string) []fx.Option {
	all := map[string]fx.Option{
		"claude": claude.Module,
		"codex":  codex.Module,
	}

	if enabled == nil {
		return []fx.Option{claude.Module, codex.Module}
	}

	var modules []fx.Option
	for _, name := range enabled {
		name = strings.TrimSpace(name)
		if mod, ok := all[name]; ok {
			modules = append(modules, mod)
		} else {
			fmt.Fprintf(os.Stderr, "Warning: unknown provider %q, skipping\n", name)
		}
	}

	if len(modules) == 0 {
		fmt.Fprintln(os.Stderr, "Error: no valid providers specified in --providers flag")
		os.Exit(1)
	}

	return modules
}

func hasAnyModelConfig(m config.ModelConfig) bool {
	return m.Research != "" ||
		m.Planning != "" ||
		m.Implementation != "" ||
		m.Review != "" ||
		m.Utilities != "" ||
		m.KBBuild != ""
}

func mergeModelConfig(base, overlay config.ModelConfig) config.ModelConfig {
	if overlay.Research != "" {
		base.Research = overlay.Research
	}
	if overlay.Planning != "" {
		base.Planning = overlay.Planning
	}
	if overlay.Implementation != "" {
		base.Implementation = overlay.Implementation
	}
	if overlay.Review != "" {
		base.Review = overlay.Review
	}
	if overlay.Utilities != "" {
		base.Utilities = overlay.Utilities
	}
	if overlay.KBBuild != "" {
		base.KBBuild = overlay.KBBuild
	}
	return base
}

func modelConfigDefaultsMap(m config.ModelConfig) map[string]string {
	return map[string]string{
		"research":       m.Research,
		"planning":       m.Planning,
		"implementation": m.Implementation,
		"review":         m.Review,
		"chat":           m.Utilities,
		"kb_build":       m.KBBuild,
	}
}

func canonicalizeModel(registry *llm.Registry, model string) string {
	if registry == nil || model == "" {
		return model
	}
	explicit := strings.IndexByte(model, ':')
	prov, bare, err := registry.ResolveModel(model)
	if err != nil {
		return model
	}
	if explicit > 0 {
		return prov.Name() + ":" + bare
	}
	return bare
}

func canonicalizeModelConfig(registry *llm.Registry, models config.ModelConfig) (config.ModelConfig, bool) {
	updated := models
	updated.Research = canonicalizeModel(registry, models.Research)
	updated.Planning = canonicalizeModel(registry, models.Planning)
	updated.Implementation = canonicalizeModel(registry, models.Implementation)
	updated.Review = canonicalizeModel(registry, models.Review)
	updated.Utilities = canonicalizeModel(registry, models.Utilities)
	updated.KBBuild = canonicalizeModel(registry, models.KBBuild)
	return updated, updated != models
}

func applyCatalogModelDefaultsToConfig(cfg *config.Config, registry *llm.Registry, overwrite bool, preferredProvider string) bool {
	if cfg == nil || registry == nil {
		return false
	}

	models, changed := canonicalizeModelConfig(registry, cfg.Defaults.Models)
	cfg.Defaults.Models = models

	preferredDefaults := config.ModelConfig{}
	if preferredProvider != "" {
		preferredDefaults = registry.CatalogDefaultModelsForProvider(preferredProvider)
	}
	defaults := mergeModelConfig(preferredDefaults, registry.CatalogDefaultModels())
	if !hasAnyModelConfig(defaults) {
		return changed
	}

	if overwrite {
		if cfg.Defaults.Models != defaults {
			cfg.Defaults.Models = defaults
			return true
		}
		return changed
	}

	return agent.ApplyStartupDefaults(cfg, modelConfigDefaultsMap(defaults)) || changed
}

// shutdownFeatures stops all active sessions and transitions any feature in a
// running state to Interrupted so the TUI shows a clean state on next launch.
// Delegates the per-feature transition to orchestrator.InterruptAllRunning,
// which uses the broader Status.IsRunning() set and clears every pending
// help/permission entry — matching the InterruptFeature behavior used
// elsewhere. The bare sm.Shutdown calls remain to close the stoppingCh barrier
// (preventing new sessions) and to catch any sessions spawned by races
// between InterruptAllRunning's per-feature stop and final shutdown.
func shutdownFeatures(orch *orchestrator.Orchestrator, sm *session.Manager) {
	if orch != nil {
		_ = orch.InterruptAllRunning()
	}

	sm.Shutdown()

	// Second shutdown pass: catch any sessions that RunImplementationLoop
	// managed to create between the first Shutdown snapshot and now.
	sm.Shutdown()
}
