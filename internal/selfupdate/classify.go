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

package selfupdate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// InstallKind is the classified installation method of the running
// executable, as reported by the public update snapshot.
type InstallKind string

const (
	InstallTarball     InstallKind = "tarball"
	InstallGoInstall   InstallKind = "go_install"
	InstallHomebrew    InstallKind = "homebrew"
	InstallAppBundle   InstallKind = "app_bundle"
	InstallDevelopment InstallKind = "development"
	InstallUnknown     InstallKind = "unknown"
)

// UnsupportedReason is the machine-readable remediation code carried by an
// unsupported update snapshot.
type UnsupportedReason string

const (
	// UnsupportedBundled: the binary lives inside a desktop app bundle's
	// Electron resources; the desktop package owns its updates.
	UnsupportedBundled UnsupportedReason = "bundled"
	// UnsupportedHomebrew: the binary is managed by Homebrew.
	UnsupportedHomebrew UnsupportedReason = "homebrew"
	// UnsupportedDevelopmentVersion: dev builds and go-install
	// pseudo-versions have no clean comparable current version.
	UnsupportedDevelopmentVersion UnsupportedReason = "development_version"
	// UnsupportedPlatform: the GOOS/GOARCH pair is outside the supported
	// darwin/linux amd64/arm64 set.
	UnsupportedPlatform UnsupportedReason = "unsupported_platform"
	// UnsupportedNonReplaceable: ownership or permissions make an in-place
	// replacement unsafe.
	UnsupportedNonReplaceable UnsupportedReason = "non_replaceable"
	// UnsupportedOwnershipContention: another live runtime holds this
	// binary's update lease (a secondary runtime).
	UnsupportedOwnershipContention UnsupportedReason = "ownership_contention"
	// UnsupportedLeaseUnavailable: the binary lease could not be acquired
	// for reasons other than contention.
	UnsupportedLeaseUnavailable UnsupportedReason = "lease_unavailable"
)

// Eligibility is the outcome of classifying the running installation for
// update availability. Supported is true only for a tarball or go-install
// binary on a supported platform whose current version is a clean comparable
// release, whose file is safely replaceable by this runtime, and whose binary
// lease this runtime holds.
type Eligibility struct {
	Supported   bool
	Install     InstallKind
	Reason      UnsupportedReason
	Remediation string
}

// LeaseState describes this runtime's relationship to the binary-scoped
// update lease at classification time.
type LeaseState int

const (
	// LeaseHeld: this runtime holds the binary lease (acquired, adopted, or
	// retained through recovery).
	LeaseHeld LeaseState = iota
	// LeaseContended: another live runtime holds the lease — this process is
	// a secondary runtime serving from the same installed binary.
	LeaseContended
	// LeaseUnavailable: acquisition failed for reasons other than contention
	// (missing executable identity, filesystem failure).
	LeaseUnavailable
)

// ClassifyInputs are the resolved signals ClassifyInstallation consumes. All
// filesystem and environment access happens in the caller so the decision
// stays pure and table-testable.
type ClassifyInputs struct {
	// ExecPath is the canonical (symlink-resolved) executable path.
	ExecPath string
	// HasExec reports whether the executable identity could be captured at
	// all; false means no path-level checks are trustworthy.
	HasExec bool
	// BinaryDir is the directory of the canonical executable path.
	BinaryDir string
	// GoBinDir is the env-resolved Go bin directory.
	GoBinDir string
	// BuildInfoVersion is the build-info Main.Version.
	BuildInfoVersion string
	// InjectedVersion is the raw ldflags-injected version.
	InjectedVersion string
	GOOS            string
	GOARCH          string
	// EUID is the running process's effective uid.
	EUID int
	// FileUID is the uid owning the installed binary file.
	FileUID int
	// FileMode is the installed binary's permission bits.
	FileMode uint32
	// DirWritable reports whether the containing directory is writable by
	// the runtime (required for an atomic rename swap).
	DirWritable bool
	// Lease is this runtime's binary-lease state.
	Lease LeaseState
}

// remediation texts for each unsupported reason. Actionable, never exposing
// raw paths or environment values.
const (
	remediationBundled          = "This server runs from the desktop app's bundled resources, so desktop package updates own it. Install a standalone agentico binary to enable server self-update."
	remediationHomebrew         = "Update with your package manager: `brew update && brew upgrade agentico`."
	remediationDevelopment      = "Development and pseudo-version builds are never updated in place. Reinstall from a release tarball or `go install github.com/doordash-oss/agentic-orchestrator/cmd/agentico@latest`."
	remediationPlatform         = "In-place updates support darwin/amd64, darwin/arm64, linux/amd64, and linux/arm64 only."
	remediationNonReplaceable   = "The installed binary is not safely replaceable in place (ownership or permissions). Reinstall it under your own account in a directory you control."
	remediationContention       = "Another live runtime owns this binary's update lease. This runtime serves read-only availability; restart the owning runtime or install a separate executable copy."
	remediationLeaseUnavailable = "The binary update lease could not be acquired, so availability stays read-only. Check ownership and permissions of the .agentico-selfupdate directory next to the binary."
)

// ClassifyInstallation decides update eligibility from already-resolved
// signals. Classification order is deliberate: canonical app-bundle/Electron
// resources membership first (the desktop package owns those binaries no
// matter what other signals say), then Homebrew, then the go-install/tarball
// method split, then — only for otherwise-eligible tarball/go-install
// binaries — the version, platform, replaceability, and lease requirements.
// Ownership contention (a live secondary runtime) is distinguished from other
// lease failures instead of claiming every missing lease is a secondary
// runtime.
func ClassifyInstallation(in ClassifyInputs) Eligibility {
	unsupported := func(kind InstallKind, reason UnsupportedReason, remediation string) Eligibility {
		return Eligibility{Supported: false, Install: kind, Reason: reason, Remediation: remediation}
	}

	if !in.HasExec {
		return unsupported(InstallUnknown, UnsupportedLeaseUnavailable, remediationLeaseUnavailable)
	}
	if IsBundledAppResourcePath(in.ExecPath) {
		return unsupported(InstallAppBundle, UnsupportedBundled, remediationBundled)
	}
	if IsHomebrewPath(in.ExecPath) {
		return unsupported(InstallHomebrew, UnsupportedHomebrew, remediationHomebrew)
	}

	// Method split mirrors the legacy CLI classifier: homebrew and bundle
	// checks already ran above, so what remains is go-install, tarball, or
	// development.
	kind := InstallDevelopment
	if IsRealModuleVersion(in.BuildInfoVersion) || SameDir(in.BinaryDir, in.GoBinDir) {
		kind = InstallGoInstall
	} else if IsReleaseVersion(in.InjectedVersion) {
		kind = InstallTarball
	}

	// Eligible methods additionally require a clean comparable current
	// version: a go-install pseudo-version or dev build refuses rather than
	// risking an unordered comparison.
	current := CurrentReleaseVersion(in.BuildInfoVersion, in.InjectedVersion)
	if current == "" {
		return unsupported(kind, UnsupportedDevelopmentVersion, remediationDevelopment)
	}
	if !supportedPlatform(in.GOOS, in.GOARCH) {
		return unsupported(kind, UnsupportedPlatform, remediationPlatform)
	}
	if !replaceable(in) {
		return unsupported(kind, UnsupportedNonReplaceable, remediationNonReplaceable)
	}
	switch in.Lease {
	case LeaseHeld:
		return Eligibility{Supported: true, Install: kind}
	case LeaseContended:
		return unsupported(kind, UnsupportedOwnershipContention, remediationContention)
	default:
		return unsupported(kind, UnsupportedLeaseUnavailable, remediationLeaseUnavailable)
	}
}

// CurrentReleaseVersion returns the clean MAJOR.MINOR.PATCH form of the
// running build's version — preferring the injected ldflags release, then the
// build-info module version — or "" when neither is a clean release. The
// empty result means the running build has no version updates can be ordered
// against.
func CurrentReleaseVersion(buildInfoVersion, injectedVersion string) string {
	for _, v := range []string{injectedVersion, buildInfoVersion} {
		if IsReleaseVersion(v) {
			return NormalizeVersion(v)
		}
	}
	return ""
}

func supportedPlatform(goos, goarch string) bool {
	switch goarch {
	case "amd64", "arm64":
	default:
		return false
	}
	switch goos {
	case "darwin", "linux":
		return true
	default:
		return false
	}
}

// replaceable reports whether an in-place swap of the installed binary is
// safe for this runtime: it must own the file, the file must be an executable
// not writable by others, and the containing directory must be writable for
// the atomic rename.
func replaceable(in ClassifyInputs) bool {
	if in.FileUID != in.EUID {
		return false
	}
	if in.FileMode&0o111 == 0 {
		return false
	}
	if in.FileMode&0o022 != 0 {
		return false
	}
	return in.DirWritable
}

// IsBundledAppResourcePath reports whether a canonical executable path lives
// inside a desktop app bundle (macOS <Something>.app/Contents/...) or an
// Electron packaged resources tree (…/resources/bin/<exe> or
// …/resources/<exe>). The basename must be an agentico executable so an
// unrelated binary that merely sits in a directory named "resources" is not
// misclassified.
func IsBundledAppResourcePath(path string) bool {
	if path == "" {
		return false
	}
	// macOS app bundle: a path component ending in ".app" directly followed
	// by Contents (…/Agentico.app/Contents/Resources/…).
	segments := strings.Split(filepath.ToSlash(path), "/")
	for i := 0; i+1 < len(segments); i++ {
		if strings.HasSuffix(segments[i], ".app") && segments[i+1] == "Contents" {
			return true
		}
	}
	dir, base := filepath.Split(path)
	if base == "" {
		return false
	}
	parent := filepath.Base(strings.TrimSuffix(dir, sepPath))
	if parent == "bin" {
		parent = filepath.Base(filepath.Dir(strings.TrimSuffix(dir, sepPath)))
	}
	if parent != "resources" {
		return false
	}
	return base == "agentico" || base == "agentico.exe" || strings.HasPrefix(base, "agentico-")
}

// sepPath is the platform path separator, isolated for readability.
const sepPath = string(os.PathSeparator)

// LeaseStateFromError maps a binary-lease acquisition outcome to the
// classification lease state, distinguishing ownership contention (a live
// secondary runtime) from every other failure.
func LeaseStateFromError(held bool, acquireErr error) LeaseState {
	if held {
		return LeaseHeld
	}
	if acquireErr != nil && IsLeaseHeld(acquireErr) {
		return LeaseContended
	}
	return LeaseUnavailable
}

// DirWritable reports whether the runtime's effective uid can write to dir,
// which an atomic rename swap of the installed binary requires.
func DirWritable(dir string) bool {
	if dir == "" {
		return false
	}
	return unix.Access(dir, unix.W_OK) == nil
}

// DescribeEligibility renders a one-line human summary for logs; it never
// includes raw paths.
func (e Eligibility) DescribeEligibility() string {
	if e.Supported {
		return fmt.Sprintf("eligible %s installation", e.Install)
	}
	return fmt.Sprintf("unsupported %s installation: %s", e.Install, e.Reason)
}
