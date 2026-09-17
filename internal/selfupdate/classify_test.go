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
	"os"
	"path/filepath"
	"testing"
)

func eligibleTarballInputs() ClassifyInputs {
	return ClassifyInputs{
		ExecPath:         "/usr/local/bin/agentico",
		HasExec:          true,
		BinaryDir:        "/usr/local/bin",
		GoBinDir:         "/home/user/go/bin",
		BuildInfoVersion: "",
		InjectedVersion:  "1.2.3",
		GOOS:             "darwin",
		GOARCH:           "arm64",
		EUID:             1000,
		FileUID:          1000,
		FileMode:         0o755,
		DirWritable:      true,
		Lease:            LeaseHeld,
	}
}

func TestClassifyInstallationEligibleTarballAndGoInstall(t *testing.T) {
	t.Parallel()
	tarball := eligibleTarballInputs()
	if got := ClassifyInstallation(tarball); !got.Supported || got.Install != InstallTarball {
		t.Fatalf("tarball classification = %+v", got)
	}

	goInstall := eligibleTarballInputs()
	goInstall.InjectedVersion = ""
	goInstall.BuildInfoVersion = "v1.4.0"
	goInstall.BinaryDir = "/home/user/go/bin"
	goInstall.GoBinDir = "/home/user/go/bin"
	if got := ClassifyInstallation(goInstall); !got.Supported || got.Install != InstallGoInstall {
		t.Fatalf("go-install classification = %+v", got)
	}

	// A make-install binary in the Go bin dir without build info is still
	// go-install.
	makeInstall := eligibleTarballInputs()
	makeInstall.InjectedVersion = "1.2.3"
	makeInstall.BinaryDir = "/home/user/go/bin"
	makeInstall.GoBinDir = "/home/user/go/bin"
	if got := ClassifyInstallation(makeInstall); !got.Supported || got.Install != InstallGoInstall {
		t.Fatalf("make-install classification = %+v", got)
	}
}

func TestClassifyInstallationBundledWinsBeforeOtherClassifiers(t *testing.T) {
	t.Parallel()
	// A Homebrew-poured binary inside an app-bundle-shaped path is still
	// bundled: the canonical app-bundle/Electron resources membership is
	// checked first.
	bundled := eligibleTarballInputs()
	bundled.ExecPath = "/Applications/Agentico.app/Contents/Resources/bin/agentico"
	bundled.BinaryDir = "/Applications/Agentico.app/Contents/Resources/bin"
	bundled.BuildInfoVersion = "v1.4.0"
	got := ClassifyInstallation(bundled)
	if got.Supported || got.Install != InstallAppBundle || got.Reason != UnsupportedBundled {
		t.Fatalf("bundled classification = %+v", got)
	}
	if got.Remediation == "" {
		t.Fatal("bundled remediation is empty")
	}

	// Electron resources layout on Linux packaging.
	linuxBundled := eligibleTarballInputs()
	linuxBundled.ExecPath = "/opt/agentico/resources/bin/agentico"
	linuxBundled.BinaryDir = "/opt/agentico/resources/bin"
	linuxBundled.GOOS = "linux"
	got = ClassifyInstallation(linuxBundled)
	if got.Supported || got.Reason != UnsupportedBundled {
		t.Fatalf("linux bundled classification = %+v", got)
	}

	// A directly-resources-placed binary (second desktop candidate path).
	direct := eligibleTarballInputs()
	direct.ExecPath = "/opt/agentico/resources/agentico"
	direct.BinaryDir = "/opt/agentico/resources"
	got = ClassifyInstallation(direct)
	if got.Supported || got.Reason != UnsupportedBundled {
		t.Fatalf("direct resources classification = %+v", got)
	}

	// An unrelated binary in a directory named resources is NOT bundled.
	unrelated := eligibleTarballInputs()
	unrelated.ExecPath = "/home/user/resources/tool"
	unrelated.BinaryDir = "/home/user/resources"
	if got := ClassifyInstallation(unrelated); !got.Supported {
		t.Fatalf("unrelated resources dir misclassified: %+v", got)
	}
}

func TestClassifyInstallationHomebrew(t *testing.T) {
	t.Parallel()
	cellar := eligibleTarballInputs()
	cellar.ExecPath = "/opt/homebrew/Cellar/agentico/1.2.3/bin/agentico"
	cellar.BinaryDir = "/opt/homebrew/Cellar/agentico/1.2.3/bin"
	got := ClassifyInstallation(cellar)
	if got.Supported || got.Install != InstallHomebrew || got.Reason != UnsupportedHomebrew {
		t.Fatalf("cellar classification = %+v", got)
	}

	caskroom := eligibleTarballInputs()
	caskroom.ExecPath = "/usr/local/Caskroom/agentico/1.2.3/agentico"
	caskroom.BinaryDir = "/usr/local/Caskroom/agentico/1.2.3"
	if got := ClassifyInstallation(caskroom); got.Supported || got.Reason != UnsupportedHomebrew {
		t.Fatalf("caskroom classification = %+v", got)
	}
}

func TestClassifyInstallationDevelopmentAndPseudoVersions(t *testing.T) {
	t.Parallel()
	dev := eligibleTarballInputs()
	dev.InjectedVersion = "dev"
	dev.BuildInfoVersion = "(devel)"
	got := ClassifyInstallation(dev)
	if got.Supported || got.Reason != UnsupportedDevelopmentVersion {
		t.Fatalf("dev classification = %+v", got)
	}

	// A go-install pseudo-version has no clean comparable current version.
	pseudo := eligibleTarballInputs()
	pseudo.InjectedVersion = ""
	pseudo.BuildInfoVersion = "v0.0.0-20240101120000-abcdefabcdef"
	pseudo.BinaryDir = "/home/user/go/bin"
	pseudo.GoBinDir = "/home/user/go/bin"
	got = ClassifyInstallation(pseudo)
	if got.Supported || got.Reason != UnsupportedDevelopmentVersion {
		t.Fatalf("pseudo-version classification = %+v", got)
	}
}

func TestClassifyInstallationPlatformAndReplaceability(t *testing.T) {
	t.Parallel()
	windows := eligibleTarballInputs()
	windows.GOOS = "windows"
	if got := ClassifyInstallation(windows); got.Supported || got.Reason != UnsupportedPlatform {
		t.Fatalf("windows classification = %+v", got)
	}
	riscv := eligibleTarballInputs()
	riscv.GOARCH = "riscv64"
	if got := ClassifyInstallation(riscv); got.Supported || got.Reason != UnsupportedPlatform {
		t.Fatalf("riscv64 classification = %+v", got)
	}
	for _, goos := range []string{"darwin", "linux"} {
		for _, goarch := range []string{"amd64", "arm64"} {
			platform := eligibleTarballInputs()
			platform.GOOS, platform.GOARCH = goos, goarch
			if got := ClassifyInstallation(platform); !got.Supported {
				t.Fatalf("%s/%s should be supported, got %+v", goos, goarch, got)
			}
		}
	}

	// Root-owned binary under an unprivileged runtime is not replaceable.
	rootOwned := eligibleTarballInputs()
	rootOwned.FileUID = 0
	if got := ClassifyInstallation(rootOwned); got.Supported || got.Reason != UnsupportedNonReplaceable {
		t.Fatalf("root-owned classification = %+v", got)
	}
	// World/group-writable binary is not replaceable.
	worldWritable := eligibleTarballInputs()
	worldWritable.FileMode = 0o777
	if got := ClassifyInstallation(worldWritable); got.Supported || got.Reason != UnsupportedNonReplaceable {
		t.Fatalf("world-writable classification = %+v", got)
	}
	// Read-only containing directory blocks the atomic rename swap.
	readonlyDir := eligibleTarballInputs()
	readonlyDir.DirWritable = false
	if got := ClassifyInstallation(readonlyDir); got.Supported || got.Reason != UnsupportedNonReplaceable {
		t.Fatalf("read-only dir classification = %+v", got)
	}
}

func TestClassifyInstallationLeaseStates(t *testing.T) {
	t.Parallel()
	// Ownership contention (a live secondary runtime) is distinct from other
	// lease failures.
	contended := eligibleTarballInputs()
	contended.Lease = LeaseContended
	got := ClassifyInstallation(contended)
	if got.Supported || got.Reason != UnsupportedOwnershipContention {
		t.Fatalf("contention classification = %+v", got)
	}
	unavailable := eligibleTarballInputs()
	unavailable.Lease = LeaseUnavailable
	if got := ClassifyInstallation(unavailable); got.Supported || got.Reason != UnsupportedLeaseUnavailable {
		t.Fatalf("lease-unavailable classification = %+v", got)
	}
	noExec := eligibleTarballInputs()
	noExec.HasExec = false
	noExec.ExecPath = ""
	if got := ClassifyInstallation(noExec); got.Supported || got.Reason != UnsupportedLeaseUnavailable {
		t.Fatalf("no-exec classification = %+v", got)
	}
}

func TestLeaseStateFromErrorDistinguishesContention(t *testing.T) {
	t.Parallel()
	if got := LeaseStateFromError(true, nil); got != LeaseHeld {
		t.Fatalf("held = %v", got)
	}
	if got := LeaseStateFromError(false, &LeaseHeldError{}); got != LeaseContended {
		t.Fatalf("contention = %v", got)
	}
	if got := LeaseStateFromError(false, os.ErrPermission); got != LeaseUnavailable {
		t.Fatalf("other failure = %v", got)
	}
	if got := LeaseStateFromError(false, nil); got != LeaseUnavailable {
		t.Fatalf("no lease no error = %v", got)
	}
}

func TestCurrentReleaseVersionPrefersInjectedRelease(t *testing.T) {
	t.Parallel()
	if got := CurrentReleaseVersion("v0.9.0-rc1", "1.2.3"); got != "1.2.3" {
		t.Fatalf("current = %q, want 1.2.3", got)
	}
	if got := CurrentReleaseVersion("v1.4.0", ""); got != "1.4.0" {
		t.Fatalf("current = %q, want 1.4.0", got)
	}
	if got := CurrentReleaseVersion("(devel)", "dev"); got != "" {
		t.Fatalf("current = %q, want empty", got)
	}
}

func TestIsBundledAppResourcePathSymlinkedCanonicalPaths(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	real := filepath.Join(dir, "Agentico.app", "Contents", "Resources", "bin")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(real, "agentico")
	if err := os.WriteFile(binary, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "agentico-link")
	if err := os.Symlink(binary, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	resolved, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatal(err)
	}
	if !IsBundledAppResourcePath(resolved) {
		t.Fatalf("canonical resolved path %q should classify as bundled", resolved)
	}
}
