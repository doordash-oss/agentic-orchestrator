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
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/buildinfo"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
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
	"github.com/doordash-oss/agentic-orchestrator/internal/selfupdate"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/server/mutations"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/internal/skilldef"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor/claudesession"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor/codexsession"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor/opencodesession"
	"github.com/doordash-oss/agentic-orchestrator/internal/workadmission"
	"go.uber.org/fx"
	"golang.org/x/term"
)

const (
	// Default paths live under the Agentic Orchestrator runtime parent.
	defaultRuntimeParent = "~/.agentic-orchestrator"

	defaultConfigBasename = "config.yaml"
	defaultStateBasename  = "features"
)

// Names shared by the launcher and its tests to avoid duplicated literals.
const (
	chatName = "chat"

	phaseNameReview = "review"

	providerNameClaude   = "claude"
	providerNameCodex    = "codex"
	providerNameOpencode = "opencode"

	modelNameSonnet = "sonnet"

	cliSubcommandServer            = "server"
	cliSubcommandValidateArtifacts = "validate-artifacts"
	cliSubcommandVerifyEvidence    = "verify-evidence"
	cliSubcommandCapabilityProbe   = "capability-probe"
	cliSubcommandReportBlocker     = "report-blocker"
	cliFlagDir                     = "--dir"
	cliFlagPhase                   = "--phase"
	cliFlagRole                    = "--role"
	cliFlagContract                = "--contract"
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
	launchModeCapabilityProbe
	launchModeReportBlocker
	launchModeAPI
)

type launchOptions struct {
	configPath           string
	stateDir             string
	dangerouslySkipPerms bool
	enabledProviders     []string
	refreshModels        bool
	listenAddr           string
	serverName           string
	// updatesPolicy carries the raw --updates value (both --updates=v and
	// --updates v forms normalize here). Empty means the flag was not passed.
	updatesPolicy     string
	mode              launchMode
	validateArtifacts validateArtifactsOptions
	verifyEvidence    verifyEvidenceOptions
	capabilityProbe   capabilityProbeOptions
	reportBlocker     reportBlockerOptions
	api               apiOptions
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

// capabilityProbeOptions names one built-in capability, e.g.
// "authenticated-browser(slack.com)", to probe from the current environment.
type capabilityProbeOptions struct {
	name string
}

// reportBlockerOptions describes an implementer escalation: contract items
// blocked by a capability the environment lacks.
type reportBlockerOptions struct {
	contract   string
	dir        string
	items      []string
	capability string
	reason     string
}

type serverLauncher func(configPath, stateDir string, dangerouslySkipPerms bool, enabledProviders []string, refreshModels bool, listenAddr, serverName, updatesPolicy string) int

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

// legacyRuntimeParent is the pre-rename runtime parent. The server registry
// publishes into it only when the fresh parent does not exist but the legacy
// one does, so servers installed under a legacy tree register where legacy
// desktop builds look. The desktop main process mirrors this rule.
const legacyRuntimeParent = "~/.agentic-workflow"

// resolveRegistryParent resolves the runtime parent that owns the central
// server registry: the fresh parent when it exists (or nothing exists, so
// new installs always land there), the legacy parent only when it alone
// exists. Never fails — the registry publish itself warns and continues on
// any filesystem error.
func resolveRegistryParent() string {
	fresh := pickRuntimeParent()
	if _, err := os.Stat(fresh); err == nil {
		return fresh
	}
	legacy := config.ExpandHome(legacyRuntimeParent)
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return fresh
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
		renderError(stderr, errcat.InvalidUsage, errcat.WithParams(errcat.UsageParams{Reason: err.Error()}))
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
	case launchModeCapabilityProbe:
		return runCapabilityProbe(opts.capabilityProbe, stdout, stderr)
	case launchModeReportBlocker:
		return runReportBlocker(opts.reportBlocker, stdout, stderr)
	case launchModeAPI:
		return runAPI(opts.api, stdout, stderr)
	case launchModeServer:
		providers, ok := validateProviderSelection(stderr, opts.enabledProviders)
		if !ok {
			return 1
		}
		return launchServer(opts.configPath, opts.stateDir, opts.dangerouslySkipPerms, providers, opts.refreshModels, opts.listenAddr, opts.serverName, opts.updatesPolicy)
	default:
		if err := launchDesktop(); err != nil {
			renderError(stderr, errcat.DesktopLaunchFailed, errcat.WithDiagnostics(err.Error()))
			return 1
		}
		return 0
	}
}

// validateProviderSelection validates the --providers list ahead of runtime
// construction: unknown names render provider-family warnings on stderr, and
// an empty valid set is an invalid-usage failure returned through the normal
// exit-code path instead of a direct process exit. It returns the valid
// (trimmed) names and whether the launch may continue; a nil enabled list
// means "all defaults" and passes through untouched.
func validateProviderSelection(stderr io.Writer, enabled []string) ([]string, bool) {
	if enabled == nil {
		return nil, true
	}
	var unknown []string
	valid := normalizeProviderNames(enabled, true, func(name string) {
		unknown = append(unknown, name)
	})
	if len(valid) == 0 {
		renderError(stderr, errcat.InvalidUsage, errcat.WithParams(errcat.UsageParams{
			Reason: fmt.Sprintf("no valid providers specified in --providers flag (unknown: %s)", strings.Join(unknown, ", ")),
		}))
		return nil, false
	}
	for _, name := range unknown {
		renderError(stderr, errcat.ProviderUnavailable,
			errcat.WithParams(errcat.ProviderUnavailableParams{Provider: name}),
			errcat.WithDiagnostics(fmt.Sprintf("unknown provider %q, skipping", name)),
		)
	}
	return valid, true
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
	// Driver builds (agentico_selfupdate_driver tag) recognize the
	// selfupdate-driver subcommand here. Ordinary builds have a nil hook, so
	// that word falls through to the launch-flag loop and rejects as an
	// unknown command exactly like any other unrecognized first argument.
	if len(args) > 0 && driverArgParseHook != nil {
		if driverOpts, handled, err := driverArgParseHook(args); handled || err != nil {
			return driverOpts, err
		}
	}
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
	if len(args) > 0 && args[0] == cliSubcommandCapabilityProbe {
		opts.mode = launchModeCapabilityProbe
		return parseCapabilityProbeArgs(opts, args[1:])
	}
	if len(args) > 0 && args[0] == cliSubcommandReportBlocker {
		opts.mode = launchModeReportBlocker
		return parseReportBlockerArgs(opts, args[1:])
	}
	if len(args) > 0 && args[0] == cliSubcommandAPI {
		opts.mode = launchModeAPI
		return parseAPIArgs(opts, args[1:])
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
		case "--listen":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--listen requires a value")
			}
			i++
			opts.listenAddr = args[i]
		case "--name":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--name requires a value")
			}
			i++
			opts.serverName = args[i]
		case "--updates":
			// Separate-value form: --updates off.
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--updates requires a value")
			}
			i++
			opts.updatesPolicy = args[i]
		default:
			if strings.HasPrefix(arg, "--updates=") {
				// Equals form: --updates=off.
				opts.updatesPolicy = strings.TrimPrefix(arg, "--updates=")
				continue
			}
			if strings.HasPrefix(arg, "-") {
				return opts, fmt.Errorf("unknown flag: %s", arg)
			}
			return opts, fmt.Errorf("unknown command: %s", arg)
		}
	}
	if opts.mode == launchModeDesktop && serverOnlyFlag != "" {
		return opts, fmt.Errorf("%s is available only with the headless server; run 'agentico server %s ...'", serverOnlyFlag, serverOnlyFlag)
	}
	// The equals form bypasses the server-only flag probe above, so reject
	// any parsed --updates value outside server mode here.
	if opts.updatesPolicy != "" && opts.mode != launchModeServer {
		return opts, fmt.Errorf("--updates is available only with the headless server; run 'agentico server --updates ...'")
	}
	// --updates values are syntax-checked here so a typo fails fast, before
	// any socket is opened. The effective value still resolves later —
	// flag over AGENTICO_UPDATES over server.updates.policy — so an
	// overridden source's "auto" never fails a launch that never uses it.
	if opts.updatesPolicy != "" {
		if _, ok := selfupdate.ParsePolicyValue(opts.updatesPolicy); !ok {
			return opts, fmt.Errorf("invalid --updates value %q: expected off, notify, or auto", opts.updatesPolicy)
		}
	}
	// --listen/--name values are normalized and validated here at parse time
	// (after the server-only check above) so bad values fail fast, before any
	// socket is opened.
	if opts.listenAddr != "" {
		resolved, err := serverruntime.ResolveListenAddr(opts.listenAddr)
		if err != nil {
			return opts, err
		}
		opts.listenAddr = resolved
	}
	if name := strings.TrimSpace(opts.serverName); name != "" {
		if err := serverruntime.ValidateServerName(name); err != nil {
			return opts, fmt.Errorf("invalid --name value: %w", err)
		}
		opts.serverName = name
	}
	return opts, nil
}

func isServerOnlyLaunchFlag(flag string) bool {
	switch flag {
	case "--config", "--state-dir", "--dangerously-skip-permissions", "--providers", "--refresh-models", "--listen", "--name", "--updates":
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

func parseCapabilityProbeArgs(opts launchOptions, args []string) (launchOptions, error) {
	for _, arg := range args {
		switch arg {
		case "--help", "-h":
			opts.mode = launchModeHelp
			return opts, nil
		default:
			if strings.HasPrefix(arg, "-") {
				return opts, fmt.Errorf("unknown capability-probe flag: %s", arg)
			}
			if opts.capabilityProbe.name != "" {
				return opts, fmt.Errorf("capability-probe accepts exactly one capability name")
			}
			opts.capabilityProbe.name = arg
		}
	}
	if strings.TrimSpace(opts.capabilityProbe.name) == "" {
		return opts, fmt.Errorf("capability-probe requires a capability name, e.g. authenticated-browser(slack.com)")
	}
	return opts, nil
}

func parseReportBlockerArgs(opts launchOptions, args []string) (launchOptions, error) {
	value := func(i *int, flag string) (string, error) {
		if *i+1 >= len(args) {
			return "", fmt.Errorf("%s requires a value", flag)
		}
		*i++
		return args[*i], nil
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		var err error
		switch arg {
		case cliFlagContract:
			opts.reportBlocker.contract, err = value(&i, arg)
		case cliFlagDir:
			opts.reportBlocker.dir, err = value(&i, arg)
		case "--items":
			var raw string
			if raw, err = value(&i, arg); err == nil {
				for _, id := range strings.Split(raw, ",") {
					if id = strings.TrimSpace(id); id != "" {
						opts.reportBlocker.items = append(opts.reportBlocker.items, id)
					}
				}
			}
		case "--capability":
			opts.reportBlocker.capability, err = value(&i, arg)
		case "--reason":
			opts.reportBlocker.reason, err = value(&i, arg)
		case "--help", "-h":
			opts.mode = launchModeHelp
			return opts, nil
		default:
			if strings.HasPrefix(arg, "-") {
				return opts, fmt.Errorf("unknown report-blocker flag: %s", arg)
			}
			return opts, fmt.Errorf("unknown report-blocker argument: %s", arg)
		}
		if err != nil {
			return opts, err
		}
	}
	for flag, v := range map[string]string{cliFlagContract: opts.reportBlocker.contract, cliFlagDir: opts.reportBlocker.dir, "--capability": opts.reportBlocker.capability, "--reason": opts.reportBlocker.reason} {
		if strings.TrimSpace(v) == "" {
			return opts, fmt.Errorf("report-blocker requires %s", flag)
		}
	}
	if len(opts.reportBlocker.items) == 0 {
		return opts, fmt.Errorf("report-blocker requires --items <id,id,...>")
	}
	return opts, nil
}

// runCapabilityProbe runs one built-in capability probe from the current
// environment and prints its verdict. Exit 0 means available. Agents and
// operators use it to see exactly what the harness will check.
func runCapabilityProbe(opts capabilityProbeOptions, stdout, stderr io.Writer) int {
	name, arg, ok := agent.ParseCapabilityName(opts.name)
	if !ok {
		renderError(stderr, errcat.InvalidUsage, errcat.WithParams(errcat.UsageParams{Reason: fmt.Sprintf("capability %q is not name or name(argument)", opts.name)}))
		return 1
	}
	if err := agent.ValidateRegistryCapability(name, arg); err != nil {
		renderError(stderr, errcat.InvalidUsage, errcat.WithParams(errcat.UsageParams{Reason: err.Error()}))
		return 1
	}
	policy := agent.CapabilityPolicy{RuntimeDir: resolveRegistryParent(), AllowBrowserState: true}
	result := agent.RunRegistryCapabilityProbe(agent.WithCapabilityPolicy(context.Background(), policy), name, arg)
	if !result.Available {
		fmt.Fprintf(stderr, "capability %s unavailable: %s\n", opts.name, result.Reason)
		return 1
	}
	fmt.Fprintf(stdout, "capability %s available\n", opts.name)
	for _, kv := range result.Env {
		fmt.Fprintln(stdout, kv)
	}
	return 0
}

// runReportBlocker writes the implementer's capability-blocker gate into the
// iteration directory. The implementer then ends the iteration with RETRY and
// the harness pauses on the user gate instead of looping on chat questions.
func runReportBlocker(opts reportBlockerOptions, stdout, stderr io.Writer) int {
	contract, err := agent.ReadTestingContract(opts.contract)
	if err != nil {
		renderError(stderr, errcat.ContractInputUnreadable,
			errcat.WithDiagnostics(fmt.Sprintf("reading testing contract: %v", err)))
		return 1
	}
	rec, err := agent.SynthesizeAgentReportedBlockerGate(opts.contract, contract, opts.items, opts.capability, opts.reason, 0)
	if err != nil {
		renderError(stderr, errcat.InvalidUsage, errcat.WithParams(errcat.UsageParams{Reason: err.Error()}))
		return 1
	}
	gatePath := agent.NeedUserInputPath(opts.dir)
	if err := agent.WriteNeedUserInputRecord(gatePath, rec); err != nil {
		renderError(stderr, errcat.ContractInputUnreadable,
			errcat.WithDiagnostics(fmt.Sprintf("writing blocker gate: %v", err)))
		return 1
	}
	fmt.Fprintf(stdout, "blocker gate written: %s\nEnd this iteration with `RETRY` in progress.md; the harness will pause for the user's decision.\n", gatePath)
	return 0
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
		renderError(stderr, errcat.ContractInputUnreadable,
			errcat.WithDiagnostics(fmt.Sprintf("reading testing contract: %v", err)))
		return 1
	}
	violations := agent.PreflightAgentEvidence(contract, opts.dir)
	if len(violations) > 0 {
		renderProtocolViolations(stderr, "evidence contract", violations)
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
       agentico verify-evidence --contract </abs/path/testing-contract.yaml> --dir </abs/path/iteration_dir>
       agentico capability-probe <name[(argument)]>
       agentico report-blocker --contract </abs/path/testing-contract.yaml> --dir </abs/path/iteration_dir> \
                               --items <id,id,...> --capability <name> --reason <text>
       agentico api [--runtime-dir <dir>] [--timeout <duration>] [--after <cursor>] METHOD /api/v1/<path> [json]

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
Run 'agentico capability-probe' to check one built-in capability (for example
authenticated-browser(slack.com), display, docker, network(host)) the way the
harness will. Run 'agentico report-blocker' from implementer sessions when
required evidence needs a capability the environment lacks; it records the
blocked rows for the user's decision instead of an unanswerable chat question.
Agent sessions must run verify-evidence and report-blocker as one bare command
with absolute literal paths (no cd, &&, ;, pipes, redirects, or shell variables);
the permission guard refuses any other shape that references the contract.
Run 'agentico api' on the server machine to make one authenticated REST call
through the server's discovery file without handling the bearer token; SSE
stream paths require --timeout and print one line per event. See
'agentico api --help'.

Server flags (use with 'agentico server'):
  --config <path>                  Config file path (default: ~/.agentic-orchestrator/config.yaml)
  --state-dir <path>               State directory path (default: ~/.agentic-orchestrator/features)
  --providers <list>               Comma-separated provider list (default: all)
                                   Available: claude, codex, opencode
  --refresh-models                 Refresh provider model catalogs before starting the server
  --listen [host:]port             Bind address (default: ephemeral 127.0.0.1 port).
                                   Wildcards (0.0.0.0, ::) expose the server on the
                                   network and print a bearer-token connection string;
  --name <name>                    Server display name (default: generated, persisted per
                                    runtime directory)
  --updates <policy>               Release-availability policy for the server: off, notify, or
                                    auto (default: notify; accepts --updates=v and --updates v).
                                    auto installs each newer release when the server is idle,
                                    inside server.updates.window when one is set, and never
                                    stops work. Precedence: this flag, then AGENTICO_UPDATES,
                                    then server.updates.policy in config.yaml.
  --dangerously-skip-permissions   Skip all permission prompts (use with caution)
  --check, -n                      With 'update': check for a newer release without installing
Global flags:
  --help, -h                       Show this help
  --version, -v                    Show version`)
}

func runValidateArtifacts(opts validateArtifactsOptions, stdout, stderr io.Writer) int {
	phase, err := feature.ParsePhaseName(opts.phase)
	if err != nil {
		renderError(stderr, errcat.InvalidUsage, errcat.WithParams(errcat.UsageParams{Reason: err.Error()}))
		return 1
	}
	out, violations, err := agent.ValidateArtifactsPreflight(phase, agent.Role(strings.TrimSpace(opts.role)), opts.dir)
	if err != nil {
		renderError(stderr, errcat.ContractInputUnreadable,
			errcat.WithDiagnostics(fmt.Sprintf("validating artifacts: %v", err)))
		return 1
	}
	if !out.OK || len(violations) > 0 {
		renderProtocolViolations(stderr, "artifact contract", violations)
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
func checkRequiredProviders(ctx context.Context, registry *llm.Registry) ([]llm.LLMProvider, []startupWarning, []startupWarning, bool, error) {
	all := registry.All()
	var warnings []startupWarning
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
	below, detail, remedy := agent.BelowMinVersionGuidance(p)
	if !below {
		return llm.ProviderReadiness{}, true
	}
	return llm.ProviderReadiness{
		Ready:  false,
		Detail: detail,
		Remedy: remedy,
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

// formatProviderStartupNotices builds the delayed startup notices about
// missing or unready providers as structured warnings: the catalog authors
// the summary for the provider, and the original notice text rides along as
// diagnostics.
func formatProviderStartupNotices(ready []llm.LLMProvider, missing []llm.LLMProvider, unready []providerReadinessIssue) []startupWarning {
	if len(ready) == 0 || (len(missing) == 0 && len(unready) == 0) {
		return nil
	}
	readyText := formatProviderNameList(ready)
	var notices []startupWarning
	for _, p := range missing {
		notices = append(notices, startupWarning{
			code:        errcat.ProviderUnavailable,
			params:      errcat.ProviderUnavailableParams{Provider: p.Name()},
			diagnostics: fmt.Sprintf("Provider %s CLI was not found. Install with: %s. Starting with %s only.", p.Name(), p.InstallHint(), readyText),
		})
	}
	for _, issue := range unready {
		notices = append(notices, startupWarning{
			code:        errcat.ProviderUnavailable,
			params:      errcat.ProviderUnavailableParams{Provider: issue.provider.Name()},
			diagnostics: fmt.Sprintf("Provider %s is not configured: %s Starting with %s only.", issue.provider.Name(), formatReadinessProblem(issue.status), readyText),
		})
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

func discoverProviderCatalogs(ctx context.Context, providers []llm.LLMProvider, cacheRoot string, report providerCatalogDiscoveryProgress, refreshModels bool) []startupWarning {
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

	warningsByProvider := make([][]startupWarning, len(providers))
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

	var warnings []startupWarning
	for _, providerWarnings := range warningsByProvider {
		warnings = append(warnings, providerWarnings...)
	}
	return warnings
}

// catalogWarning builds one model-catalog degradation warning: the catalog
// authors the summary naming the provider, and the original condition text
// rides along as diagnostics.
func catalogWarning(providerName, diagnostics string) startupWarning {
	return startupWarning{
		code:        errcat.ModelCatalogDegraded,
		params:      errcat.ProviderIssueParams{Provider: providerName},
		diagnostics: diagnostics,
	}
}

func discoverOneProviderCatalog(ctx context.Context, p llm.LLMProvider, discoverer llm.CatalogDiscoverer, enricher llm.CatalogEnricher, cacheRoot string, report providerCatalogDiscoveryProgress, refreshModels bool) []startupWarning {
	var warnings []startupWarning
	providerName := p.Name()
	if policy, ok := p.(llm.CatalogRefreshPolicy); ok && policy.RefreshCatalogOnStartup() {
		refreshModels = true
	}
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
				warnings = append(warnings, catalogWarning(providerName,
					"reported an unrecognized CLI version; running model discovery without caching",
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
			warnings = append(warnings, catalogWarning(providerName,
				fmt.Sprintf("ignoring cached model catalog; refreshing: %v", err),
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
		warnings = append(warnings, catalogWarning(providerName,
			fmt.Sprintf("could not discover model catalog; retaining provider catalog: %v", err),
		))
		return warnings
	}
	if len(models) == 0 {
		if fallback, ok := tryStaleCacheFallback(enricher, cacheRoot, providerName, version, refreshModels, "discovered empty catalog"); ok {
			return append(warnings, fallback...)
		}
		warnings = append(warnings, catalogWarning(providerName,
			"discovered empty model catalog; retaining provider catalog",
		))
		return warnings
	}
	enricher.SetModelCatalog(models)
	if version != "" {
		if err := saveProviderCatalogCache(cacheRoot, providerName, version, models); err != nil {
			warnings = append(warnings, catalogWarning(providerName,
				fmt.Sprintf("could not cache model catalog: %v", err),
			))
		}
	}
	return warnings
}

// tryStaleCacheFallback serves a previously cached catalog when a refresh
// failed (reason); ok is false if no cache fallback applies.
func tryStaleCacheFallback(enricher llm.CatalogEnricher, cacheRoot, providerName, version string, refreshModels bool, reason string) ([]startupWarning, bool) {
	if !refreshModels || cacheRoot == "" || version == "" {
		return nil, false
	}
	cached, cerr := loadProviderCatalogCacheFile(cacheRoot, providerName, version)
	if cerr != nil {
		return nil, false
	}
	enricher.SetModelCatalog(cached.Models)
	warning := catalogWarning(providerName, fmt.Sprintf(
		"could not refresh model catalog; %s; using stale cache from %s",
		reason,
		cached.DiscoveredAt.Format(time.RFC3339),
	))
	return []startupWarning{warning}, true
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

func showProviderStartupNotices(w io.Writer, notices []startupWarning, delay time.Duration) {
	if len(notices) == 0 {
		return
	}
	renderStartupWarnings(w, notices)
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
	selfUpdateExec  selfupdate.Executable
	updateLease     *selfupdate.Lease
	// updateLeaseErr records why the binary lease was not acquired when it
	// is nil, so eligibility can distinguish ownership contention from
	// other failures.
	updateLeaseErr error
	// admission is the runtime work-admission boundary: one instance shared
	// by the orchestrator, repository work, and the HTTP server.
	admission *workadmission.Coordinator
	// supervisor owns the supervisor conversation for a serving runtime;
	// nil outside `agentico server`.
	supervisor *supervisor.Coordinator
}

// serverUpdates returns the raw server.updates config values, tolerating a
// missing config so policy resolution still runs with defaults.
func (b *runtimeBootstrap) serverUpdates() config.ServerUpdatesConfig {
	if b == nil || b.cfg == nil {
		return config.ServerUpdatesConfig{}
	}
	return b.cfg.Server.Updates
}

// StopServices stops the fx graph without releasing the instance lock: an
// exec handoff needs service cleanup to complete while the lock is still
// held. Idempotent.
func (b *runtimeBootstrap) StopServices(ctx context.Context) error {
	if b == nil {
		return nil
	}
	var errStop error
	if b.fxApp != nil {
		errStop = b.fxApp.Stop(ctx)
		b.fxApp = nil
	}
	return errStop
}

// ReleaseLock closes the instance lock only. Idempotent.
func (b *runtimeBootstrap) ReleaseLock() error {
	if b == nil {
		return nil
	}
	var errLock error
	if b.lock != nil {
		errLock = b.lock.Close()
		b.lock = nil
	}
	return errLock
}

// Close stops services, releases the update lease, then releases the
// instance lock — in that order, so the lock outlives the fx graph and no
// later lock holder can observe a half-torn runtime. The lease close never
// removes files. Idempotent.
func (b *runtimeBootstrap) Close(ctx context.Context) error {
	if b == nil {
		return nil
	}
	errStop := b.StopServices(ctx)
	var errLease error
	if b.updateLease != nil {
		errLease = b.updateLease.Close()
		b.updateLease = nil
	}
	errLock := b.ReleaseLock()
	return errors.Join(errStop, errLease, errLock)
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

func bootstrapRuntime(ctx context.Context, configPath, stateDir string, dangerouslySkipPerms bool, enabledProviders []string, refreshModels bool, stderr io.Writer) (*runtimeBootstrap, error) {
	stateDir = canonicalizeStateDir(stateDir)
	runtimeDir := filepath.Dir(stateDir)
	lock, acquired, owner, err := instancelock.Acquire(runtimeDir, stateDir, configPath, buildinfo.Version())
	if err != nil {
		return nil, &runtimeInitError{fmt.Errorf("acquiring instance lock: %w", err)}
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
	// Best-effort binary-scoped update ownership. Any failure — including
	// contention with the true owner (a secondary runtime serving from the
	// same installed binary) — leaves the lease nil and the launch
	// untouched: inability to own updates must not break boot or weaken the
	// instance lock. The lease fd is close-on-exec, so ordinary children
	// never inherit it.
	if exec, execErr := selfupdate.CaptureExecutable(); execErr == nil {
		boot.selfUpdateExec = exec
		if lease, leaseErr := selfupdate.AcquireLease(exec, selfupdate.OwnershipRecord{
			RuntimeDir:       runtimeDir,
			StateDir:         stateDir,
			Config:           configPath,
			PID:              os.Getpid(),
			PGID:             owner.PGID,
			Version:          buildinfo.Version(),
			StartedAt:        owner.StartedAt,
			ExecutablePath:   exec.Path,
			ExecutableDigest: exec.Digest,
			ExecutableIno:    exec.ID.Ino,
		}); leaseErr == nil {
			boot.updateLease = lease
		} else {
			boot.updateLeaseErr = leaseErr
		}
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
	// The runtime work-admission boundary is shared by orchestration,
	// repository work, and the HTTP surface; one instance is supplied to
	// the fx graph and reused for the server construction below.
	admission := workadmission.New(workadmission.Options{})
	boot.admission = admission

	var fm *feature.Manager
	var sm *session.Manager
	var orch *orchestrator.Orchestrator
	var registry *llm.Registry
	var cfg *config.Config
	var phaseRunner *agent.PhaseRunner
	var observer *observe.Observer
	var permissionCache *permission.Cache
	var worktrees feature.WorktreeOps
	providerModules, err := providerFxModules(enabledProviders)
	if err != nil {
		return nil, &runtimeInitError{err}
	}
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
		fx.Options(providerModules...),
		agent.Module,
		orchestrator.Module,
		fx.Populate(&fm, &sm, &orch, &registry, &cfg, &phaseRunner, &observer, &permissionCache, &worktrees),
		fx.NopLogger,
	)
	boot.fxApp = fxApp
	if err := fxApp.Start(ctx); err != nil {
		return nil, &runtimeInitError{fmt.Errorf("initializing: %w", err)}
	}
	// The work-admission boundary is installed on the fx-built orchestrator
	// before any serving or dispatch path can run.
	orch.SetAdmissionBoundary(admission)

	detected, warnings, startupNotices, availabilityFiltered, err := checkRequiredProviders(ctx, registry)
	if err != nil {
		// Setup-capable mode: the headless server stays reachable when no
		// provider CLI is installed or authenticated so a first-launch client
		// can drive remediation through /api/v1/readiness and re-probe with
		// /api/v1/readiness/refresh instead of requiring a new runtime. Model
		// routing is restricted to nothing until a readiness refresh finds a
		// usable provider; feature creation is gated server-side meanwhile.
		renderError(stderr, errcat.ProviderUnavailable,
			errcat.WithParams(errcat.ProviderUnavailableParams{SetupCapable: true}),
			errcat.WithDiagnostics(err.Error()),
		)
		registry.RestrictToProviders(nil)
		detected, warnings, startupNotices, availabilityFiltered = nil, nil, nil, true
	}
	renderStartupWarnings(stderr, warnings)

	toolHard, toolSoft := agent.CheckRequiredTools()
	for _, issue := range toolSoft {
		startupWarning{code: issue.Code, params: issue.Params, diagnostics: issue.Diagnostics}.render(stderr)
	}
	if len(toolHard) > 0 {
		return nil, &toolStartupError{issues: toolHard}
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
		renderError(stderr, errcat.AssetsReconcileFailed,
			errcat.WithDiagnostics(fmt.Sprintf("could not reconcile skills: %v", err)))
	} else {
		phaseRunner.SkillsDir = skillsDir
	}
	if err := guidelinedef.ReconcileGuidelines(guidelinesDir); err != nil {
		stop()
		stop = func() {}
		renderError(stderr, errcat.AssetsReconcileFailed,
			errcat.WithDiagnostics(fmt.Sprintf("could not reconcile guidelines: %v", err)))
	} else {
		phaseRunner.GuidelinesDir = guidelinesDir
	}
	stop()

	for _, vr := range agent.CheckProviderVersions(detected) {
		switch {
		case vr.Err != nil:
			renderError(stderr, errcat.ProviderVersionCheckFailed,
				errcat.WithParams(errcat.ProviderIssueParams{Provider: vr.Provider}),
				errcat.WithDiagnostics(fmt.Sprintf("could not check %s CLI version: %v", vr.Provider, vr.Err)))
		case vr.Warning != "":
			renderError(stderr, errcat.ProviderVersionCheckFailed,
				errcat.WithParams(errcat.ProviderIssueParams{Provider: vr.Provider}),
				errcat.WithDiagnostics(vr.Warning))
		default:
			fmt.Fprintf(stderr, "%s CLI version: %s\n", vr.Provider, vr.Version)
		}
	}

	modelDiscovery := newModelDiscoveryProgressPrinter(stderr)
	catalogWarnings := discoverProviderCatalogs(ctx, detected, runtimeDir, modelDiscovery.Report, refreshModels)
	modelDiscovery.Done()
	renderStartupWarnings(stderr, catalogWarnings)

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
	if recoveryScanOK && fm != nil {
		var busyIDs []string
		for _, item := range recoveryItems {
			if item.Feature != nil {
				busyIDs = append(busyIDs, item.Feature.ID)
			}
		}
		go agent.PruneStaleReviewScratch(fm, busyIDs)
	}
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
		renderError(stderr, errcat.StartupMaintenanceFailed,
			errcat.WithDiagnostics(fmt.Sprintf("startup recovery scan: %v", err)))
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

// runServer is the server body. Ordinary builds adopt inherited update
// handoffs and resolve interrupted transactions as production behavior;
// driver builds (agentico_selfupdate_driver tag) additionally install
// failure-injection and barrier seams.
func runServer(configPath, stateDir string, dangerouslySkipPerms bool, enabledProviders []string, refreshModels bool, listenAddr, serverName, updatesPolicy string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runtimeCtx, requestShutdown := context.WithCancel(ctx)
	defer requestShutdown()

	id := resolveLaunchIdentity(stateDir, configPath, listenAddr)
	if id.exit {
		return id.code
	}
	listenAddr = id.listenAddr

	// Driver-only deterministic abort for a recovered or restarted image's
	// own startup: the chain guard already forbids another automatic
	// recovery, so this terminates nonzero. Nil (no-op) in ordinary builds.
	if selfUpdateStartupAbortHook != nil {
		if err := selfUpdateStartupAbortHook(); err != nil {
			renderStartupFailure(os.Stderr, &runtimeInitError{err})
			return 1
		}
	}

	// The adopted target boots under a cooperative startup deadline: expiry
	// routes through the recovery boundary like any other startup failure.
	bootCtx := ctx
	var cancelBootCtx context.CancelFunc
	if id.adopted != nil {
		bootCtx, cancelBootCtx = context.WithTimeout(ctx, startupDeadline())
		defer cancelBootCtx()
	}

	// The one-shot recovery boundary for this launch chain. Every handled
	// failure of the adopted target's startup — configuration/bootstrap
	// errors, cooperative deadline expiry, preserved-endpoint bind failure,
	// health failure, mandatory discovery publication failure, confirmation
	// persistence failure, and direct target-exec failure — funnels here:
	// tear down, restore the previous build, and exec it once with the chain
	// guard. Ordinary launches keep their existing failure behavior.
	rt := failedTargetRecovery{
		lease:      id.adopted,
		receipt:    id.adoptedReceipt,
		exec:       selfupdate.Executable{Path: id.adoptedHandoff.ExecutablePath},
		runtimeDir: id.adoptedHandoff.RuntimeDir,
		bind:       id.adoptedReceipt.Bind,
		reason:     fmt.Sprintf("target %s failed to return to service; previous build %s restored", id.adoptedReceipt.ToVersion, id.adoptedReceipt.FromVersion),
	}
	targetStartupFailure := func(render func(error), err error) int {
		if id.adopted == nil {
			render(err)
			return 1
		}
		rt.reason = fmt.Sprintf("target %s failed to return to service: %v", id.adoptedReceipt.ToVersion, err)
		return rt.recover()
	}

	boot, err := bootstrapRuntime(bootCtx, configPath, stateDir, dangerouslySkipPerms, enabledProviders, refreshModels, os.Stderr)
	if err != nil {
		return targetStartupFailure(func(e error) { renderStartupFailure(os.Stderr, e) }, err)
	}
	defer func() {
		if err := boot.Close(context.Background()); err != nil {
			reportDeferredClose(os.Stderr, "close runtime", err)
		}
	}()
	if id.adopted != nil {
		// bootstrapRuntime's own lease acquisition reports contention here:
		// the adopted open file description already holds the binary-scoped
		// flock, so its fresh non-blocking attempt cannot succeed. The
		// adopted lease is this process's true owner.
		boot.updateLease = id.adopted
	} else if id.recoveryLease != nil {
		// Recovery resolution acquired the lease before bootstrap: ownership
		// stays continuous for this process's lifetime.
		boot.updateLease = id.recoveryLease
	}
	if rt.boot == nil {
		rt.boot = boot
	}

	// Release-availability startup configuration resolves after mandatory
	// recovery and bootstrap, from the captured executable identity and this
	// runtime's ownership lease.
	updateSettings, updateErr := selfupdate.ResolveStartupSettings(selfupdate.SettingsSources{
		Flag:                updatesPolicy,
		Env:                 os.Getenv("AGENTICO_UPDATES"),
		ConfigPolicy:        boot.serverUpdates().Policy,
		ConfigChannel:       boot.serverUpdates().Channel,
		ConfigCheckInterval: boot.serverUpdates().CheckInterval,
		ConfigStrategy:      boot.serverUpdates().Strategy,
		ConfigWindow:        boot.serverUpdates().Window,
	})
	if updateErr != nil {
		return targetStartupFailure(func(e error) {
			renderError(os.Stderr, errcat.UpdateConfigInvalid, errcat.WithParams(errcat.UsageParams{Reason: updateErr.Error()}))
		}, updateErr)
	}
	eligibility := classifyRuntimeEligibility(boot)
	wiring := buildUpdateOptions(boot, id, updateSettings, eligibility)

	if shouldInterruptRunningOnStartup(
		boot.recoveryScanOK,
		len(boot.recoveryItems),
		len(boot.sessionManager.ActiveSessions()),
	) {
		if err := boot.orchestrator.InterruptAllRunning(); err != nil {
			renderError(os.Stderr, errcat.StartupMaintenanceFailed,
				errcat.WithDiagnostics(fmt.Sprintf("startup sweep: %v", err)))
		}
	}

	listen, err := serverruntime.ResolveListen(listenAddr)
	if err != nil {
		return targetStartupFailure(func(e error) { renderStartupFailure(os.Stderr, &serverStartError{e}) }, err)
	}
	networkBind := listen.Policy == serverruntime.CompatibilityNetworkRuntimePolicy
	boot.phaseRunner.CapabilityPolicy = resolveCapabilityPolicy(boot.cfg, boot.runtime.RuntimeDir, networkBind)
	initialReviewerModel := boot.cfg.Defaults.Models.AutomaticReview
	boot.phaseRunner.CurrentAutomaticReviewModel = func() string {
		if current, err := config.Load(boot.runtime.Config); err == nil {
			return current.Defaults.Models.AutomaticReview
		}
		return initialReviewerModel
	}

	policy := runtimeLaunchPolicy(boot.registry, dangerouslySkipPerms)
	discoveryClient := &http.Client{Timeout: time.Second}
	decision, err := serverruntime.PrepareDiscovery(bootCtx, boot.runtime.RuntimeDir, boot.runtime, policy, discoveryClient, networkBind)
	if err != nil {
		return targetStartupFailure(func(e error) {
			renderStartupFailure(os.Stderr, &runtimeInitError{fmt.Errorf("validating discovery metadata: %w", e)})
		}, err)
	}
	if decision.AlreadyRunning {
		if id.adopted != nil {
			// The target could not take over its own runtime: recover.
			rt.reason = "target startup found the runtime already served by another process"
			return rt.recover()
		}
		renderStartupFailure(os.Stderr, alreadyRunningError{baseURL: decision.Record.BaseURL})
		return 1
	}
	authToken, err := serverruntime.EnsureAuthToken(boot.runtime.RuntimeDir)
	if err != nil {
		return targetStartupFailure(func(e error) {
			renderStartupFailure(os.Stderr, &runtimeInitError{fmt.Errorf("preparing server auth token: %w", e)})
		}, err)
	}
	var configName string
	if boot.cfg != nil {
		configName = boot.cfg.Server.Name
	}
	resolvedName, err := serverruntime.ResolveServerName(serverName, configName, boot.runtime.RuntimeDir)
	if err != nil {
		return targetStartupFailure(func(e error) {
			renderStartupFailure(os.Stderr, &runtimeInitError{fmt.Errorf("resolving server name: %w", e)})
		}, err)
	}

	supervisorCoordinator, err := newSupervisorCoordinator(boot)
	if err != nil {
		return targetStartupFailure(func(e error) {
			renderStartupFailure(os.Stderr, &runtimeInitError{fmt.Errorf("preparing supervisor state: %w", e)})
		}, err)
	}
	boot.supervisor = supervisorCoordinator

	runtimeServer, err := serverruntime.Start(bootCtx, serverruntime.Options{
		Runtime:      boot.runtime,
		LaunchPolicy: policy,
		StartMode:    cliSubcommandServer,
		Owner:        boot.owner,
		AuthToken:    authToken,
		ListenAddr:   listenAddr,
		Name:         resolvedName,
		Features:     boot.featureManager,
		FeatureStore: boot.featureManager.Store,
		Freshness:    newGitFreshnessProvider(),
		Config:       boot.cfg,
		Registry:     boot.registry,
		Sessions:     boot.sessionManager,
		Events:       boot.eventCh,
		DomainEvents: boot.orchestrator.Events(),
		Mutations: mutations.New(mutations.Deps{
			Orchestrator:    boot.orchestrator,
			Sessions:        boot.sessionManager,
			PermissionCache: boot.permissionCache,
			Config:          boot.cfg,
			ConfigPath:      boot.runtime.Config,
		}),

		PersistProviderModelCatalog: func(provider llm.LLMProvider, models []llm.ModelInfo) error {
			return persistRefreshedProviderModelCatalog(boot.runtime.RuntimeDir, provider, models)
		},
		Worktrees:   boot.worktrees,
		Updates:     wiring.options,
		Admission:   boot.admission,
		Lifetime:    ctx,
		HTTPMetrics: boot.observer,
		Supervisor:  supervisorCoordinator,
	})
	if err != nil {
		return targetStartupFailure(func(e error) {
			renderStartupFailure(os.Stderr, &serverStartError{fmt.Errorf("starting server: %w", e)})
		}, err)
	}
	rt.server = runtimeServer
	rt.authToken = authToken
	// Complete the install lifecycle's server handle now that the server is
	// running: install requests arriving through HTTP find it ready.
	installLifecycle := wiring.lifecycle
	installLifecycle.setRun(&serverRun{boot: boot, server: runtimeServer, authToken: authToken})
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := runtimeServer.Close(shutdownCtx); err != nil {
			reportDeferredClose(os.Stderr, "close server", err)
		}
	}()

	now := time.Now().UTC()
	record := registryRecord(boot, runtimeServer, authToken, resolvedName, policy, now)
	if err := publishDiscoveryFn(boot.runtime.RuntimeDir, record); err != nil {
		return targetStartupFailure(func(e error) {
			renderStartupFailure(os.Stderr, &serverStartError{fmt.Errorf("publishing discovery metadata: %w", e)})
		}, err)
	}

	// The central registry entry is a verbatim copy of the published
	// discovery record under the resolved runtime parent's servers/ dir.
	// Publication failure never affects serving: the server stays fully
	// attachable via its per-runtime discovery file.
	registryDir := serverruntime.RegistryDir(resolveRegistryParent())
	rt.registryDir = registryDir
	installLifecycle.setRegistryDir(registryDir)
	if err := serverruntime.PublishRegistryEntry(registryDir, record); err != nil {
		renderError(os.Stderr, errcat.StartupMaintenanceFailed,
			errcat.WithDiagnostics(fmt.Sprintf("publishing server registry entry: %v", err)))
	} else {
		defer func() {
			if err := serverruntime.RemoveRegistryEntry(registryDir, boot.runtime.RuntimeDir); err != nil {
				renderError(os.Stderr, errcat.StartupMaintenanceFailed,
					errcat.WithDiagnostics(fmt.Sprintf("removing server registry entry: %v", err)))
			}
		}()
	}

	if code, stopLaunch := confirmAdoptedStartup(id, &rt, runtimeServer); stopLaunch {
		return code
	}

	fmt.Fprintf(os.Stderr, "Agentic server %q listening at %s\n", resolvedName, runtimeServer.BaseURL())
	if id.restoredRollback != nil {
		// The restored runtime reports the consumed update result as the
		// canonical update_rolled_back failure while continuing to serve on
		// the previous build.
		renderError(os.Stderr, errcat.UpdateRolledBack,
			errcat.WithParams(errcat.UpdateRolledBackParams{
				FromVersion: id.restoredRollback.FromVersion,
				ToVersion:   id.restoredRollback.ToVersion,
			}),
			errcat.WithDiagnostics(id.restoredRollback.Error))
	}
	if err := writeNetworkAccessNotice(os.Stderr, runtimeServer.RuntimePolicy(), runtimeServer.BaseURL(), runtimeServer.WildcardBind(), authToken, resolvedName); err != nil {
		renderError(os.Stderr, errcat.StartupMaintenanceFailed,
			errcat.WithDiagnostics(fmt.Sprintf("writing network access notice: %v", err)))
	}
	var journey serverJourney
	if serverJourneyHook != nil && id.adopted == nil {
		journey = serverJourneyHook()
	}
	if journey != nil {
		run, err := installLifecycle.current()
		if err != nil {
			return 1
		}
		if code := journey.run(run); code >= 0 {
			return code
		}
	}
	// A failed recovery boundary (second failure after teardown) hands its
	// termination code to this serving goroutine: the process exits without
	// an exec loop and without os.Exit from the install worker.
	select {
	case <-runtimeCtx.Done():
	case code := <-installLifecycle.exitCode:
		return code
	}
	shutdownRuntimeWork(boot)
	return 0
}

// newSupervisorCoordinator loads the supervisor conversation for this
// server without launching anything. The provider runs in the workspace
// root, else the state directory.
func newSupervisorCoordinator(boot *runtimeBootstrap) (*supervisor.Coordinator, error) {
	stateDir := boot.phaseRunner.StateDir
	workDir := boot.workspaceDir
	if workDir == "" {
		workDir = stateDir
	}
	return supervisor.New(supervisor.Options{
		StateDir: stateDir,
		WorkDir:  workDir,
		Catalog:  supervisor.RegistryCatalog{Registry: boot.registry},
		Launcher: &supervisor.SessionLauncher{
			Runner:        boot.phaseRunner,
			Sessions:      boot.sessionManager,
			RuntimeDir:    boot.runtime.RuntimeDir,
			ConfigPath:    boot.runtime.Config,
			DiscoveryPath: serverruntime.DiscoveryPath(boot.runtime.RuntimeDir),
		},
		Admission:  boot.admission,
		Converters: supervisorConverters(boot.registry),
	})
}

// supervisorConverters restores history for every harness: Claude and Codex
// resume a rebuilt native session, OpenCode receives a history seed sized
// from the registry's model catalog.
func supervisorConverters(registry *llm.Registry) map[string]supervisor.Converter {
	return map[string]supervisor.Converter{
		claudesession.Harness: claudesession.New(claudesession.Options{}),
		codexsession.Harness:  codexsession.New(codexsession.Options{}),
		opencodesession.Harness: opencodesession.New(opencodesession.Options{
			ContextWindow: opencodesession.RegistryContextWindow(registry),
		}),
	}
}

// shutdownRuntimeWork ends the supervisor (refusing any relaunch) and then
// stops features and sessions.
func shutdownRuntimeWork(boot *runtimeBootstrap) {
	if boot == nil {
		return
	}
	if boot.supervisor != nil {
		_ = boot.supervisor.Close()
	}
	shutdownFeatures(boot.orchestrator, boot.sessionManager)
}

// launchIdentity is the update-launch state resolved before bootstrap: the
// adopted inherited handoff when this process is the exec'd target of a
// replacement, otherwise the boot-time recovery resolution of an operator
// launch.
type launchIdentity struct {
	// exit is true when the launch must stop with code (a rendered refusal;
	// a restore decision execs and never returns).
	exit bool
	code int
	// adopted is the inherited lease of the exec'd replacement target, with
	// its validated handoff metadata and receipt.
	adopted        *selfupdate.Lease
	adoptedHandoff selfupdate.HandoffMetadata
	adoptedReceipt selfupdate.Receipt
	// recoveryLease is the binary lease acquired during boot-time recovery
	// resolution, carried into the ordinary boot so ownership stays
	// continuous for the process lifetime.
	recoveryLease *selfupdate.Lease
	// restoredRollback marks that this launch resolved (or finished) an
	// actual rollback and should report the canonical update_rolled_back
	// failure for the consumed update result.
	restoredRollback *selfupdate.Receipt
	// recoveredChain marks that this launch carries the recovery guard: it
	// is the recovered or restarted image of a prior chain, so its own
	// startup is held to the same health-wait bar and a further automatic
	// recovery is forbidden.
	recoveredChain bool
	// listenAddr is the effective listen address after the adopted target's
	// or the recovered build's recorded endpoint rebind.
	listenAddr string
}

// resolveLaunchIdentity resolves the launch's update identity before general
// bootstrap: handoff adoption for the exec'd replacement target, mandatory
// recovery resolution for every other launch. Adoption precedes general
// bootstrap so no child process can spawn before the inherited lease
// descriptor is validated and made close-on-exec again; a present-but-invalid
// handoff fails the launch closed. Recovery runs before configuration
// loading, fx construction, bootstrap children, update-policy selection, and
// any ownership-record overwrite: an unsafe record refuses nonzero with a
// sanitized diagnostic; absence of a transaction preserves ordinary startup,
// including serving without update ownership.
func resolveLaunchIdentity(stateDir, configPath, listenAddr string) launchIdentity {
	id := launchIdentity{listenAddr: listenAddr}
	lease, meta, receipt, err := adoptHandoffForLaunch()
	if err != nil {
		renderStartupFailure(os.Stderr, &runtimeInitError{fmt.Errorf("adopting update handoff: %w", err)})
		return launchIdentity{exit: true, code: 1}
	}
	id.adopted, id.adoptedHandoff, id.adoptedReceipt = lease, meta, receipt
	if id.adopted != nil && selfUpdateAdoptedStartHook != nil {
		selfUpdateAdoptedStartHook()
	}
	if id.adopted == nil {
		res := resolveRecoveryOnBoot(stateDir, configPath, id.listenAddr)
		if res.exit {
			return launchIdentity{exit: true, code: res.code}
		}
		id.recoveryLease = res.lease
		id.restoredRollback = res.restoredRollback
		if res.listenOverride != "" {
			id.listenAddr = res.listenOverride
			id.recoveredChain = true
		}
		if res.recoveredChain {
			id.recoveredChain = true
		}
		return id
	}
	if override := handoffListenAddr(id.adoptedHandoff); override != "" {
		// The adopted target rebinds its recorded concrete endpoint; argv's
		// --listen may still name an ephemeral port.
		id.listenAddr = override
	}
	return id
}

// updateWiring bundles the resolved update subsystem for the serving
// runtime: the update coordinator's options and the production install
// lifecycle that carries authenticated install requests.
type updateWiring struct {
	options   serverruntime.UpdateOptions
	lifecycle *productionInstallLifecycle
}

// buildUpdateOptions resolves the update subsystem after bootstrap and
// recovery: the startup receipt exposed to clients, the release feed, the
// coordinator options, and the production install lifecycle (wired only when
// a real feed client exists).
func buildUpdateOptions(boot *runtimeBootstrap, id launchIdentity, settings selfupdate.StartupSettings, eligibility selfupdate.Eligibility) updateWiring {
	var startupUpdateReceipt *selfupdate.Receipt
	switch {
	case id.restoredRollback != nil:
		startupUpdateReceipt = id.restoredRollback
	case id.adopted != nil:
		receiptCopy := id.adoptedReceipt
		startupUpdateReceipt = &receiptCopy
	}
	var updateFeed serverruntime.FeedChecker
	if updateFeedHook != nil {
		// Driver-only fixture routing: deliberately built test binaries
		// inject a local metadata feed that receives no credentials.
		updateFeed = updateFeedHook()
	} else {
		slug, _ := moduleSlug()
		updateFeed = selfupdate.NewProductionFeedClient(slug, githubToken)
	}
	options := serverruntime.UpdateOptions{
		Policy:         settings.Policy,
		Settings:       settings,
		CurrentVersion: buildinfo.Version(),
		Eligibility:    eligibility,
		ExecPath:       boot.selfUpdateExec.Path,
		StartupReceipt: startupUpdateReceipt,
		Feed:           updateFeed,
		Log:            func(line string) { fmt.Fprintln(os.Stderr, line) },
		Observer:       boot.observer,
		Admission:      boot.admission,
	}
	// The production install path: the same signed-release pipeline and
	// recoverable replacement the driver journeys prove, reachable only
	// through the authenticated install endpoint. The lifecycle's server
	// handle is completed after the server starts; install requests can
	// only arrive after that point.
	lifecycle := &productionInstallLifecycle{
		exitCode: make(chan int, 1),
		stager: &productionReleaseStager{
			exec:           boot.selfUpdateExec,
			currentVersion: buildinfo.Version(),
			eligibility:    eligibility,
		},
	}
	if feedClient, ok := updateFeed.(*selfupdate.FeedClient); ok {
		lifecycle.stager.feed = feedClient
	}
	if lifecycle.stager.feed != nil {
		options.Stager = lifecycle.stager
		options.Install = lifecycle
	}
	// Test-only seams for the tagged driver's explicit-stop journeys:
	// ordinary builds keep every hook nil so production installs use the
	// fixed stop budget, never pause before the protected-work recheck,
	// and never inject stop failures.
	if updateStopWorkTimeoutHook != nil {
		if budget := updateStopWorkTimeoutHook(); budget > 0 {
			options.StopWorkTimeout = budget
		}
	}
	if updateStopEntryGateHook != nil {
		options.StopEntryGate = updateStopEntryGateHook()
	}
	if updateStopFeatureFailureHook != nil {
		options.StopFeatureHook = updateStopFeatureFailureHook()
	}
	if updateStopDetectionFailHook != nil {
		options.DetectFailHook = updateStopDetectionFailHook()
	}
	return updateWiring{options: options, lifecycle: lifecycle}
}

// confirmAdoptedStartup gates the adopted target's transition to serving:
// the mandatory self-health wait (a recovered build's own startup is held to
// the same bar), then the write-once confirmation, the public receipt
// exposure, and the settled cleanup. It returns the exit code and true when
// the launch must stop; false means serving may proceed.
func confirmAdoptedStartup(id launchIdentity, rt *failedTargetRecovery, runtimeServer *serverruntime.RuntimeServer) (int, bool) {
	// No ordinary work is admitted before full bootstrap, the mandatory
	// discovery publication, and — for the adopted target and the recovered
	// build — the self-health wait.
	if id.adopted == nil && !id.recoveredChain {
		return 0, false
	}
	if err := waitSelfHealthy(selfHealthProbeURLs(runtimeServer), 30*time.Second); err != nil {
		if id.adopted != nil {
			rt.reason = fmt.Sprintf("target %s failed its health wait: %v", id.adoptedReceipt.ToVersion, err)
			return rt.recover(), true
		}
		// The recovered build's own startup failure terminates nonzero: the
		// chain guard forbids another automatic recovery.
		renderStartupFailure(os.Stderr, &serverStartError{fmt.Errorf("recovered build health wait: %w", err)})
		return 1, true
	}
	if id.adopted == nil {
		return 0, false
	}
	// Confirmation is write-once and only happens after full bootstrap,
	// health wait, and discovery publication. A failure to persist
	// confirmation stays unconfirmed and enters the recovery boundary
	// when safe.
	confirmedReceipt, err := confirmAdoptedTransaction(id.adoptedHandoff.ExecutablePath, id.adoptedHandoff.TransactionID)
	if err != nil {
		rt.reason = fmt.Sprintf("target %s could not durably confirm its update: %v", id.adoptedReceipt.ToVersion, err)
		return rt.recover(), true
	}
	// The public update snapshot now exposes the confirmed outcome and its
	// sanitized receipt.
	runtimeServer.SetUpdateReceipt(confirmedReceipt)
	// Cleanup failure retains the healthy target and the confirmed outcome:
	// warn and keep serving; a later launch retries the validated cleanup.
	if err := selfupdate.CleanupSettledTransaction(id.adoptedHandoff.ExecutablePath, id.adoptedHandoff.TransactionID, selfUpdateCleanupSeams); err != nil {
		renderError(os.Stderr, errcat.StartupMaintenanceFailed,
			errcat.WithDiagnostics(fmt.Sprintf("selfupdate cleanup for confirmed transaction %s: %v", id.adoptedHandoff.TransactionID, err)))
	}
	fmt.Fprintf(os.Stderr, "selfupdate: confirmed %s\n", id.adoptedHandoff.TransactionID)
	return 0, false
}

// writeNetworkAccessNotice prints the network-bind security notice and the
// one-line connection string after the "listening at" line. Loopback servers
// print nothing — their startup output is byte-for-byte unchanged.
func writeNetworkAccessNotice(w io.Writer, runtimePolicy, baseURL string, wildcard bool, authToken, resolvedName string) error {
	if runtimePolicy != serverruntime.CompatibilityNetworkRuntimePolicy {
		return nil
	}
	connStr, err := serverruntime.ConnectionStringFromBaseURL(baseURL, authToken, resolvedName)
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "SECURITY NOTICE: this server is reachable over the network with plain HTTP plus a bearer token.")
	fmt.Fprintln(w, "Anyone with the connection string below has full access to this machine's agentic runtime.")
	fmt.Fprintln(w, "Expose it only on a trusted network (prefer a VPN or Tailscale); use SSH tunneling across")
	fmt.Fprintln(w, "untrusted links.")
	if wildcard {
		fmt.Fprintf(w, "Listening on all interfaces; advertising the primary interface address %s.\n", baseURL)
	}
	fmt.Fprintf(w, "Connection string: %s\n", connStr)
	return nil
}

// registryRecord builds the shared publish record: the same value is written
// to the per-runtime discovery file and (verbatim, token included) to the
// central server registry.
func registryRecord(boot *runtimeBootstrap, runtimeServer *serverruntime.RuntimeServer, authToken, resolvedName string, policy serverruntime.LaunchPolicy, now time.Time) serverruntime.DiscoveryRecord {
	return serverruntime.DiscoveryRecord{
		SchemaVersion: 1,
		APIVersion:    serverruntime.APIVersion,
		BaseURL:       runtimeServer.BaseURL(),
		Epoch:         runtimeServer.EventEpoch(),
		AuthToken:     authToken,
		Name:          resolvedName,
		Runtime:       boot.runtime,
		LaunchPolicy:  policy,
		StartMode:     cliSubcommandServer,
		PID:           boot.owner.PID,
		PGID:          boot.owner.PGID,
		StartedAt:     runtimeServer.StartedAt(),
		PublishedAt:   now,
		Owner:         serverruntime.OwnerFromInstanceOwner(boot.owner),
	}
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
	fallbacks := registry.CatalogDefaultModels()
	// Preserve compatibility with providers that only advertise model IDs.
	hasCatalog := false
	for _, provider := range registry.DetectedProviders() {
		if cp, ok := provider.(llm.CatalogProvider); ok && len(cp.ModelCatalog()) > 0 {
			hasCatalog = true
			break
		}
	}
	remap := func(field *string, fallback string) {
		if fallback == "" && !hasCatalog {
			if models := registry.AvailableModels(); len(models) > 0 {
				fallback = models[0]
			}
		}
		if fallback == "" {
			return
		}
		if *field == "" {
			*field = fallback
		} else if _, _, err := registry.ResolveModel(*field); err != nil {
			*field = fallback
		}
	}
	remap(&m.Inquiry, fallbacks.Inquiry)
	remap(&m.Research, fallbacks.Research)
	remap(&m.Planning, fallbacks.Planning)
	remap(&m.Implementation, fallbacks.Implementation)
	remap(&m.Review, fallbacks.Review)
	remap(&m.Utilities, fallbacks.Utilities)
	remap(&m.KBBuild, fallbacks.KBBuild)
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
//
// The CLI path validates the provider list ahead of runtime construction, so
// this function never exits the process; a caller that reaches it with no
// valid names gets an error to render through the normal exit-code path.
func providerFxModules(enabled []string) ([]fx.Option, error) {
	all := map[string]fx.Option{
		providerNameClaude:   claude.Module,
		providerNameCodex:    codex.Module,
		providerNameOpencode: opencode.Module,
	}

	names := normalizeProviderNames(enabled, true, nil)

	if len(names) == 0 {
		return nil, fmt.Errorf("no valid providers specified in --providers flag")
	}

	modules := make([]fx.Option, 0, len(names))
	for _, name := range names {
		modules = append(modules, all[name])
	}

	return modules, nil
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
	if defaults.IsEmpty() {
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

// resolveCapabilityPolicy derives the built-in capability probe policy for
// this server. Servers exposed to the network deny authenticated browser
// state unless server.capabilities.browser_state explicitly allows it.
func resolveCapabilityPolicy(cfg *config.Config, runtimeDir string, networkBind bool) *agent.CapabilityPolicy {
	allow := !networkBind
	if cfg != nil {
		switch strings.ToLower(strings.TrimSpace(cfg.Server.Capabilities.BrowserState)) {
		case "allow":
			allow = true
		case "deny":
			allow = false
		}
	}
	return &agent.CapabilityPolicy{RuntimeDir: runtimeDir, AllowBrowserState: allow}
}
