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
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
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
	launchModeServer
)

type launchOptions struct {
	configPath           string
	stateDir             string
	dangerouslySkipPerms bool
	enabledProviders     []string
	refreshModels        bool
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

type defaultLauncher func(configPath, stateDir string, dangerouslySkipPerms bool, enabledProviders []string, refreshModels bool) int

type serverLauncher func(configPath, stateDir string, dangerouslySkipPerms bool, enabledProviders []string, refreshModels bool) int

// updater is the injectable update seam. It mirrors defaultLauncher: production
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
	os.Exit(runArgs(os.Args[1:], os.Stdout, os.Stderr, runDefaultClientServer, runServer, runUpdate))
}

func runArgs(args []string, stdout, stderr io.Writer, launch defaultLauncher, launchServer serverLauncher, update updater) int {
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
	case launchModeServer:
		return launchServer(opts.configPath, opts.stateDir, opts.dangerouslySkipPerms, opts.enabledProviders, opts.refreshModels)
	default:
		return launch(opts.configPath, opts.stateDir, opts.dangerouslySkipPerms, opts.enabledProviders, opts.refreshModels)
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
	if len(args) > 0 && args[0] == "server" {
		opts.mode = launchModeServer
		args = args[1:]
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
			opts.refreshModels = true
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
       agentico server [flags]
       agentico update [--check|-n]
       agentico validate-artifacts --phase <phase> --role <role> --dir <iteration_dir>

Launches the Bubble Tea TUI dashboard.
Run 'agentico server' to start the foreground loopback HTTP server.
Run 'agentico update' to upgrade the binary, or 'agentico update --check'
(alias -n) to report the latest available release without installing.
Run 'agentico validate-artifacts' from agent sessions before phase_complete
to parse and validate role output artifacts without starting the TUI.

Flags:
  --config <path>                  Config file path (default: ~/.agentic-orchestrator/config.yaml)
  --state-dir <path>               State directory path (default: ~/.agentic-orchestrator/features)
  --providers <list>               Comma-separated provider list (default: all)
                                   Available: claude, codex, opencode
  --refresh-models                 Refresh provider model catalogs before opening the TUI
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

func discoverProviderCatalogs(ctx context.Context, providers []llm.LLMProvider, cacheRoot string, report providerCatalogDiscoveryProgress, refreshModels bool) []string {
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
			warningsByProvider[job.index] = discoverOneProviderCatalog(ctx, job.provider, job.discoverer, job.enricher, cacheRoot, reportProgress, refreshModels)
		}(job)
	}
	wg.Wait()

	var warnings []string
	for _, providerWarnings := range warningsByProvider {
		warnings = append(warnings, providerWarnings...)
	}
	return warnings
}

func discoverOneProviderCatalog(ctx context.Context, p llm.LLMProvider, discoverer llm.CatalogDiscoverer, enricher llm.CatalogEnricher, cacheRoot string, report providerCatalogDiscoveryProgress, refreshModels bool) []string {
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
	if version != "" && !refreshModels {
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
		if refreshModels && cacheRoot != "" && version != "" {
			if cached, cerr := loadProviderCatalogCacheFile(cacheRoot, providerName, version); cerr == nil {
				enricher.SetModelCatalog(cached.Models)
				warnings = append(warnings, fmt.Sprintf(
					"Warning: could not refresh %s model catalog; using stale cache from %s: %v",
					providerName,
					cached.DiscoveredAt.Format(time.RFC3339),
					err,
				))
				return warnings
			}
		}
		warnings = append(warnings, fmt.Sprintf(
			"Warning: could not discover %s model catalog; using built-in fallback: %v",
			providerName,
			err,
		))
		return warnings
	}
	if len(models) == 0 {
		if refreshModels && cacheRoot != "" && version != "" {
			if cached, cerr := loadProviderCatalogCacheFile(cacheRoot, providerName, version); cerr == nil {
				enricher.SetModelCatalog(cached.Models)
				warnings = append(warnings, fmt.Sprintf(
					"Warning: could not refresh %s model catalog; discovered empty catalog; using stale cache from %s",
					providerName,
					cached.DiscoveredAt.Format(time.RFC3339),
				))
				return warnings
			}
		}
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

type runtimeLockBusyError struct {
	stateDir string
	owner    instancelock.Owner
}

func (e runtimeLockBusyError) Error() string {
	return formatInstanceLockBusyMessage(e.stateDir, e.owner)
}

type runtimeBootstrap struct {
	lock            *instancelock.Lock
	owner           instancelock.Owner
	fxApp           *fx.App
	featureManager  *feature.Manager
	sessionManager  *session.Manager
	orchestrator    *orchestrator.Orchestrator
	registry        *llm.Registry
	cfg             *config.Config
	phaseRunner     *agent.PhaseRunner
	observer        *observe.Observer
	permissionCache *permission.Cache
	eventCh         chan interface{}
	runtime         serverruntime.RuntimeIdentity
	workspaceDir    string
	recoveryItems   []session.RecoveryItem
	recoveryScanOK  bool
}

func (b *runtimeBootstrap) Close(ctx context.Context) error {
	if b == nil {
		return nil
	}
	var errStop error
	if b.fxApp != nil {
		errStop = b.fxApp.Stop(ctx)
		b.fxApp = nil
	}
	var errLock error
	if b.lock != nil {
		errLock = b.lock.Close()
		b.lock = nil
	}
	return errors.Join(errStop, errLock)
}

type serverMutationTarget struct {
	mu         sync.Mutex
	orch       *orchestrator.Orchestrator
	cfg        *config.Config
	configPath string
	store      *feature.Store
	sessions   ports.SessionManager
	reviewer   ports.ReviewCommentOperator
}

func (t *serverMutationTarget) CreateFeature(req serverruntime.CreateFeatureRequest) (serverruntime.OperationResult, error) {
	cfg := t.cfg
	if cfg == nil {
		cfg = config.NewDefault()
	}
	models := cfg.Defaults.Models
	if hasAnyModelConfig(req.Models) {
		models = mergeModelConfig(models, req.Models)
	}
	pipeline := effectiveCreatePipeline(req.Pipeline, cfg)
	checkpoints := pipeline.ProjectGates(req.Checkpoints, true).Checkpoints
	f, err := t.orch.CreateFeature(req.Name, req.Description, req.Repos, models, req.ExitCriteria, req.Inquireness, req.Images, feature.CreateOptions{
		UseCurrentBranch:        req.UseCurrentBranch,
		UseCurrentBranchPerRepo: req.UseCurrentBranchPerRepo,
		Checkpoints:             checkpoints,
		Attachments:             req.Attachments,
		RiskLevel:               req.RiskLevel,
		Pipeline:                req.Pipeline,
	})
	if err != nil {
		return serverruntime.OperationResult{}, err
	}
	if err := t.persistPipelinePreferences(featureRepoNames(f), f.EffectivePipeline(), f.Models, f.Inquireness, f.Checkpoints, true); err != nil {
		return serverruntime.OperationResult{}, err
	}
	return serverruntime.OperationResult{Metadata: map[string]string{"feature_id": f.ID}}, nil
}

func (t *serverMutationTarget) StartFeature(featureID string) (serverruntime.OperationResult, error) {
	if err := t.orch.StartFeature(featureID); err != nil {
		return serverruntime.OperationResult{}, err
	}
	return serverruntime.OperationResult{Metadata: map[string]string{"feature_id": featureID}}, nil
}

func (t *serverMutationTarget) StopFeature(featureID string) (serverruntime.OperationResult, error) {
	if err := t.orch.InterruptFeature(featureID); err != nil {
		return serverruntime.OperationResult{}, err
	}
	return serverruntime.OperationResult{Metadata: map[string]string{"feature_id": featureID}}, nil
}

func (t *serverMutationTarget) RestartFeature(featureID string, req serverruntime.RestartFeatureRequest) (serverruntime.OperationResult, error) {
	outcome, err := t.orch.RestartPhase(featureID, req.MaxIterationsDelta, req.MaxPlanIterationsDelta)
	if err != nil {
		return serverruntime.OperationResult{}, err
	}
	metadata := map[string]string{
		"feature_id": featureID,
		"status":     "restarted",
	}
	if outcome.Phase.String() != "" {
		metadata["phase"] = outcome.Phase.String()
	}
	if err := t.dispatchRestartOutcome(featureID, outcome, metadata); err != nil {
		metadata["status"] = "failed"
		return serverruntime.OperationResult{Metadata: metadata}, err
	}
	return serverruntime.OperationResult{Metadata: metadata}, nil
}

func (t *serverMutationTarget) dispatchRestartOutcome(featureID string, outcome orchestrator.RestartOutcome, metadata map[string]string) error {
	if t.orch == nil {
		return errors.New("orchestrator is not available")
	}
	switch outcome.Action {
	case orchestrator.RestartNoOp:
		metadata["dispatch"] = "none"
		return nil
	case orchestrator.RestartDispatchPhase:
		metadata["dispatch"] = "phase"
		if outcome.Phase.String() != "" {
			metadata["phase"] = outcome.Phase.String()
		}
		return t.orch.StartFeature(featureID)
	case orchestrator.RestartDispatchRepoCycles:
		metadata["dispatch"] = "repo_cycles"
		metadata["repo_cycle_count"] = strconv.Itoa(len(outcome.RepoCycleRestarts))
		sessionIDs := make([]string, 0, len(outcome.RepoCycleRestarts)+1)
		for _, restart := range outcome.RepoCycleRestarts {
			sessionID, err := t.orch.StartRepoCycleImplement(featureID, restart.RepoName, restart.CycleType, restart.PlanContent)
			if sessionID != "" {
				sessionIDs = append(sessionIDs, sessionID)
			}
			if err != nil {
				return err
			}
		}
		if outcome.RefactorRestart != nil {
			metadata["refactor_count"] = "1"
			sessionID, err := t.orch.RestartRefactorCycle(featureID, outcome.RefactorRestart.RepoName, outcome.RefactorRestart.Prompt)
			if sessionID != "" {
				sessionIDs = append(sessionIDs, sessionID)
			}
			if err != nil {
				return err
			}
		} else {
			metadata["refactor_count"] = "0"
		}
		if len(sessionIDs) > 0 {
			metadata["session_ids"] = strings.Join(sessionIDs, ",")
		}
		return nil
	default:
		return fmt.Errorf("unknown restart action %d", outcome.Action)
	}
}

func (t *serverMutationTarget) ReviewDecision(featureID string, req serverruntime.ReviewDecisionRequest) (serverruntime.OperationResult, error) {
	decision := orchestrator.ReviewDecision{
		Decision:    req.Decision,
		TargetPhase: parseServerPhase(req.Phase),
		IsRewind:    req.IsRewind,
		PhasePlan:   req.PhasePlan,
		Roadmap:     req.Roadmap,
		Comment:     req.Comment,
	}
	if err := t.orch.HandleReviewDecision(featureID, decision); err != nil {
		return serverruntime.OperationResult{}, err
	}
	return serverruntime.OperationResult{Metadata: map[string]string{"feature_id": featureID, "decision": req.Decision}}, nil
}

func (t *serverMutationTarget) UpdateFeatureConfig(featureID string, req serverruntime.FeatureConfigMutationRequest) (serverruntime.OperationResult, error) {
	if err := t.orch.UpdateFeatureConfig(featureID, orchestrator.UpdateFeatureConfigInput{
		Models:      req.Models,
		Inquireness: feature.Inquireness(req.Inquireness),
		Checkpoints: req.Checkpoints,
	}); err != nil {
		return serverruntime.OperationResult{}, err
	}
	if t.store == nil {
		return serverruntime.OperationResult{}, errors.New("feature store is not available")
	}
	f, err := t.store.Load(featureID)
	if err != nil {
		return serverruntime.OperationResult{}, err
	}
	pipeline := req.Pipeline
	if pipeline == "" {
		pipeline = f.EffectivePipeline()
	}
	if err := t.persistPipelinePreferences(featureRepoNames(f), pipeline, f.Models, f.Inquireness, f.Checkpoints, f.IsPublishable()); err != nil {
		return serverruntime.OperationResult{}, err
	}
	return serverruntime.OperationResult{Metadata: map[string]string{"feature_id": featureID}}, nil
}

func (t *serverMutationTarget) NeedUserInputDecision(featureID string, req serverruntime.NeedUserInputDecisionRequest) (serverruntime.OperationResult, error) {
	if err := t.orch.HandleNeedUserInputDecision(featureID, orchestrator.NeedUserInputDecision{
		Decision:  req.Decision,
		RepoName:  req.RepoName,
		CycleType: feature.RepoCycleType(req.CycleType),
	}); err != nil {
		return serverruntime.OperationResult{}, err
	}
	return serverruntime.OperationResult{Metadata: map[string]string{"feature_id": featureID, "decision": req.Decision}}, nil
}

func (t *serverMutationTarget) DraftNeedUserInputAnswers(featureID string, req serverruntime.NeedUserInputDraftRequest) (serverruntime.OperationResult, error) {
	gatePath, err := t.needUserInputGatePath(featureID, req.RepoName)
	if err != nil {
		return serverruntime.OperationResult{}, err
	}
	rec, err := agent.ReadNeedUserInputRecord(gatePath)
	if err != nil {
		return serverruntime.OperationResult{}, fmt.Errorf("read need-user-input gate: %w", err)
	}
	if err := applyNeedUserInputDraftAnswers(&rec, req.Answers); err != nil {
		return serverruntime.OperationResult{}, err
	}
	if err := agent.WriteNeedUserInputRecord(gatePath, rec); err != nil {
		return serverruntime.OperationResult{}, fmt.Errorf("write need-user-input gate: %w", err)
	}
	return serverruntime.OperationResult{Metadata: map[string]string{"feature_id": featureID, "status": "drafted"}}, nil
}

func (t *serverMutationTarget) AnswerPermission(req serverruntime.PermissionAnswerRequest) (serverruntime.OperationResult, error) {
	decision := strings.ToLower(strings.TrimSpace(req.Decision))
	if decision != "allow" && decision != "deny" {
		return serverruntime.OperationResult{}, fmt.Errorf("unknown permission decision %q", req.Decision)
	}
	sess, pending, err := t.findPendingControlRequest(req.SessionID, req.RequestID, false)
	if err != nil {
		return serverruntime.OperationResult{}, err
	}
	reason := ""
	if decision == "deny" {
		reason = "denied by user"
	}
	if err := sess.RespondToControl(pending.RequestID, decision == "allow", reason); err != nil {
		return serverruntime.OperationResult{}, fmt.Errorf("answer permission: %w", err)
	}
	return serverruntime.OperationResult{Metadata: map[string]string{
		"session_id": sess.ID(),
		"request_id": pending.RequestID,
		"decision":   decision,
	}}, nil
}

func (t *serverMutationTarget) AnswerAskUser(req serverruntime.AskUserAnswerRequest) (serverruntime.OperationResult, error) {
	sess, pending, err := t.findPendingControlRequest(req.SessionID, req.RequestID, true)
	if err != nil {
		return serverruntime.OperationResult{}, err
	}
	if err := sess.RespondToAskUser(pending.RequestID, pending.Request.Input, req.Answers, nil); err != nil {
		return serverruntime.OperationResult{}, fmt.Errorf("answer ask-user question: %w", err)
	}
	return serverruntime.OperationResult{Metadata: map[string]string{
		"session_id": sess.ID(),
		"request_id": pending.RequestID,
		"decision":   "answered",
	}}, nil
}

func (t *serverMutationTarget) SendHelp(req serverruntime.HelpAnswerRequest) (serverruntime.OperationResult, error) {
	sess, err := t.helpSession(req)
	if err != nil {
		return serverruntime.OperationResult{}, err
	}
	if err := sess.SendUserMessage(req.Message); err != nil {
		return serverruntime.OperationResult{}, fmt.Errorf("send help message: %w", err)
	}
	return serverruntime.OperationResult{Metadata: map[string]string{
		"feature_id": sess.FeatureID(),
		"session_id": sess.ID(),
		"status":     "sent",
	}}, nil
}

func (t *serverMutationTarget) RuntimeConfig(req serverruntime.RuntimeConfigMutationRequest) (serverruntime.OperationResult, error) {
	if t.configPath == "" {
		return serverruntime.OperationResult{}, errors.New("config path is not available")
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	cfg := t.cfg
	if cfg == nil {
		cfg = config.NewDefault()
	}
	changed := mergeRuntimeDefaultsMutation(&cfg.Defaults, req.Defaults)
	if req.WorkspaceRoots != nil {
		if !sameStringSlice(cfg.WorkspaceRoots, *req.WorkspaceRoots) {
			changed = true
		}
		cfg.WorkspaceRoots = append([]string(nil), (*req.WorkspaceRoots)...)
		config.DiscoverReposFromRoots(cfg)
	}
	if req.UI != nil {
		if !sameStringSlice(cfg.UI.CollapsedSections, req.UI.CollapsedSections) {
			cfg.UI.CollapsedSections = append([]string(nil), req.UI.CollapsedSections...)
			changed = true
		}
		if req.UI.KeyboardLayout != "" && req.UI.KeyboardLayout != cfg.UI.KeyboardLayout {
			cfg.UI.KeyboardLayout = req.UI.KeyboardLayout
			changed = true
		}
	}
	if req.Notifications != nil && cfg.Notifications.MuteFeatureInput != req.Notifications.MuteFeatureInput {
		cfg.Notifications.MuteFeatureInput = req.Notifications.MuteFeatureInput
		changed = true
	}
	if err := config.Save(t.configPath, cfg); err != nil {
		return serverruntime.OperationResult{}, err
	}
	t.cfg = cfg
	status := "unchanged"
	if changed {
		status = "updated"
	}
	return serverruntime.OperationResult{Metadata: map[string]string{"kind": "runtime.config", "status": status}}, nil
}

func (t *serverMutationTarget) ToggleInputNotifications(featureID string) (serverruntime.OperationResult, error) {
	if t.store == nil {
		return serverruntime.OperationResult{}, errors.New("feature store is not available")
	}
	defaultMuted := false
	if t.cfg != nil {
		defaultMuted = t.cfg.Notifications.MuteFeatureInput
	}
	muted := false
	if err := t.store.Modify(featureID, func(f *feature.Feature) error {
		current := defaultMuted
		if f.MuteInputNotifications != nil {
			current = *f.MuteInputNotifications
		}
		next := !current
		f.MuteInputNotifications = &next
		muted = next
		return nil
	}); err != nil {
		return serverruntime.OperationResult{}, fmt.Errorf("toggling input notifications: %w", err)
	}
	status := "enabled"
	if muted {
		status = "muted"
	}
	return serverruntime.OperationResult{Metadata: map[string]string{
		"feature_id": featureID,
		"status":     status,
		"muted":      strconv.FormatBool(muted),
	}}, nil
}

func (t *serverMutationTarget) ScanRecovery(ctx context.Context) ([]ports.RecoveryItem, error) {
	return t.orch.ScanRecovery(ctx)
}

func (t *serverMutationTarget) ExecuteRecovery(ctx context.Context, items []ports.RecoveryItem, actions map[string]ports.RecoveryAction) (serverruntime.OperationResult, error) {
	if err := t.orch.ExecuteRecovery(ctx, items, actions); err != nil {
		return serverruntime.OperationResult{}, err
	}
	return serverruntime.OperationResult{Metadata: map[string]string{"status": "recovered"}}, nil
}

func (t *serverMutationTarget) PublishFeature(featureID string, req serverruntime.PublishFeatureRequest) (serverruntime.OperationResult, error) {
	metadata := actionMetadata(featureID, "publish")
	if err := t.ensureWholeFeatureRepoSelection(featureID, req.Repos, "publish"); err != nil {
		metadata["status"] = "unsupported"
		return serverruntime.OperationResult{Metadata: metadata}, err
	}
	if t.orch == nil {
		return serverruntime.OperationResult{Metadata: metadata}, errors.New("orchestrator is not available")
	}
	if err := t.orch.Publish(featureID); err != nil {
		addConflictMetadata(metadata, err)
		if metadata["status"] == "" {
			metadata["status"] = "failed"
		}
		return serverruntime.OperationResult{Metadata: metadata}, operationFailureForMetadata(err, metadata)
	}
	metadata["status"] = "published"
	return serverruntime.OperationResult{Metadata: metadata}, nil
}

func (t *serverMutationTarget) MergeFeature(featureID string) (serverruntime.OperationResult, error) {
	metadata := actionMetadata(featureID, "merge")
	if t.orch == nil {
		return serverruntime.OperationResult{Metadata: metadata}, errors.New("orchestrator is not available")
	}
	if err := t.orch.MergeFeatureLocal(featureID); err != nil {
		metadata["status"] = "failed"
		return serverruntime.OperationResult{Metadata: metadata}, err
	}
	metadata["status"] = "merged"
	return serverruntime.OperationResult{Metadata: metadata}, nil
}

func (t *serverMutationTarget) RewindFeature(featureID string, req serverruntime.RewindFeatureRequest) (serverruntime.OperationResult, error) {
	requestedTarget := strings.ToLower(strings.TrimSpace(req.TargetPhase))
	targetPhase, err := parseServerPhaseStrict(req.TargetPhase)
	metadata := actionMetadata(featureID, "rewind")
	metadata["target_phase"] = requestedTarget
	if err == nil {
		metadata["target_phase"] = targetPhase.DirName()
	}
	if req.RoadmapPhase > 0 {
		metadata["roadmap_phase"] = strconv.Itoa(req.RoadmapPhase)
	}
	if req.UpgradePipeline != "" {
		metadata["upgrade_pipeline"] = string(req.UpgradePipeline)
	}
	if err != nil {
		metadata["status"] = "failed"
		return serverruntime.OperationResult{Metadata: metadata}, err
	}
	if t.orch == nil {
		metadata["status"] = "failed"
		return serverruntime.OperationResult{Metadata: metadata}, errors.New("orchestrator is not available")
	}
	if req.UpgradePipeline != "" {
		if err := t.orch.UpgradePipeline(featureID, req.UpgradePipeline); err != nil {
			metadata["status"] = "failed"
			metadata["failed_stage"] = "upgrade_pipeline"
			return serverruntime.OperationResult{Metadata: metadata}, err
		}
	}
	t.orch.StopFeatureSessions(featureID)
	warnings, effectiveTarget, err := t.orch.RewindWithRequest(featureID, feature.RewindRequest{
		TargetPhase:  targetPhase,
		RoadmapPhase: req.RoadmapPhase,
	})
	if effectiveTarget != 0 || strings.EqualFold(req.TargetPhase, "research") {
		metadata["effective_phase"] = effectiveTarget.DirName()
	}
	metadata["warning_count"] = strconv.Itoa(len(warnings))
	if err != nil {
		metadata["status"] = "failed"
		return serverruntime.OperationResult{Metadata: metadata}, err
	}
	metadata["status"] = "rewound"
	return serverruntime.OperationResult{Metadata: metadata}, nil
}

func (t *serverMutationTarget) RetryFeature(featureID string) (serverruntime.OperationResult, error) {
	metadata := actionMetadata(featureID, "retry")
	if t.orch == nil {
		return serverruntime.OperationResult{Metadata: metadata}, errors.New("orchestrator is not available")
	}
	if err := t.orch.RetryPhase(featureID); err != nil {
		metadata["status"] = "failed"
		return serverruntime.OperationResult{Metadata: metadata}, err
	}
	metadata["status"] = "retried"
	return serverruntime.OperationResult{Metadata: metadata}, nil
}

func (t *serverMutationTarget) StartRebase(featureID string, req serverruntime.RebaseActionRequest) (serverruntime.OperationResult, error) {
	metadata := actionMetadata(featureID, "rebase")
	metadata["cycle_type"] = string(feature.CycleRebase)
	if req.Repo != "" {
		metadata["repo"] = req.Repo
	}
	if req.RebaseTarget != "" {
		metadata["rebase_target"] = req.RebaseTarget
	}
	metadata["conflict_files"] = strconv.Itoa(len(req.ConflictFiles))
	if t.orch == nil {
		return serverruntime.OperationResult{Metadata: metadata}, errors.New("orchestrator is not available")
	}
	sessionID, err := t.orch.StartRepoCycleImplement(featureID, req.Repo, feature.CycleRebase, req.RebaseTarget, req.ConflictFiles...)
	if sessionID != "" {
		metadata["session_id"] = sessionID
	}
	if err != nil {
		addConflictMetadata(metadata, err)
		if metadata["status"] == "" {
			metadata["status"] = "failed"
		}
		return serverruntime.OperationResult{Metadata: metadata}, operationFailureForMetadata(err, metadata)
	}
	metadata["status"] = "started"
	return serverruntime.OperationResult{Metadata: metadata}, nil
}

func (t *serverMutationTarget) FetchReviewComments(featureID string, req serverruntime.ReviewCommentsFetchRequest) (serverruntime.ReviewCommentsFetchResponse, error) {
	comments, err := t.fetchUnaddressedReviewComments(featureID, req.Repo)
	if err != nil {
		return serverruntime.ReviewCommentsFetchResponse{}, err
	}
	return serverruntime.ReviewCommentsFetchResponse{
		APIVersion: serverruntime.APIVersion,
		FeatureID:  featureID,
		Repo:       req.Repo,
		Mode:       "auto",
		Comments:   reviewCommentDTOs(req.Repo, comments),
	}, nil
}

func (t *serverMutationTarget) fetchUnaddressedReviewComments(featureID, repoName string) ([]ports.ReviewComment, error) {
	if t.store == nil {
		return nil, errors.New("feature store is not available")
	}
	if t.reviewer == nil {
		return nil, errors.New("review comment adapter is not available")
	}
	f, err := t.store.Load(featureID)
	if err != nil {
		return nil, err
	}
	repo, ok := findFeatureRepo(f, repoName)
	if !ok {
		return nil, fmt.Errorf("repo %s not found", repoName)
	}
	prURL := f.PRURLs()[repoName]
	if prURL == "" {
		return nil, fmt.Errorf("repo %s has no PR URL", repoName)
	}
	comments, err := t.reviewer.FetchPRComments(repo.Path, prURL)
	if err != nil {
		return nil, err
	}
	for i := range comments {
		comments[i].RepoName = repoName
	}
	addressed, _ := agent.LoadAddressedIDsForRepo(t.store.BaseDir, f, repoName)
	if len(addressed) > 0 {
		filtered := comments[:0]
		for _, comment := range comments {
			if !addressed[comment.ID] {
				filtered = append(filtered, comment)
			}
		}
		comments = filtered
	}
	return comments, nil
}

func (t *serverMutationTarget) StartReviewComments(featureID string, req serverruntime.ReviewCommentsActionRequest) (serverruntime.OperationResult, error) {
	metadata := actionMetadata(featureID, "review-comments")
	metadata["cycle_type"] = string(feature.CycleReviewComments)
	metadata["repo"] = req.Repo
	metadata["mode"] = req.Mode
	if t.orch == nil {
		return serverruntime.OperationResult{Metadata: metadata}, errors.New("orchestrator is not available")
	}
	comments := reviewCommentDTOsToPorts(req.Repo, req.Comments)
	if len(comments) == 0 {
		var err error
		comments, err = t.fetchUnaddressedReviewComments(featureID, req.Repo)
		if err != nil {
			metadata["status"] = "failed"
			return serverruntime.OperationResult{Metadata: metadata}, err
		}
	} else {
		metadata["source"] = "provided"
	}
	if len(comments) == 0 {
		metadata["status"] = "no_comments"
		return serverruntime.OperationResult{Metadata: metadata}, fmt.Errorf("review-comments: no unaddressed comments for repo %s", req.Repo)
	}
	metadata["comment_count"] = strconv.Itoa(len(comments))
	f, err := t.store.Load(featureID)
	if err != nil {
		metadata["status"] = "failed"
		return serverruntime.OperationResult{Metadata: metadata}, err
	}
	if err := agent.SaveReviewCommentsForRepo(t.store.BaseDir, f, req.Repo, agent.ReviewCommentsData{Mode: req.Mode, Comments: comments}); err != nil {
		metadata["status"] = "failed"
		return serverruntime.OperationResult{Metadata: metadata}, err
	}
	sessionID, err := t.orch.StartRepoCycleImplement(featureID, req.Repo, feature.CycleReviewComments, "")
	if sessionID != "" {
		metadata["session_id"] = sessionID
	}
	if err != nil {
		metadata["status"] = "failed"
		return serverruntime.OperationResult{Metadata: metadata}, err
	}
	metadata["status"] = "started"
	return serverruntime.OperationResult{Metadata: metadata}, nil
}

func (t *serverMutationTarget) StartTweak(featureID string, req serverruntime.TweakActionRequest) (serverruntime.OperationResult, error) {
	metadata := actionMetadata(featureID, "tweak")
	metadata["cycle_type"] = string(feature.CycleTweak)
	if t.orch == nil {
		return serverruntime.OperationResult{Metadata: metadata}, errors.New("orchestrator is not available")
	}
	sessionID, err := t.orch.StartTweak(featureID)
	if sessionID != "" {
		metadata["session_id"] = sessionID
	}
	if err != nil {
		metadata["status"] = "failed"
		return serverruntime.OperationResult{Metadata: metadata}, err
	}
	metadata["status"] = "started"
	return serverruntime.OperationResult{Metadata: metadata}, nil
}

func (t *serverMutationTarget) FinishTweak(featureID string, req serverruntime.TweakFinishRequest) (serverruntime.OperationResult, error) {
	metadata := actionMetadata(featureID, "tweak.finish")
	metadata["decision"] = strings.ToLower(strings.TrimSpace(req.Decision))
	if t.orch == nil {
		return serverruntime.OperationResult{Metadata: metadata}, errors.New("orchestrator is not available")
	}
	var err error
	switch metadata["decision"] {
	case "commit":
		hadChanges, commitErr := t.orch.CompleteTweakCommit(featureID)
		metadata["had_changes"] = strconv.FormatBool(hadChanges)
		err = commitErr
	case "skip-review":
		metadata["had_changes"] = strconv.FormatBool(req.HadChanges)
		err = t.orch.CompleteTweakFinish(featureID, req.HadChanges)
	case "restore-from-review":
		err = t.orch.RestoreTweakFromReview(featureID)
	case "fail":
		err = t.orch.FailTweakSession(featureID)
	case "final-review":
		metadata["had_changes"] = strconv.FormatBool(req.HadChanges)
		err = t.orch.StartCycleFinalReview(featureID)
		if err == nil {
			metadata["status"] = "review_started"
		}
	default:
		err = fmt.Errorf("unknown tweak finish decision %q", req.Decision)
	}
	if err != nil {
		addConflictMetadata(metadata, err)
		if metadata["status"] == "" {
			metadata["status"] = "failed"
		}
		return serverruntime.OperationResult{Metadata: metadata}, operationFailureForMetadata(err, metadata)
	}
	if metadata["status"] == "" {
		metadata["status"] = "finished"
	}
	return serverruntime.OperationResult{Metadata: metadata}, nil
}

func (t *serverMutationTarget) StartRefactor(featureID string, req serverruntime.RefactorActionRequest) (serverruntime.OperationResult, error) {
	return t.startRefactorAction(featureID, req, false)
}

func (t *serverMutationTarget) RestartRefactor(featureID string, req serverruntime.RefactorActionRequest) (serverruntime.OperationResult, error) {
	return t.startRefactorAction(featureID, req, true)
}

func (t *serverMutationTarget) MarkDone(featureID string) (serverruntime.OperationResult, error) {
	metadata := actionMetadata(featureID, "mark-done")
	if t.orch == nil {
		return serverruntime.OperationResult{Metadata: metadata}, errors.New("orchestrator is not available")
	}
	if err := t.orch.MarkDone(featureID); err != nil {
		metadata["status"] = "failed"
		return serverruntime.OperationResult{Metadata: metadata}, err
	}
	metadata["status"] = "done"
	return serverruntime.OperationResult{Metadata: metadata}, nil
}

func (t *serverMutationTarget) CleanupFeature(featureID string, req serverruntime.CleanupActionRequest) (serverruntime.OperationResult, error) {
	metadata := actionMetadata(featureID, "cleanup")
	target := strings.ToLower(strings.TrimSpace(req.Target))
	if target == "" {
		target = "worktrees"
	}
	metadata["target"] = target
	if req.Repo != "" {
		metadata["repo"] = req.Repo
	}
	if t.orch == nil {
		return serverruntime.OperationResult{Metadata: metadata}, errors.New("orchestrator is not available")
	}
	if req.Repo != "" {
		metadata["status"] = "unsupported"
		return serverruntime.OperationResult{Metadata: metadata}, errors.New("repo-scoped cleanup is not supported by the orchestrator adapter")
	}
	switch target {
	case "worktrees":
		if err := t.orch.CleanWorktree(featureID); err != nil {
			metadata["status"] = "failed"
			return serverruntime.OperationResult{Metadata: metadata}, err
		}
	case "cycles":
		if err := t.orch.ClearRepoCycles(featureID); err != nil {
			metadata["status"] = "failed"
			return serverruntime.OperationResult{Metadata: metadata}, err
		}
	case "failed-cycles", "completed-cycles":
		metadata["status"] = "unsupported"
		return serverruntime.OperationResult{Metadata: metadata}, fmt.Errorf("cleanup target %q is not supported by the orchestrator adapter", target)
	default:
		metadata["status"] = "failed"
		return serverruntime.OperationResult{Metadata: metadata}, fmt.Errorf("unknown cleanup target %q", req.Target)
	}
	metadata["status"] = "cleaned"
	return serverruntime.OperationResult{Metadata: metadata}, nil
}

func (t *serverMutationTarget) DeleteFeature(featureID string) (serverruntime.OperationResult, error) {
	metadata := actionMetadata(featureID, "delete")
	if t.orch == nil {
		return serverruntime.OperationResult{Metadata: metadata}, errors.New("orchestrator is not available")
	}
	if err := t.orch.Delete(featureID); err != nil {
		metadata["status"] = "failed"
		return serverruntime.OperationResult{Metadata: metadata}, err
	}
	metadata["status"] = "deleted"
	return serverruntime.OperationResult{Metadata: metadata}, nil
}

func actionMetadata(featureID, action string) map[string]string {
	return map[string]string{
		"feature_id": featureID,
		"action":     action,
	}
}

func addConflictMetadata(metadata map[string]string, err error) {
	if metadata == nil || err == nil {
		return
	}
	var publishConflict *orchestrator.PublishConflictError
	if errors.As(err, &publishConflict) {
		metadata["status"] = "conflict"
		metadata["conflict"] = "publish"
		metadata["repo"] = publishConflict.RepoName
		metadata["branch"] = publishConflict.Branch
		metadata["rebase_target"] = publishConflict.RebaseTarget
		metadata["conflict_files"] = "0"
		return
	}
	var rebaseConflict *orchestrator.RebaseConflictError
	if errors.As(err, &rebaseConflict) {
		metadata["status"] = "conflict"
		metadata["conflict"] = "rebase"
		metadata["repo"] = rebaseConflict.RepoName
		metadata["branch"] = rebaseConflict.Branch
		metadata["rebase_target"] = rebaseConflict.RebaseTarget
		metadata["conflict_files"] = strconv.Itoa(len(rebaseConflict.ConflictFiles))
	}
}

func operationFailureForMetadata(err error, metadata map[string]string) error {
	if err == nil {
		return nil
	}
	if metadata["status"] != "conflict" && metadata["conflict"] == "" {
		return err
	}
	return serverruntime.OperationFailureError{
		Err: err,
		Failure: &serverruntime.OperationError{
			Code:     "conflict",
			Message:  "operation conflict",
			Metadata: metadata,
		},
	}
}

func (t *serverMutationTarget) ensureWholeFeatureRepoSelection(featureID string, repos []string, action string) error {
	requested := normalizedStringSet(repos)
	if len(requested) == 0 {
		return nil
	}
	if t.store == nil {
		return fmt.Errorf("%s repo selection requires feature store", action)
	}
	f, err := t.store.Load(featureID)
	if err != nil {
		return err
	}
	featureRepos := featureRepoNames(f)
	if len(requested) != len(featureRepos) {
		return fmt.Errorf("%s for selected repos is not supported by the orchestrator adapter", action)
	}
	for _, repo := range featureRepos {
		if !requested[repo] {
			return fmt.Errorf("%s for selected repos is not supported by the orchestrator adapter", action)
		}
	}
	return nil
}

func normalizedStringSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out[value] = true
		}
	}
	return out
}

func (t *serverMutationTarget) startRefactorAction(featureID string, req serverruntime.RefactorActionRequest, restart bool) (serverruntime.OperationResult, error) {
	action := "refactor"
	if restart {
		action = "refactor.restart"
	}
	metadata := actionMetadata(featureID, action)
	metadata["cycle_type"] = string(feature.CycleRefactor)
	if req.Repo != "" {
		metadata["repo"] = req.Repo
	}
	if req.Pipeline != "" {
		metadata["pipeline"] = string(req.Pipeline)
	}
	if t.orch == nil {
		return serverruntime.OperationResult{Metadata: metadata}, errors.New("orchestrator is not available")
	}
	if req.Pipeline != "" {
		if err := t.orch.ApplyRefactorPipeline(featureID, req.Pipeline); err != nil {
			metadata["status"] = "failed"
			return serverruntime.OperationResult{Metadata: metadata}, err
		}
	}
	var (
		sessionID string
		err       error
	)
	if restart {
		sessionID, err = t.orch.RestartRefactorCycle(featureID, req.Repo, req.Prompt)
	} else {
		sessionID, err = t.orch.StartRefactorCycle(featureID, req.Repo, req.Prompt)
	}
	if sessionID != "" {
		metadata["session_id"] = sessionID
	}
	if err != nil {
		metadata["status"] = "failed"
		return serverruntime.OperationResult{Metadata: metadata}, err
	}
	if restart {
		metadata["status"] = "restarted"
	} else {
		metadata["status"] = "started"
	}
	return serverruntime.OperationResult{Metadata: metadata}, nil
}

func (t *serverMutationTarget) updateSavedReviewCommentsMode(featureID, repoName, mode string) error {
	if t.store == nil {
		return errors.New("feature store is not available")
	}
	f, err := t.store.Load(featureID)
	if err != nil {
		return err
	}
	data, err := agent.LoadReviewCommentsForRepo(t.store.BaseDir, f, repoName)
	if err != nil {
		return err
	}
	data.Mode = mode
	return agent.SaveReviewCommentsForRepo(t.store.BaseDir, f, repoName, *data)
}

func reviewCommentDTOs(repoName string, comments []ports.ReviewComment) []serverruntime.ReviewCommentDTO {
	out := make([]serverruntime.ReviewCommentDTO, 0, len(comments))
	for _, comment := range comments {
		dtoRepo := comment.RepoName
		if dtoRepo == "" {
			dtoRepo = repoName
		}
		out = append(out, serverruntime.ReviewCommentDTO{
			ID:        comment.ID,
			Type:      comment.Type,
			RepoName:  dtoRepo,
			Path:      comment.Path,
			Line:      comment.Line,
			Body:      comment.Body,
			UserLogin: comment.User.Login,
			CreatedAt: comment.CreatedAt,
			DiffHunk:  comment.DiffHunk,
			InReplyTo: comment.InReplyTo,
		})
	}
	return out
}

func reviewCommentDTOsToPorts(repoName string, comments []serverruntime.ReviewCommentDTO) []ports.ReviewComment {
	out := make([]ports.ReviewComment, 0, len(comments))
	for _, comment := range comments {
		dtoRepo := comment.RepoName
		if dtoRepo == "" {
			dtoRepo = repoName
		}
		portComment := ports.ReviewComment{
			ID:        comment.ID,
			Type:      comment.Type,
			RepoName:  dtoRepo,
			Path:      comment.Path,
			Line:      comment.Line,
			Body:      comment.Body,
			CreatedAt: comment.CreatedAt,
			DiffHunk:  comment.DiffHunk,
			InReplyTo: comment.InReplyTo,
		}
		portComment.User.Login = comment.UserLogin
		out = append(out, portComment)
	}
	return out
}

func findFeatureRepo(f *feature.Feature, repoName string) (feature.FeatureRepo, bool) {
	if f == nil {
		return feature.FeatureRepo{}, false
	}
	for _, repo := range f.Repos {
		if repo.Name == repoName {
			return repo, true
		}
	}
	return feature.FeatureRepo{}, false
}

func parseServerPhaseStrict(in string) (feature.Phase, error) {
	switch strings.ToLower(strings.TrimSpace(in)) {
	case "knowledgebase", "knowledge-base", "knowledge base", "kb":
		return feature.PhaseKnowledgeBase, nil
	case "inquire":
		return feature.PhaseInquire, nil
	case "research":
		return feature.PhaseResearch, nil
	case "design":
		return feature.PhaseDesign, nil
	case "plan":
		return feature.PhasePlan, nil
	case "implement":
		return feature.PhaseImplement, nil
	case "review":
		return feature.PhaseReview, nil
	case "final-review", "final review":
		return feature.PhaseFinalReview, nil
	case "publish":
		return feature.PhasePublish, nil
	default:
		return feature.PhaseResearch, fmt.Errorf("unknown phase %q", in)
	}
}

func (t *serverMutationTarget) findPendingControlRequest(sessionID, requestID string, wantAskUser bool) (ports.SessionView, *llm.ControlRequestMessage, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, nil, errors.New("request_id is required")
	}
	if t.sessions == nil {
		return nil, nil, errors.New("session manager is not available")
	}
	var candidates []ports.SessionView
	if strings.TrimSpace(sessionID) != "" {
		sess := t.sessions.GetSession(strings.TrimSpace(sessionID))
		if sess == nil {
			return nil, nil, fmt.Errorf("session %s not found", sessionID)
		}
		candidates = []ports.SessionView{sess}
	} else {
		candidates = t.sessions.ActiveSessions()
	}
	for _, sess := range candidates {
		if sess == nil {
			continue
		}
		for _, pending := range sess.PendingControlRequests() {
			if pending == nil || pending.RequestID != requestID {
				continue
			}
			isAskUser := pending.Request.ToolName == "AskUserQuestion"
			if isAskUser != wantAskUser {
				return nil, nil, fmt.Errorf("request %s has incompatible control type", requestID)
			}
			return sess, pending, nil
		}
	}
	return nil, nil, fmt.Errorf("pending request %s not found", requestID)
}

func (t *serverMutationTarget) helpSession(req serverruntime.HelpAnswerRequest) (ports.SessionView, error) {
	if t.sessions == nil {
		return nil, errors.New("session manager is not available")
	}
	if id := strings.TrimSpace(req.SessionID); id != "" {
		sess := t.sessions.GetSession(id)
		if sess == nil {
			return nil, fmt.Errorf("session %s not found", id)
		}
		return sess, nil
	}
	featureID := strings.TrimSpace(req.FeatureID)
	if featureID == "" {
		return nil, errors.New("session_id or feature_id is required")
	}
	var active []ports.SessionView
	for _, sess := range t.sessions.FeatureSessions(featureID) {
		if sess != nil && sess.IsActive() {
			active = append(active, sess)
		}
	}
	switch len(active) {
	case 0:
		return nil, fmt.Errorf("no active session for feature %s", featureID)
	case 1:
		return active[0], nil
	default:
		return nil, fmt.Errorf("multiple active sessions for feature %s; session_id is required", featureID)
	}
}

func (t *serverMutationTarget) needUserInputGatePath(featureID, repoName string) (string, error) {
	if t.store == nil {
		return "", errors.New("feature store is not available")
	}
	f, err := t.store.Load(featureID)
	if err != nil {
		return "", err
	}
	repoName = strings.TrimSpace(repoName)
	if repoName != "" {
		if rc := f.RepoCycles[repoName]; rc != nil && rc.Status == feature.RepoCycleNeedUserInput && rc.PendingNeedUserInputPath != "" {
			return rc.PendingNeedUserInputPath, nil
		}
		return "", fmt.Errorf("repo %s is not paused on a need-user-input gate", repoName)
	}
	if f.PendingNeedUserInputPath == "" {
		return "", fmt.Errorf("feature %s is not paused on a need-user-input gate", featureID)
	}
	return f.PendingNeedUserInputPath, nil
}

func applyNeedUserInputDraftAnswers(rec *agent.NeedUserInputRecord, answers map[string]string) error {
	if rec == nil {
		return errors.New("nil need-user-input record")
	}
	questionByKey := make(map[string]*agent.NeedUserInputQuestion)
	for i := range rec.Questions {
		q := &rec.Questions[i]
		if q.Index > 0 {
			questionByKey[strconv.Itoa(q.Index)] = q
			questionByKey[fmt.Sprintf("q%d", q.Index)] = q
		}
		if prompt := strings.TrimSpace(q.Prompt); prompt != "" {
			questionByKey[prompt] = q
		}
	}
	for key, answer := range answers {
		q := questionByKey[strings.TrimSpace(key)]
		if q == nil {
			return fmt.Errorf("answer key %q does not match a need-user-input question", key)
		}
		q.Answer = answer
	}
	return nil
}

func mergeRuntimeDefaultsMutation(dst *config.DefaultsConfig, patch config.DefaultsConfig) bool {
	if dst == nil {
		return false
	}
	changed := false
	if hasAnyModelConfig(patch.Models) {
		next := mergeModelConfig(dst.Models, patch.Models)
		if next != dst.Models {
			dst.Models = next
			changed = true
		}
	}
	if patch.ExitCriteria != "" && patch.ExitCriteria != dst.ExitCriteria {
		dst.ExitCriteria = patch.ExitCriteria
		changed = true
	}
	if patch.Inquireness != "" && patch.Inquireness != dst.Inquireness {
		dst.Inquireness = patch.Inquireness
		changed = true
	}
	if patch.Pipeline != "" && patch.Pipeline != dst.Pipeline {
		dst.Pipeline = patch.Pipeline
		changed = true
	}
	if patch.MaxIterations > 0 && patch.MaxIterations != dst.MaxIterations {
		dst.MaxIterations = patch.MaxIterations
		changed = true
	}
	if patch.MaxConsecutiveFailures > 0 && patch.MaxConsecutiveFailures != dst.MaxConsecutiveFailures {
		dst.MaxConsecutiveFailures = patch.MaxConsecutiveFailures
		changed = true
	}
	if patch.MaxConsecutiveNoProgress > 0 && patch.MaxConsecutiveNoProgress != dst.MaxConsecutiveNoProgress {
		dst.MaxConsecutiveNoProgress = patch.MaxConsecutiveNoProgress
		changed = true
	}
	if patch.MaxPhasePlanIterations > 0 && patch.MaxPhasePlanIterations != dst.MaxPhasePlanIterations {
		dst.MaxPhasePlanIterations = patch.MaxPhasePlanIterations
		changed = true
	}
	if patch.Checkpoints != (config.Checkpoints{}) && patch.Checkpoints != dst.Checkpoints {
		dst.Checkpoints = patch.Checkpoints
		changed = true
	}
	if len(patch.PipelinePreferences) > 0 {
		dst.PipelinePreferences = patch.PipelinePreferences
		changed = true
	}
	return changed
}

func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func effectiveCreatePipeline(requested feature.PipelineProfile, cfg *config.Config) feature.PipelineProfile {
	if requested.IsValid() {
		return requested
	}
	if cfg != nil && cfg.Defaults.Pipeline != "" {
		if parsed, err := feature.ParsePipelineProfile(cfg.Defaults.Pipeline); err == nil {
			return parsed
		}
	}
	return feature.PipelineMoonshot
}

func featureRepoNames(f *feature.Feature) []string {
	if f == nil {
		return nil
	}
	repos := make([]string, 0, len(f.Repos))
	for _, repo := range f.Repos {
		repos = append(repos, repo.Name)
	}
	return repos
}

func (t *serverMutationTarget) persistPipelinePreferences(repos []string, pipeline feature.PipelineProfile, models config.ModelConfig, inquireness feature.Inquireness, checkpoints feature.Checkpoints, publishable bool) error {
	if t.configPath == "" {
		return errors.New("config path is not available")
	}
	if !pipeline.IsValid() {
		return fmt.Errorf("invalid pipeline profile %q", pipeline)
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	cfg := t.cfg
	if cfg == nil {
		cfg = config.NewDefault()
	}
	if cfg.Defaults.PipelinePreferences == nil {
		cfg.Defaults.PipelinePreferences = make(map[string]config.PipelinePreference)
	}
	if cfg.Repos == nil {
		cfg.Repos = make(map[string]config.RepoConfig)
	}
	projection := pipeline.ProjectGates(checkpoints, publishable)
	profileKey := string(pipeline)
	cfg.Defaults.PipelinePreferences[profileKey] = config.PipelinePreference{
		Models:      models,
		Inquireness: string(inquireness),
	}
	configGates := feature.FeatureCheckpointsToConfig(projection.Checkpoints)
	for _, repoName := range repos {
		rc := cfg.Repos[repoName]
		if rc.PipelineGates == nil {
			rc.PipelineGates = make(map[string]config.Checkpoints)
		}
		rc.PipelineGates[profileKey] = configGates
		cfg.Repos[repoName] = rc
	}
	if err := config.Save(t.configPath, cfg); err != nil {
		return err
	}
	t.cfg = cfg
	return nil
}

func parseServerPhase(in string) feature.Phase {
	switch strings.ToLower(strings.TrimSpace(in)) {
	case "knowledgebase", "knowledge-base", "knowledge base", "kb":
		return feature.PhaseKnowledgeBase
	case "inquire":
		return feature.PhaseInquire
	case "research":
		return feature.PhaseResearch
	case "design":
		return feature.PhaseDesign
	case "plan":
		return feature.PhasePlan
	case "implement":
		return feature.PhaseImplement
	case "review":
		return feature.PhaseReview
	case "final-review", "final review":
		return feature.PhaseFinalReview
	case "publish":
		return feature.PhasePublish
	default:
		return feature.Phase(0)
	}
}

func bootstrapRuntime(ctx context.Context, configPath, stateDir string, dangerouslySkipPerms bool, enabledProviders []string, refreshModels bool, stdin io.Reader, stderr io.Writer) (*runtimeBootstrap, error) {
	runtimeDir := filepath.Dir(stateDir)
	lock, acquired, owner, err := instancelock.Acquire(runtimeDir, stateDir, configPath, tui.GetVersion())
	if err != nil {
		return nil, fmt.Errorf("acquiring instance lock: %w", err)
	}
	if !acquired {
		return nil, runtimeLockBusyError{stateDir: stateDir, owner: owner}
	}
	boot := &runtimeBootstrap{
		lock:  lock,
		owner: owner,
		runtime: serverruntime.RuntimeIdentity{
			RuntimeDir: runtimeDir,
			StateDir:   stateDir,
			Config:     configPath,
		},
	}
	success := false
	defer func() {
		if !success {
			_ = boot.Close(context.Background())
		}
	}()

	configIsNew := !fileExists(configPath)
	workspaceDir, _ := os.Getwd()
	eventCh := make(chan interface{}, 1000)

	var fm *feature.Manager
	var sm *session.Manager
	var orch *orchestrator.Orchestrator
	var registry *llm.Registry
	var cfg *config.Config
	var phaseRunner *agent.PhaseRunner
	var observer *observe.Observer
	var permissionCache *permission.Cache
	fxApp := fx.New(
		fx.Supply(
			fx.Annotate(configPath, fx.ResultTags(`name:"configPath"`)),
			fx.Annotate(stateDir, fx.ResultTags(`name:"stateDir"`)),
			fx.Annotate(dangerouslySkipPerms, fx.ResultTags(`name:"dsp"`)),
			fx.Annotate(workspaceDir, fx.ResultTags(`name:"workspaceDir"`)),
			fx.Annotate(eventCh, fx.ResultTags(`name:"eventCh"`)),
		),
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
		fx.Invoke(func(c *config.Config) {
			if c.Notifications.TerminalBundleID != "" {
				tui.SetTerminalBundleID(c.Notifications.TerminalBundleID)
			}
		}),
		fx.Populate(&fm, &sm, &orch, &registry, &cfg, &phaseRunner, &observer, &permissionCache),
		fx.NopLogger,
	)
	boot.fxApp = fxApp
	if err := fxApp.Start(ctx); err != nil {
		return nil, fmt.Errorf("initializing: %w", err)
	}

	detected, warnings, startupNotices, availabilityFiltered, err := checkRequiredProviders(ctx, registry)
	if err != nil {
		return nil, err
	}
	for _, w := range warnings {
		fmt.Fprintln(stderr, w)
	}

	toolErrors, toolWarnings := agent.CheckRequiredTools()
	for _, w := range toolWarnings {
		fmt.Fprintln(stderr, w)
	}
	if len(toolErrors) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(toolErrors, "\n"))
	}

	skillsDir := filepath.Join(runtimeDir, "skills")
	guidelinesDir := filepath.Join(runtimeDir, "guidelines")
	stop := func() {}
	if skilldef.NeedsReconcile(skillsDir) || guidelinedef.NeedsReconcile(guidelinesDir) {
		stop = startSyncSpinner(stderr, "Syncing skills and guidelines")
	}
	if err := skilldef.ReconcileSkills(skillsDir); err != nil {
		stop()
		stop = func() {}
		fmt.Fprintf(stderr, "Warning: could not reconcile skills: %v\n", err)
	} else {
		phaseRunner.SkillsDir = skillsDir
	}
	if err := guidelinedef.ReconcileGuidelines(guidelinesDir); err != nil {
		stop()
		stop = func() {}
		fmt.Fprintf(stderr, "Warning: could not reconcile guidelines: %v\n", err)
	} else {
		phaseRunner.GuidelinesDir = guidelinesDir
	}
	stop()

	for _, vr := range agent.CheckProviderVersions(detected) {
		if vr.Err != nil {
			fmt.Fprintf(stderr, "Warning: could not check %s CLI version: %v\n", vr.Provider, vr.Err)
		} else if vr.Warning != "" {
			fmt.Fprintf(stderr, "Warning: %s\n", vr.Warning)
		} else {
			fmt.Fprintf(stderr, "%s CLI version: %s\n", vr.Provider, vr.Version)
		}
	}

	modelDiscovery := newModelDiscoveryProgressPrinter(stderr)
	catalogWarnings := discoverProviderCatalogs(ctx, detected, runtimeDir, modelDiscovery.Report, refreshModels)
	modelDiscovery.Done()
	for _, w := range catalogWarnings {
		fmt.Fprintln(stderr, w)
	}

	// Apply catalog-driven defaults after discovery. For a brand-new config,
	// replace the bootstrap defaults entirely so persisted config reflects the
	// discovered catalogs rather than built-in placeholders. Defaults are
	// provider-neutral: OpenCode competes with Claude and Codex as a peer and no
	// first-run prompt biases the selection toward a single provider.
	// Apply catalog-driven defaults, remap selections the (possibly
	// provider-filtered) registry can no longer resolve, and persist when
	// appropriate. A brand-new config persists its discovered provider-neutral
	// defaults even under an explicit --providers filter or readiness filtering;
	// an existing broader config keeps those remaps runtime-only so a transient
	// launch flag never rewrites the user's selections.
	reconcileModelDefaults(cfg, registry, configPath, configIsNew, enabledProviders != nil, availabilityFiltered)

	showProviderStartupNotices(stderr, startupNotices, providerReadinessNoticeDelay)

	recoveryItems, recoveryScanOK := scanStartupRecovery(ctx, orch, stderr)
	boot.featureManager = fm
	boot.sessionManager = sm
	boot.orchestrator = orch
	boot.registry = registry
	boot.cfg = cfg
	boot.phaseRunner = phaseRunner
	boot.observer = observer
	boot.permissionCache = permissionCache
	boot.eventCh = eventCh
	boot.workspaceDir = workspaceDir
	boot.recoveryItems = recoveryItems
	boot.recoveryScanOK = recoveryScanOK
	success = true
	return boot, nil
}

func scanStartupRecovery(ctx context.Context, orch *orchestrator.Orchestrator, stderr io.Writer) ([]session.RecoveryItem, bool) {
	if orch == nil {
		return nil, true
	}
	items, err := orch.ScanRecovery(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "Warning: startup recovery scan: %v\n", err)
		return nil, false
	}
	recoveryItems := make([]session.RecoveryItem, len(items))
	for i, item := range items {
		recoveryItems[i] = item
	}
	return recoveryItems, true
}

func runtimeOperationReconciler(store *feature.Store) serverruntime.OperationReconciler {
	return func(rec serverruntime.OperationRecord) serverruntime.OperationReconciliation {
		if rec.Status == serverruntime.OperationStatusQueued {
			return interruptedOperationReconciliation("operation interrupted by server restart before it started", map[string]string{
				"feature_id": rec.Target.FeatureID,
			})
		}
		if store == nil || rec.Target.FeatureID == "" {
			return interruptedOperationReconciliation("operation interrupted by server restart", nil)
		}
		f, err := store.Load(rec.Target.FeatureID)
		if err != nil {
			return interruptedOperationReconciliation("operation interrupted by server restart", map[string]string{
				"feature_id": rec.Target.FeatureID,
			})
		}
		metadata := recoveredOperationMetadata(f)
		switch {
		case rec.Kind == "feature.stop" && f.Status == feature.StatusInterrupted:
			metadata["action"] = "stop"
			return serverruntime.OperationReconciliation{Status: serverruntime.OperationStatusSucceeded, Result: metadata}
		case f.Status == feature.StatusFailed:
			return serverruntime.OperationReconciliation{
				Status: serverruntime.OperationStatusFailed,
				Error: &serverruntime.OperationError{
					Code:     "failed",
					Metadata: metadata,
				},
			}
		case f.Status == feature.StatusInterrupted:
			return interruptedOperationReconciliation("operation interrupted by server restart", metadata)
		default:
			return serverruntime.OperationReconciliation{Status: serverruntime.OperationStatusSucceeded, Result: metadata}
		}
	}
}

func recoveredOperationMetadata(f *feature.Feature) map[string]string {
	metadata := map[string]string{
		"feature_id": f.ID,
		"status":     f.Status.String(),
	}
	if phase := f.CurrentPhase.String(); phase != "" {
		metadata["phase"] = phase
	}
	if f.ActiveRun > 0 {
		metadata["run_number"] = strconv.Itoa(f.ActiveRun)
	}
	if f.CurrentRoadmapPhase > 0 {
		metadata["roadmap_phase"] = strconv.Itoa(f.CurrentRoadmapPhase)
	}
	return metadata
}

func interruptedOperationReconciliation(message string, metadata map[string]string) serverruntime.OperationReconciliation {
	return serverruntime.OperationReconciliation{
		Status: serverruntime.OperationStatusInterrupted,
		Error: &serverruntime.OperationError{
			Code:     "interrupted",
			Message:  message,
			Metadata: metadata,
		},
	}
}

type defaultLaunchRequest struct {
	ConfigPath                 string
	StateDir                   string
	DangerouslySkipPermissions bool
	EnabledProviders           []string
	RefreshModels              bool
	Stdin                      io.Reader
	Stdout                     io.Writer
	Stderr                     io.Writer
	WaitForReadyTimeout        time.Duration
	WaitForReadyPollInterval   time.Duration
}

type defaultLaunchDeps struct {
	ResolvePolicy    func(context.Context, defaultLaunchRequest) (serverruntime.LaunchPolicy, error)
	PrepareDiscovery func(context.Context, string, serverruntime.RuntimeIdentity, serverruntime.LaunchPolicy, *http.Client) (serverruntime.DiscoveryDecision, error)
	StartServer      func(context.Context, serverStartRequest) (serverProcess, error)
	LaunchClient     func(context.Context, defaultClientLaunch) error
	HTTPClient       *http.Client
}

type serverStartRequest struct {
	ConfigPath                 string
	StateDir                   string
	DangerouslySkipPermissions bool
	EnabledProviders           []string
	RefreshModels              bool
	Stdin                      io.Reader
	Stdout                     io.Writer
	Stderr                     io.Writer
}

type serverProcess interface {
	Stop(context.Context) error
}

type serverProcessWaiter interface {
	Wait(context.Context) error
}

type serverProcessPID interface {
	PID() int
}

type defaultClientLaunch struct {
	BaseURL      string
	Runtime      serverruntime.RuntimeIdentity
	LaunchPolicy serverruntime.LaunchPolicy
	OwnedServer  bool
	Server       serverProcess
}

func runDefaultClientServer(configPath, stateDir string, dangerouslySkipPerms bool, enabledProviders []string, refreshModels bool) int {
	err := launchDefaultClientServer(context.Background(), defaultLaunchRequest{
		ConfigPath:                 configPath,
		StateDir:                   stateDir,
		DangerouslySkipPermissions: dangerouslySkipPerms,
		EnabledProviders:           enabledProviders,
		RefreshModels:              refreshModels,
		Stdin:                      os.Stdin,
		Stdout:                     os.Stdout,
		Stderr:                     os.Stderr,
	}, defaultLaunchDeps{
		LaunchClient: launchAPIClientTUI,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	return 0
}

func launchDefaultClientServer(ctx context.Context, req defaultLaunchRequest, deps defaultLaunchDeps) error {
	deps = deps.withDefaults()
	if deps.LaunchClient == nil {
		return errors.New("client TUI launcher is required")
	}
	runtimeDir := filepath.Dir(req.StateDir)
	identity := serverruntime.RuntimeIdentity{
		RuntimeDir: runtimeDir,
		StateDir:   req.StateDir,
		Config:     req.ConfigPath,
	}
	policy, err := deps.ResolvePolicy(ctx, req)
	if err != nil {
		return fmt.Errorf("resolve launch policy: %w", err)
	}
	client := deps.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: time.Second}
	}
	decision, err := deps.PrepareDiscovery(ctx, runtimeDir, identity, policy, client)
	if err != nil {
		return fmt.Errorf("validating discovery metadata: %w", err)
	}
	if decision.AlreadyRunning {
		return deps.LaunchClient(ctx, defaultClientLaunch{
			BaseURL:      decision.Record.BaseURL,
			Runtime:      identity,
			LaunchPolicy: policy,
		})
	}

	child, err := deps.StartServer(ctx, serverStartRequest{
		ConfigPath:                 req.ConfigPath,
		StateDir:                   req.StateDir,
		DangerouslySkipPermissions: req.DangerouslySkipPermissions,
		EnabledProviders:           req.EnabledProviders,
		RefreshModels:              req.RefreshModels,
		Stdin:                      req.Stdin,
		Stdout:                     req.Stdout,
		Stderr:                     req.Stderr,
	})
	if err != nil {
		if isRuntimeLockBusy(err) {
			record, readyErr := waitForDefaultLaunchServerReady(ctx, req, deps, runtimeDir, identity, policy, client)
			if readyErr != nil {
				return errors.Join(fmt.Errorf("lock contention from another live owner: %w", err), readyErr)
			}
			return deps.LaunchClient(ctx, defaultClientLaunch{
				BaseURL:      record.BaseURL,
				Runtime:      identity,
				LaunchPolicy: policy,
			})
		}
		return fmt.Errorf("server boot failed: %w", err)
	}
	record, err := waitForDefaultLaunchServerReady(ctx, req, deps, runtimeDir, identity, policy, client)
	if err != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return errors.Join(err, child.Stop(stopCtx))
	}
	owned := childOwnsReadyRecord(child, record)
	launchServer := child
	if !owned {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		stopErr := child.Stop(stopCtx)
		cancel()
		if stopErr != nil {
			return fmt.Errorf("stop losing child server: %w", stopErr)
		}
		launchServer = nil
	}
	return deps.LaunchClient(ctx, defaultClientLaunch{
		BaseURL:      record.BaseURL,
		Runtime:      identity,
		LaunchPolicy: policy,
		OwnedServer:  owned,
		Server:       launchServer,
	})
}

func isRuntimeLockBusy(err error) bool {
	var busy runtimeLockBusyError
	return errors.As(err, &busy)
}

func childOwnsReadyRecord(child serverProcess, record serverruntime.DiscoveryRecord) bool {
	reporter, ok := child.(serverProcessPID)
	if !ok {
		return true
	}
	childPID := reporter.PID()
	if childPID <= 0 || record.PID <= 0 {
		return true
	}
	return childPID == record.PID
}

func (deps defaultLaunchDeps) withDefaults() defaultLaunchDeps {
	if deps.ResolvePolicy == nil {
		deps.ResolvePolicy = resolveDefaultLaunchPolicy
	}
	if deps.PrepareDiscovery == nil {
		deps.PrepareDiscovery = serverruntime.PrepareDiscovery
	}
	if deps.StartServer == nil {
		deps.StartServer = startServerProcess
	}
	return deps
}

func launchAPIClientTUI(ctx context.Context, launch defaultClientLaunch) error {
	client, err := serverruntime.NewClient(serverruntime.ClientOptions{BaseURL: launch.BaseURL})
	if err != nil {
		return fmt.Errorf("create API client: %w", err)
	}
	opts := tui.APIAppOptions{
		Runtime:      launch.Runtime,
		LaunchPolicy: launch.LaunchPolicy,
		OwnedServer:  launch.OwnedServer,
		EventOptions: serverruntime.EventSubscriptionOptions{
			HeartbeatInterval: 30 * time.Second,
			ReconnectDelay:    250 * time.Millisecond,
		},
	}
	if launch.OwnedServer && launch.Server != nil {
		opts.WaitForOwnedServerShutdown = waitForOwnedServerShutdownOrStop(launch.Server, 2*time.Second)
	}
	tui.SetMarkdownRenderer(markdown.Render)
	app, err := tui.NewAPIAppModel(ctx, client, opts)
	if err != nil {
		return fmt.Errorf("initialize API TUI: %w", err)
	}
	defer app.Close()

	restoreStderr := redirectStderrToRuntimeLog(launch.Runtime.RuntimeDir)
	defer restoreStderr()

	p := tea.NewProgram(app, tuiProgramOptions()...)
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("run API TUI: %w", err)
	}
	return nil
}

func redirectStderrToRuntimeLog(runtimeDir string) func() {
	if runtimeDir == "" {
		return func() {}
	}
	logPath := filepath.Join(runtimeDir, defaultLogBasename)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return func() {}
	}
	origStderr := os.Stderr
	os.Stderr = logFile
	log.SetOutput(logFile)
	return func() {
		os.Stderr = origStderr
		log.SetOutput(origStderr)
		_ = logFile.Close()
	}
}

func tuiProgramOptions() []tea.ProgramOption {
	opts := []tea.ProgramOption{tea.WithFilter(tui.NewScrollRateLimiter())}
	if profile, ok := overrideColorProfile(); ok {
		opts = append(opts, tea.WithColorProfile(profile))
	}
	return opts
}

func waitForDefaultLaunchServerReady(ctx context.Context, req defaultLaunchRequest, deps defaultLaunchDeps, runtimeDir string, identity serverruntime.RuntimeIdentity, policy serverruntime.LaunchPolicy, client *http.Client) (serverruntime.DiscoveryRecord, error) {
	timeout := req.WaitForReadyTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	poll := req.WaitForReadyPollInterval
	if poll <= 0 {
		poll = 50 * time.Millisecond
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	lastReason := "no discovery record"
	for {
		decision, err := deps.PrepareDiscovery(waitCtx, runtimeDir, identity, policy, client)
		if err != nil {
			return serverruntime.DiscoveryRecord{}, fmt.Errorf("server readiness discovery failed: %w", err)
		}
		if decision.AlreadyRunning {
			return decision.Record, nil
		}
		if decision.Reason != "" {
			lastReason = decision.Reason
		}
		timer := time.NewTimer(poll)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return serverruntime.DiscoveryRecord{}, fmt.Errorf("server readiness timed out: %s: %w", lastReason, waitCtx.Err())
		case <-timer.C:
		}
	}
}

func resolveDefaultLaunchPolicy(ctx context.Context, req defaultLaunchRequest) (serverruntime.LaunchPolicy, error) {
	registry, err := newLaunchPolicyRegistry(req.EnabledProviders, req.Stderr)
	if err != nil {
		return serverruntime.LaunchPolicy{}, err
	}
	if _, warnings, _, _, err := checkRequiredProviders(ctx, registry); err != nil {
		return serverruntime.LaunchPolicy{}, err
	} else if req.Stderr != nil {
		for _, warning := range warnings {
			fmt.Fprintln(req.Stderr, warning)
		}
	}
	return runtimeLaunchPolicy(registry, req.DangerouslySkipPermissions), nil
}

func newLaunchPolicyRegistry(enabled []string, stderr io.Writer) (*llm.Registry, error) {
	registry := llm.NewRegistry()
	register := func(name string) bool {
		switch strings.TrimSpace(name) {
		case "claude":
			registry.Register(&claude.Provider{})
			return true
		case "codex":
			registry.Register(&codex.Provider{})
			return true
		case "opencode":
			registry.Register(&opencode.Provider{})
			return true
		case "":
			return false
		default:
			if stderr != nil {
				fmt.Fprintf(stderr, "Warning: unknown provider %q, skipping\n", strings.TrimSpace(name))
			}
			return false
		}
	}
	if enabled == nil {
		register("claude")
		register("codex")
		register("opencode")
		return registry, nil
	}
	valid := 0
	for _, name := range enabled {
		if register(name) {
			valid++
		}
	}
	if valid == 0 {
		return nil, errors.New("no valid providers specified in --providers flag")
	}
	return registry, nil
}

type childServerProcess struct {
	cmd  *exec.Cmd
	done chan error
}

func startServerProcess(ctx context.Context, req serverStartRequest) (serverProcess, error) {
	args := []string{"server", "--config", req.ConfigPath, "--state-dir", req.StateDir}
	if len(req.EnabledProviders) > 0 {
		args = append(args, "--providers", strings.Join(req.EnabledProviders, ","))
	}
	if req.DangerouslySkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	if req.RefreshModels {
		args = append(args, "--refresh-models")
	}
	cmd := exec.CommandContext(ctx, os.Args[0], args...)
	cmd.Stdin = req.Stdin
	cmd.Stdout = req.Stdout
	cmd.Stderr = req.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	child := &childServerProcess{cmd: cmd, done: make(chan error, 1)}
	go func() {
		child.done <- cmd.Wait()
	}()
	return child, nil
}

func (p *childServerProcess) Stop(ctx context.Context) error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	if err := p.cmd.Process.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	select {
	case err := <-p.done:
		return ignoreExpectedProcessStopError(err)
	case <-ctx.Done():
		if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		select {
		case <-p.done:
			return nil
		case <-time.After(time.Second):
			return ctx.Err()
		}
	}
}

func ignoreExpectedProcessStopError(err error) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return err
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return err
	}
	switch status.Signal() {
	case syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL:
		return nil
	default:
		return err
	}
}

func (p *childServerProcess) Wait(ctx context.Context) error {
	if p == nil {
		return nil
	}
	select {
	case err := <-p.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *childServerProcess) PID() int {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func waitForOwnedServerShutdownOrStop(process serverProcess, grace time.Duration) func(context.Context) error {
	return func(ctx context.Context) error {
		if process == nil {
			return nil
		}
		if waiter, ok := process.(serverProcessWaiter); ok {
			waitCtx := ctx
			var cancel context.CancelFunc
			if grace > 0 {
				waitCtx, cancel = context.WithTimeout(ctx, grace)
			}
			if cancel != nil {
				defer cancel()
			}
			err := waiter.Wait(waitCtx)
			if err == nil {
				return nil
			}
			if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		return process.Stop(ctx)
	}
}

func runServer(configPath, stateDir string, dangerouslySkipPerms bool, enabledProviders []string, refreshModels bool) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runtimeCtx, requestShutdown := context.WithCancel(ctx)
	defer requestShutdown()

	boot, err := bootstrapRuntime(ctx, configPath, stateDir, dangerouslySkipPerms, enabledProviders, refreshModels, os.Stdin, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	defer func() {
		if err := boot.Close(context.Background()); err != nil {
			log.Printf("close runtime: %v", err)
		}
	}()

	if boot.recoveryScanOK && len(boot.recoveryItems) == 0 {
		if err := boot.orchestrator.InterruptAllRunning(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: startup sweep: %v\n", err)
		}
	}

	policy := runtimeLaunchPolicy(boot.registry, dangerouslySkipPerms)
	discoveryClient := &http.Client{Timeout: time.Second}
	decision, err := serverruntime.PrepareDiscovery(ctx, boot.runtime.RuntimeDir, boot.runtime, policy, discoveryClient)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: validating discovery metadata: %v\n", err)
		return 1
	}
	if decision.AlreadyRunning {
		fmt.Fprintf(os.Stderr, "Error: Agentic server is already running at %s\n", decision.Record.BaseURL)
		return 1
	}

	operations, err := serverruntime.NewOperationRegistry(serverruntime.OperationRegistryOptions{
		Dir:          filepath.Join(boot.runtime.RuntimeDir, "operations"),
		DefaultLimit: 50,
		MaxLimit:     200,
		Reconcile:    runtimeOperationReconciler(boot.featureManager.Store),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: loading operation registry: %v\n", err)
		return 1
	}

	runtimeServer, err := serverruntime.Start(ctx, serverruntime.Options{
		Runtime:      boot.runtime,
		LaunchPolicy: policy,
		StartMode:    "server",
		Owner:        boot.owner,
		Features:     boot.featureManager,
		FeatureStore: boot.featureManager.Store,
		Config:       boot.cfg,
		Registry:     boot.registry,
		Sessions:     boot.sessionManager,
		Events:       boot.eventCh,
		DomainEvents: boot.orchestrator.Events(),
		Operations:   operations,
		Mutations: &serverMutationTarget{
			orch:       boot.orchestrator,
			cfg:        boot.cfg,
			configPath: boot.runtime.Config,
			store:      boot.featureManager.Store,
			sessions:   boot.sessionManager,
			reviewer:   &git.ReviewCommentAdapter{},
		},
		MutationLimits:  serverruntime.MutationLimits{FeatureQueue: 0, RuntimeQueue: 0, AggregateQueue: 100},
		RequestShutdown: requestShutdown,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: starting server: %v\n", err)
		return 1
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := runtimeServer.Close(shutdownCtx); err != nil {
			log.Printf("close server: %v", err)
		}
	}()

	now := time.Now().UTC()
	if err := serverruntime.PublishDiscovery(boot.runtime.RuntimeDir, serverruntime.DiscoveryRecord{
		SchemaVersion: 1,
		APIVersion:    serverruntime.APIVersion,
		BaseURL:       runtimeServer.BaseURL(),
		Runtime:       boot.runtime,
		LaunchPolicy:  policy,
		StartMode:     "server",
		PID:           boot.owner.PID,
		PGID:          boot.owner.PGID,
		StartedAt:     runtimeServer.StartedAt(),
		PublishedAt:   now,
		Owner:         serverruntime.OwnerDTOFromInstanceOwner(boot.owner),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Error: publishing discovery metadata: %v\n", err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "Agentic server listening at %s\n", runtimeServer.BaseURL())
	<-runtimeCtx.Done()
	shutdownFeatures(boot.orchestrator, boot.sessionManager)
	return 0
}

func runtimeLaunchPolicy(registry *llm.Registry, dangerouslySkipPerms bool) serverruntime.LaunchPolicy {
	var providers []string
	if registry != nil {
		for _, provider := range registry.DetectedProviders() {
			providers = append(providers, provider.Name())
		}
	}
	return serverruntime.NewLaunchPolicy(providers, dangerouslySkipPerms)
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

	remap(&m.Inquiry)
	remap(&m.Research)
	remap(&m.Planning)
	remap(&m.Implementation)
	remap(&m.Review)
	remap(&m.Utilities)
	remap(&m.KBBuild)
}

// shouldPersistCatalogDefaults reports whether catalog-derived model defaults
// should be written back to the config file after discovery.
//
// A brand-new config persists its discovered provider-neutral defaults even when
// an explicit --providers filter or readiness filtering narrowed the registry —
// there is no prior user config to clobber, and the persisted config must
// reflect the providers that are actually ready (bare backend IDs for a single
// ready provider, prefixed when several are ready). An existing (broader) config
// keeps provider-filtered or availability-filtered remaps runtime-only, so a
// transient launch flag or a missing CLI never rewrites the user's selections.
func shouldPersistCatalogDefaults(configIsNew, changed, providerFiltered, availabilityFiltered bool) bool {
	if providerFiltered || availabilityFiltered {
		return configIsNew && changed
	}
	return changed
}

// reconcileModelDefaults applies catalog-driven defaults to cfg after discovery,
// remaps any model selections the active registry can no longer resolve, and
// persists the result when shouldPersistCatalogDefaults allows. It returns true
// when the config was written to disk.
func reconcileModelDefaults(cfg *config.Config, registry *llm.Registry, configPath string, configIsNew, providerFiltered, availabilityFiltered bool) bool {
	changed := applyCatalogModelDefaultsToConfig(cfg, registry, configIsNew)
	if providerFiltered || availabilityFiltered {
		remapUnresolvableModels(cfg, registry)
	}
	if shouldPersistCatalogDefaults(configIsNew, changed, providerFiltered, availabilityFiltered) {
		_ = config.Save(configPath, cfg)
		return true
	}
	return false
}

// providerFxModules returns the fx modules for the requested providers.
//
// When enabled is non-nil, exactly the named providers are registered (the
// explicit `--providers` opt-in, which accepts claude, codex, and opencode in
// any order). When enabled is nil, the default set is claude+codex+opencode:
// OpenCode is a normal default provider that participates in readiness checks,
// catalog discovery, provider-neutral defaults, and the setup UI exactly like
// the others. A missing, unready, or too-old OpenCode is filtered out downstream
// by the same readiness path as any other provider.
func providerFxModules(enabled []string) []fx.Option {
	all := map[string]fx.Option{
		"claude":   claude.Module,
		"codex":    codex.Module,
		"opencode": opencode.Module,
	}

	if enabled == nil {
		return []fx.Option{claude.Module, codex.Module, opencode.Module}
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
	return m.Inquiry != "" ||
		m.Research != "" ||
		m.Planning != "" ||
		m.Implementation != "" ||
		m.Review != "" ||
		m.Utilities != "" ||
		m.KBBuild != ""
}

func modelConfigDefaultsMap(m config.ModelConfig) map[string]string {
	return map[string]string{
		"inquiry":        m.Inquiry,
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
	updated.Inquiry = canonicalizeModel(registry, models.Inquiry)
	updated.Research = canonicalizeModel(registry, models.Research)
	updated.Planning = canonicalizeModel(registry, models.Planning)
	updated.Implementation = canonicalizeModel(registry, models.Implementation)
	updated.Review = canonicalizeModel(registry, models.Review)
	updated.Utilities = canonicalizeModel(registry, models.Utilities)
	updated.KBBuild = canonicalizeModel(registry, models.KBBuild)
	return updated, updated != models
}

// applyCatalogModelDefaultsToConfig canonicalizes the config's existing model
// selections against the live registry and fills in catalog-driven defaults.
// The defaults are provider-neutral (CatalogDefaultModels ranks Claude, Codex,
// and OpenCode as peers); first-run setup applies no provider-wide preference
// override, so a brand-new config persists the same neutral defaults a returning
// user would see.
func applyCatalogModelDefaultsToConfig(cfg *config.Config, registry *llm.Registry, overwrite bool) bool {
	if cfg == nil || registry == nil {
		return false
	}

	models, changed := canonicalizeModelConfig(registry, cfg.Defaults.Models)
	cfg.Defaults.Models = models

	defaults := registry.CatalogDefaultModels()
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
