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
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/clirun"
	codex "github.com/doordash-oss/agentic-orchestrator/internal/llm/codex"
	opencode "github.com/doordash-oss/agentic-orchestrator/internal/llm/opencode"
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
	launchModeUpdate
	launchModeValidateArtifacts
)

type launchOptions struct {
	configPath           string
	stateDir             string
	dangerouslySkipPerms bool
	enabledProviders     []string
	mode                 launchMode
	validateArtifacts    validateArtifactsOptions
	// updateCheck is set when update mode was selected with --check / -n,
	// requesting a check-only run that never attempts to install.
	updateCheck bool
}

type validateArtifactsOptions struct {
	phase string
	role  string
	dir   string
}

type tuiLauncher func(configPath, stateDir string, dangerouslySkipPerms bool, enabledProviders []string)

// updater is the injectable update seam. It mirrors tuiLauncher: production
// wiring passes the real updater, tests pass a fake. It returns the process
// exit code the router propagates verbatim. The update path deliberately never
// acquires the instance lock, starts the fx container, or reconciles assets.
type updater func(checkOnly bool, stdout, stderr io.Writer) int

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
	os.Exit(runArgs(os.Args[1:], os.Stdout, os.Stderr, runTUI, runUpdate))
}

func runArgs(args []string, stdout, stderr io.Writer, launch tuiLauncher, update updater) int {
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
	case launchModeUpdate:
		// Dispatch through the updater seam ahead of the TUI branch, early
		// returning its exit code — exactly as help/version early-return.
		// The update path never reaches the TUI launcher below, so it takes
		// no instance lock, builds no fx container, and reconciles no assets.
		return update(opts.updateCheck, stdout, stderr)
	case launchModeValidateArtifacts:
		return runValidateArtifacts(opts.validateArtifacts, stdout, stderr)
	default:
		launch(opts.configPath, opts.stateDir, opts.dangerouslySkipPerms, opts.enabledProviders)
		return 0
	}
}

func parseLaunchArgs(args []string) (launchOptions, error) {
	opts := defaultLaunchOptions()
	// `update` is a standalone subcommand recognized only as the first
	// argument. Its sub-flags (--check / -n) are valid only in this context;
	// elsewhere they fall through to the launch-flag loop and reject as
	// unknown flags, and every other bare word still rejects as an unknown
	// command.
	if len(args) > 0 && args[0] == "update" {
		return parseUpdateArgs(opts, args[1:])
	}
	if len(args) > 0 && args[0] == "validate-artifacts" {
		opts.mode = launchModeValidateArtifacts
		return parseValidateArtifactsArgs(opts, args[1:])
	}
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

func parseValidateArtifactsArgs(opts launchOptions, args []string) (launchOptions, error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--phase":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--phase requires a value")
			}
			i++
			opts.validateArtifacts.phase = args[i]
		case "--role":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--role requires a value")
			}
			i++
			opts.validateArtifacts.role = args[i]
		case "--dir":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--dir requires a value")
			}
			i++
			opts.validateArtifacts.dir = args[i]
		case "--help", "-h":
			opts.mode = launchModeHelp
			return opts, nil
		default:
			if strings.HasPrefix(arg, "-") {
				return opts, fmt.Errorf("unknown validate-artifacts flag: %s", arg)
			}
			return opts, fmt.Errorf("unknown validate-artifacts argument: %s", arg)
		}
	}
	if strings.TrimSpace(opts.validateArtifacts.phase) == "" {
		return opts, fmt.Errorf("validate-artifacts requires --phase")
	}
	if strings.TrimSpace(opts.validateArtifacts.role) == "" {
		return opts, fmt.Errorf("validate-artifacts requires --role")
	}
	if strings.TrimSpace(opts.validateArtifacts.dir) == "" {
		return opts, fmt.Errorf("validate-artifacts requires --dir")
	}
	return opts, nil
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `Agentic Orchestrator

Usage: agentico [flags]
       agentico update [--check|-n]
       agentico validate-artifacts --phase <phase> --role <role> --dir <iteration_dir>

Launches the Bubble Tea TUI dashboard.
Run 'agentico update' to upgrade the binary, or 'agentico update --check'
(alias -n) to report the latest available release without installing.
Run 'agentico validate-artifacts' from agent sessions before phase_complete
to parse and validate role output artifacts without starting the TUI.

Flags:
  --config <path>                  Config file path (default: ~/.agentic-orchestrator/config.yaml)
  --state-dir <path>               State directory path (default: ~/.agentic-orchestrator/features)
  --providers <list>               Comma-separated provider list (default: all)
                                   Available: claude, codex
  --dangerously-skip-permissions   Skip all permission prompts (use with caution)
  --check, -n                      With 'update': check for a newer release without installing
  --help, -h                       Show this help
  --version, -v                    Show version

When no explicit paths are passed, an existing ~/.agentic-workflow/
runtime parent is used in place so legacy installs remain discoverable.`)
}

func runValidateArtifacts(opts validateArtifactsOptions, stdout, stderr io.Writer) int {
	phase, err := parseValidateArtifactsPhase(opts.phase)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	out, violations, err := agent.ValidateArtifactsPreflight(phase, agent.Role(strings.TrimSpace(opts.role)), opts.dir)
	if err != nil {
		fmt.Fprintf(stderr, "validating artifacts: %v\n", err)
		return 1
	}
	if !out.OK || len(violations) > 0 {
		reason := agent.JoinProtocolViolations(violations)
		if reason == "" {
			reason = "artifact validation failed"
		}
		fmt.Fprintln(stderr, reason)
		return 1
	}
	fmt.Fprintln(stdout, "artifacts OK")
	return 0
}

func parseValidateArtifactsPhase(value string) (feature.Phase, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	for _, phase := range []feature.Phase{
		feature.PhaseResearch,
		feature.PhasePlan,
		feature.PhaseImplement,
		feature.PhasePublish,
		feature.PhaseReview,
		feature.PhaseKnowledgeBase,
		feature.PhaseInquire,
		feature.PhaseDesign,
	} {
		if normalized == strings.ToLower(phase.DirName()) || normalized == strings.ToLower(phase.String()) {
			return phase, nil
		}
	}
	if normalized == strings.ToLower(feature.PhaseFinalReview.String()) {
		return feature.PhaseReview, nil
	}
	return 0, fmt.Errorf("unknown validate-artifacts phase: %s", value)
}

const providerReadinessTimeout = 5 * time.Second
const providerReadinessNoticeDelay = 3 * time.Second
const providerCatalogDiscoveryTimeout = 45 * time.Second

type providerReadinessIssue struct {
	provider llm.LLMProvider
	status   llm.ProviderReadiness
}

// checkRequiredProviders uses the registry to verify provider CLIs are
// available and ready. Returns (ready providers, warnings, startup notices,
// availabilityFiltered, error): errors when no provider is ready.
func checkRequiredProviders(ctx context.Context, registry *llm.Registry) ([]llm.LLMProvider, []string, []string, bool, error) {
	all := registry.All()
	var warnings []string
	var missing []llm.LLMProvider
	var detected []llm.LLMProvider
	for _, p := range all {
		if p.DetectCLI() {
			detected = append(detected, p)
			continue
		}
		missing = append(missing, p)
	}
	if len(detected) == 0 {
		return nil, nil, nil, true, fmt.Errorf("%s", agent.FormatNoCLIMessage(all))
	}

	var ready []llm.LLMProvider
	var unready []providerReadinessIssue
	for _, p := range detected {
		status := checkProviderReadiness(ctx, p)
		if status.Ready {
			ready = append(ready, p)
			continue
		}
		unready = append(unready, providerReadinessIssue{provider: p, status: status})
	}
	if len(ready) == 0 {
		return nil, nil, nil, true, fmt.Errorf("%s", formatNoReadyProviderMessage(all, unready))
	}
	startupNotices := formatProviderStartupNotices(ready, missing, unready)
	registry.RestrictToProviders(ready)
	return ready, warnings, startupNotices, len(ready) < len(all), nil
}

func checkProviderReadiness(ctx context.Context, p llm.LLMProvider) llm.ProviderReadiness {
	// Enforce MinVersion before the readiness probe so a too-old CLI is treated
	// as unavailable here — excluded from the ready set and from routing — rather
	// than left selectable with only a later warning.
	if status, ok := checkProviderVersionGate(p); !ok {
		return status
	}
	checker, ok := p.(llm.ReadinessChecker)
	if !ok {
		return llm.ProviderReadiness{Ready: true}
	}
	checkCtx, cancel := context.WithTimeout(ctx, providerReadinessTimeout)
	defer cancel()
	status := checker.CheckReadiness(checkCtx)
	if status.Detail == "" && !status.Ready {
		status.Detail = "not ready"
	}
	return status
}

// checkProviderVersionGate enforces MinVersion for providers that opt in via
// llm.VersionEnforcer. It returns ok=false with a not-ready status when the
// installed CLI is older than the provider's minimum, so the provider is
// excluded from the ready set before readiness is even probed. This preserves
// fallback to other ready providers and the existing no-ready-provider failure
// when the too-old provider is the only selected one. Providers that do not
// enforce, or whose version is acceptable or undeterminable, return ok=true and
// proceed to the normal readiness probe.
func checkProviderVersionGate(p llm.LLMProvider) (llm.ProviderReadiness, bool) {
	enforcer, ok := p.(llm.VersionEnforcer)
	if !ok || !enforcer.EnforcesMinVersion() {
		return llm.ProviderReadiness{}, true
	}
	below, version, minVer := agent.BelowMinVersion(p)
	if !below {
		return llm.ProviderReadiness{}, true
	}
	return llm.ProviderReadiness{
		Ready: false,
		Detail: fmt.Sprintf("%s CLI version %s is below the required minimum %d.%d.%d",
			p.Name(), version, minVer[0], minVer[1], minVer[2]),
		Remedy: "Upgrade with: " + p.InstallHint(),
	}, false
}

func formatReadinessProblem(status llm.ProviderReadiness) string {
	detail := strings.TrimSpace(status.Detail)
	if detail == "" {
		detail = "not ready"
	}
	remedy := strings.TrimSpace(status.Remedy)
	if remedy == "" {
		return detail
	}
	return detail + ". " + remedy + "."
}

func formatProviderStartupNotices(ready []llm.LLMProvider, missing []llm.LLMProvider, unready []providerReadinessIssue) []string {
	if len(ready) == 0 || (len(missing) == 0 && len(unready) == 0) {
		return nil
	}
	readyText := formatProviderNameList(ready)
	var notices []string
	for _, p := range missing {
		notices = append(notices, fmt.Sprintf(
			"Provider %s CLI was not found. Install with: %s. Starting with %s only.",
			p.Name(),
			p.InstallHint(),
			readyText,
		))
	}
	for _, issue := range unready {
		notices = append(notices, fmt.Sprintf(
			"Provider %s is not configured: %s Starting with %s only.",
			issue.provider.Name(),
			formatReadinessProblem(issue.status),
			readyText,
		))
	}
	return notices
}

func formatProviderNameList(providers []llm.LLMProvider) string {
	names := make([]string, 0, len(providers))
	for _, p := range providers {
		names = append(names, p.Name())
	}
	return strings.Join(names, ", ")
}

type providerCatalogDiscoveryProgress func(provider string, model llm.ModelInfo)

type providerCatalogDiscoveryJob struct {
	index      int
	provider   llm.LLMProvider
	discoverer llm.CatalogDiscoverer
	enricher   llm.CatalogEnricher
}

func discoverProviderCatalogs(ctx context.Context, providers []llm.LLMProvider, cacheRoot string, report providerCatalogDiscoveryProgress) []string {
	var jobs []providerCatalogDiscoveryJob
	for i, p := range providers {
		discoverer, ok := p.(llm.CatalogDiscoverer)
		if !ok {
			continue
		}
		enricher, ok := p.(llm.CatalogEnricher)
		if !ok {
			continue
		}
		jobs = append(jobs, providerCatalogDiscoveryJob{
			index:      i,
			provider:   p,
			discoverer: discoverer,
			enricher:   enricher,
		})
	}
	if len(jobs) == 0 {
		return nil
	}

	warningsByProvider := make([][]string, len(providers))
	var wg sync.WaitGroup
	var reportMu sync.Mutex
	reportProgress := func(provider string, model llm.ModelInfo) {
		if report == nil {
			return
		}
		reportMu.Lock()
		defer reportMu.Unlock()
		report(provider, model)
	}
	wg.Add(len(jobs))
	for _, job := range jobs {
		go func(job providerCatalogDiscoveryJob) {
			defer wg.Done()
			warningsByProvider[job.index] = discoverOneProviderCatalog(ctx, job.provider, job.discoverer, job.enricher, cacheRoot, reportProgress)
		}(job)
	}
	wg.Wait()

	var warnings []string
	for _, providerWarnings := range warningsByProvider {
		warnings = append(warnings, providerWarnings...)
	}
	return warnings
}

func discoverOneProviderCatalog(ctx context.Context, p llm.LLMProvider, discoverer llm.CatalogDiscoverer, enricher llm.CatalogEnricher, cacheRoot string, report providerCatalogDiscoveryProgress) []string {
	var warnings []string
	providerName := p.Name()
	reportModel := func(model llm.ModelInfo) {
		if report != nil {
			report(providerName, model)
		}
	}

	version := ""
	if cacheRoot != "" {
		if rawVersion, err := p.VersionInfo(); err == nil {
			// Normalize provider version output through the shared semver parser
			// before using it as a cache key. Catalog providers' VersionInfo may
			// return human-readable CLI output with a name or "v" prefix (for
			// example "claude 2.1.112" or "OpenAI Codex v0.120.0"); the parser
			// extracts the semver token and drops the surrounding text, including
			// any trailing credential-like or terminal-control content a malformed
			// version could carry. cacheableVersion is the final backstop on the
			// parsed token.
			if v, perr := clirun.ParseVersionOutput([]byte(rawVersion)); perr == nil && cacheableVersion(v) {
				version = v
			} else if strings.TrimSpace(rawVersion) != "" {
				// Non-empty output with no recognizable semver: never echo it (it
				// may carry credential-like content). Warn generically and run
				// discovery without caching this startup.
				warnings = append(warnings, fmt.Sprintf(
					"Warning: %s reported an unrecognized CLI version; running model discovery without caching",
					providerName,
				))
			}
		}
	}
	if version != "" {
		models, err := loadProviderCatalogCache(cacheRoot, providerName, version)
		if err == nil {
			enricher.SetModelCatalog(models)
			return nil
		}
		if !os.IsNotExist(err) {
			warnings = append(warnings, fmt.Sprintf(
				"Warning: ignoring cached %s model catalog; refreshing: %v",
				providerName,
				err,
			))
		}
	}

	// On a discovery error or an empty result we leave the catalog unset and warn:
	// the provider's CatalogProvider supplies the built-in fallback catalog (see
	// the CatalogDiscoverer contract), so downstream model lists and routing still
	// see a populated catalog rather than nothing.
	discoveryCtx, cancel := context.WithTimeout(ctx, providerCatalogDiscoveryTimeout)
	models, err := discoverModelCatalog(discoveryCtx, discoverer, reportModel)
	cancel()
	if err != nil {
		warnings = append(warnings, fmt.Sprintf(
			"Warning: could not discover %s model catalog; using built-in fallback: %v",
			providerName,
			err,
		))
		return warnings
	}
	if len(models) == 0 {
		warnings = append(warnings, fmt.Sprintf(
			"Warning: discovered empty %s model catalog; using built-in fallback",
			providerName,
		))
		return warnings
	}
	enricher.SetModelCatalog(models)
	if version != "" {
		if err := saveProviderCatalogCache(cacheRoot, providerName, version, models); err != nil {
			warnings = append(warnings, fmt.Sprintf(
				"Warning: could not cache %s model catalog: %v",
				providerName,
				err,
			))
		}
	}
	return warnings
}

func discoverModelCatalog(ctx context.Context, discoverer llm.CatalogDiscoverer, report llm.ModelDiscoveryReporter) ([]llm.ModelInfo, error) {
	if progressDiscoverer, ok := discoverer.(llm.CatalogProgressDiscoverer); ok {
		return progressDiscoverer.DiscoverModelCatalogWithProgress(ctx, report)
	}
	models, err := discoverer.DiscoverModelCatalog(ctx)
	if err != nil {
		return nil, err
	}
	if report != nil {
		for _, model := range models {
			report(model)
		}
	}
	return models, nil
}

func showProviderStartupNotices(w io.Writer, notices []string, delay time.Duration) {
	if len(notices) == 0 {
		return
	}
	for _, notice := range notices {
		fmt.Fprintln(w, notice)
	}
	if delay > 0 {
		time.Sleep(delay)
	}
}

func formatNoReadyProviderMessage(all []llm.LLMProvider, issues []providerReadinessIssue) string {
	var b strings.Builder
	b.WriteString("No ready AI coding assistant providers detected.\n\n")
	b.WriteString("Agentic Orchestrator requires at least one provider CLI to be installed and authenticated.\n\n")
	if len(issues) > 0 {
		b.WriteString("Installed provider CLIs that need setup:\n\n")
		for _, issue := range issues {
			fmt.Fprintf(&b, "  %-8s %s\n", issue.provider.Name(), formatReadinessProblem(issue.status))
		}
		b.WriteString("\n")
	}
	var missing []llm.LLMProvider
	for _, p := range all {
		if !p.DetectCLI() {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		b.WriteString("Missing provider CLIs:\n\n")
		for _, p := range missing {
			fmt.Fprintf(&b, "  %-8s %s\n", p.Name(), p.InstallHint())
		}
		b.WriteString("\n")
	}
	b.WriteString("Fix one provider and run 'agentico' again.")
	return b.String()
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

	// OpenCode is not in the unconditional default provider set. Beyond the
	// explicit `--providers ...,opencode` opt-in, auto-register it only when the
	// existing config already selects an `opencode:` model, so a config-driven
	// OpenCode selection resolves under normal startup. Registering it (rather
	// than the old AvailableModels==nil trick) is what keeps an unsolicited
	// OpenCode out of the default experience; once registered and ready it
	// discovers and contributes a catalog like any other provider.
	autoRegisterOpenCode := enabledProviders == nil && configSelectsOpenCode(configPath)

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
		fx.Options(providerFxModules(enabledProviders, autoRegisterOpenCode)...),
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
	detected, warnings, startupNotices, availabilityFiltered, err := checkRequiredProviders(context.Background(), registry)
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

	modelDiscovery := newModelDiscoveryProgressPrinter(os.Stderr)
	catalogWarnings := discoverProviderCatalogs(context.Background(), detected, filepath.Dir(stateDir), modelDiscovery.Report)
	modelDiscovery.Done()
	for _, w := range catalogWarnings {
		fmt.Fprintln(os.Stderr, w)
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
	} else if availabilityFiltered {
		remapUnresolvableModels(cfg, registry)
		if configIsNew && changed {
			_ = config.Save(configPath, cfg)
		}
	} else if changed {
		_ = config.Save(configPath, cfg)
	}

	showProviderStartupNotices(os.Stderr, startupNotices, providerReadinessNoticeDelay)

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

type modelDiscoveryProgressPrinter struct {
	mu      sync.Mutex
	w       io.Writer
	stop    func(doneLine bool)
	stopped bool
}

func newModelDiscoveryProgressPrinter(w io.Writer) *modelDiscoveryProgressPrinter {
	return &modelDiscoveryProgressPrinter{
		w:    w,
		stop: startSyncSpinnerControl(w, "Discovering models"),
	}
}

func (p *modelDiscoveryProgressPrinter) Report(provider string, model llm.ModelInfo) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopSpinnerLocked(false)
	fmt.Fprintf(
		p.w,
		"Discovered: %s - %s\n",
		startupProviderDisplayName(provider),
		startupModelDisplayName(model),
	)
}

func (p *modelDiscoveryProgressPrinter) Done() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopSpinnerLocked(true)
}

func (p *modelDiscoveryProgressPrinter) stopSpinnerLocked(doneLine bool) {
	if p.stopped {
		return
	}
	p.stop(doneLine)
	p.stopped = true
}

func startupProviderDisplayName(provider string) string {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return "Provider"
	}
	return strings.ToUpper(provider[:1]) + provider[1:]
}

func startupModelDisplayName(model llm.ModelInfo) string {
	if displayName := strings.TrimSpace(model.DisplayName); displayName != "" {
		return displayName
	}
	return strings.TrimSpace(model.ID)
}

// startSyncSpinner shows a "<msg>..." startup indicator on w. On a TTY the
// trailing dots animate so the user can tell the process is still doing work;
// on a non-TTY (CI, piped output) a single static line is printed instead. The
// returned stop() blocks until the animation goroutine has cleared its line, so
// subsequent writes to w don't collide with the spinner.
func startSyncSpinner(w io.Writer, msg string) func() {
	stop := startSyncSpinnerControl(w, msg)
	return func() {
		stop(true)
	}
}

func startSyncSpinnerControl(w io.Writer, msg string) func(doneLine bool) {
	f, _ := w.(*os.File)
	isTTY := f != nil && term.IsTerminal(int(f.Fd()))
	if !isTTY {
		fmt.Fprintf(w, "%s...\n", msg)
		return func(bool) {}
	}

	done := make(chan bool)
	var wg sync.WaitGroup
	wg.Go(func() {
		frames := []string{".  ", ".. ", "..."}
		i := 0
		fmt.Fprintf(w, "\r%s%s", msg, frames[i])
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case doneLine := <-done:
				clear := strings.Repeat(" ", len(msg)+len(frames[0]))
				if doneLine {
					fmt.Fprintf(w, "\r%s\r%s... done\n", clear, msg)
				} else {
					fmt.Fprintf(w, "\r%s\r", clear)
				}
				return
			case <-ticker.C:
				i = (i + 1) % len(frames)
				fmt.Fprintf(w, "\r%s%s", msg, frames[i])
			}
		}
	})
	return func(doneLine bool) {
		done <- doneLine
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
//
// When enabled is non-nil, exactly the named providers are registered (the
// explicit `--providers` opt-in). When enabled is nil, the default set is
// claude+codex. OpenCode joins the default set only when autoRegisterOpenCode is
// set — it is auto-registered so an explicit `opencode:` model selected in
// config resolves through normal startup. Registration is the gate that keeps an
// unsolicited OpenCode out of the default experience: once registered and ready,
// OpenCode discovers a live model catalog and contributes models like any other
// provider. Promoting OpenCode into provider-neutral defaults and the setup UI is
// a later roadmap concern, not this gating.
func providerFxModules(enabled []string, autoRegisterOpenCode bool) []fx.Option {
	all := map[string]fx.Option{
		"claude":   claude.Module,
		"codex":    codex.Module,
		"opencode": opencode.Module,
	}

	if enabled == nil {
		modules := []fx.Option{claude.Module, codex.Module}
		if autoRegisterOpenCode {
			modules = append(modules, opencode.Module)
		}
		return modules
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

// configSelectsOpenCode reports whether the config at path explicitly selects an
// OpenCode-routed model — a model string carrying the `opencode:` routing prefix
// in any of its model-selection fields. It is the signal used to auto-register
// the OpenCode provider in the default set so config-driven routing can resolve.
//
// A missing config reports false: a config that does not yet exist cannot
// reference OpenCode. An unreadable or unparseable config also reports false; the
// genuine parse error is surfaced later when the config fx module loads it.
func configSelectsOpenCode(path string) bool {
	if !fileExists(path) {
		return false
	}
	cfg, err := config.Load(path)
	if err != nil {
		return false
	}
	if modelConfigSelectsOpenCode(cfg.Defaults.Models) {
		return true
	}
	for _, pref := range cfg.Defaults.PipelinePreferences {
		if modelConfigSelectsOpenCode(pref.Models) {
			return true
		}
	}
	return false
}

// modelConfigSelectsOpenCode reports whether any model field in m carries the
// OpenCode routing prefix.
func modelConfigSelectsOpenCode(m config.ModelConfig) bool {
	for _, model := range []string{m.Research, m.Planning, m.Implementation, m.Review, m.Utilities, m.KBBuild} {
		if strings.HasPrefix(strings.TrimSpace(model), opencode.RoutingPrefix) {
			return true
		}
	}
	return false
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
