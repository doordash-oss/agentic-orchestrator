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
	// installMethodDevBuild: built from source; not safe to auto-update, so the
	// command refuses and points the user back at their build.
	installMethodDevBuild
)

// errResolveHome is returned by the home-dir resolver injected into
// resolveGoBinDir when the user home directory cannot be determined.
var errResolveHome = errors.New("home directory unavailable")

// classifyInstallMethod is the pure decision function at the heart of the
// update command. Given four already-resolved signals — the build-info module
// version, the raw injected ldflags version, the binary's containing directory,
// and the Go bin directory — it returns the install method. It performs no
// filesystem, network, or environment access of its own; symlink resolution and
// path normalization happen in the caller, so this stays hermetic and
// table-testable.
//
// Precedence (see the phase plan's Overview block):
//
//  1. go-install  if build info is a real module version, OR the binary sits in
//     the Go bin dir (which also captures `make install`).
//  2. tarball     else if the injected version is a clean MAJOR.MINOR.PATCH.
//  3. dev-refuse  otherwise.
func classifyInstallMethod(buildInfoVersion, injectedVersion, binaryDir, goBinDir string) installMethod {
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
// wrong swap.
func isReleaseVersion(v string) bool {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if !isAllDigits(p) {
			return false
		}
	}
	return true
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

// installInputs are the four signals classifyInstallMethod consumes for the
// running binary.
type installInputs struct {
	buildInfoVersion string
	injectedVersion  string
	binaryDir        string
	goBinDir         string
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
	}
}

// gatherInstallMethod resolves and classifies the running binary's install
// method. It is the production detector wired behind the updater seam.
func gatherInstallMethod() installMethod {
	in := gatherInstallInputs()
	return classifyInstallMethod(in.buildInfoVersion, in.injectedVersion, in.binaryDir, in.goBinDir)
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
		return "Would update by re-running the Go install of the latest version."
	case installMethodTarball:
		return "Would update by downloading the latest release and replacing the binary in place."
	case installMethodDevBuild:
		return "Would update from source (development builds are not updated automatically)."
	default:
		return ""
	}
}
