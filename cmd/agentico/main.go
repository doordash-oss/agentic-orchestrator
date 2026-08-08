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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	agentprompts "github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
	"github.com/doordash-oss/agentic-orchestrator/internal/buildinfo"
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
	"github.com/doordash-oss/agentic-orchestrator/internal/utilskill"
	"go.uber.org/fx"
	"golang.org/x/term"
)

const (
	// Default paths live under the Agentic Orchestrator runtime parent.
	defaultRuntimeParent = "~/.agentic-orchestrator"

	defaultConfigBasename = "config.yaml"
	defaultStateBasename  = "features"
)

// Wire-level result/status/action strings shared across server mutation
// handlers (and reused by their tests) to avoid duplicated literals.
const (
	resultFailed       = "failed"
	resultAnswered     = "answered"
	resultConflict     = "conflict"
	resultCleaned      = "cleaned"
	resultStarted      = "started"
	resultUpdated      = "updated"
	resultSent         = "sent"
	resultRetried      = "retried"
	resultSetupStarted = "setup_started"
	resultCreated      = "created"

	dispatchNone = "none"

	maxIterationsRetryDelta     = 10
	maxPlanIterationsRetryDelta = 2

	toolNameBash            = "Bash"
	toolNameAskUserQuestion = "AskUserQuestion"

	chatName = "chat"

	phaseNameInquire     = "inquire"
	phaseNameImplement   = "implement"
	phaseNameFinalReview = "final-review"
	phaseNameResearch    = "research"
	phaseNamePlan        = "plan"
	phaseNameReview      = "review"
	phaseNamePublish     = "publish"

	cleanupTargetWorktrees = "worktrees"

	providerNameClaude   = "claude"
	providerNameCodex    = "codex"
	providerNameOpencode = "opencode"

	modelNameSonnet = "sonnet"

	cliSubcommandServer            = "server"
	cliSubcommandValidateArtifacts = "validate-artifacts"
	cliSubcommandVerifyEvidence    = "verify-evidence"
	cliFlagDir                     = "--dir"
	cliFlagPhase                   = "--phase"
	cliFlagRole                    = "--role"
	cliFlagContract                = "--contract"

	// repoConflictKey is the Target map key used to report the repo name on
	// a publish conflict. Coincidentally shares its value with an unrelated
	// "repo" fixture in update_test.go's slug-parsing table.
	repoConflictKey = "repo"
)

func main() {
	run()
}

type launchMode int

const (
	launchModeDesktop launchMode = iota
	launchModeServer
	launchModeHelp
	launchModeVersion
	launchModeUpdate
	launchModeValidateArtifacts
	launchModeVerifyEvidence
)

type launchOptions struct {
	configPath           string
	stateDir             string
	dangerouslySkipPerms bool
	enabledProviders     []string
	refreshModels        bool
	mode                 launchMode
	validateArtifacts    validateArtifactsOptions
	verifyEvidence       verifyEvidenceOptions
	// updateCheck is set when update mode was selected with --check / -n,
	// requesting a check-only run that never attempts to install.
	updateCheck bool
}

type validateArtifactsOptions struct {
	phase string
	role  string
	dir   string
}

type verifyEvidenceOptions struct {
	contract string
	dir      string
}

type serverLauncher func(configPath, stateDir string, dangerouslySkipPerms bool, enabledProviders []string, refreshModels bool) int

// updater is the injectable update seam. Production
// wiring passes the real updater, tests pass a fake. It returns the process
// exit code the router propagates verbatim. The update path deliberately never
// acquires the instance lock, starts the fx container, or reconciles assets.
type updater func(checkOnly bool, stdout, stderr io.Writer) int

func defaultLaunchOptions() launchOptions {
	parent := pickRuntimeParent()
	return launchOptions{
		configPath: filepath.Join(parent, defaultConfigBasename),
		stateDir:   filepath.Join(parent, defaultStateBasename),
	}
}

// pickRuntimeParent returns the current runtime parent used to derive default
// paths when the user has not passed --config or --state-dir.
func pickRuntimeParent() string {
	return config.ExpandHome(defaultRuntimeParent)
}

func run() {
	os.Exit(runArgs(os.Args[1:], os.Stdout, os.Stderr, runServer, runUpdate))
}

func runArgs(args []string, stdout, stderr io.Writer, launchServer serverLauncher, update updater) int {
	return runArgsWithDesktop(args, stdout, stderr, openRegisteredDesktop, launchServer, update)
}

type desktopLauncher func() error

func runArgsWithDesktop(args []string, stdout, stderr io.Writer, launchDesktop desktopLauncher, launchServer serverLauncher, update updater) int {
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
		fmt.Fprintln(stdout, buildinfo.VersionLine())
		return 0
	case launchModeUpdate:
		// Dispatch through the updater seam ahead of the desktop app branch, early
		// returning its exit code — exactly as help/version early-return.
		// The update path never reaches the desktop app launcher below, so it takes
		// no instance lock, builds no fx container, and reconciles no assets.
		return update(opts.updateCheck, stdout, stderr)
	case launchModeValidateArtifacts:
		return runValidateArtifacts(opts.validateArtifacts, stdout, stderr)
	case launchModeVerifyEvidence:
		return runVerifyEvidence(opts.verifyEvidence, stdout, stderr)
	case launchModeServer:
		return launchServer(opts.configPath, opts.stateDir, opts.dangerouslySkipPerms, opts.enabledProviders, opts.refreshModels)
	default:
		if err := launchDesktop(); err != nil {
			fmt.Fprintf(stderr, "Could not open the Agentico desktop app: %v\n", err)
			fmt.Fprintln(stderr, "Install the signed Agentico desktop package from GitHub Releases, or run 'agentico server' for headless automation.")
			return 1
		}
		return 0
	}
}

// canonicalizeStateDir resolves stateDir to its real, symlink-free path,
// creating it first if necessary. macOS routes common runtime parents through
// symlinks (e.g. /var -> /private/var, /tmp -> /private/tmp), and every path
// Agentico later derives from stateDir — worktrees, the knowledge-base tree,
// skills/guidelines mounts — gets handed to providers as a workdir or a
// writable/read root. OpenCode in particular matches a tool call's path
// against permission globs inconsistently, sometimes against the raw cwd and
// sometimes against a symlink-resolved worktree root (upstream opencode#14473,
// opencode#20045), so an unresolved stateDir can make an otherwise-correct
// "allow" rule silently never match. Resolving once here, before any of those
// derived paths are computed, means they are all already canonical — no
// downstream string comparison can be fooled by a symlink. Falls back to the
// original (unresolved) path when the directory can't be created or resolved,
// so this never turns into a hard failure. Called from bootstrapRuntime
// (rather than at CLI-flag-dispatch time) so pure argument-parsing/dispatch
// tests never touch the real filesystem.
func canonicalizeStateDir(stateDir string) string {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return stateDir
	}
	resolved, err := filepath.EvalSymlinks(stateDir)
	if err != nil {
		return stateDir
	}
	return resolved
}

func parseLaunchArgs(args []string) (launchOptions, error) {
	opts := defaultLaunchOptions()
	serverOnlyFlag := ""
	// `update` is a standalone subcommand recognized only as the first
	// argument. Its sub-flags (--check / -n) are valid only in this context;
	// elsewhere they fall through to the launch-flag loop and reject as
	// unknown flags, and every other bare word still rejects as an unknown
	// command.
	if len(args) > 0 && args[0] == "update" {
		return parseUpdateArgs(opts, args[1:])
	}
	if len(args) > 0 && args[0] == cliSubcommandValidateArtifacts {
		opts.mode = launchModeValidateArtifacts
		return parseValidateArtifactsArgs(opts, args[1:])
	}
	if len(args) > 0 && args[0] == cliSubcommandVerifyEvidence {
		opts.mode = launchModeVerifyEvidence
		return parseVerifyEvidenceArgs(opts, args[1:])
	}
	if len(args) > 0 && args[0] == cliSubcommandServer {
		opts.mode = launchModeServer
		args = args[1:]
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if isServerOnlyLaunchFlag(arg) {
			serverOnlyFlag = arg
		}
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
	if opts.mode == launchModeDesktop && serverOnlyFlag != "" {
		return opts, fmt.Errorf("%s is available only with the headless server; run 'agentico server %s ...'", serverOnlyFlag, serverOnlyFlag)
	}
	return opts, nil
}

func isServerOnlyLaunchFlag(flag string) bool {
	switch flag {
	case "--config", "--state-dir", "--dangerously-skip-permissions", "--providers", "--refresh-models":
		return true
	default:
		return false
	}
}

func parseValidateArtifactsArgs(opts launchOptions, args []string) (launchOptions, error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case cliFlagPhase:
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--phase requires a value")
			}
			i++
			opts.validateArtifacts.phase = args[i]
		case cliFlagRole:
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--role requires a value")
			}
			i++
			opts.validateArtifacts.role = args[i]
		case cliFlagDir:
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

func parseVerifyEvidenceArgs(opts launchOptions, args []string) (launchOptions, error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case cliFlagContract:
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--contract requires a value")
			}
			i++
			opts.verifyEvidence.contract = args[i]
		case cliFlagDir:
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--dir requires a value")
			}
			i++
			opts.verifyEvidence.dir = args[i]
		case "--help", "-h":
			opts.mode = launchModeHelp
			return opts, nil
		default:
			if strings.HasPrefix(arg, "-") {
				return opts, fmt.Errorf("unknown verify-evidence flag: %s", arg)
			}
			return opts, fmt.Errorf("unknown verify-evidence argument: %s", arg)
		}
	}
	if strings.TrimSpace(opts.verifyEvidence.contract) == "" {
		return opts, fmt.Errorf("verify-evidence requires --contract")
	}
	if strings.TrimSpace(opts.verifyEvidence.dir) == "" {
		return opts, fmt.Errorf("verify-evidence requires --dir")
	}
	return opts, nil
}

// runVerifyEvidence is the in-session self-check the implementer runs before
// declaring semantic success: it reads the testing contract and confirms every required
// agent-owned capture is present, well-formed, correctly sized, and not a
// byte-identical duplicate of another row — the same file-backed checks the
// post-handoff report-integrity gate applies, surfaced early so a missing or
// duplicated capture costs seconds here instead of a whole failed iteration.
func runVerifyEvidence(opts verifyEvidenceOptions, stdout, stderr io.Writer) int {
	contract, err := agent.ReadTestingContract(opts.contract)
	if err != nil {
		fmt.Fprintf(stderr, "reading testing contract: %v\n", err)
		return 1
	}
	violations := agent.PreflightAgentEvidence(contract, opts.dir)
	if len(violations) > 0 {
		fmt.Fprintln(stderr, agent.JoinProtocolViolations(violations))
		return 1
	}
	fmt.Fprintln(stdout, "evidence OK")
	return 0
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `Agentic Orchestrator

Usage: agentico
       agentico server [flags]
       agentico update [--check|-n]
       agentico validate-artifacts --phase <phase> --role <role> --dir <iteration_dir>
       agentico verify-evidence --contract <testing-contract.yaml> --dir <iteration_dir>

Starts or focuses the installed Agentico desktop app. Use the explicit 'server'
subcommand to start the foreground loopback HTTP server for headless automation.
Run 'agentico update' to open the desktop Updates panel when Agentico is
registered, or print package-manager guidance otherwise. Run
'agentico update --check' (alias -n) for a read-only stable-version check.
Run 'agentico validate-artifacts' from agent sessions before declaring an outcome
to parse and validate role output artifacts without starting the server.
Run 'agentico verify-evidence' from implementer sessions before declaring an outcome
to confirm required agent-owned captures are present, correctly sized, and not
duplicates — catching gaps before the post-handoff integrity gate does.

Server flags (use with 'agentico server'):
  --config <path>                  Config file path (default: ~/.agentic-orchestrator/config.yaml)
  --state-dir <path>               State directory path (default: ~/.agentic-orchestrator/features)
  --providers <list>               Comma-separated provider list (default: all)
                                   Available: claude, codex, opencode
  --refresh-models                 Refresh provider model catalogs before starting the server
  --dangerously-skip-permissions   Skip all permission prompts (use with caution)
  --check, -n                      With 'update': check for a newer release without installing
Global flags:
  --help, -h                       Show this help
  --version, -v                    Show version`)
}

func runValidateArtifacts(opts validateArtifactsOptions, stdout, stderr io.Writer) int {
	phase, err := parseServerPhaseStrict(opts.phase)
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
		if fallback, ok := tryStaleCacheFallback(enricher, cacheRoot, providerName, version, refreshModels, err.Error()); ok {
			return append(warnings, fallback...)
		}
		warnings = append(warnings, fmt.Sprintf(
			"Warning: could not discover %s model catalog; using built-in fallback: %v",
			providerName,
			err,
		))
		return warnings
	}
	if len(models) == 0 {
		if fallback, ok := tryStaleCacheFallback(enricher, cacheRoot, providerName, version, refreshModels, "discovered empty catalog"); ok {
			return append(warnings, fallback...)
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

// tryStaleCacheFallback serves a previously cached catalog when a refresh
// failed (reason); ok is false if no cache fallback applies.
func tryStaleCacheFallback(enricher llm.CatalogEnricher, cacheRoot, providerName, version string, refreshModels bool, reason string) ([]string, bool) {
	if !refreshModels || cacheRoot == "" || version == "" {
		return nil, false
	}
	cached, cerr := loadProviderCatalogCacheFile(cacheRoot, providerName, version)
	if cerr != nil {
		return nil, false
	}
	enricher.SetModelCatalog(cached.Models)
	warning := fmt.Sprintf(
		"Warning: could not refresh %s model catalog; %s; using stale cache from %s",
		providerName,
		reason,
		cached.DiscoveredAt.Format(time.RFC3339),
	)
	return []string{warning}, true
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

func persistRefreshedProviderModelCatalog(cacheRoot string, provider llm.LLMProvider, models []llm.ModelInfo) error {
	if cacheRoot == "" {
		return nil
	}
	rawVersion, err := provider.VersionInfo()
	if err != nil {
		return nil
	}
	version, err := clirun.ParseVersionOutput([]byte(rawVersion))
	if err != nil || !cacheableVersion(version) {
		return nil
	}
	return saveProviderCatalogCache(cacheRoot, provider.Name(), version, models)
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
	worktrees       feature.WorktreeOps
	eventCh         chan interface{}
	runtime         serverruntime.RuntimeIdentity
	workspaceDir    string
	recoveryItems   []ports.RecoveryItem
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
	mu                    sync.Mutex
	orch                  *orchestrator.Orchestrator
	childCreator          featureRefactorChildCreator
	reviewFeedbackCreator featureReviewFeedbackChildCreator
	rebaseChildCreator    featureRebaseChildCreator
	cfg                   *config.Config
	configPath            string
	store                 *feature.Store
	sessions              ports.SessionManager
	phaseRunner           *agent.PhaseRunner
	permissionCache       *permission.Cache
	workspaceDir          string
	// dispatchAsync runs server-owned background work (durable feature
	// setup). Nil means `go fn()`; tests inject a synchronous dispatcher.
	dispatchAsync func(fn func())
}

// featureRefactorChildCreator is the narrow feature.Manager surface the
// refactor action needs to atomically create and persist a refactor child.
type featureRefactorChildCreator interface {
	CreateRefactorChild(parentID string, spec feature.RefactorChildSpec) (*feature.Feature, error)
}

type featureReviewFeedbackChildCreator interface {
	CreateReviewFeedbackChild(parentID string, spec feature.ReviewFeedbackChildSpec) (*feature.Feature, error)
}

type featureRebaseChildCreator interface {
	CreateRebaseChild(parentID string, spec feature.RebaseChildSpec) (*feature.Feature, error)
}

// gitFreshnessProvider maps cached git freshness onto the API vocabulary. The
// cache does the work that matters: deduplicated background probes, so a read
// never waits on git and concurrent reads of one worktree cost one probe.
type gitFreshnessProvider struct {
	cache *git.FreshnessCache
}

func newGitFreshnessProvider() *gitFreshnessProvider {
	return &gitFreshnessProvider{cache: git.NewFreshnessCache()}
}

func newGitFreshnessProviderWithProbe(probe func(worktreePath string) string) *gitFreshnessProvider {
	return &gitFreshnessProvider{cache: git.NewFreshnessCacheWithProbe(probe)}
}

func (p *gitFreshnessProvider) Freshness(_ *feature.Feature, repo feature.FeatureRepo) serverruntime.RepoFreshness {
	worktree := repo.WorktreePath
	if worktree == "" {
		worktree = repo.Path
	}
	switch p.cache.Freshness(worktree) {
	case "in sync":
		return serverruntime.RepoFreshnessInSync
	case git.FreshnessLocalChanges:
		return serverruntime.RepoFreshnessLocalChanges
	case "local only":
		return serverruntime.RepoFreshnessLocalOnly
	default:
		return serverruntime.RepoFreshnessUnknown
	}
}

func (t *serverMutationTarget) CreateFeature(req serverruntime.CreateFeatureRequest) (serverruntime.CreateFeatureResponse, error) {
	cfg := t.cfg
	if cfg == nil {
		cfg = config.NewDefault()
	}
	models := cfg.Defaults.Models
	if hasAnyModelConfig(req.Models) {
		models = mergeModelConfig(models, req.Models)
	}
	effort := cfg.Defaults.Effort
	effort = config.OverlayEffortConfig(effort, req.Effort)
	pipeline := effectiveCreatePipeline(req.Pipeline, cfg)
	checkpoints := pipeline.ProjectGates(req.Checkpoints, true).Checkpoints
	f, err := t.orch.CreateFeature(req.Name, req.Description, req.Repos, models, req.ExitCriteria, req.Inquireness, req.Images, feature.CreateOptions{
		UseCurrentBranch:        req.UseCurrentBranch,
		UseCurrentBranchPerRepo: req.UseCurrentBranchPerRepo,
		Checkpoints:             checkpoints,
		Effort:                  effort,
		Attachments:             req.Attachments,
		QueueSetup:              true,
		RiskLevel:               req.RiskLevel,
		Pipeline:                req.Pipeline,
	})
	if err != nil {
		return serverruntime.CreateFeatureResponse{}, err
	}
	if err := t.persistPipelinePreferences(featureRepoNames(f), f.EffectivePipeline(), f.Models, f.Effort, f.Inquireness, f.Checkpoints, true); err != nil {
		return serverruntime.CreateFeatureResponse{}, err
	}
	return serverruntime.CreateFeatureResponse{FeatureID: f.ID, Result: "created"}, nil
}

// SetupFeature dispatches server-owned durable setup for a freshly created
// feature — or a retry of a failed setup that reruns only the unfinished
// tasks — without starting orchestration. On success the feature returns to
// the startable StatusCreated state; failures are persisted on the feature's
// setup state and surfaced through setup events, so the HTTP response only
// acknowledges the dispatch.
func (t *serverMutationTarget) SetupFeature(featureID string) (serverruntime.FeatureSetupResponse, error) {
	resp := serverruntime.FeatureSetupResponse{FeatureID: featureID}
	if t.orch == nil {
		return resp, errors.New("orchestrator is not available")
	}
	if t.store == nil {
		return resp, errors.New("feature store is not available")
	}
	f, err := t.store.Load(featureID)
	if err != nil {
		return resp, err
	}
	retry := isFailedSetupFeature(f)
	if !retry && !isPendingSetupFeature(f) {
		return resp, &serverruntime.ActionConflictError{
			Message: "feature has no pending or failed setup work",
			Target:  map[string]any{"feature_id": featureID},
		}
	}
	dispatch := t.dispatchAsync
	if dispatch == nil {
		dispatch = func(fn func()) { go fn() }
	}
	dispatch(func() {
		// Errors are durable: the setup runner persists per-task and failure
		// state on the feature and emits setup events that reach the SSE
		// stream, so the API surface reports them via the read model.
		if retry {
			_ = t.orch.RetrySetupOnly(featureID)
		} else {
			_ = t.orch.RunSetupOnly(featureID)
		}
	})
	resp.Result = resultSetupStarted
	return resp, nil
}

// isPendingSetupFeature reports whether the feature has queued durable setup
// that has not completed yet (the state Create leaves it in with QueueSetup).
func isPendingSetupFeature(f *feature.Feature) bool {
	if f == nil || f.Status != feature.StatusSettingUpWorktrees {
		return false
	}
	setup := f.Run().Setup
	return setup != nil &&
		(setup.Status == feature.SetupStatusQueued || setup.Status == feature.SetupStatusRunning)
}

func (t *serverMutationTarget) StartFeature(featureID string) (serverruntime.FeatureStartResponse, error) {
	if err := t.orch.StartFeature(featureID); err != nil {
		return serverruntime.FeatureStartResponse{}, err
	}
	return serverruntime.FeatureStartResponse{FeatureID: featureID, Result: resultStarted}, nil
}

func (t *serverMutationTarget) ResumeFeature(featureID string) (serverruntime.FeatureStartResponse, error) {
	if t.orch == nil {
		return serverruntime.FeatureStartResponse{}, errors.New("orchestrator is not available")
	}
	return t.StartFeature(featureID)
}

func (t *serverMutationTarget) StopFeature(featureID string) (serverruntime.FeatureStopResponse, error) {
	if err := t.orch.WithRelationshipReadLock(func() error {
		if err := t.orch.RelationshipGuard(featureID, orchestrator.MutationStop); err != nil {
			return err
		}
		return t.orch.InterruptFeature(featureID)
	}); err != nil {
		return serverruntime.FeatureStopResponse{}, err
	}
	return serverruntime.FeatureStopResponse{FeatureID: featureID, Result: "stopped"}, nil
}

func (t *serverMutationTarget) RestartFeature(featureID string, req serverruntime.RestartFeatureRequest) (serverruntime.FeatureRestartResponse, error) {
	outcome, err := t.orch.RestartPhase(featureID, req.MaxIterationsDelta, req.MaxPlanIterationsDelta)
	if err != nil {
		return serverruntime.FeatureRestartResponse{}, err
	}
	resp := serverruntime.FeatureRestartResponse{FeatureID: featureID, Result: "restarted"}
	if outcome.Phase.String() != "" {
		resp.Phase = outcome.Phase.String()
	}
	if err := t.dispatchRestartOutcome(featureID, outcome, &resp); err != nil {
		resp.Result = resultFailed
		return resp, err
	}
	return resp, nil
}

func (t *serverMutationTarget) dispatchRestartOutcome(featureID string, outcome orchestrator.RestartOutcome, resp *serverruntime.FeatureRestartResponse) error {
	if t.orch == nil {
		return errors.New("orchestrator is not available")
	}
	switch outcome.Action {
	case orchestrator.RestartNoOp:
		resp.Dispatch = dispatchNone
		return nil
	case orchestrator.RestartDispatchPhase:
		resp.Dispatch = "phase"
		if outcome.Phase.String() != "" {
			resp.Phase = outcome.Phase.String()
		}
		return t.orch.StartFeature(featureID)
	default:
		return fmt.Errorf("unknown restart action %d", outcome.Action)
	}
}

func (t *serverMutationTarget) ReviewDecision(featureID string, req serverruntime.ReviewDecisionRequest) error {
	decision := orchestrator.ReviewDecision{
		Decision:    req.Decision,
		TargetPhase: parseServerPhase(req.Phase),
		IsRewind:    req.IsRewind,
		PhasePlan:   req.PhasePlan,
		Roadmap:     req.Roadmap,
		Comment:     req.Comment,
	}
	return t.orch.HandleReviewDecision(featureID, decision)
}

func (t *serverMutationTarget) UpdateFeatureConfig(featureID string, req serverruntime.FeatureConfigMutationRequest) (serverruntime.FeatureConfigUpdateResponse, error) {
	if t.store == nil {
		return serverruntime.FeatureConfigUpdateResponse{}, errors.New("feature store is not available")
	}
	current, err := t.store.Load(featureID)
	if err != nil {
		return serverruntime.FeatureConfigUpdateResponse{}, err
	}
	automaticReviewMode := feature.NormalizeAutomaticReviewMode(current.AutomaticReviewMode)
	if req.AutomaticReviewMode != nil {
		automaticReviewMode, err = feature.ParseAutomaticReviewMode(*req.AutomaticReviewMode)
		if err != nil {
			return serverruntime.FeatureConfigUpdateResponse{}, err
		}
	}
	// Detect parent/child relationship and route to paired config update
	// when the addressed feature is either a parent with an active child
	// or the active child itself. The submitted pipeline must match the
	// addressed record's pipeline. The detect + update window is wrapped
	// in the relationship read lock so a concurrent child creation cannot
	// interleave between detection and the write.
	if t.orch != nil {
		var configErr error
		var configResp serverruntime.FeatureConfigUpdateResponse
		configErr = t.orch.WithRelationshipReadLock(func() error {
			parentID, _, paired, dErr := t.orch.DetectPairedConfigTarget(featureID)
			if dErr != nil {
				return fmt.Errorf("detecting paired config target: %w", dErr)
			}
			if paired {
				if err := t.orch.UpdatePairedFeatureConfig(parentID, feature.PairedConfigInput{
					Models:              req.Models,
					Effort:              req.Effort,
					Inquireness:         feature.Inquireness(req.Inquireness),
					Checkpoints:         req.Checkpoints,
					InputNotifications:  feature.InputNotificationsMode(req.InputNotifications),
					AutomaticReviewMode: automaticReviewMode,
				}, feature.PipelineProfile(req.Pipeline), featureID); err != nil {
					return err
				}
				f, err := t.store.Load(featureID)
				if err != nil {
					return err
				}
				pipeline := req.Pipeline
				if pipeline == "" {
					pipeline = f.EffectivePipeline()
				}
				if err := t.persistPipelinePreferences(featureRepoNames(f), pipeline, f.Models, f.Effort, f.Inquireness, f.Checkpoints, f.IsPublishable()); err != nil {
					return err
				}
				configResp = serverruntime.FeatureConfigUpdateResponse{FeatureID: featureID, Result: resultUpdated}
				return nil
			}
			if err := t.orch.UpdateFeatureConfig(featureID, orchestrator.UpdateFeatureConfigInput{
				Models:              req.Models,
				Effort:              req.Effort,
				Inquireness:         feature.Inquireness(req.Inquireness),
				Checkpoints:         req.Checkpoints,
				InputNotifications:  feature.InputNotificationsMode(req.InputNotifications),
				AutomaticReviewMode: automaticReviewMode,
			}); err != nil {
				return err
			}
			f, err := t.store.Load(featureID)
			if err != nil {
				return err
			}
			pipeline := req.Pipeline
			if pipeline == "" {
				pipeline = f.EffectivePipeline()
			}
			if err := t.persistPipelinePreferences(featureRepoNames(f), pipeline, f.Models, f.Effort, f.Inquireness, f.Checkpoints, f.IsPublishable()); err != nil {
				return err
			}
			configResp = serverruntime.FeatureConfigUpdateResponse{FeatureID: featureID, Result: resultUpdated}
			return nil
		})
		if configErr != nil {
			return serverruntime.FeatureConfigUpdateResponse{}, configErr
		}
		if configResp.Result != "" {
			return configResp, nil
		}
	}
	return serverruntime.FeatureConfigUpdateResponse{}, errors.New("orchestrator is not available")
}

func (t *serverMutationTarget) ResumeNeedUserInput(featureID string, req serverruntime.NeedUserInputResumeRequest) (serverruntime.NeedUserInputResumeResponse, error) {
	if err := t.orch.ResumeNeedUserInput(featureID, orchestrator.NeedUserInputResume{}); err != nil {
		return serverruntime.NeedUserInputResumeResponse{}, err
	}
	return serverruntime.NeedUserInputResumeResponse{FeatureID: featureID, Result: "resumed"}, nil
}

func (t *serverMutationTarget) DraftNeedUserInputAnswers(featureID string, req serverruntime.NeedUserInputDraftRequest) (serverruntime.NeedUserInputDraftResponse, error) {
	gatePath, err := t.needUserInputGatePath(featureID)
	if err != nil {
		return serverruntime.NeedUserInputDraftResponse{}, err
	}
	rec, err := agent.ReadNeedUserInputRecord(gatePath)
	if err != nil {
		return serverruntime.NeedUserInputDraftResponse{}, fmt.Errorf("read need-user-input gate: %w", err)
	}
	if err := applyNeedUserInputDraftAnswers(&rec, req.Answers); err != nil {
		return serverruntime.NeedUserInputDraftResponse{}, err
	}
	if err := agent.WriteNeedUserInputRecord(gatePath, rec); err != nil {
		return serverruntime.NeedUserInputDraftResponse{}, fmt.Errorf("write need-user-input gate: %w", err)
	}
	return serverruntime.NeedUserInputDraftResponse{FeatureID: featureID, Result: "drafted"}, nil
}

func (t *serverMutationTarget) AnswerPermission(req serverruntime.PermissionAnswerRequest) (serverruntime.PermissionAnswerResponse, error) {
	sess, pending, err := t.findPendingControlRequest(req.SessionID, req.RequestID, false)
	if err != nil {
		return serverruntime.PermissionAnswerResponse{}, err
	}
	rememberScope := ""
	if req.RememberScope != nil {
		rememberScope = *req.RememberScope
	}
	result, err := t.permissionAnswerService().Answer(permission.AnswerRequest{
		RequestID:        pending.RequestID,
		SessionID:        sess.ID(),
		FeatureID:        sess.FeatureID(),
		ToolName:         pending.Request.ToolName,
		ToolInput:        string(pending.Request.Input),
		Decision:         req.Decision,
		RememberPattern:  req.RememberPattern,
		RememberScope:    rememberScope,
		RememberScopeSet: req.RememberScope != nil,
	}, func(requestID string, allow bool, reason string) error {
		return sess.RespondToControl(requestID, allow, reason)
	})
	if err != nil {
		return serverruntime.PermissionAnswerResponse{}, err
	}
	return serverruntime.PermissionAnswerResponse{
		SessionID:      sess.ID(),
		RequestID:      pending.RequestID,
		Decision:       result.Decision,
		Result:         resultAnswered,
		AlreadyExisted: result.AlreadyExisted,
		AuditWarning:   result.AuditWarning,
	}, nil
}

func (t *serverMutationTarget) permissionAnswerService() *permission.AnswerService {
	var audit *permission.AuditSink
	if t != nil && t.permissionCache != nil && t.permissionCache.StoreRef() != nil {
		audit = permission.NewAuditSink(t.permissionCache.StoreRef().BaseDir)
	}
	return permission.NewAnswerService(t.permissionCache, audit)
}

func (t *serverMutationTarget) AnswerAskUser(req serverruntime.AskUserAnswerRequest) (serverruntime.AskUserAnswerResponse, error) {
	sess, pending, err := t.findPendingControlRequest(req.SessionID, req.RequestID, true)
	if err != nil {
		return serverruntime.AskUserAnswerResponse{}, err
	}
	answers := normalizeAskUserAnswerKeys(pending.Request.Input, req.Answers)
	if err := sess.RespondToAskUser(pending.RequestID, pending.Request.Input, answers, nil); err != nil {
		return serverruntime.AskUserAnswerResponse{}, fmt.Errorf("answer ask-user question: %w", err)
	}
	return serverruntime.AskUserAnswerResponse{SessionID: sess.ID(), RequestID: pending.RequestID, Result: resultAnswered}, nil
}

func normalizeAskUserAnswerKeys(input json.RawMessage, answers map[string]string) map[string]string {
	if len(input) == 0 || len(answers) == 0 {
		return answers
	}
	var envelope struct {
		Questions []struct {
			Question string `json:"question"`
			Header   string `json:"header"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(input, &envelope); err != nil || len(envelope.Questions) == 0 {
		return answers
	}
	keys := make([]string, 0, len(envelope.Questions))
	for _, q := range envelope.Questions {
		key := q.Question
		if strings.TrimSpace(key) == "" {
			key = q.Header
		}
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return answers
	}

	normalized := make(map[string]string, len(answers))
	remaining := make(map[string]string, len(answers))
	for key, answer := range answers {
		remaining[key] = answer
	}
	for _, originalKey := range keys {
		if answer, ok := remaining[originalKey]; ok {
			normalized[originalKey] = answer
			delete(remaining, originalKey)
			continue
		}
		var matchedKey string
		for submittedKey := range remaining {
			if askUserSubmittedKeyMatchesOriginal(submittedKey, originalKey) {
				if matchedKey != "" {
					matchedKey = ""
					break
				}
				matchedKey = submittedKey
			}
		}
		if matchedKey != "" {
			normalized[originalKey] = remaining[matchedKey]
			delete(remaining, matchedKey)
		}
	}
	if len(keys) == 1 && len(normalized) == 0 && len(answers) == 1 {
		for _, answer := range answers {
			return map[string]string{keys[0]: answer}
		}
	}
	for key, answer := range remaining {
		normalized[key] = answer
	}
	return normalized
}

func askUserSubmittedKeyMatchesOriginal(submittedKey, originalKey string) bool {
	submittedKey = strings.TrimSpace(submittedKey)
	originalKey = strings.TrimSpace(originalKey)
	if submittedKey == "" || originalKey == "" {
		return false
	}
	if submittedKey == originalKey {
		return true
	}
	if strings.HasSuffix(submittedKey, "...") {
		prefix := strings.TrimSuffix(submittedKey, "...")
		return prefix != "" && strings.HasPrefix(originalKey, prefix)
	}
	return false
}

func (t *serverMutationTarget) SendHelp(req serverruntime.HelpAnswerRequest) (serverruntime.HelpSendResponse, error) {
	sess, err := t.helpSession(req)
	if err != nil {
		if resp, ok, queueErr := t.sendQueuedFeatureHelp(req); ok || queueErr != nil {
			return resp, queueErr
		}
		return serverruntime.HelpSendResponse{}, err
	}
	if err := sess.SendUserMessage(req.Message); err != nil {
		return serverruntime.HelpSendResponse{}, fmt.Errorf("send help message: %w", err)
	}
	return serverruntime.HelpSendResponse{FeatureID: sess.FeatureID(), SessionID: sess.ID(), Result: resultSent}, nil
}

const serverChatSessionID = serverruntime.ChatSessionID

func (t *serverMutationTarget) StartChat(req serverruntime.ChatStartRequest) (serverruntime.ChatStartResponse, error) {
	message := strings.TrimSpace(req.Message)
	if message == "" {
		return serverruntime.ChatStartResponse{}, errors.New("message is required")
	}
	if t.sessions == nil {
		return serverruntime.ChatStartResponse{}, errors.New("session manager is not available")
	}
	deliveryMessage := chatMessageWithImages(message, req.Images)
	if sess := t.sessions.GetSession(serverChatSessionID); sess != nil && sess.IsActive() {
		if err := sess.SendUserMessage(deliveryMessage); err != nil {
			return serverruntime.ChatStartResponse{}, fmt.Errorf("send chat message: %w", err)
		}
		return serverruntime.ChatStartResponse{SessionID: sess.ID(), Result: resultSent}, nil
	}
	if t.phaseRunner == nil {
		return serverruntime.ChatStartResponse{}, errors.New("phase runner is not available")
	}

	chatSkillPath := serverChatSkillPath(t.phaseRunner.SkillsDir)
	prompt := deliveryMessage
	if instruction := serverChatSkillInstruction(t.phaseRunner.SkillsDir); instruction != "" {
		prompt = instruction + "\n\n" + prompt
	}
	model := modelNameSonnet
	if t.cfg != nil && t.cfg.Defaults.Models.Utilities != "" {
		model = t.cfg.Defaults.Models.Utilities
	}
	model = t.phaseRunner.ModelForRole(model, llm.PhaseChat)
	workDir := t.workspaceDir
	if workDir == "" {
		workDir = t.phaseRunner.StateDir
	}
	chatDir := filepath.Join(t.phaseRunner.StateDir, chatName)
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		return serverruntime.ChatStartResponse{}, fmt.Errorf("prepare chat state: %w", err)
	}
	cmd, env, sessOpts, err := t.phaseRunner.BuildSession(agent.BuildSessionOpts{
		Model:           model,
		Prompt:          prompt,
		SystemPrompt:    t.buildChatSystemPrompt(chatSkillPath),
		DisallowedTools: []string{"Task"},
		WorkDir:         workDir,
		PIDDir:          chatDir,
		PermHandler:     &permission.AMAHandler{},
		Phase:           utilskill.PhaseAll,
		TurnMode:        ports.TurnModeInteractive,
		EffortLevel:     llm.EffortLow,
		Interactive:     true,
	})
	if err != nil {
		return serverruntime.ChatStartResponse{}, fmt.Errorf("build chat session: %w", err)
	}
	if sessOpts == nil {
		sessOpts = &ports.SessionOpts{}
	}
	sessOpts.InitialPrompt = deliveryMessage
	sessOpts.Kind = ports.KindChat
	sessOpts.TurnMode = ports.TurnModeInteractive
	sessOpts.Label = chatName
	sessOpts.LogPath = filepath.Join(chatDir, "output.txt")
	sessOpts.StderrPath = filepath.Join(chatDir, "stderr.log")
	sess, err := t.sessions.StartSession(serverChatSessionID, serverChatSessionID, feature.PhaseResearch, cmd, workDir, env, sessOpts)
	if err != nil {
		return serverruntime.ChatStartResponse{}, fmt.Errorf("start chat session: %w", err)
	}
	return serverruntime.ChatStartResponse{SessionID: sess.ID(), Result: resultStarted}, nil
}

func chatMessageWithImages(message string, images []string) string {
	if len(images) == 0 {
		return message
	}
	var prompt strings.Builder
	prompt.WriteString(message)
	prompt.WriteString("\n\nAttached images (inspect these local files):")
	for _, image := range images {
		prompt.WriteString("\n- ")
		prompt.WriteString(strconv.Quote(image))
	}
	return prompt.String()
}

func (t *serverMutationTarget) EndChat() (serverruntime.ChatEndResponse, error) {
	if t.sessions == nil {
		return serverruntime.ChatEndResponse{}, errors.New("session manager is not available")
	}
	sess := t.sessions.GetSession(serverChatSessionID)
	if sess == nil || !sess.IsActive() {
		return serverruntime.ChatEndResponse{SessionID: serverChatSessionID, Result: "not_active"}, nil
	}
	if err := t.sessions.StopSession(serverChatSessionID); err != nil {
		return serverruntime.ChatEndResponse{}, fmt.Errorf("end chat session: %w", err)
	}
	return serverruntime.ChatEndResponse{SessionID: serverChatSessionID, Result: "ended"}, nil
}

func serverChatSkillInstruction(skillsDir string) string {
	skillPath := serverChatSkillPath(skillsDir)
	if skillPath == "" {
		return ""
	}
	return fmt.Sprintf("Before starting your task, read the methodology instructions at: %s\n\nRead the file completely, then follow its instructions as you work on the task below.", skillPath)
}

func serverChatSkillPath(skillsDir string) string {
	if strings.TrimSpace(skillsDir) == "" {
		return ""
	}
	return filepath.Join(skillsDir, chatName, "SKILL.md")
}

func (t *serverMutationTarget) buildChatSystemPrompt(skillPath string) string {
	runtimeRoot := ""
	stateDir := ""
	if t.phaseRunner != nil {
		stateDir = t.phaseRunner.StateDir
		if stateDir != "" {
			runtimeRoot = filepath.Dir(stateDir)
		}
	}
	return agentprompts.ChatSystemPrompt(agentprompts.ChatSystemInput{
		SkillPath:       skillPath,
		RuntimeRoot:     runtimeRoot,
		StateDir:        stateDir,
		ConfigPath:      t.configPath,
		WorkspaceDir:    t.workspaceDir,
		CurrentFeatures: strings.TrimSpace(t.buildChatContext()),
	})
}

func (t *serverMutationTarget) buildChatContext() string {
	if t.store == nil {
		return ""
	}
	features, err := t.store.List()
	if err != nil || len(features) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Current Features\n\n")
	for _, f := range features {
		if f == nil {
			continue
		}
		fmt.Fprintf(&b, "- **%s** (ID: %s): %s - Status: %s\n", f.Name, f.ID, f.Description, f.Status)
		if len(f.Repos) > 0 {
			fmt.Fprintf(&b, "  Repo: %s", f.Repos[0].Path)
			if f.Repos[0].WorktreePath != "" {
				fmt.Fprintf(&b, ", Worktree: %s", f.Repos[0].WorktreePath)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

func (t *serverMutationTarget) RuntimeConfig(req serverruntime.RuntimeConfigMutationRequest) (serverruntime.RuntimeConfigUpdateResponse, error) {
	if t.configPath == "" {
		return serverruntime.RuntimeConfigUpdateResponse{}, errors.New("config path is not available")
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	cfg := t.cfg
	if cfg == nil {
		cfg = config.NewDefault()
	}
	changed := mergeRuntimeDefaultsMutation(&cfg.Defaults, req.Defaults)
	if req.WorkspaceRoots != nil {
		if !slices.Equal(cfg.WorkspaceRoots, *req.WorkspaceRoots) {
			changed = true
		}
		cfg.WorkspaceRoots = append([]string(nil), (*req.WorkspaceRoots)...)
		config.DiscoverReposFromRoots(cfg)
	}
	if req.Notifications != nil && cfg.Notifications.MuteFeatureInput != req.Notifications.MuteFeatureInput {
		cfg.Notifications.MuteFeatureInput = req.Notifications.MuteFeatureInput
		changed = true
	}
	if err := config.Save(t.configPath, cfg); err != nil {
		return serverruntime.RuntimeConfigUpdateResponse{}, err
	}
	t.cfg = cfg
	status := "unchanged"
	if changed {
		status = resultUpdated
	}
	return serverruntime.RuntimeConfigUpdateResponse{Result: status}, nil
}

func (t *serverMutationTarget) ScanRecovery(ctx context.Context) ([]ports.RecoveryItem, error) {
	return t.orch.ScanRecovery(ctx)
}

func (t *serverMutationTarget) ExecuteRecovery(ctx context.Context, items []ports.RecoveryItem, actions map[string]ports.RecoveryAction) (serverruntime.RecoveryActionResponse, error) {
	if err := t.orch.ExecuteRecovery(ctx, items, actions); err != nil {
		return serverruntime.RecoveryActionResponse{}, err
	}
	return serverruntime.RecoveryActionResponse{Result: "recovered"}, nil
}

func (t *serverMutationTarget) PublishFeature(featureID string, req serverruntime.PublishFeatureRequest) (serverruntime.PublishFeatureResponse, error) {
	if t.orch == nil {
		return serverruntime.PublishFeatureResponse{FeatureID: featureID}, errors.New("orchestrator is not available")
	}
	if err := t.rejectStaleCompletionPreflight(featureID, req.SourceRevision); err != nil {
		return serverruntime.PublishFeatureResponse{FeatureID: featureID, Result: resultFailed}, err
	}
	if err := t.orch.PublishWithOptions(featureID, orchestrator.PublishOptions{
		Repos: req.Repos,
		Title: req.Title,
		Body:  req.Body,
	}); err != nil {
		if conflict := actionConflictError(err); conflict != nil {
			return serverruntime.PublishFeatureResponse{FeatureID: featureID, Result: resultConflict}, conflict
		}
		return serverruntime.PublishFeatureResponse{FeatureID: featureID, Result: resultFailed}, err
	}
	return serverruntime.PublishFeatureResponse{FeatureID: featureID, Result: "published"}, nil
}

func (t *serverMutationTarget) GeneratePublishDescription(featureID string, req serverruntime.PublishDescriptionRequest) (serverruntime.PublishDescriptionResponse, error) {
	if t.orch == nil {
		return serverruntime.PublishDescriptionResponse{FeatureID: featureID}, errors.New("orchestrator is not available")
	}
	title, body, err := t.orch.GeneratePublishDescription(featureID, orchestrator.PublishDescriptionOptions{
		Repos: req.Repos,
	})
	if err != nil {
		return serverruntime.PublishDescriptionResponse{FeatureID: featureID, Title: title, Body: body, Result: "generated"}, err
	}
	return serverruntime.PublishDescriptionResponse{FeatureID: featureID, Title: title, Body: body, Result: "generated"}, nil
}

func (t *serverMutationTarget) MergeFeature(featureID string, req serverruntime.GuardedFeatureActionRequest) (serverruntime.MergeFeatureResponse, error) {
	if t.orch == nil {
		return serverruntime.MergeFeatureResponse{FeatureID: featureID}, errors.New("orchestrator is not available")
	}
	if err := t.rejectStaleCompletionPreflight(featureID, req.SourceRevision); err != nil {
		return serverruntime.MergeFeatureResponse{FeatureID: featureID, Result: resultFailed}, err
	}
	if err := t.orch.MergeFeatureLocal(featureID); err != nil {
		return serverruntime.MergeFeatureResponse{FeatureID: featureID, Result: resultFailed}, err
	}
	return serverruntime.MergeFeatureResponse{FeatureID: featureID, Result: "merged"}, nil
}

func (t *serverMutationTarget) RepositoryPath(featureID, repoName string) (serverruntime.RepositoryPathResponse, error) {
	resp := serverruntime.RepositoryPathResponse{FeatureID: featureID, Repo: repoName}
	if t.orch == nil {
		return resp, errors.New("orchestrator is not available")
	}
	path, err := t.orch.RepositoryWorktreePath(featureID, repoName)
	if err != nil {
		return resp, err
	}
	resp.Path = path
	return resp, nil
}

func (t *serverMutationTarget) RewindFeature(featureID string, req serverruntime.RewindFeatureRequest) (serverruntime.RewindFeatureResponse, error) {
	requestedTarget := strings.ToLower(strings.TrimSpace(req.TargetPhase))
	targetPhase, err := parseServerPhaseStrict(req.TargetPhase)
	resp := serverruntime.RewindFeatureResponse{FeatureID: featureID, TargetPhase: requestedTarget, RoadmapPhase: req.RoadmapPhase}
	if err == nil {
		resp.TargetPhase = targetPhase.DirName()
	}
	if req.UpgradePipeline != "" {
		resp.UpgradePipeline = string(req.UpgradePipeline)
	}
	if err != nil {
		resp.Result = resultFailed
		return resp, err
	}
	if t.orch == nil {
		resp.Result = resultFailed
		return resp, errors.New("orchestrator is not available")
	}
	// Stale-preview guard: when the client presents a preview's source run
	// and revision, reject before any side effect if the active run changed
	// or rewind-relevant state advanced since the preview was computed.
	// Historical source runs (run number below the active run) are rejected
	// outright — rewind executes only against the current active run.
	current, err := t.validateRewindGuard(featureID, req)
	if err != nil {
		resp.Result = resultFailed
		return resp, err
	}
	sourceRunNumber := 0
	if current != nil {
		sourceRunNumber = current.ActiveRun
	}
	warnings, effectiveTarget, err := t.orch.RewindWithUpgrade(featureID, feature.RewindRequest{
		TargetPhase:  targetPhase,
		RoadmapPhase: req.RoadmapPhase,
	}, feature.PipelineProfile(req.UpgradePipeline))
	if effectiveTarget != 0 || strings.EqualFold(req.TargetPhase, phaseNameResearch) {
		resp.EffectivePhase = effectiveTarget.DirName()
	}
	resp.WarningCount = len(warnings)
	resp.SourceRunNumber = sourceRunNumber
	resp.Warnings = redactRewindWarnings(warnings)
	if err != nil {
		resp.Result = resultFailed
		return resp, err
	}
	resp.Result = "rewound"
	if t.store != nil {
		if updated, loadErr := t.store.Load(featureID); loadErr == nil {
			resp.NewRunNumber = updated.ActiveRun
		}
	}
	return resp, nil
}

// staleRewindError is a sentinel for a stale/historical rewind-preview guard
// rejection. It carries a redacted reason; no internal path or token is
// exposed across the API boundary.
type staleRewindError struct {
	reason string
}

func (e staleRewindError) Error() string { return e.reason }

// validateRewindGuard enforces that a rewind request was previewed against
// the current active run and rewind-relevant state. It performs no side
// effect and is safe to call before any mutation. It returns the current
// feature so execution can use the same loaded snapshot for its source run.
func (t *serverMutationTarget) validateRewindGuard(featureID string, req serverruntime.RewindFeatureRequest) (*feature.Feature, error) {
	if t.store == nil {
		if req.SourceRevision == "" {
			return nil, nil
		}
		return nil, errors.New("store is not available for rewind guard")
	}
	current, loadErr := t.store.Load(featureID)
	if loadErr != nil {
		return nil, fmt.Errorf("loading feature for rewind guard: %w", loadErr)
	}
	if req.SourceRevision == "" {
		return current, nil
	}
	if req.SourceRunNumber != 0 && req.SourceRunNumber != current.ActiveRun {
		return nil, staleRewindError{reason: "active run changed since preview"}
	}
	if got := feature.RewindRevision(current); got != req.SourceRevision {
		return nil, staleRewindError{reason: "rewind state changed since preview"}
	}
	return current, nil
}

// redactRewindWarnings sanitizes rewind warning strings for API exposure,
// stripping private tokens and bounding length, mirroring the server's
// safeDisplayText redaction.
func redactRewindWarnings(warnings []string) []string {
	if len(warnings) == 0 {
		return nil
	}
	out := make([]string, 0, len(warnings))
	for _, w := range warnings {
		out = append(out, serverruntime.SafeDisplayText(w, 300))
	}
	return out
}

func (t *serverMutationTarget) RetryFeature(featureID string) (serverruntime.RetryFeatureResponse, error) {
	if t.orch == nil {
		return serverruntime.RetryFeatureResponse{FeatureID: featureID}, errors.New("orchestrator is not available")
	}
	var current *feature.Feature
	if t.store != nil {
		f, err := t.store.Load(featureID)
		if err == nil {
			current = f
			if isFailedSetupFeature(f) {
				if err := t.orch.RetrySetup(featureID); err != nil {
					return serverruntime.RetryFeatureResponse{FeatureID: featureID, Result: resultFailed}, err
				}
				return serverruntime.RetryFeatureResponse{FeatureID: featureID, Result: resultRetried}, nil
			}
		}
	}
	maxIterationsDelta, maxPlanIterationsDelta := retryFeatureIterationDeltas(current)
	outcome, err := t.orch.RestartPhase(featureID, maxIterationsDelta, maxPlanIterationsDelta)
	if err != nil {
		return serverruntime.RetryFeatureResponse{FeatureID: featureID, Result: resultFailed}, err
	}
	restartResp := serverruntime.FeatureRestartResponse{FeatureID: featureID, Result: resultRetried}
	if err := t.dispatchRestartOutcome(featureID, outcome, &restartResp); err != nil {
		return serverruntime.RetryFeatureResponse{FeatureID: featureID, Result: resultFailed}, err
	}
	return serverruntime.RetryFeatureResponse{FeatureID: featureID, Result: resultRetried}, nil
}

func retryFeatureIterationDeltas(f *feature.Feature) (int, int) {
	if f == nil || f.Status != feature.StatusFailed || f.FailureType != feature.FailureMaxIterations {
		return 0, 0
	}
	return maxIterationsRetryDelta, maxPlanIterationsRetryDelta
}

func isFailedSetupFeature(f *feature.Feature) bool {
	if f == nil {
		return false
	}
	setup := f.Run().Setup
	return f.Status == feature.StatusFailed &&
		f.FailureType == feature.FailureWorktreeSetup &&
		setup != nil &&
		setup.Status == feature.SetupStatusFailed
}

func (t *serverMutationTarget) CompletionPreflight(featureID string) (serverruntime.CompletionPreflightResponse, error) {
	if t.orch == nil {
		return serverruntime.CompletionPreflightResponse{FeatureID: featureID}, errors.New("orchestrator is not available")
	}
	result, err := t.orch.CompletionPreflight(featureID)
	if err != nil {
		return serverruntime.CompletionPreflightResponse{FeatureID: featureID}, err
	}
	resp := serverruntime.CompletionPreflightResponse{
		APIVersion:      serverruntime.APIVersion,
		FeatureID:       result.FeatureID,
		SourceRevision:  result.SourceRevision,
		CanMarkDone:     result.CanMarkDone,
		MarkDoneBlocker: result.MarkDoneBlocker,
	}
	for _, r := range result.Repos {
		resp.Repos = append(resp.Repos, serverruntime.CompletionPreflightRepo{
			Repo:                  r.Repo,
			Publishable:           r.Publishable,
			Touched:               r.Touched,
			Status:                r.Status,
			PrURL:                 r.PRURL,
			Blocker:               r.Blocker,
			Freshness:             r.Freshness,
			LastError:             r.LastError,
			BaseBranch:            r.BaseBranch,
			Branch:                r.Branch,
			PendingCommits:        r.PendingCommits,
			PendingDirty:          r.PendingDirty,
			PushMode:              r.PushMode,
			PendingDirtyFiles:     r.PendingDirtyFiles,
			PendingDirtyFileTotal: r.PendingDirtyFileTotal,
		})
	}
	return resp, nil
}

func (t *serverMutationTarget) RepositoryDiff(featureID, repoName, filePath string) (serverruntime.RepositoryDiffResponse, error) {
	if t.orch == nil {
		return serverruntime.RepositoryDiffResponse{FeatureID: featureID, Repo: repoName}, errors.New("orchestrator is not available")
	}
	result, err := t.orch.RepositoryDiff(featureID, repoName, filePath)
	if err != nil {
		return serverruntime.RepositoryDiffResponse{FeatureID: featureID, Repo: repoName}, err
	}
	resp := serverruntime.RepositoryDiffResponse{
		APIVersion:      serverruntime.APIVersion,
		FeatureID:       result.FeatureID,
		Repo:            result.Repo,
		SourceRevision:  result.SourceRevision,
		Truncated:       result.Truncated,
		FileDiff:        result.FileDiff,
		FileTruncated:   result.FileTruncated,
		FileBinary:      result.FileBinary,
		FileUnavailable: result.FileUnavailable,
		PartialFailure:  result.PartialFailure,
	}
	for _, f := range result.Files {
		resp.Files = append(resp.Files, serverruntime.RepositoryDiffFile{
			Path:         f.Path,
			OldPath:      f.OldPath,
			Operation:    f.Operation,
			AddedLines:   f.AddedLines,
			RemovedLines: f.RemovedLines,
			Binary:       f.Binary,
			Fingerprint:  f.Fingerprint,
		})
	}
	return resp, nil
}

func (t *serverMutationTarget) RefactorFeature(featureID string, req serverruntime.RefactorFeatureRequest) (serverruntime.RefactorFeatureResponse, error) {
	resp := serverruntime.RefactorFeatureResponse{ParentID: featureID, Result: resultFailed}
	creator := t.childCreator
	if creator == nil {
		return resp, errors.New("feature manager is not available")
	}
	spec, err := serverruntime.RefactorChildSpecFromRequest(req)
	if err != nil {
		return resp, err
	}
	var child *feature.Feature
	if t.orch != nil {
		if wErr := t.orch.WithRelationshipWriteLock(func() error {
			var cErr error
			child, cErr = creator.CreateRefactorChild(featureID, spec)
			return cErr
		}); wErr != nil {
			return resp, wErr
		}
	} else {
		child, err = creator.CreateRefactorChild(featureID, spec)
		if err != nil {
			return resp, err
		}
	}
	// Setup intent is queued durably on creation; run it asynchronously so the
	// response returns with the child identifier immediately. RunSetupAsync
	// keeps the goroutine orchestrator-owned: its terminal errors are recorded
	// durably and signalled, and RunSetup serializes per feature with an
	// in-process lock. The orchestrator parks setup-complete children at
	// Created without starting the pipeline.
	if t.orch != nil {
		t.orch.ChildCreated(child)
		t.orch.RunSetupAsync(child.ID)
	}
	resp.FeatureID = child.ID
	resp.Result = resultCreated
	return resp, nil
}

func (t *serverMutationTarget) ReviewFeedbackFeature(featureID string, req serverruntime.ReviewFeedbackFeatureRequest) (serverruntime.ReviewFeedbackFeatureResponse, error) {
	resp := serverruntime.ReviewFeedbackFeatureResponse{ParentID: featureID, Result: resultFailed}
	creator := t.reviewFeedbackCreator
	if creator == nil {
		return resp, errors.New("feature manager is not available")
	}
	spec := serverruntime.ReviewFeedbackChildSpecFromRequest(req)
	var child *feature.Feature
	if t.orch != nil {
		if lockErr := t.orch.WithRelationshipWriteLock(func() error {
			var createErr error
			child, createErr = creator.CreateReviewFeedbackChild(featureID, spec)
			return createErr
		}); lockErr != nil {
			return resp, lockErr
		}
	} else {
		var createErr error
		child, createErr = creator.CreateReviewFeedbackChild(featureID, spec)
		if createErr != nil {
			return resp, createErr
		}
	}
	if t.orch != nil {
		t.orch.ChildCreated(child)
		t.orch.RunSetupAsync(child.ID)
	}
	resp.FeatureID = child.ID
	resp.Result = resultCreated
	return resp, nil
}

func (t *serverMutationTarget) RebaseFeature(featureID string, _ serverruntime.RebaseFeatureRequest) (serverruntime.RebaseFeatureResponse, error) {
	resp := serverruntime.RebaseFeatureResponse{ParentID: featureID, Result: resultFailed}
	creator := t.rebaseChildCreator
	if creator == nil {
		return resp, errors.New("feature manager is not available")
	}
	if t.orch == nil {
		return resp, errors.New("orchestrator is not available")
	}
	preflight, err := t.orch.RebaseChildPreflight(featureID)
	if err != nil {
		return resp, err
	}
	spec := feature.RebaseChildSpec{
		Bases:   preflight.Bases,
		Targets: preflight.Targets,
		Behind:  preflight.Behind,
	}
	var child *feature.Feature
	if wErr := t.orch.WithRelationshipWriteLock(func() error {
		var cErr error
		child, cErr = creator.CreateRebaseChild(featureID, spec)
		return cErr
	}); wErr != nil {
		return resp, wErr
	}
	t.orch.ChildCreated(child)
	t.orch.RunSetupAsync(child.ID)
	resp.FeatureID = child.ID
	resp.Result = resultCreated
	return resp, nil
}

func (t *serverMutationTarget) MarkDone(featureID string, req serverruntime.GuardedFeatureActionRequest) (serverruntime.MarkDoneResponse, error) {
	if t.orch == nil {
		return serverruntime.MarkDoneResponse{FeatureID: featureID}, errors.New("orchestrator is not available")
	}
	if err := t.rejectStaleCompletionPreflight(featureID, req.SourceRevision); err != nil {
		return serverruntime.MarkDoneResponse{FeatureID: featureID, Result: resultFailed}, err
	}
	if err := t.orch.MarkDone(featureID); err != nil {
		return serverruntime.MarkDoneResponse{FeatureID: featureID, Result: resultFailed}, err
	}
	return serverruntime.MarkDoneResponse{FeatureID: featureID, Result: "done"}, nil
}

func (t *serverMutationTarget) CleanupFeature(featureID string, req serverruntime.CleanupActionRequest) (serverruntime.CleanupFeatureResponse, error) {
	target := strings.ToLower(strings.TrimSpace(req.Target))
	if target == "" {
		target = cleanupTargetWorktrees
	}
	resp := serverruntime.CleanupFeatureResponse{FeatureID: featureID, Target: target}
	if t.orch == nil {
		return resp, errors.New("orchestrator is not available")
	}
	if err := t.rejectStaleCompletionPreflight(featureID, req.SourceRevision); err != nil {
		resp.Result = resultFailed
		return resp, err
	}
	switch target {
	case cleanupTargetWorktrees:
		if err := t.orch.CleanWorktree(featureID); err != nil {
			resp.Result = resultFailed
			return resp, err
		}
	default:
		resp.Result = resultFailed
		return resp, fmt.Errorf("unknown cleanup target %q", req.Target)
	}
	resp.Result = resultCleaned
	return resp, nil
}

func (t *serverMutationTarget) DeleteFeature(featureID string, req serverruntime.GuardedFeatureActionRequest) (serverruntime.DeleteFeatureResponse, error) {
	if t.orch == nil {
		return serverruntime.DeleteFeatureResponse{FeatureID: featureID}, errors.New("orchestrator is not available")
	}
	if err := t.rejectStaleCompletionPreflight(featureID, req.SourceRevision); err != nil {
		return serverruntime.DeleteFeatureResponse{FeatureID: featureID}, err
	}
	result, err := t.orch.DeleteCascade(featureID)
	if err != nil {
		return serverruntime.DeleteFeatureResponse{FeatureID: featureID}, err
	}
	return serverruntime.DeleteFeatureResponse{
		FeatureID:   result.ParentID,
		OperationID: result.OperationID,
		Status:      result.Status,
		Diagnostics: result.Diagnostics,
	}, nil
}

func (t *serverMutationTarget) DiscardChild(featureID string) (serverruntime.DiscardChildResponse, error) {
	if t.orch == nil {
		return serverruntime.DiscardChildResponse{FeatureID: featureID}, errors.New("orchestrator is not available")
	}
	if err := t.orch.DiscardChild(featureID); err != nil {
		return serverruntime.DiscardChildResponse{FeatureID: featureID, Result: resultFailed}, err
	}
	return serverruntime.DiscardChildResponse{FeatureID: featureID, Result: "discarded"}, nil
}

func (t *serverMutationTarget) rejectStaleCompletionPreflight(featureID, sourceRevision string) error {
	if sourceRevision == "" {
		return nil
	}
	if t.orch == nil {
		return errors.New("orchestrator is not available")
	}
	current, err := t.orch.CompletionPreflightSourceRevision(featureID)
	if err != nil {
		return err
	}
	if current == sourceRevision {
		return nil
	}
	return &serverruntime.ActionConflictError{
		Err:     orchestrator.ErrStalePreflight,
		Message: "stale completion preflight",
		Target:  map[string]any{"reason": "stale_preflight"},
	}
}

func actionConflictError(err error) error {
	if err == nil {
		return nil
	}
	var publishConflict *orchestrator.PublishConflictError
	if errors.As(err, &publishConflict) {
		return &serverruntime.ActionConflictError{
			Err:     err,
			Message: "publish conflict — resolve using the Rebase aftercare action",
			Target: map[string]any{
				resultConflict:   phaseNamePublish,
				repoConflictKey:  publishConflict.RepoName,
				"branch":         publishConflict.Branch,
				"rebase_target":  publishConflict.RebaseTarget,
				"conflict_files": []string{},
			},
		}
	}
	return nil
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
	if phase, ok := feature.ParsePhaseDirName(in); ok {
		return phase, nil
	}
	switch strings.ToLower(strings.TrimSpace(in)) {
	case phaseNameReview:
		return feature.PhaseReview, nil
	case phaseNamePublish:
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
			isAskUser := pending.Request.ToolName == toolNameAskUserQuestion
			if isAskUser != wantAskUser {
				return nil, nil, fmt.Errorf("request %s has incompatible control type", requestID)
			}
			return sess, pending, nil
		}
	}
	return nil, nil, fmt.Errorf("pending request %s not found", requestID)
}

func (t *serverMutationTarget) sendQueuedFeatureHelp(req serverruntime.HelpAnswerRequest) (serverruntime.HelpSendResponse, bool, error) {
	featureID := strings.TrimSpace(req.FeatureID)
	if featureID == "" || strings.TrimSpace(req.SessionID) != "" || t.store == nil {
		return serverruntime.HelpSendResponse{}, false, nil
	}
	message := strings.TrimSpace(req.Message)
	found := false
	if err := t.store.Modify(featureID, func(f *feature.Feature) error {
		for i := range f.HelpQueue {
			if !f.HelpQueue[i].Pending {
				continue
			}
			f.HelpQueue[i].Answer = message
			f.HelpQueue[i].Pending = false
			found = true
			return nil
		}
		return nil
	}); err != nil {
		return serverruntime.HelpSendResponse{}, true, fmt.Errorf("answer feature help queue: %w", err)
	}
	if !found {
		return serverruntime.HelpSendResponse{}, false, nil
	}
	return serverruntime.HelpSendResponse{FeatureID: featureID, Result: resultSent}, true, nil
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

func (t *serverMutationTarget) needUserInputGatePath(featureID string) (string, error) {
	if t.store == nil {
		return "", errors.New("feature store is not available")
	}
	f, err := t.store.Load(featureID)
	if err != nil {
		return "", err
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
		} else {
			ordinal := i + 1
			questionByKey[strconv.Itoa(ordinal)] = q
			questionByKey[fmt.Sprintf("q%d", ordinal)] = q
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

func mergeRuntimeDefaultsMutation(dst *config.DefaultsConfig, patch serverruntime.RuntimeDefaultsMutation) bool {
	if dst == nil {
		return false
	}
	changed := false
	if patch.Models != nil {
		next := serverruntime.ApplyModelConfigPatch(dst.Models, *patch.Models)
		if next != dst.Models {
			dst.Models = next
			changed = true
		}
	}
	if hasAnyEffortConfig(patch.Effort) {
		next := config.OverlayEffortConfig(dst.Effort, patch.Effort)
		if next != dst.Effort {
			dst.Effort = next
			changed = true
		}
	}
	if patch.ExitCriteria != "" && setIfChanged(&dst.ExitCriteria, patch.ExitCriteria) {
		changed = true
	}
	if patch.Inquireness != "" && setIfChanged(&dst.Inquireness, patch.Inquireness) {
		changed = true
	}
	if patch.Pipeline != "" && setIfChanged(&dst.Pipeline, patch.Pipeline) {
		changed = true
	}
	if patch.MaxIterations > 0 && setIfChanged(&dst.MaxIterations, patch.MaxIterations) {
		changed = true
	}
	if patch.MaxConsecutiveFailures > 0 && setIfChanged(&dst.MaxConsecutiveFailures, patch.MaxConsecutiveFailures) {
		changed = true
	}
	if patch.MaxConsecutiveNoProgress > 0 && setIfChanged(&dst.MaxConsecutiveNoProgress, patch.MaxConsecutiveNoProgress) {
		changed = true
	}
	if patch.MaxPhasePlanIterations > 0 && setIfChanged(&dst.MaxPhasePlanIterations, patch.MaxPhasePlanIterations) {
		changed = true
	}
	if patch.Checkpoints != nil && setCheckpointsIfChanged(&dst.Checkpoints, *patch.Checkpoints) {
		changed = true
	}
	if len(patch.PipelinePreferences) > 0 {
		dst.PipelinePreferences = patch.PipelinePreferences
		changed = true
	}
	if patch.AutomaticReviewEnabled != nil && setIfChanged(&dst.AutomaticReviewEnabled, *patch.AutomaticReviewEnabled) {
		changed = true
	}
	return changed
}

func setCheckpointsIfChanged(dst *config.Checkpoints, val config.Checkpoints) bool {
	if dst.InquiryReview == val.InquiryReview &&
		dst.ResearchReview == val.ResearchReview &&
		dst.DesignReview == val.DesignReview &&
		dst.RoadmapReview == val.RoadmapReview &&
		dst.PhasePlanReview == val.PhasePlanReview &&
		dst.ManualPublish == val.ManualPublish &&
		dst.DraftPublish == val.DraftPublish {
		return false
	}
	*dst = val
	return true
}

// setIfChanged assigns val to *dst and reports true if that changed dst's value.
func setIfChanged[T comparable](dst *T, val T) bool {
	if val == *dst {
		return false
	}
	*dst = val
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

func (t *serverMutationTarget) persistPipelinePreferences(repos []string, pipeline feature.PipelineProfile, models config.ModelConfig, effort config.EffortConfig, inquireness feature.Inquireness, checkpoints feature.Checkpoints, publishable bool) error {
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
		Effort:      effort,
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
	phase, err := parseServerPhaseStrict(in)
	if err != nil {
		return feature.Phase(0)
	}
	return phase
}

func bootstrapRuntime(ctx context.Context, configPath, stateDir string, dangerouslySkipPerms bool, enabledProviders []string, refreshModels bool, stderr io.Writer) (*runtimeBootstrap, error) {
	stateDir = canonicalizeStateDir(stateDir)
	runtimeDir := filepath.Dir(stateDir)
	lock, acquired, owner, err := instancelock.Acquire(runtimeDir, stateDir, configPath, buildinfo.Version())
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
	var worktrees feature.WorktreeOps
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
		session.Module,
		observe.Module,
		permission.Module,
		llm.Module,
		fx.Options(providerFxModules(enabledProviders)...),
		agent.Module,
		orchestrator.Module,
		fx.Populate(&fm, &sm, &orch, &registry, &cfg, &phaseRunner, &observer, &permissionCache, &worktrees),
		fx.NopLogger,
	)
	boot.fxApp = fxApp
	if err := fxApp.Start(ctx); err != nil {
		return nil, fmt.Errorf("initializing: %w", err)
	}

	detected, warnings, startupNotices, availabilityFiltered, err := checkRequiredProviders(ctx, registry)
	if err != nil {
		// Setup-capable mode: the headless server stays reachable when no
		// provider CLI is installed or authenticated so a first-launch client
		// can drive remediation through /api/v1/readiness and re-probe with
		// /api/v1/readiness/refresh instead of requiring a new runtime. Model
		// routing is restricted to nothing until a readiness refresh finds a
		// usable provider; feature creation is gated server-side meanwhile.
		fmt.Fprintf(stderr, "Warning: no usable LLM provider; starting in setup-capable mode.\n%v\n", err)
		registry.RestrictToProviders(nil)
		detected, warnings, startupNotices, availabilityFiltered = nil, nil, nil, true
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
		switch {
		case vr.Err != nil:
			fmt.Fprintf(stderr, "Warning: could not check %s CLI version: %v\n", vr.Provider, vr.Err)
		case vr.Warning != "":
			fmt.Fprintf(stderr, "Warning: %s\n", vr.Warning)
		default:
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
	boot.worktrees = worktrees
	boot.eventCh = eventCh
	boot.workspaceDir = workspaceDir
	boot.recoveryItems = recoveryItems
	boot.recoveryScanOK = recoveryScanOK
	success = true
	return boot, nil
}

func scanStartupRecovery(ctx context.Context, orch *orchestrator.Orchestrator, stderr io.Writer) ([]ports.RecoveryItem, bool) {
	if orch == nil {
		return nil, true
	}
	items, err := orch.ScanRecovery(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "Warning: startup recovery scan: %v\n", err)
		return nil, false
	}
	recoveryItems := make([]ports.RecoveryItem, len(items))
	copy(recoveryItems, items)
	return recoveryItems, true
}

// knownProviderNames is the fixed set of names accepted by --providers.
func knownProviderNames() []string {
	return []string{providerNameClaude, providerNameCodex, providerNameOpencode}
}

// normalizeProviderNames validates/trims enabled against knownProviderNames,
// defaulting to all of them when enabled is nil and reporting unknowns via
// warn. warnBlank preserves each caller's differing blank-name behavior.
func normalizeProviderNames(enabled []string, warnBlank bool, warn func(name string)) []string {
	if enabled == nil {
		return knownProviderNames()
	}
	known := make(map[string]bool)
	for _, name := range knownProviderNames() {
		known[name] = true
	}
	var valid []string
	for _, name := range enabled {
		name = strings.TrimSpace(name)
		if name == "" && !warnBlank {
			continue
		}
		if known[name] {
			valid = append(valid, name)
			continue
		}
		if warn != nil {
			warn(name)
		}
	}
	return valid
}

func runServer(configPath, stateDir string, dangerouslySkipPerms bool, enabledProviders []string, refreshModels bool) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runtimeCtx, requestShutdown := context.WithCancel(ctx)
	defer requestShutdown()

	boot, err := bootstrapRuntime(ctx, configPath, stateDir, dangerouslySkipPerms, enabledProviders, refreshModels, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	defer func() {
		if err := boot.Close(context.Background()); err != nil {
			log.Printf("close runtime: %v", err)
		}
	}()

	if shouldInterruptRunningOnStartup(
		boot.recoveryScanOK,
		len(boot.recoveryItems),
		len(boot.sessionManager.ActiveSessions()),
	) {
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
	authToken, err := serverruntime.EnsureAuthToken(boot.runtime.RuntimeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: preparing server auth token: %v\n", err)
		return 1
	}

	runtimeServer, err := serverruntime.Start(ctx, serverruntime.Options{
		Runtime:      boot.runtime,
		LaunchPolicy: policy,
		StartMode:    cliSubcommandServer,
		Owner:        boot.owner,
		AuthToken:    authToken,
		Features:     boot.featureManager,
		FeatureStore: boot.featureManager.Store,
		Freshness:    newGitFreshnessProvider(),
		Config:       boot.cfg,
		Registry:     boot.registry,
		Sessions:     boot.sessionManager,
		Events:       boot.eventCh,
		DomainEvents: boot.orchestrator.Events(),
		Mutations: &serverMutationTarget{
			orch:                  boot.orchestrator,
			childCreator:          boot.featureManager,
			reviewFeedbackCreator: boot.featureManager,
			rebaseChildCreator:    boot.featureManager,
			cfg:                   boot.cfg,
			configPath:            boot.runtime.Config,
			store:                 boot.featureManager.Store,
			sessions:              boot.sessionManager,
			phaseRunner:           boot.phaseRunner,
			permissionCache:       boot.permissionCache,
			workspaceDir:          boot.workspaceDir,
		},
		PersistProviderModelCatalog: func(provider llm.LLMProvider, models []llm.ModelInfo) error {
			return persistRefreshedProviderModelCatalog(boot.runtime.RuntimeDir, provider, models)
		},
		Worktrees: boot.worktrees,
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
		Epoch:         runtimeServer.EventEpoch(),
		AuthToken:     authToken,
		Runtime:       boot.runtime,
		LaunchPolicy:  policy,
		StartMode:     cliSubcommandServer,
		PID:           boot.owner.PID,
		PGID:          boot.owner.PGID,
		StartedAt:     runtimeServer.StartedAt(),
		PublishedAt:   now,
		Owner:         serverruntime.OwnerFromInstanceOwner(boot.owner),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Error: publishing discovery metadata: %v\n", err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "Agentic server listening at %s\n", runtimeServer.BaseURL())
	<-runtimeCtx.Done()
	shutdownFeatures(boot.orchestrator, boot.sessionManager)
	return 0
}

func shouldInterruptRunningOnStartup(recoveryScanOK bool, recoveryItemCount, activeSessionCount int) bool {
	return recoveryScanOK && recoveryItemCount == 0 && activeSessionCount == 0
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
		providerNameClaude:   claude.Module,
		providerNameCodex:    codex.Module,
		providerNameOpencode: opencode.Module,
	}

	names := normalizeProviderNames(enabled, true, func(name string) {
		fmt.Fprintf(os.Stderr, "Warning: unknown provider %q, skipping\n", name)
	})

	if len(names) == 0 {
		fmt.Fprintln(os.Stderr, "Error: no valid providers specified in --providers flag")
		os.Exit(1)
	}

	modules := make([]fx.Option, 0, len(names))
	for _, name := range names {
		modules = append(modules, all[name])
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
		m.KBBuild != "" ||
		m.AutomaticReview != ""
}

func hasAnyEffortConfig(e config.EffortConfig) bool {
	return e.Inquiry != "" ||
		e.Research != "" ||
		e.Planning != "" ||
		e.Implementation != "" ||
		e.Review != "" ||
		e.Utilities != "" ||
		e.KBBuild != ""
}

func mergeModelConfig(base, overlay config.ModelConfig) config.ModelConfig {
	return serverruntime.ApplyModelConfigPatch(base, serverruntime.ModelConfigToPatch(overlay))
}

func modelConfigDefaultsMap(m config.ModelConfig) map[string]string {
	return map[string]string{
		"inquiry":        m.Inquiry,
		"research":       m.Research,
		"planning":       m.Planning,
		"implementation": m.Implementation,
		"review":         m.Review,
		chatName:         m.Utilities,
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
// running state to Interrupted so the desktop app shows a clean state on next launch.
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
