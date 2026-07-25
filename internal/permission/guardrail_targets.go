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

package permission

import (
	"strings"
)

// classifyProjectTarget handles task runners, native generator dispatchers,
// npm/pnpm/Yarn scripts, and Git read-only inspection.
// Returns true if the segment is eligible.
func classifyProjectTarget(name string, seg *parsedSegment, workDir string, writableRoots []string) bool {
	switch name {
	case "make", "gmake":
		return classifyTargetCommand(seg, &makeTargetPolicy)
	case "just":
		return classifyTargetCommand(seg, &justTargetPolicy)
	case "task":
		return classifyTargetCommand(seg, &taskRunnerTargetPolicy)
	case "bazel", "bazelisk":
		return classifyBazelTarget(seg)
	case "gradlew":
		return classifyTargetCommand(seg, &gradlewTargetPolicy)
	case "mvnw":
		return classifyTargetCommand(seg, &mvnwTargetPolicy)
	case "npm", "pnpm", "yarn":
		return classifyPackageScript(seg)
	case "git":
		return classifyGit(seg, workDir, writableRoots)
	case "buf":
		return classifyBuf(seg, workDir, writableRoots)
	case "npx":
		return false // package executor — always defers
	case "dlx":
		return false // pnpm dlx — always defers
	default:
		return false
	}
}

// targetPolicy describes a task runner's flag rules for the shared
// target-command evaluator. Runners use strict mode: only explicitly
// listed safe flags are admitted, and all unknown flags defer. This
// prevents --flag=value bypasses (e.g., make --file=/tmp/evil.mk test)
// and ensures unknown runner flags cannot reach the reviewer.
type targetPolicy struct {
	safeFlags  map[string]bool
	valueFlags map[string]bool // flags that consume the next arg as a value
}

// classifyTargetCommand evaluates a task runner invocation against a
// targetPolicy. At least one non-flag argument must be an ordinary target
// name that passes classifyTargetComponents; safe runner flags alone are not
// enough because they may invoke a project-defined default target.
// Assignment-shaped operands defer before target matching because runners
// such as Make interpret them as variable overrides rather than targets.
// Flags are matched by name (with =value stripped) against the safe set;
// combined short flags (e.g., -j4) are recognized when the short prefix is a
// value flag. Unknown flags defer.
func classifyTargetCommand(seg *parsedSegment, policy *targetPolicy) bool {
	if len(seg.args) == 0 {
		return false
	}
	targetSeen := false
	for i := 0; i < len(seg.args); i++ {
		arg := seg.args[i]
		if strings.HasPrefix(arg, "-") {
			name := flagName(arg)
			value := flagValue(arg)
			if name == "--" {
				continue
			}
			// Handle combined short flags (e.g., -j4) where the short
			// prefix is a value flag.
			if value == "" && len(arg) > 2 && arg[0] == '-' && arg[1] != '-' {
				shortName := arg[:2]
				if policy.valueFlags[shortName] {
					name = shortName
					value = arg[2:]
				}
			}
			if !policy.safeFlags[name] {
				return false
			}
			if value != "" && isRunnerOverrideOperand(value) {
				return false
			}
			if value == "" && policy.valueFlags[name] {
				i++
				if i >= len(seg.args) {
					return false
				}
				if isRunnerOverrideOperand(seg.args[i]) {
					return false
				}
			}
			continue
		}
		if isRunnerOverrideOperand(arg) {
			return false
		}
		if !classifyTargetComponents(arg) {
			return false
		}
		targetSeen = true
	}
	return targetSeen
}

func isRunnerOverrideOperand(arg string) bool {
	return strings.Contains(arg, "=")
}

// makeTargetPolicy admits harmless make flags. File-selection and
// directory-change flags (-f, --file, -C, --directory, -I, etc.) are
// absent so they defer; they can load attacker-controlled makefiles.
var makeTargetPolicy = targetPolicy{
	safeFlags: map[string]bool{
		"-s": true, "--silent": true, "--quiet": true,
		"-k": true, "--keep-going": true,
		"-j": true, "--jobs": true,
		"-n": true, "--dry-run": true, "--just-print": true, "--recon": true,
		"-q": true, "--question": true,
		"-w": true, "--print-directory": true, "--no-print-directory": true,
		"--warn-undefined-variables": true,
		"--no-builtin-rules":         true, "--no-builtin-variables": true,
		"-B": true, "--always-make": true,
		"-l": true, "--load-average": true,
		"--shuffle": true, "--trace": true,
		"-S": true, "--no-keep-going": true,
		"--": true,
	},
	valueFlags: map[string]bool{
		"-j": true, "--jobs": true,
		"-l": true, "--load-average": true,
		"--shuffle": true,
	},
}

// justTargetPolicy admits harmless just flags. File-selection and
// shell-override flags (--justfile, --shell, --command, etc.) are absent.
var justTargetPolicy = targetPolicy{
	safeFlags: map[string]bool{
		"--list": true, "-l": true, "--summary": true,
		"--dry-run": true, "--quiet": true, "-q": true,
		"--": true,
	},
}

// taskRunnerTargetPolicy admits harmless Taskfile flags. File-selection
// and directory-override flags (--taskfile, --dir, etc.) are absent.
var taskRunnerTargetPolicy = targetPolicy{
	safeFlags: map[string]bool{
		"--list": true, "-l": true, "--summary": true,
		"--silent": true, "--verbose": true, "-v": true,
		"--color": true, "--no-color": true,
		"--dry": true,
		"--":    true,
	},
}

// gradlewTargetPolicy reuses the curated Gradle safe flags. Init-script
// and scripting-language flags are absent so they defer.
var gradlewTargetPolicy = targetPolicy{
	safeFlags:  gradleSafeFlags,
	valueFlags: map[string]bool{"-x": true, "--exclude-task": true},
}

// mvnwTargetPolicy reuses the curated Maven safe flags.
var mvnwTargetPolicy = targetPolicy{
	safeFlags:  mvnSafeFlags,
	valueFlags: map[string]bool{"-pl": true, "--projects": true, "-T": true, "--threads": true},
}

// classifyBazelTarget checks a Bazel invocation. Bazel subcommands like
// build/test are development verbs; run/query/coverage have their own rules.
func classifyBazelTarget(seg *parsedSegment) bool {
	if len(seg.args) == 0 {
		return false
	}
	sub := seg.args[0]
	if isProhibitedSubcommand(sub) || isGlobalLongRunningMode(sub) {
		return false
	}
	switch sub {
	case "build", "test", "coverage", "lint":
		return classifyBazelFlags(seg.args[1:], classifyBazelExecutableLabel)
	case "query", "cquery":
		return classifyBazelFlags(seg.args[1:], classifyBazelQueryOperand)
	case "run":
		return false // run executes a target — defers
	default:
		return false
	}
}

func classifyBazelFlags(args []string, classifyOperand func(string) bool) bool {
	operandSeen := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			if arg == "--" {
				continue
			}
			name := flagName(arg)
			if !bazelSafeFlags[name] {
				return false
			}
			if value := flagValue(arg); value != "" && !validateBazelFlagValue(name, value) {
				return false
			}
			if bazelValueFlags[name] && flagValue(arg) == "" {
				i++
				if i >= len(args) {
					return false
				}
				if !validateBazelFlagValue(name, args[i]) {
					return false
				}
			}
			continue
		}
		if !classifyOperand(arg) {
			return false
		}
		operandSeen = true
	}
	return operandSeen
}

// bazelSafeFlags lists explicitly safe Bazel flags. Opaque pass-through
// options (--copt, --cxxopt, --linkopt, --action_env, --host_action_env,
// --python_path, --define, --features, --test_arg, --test_env, --config,
// --disk_cache, --repository_cache) are absent because they can forward
// plugin-loading flags, select arbitrary executables, inject environment
// variables, expand repository-defined options, or select external caches that
// bypass the guardrail. Hazardous flags like --override_repository,
// --spawn_strategy, --strategy, --genrule_strategy, --profile, and
// --experimental_repository_resolved_file are also absent. Unknown flags defer
// (strict mode).
var bazelSafeFlags = map[string]bool{
	"--keep_going": true, "-k": true,
	"--jobs": true, "-j": true,
	"--stamp": true, "--nostamp": true,
	"--verbose_failures": true,
	"--test_output":      true, "--test_filter": true,
	"--color": true, "--curses": true,
	"--show_progress": true, "--show_timestamps": true,
	"--cpu": true, "--compilation_mode": true, "-c": true,
	"--collect_code_coverage": true,
	"--combined_report":       true,
	"--":                      true,
}

// bazelValueFlags lists Bazel flags that consume the next argument as a
// separate value.
var bazelValueFlags = map[string]bool{
	"--jobs": true, "-j": true,
	"--test_output": true, "--test_filter": true,
	"--cpu": true, "--compilation_mode": true, "-c": true,
}

func validateBazelFlagValue(name, value string) bool {
	if value == "" {
		return false
	}
	if valueMatchesSecretPattern(value) || containsSecretKeyword(value) {
		return false
	}
	switch name {
	case "--jobs", "-j":
		return isDecimalValue(value)
	case "--test_output":
		return stringInSet(value, "summary", "errors", "all", "streamed")
	case "--test_filter":
		return isBazelTextValue(value)
	case "--color", "--curses":
		return stringInSet(value, "yes", "no", "auto", "true", "false", "1", "0")
	case "--show_progress", "--show_timestamps", "--keep_going", "-k",
		"--stamp", "--nostamp", "--verbose_failures", "--collect_code_coverage":
		return stringInSet(value, "true", "false", "1", "0", "yes", "no")
	case "--cpu":
		return isBazelIdentifierValue(value)
	case "--compilation_mode", "-c":
		return stringInSet(value, "fastbuild", "dbg", "opt")
	case "--combined_report":
		return stringInSet(value, "lcov")
	default:
		return false
	}
}

func classifyBazelLabel(label string) bool {
	if label == "" || strings.HasPrefix(label, "-") || strings.HasPrefix(label, "@") {
		return false
	}
	if strings.HasPrefix(label, "//") {
		return validateBazelLabelBody(strings.TrimPrefix(label, "//"))
	}
	if strings.HasPrefix(label, ":") {
		return validateBazelLabelBody(strings.TrimPrefix(label, ":"))
	}
	return false
}

func classifyBazelExecutableLabel(label string) bool {
	if !classifyBazelLabel(label) {
		return false
	}
	body := label
	if strings.HasPrefix(body, "//") {
		body = strings.TrimPrefix(body, "//")
	} else {
		body = strings.TrimPrefix(body, ":")
	}
	return !bazelLabelHasProhibitedComponent(body)
}

func bazelLabelHasProhibitedComponent(body string) bool {
	for _, part := range strings.FieldsFunc(body, func(r rune) bool {
		return r == '/' || r == ':'
	}) {
		for _, component := range splitTargetComponents(strings.ToLower(part)) {
			if prohibitedTargetComponents[component] {
				return true
			}
		}
	}
	return false
}

func classifyBazelQueryOperand(expr string) bool {
	if classifyBazelLabel(expr) {
		return true
	}
	if expr == "" || strings.Contains(expr, "@") || valueMatchesSecretPattern(expr) {
		return false
	}
	for _, r := range expr {
		if !isBazelQueryRune(r) {
			return false
		}
	}
	hasLabel := false
	for _, token := range strings.FieldsFunc(expr, func(r rune) bool {
		switch r {
		case '(', ')', ',', ' ', '\t':
			return true
		default:
			return false
		}
	}) {
		if strings.HasPrefix(token, "//") || strings.HasPrefix(token, ":") {
			if !classifyBazelLabel(token) {
				return false
			}
			hasLabel = true
		}
	}
	return hasLabel
}

func validateBazelLabelBody(body string) bool {
	if body == "" {
		return false
	}
	if strings.HasPrefix(body, "/") || strings.Contains(body, "//") ||
		strings.Contains(body, "::") || strings.HasSuffix(body, ":") {
		return false
	}
	for _, r := range body {
		if !isBazelLabelRune(r) {
			return false
		}
	}
	for _, part := range strings.FieldsFunc(body, func(r rune) bool {
		return r == '/' || r == ':'
	}) {
		if part == "" {
			continue
		}
		if part == ".." || isSensitivePathComponent(part) {
			return false
		}
	}
	return true
}

func isBazelTextValue(value string) bool {
	for _, r := range value {
		if !(isBazelIdentifierRune(r) || r == '.' || r == ':' || r == '/' || r == '-' || r == '+' || r == '=') {
			return false
		}
	}
	return true
}

func isBazelIdentifierValue(value string) bool {
	for _, r := range value {
		if !(isBazelIdentifierRune(r) || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

func isBazelQueryRune(r rune) bool {
	return isBazelLabelRune(r) || r == '(' || r == ')' || r == ','
}

func isBazelLabelRune(r rune) bool {
	return isBazelIdentifierRune(r) || r == '.' || r == '/' || r == ':' || r == '-' || r == '+'
}

func isBazelIdentifierRune(r rune) bool {
	return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_'
}

func isDecimalValue(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func stringInSet(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

// classifyPackageScript checks npm/pnpm/yarn run <script> invocations.
// Only recognized named scripts and conventional test aliases are accepted;
// package mutation, exec, dlx, npx, lifecycle manipulation, pass-through
// arguments, and runner variable overrides defer.
func classifyPackageScript(seg *parsedSegment) bool {
	if len(seg.args) == 0 {
		return false
	}
	sub := seg.args[0]

	// Reject package mutation and executor forms
	switch sub {
	case "install", "update", "upgrade", "uninstall", "remove", "add",
		"publish", "deprecate", "unpublish",
		"exec", "dlx", "create",
		"cache", "config", "token", "login", "logout",
		"pack", "unlink", "link":
		return false
	}

	// npm/pnpm/yarn test — already handled by direct policy; if we reach here,
	// the subcommand was not in the direct policy table.
	if sub == "test" || sub == "t" || sub == "tst" {
		return classifyPackageScriptFlags(seg.args[1:])
	}

	// npm run <script> / npm run-script <script>
	if sub == "run" || sub == "run-script" {
		if len(seg.args) < 2 {
			return false
		}
		script := seg.args[1]
		// Reject lifecycle scripts and pass-through
		if isLifecycleScript(script) {
			return false
		}
		if !classifyTargetComponents(script) {
			return false
		}
		return classifyPackageScriptFlags(seg.args[2:])
	}

	return false
}

func classifyPackageScriptFlags(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if strings.HasPrefix(arg, "-") {
			name := flagName(arg)
			if !packageScriptSafeFlags[name] {
				return false
			}
			continue
		}
		return false
	}
	return true
}

// packageScriptSafeFlags lists explicitly safe npm/pnpm/yarn script flags.
// Hazardous flags like --prefix, --global, --registry, and --proxy are
// absent so they defer. Unknown flags also defer (strict mode).
var packageScriptSafeFlags = map[string]bool{
	"--silent": true, "--quiet": true, "--verbose": true,
	"--no-color": true, "--color": true,
}

// isLifecycleScript reports whether a script name is an npm lifecycle hook.
func isLifecycleScript(script string) bool {
	switch script {
	case "preinstall", "install", "postinstall",
		"prepublish", "prepare", "prepublishOnly", "prepack", "postpack",
		"publish", "postpublish",
		"prerestart", "restart", "postrestart",
		"preshrinkwrap", "shrinkwrap", "postshrinkwrap",
		"prestart", "start", "poststart",
		"prestop", "stop", "poststop",
		"pretest", "posttest",
		"preuninstall", "uninstall", "postuninstall",
		"preversion", "version", "postversion":
		return true
	default:
		return false
	}
}

// classifyBuf checks a buf invocation. All non-flag operands are validated
// for root bounds and sensitive components so external or sensitive paths
// cannot bypass the guardrail.
func classifyBuf(seg *parsedSegment, workDir string, writableRoots []string) bool {
	if len(seg.args) == 0 {
		return false
	}
	sub := seg.args[0]
	if isProhibitedSubcommand(sub) || isGlobalLongRunningMode(sub) {
		return false
	}
	switch sub {
	case "generate", "lint", "format", "build", "verify":
		return classifyBufFlags(seg.args[1:], workDir, writableRoots)
	default:
		return false
	}
}

func classifyBufFlags(args []string, workDir string, writableRoots []string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			if arg == "--" {
				continue
			}
			name := flagName(arg)
			if !bufSafeFlags[name] {
				return false
			}
			continue
		}
		if !validateOperand(arg, workDir, writableRoots) {
			return false
		}
	}
	return true
}

// bufSafeFlags lists explicitly safe Buf flags. Hazardous flags like
// --template (selects an external template file) and --config (selects
// an external config file) are absent so they defer. Unknown flags also
// defer (strict mode).
var bufSafeFlags = map[string]bool{
	"--debug":        true,
	"--error-format": true,
	"--format":       true,
	"--log-format":   true, "--log-level": true,
	"--no-config": true,
	"--timeout":   true,
	"--version":   true,
	"--":          true,
}

// classifyGit checks a git invocation with a strict local read-only subcommand
// and flag policy. diff, show, and log require --no-pager and --no-textconv
// (disabling paging and text-conversion helpers), and must not enable external
// diff helpers. All remote and state-mutating behavior defers.
func classifyGit(seg *parsedSegment, workDir string, writableRoots []string) bool {
	if len(seg.args) == 0 {
		return false
	}

	// Scan for global flags and the subcommand
	var subcommand string
	var subArgs []string
	hasNoPager := false

	i := 0
	for i < len(seg.args) {
		arg := seg.args[i]
		if strings.HasPrefix(arg, "-") {
			switch arg {
			case "--no-pager":
				hasNoPager = true
			case "--no-color":
				// harmless
			case "--no-replace-objects":
				// harmless read-only flag
			case "-c":
				return false // config override
			case "--git-dir", "--work-tree", "--namespace", "-C":
				return false // repo/worktree redirect
			case "--bare":
				return false
			default:
				// Any other global flag defers
				if subcommand == "" {
					return false
				}
			}
			i++
			continue
		}
		subcommand = arg
		subArgs = seg.args[i+1:]
		break
	}

	if subcommand == "" {
		return false
	}

	switch subcommand {
	case "status":
		return classifyGitStatusFlags(subArgs, workDir, writableRoots)
	case "ls-files":
		return classifyGitLsFilesFlags(subArgs, workDir, writableRoots)
	case "rev-parse":
		return classifyGitRevParseFlags(subArgs)
	case "symbolic-ref":
		return classifyGitSymbolicRefFlags(subArgs)
	case "describe", "name-rev", "show-ref", "for-each-ref":
		return classifyGitInspectionFlags(subArgs)
	case "diff", "log", "show":
		if !hasNoPager {
			return false
		}
		if hasGitExternalHelpers(subArgs) {
			return false
		}
		if !hasGitTextConvDisabled(subArgs) {
			return false
		}
		return classifyGitDiffLogShowFlags(subArgs, workDir, writableRoots)
	case "branch":
		return classifyGitBranchFlags(subArgs)
	case "remote":
		return false // remote operations defer
	case "tag":
		return false // tag can create/delete — defer
	case "stash":
		return false // stash mutates state — defer
	default:
		return false
	}
}

func classifyGitStatusFlags(args []string, workDir string, writableRoots []string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			switch arg {
			case "--short", "-s", "--porcelain", "--branch", "-b",
				"--long", "--null", "-z", "--no-column", "--column",
				"--ahead-behind", "--no-ahead-behind",
				"--ignored", "--untracked-files", "-u", "--renames",
				"--no-renames", "--find-renames", "-M":
				continue
			default:
				return false
			}
		}
		if !validateOperand(arg, workDir, writableRoots) {
			return false
		}
	}
	return true
}

func classifyGitLsFilesFlags(args []string, workDir string, writableRoots []string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			switch arg {
			case "--cached", "--deleted", "--modified", "--others",
				"--ignored", "--stage", "--unmerged", "--killed",
				"-z", "--null", "-v", "--verbose",
				"-m", "-o", "-i", "-s",
				"--full-name", "--recurse-submodules",
				"--abbrev", "--no-abbrev":
				continue
			default:
				return false
			}
		}
		if !validateOperand(arg, workDir, writableRoots) {
			return false
		}
	}
	return true
}

func classifyGitRevParseFlags(args []string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			switch arg {
			case "--git-dir", "--show-toplevel", "--show-prefix",
				"--show-cdup", "--show-superproject-working-tree",
				"--is-inside-work-tree", "--is-inside-git-dir",
				"--is-bare-repository", "--is-shallow-repository",
				"--short", "--abbrev-ref", "--symbolic-full-name",
				"--verify", "--quiet", "-q":
				continue
			default:
				if strings.HasPrefix(arg, "--short=") {
					continue
				}
				return false
			}
		}
	}
	return true
}

func classifyGitInspectionFlags(args []string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			switch arg {
			case "--quiet", "-q", "--verbose", "-v",
				"--tags", "--all", "--always", "--long",
				"--short", "--heads", "--no-color":
				continue
			default:
				if strings.HasPrefix(arg, "--abbrev=") ||
					strings.HasPrefix(arg, "--match=") ||
					strings.HasPrefix(arg, "--exclude=") ||
					strings.HasPrefix(arg, "--candidates=") ||
					strings.HasPrefix(arg, "--count=") {
					continue
				}
				return false
			}
		}
	}
	return true
}

// classifyGitSymbolicRefFlags validates a git symbolic-ref invocation.
// Only the read-only form (zero or one operand) is eligible; the two-operand
// set form and the -d/--delete form mutate repository state and defer.
func classifyGitSymbolicRefFlags(args []string) bool {
	nonFlagCount := 0
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			switch arg {
			case "--short", "--quiet", "-q", "--no-color":
				continue
			default:
				return false
			}
		}
		nonFlagCount++
		if nonFlagCount > 1 {
			return false
		}
	}
	return true
}

// hasGitExternalHelpers reports whether the args enable external diff or
// text-conversion helpers. Only enabling forms (--ext-diff, --textconv)
// are rejected; disabling forms (--no-textconv, --no-ext-diff) are accepted
// and handled separately by hasGitTextConvDisabled.
func hasGitExternalHelpers(args []string) bool {
	for _, arg := range args {
		name := flagName(arg)
		switch name {
		case "--ext-diff", "--textconv":
			return true
		}
	}
	return false
}

// hasGitTextConvDisabled reports whether the args include --no-textconv,
// which disables text-conversion helpers. diff, show, and log require
// this flag so repository attributes cannot invoke a textconv helper
// after model approval.
func hasGitTextConvDisabled(args []string) bool {
	for _, arg := range args {
		if flagName(arg) == "--no-textconv" {
			return true
		}
	}
	return false
}

func classifyGitDiffLogShowFlags(args []string, workDir string, writableRoots []string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			switch arg {
			case "--stat", "--shortstat", "--numstat", "--dirstat",
				"--summary", "--name-only", "--name-status",
				"--no-color", "--color", "--no-renames", "--renames",
				"--find-renames", "-M", "-C", "--no-prefix",
				"--abbrev", "--full-index", "--binary",
				"--patch", "-p", "--no-patch", "--s",
				"--root", "--max-count", "-n",
				"--oneline", "--graph", "--decorate", "--no-decorate",
				"--format", "--pretty", "--abbrev-commit",
				"--no-abbrev-commit", "--relative-date", "--date",
				"--author", "--grep", "--all-match", "--invert-grep",
				"--merges", "--no-merges", "--first-parent",
				"--reverse", "--skip",
				"--since", "--until", "--after", "--before",
				"-U", "--unified", "--raw", "--patch-with-raw",
				"--patch-with-stat", "--exit-code",
				"--quiet", "-q", "--full-history", "--simplify-by-decoration",
				"--topo-order", "--date-order", "--author-date-order",
				"--reflog", "--walk-reflogs", "--merged", "--no-merged",
				"--contains", "--no-contains", "--branches", "--tags",
				"--remotes", "--all", "--glob", "--exclude",
				"--single-worktree", "--no-walk", "--do-walk",
				"--cumulative",
				"--line-prefix", "--function-context", "-W",
				"--pickaxe-all", "--pickaxe-regex", "-S", "-G",
				"--log-size", "--source", "--mailmap", "--no-mailmap",
				"--show-notes", "--no-notes",
				"--expand-tabs", "--no-expand-tabs",
				"--minimal", "--patience", "--histogram",
				"--anchored", "--rotation", "--skip-to",
				"--output-indicator-new", "--output-indicator-old",
				"--output-indicator-context",
				"--no-textconv", "--no-ext-diff":
				continue
			default:
				if strings.HasPrefix(arg, "--stat=") ||
					strings.HasPrefix(arg, "--dirstat=") ||
					strings.HasPrefix(arg, "--format=") ||
					strings.HasPrefix(arg, "--pretty=") ||
					strings.HasPrefix(arg, "--abbrev=") ||
					strings.HasPrefix(arg, "--date=") ||
					strings.HasPrefix(arg, "--max-count=") ||
					strings.HasPrefix(arg, "--skip=") ||
					strings.HasPrefix(arg, "--since=") ||
					strings.HasPrefix(arg, "--until=") ||
					strings.HasPrefix(arg, "--after=") ||
					strings.HasPrefix(arg, "--before=") ||
					strings.HasPrefix(arg, "-U") ||
					strings.HasPrefix(arg, "--unified=") ||
					strings.HasPrefix(arg, "--find-renames=") ||
					strings.HasPrefix(arg, "-M=") ||
					strings.HasPrefix(arg, "-C=") ||
					strings.HasPrefix(arg, "--author=") ||
					strings.HasPrefix(arg, "--grep=") ||
					strings.HasPrefix(arg, "--glob=") ||
					strings.HasPrefix(arg, "--exclude=") ||
					strings.HasPrefix(arg, "--output=") ||
					strings.HasPrefix(arg, "-o") {
					if strings.HasPrefix(arg, "--output=") || arg == "-o" {
						return false
					}
					continue
				}
				return false
			}
		} else {
			if !validateGitPathspec(arg, workDir, writableRoots) {
				return false
			}
		}
	}
	return true
}

func classifyGitBranchFlags(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "-a", "--all", "-r", "--remotes", "-v", "-vv", "--verbose",
			"--list", "--show-current", "--no-color", "--color",
			"--merged", "--no-merged", "--contains", "--no-contains",
			"-q", "--quiet", "-d", "--delete", "-D":
			if arg == "-d" || arg == "--delete" || arg == "-D" {
				return false // branch deletion mutates state
			}
			continue
		default:
			return false // branch creation or unknown flag
		}
	}
	return true
}

// classifyTargetComponents analyzes a target name by splitting on
// conventional delimiters (-, :, _) and checking that at least one component
// is a recognized development verb and no component is prohibited.
func classifyTargetComponents(target string) bool {
	if target == "" {
		return false
	}
	components := splitTargetComponents(target)
	if len(components) == 0 {
		return false
	}
	hasDevVerb := false
	for _, comp := range components {
		normalized := strings.ToLower(comp)
		if prohibitedTargetComponents[normalized] {
			return false
		}
		if developmentVerbComponents[normalized] {
			hasDevVerb = true
		}
	}
	return hasDevVerb
}

func splitTargetComponents(target string) []string {
	return strings.FieldsFunc(target, func(r rune) bool {
		return r == '-' || r == ':' || r == '_'
	})
}

// developmentVerbComponents are recognized test, lint/check, build/compile,
// format, and generate target components.
var developmentVerbComponents = map[string]bool{
	"test": true, "tests": true, "spec": true, "specs": true,
	"lint": true, "lints": true, "check": true, "checks": true,
	"build": true, "builds": true, "compile": true, "compiles": true,
	"format": true, "fmt": true,
	"generate": true, "gen": true,
	"verify": true, "validate": true,
	"typecheck": true, "typechecks": true,
	"analyze": true, "inspect": true, "scan": true,
	"ci": true, "qa": true,
	"unit": true, "integration": true, "e2e": true,
	"coverage": true, "cover": true,
	"report":    true,
	"benchmark": true, "bench": true,
	"snapshot":   true,
	"checkstyle": true, "spotless": true, "spotbugs": true,
	"jacoco": true, "clippy": true,
	"doc": true, "docs": true,
	"swagger": true, "openapi": true,
	"proto": true, "protoc": true,
	"mock": true, "mockgen": true,
	"codegen": true,
	"tidy":    true,
	"fix":     true,
	"sort":    true, "isort": true,
	"imports": true,
	"vet":     true, "vets": true,
	"staticcheck": true, "revive": true, "errcheck": true,
	"static":  true,
	"profile": true,
}

// prohibitedTargetComponents override eligibility wherever they appear.
var prohibitedTargetComponents = map[string]bool{
	"install": true, "update": true, "upgrade": true,
	"publish": true, "release": true, "deploy": true,
	"uninstall": true, "remove": true, "delete": true,
	"destroy": true, "erase": true, "purge": true,
	"wipe": true, "drop": true,
	"clean": true, "distclean": true, "mrproper": true,
	"preinstall": true, "postinstall": true,
	"preuninstall": true, "postuninstall": true,
	"prepublish": true, "postpublish": true,
	"watch": true, "serve": true, "daemon": true,
	"start": true, "stop": true, "restart": true,
	"dev": true, "run": true,
	"push": true, "pull": true, "fetch": true,
	"commit": true, "tag": true,
	"reset": true, "rebase": true, "merge": true,
	"stash": true, "pop": true,
	"login": true, "logout": true, "auth": true,
	"docker": true, "container": true, "podman": true,
	"kubectl": true, "helm": true,
	"ssh": true, "scp": true, "rsync": true,
}
