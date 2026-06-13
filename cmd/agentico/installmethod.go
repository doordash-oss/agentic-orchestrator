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
	"errors"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/tui"
)

// installMethod is how the running binary was installed, which decides the
// update action the command would take.
type installMethod int

const (
	// installMethodGoInstall: installed via `go install` (a real module version
	// in build info) or `make install` (lands under the Go bin dir).
	installMethodGoInstall installMethod = iota
	// installMethodTarball: a released binary distributed as a tarball, carrying
	// a clean injected release version.
	installMethodTarball
	// installMethodHomebrew: installed via the Homebrew tap; updates delegate to
	// `brew upgrade` rather than swapping the brew-managed binary in place.
	installMethodHomebrew
	// installMethodDevBuild: built from source; not safe to auto-update, so the
	// command refuses and points the user back at their build.
	installMethodDevBuild
)

// errResolveHome is returned by the home-dir resolver injected into
// resolveGoBinDir when the user home directory cannot be determined.
var errResolveHome = errors.New("home directory unavailable")

// classifyInstallMethod is the pure decision function at the heart of the
// update command. Given five already-resolved signals — the build-info module
// version, the raw injected ldflags version, the binary's containing directory,
// the Go bin directory, and whether the resolved binary lives inside a Homebrew
// Cellar — it returns the install method. It performs no filesystem, network, or
// environment access of its own; symlink resolution, path normalization, and the
// Homebrew-path check all happen in the caller, so this stays hermetic and
// table-testable.
//
// Precedence:
//
//  1. homebrew    if the resolved binary lives inside a Homebrew Cellar. Checked
//     first because the Cellar location is the most unambiguous signal: a
//     Homebrew-poured binary also carries a clean injected release version and,
//     for users whose Go bin dir coincides with the brew prefix, can sit in (or
//     resolve through) the Go bin dir — so any later go-install or tarball check
//     would misclassify it and the in-place swap would desync Homebrew's
//     bookkeeping.
//  2. go-install  else if build info is a real module version, OR the binary sits
//     in the Go bin dir (which also captures `make install`).
//  3. tarball     else if the injected version is a clean MAJOR.MINOR.PATCH.
//  4. dev-refuse  otherwise.
func classifyInstallMethod(buildInfoVersion, injectedVersion, binaryDir, goBinDir string, homebrew bool) installMethod {
	if homebrew {
		return installMethodHomebrew
	}
	if isRealModuleVersion(buildInfoVersion) || sameDir(binaryDir, goBinDir) {
		return installMethodGoInstall
	}
	if isReleaseVersion(injectedVersion) {
		return installMethodTarball
	}
	return installMethodDevBuild
}

// isRealModuleVersion reports whether a build-info Main.Version is a genuine
// module version. The Go toolchain only sets Main.Version to a tag or a
// pseudo-version when the binary was installed from the module proxy; local
// builds report "(devel)" (and tests/older toolchains may report ""). Trusting
// the toolchain here means commit-pinned `go install` (a pseudo-version) is
// correctly treated as go-install.
func isRealModuleVersion(v string) bool {
	return v != "" && v != "(devel)"
}

// isReleaseVersion reports whether v is a clean MAJOR.MINOR.PATCH release
// version after trimming surrounding whitespace and a single leading "v". It is
// hand-rolled — no new module dependency — and deliberately conservative: any
// git-describe suffix (v1.2.3-5-gabc1234), -dirty marker, bare SHA, "dev", or
// empty string is NOT a release, so the classifier refuses rather than risk a
// wrong swap. It delegates to parseReleaseVersion so the acceptance set and the
// downgrade-guard's ordering share one definition of "clean release version".
func isReleaseVersion(v string) bool {
	_, ok := parseReleaseVersion(v)
	return ok
}

// parseReleaseVersion parses a clean MAJOR.MINOR.PATCH version into its three
// numeric components after trimming surrounding whitespace and a single leading
// "v". ok is false for anything that is not exactly three all-digit fields (a
// git-describe suffix, -dirty marker, bare SHA, "dev", go-install pseudo-version,
// or empty string) and for a field too large to fit an int, so callers never
// attempt to order versions they cannot reason about.
func parseReleaseVersion(v string) (parts [3]int, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	fields := strings.Split(v, ".")
	if len(fields) != 3 {
		return parts, false
	}
	for i, f := range fields {
		if !isAllDigits(f) {
			return parts, false
		}
		n, err := strconv.Atoi(f)
		if err != nil {
			return parts, false
		}
		parts[i] = n
	}
	return parts, true
}

// compareReleaseVersions orders two release versions numerically. cmp is -1, 0,
// or 1 for a<b, a==b, a>b, and ordered is true only when BOTH parse as clean
// MAJOR.MINOR.PATCH versions. When either side is not a clean release version (a
// go-install pseudo-version, a dev build, an empty string), ordered is false and
// the caller falls back to plain inequality rather than guessing an order — so
// the downgrade guard engages only where the comparison is trustworthy.
func compareReleaseVersions(a, b string) (cmp int, ordered bool) {
	pa, oka := parseReleaseVersion(a)
	pb, okb := parseReleaseVersion(b)
	if !oka || !okb {
		return 0, false
	}
	for i := range 3 {
		switch {
		case pa[i] < pb[i]:
			return -1, true
		case pa[i] > pb[i]:
			return 1, true
		}
	}
	return 0, true
}

// isAllDigits reports whether s is a non-empty run of ASCII digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// sameDir reports whether two directories refer to the same location after
// path normalization. It is pure (filepath.Clean does no I/O); any symlink
// resolution must already have happened in the caller. An empty goBinDir never
// matches, so a binary with no resolvable directory is not mistaken for a
// go-install.
func sameDir(binaryDir, goBinDir string) bool {
	if binaryDir == "" || goBinDir == "" {
		return false
	}
	return filepath.Clean(binaryDir) == filepath.Clean(goBinDir)
}

// isHomebrewBinary reports whether the symlink-resolved binary path sits inside a
// Homebrew Cellar (<prefix>/Cellar/...). The "/Cellar/" segment is prefix-agnostic,
// covering /opt/homebrew, /usr/local, and Linuxbrew. Symlink resolution happens in
// the caller, so this stays pure and table-testable.
func isHomebrewBinary(resolvedBinaryPath string) bool {
	if resolvedBinaryPath == "" {
		return false
	}
	sep := string(os.PathSeparator)
	return strings.Contains(resolvedBinaryPath, sep+"Cellar"+sep)
}

// installInputs are the five signals classifyInstallMethod consumes for the
// running binary.
type installInputs struct {
	buildInfoVersion string
	injectedVersion  string
	binaryDir        string
	goBinDir         string
	homebrew         bool
}

// gatherInstallInputs resolves the classifier inputs for the running binary
// from real sources: the symlink-resolved executable directory, the build-info
// Main.Version, the raw injected ldflags version (read separately from the
// collapsed GetVersion accessor), and the env-resolved Go bin directory. It
// never invokes the `go` toolchain — release users typically have no Go
// installed.
func gatherInstallInputs() installInputs {
	return installInputs{
		buildInfoVersion: buildInfoMainVersion(),
		injectedVersion:  tui.InjectedVersion(),
		binaryDir:        normalizeDir(resolveBinaryDir()),
		goBinDir:         normalizeDir(resolveGoBinDir(os.Getenv, os.UserHomeDir)),
		homebrew:         isHomebrewBinary(resolveBinaryPath()),
	}
}

// gatherInstallMethod resolves and classifies the running binary's install
// method. It is the production detector wired behind the updater seam.
func gatherInstallMethod() installMethod {
	in := gatherInstallInputs()
	return classifyInstallMethod(in.buildInfoVersion, in.injectedVersion, in.binaryDir, in.goBinDir, in.homebrew)
}

// buildInfoMainVersion returns the build-info Main.Version, or "" when build
// info is unavailable.
func buildInfoMainVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		return info.Main.Version
	}
	return ""
}

// resolveBinaryPath returns the symlink-resolved path of the running
// executable — the file the tarball swap replaces in place. It returns "" when
// the executable path cannot be resolved, which the caller surfaces as a clear
// error that leaves the binary untouched.
func resolveBinaryPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved
	}
	return exe
}

// resolveBinaryDir returns the directory containing the running executable,
// resolving symlinks first so a binary reached through a symlink is compared on
// its real location. It returns "" when the executable path cannot be resolved.
func resolveBinaryDir() string {
	if p := resolveBinaryPath(); p != "" {
		return filepath.Dir(p)
	}
	return ""
}

// resolveGoBinDir resolves the Go bin directory from the environment without
// invoking the `go` toolchain: GOBIN if set, else the first GOPATH entry +
// "/bin", else ~/go/bin. The getenv and home-dir lookups are injected so the
// precedence is unit-testable. It returns "" only when none of the three are
// resolvable.
func resolveGoBinDir(getenv func(string) string, homeDir func() (string, error)) string {
	if gobin := strings.TrimSpace(getenv("GOBIN")); gobin != "" {
		return gobin
	}
	if gopath := strings.TrimSpace(getenv("GOPATH")); gopath != "" {
		if parts := filepath.SplitList(gopath); len(parts) > 0 {
			if first := strings.TrimSpace(parts[0]); first != "" {
				return filepath.Join(first, "bin")
			}
		}
	}
	if home, err := homeDir(); err == nil && home != "" {
		return filepath.Join(home, "go", "bin")
	}
	return ""
}

// normalizeDir best-effort resolves symlinks and normalizes a directory path so
// the classifier's pure comparison sees canonical forms. When the path does not
// exist (EvalSymlinks fails — common for a Go bin dir on a release machine), it
// falls back to filepath.Clean. An empty input stays empty.
func normalizeDir(dir string) string {
	if dir == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return resolved
	}
	return filepath.Clean(dir)
}

// label returns the human-facing name of the install method for --check output
// and the bare-update "not yet implemented" message.
func (m installMethod) label() string {
	switch m {
	case installMethodGoInstall:
		return "go install"
	case installMethodTarball:
		return "release tarball"
	case installMethodHomebrew:
		return "homebrew"
	case installMethodDevBuild:
		return "development build (built from source)"
	default:
		return "unknown"
	}
}

// wouldDoAction returns the action `agentico update --check` reports it would
// take for this install method.
func (m installMethod) wouldDoAction() string {
	switch m {
	case installMethodGoInstall:
		return "Would update by re-running the Go install of the latest version, falling back to the release tarball if Go is unavailable."
	case installMethodTarball:
		return "Would update by downloading the latest release and replacing the binary in place."
	case installMethodHomebrew:
		return "Would update via Homebrew by running `brew upgrade agentico`."
	case installMethodDevBuild:
		return "Would update from source (development builds are not updated automatically)."
	default:
		return ""
	}
}
