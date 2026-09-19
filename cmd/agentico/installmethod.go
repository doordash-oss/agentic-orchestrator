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

	"github.com/doordash-oss/agentic-orchestrator/internal/buildinfo"
	"github.com/doordash-oss/agentic-orchestrator/internal/selfupdate"
)

// installMethod is how the running binary was installed, which decides the
// package-manager guidance the read-only update bridge prints.
type installMethod int

const (
	// installMethodGoInstall: installed via `go install` (a real module version
	// in build info) or `make install` (lands under the Go bin dir).
	installMethodGoInstall installMethod = iota
	// installMethodTarball: a released binary distributed as a tarball, carrying
	// a clean injected release version.
	installMethodTarball
	// installMethodHomebrew: installed via the Homebrew tap; guidance points to
	// Homebrew rather than swapping the brew-managed binary in place.
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
// correctly treated as go-install. Thin legacy adapter over the shared
// selfupdate helper.
func isRealModuleVersion(v string) bool {
	return selfupdate.IsRealModuleVersion(v)
}

// isReleaseVersion reports whether v is a clean MAJOR.MINOR.PATCH release
// version. Thin legacy adapter over the shared selfupdate helper; see
// selfupdate.IsReleaseVersion for the conservative acceptance set.
func isReleaseVersion(v string) bool {
	return selfupdate.IsReleaseVersion(v)
}

// parseReleaseVersion parses a clean MAJOR.MINOR.PATCH version into its three
// numeric components. Thin legacy adapter over the shared selfupdate helper.
func parseReleaseVersion(v string) (parts [3]int, ok bool) {
	return selfupdate.ParseReleaseVersion(v)
}

// compareReleaseVersions orders two release versions numerically. Thin legacy
// adapter over the shared selfupdate helper.
func compareReleaseVersions(a, b string) (cmp int, ordered bool) {
	return selfupdate.CompareReleaseVersions(a, b)
}

// sameDir reports whether two directories refer to the same location after
// path normalization. Thin legacy adapter over the shared selfupdate helper.
func sameDir(binaryDir, goBinDir string) bool {
	return selfupdate.SameDir(binaryDir, goBinDir)
}

// isHomebrewBinary reports whether the symlink-resolved binary path sits inside
// a Homebrew Cellar or Caskroom. Thin legacy adapter over the shared
// selfupdate helper.
func isHomebrewBinary(resolvedBinaryPath string) bool {
	return selfupdate.IsHomebrewPath(resolvedBinaryPath)
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
		injectedVersion:  buildinfo.InjectedVersion(),
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
// invoking the `go` toolchain. Thin legacy adapter over the shared selfupdate
// helper.
func resolveGoBinDir(getenv func(string) string, homeDir func() (string, error)) string {
	return selfupdate.ResolveGoBinDir(getenv, homeDir)
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

// label returns the human-facing name of the install method for update output.
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

// wouldDoAction returns the package-manager or desktop guidance the read-only
// update bridge reports for this install method.
func (m installMethod) wouldDoAction() string {
	switch m {
	case installMethodGoInstall:
		return "Run `go install github.com/doordash-oss/agentic-orchestrator/cmd/agentico@latest` for a standalone headless CLI, or install the signed desktop package from GitHub Releases."
	case installMethodTarball:
		return "Download the signed artifact for your OS and architecture from GitHub Releases, then verify its checksum and signature before replacing this standalone CLI."
	case installMethodHomebrew:
		return "Update with your package manager: `brew update && brew upgrade agentico`."
	case installMethodDevBuild:
		return "Development builds are not changed by this command. Pull the source checkout and rebuild, or install the signed desktop package from GitHub Releases."
	default:
		return ""
	}
}
