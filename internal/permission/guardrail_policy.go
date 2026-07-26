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

// riskTier controls how unknown flags are handled.
type riskTier int

const (
	// riskTierBounded: intrinsically bounded commands. Unknown flags are
	// allowed; only explicit hazardous flags are denied.
	riskTierBounded riskTier = iota
	// riskTierStrict: commands capable of executing helpers, loading plugins,
	// selecting outputs, reaching the network, or mutating state. Unknown
	// flags defer; only explicit safe flags are allowed.
	riskTierStrict
)

// cmdPolicy describes the flag and argument rules for one command or
// subcommand.
type cmdPolicy struct {
	tier             riskTier
	safeFlags        map[string]bool // strict tier: only these flags allowed
	safeFlagPrefixes map[string]bool // strict tier: flags whose attached form is safe (e.g., -I, -D)
	hazardousFlags   map[string]bool // these flags are denied before exact-safe or prefix admission
	longRunningFlags map[string]bool // always denied (both tiers)
	valueFlags       map[string]bool // flags that consume the next arg as a value
	allowArgs        bool            // non-flag arguments allowed (with path validation)
	rejectDelimiter  bool            // reject -- when it would pass following args to opaque script logic
}

// classifyByPolicy checks a segment against the curated direct-command policy
// tables. Returns (eligible, found) where found indicates the binary and
// subcommand were recognized by the policy. When found is false, the caller
// may try classifyProjectTarget for aliases and task runners.
func classifyByPolicy(name string, seg *parsedSegment, workDir string, writableRoots []string) (bool, bool) {
	if name == "cmake" {
		return classifyCMake(seg, workDir, writableRoots), true
	}
	if name == "ruff" {
		return classifyRuff(seg, workDir, writableRoots), true
	}
	if name == "golangci-lint" {
		return classifyGolangCILint(seg, workDir, writableRoots), true
	}
	if name == "clang-format" {
		return classifyClangFormat(seg, workDir, writableRoots), true
	}
	if subs, ok := subcommandPolicyTable[name]; ok {
		if len(seg.args) == 0 {
			return false, true
		}
		sub := seg.args[0]
		if isGlobalLongRunningMode(sub) || isProhibitedSubcommand(sub) {
			return false, true
		}
		policy, ok := subs[sub]
		if !ok {
			return false, false
		}
		return applyPolicy(&policy, seg.args[1:], workDir, writableRoots), true
	}
	if policy, ok := simpleCommandPolicyTable[name]; ok {
		return applyPolicy(&policy, seg.args, workDir, writableRoots), true
	}
	return false, false
}

// applyPolicy checks a command's arguments against its policy. Flags are
// handled per the risk tier; operands are validated for sensitive components
// and root bounds; long-running modes always defer. Quoting does not change
// flag semantics: Bash removes quotes before constructing argv, so every
// element beginning with "-" is treated as a flag regardless of how it was
// quoted in the source command.
func applyPolicy(policy *cmdPolicy, args []string, workDir string, writableRoots []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]

		if isLongRunningFlag(arg, policy.longRunningFlags) {
			return false
		}

		if strings.HasPrefix(arg, "-") {
			if arg == "--" {
				if policy.rejectDelimiter {
					return false
				}
				continue
			}
			if !checkFlag(policy, arg, workDir, writableRoots) {
				return false
			}
			if policy.valueFlags[flagName(arg)] && flagValue(arg) == "" {
				i++
				if i >= len(args) {
					return false
				}
				if !validateOperand(args[i], workDir, writableRoots) {
					return false
				}
			}
			continue
		}

		if !policy.allowArgs {
			return false
		}
		if !validateOperand(arg, workDir, writableRoots) {
			return false
		}
	}
	return true
}

func classifyCMake(seg *parsedSegment, workDir string, writableRoots []string) bool {
	if len(seg.args) == 0 {
		return false
	}
	if flagName(seg.args[0]) == "--build" {
		return classifyCMakeBuild(seg.args, workDir, writableRoots)
	}
	for _, arg := range seg.args[1:] {
		if flagName(arg) == "--build" {
			return false
		}
	}
	policy := cmdPolicy{
		tier:       riskTierStrict,
		safeFlags:  cmakeConfigureSafeFlags,
		valueFlags: cmakeConfigureValueFlags,
		allowArgs:  true,
	}
	return applyPolicy(&policy, seg.args, workDir, writableRoots)
}

func classifyCMakeBuild(args []string, workDir string, writableRoots []string) bool {
	buildDirSeen := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !buildDirSeen {
			if flagName(arg) != "--build" {
				return false
			}
			value := flagValue(arg)
			if value == "" {
				i++
				if i >= len(args) {
					return false
				}
				value = args[i]
			}
			if !validateOperand(value, workDir, writableRoots) {
				return false
			}
			buildDirSeen = true
			continue
		}

		if arg == "--" || !strings.HasPrefix(arg, "-") {
			return false
		}
		name := flagName(arg)
		value := flagValue(arg)
		if value == "" && len(arg) > 2 && strings.HasPrefix(arg, "-j") && !strings.HasPrefix(arg, "--") {
			name = "-j"
			value = arg[2:]
		}
		if !cmakeBuildSafeFlags[name] {
			return false
		}
		if value == "" && cmakeBuildValueFlags[name] {
			i++
			if i >= len(args) {
				return false
			}
			value = args[i]
		}
		if value != "" {
			if !cmakeBuildValueFlags[name] || !validateCMakeBuildFlagValue(name, value) {
				return false
			}
		}
	}
	return buildDirSeen
}

func classifyRuff(seg *parsedSegment, workDir string, writableRoots []string) bool {
	if len(seg.args) == 0 {
		return false
	}
	sub := strings.ToLower(seg.args[0])
	if isGlobalLongRunningMode(sub) || isProhibitedSubcommand(sub) {
		return false
	}
	switch sub {
	case "check", "format":
		policy := cmdPolicy{
			tier:           riskTierBounded,
			hazardousFlags: ruffHazardousFlags,
			allowArgs:      true,
		}
		return applyPolicy(&policy, seg.args[1:], workDir, writableRoots)
	default:
		return false
	}
}

func classifyGolangCILint(seg *parsedSegment, workDir string, writableRoots []string) bool {
	if len(seg.args) == 0 {
		return true
	}
	if strings.HasPrefix(seg.args[0], "-") {
		return false
	}
	sub := strings.ToLower(seg.args[0])
	if isGlobalLongRunningMode(sub) || isProhibitedSubcommand(sub) || sub == "cache" {
		return false
	}
	switch sub {
	case "run":
		policy := cmdPolicy{
			tier:           riskTierBounded,
			hazardousFlags: golangciHazardousFlags,
			allowArgs:      true,
		}
		return applyPolicy(&policy, seg.args[1:], workDir, writableRoots)
	default:
		return false
	}
}

func classifyClangFormat(seg *parsedSegment, workDir string, writableRoots []string) bool {
	policy := cmdPolicy{
		tier:      riskTierBounded,
		allowArgs: true,
	}
	for i := 0; i < len(seg.args); i++ {
		arg := seg.args[i]
		if isLongRunningFlag(arg, policy.longRunningFlags) {
			return false
		}
		if strings.HasPrefix(arg, "-") {
			if arg == "--" {
				continue
			}
			name := flagName(arg)
			value := flagValue(arg)
			if name == "--style" || name == "-style" {
				if value == "" {
					i++
					if i >= len(seg.args) {
						return false
					}
					value = seg.args[i]
				}
				if !validateClangFormatStyleValue(value, workDir, writableRoots) {
					return false
				}
				continue
			}
			if !checkFlag(&policy, arg, workDir, writableRoots) {
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

func validateClangFormatStyleValue(value, workDir string, writableRoots []string) bool {
	if value == "" || valueMatchesSecretPattern(value) || containsSecretKeyword(value) {
		return false
	}
	lower := strings.ToLower(value)
	if lower == "file" {
		return true
	}
	if strings.HasPrefix(lower, "file:") {
		path := value[len("file:"):]
		if path == "" {
			return false
		}
		return validateOperand(path, workDir, writableRoots)
	}
	switch lower {
	case "llvm", "gnu", "google", "chromium", "microsoft", "mozilla", "webkit", "none":
		return true
	default:
		return false
	}
}

func validateCMakeBuildFlagValue(name, value string) bool {
	if value == "" || valueMatchesSecretPattern(value) || containsSecretKeyword(value) {
		return false
	}
	switch name {
	case "-j", "--parallel":
		return isDecimalValue(value)
	default:
		return false
	}
}

// checkFlag validates a single flag argument against the policy. For flags
// with =values, the value is validated for sensitive components and root
// bounds.
func checkFlag(policy *cmdPolicy, arg, workDir string, writableRoots []string) bool {
	name := flagName(arg)
	value := flagValue(arg)

	if !validateFlagPrivacy(arg, name, value) {
		return false
	}

	if isHazardousFlag(name, policy.hazardousFlags) {
		return false
	}

	if policy.tier == riskTierStrict {
		if !policy.safeFlags[name] {
			prefix, ok := matchSafeFlagPrefix(name, policy.safeFlagPrefixes)
			if !ok {
				return false
			}
			if value == "" {
				value = name[len(prefix):]
			}
		}
	}

	if value != "" {
		if !validateOperand(value, workDir, writableRoots) {
			return false
		}
	}
	return true
}

func validateFlagPrivacy(parts ...string) bool {
	for _, part := range parts {
		if part == "" {
			continue
		}
		if valueMatchesSecretPattern(part) || containsSecretKeyword(part) {
			return false
		}
	}
	return true
}

// isHazardousFlag reports whether name matches a hazardous flag either
// exactly or as an attached form (e.g., -B./toolchain is an attached form
// of -B). This prevents attached-value bypasses of the denylist.
func isHazardousFlag(name string, hazardousFlags map[string]bool) bool {
	if hazardousFlags == nil {
		return false
	}
	if hazardousFlags[name] {
		return true
	}
	for hz := range hazardousFlags {
		if len(name) > len(hz) && strings.HasPrefix(name, hz) {
			return true
		}
	}
	return false
}

// matchSafeFlagPrefix reports whether name is an attached form of a safe
// flag prefix (e.g., -I./include matches prefix -I). Returns the matched
// prefix and true on success.
func matchSafeFlagPrefix(name string, prefixes map[string]bool) (string, bool) {
	if prefixes == nil {
		return "", false
	}
	for prefix := range prefixes {
		if len(name) > len(prefix) && strings.HasPrefix(name, prefix) {
			return prefix, true
		}
	}
	return "", false
}

func flagName(arg string) string {
	if idx := strings.Index(arg, "="); idx >= 0 {
		return arg[:idx]
	}
	return arg
}

func flagValue(arg string) string {
	if idx := strings.Index(arg, "="); idx >= 0 {
		return arg[idx+1:]
	}
	return ""
}

// isLongRunningFlag reports whether a flag indicates a long-running or
// interactive mode.
func isLongRunningFlag(arg string, perCmd map[string]bool) bool {
	name := flagName(arg)
	if globalLongRunningFlags[name] {
		return true
	}
	if perCmd != nil && perCmd[name] {
		return true
	}
	return false
}

// isGlobalLongRunningMode reports whether a subcommand or argument is a
// globally recognized long-running mode.
func isGlobalLongRunningMode(arg string) bool {
	return globalLongRunningModes[arg]
}

// isProhibitedSubcommand reports whether a subcommand is universally
// prohibited (install, update, publish, deploy, etc.).
func isProhibitedSubcommand(sub string) bool {
	return prohibitedSubcommands[sub]
}

var globalLongRunningFlags = map[string]bool{
	"--watch": true, "--serve": true, "--daemon": true,
	"--interactive": true, "--tui": true,
	"--no-exit": true, "--keep-alive": true,
	"--continuous": true,
}

var globalLongRunningModes = map[string]bool{
	"watch": true, "serve": true, "daemon": true,
	"server": true,
	"dev":    true, "start": true,
}

var prohibitedSubcommands = map[string]bool{
	"install": true, "update": true, "upgrade": true,
	"publish": true, "release": true, "deploy": true,
	"uninstall": true, "remove": true, "add": true,
	"clean": true,
}

// subcommandPolicyTable maps binary → subcommand → policy.
var subcommandPolicyTable = map[string]map[string]cmdPolicy{
	"go": {
		"test": {
			tier:       riskTierStrict,
			safeFlags:  goTestSafeFlags,
			valueFlags: goValueFlags,
			allowArgs:  true,
		},
		"build": {
			tier:       riskTierStrict,
			safeFlags:  goBuildSafeFlags,
			valueFlags: goValueFlags,
			allowArgs:  true,
		},
		"vet": {
			tier:       riskTierStrict,
			safeFlags:  goVetSafeFlags,
			valueFlags: goValueFlags,
			allowArgs:  true,
		},
		"fmt": {
			tier:      riskTierBounded,
			allowArgs: true,
		},
		"generate": {
			tier:       riskTierStrict,
			safeFlags:  goGenerateSafeFlags,
			valueFlags: goValueFlags,
			allowArgs:  true,
		},
		"env": {
			tier:           riskTierBounded,
			hazardousFlags: map[string]bool{"-w": true, "-u": true},
			allowArgs:      true,
		},
		"list": {
			tier:      riskTierStrict,
			safeFlags: goListSafeFlags,
			allowArgs: true,
		},
		"doc": {
			tier:      riskTierBounded,
			allowArgs: true,
		},
		"version": {
			tier:      riskTierBounded,
			allowArgs: false,
		},
	},
	"cargo": {
		"test": {
			tier:      riskTierStrict,
			safeFlags: cargoTestSafeFlags,
			allowArgs: true,
		},
		"build": {
			tier:      riskTierStrict,
			safeFlags: cargoBuildSafeFlags,
			allowArgs: true,
		},
		"check": {
			tier:      riskTierStrict,
			safeFlags: cargoCheckSafeFlags,
			allowArgs: true,
		},
		"clippy": {
			tier:      riskTierStrict,
			safeFlags: cargoClippySafeFlags,
			allowArgs: true,
		},
		"fmt": {
			tier:      riskTierStrict,
			safeFlags: cargoFmtSafeFlags,
			allowArgs: true,
		},
		"verify": {
			tier:      riskTierStrict,
			safeFlags: cargoVerifySafeFlags,
			allowArgs: true,
		},
	},
	"npm": {
		"test": {
			tier:            riskTierStrict,
			safeFlags:       npmTestSafeFlags,
			allowArgs:       false,
			rejectDelimiter: true,
		},
	},
	"pnpm": {
		"test": {
			tier:            riskTierStrict,
			safeFlags:       npmTestSafeFlags,
			allowArgs:       false,
			rejectDelimiter: true,
		},
	},
	"yarn": {
		"test": {
			tier:            riskTierStrict,
			safeFlags:       npmTestSafeFlags,
			allowArgs:       false,
			rejectDelimiter: true,
		},
	},
	"mvn": {
		"test": {
			tier:      riskTierStrict,
			safeFlags: mvnSafeFlags,
			allowArgs: false,
		},
		"verify": {
			tier:      riskTierStrict,
			safeFlags: mvnSafeFlags,
			allowArgs: false,
		},
		"compile": {
			tier:      riskTierStrict,
			safeFlags: mvnSafeFlags,
			allowArgs: false,
		},
		"checkstyle": {
			tier:      riskTierBounded,
			allowArgs: false,
		},
	},
	"gradle": {
		"test": {
			tier:      riskTierStrict,
			safeFlags: gradleSafeFlags,
			allowArgs: false,
		},
		"build": {
			tier:      riskTierStrict,
			safeFlags: gradleSafeFlags,
			allowArgs: false,
		},
		"check": {
			tier:      riskTierStrict,
			safeFlags: gradleSafeFlags,
			allowArgs: false,
		},
	},
}

// simpleCommandPolicyTable maps binary → policy for commands without
// subcommands.
var simpleCommandPolicyTable = map[string]cmdPolicy{
	"goimports": {tier: riskTierBounded, allowArgs: true},
	"gofmt":     {tier: riskTierBounded, hazardousFlags: map[string]bool{"-r": true}, allowArgs: true},
	"eslint": {
		tier:           riskTierBounded,
		hazardousFlags: eslintHazardousFlags,
		allowArgs:      true,
	},
	"prettier": {
		tier:           riskTierBounded,
		hazardousFlags: prettierHazardousFlags,
		allowArgs:      true,
	},
	"tsc": {
		tier:           riskTierBounded,
		hazardousFlags: tscHazardousFlags,
		allowArgs:      true,
	},
	"jest": {
		tier:      riskTierStrict,
		safeFlags: jestSafeFlags,
		allowArgs: true,
	},
	"vitest": {
		tier:      riskTierStrict,
		safeFlags: vitestSafeFlags,
		allowArgs: true,
	},
	"mocha": {
		tier:      riskTierStrict,
		safeFlags: mochaSafeFlags,
		allowArgs: true,
	},
	"pytest": {
		tier:      riskTierStrict,
		safeFlags: pytestSafeFlags,
		allowArgs: true,
	},
	"black": {
		tier:           riskTierBounded,
		hazardousFlags: blackHazardousFlags,
		allowArgs:      true,
	},
	"mypy": {
		tier:           riskTierBounded,
		hazardousFlags: mypyHazardousFlags,
		allowArgs:      true,
	},
	"pylint": {
		tier:           riskTierStrict,
		safeFlags:      pylintSafeFlags,
		hazardousFlags: pylintHazardousFlags,
		valueFlags:     pylintValueFlags,
		allowArgs:      true,
	},
	"flake8": {
		tier:           riskTierBounded,
		hazardousFlags: flake8HazardousFlags,
		allowArgs:      true,
	},
	"isort": {
		tier:           riskTierBounded,
		hazardousFlags: isortHazardousFlags,
		allowArgs:      true,
	},
	"rustfmt": {
		tier:           riskTierBounded,
		hazardousFlags: map[string]bool{"--emit": true},
		allowArgs:      true,
	},
	"ktlint": {
		tier:           riskTierStrict,
		safeFlags:      ktlintSafeFlags,
		hazardousFlags: ktlintHazardousFlags,
		valueFlags:     ktlintValueFlags,
		allowArgs:      true,
	},
	"javac": {
		tier:             riskTierStrict,
		safeFlags:        javacSafeFlags,
		safeFlagPrefixes: javacSafeFlagPrefixes,
		valueFlags:       javacValueFlags,
		allowArgs:        true,
	},
	"kotlinc": {
		tier:       riskTierStrict,
		safeFlags:  kotlincSafeFlags,
		valueFlags: kotlincValueFlags,
		allowArgs:  true,
	},
	"clang": {
		tier:             riskTierStrict,
		safeFlags:        compilerSafeFlags,
		safeFlagPrefixes: compilerSafeFlagPrefixes,
		hazardousFlags:   compilerHazardousFlags,
		valueFlags:       compilerValueFlags,
		allowArgs:        true,
	},
	"clang++": {
		tier:             riskTierStrict,
		safeFlags:        compilerSafeFlags,
		safeFlagPrefixes: compilerSafeFlagPrefixes,
		hazardousFlags:   compilerHazardousFlags,
		valueFlags:       compilerValueFlags,
		allowArgs:        true,
	},
	"gcc": {
		tier:             riskTierStrict,
		safeFlags:        compilerSafeFlags,
		safeFlagPrefixes: compilerSafeFlagPrefixes,
		hazardousFlags:   compilerHazardousFlags,
		valueFlags:       compilerValueFlags,
		allowArgs:        true,
	},
	"g++": {
		tier:             riskTierStrict,
		safeFlags:        compilerSafeFlags,
		safeFlagPrefixes: compilerSafeFlagPrefixes,
		hazardousFlags:   compilerHazardousFlags,
		valueFlags:       compilerValueFlags,
		allowArgs:        true,
	},
	"clang-tidy": {
		tier:           riskTierStrict,
		safeFlags:      clangTidySafeFlags,
		hazardousFlags: clangTidyHazardousFlags,
		allowArgs:      true,
	},
	"cppcheck": {
		tier:             riskTierStrict,
		safeFlags:        cppcheckSafeFlags,
		safeFlagPrefixes: cppcheckSafeFlagPrefixes,
		valueFlags:       cppcheckValueFlags,
		allowArgs:        true,
	},
	"staticcheck": {
		tier:      riskTierBounded,
		allowArgs: true,
	},
	"swag": {
		tier:      riskTierBounded,
		allowArgs: true,
	},
	"mockgen": {
		tier:      riskTierStrict,
		safeFlags: mockgenSafeFlags,
		allowArgs: true,
	},
	"stringer": {
		tier:      riskTierBounded,
		allowArgs: true,
	},
	"protoc": {
		tier:             riskTierStrict,
		safeFlags:        protocSafeFlags,
		safeFlagPrefixes: protocSafeFlagPrefixes,
		valueFlags:       protocValueFlags,
		allowArgs:        true,
	},
	"protoc-gen-go": {
		tier:      riskTierBounded,
		allowArgs: true,
	},
	"sqlc": {
		tier:      riskTierBounded,
		allowArgs: false,
	},
}

// --- Flag sets ---

// goValueFlags lists Go toolchain flags whose value is passed as the next
// separate argument (e.g., `go test -run TestFoo`). Without this, a value
// starting with "-" would be misclassified as an independent flag.
// Pass-through flags (-gcflags, -asmflags, -ldflags, -gccgoflags) and
// compiler selection (-compiler) are absent: their values can carry
// arbitrary sub-tool flags that bypass the policy, so they defer.
var goValueFlags = map[string]bool{
	"-o":    true,
	"-tags": true, "-mod": true, "-modfile": true, "-overlay": true,
	"-pkgdir":        true,
	"-installsuffix": true, "-buildmode": true, "-coverpkg": true,
	"-run": true, "-bench": true, "-benchtime": true, "-timeout": true,
	"-coverprofile": true, "-cpuprofile": true, "-memprofile": true,
	"-mutexprofile": true, "-trace": true, "-p": true, "-parallel": true,
	"-shuffle": true, "-cpu": true, "-list": true, "-covermode": true,
}

var goTestSafeFlags = map[string]bool{
	"-v": true, "-short": true, "-count": true, "-run": true,
	"-race": true, "-cover": true, "-coverprofile": true, "-coverpkg": true,
	"-timeout": true, "-parallel": true, "-failfast": true,
	"-bench": true, "-benchmem": true, "-benchtime": true,
	"-cpuprofile": true, "-memprofile": true, "-mutexprofile": true,
	"-trace": true, "-json": true, "-p": true, "-shuffle": true,
	"-tags": true, "-mod": true, "-modfile": true, "-overlay": true,
	"-cpu": true, "-list": true, "-msan": true, "-asan": true,
	"-trimpath":      true,
	"-installsuffix": true, "-linkshared": true, "-pkgdir": true,
	"-covermode": true, "-GOMAXPROCS": true, "-GOGC": true,
	"-GOMEMLIMIT": true, "-godebug": true, "--": true,
	"-x": true, "-n": true, "-work": true,
	"-buildvcs": true, "-buildmode": true, "-o": true,
}

var goBuildSafeFlags = map[string]bool{
	"-v": true, "-o": true, "-tags": true, "-race": true,
	"-msan": true, "-asan": true,
	"-mod": true, "-modfile": true, "-overlay": true,
	"-pkgdir": true, "-trimpath": true, "-buildvcs": true, "-buildmode": true,
	"-n": true, "-p": true, "-work": true, "-x": true,
	"-installsuffix": true, "-linkshared": true, "--": true,
}

var goVetSafeFlags = map[string]bool{
	"-v": true, "-n": true, "-x": true,
	"-tags": true, "-mod": true, "-modfile": true, "-overlay": true,
	"-trimpath":      true,
	"-installsuffix": true, "-linkshared": true, "-pkgdir": true,
	"-json": true, "--": true,
}

var goListSafeFlags = map[string]bool{
	"-json": true, "-deps": true, "-e": true, "-find": true,
	"--": true,
}

var goGenerateSafeFlags = map[string]bool{
	"-v": true, "-n": true, "-x": true, "-tags": true,
	"-mod": true, "-modfile": true, "--": true,
}

var cargoTestSafeFlags = map[string]bool{
	"--lib": true, "--bin": true, "--test": true, "--bench": true,
	"--no-run": true, "--no-fail-fast": true, "--package": true,
	"--workspace": true, "--all": true, "--release": true,
	"--verbose": true, "-v": true, "--quiet": true, "-q": true,
	"--color": true, "--frozen": true, "--locked": true,
	"--offline": true, "--features": true, "--all-features": true,
	"--no-default-features": true, "--target": true,
	"--profile": true, "--": true,
	"-j": true, "--jobs": true, "-r": true,
}

var cargoBuildSafeFlags = map[string]bool{
	"--lib": true, "--bin": true, "--release": true,
	"--verbose": true, "-v": true, "--quiet": true, "-q": true,
	"--color": true, "--frozen": true, "--locked": true,
	"--offline": true, "--features": true, "--all-features": true,
	"--no-default-features": true, "--target": true,
	"--profile": true, "-j": true, "--jobs": true,
	"--package": true, "--workspace": true, "--all": true,
}

var cargoClippySafeFlags = map[string]bool{
	"--lib": true, "--bin": true, "--test": true, "--bench": true,
	"--no-deps": true, "--no-run": true,
	"--package": true, "--workspace": true, "--all": true, "--release": true,
	"--verbose": true, "-v": true, "--quiet": true, "-q": true,
	"--color": true, "--frozen": true, "--locked": true,
	"--offline": true, "--features": true, "--all-features": true,
	"--no-default-features": true, "--target": true,
	"--profile": true, "--": true,
	"-j": true, "--jobs": true, "-r": true,
}

var cargoCheckSafeFlags = map[string]bool{
	"--lib": true, "--bin": true, "--test": true, "--bench": true,
	"--no-run":  true,
	"--package": true, "--workspace": true, "--all": true, "--release": true,
	"--verbose": true, "-v": true, "--quiet": true, "-q": true,
	"--color": true, "--frozen": true, "--locked": true,
	"--offline": true, "--features": true, "--all-features": true,
	"--no-default-features": true, "--target": true,
	"--profile": true, "--": true,
	"-j": true, "--jobs": true, "-r": true,
}

var cargoFmtSafeFlags = map[string]bool{
	"--check":   true,
	"--verbose": true, "-v": true, "--quiet": true, "-q": true,
	"--color": true, "--frozen": true, "--locked": true,
	"--offline": true, "--package": true,
	"--": true,
}

var cargoVerifySafeFlags = map[string]bool{
	"--verbose": true, "-v": true, "--quiet": true, "-q": true,
	"--color": true, "--frozen": true, "--locked": true,
	"--offline": true, "--": true,
}

var npmTestSafeFlags = map[string]bool{
	"--silent": true, "--quiet": true, "--verbose": true,
}

var mvnSafeFlags = map[string]bool{
	"-q": true, "--quiet": true, "-X": true, "--debug": true,
	"-e": true, "--errors": true, "-fn": true, "--fail-never": true,
	"-fae": true, "--fail-at-end": true, "-ff": true, "--fail-fast": true,
	"-pl": true, "--projects": true, "-am": true, "--also-make": true,
	"-amd": true, "--also-make-dependents": true, "-B": true, "--batch-mode": true,
	"-V": true, "--show-version": true, "-N": true, "--non-recursive": true,
	"-T": true, "--threads": true, "-Dtest": true, "-DfailIfNoTests": true,
	"-Dmaven.test.failure.ignore": true, "-DskipTests": true,
	"-Dmaven.test.skip": true, "-Dcheckstyle.skip": true,
	"-Dspotbugs.skip": true, "-Dpmd.skip": true,
	"-Djacoco.skip": true, "--": true,
}

var gradleSafeFlags = map[string]bool{
	"-q": true, "--quiet": true, "-v": true, "--verbose": true,
	"-d": true, "--debug": true, "-i": true, "--info": true,
	"-w": true, "--warn": true, "-s": true, "--stacktrace": true,
	"-S": true, "--full-stacktrace": true,
	"--no-daemon": true,
	"--parallel":  true, "--no-parallel": true,
	"-Dtest.single": true, "-Dtest.failfast": true,
	"--build-cache": true, "--no-build-cache": true,
	"--rerun-tasks": true, "-x": true, "--exclude-task": true,
	"--": true,
}

var eslintHazardousFlags = map[string]bool{
	"--plugin": true, "--rulesdir": true, "--rules": true,
	"--cache-location": true, "--config": true, "-c": true,
	"--ext": true, "--resolve-plugins-relative-to": true,
	"--report-unused-disable-directives-severity": true,
	"--parser": true, "--format": true, "-f": true,
}

var prettierHazardousFlags = map[string]bool{
	"--plugin": true, "--plugin-search-dir": true, "--config": true,
	"-c": true, "--editorconfig": true, "--resolve-config": true,
}

var tscHazardousFlags = map[string]bool{
	"--project": true, "-p": true, "--build": true, "-b": true,
	"--plugins": true, "--watch": true, "-w": true,
	"--preserveWatchOutput": true,
}

var jestSafeFlags = map[string]bool{
	"--testPathPattern": true, "--testNamePattern": true, "-t": true,
	"--verbose": true, "--silent": true, "--coverage": true,
	"--collectCoverage": true, "--collectCoverageFrom": true,
	"--coverageDirectory": true, "--coverageReporters": true,
	"--json": true, "--outputFile": true, "--ci": true,
	"--detectOpenHandles": true, "--forceExit": true,
	"--bail": true, "--testTimeout": true, "--no-cache": true,
	"--showConfig": true, "--listTests": true,
	"--runInBand": true, "--no-color": true, "--color": true,
	"-u": true, "--updateSnapshot": true, "--": true,
}

// vitestSafeFlags lists explicitly safe Vitest flags. The --reporter flag is
// absent because it can load an arbitrary JavaScript file as a custom reporter.
// Unknown flags also defer (strict mode).
var vitestSafeFlags = map[string]bool{
	"--run": true, "--testNamePattern": true, "-t": true,
	"--coverage": true, "--": true,
	"--no-color": true, "--color": true, "--silent": true,
	"--coverage.enabled": true, "--coverage.reporter": true,
	"--coverage.directory": true, "--coverage.include": true,
}

// mochaSafeFlags lists explicitly safe Mocha flags. The --reporter/-R flag is
// absent because it can load an arbitrary JavaScript file as a custom reporter.
// Unknown flags also defer (strict mode).
var mochaSafeFlags = map[string]bool{
	"--grep": true, "-g": true,
	"--invert": true, "--check-leaks": true, "--no-deprecation": true,
	"--trace-deprecation": true, "--trace-warnings": true,
	"--recursive": true, "--bail": true, "--retries": true,
	"--timeout": true, "-t": true, "--slow": true, "-s": true,
	"--exit": true, "--forbid-only": true,
	"--forbid-pending": true, "--extension": true,
	"--ui": true, "-u": true,
	"--color": true, "--no-color": true,
	"--": true,
}

// pytestSafeFlags omits --cov-config because coverage.py configuration can
// name importable plugins, which executes repository code during startup.
var pytestSafeFlags = map[string]bool{
	"-v": true, "--verbose": true, "-q": true, "--quiet": true,
	"-s": true, "--capture=no": true, "-x": true, "--exitfirst": true,
	"--maxfail": true, "-k": true, "--keyword": true, "-m": true,
	"--marker": true, "--tb": true, "--traceback": true,
	"--cov": true, "--cov-report": true,
	"--cov-fail-under": true, "--cov-branch": true,
	"--json-report": true, "--json-report-file": true,
	"-n": true, "--numprocesses": true, "-d": true, "--dist": true,
	"--html": true, "--self-contained-html": true,
	"--lf": true, "--last-failed": true, "--ff": true, "--failed-first": true,
	"--nf": true, "--new-first": true, "--cache-show": true,
	"--no-cov": true, "--no-header": true,
	"--rootdir": true, "--continue-on-collection-errors": true,
	"--ignore": true, "--ignore-glob": true,
	"-r": true, "--report": true, "--strict-markers": true,
	"--strict-config": true, "--setup-show": true,
	"--fixtures": true, "--fixtures-per-test": true,
	"--collect-only": true, "--co": true,
	"--junitxml": true, "--junit-xml": true,
	"-l": true, "--showlocals": true, "--": true,
}

var ruffHazardousFlags = map[string]bool{
	"--config": true, "--extend-config": true,
	"--select": true, "--extend-select": true,
	"--ignore": true, "--extend-ignore": true,
	"--fixable": true, "--unfixable": true,
	"--add-noqa": true, "--add-selection": true,
	"--cache-dir": true, "--config-file": true,
}

var blackHazardousFlags = map[string]bool{
	"--config": true, "-c": true, "--code": true,
	"--cache-dir": true,
}

var mypyHazardousFlags = map[string]bool{
	"--config-file": true, "--cache-dir": true,
	"--plugins": true, "--custom-typeshed-dir": true,
	"--python-executable": true,
	"--pdb":               true,
}

var flake8HazardousFlags = map[string]bool{
	"--config": true, "--append-config": true,
	"--enable-extensions": true, "--require-plugins": true,
}

var isortHazardousFlags = map[string]bool{
	"--settings-path": true, "--profile": true,
	"--plugin": true, "--ext-format": true,
}

// ktlintSafeFlags lists explicitly safe ktlint flags. Custom ruleset loading
// (--ruleset/-R) is absent and hazardous so future safe-list edits cannot
// accidentally admit JVM provider code.
var ktlintSafeFlags = map[string]bool{
	"--format": true, "-F": true,
	"--relative": true,
	"--color":    true, "--no-color": true,
	"--verbose": true, "--debug": true,
	"--log-level": true,
	"--version":   true,
	"--help":      true, "-h": true,
	"--": true,
}

var ktlintHazardousFlags = map[string]bool{
	"--ruleset": true,
	"-R":        true,
}

var ktlintValueFlags = map[string]bool{
	"--log-level": true,
}

var golangciHazardousFlags = map[string]bool{
	"--enable": true, "-E": true, "--disable": true, "-D": true,
	"--disable-all": true, "--plugins": true,
	"--config": true, "-c": true, "--out-format": true, "-o": true,
	"--issues-exit-code": true, "--new-from-rev": true,
	"--rev": true, "--cpu-profile": true, "--mem-profile": true,
	"--trace-profile": true,
}

// protocSafeFlags lists explicitly safe protoc flags. Only well-known
// language output generators are admitted; unknown --NAME_out flags defer
// because they dispatch protoc-gen-NAME, an arbitrary plugin executable.
var protocSafeFlags = map[string]bool{
	// Language output generators (only well-known ones)
	"--go_out": true, "--go-grpc_out": true,
	"--cpp_out": true, "--csharp_out": true,
	"--java_out": true, "--kotlin_out": true,
	"--js_out": true, "--objc_out": true,
	"--php_out": true, "--py_out": true, "--python_out": true,
	"--ruby_out": true, "--swift_out": true,
	"--dart_out": true,
	// Other safe flags
	"--proto_path": true, "-I": true,
	"--descriptor_set_out": true, "-o": true,
	"--encode": true, "--decode": true,
	"--print_free_field_numbers": true,
	"--version":                  true,
	"--":                         true,
}

// protocSafeFlagPrefixes lists flag prefixes whose attached form is safe.
var protocSafeFlagPrefixes = map[string]bool{
	"-I": true, // include path (attached: -I./proto)
}

// protocValueFlags lists protoc flags that consume the next argument as a
// separate value.
var protocValueFlags = map[string]bool{
	"--proto_path": true, "-I": true,
	"--descriptor_set_out": true, "-o": true,
	"--go_out": true, "--go-grpc_out": true,
	"--cpp_out": true, "--csharp_out": true,
	"--java_out": true, "--kotlin_out": true,
	"--js_out": true, "--objc_out": true,
	"--php_out": true, "--py_out": true, "--python_out": true,
	"--ruby_out": true, "--swift_out": true,
	"--dart_out": true,
}

// compilerHazardousFlags denies flags that load plugins, wrapper programs,
// or redirect compiler subprogram search paths in GCC and Clang. These can
// execute arbitrary helper code or load attacker-supplied binaries. In strict
// tier, these are checked before safe flags and prefixes so that future policy
// edits cannot accidentally re-admit executable selectors.
var compilerHazardousFlags = map[string]bool{
	// GCC/Clang plugin loading
	"-plugin": true, "-fplugin": true, "-fpass-plugin": true,
	"-add-plugin": true, "-load": true,
	// Compiler/linker executable selection
	"-fuse-ld": true, "-fthinlto-distributor": true, "-flto": true,
	// External program invocation
	"-wrapper": true,
	// Specs file (can define program execution)
	"-specs": true,
	// Search path for compiler subprograms (executables like cc1, as, ld)
	"-B": true,
	// Pass-through to sub-invocations (can execute helpers or load plugins)
	"-Xclang": true, "-mllvm": true,
	"-Xassembler": true, "-Xlinker": true, "-Xpreprocessor": true,
	// Linker plugin loading via -Wl, pass-through
	"-Wl,--plugin": true, "-Wl,-plugin": true,
}

// compilerSafeFlags lists flags that are explicitly safe for GCC and Clang
// in strict tier. Unknown flags defer to human review.
var compilerSafeFlags = map[string]bool{
	// Output control
	"-c": true, "-S": true, "-E": true,
	// Optimization (exact forms; -O prefix covers -O0, -O1, -O2, -O3, -Os, -Ofast, -Og)
	"-O": true,
	// Debug info (exact form; -g prefix covers -g0, -g1, -g2, -g3, -ggdb, -gdwarf-*)
	"-g": true,
	// Warning control
	"-w": true, "-Wall": true, "-Wextra": true, "-Werror": true,
	"-Wpedantic": true, "-pedantic": true, "-pedantic-errors": true,
	// Language standard (= form: -std=c++17, flagName is -std)
	"-std": true,
	// Threading
	"-pthread": true,
	// Misc
	"-pipe": true, "-v": true, "--version": true,
	// Dependency generation
	"-M": true, "-MM": true, "-MD": true, "-MMD": true, "-MP": true,
	"-MF": true, "-MT": true, "-MQ": true,
	// Linker
	"-static": true, "-shared": true,
	// Enumerated safe -f feature flags. The compiler policy deliberately
	// avoids an open-ended -f prefix because some -f families select helper
	// executables or linker programs.
	"-fPIC": true, "-fpic": true, "-fPIE": true, "-fpie": true,
	"-fcommon": true, "-fno-common": true,
	"-fexceptions": true, "-fno-exceptions": true,
	"-frtti": true, "-fno-rtti": true,
	"-fomit-frame-pointer": true, "-fno-omit-frame-pointer": true,
	"-fstrict-aliasing": true, "-fno-strict-aliasing": true,
	"-fvisibility": true, "-fvisibility-inlines-hidden": true,
	// Output and language (value flags)
	"-o": true, "-x": true,
	// End of flags
	"--": true,
}

// compilerSafeFlagPrefixes lists flag prefixes whose attached form is safe
// (e.g., -I./include, -DFOO=bar). It deliberately excludes the broad -W
// warning prefix and all -W{p,a,l}, comma pass-through forms: GCC and Clang
// split those into forwarded preprocessor, assembler, or linker options whose
// output paths and helper-loading behavior cannot be validated as one operand.
var compilerSafeFlagPrefixes = map[string]bool{
	"-I":    true, // include path
	"-D":    true, // define macro
	"-U":    true, // undefine macro
	"-L":    true, // library path
	"-l":    true, // link library
	"-Wno-": true, // warning disables (e.g., -Wno-unused)
	"-O":    true, // optimization (e.g., -O2 — also exact match)
	"-g":    true, // debug info (e.g., -g3, -ggdb — also exact match)
	"-m":    true, // machine flags (e.g., -m64, -march=native)
}

// compilerValueFlags lists compiler flags that consume the next argument as
// a separate value (e.g., -o main, -x c). Every entry must also appear in
// compilerSafeFlags or compilerSafeFlagPrefixes; otherwise the flag is
// rejected by checkFlag before the value-consumption check is reached,
// making the entry dead metadata. Attached forms are handled by
// safeFlagPrefixes.
var compilerValueFlags = map[string]bool{
	"-o": true, "-x": true,
	"-MF": true, "-MT": true, "-MQ": true,
	"-D": true, "-I": true, "-L": true,
}

// javacSafeFlags lists explicitly safe javac flags. Annotation processor
// loading (-processor), JVM pass-through (-J), and classpath-style path-list
// flags (-cp, -classpath, -sourcepath, -bootclasspath, -extdirs,
// -endorseddirs) are absent so they defer. Unknown flags also defer
// (strict mode).
var javacSafeFlags = map[string]bool{
	"-d":        true,
	"-s":        true,
	"-encoding": true,
	"-source":   true, "-target": true,
	"--release": true,
	"-g":        true,
	"-nowarn":   true, "-deprecation": true,
	"-verbose": true, "-version": true,
	"-Werror":     true,
	"-parameters": true,
	"-proc":       true, "-proc:none": true, "-proc:only": true,
	"-implicit:none": true, "-implicit:class": true,
	"--": true,
}

var javacSafeFlagPrefixes = map[string]bool{
	"-g:":    true, // -g:none, -g:lines, -g:vars
	"-Xlint": true, // -Xlint:all, -Xlint:unchecked, etc.
}

var javacValueFlags = map[string]bool{
	"-d":        true,
	"-s":        true,
	"-encoding": true,
	"-source":   true, "-target": true,
	"--release": true,
}

// kotlincSafeFlags lists explicitly safe kotlinc flags. Compiler plugin
// loading (-plugin, -Xplugin), JVM pass-through (-J), and classpath-style
// path-list flags (-cp, -classpath) are absent so they defer. Unknown flags
// also defer (strict mode).
var kotlincSafeFlags = map[string]bool{
	"-d":         true,
	"-no-stdlib": true, "-no-reflect": true,
	"-verbose": true, "-version": true,
	"-nowarn": true, "-Werror": true,
	"-encoding":        true,
	"-java-parameters": true,
	"-module":          true,
	"-api-version":     true, "-language-version": true,
	"-progressive": true,
	"-jvm-target":  true,
	"--":           true,
}

var kotlincValueFlags = map[string]bool{
	"-d":           true,
	"-encoding":    true,
	"-module":      true,
	"-api-version": true, "-language-version": true,
	"-jvm-target": true,
}

// pylintSafeFlags lists explicitly safe Pylint flags. Plugin loading
// (--load-plugins, --init-hook), config file selection, and output reporter
// selection are absent so they defer. Unknown flags also defer (strict mode).
var pylintSafeFlags = map[string]bool{
	"-v": true, "--verbose": true,
	"-q": true, "--quiet": true,
	"-rn": true, "-ry": true,
	"-sn": true, "-sy": true,
	"-e": true, "--enable": true,
	"-d": true, "--disable": true,
	"-j": true, "--jobs": true,
	"-r": true, "--recursive": true,
	"--fail-under": true, "--fail-on": true,
	"--exit-zero": true,
	"--version":   true,
	"-h":          true, "--help": true,
	"--ignore": true, "--ignore-paths": true, "--ignore-patterns": true,
	"--msg-template": true,
	"--":             true,
}

var pylintHazardousFlags = map[string]bool{
	"-f": true, "--format": true, "--output-format": true,
}

var pylintValueFlags = map[string]bool{
	"-j": true, "--jobs": true,
	"--fail-under":   true,
	"--msg-template": true,
}

// clangTidySafeFlags lists explicitly safe clang-tidy flags. Plugin loading
// (--load, --plugin), fix application (--fix*), inline or file-backed
// configuration (--config, --config-file), and compiler argument pass-through
// (--extra-arg*) are absent so they defer. A compilation database (-p) can
// inject arbitrary compiler arguments, including dynamic plugin loading, and
// is explicitly hazardous. Unknown flags also defer (strict mode).
var clangTidySafeFlags = map[string]bool{
	"--quiet": true, "--version": true,
	"--list-checks": true, "--explain-config": true,
	"--show-categories":    true,
	"--checks":             true,
	"--warnings-as-errors": true,
	"--header-filter":      true,
	"--":                   true,
}

var clangTidyHazardousFlags = map[string]bool{
	"-p": true,
}

// cppcheckSafeFlags lists explicitly safe cppcheck flags. Addon loading
// (--addon), library definition (--library), and report output are absent
// so they defer. Unknown flags also defer (strict mode).
var cppcheckSafeFlags = map[string]bool{
	"-q": true, "--quiet": true,
	"-v": true, "--verbose": true,
	"--version":        true,
	"--inline-suppr":   true,
	"--xml":            true,
	"--error-exitcode": true,
	"--enable":         true, "--disable": true,
	"--suppress": true,
	"-D":         true, "-U": true, "-I": true,
	"-j": true, "--jobs": true,
	"--": true,
}

var cppcheckSafeFlagPrefixes = map[string]bool{
	"-D": true, // -DFOO
	"-U": true, // -UFOO
	"-I": true, // -I./include
}

var cppcheckValueFlags = map[string]bool{
	"-j": true, "--jobs": true,
	"-I":               true,
	"--error-exitcode": true,
}

var cmakeConfigureSafeFlags = map[string]bool{
	"-S": true, "-B": true, "-G": true,
	"--generator": true, "-Wno-dev": true, "-Wdev": true,
	"--warn-uninitialized": true, "--warn-unused-vars": true,
	"--no-warn-unused-cli": true,
}

var cmakeConfigureValueFlags = map[string]bool{
	"-S": true, "-B": true, "-G": true,
	"--generator": true,
}

var cmakeBuildSafeFlags = map[string]bool{
	"-j": true, "--parallel": true,
	"--verbose": true, "-v": true,
}

var cmakeBuildValueFlags = map[string]bool{
	"-j": true, "--parallel": true,
}

// mockgenSafeFlags lists explicitly safe mockgen flags. The -exec_only flag is
// absent because it selects an arbitrary program to execute. Unknown flags
// also defer (strict mode).
var mockgenSafeFlags = map[string]bool{
	"-source": true, "-destination": true, "-package": true,
	"-imports": true, "-aux_files": true, "-write_package_comment": true,
	"-self_package": true, "-copyright_file": true,
	"-mock_names": true, "-write_generate_directive": true,
	"-exclude_source": true, "--": true,
}
