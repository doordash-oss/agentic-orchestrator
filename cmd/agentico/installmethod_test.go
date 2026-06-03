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
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/tui"
)

// --- Task 1: hand-rolled release-version predicate -------------------------

func TestIsReleaseVersion(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		// Clean MAJOR.MINOR.PATCH, with and without the optional leading v.
		{"1.2.3", true},
		{"v1.2.3", true},
		{" v1.2.3 ", true},
		{"0.0.0", true},
		{"v10.20.30", true},
		// Not three clean numeric components.
		{"1.2", false},
		{"v1.2", false},
		{"1.2.3.4", false},
		{"1.2.x", false},
		// git-describe / dirty / SHA / dev / empty all fall through to refuse.
		{"v1.2.3-5-gabc1234", false},
		{"1.2.3-5-gabc1234", false},
		{"1.2.3-dirty", false},
		{"abc1234", false},
		{"dev", false},
		{"", false},
		{"   ", false},
	}
	for _, tt := range tests {
		if got := isReleaseVersion(tt.in); got != tt.want {
			t.Errorf("isReleaseVersion(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

// --- downgrade guard: release-version parsing and ordering -----------------

func TestParseReleaseVersion(t *testing.T) {
	tests := []struct {
		in        string
		wantParts [3]int
		wantOK    bool
	}{
		{"1.2.3", [3]int{1, 2, 3}, true},
		{"v1.2.3", [3]int{1, 2, 3}, true},
		{" v10.20.30 ", [3]int{10, 20, 30}, true},
		{"0.0.0", [3]int{0, 0, 0}, true},
		{"02.0.0", [3]int{2, 0, 0}, true}, // leading zeros parse numerically
		// Anything not exactly three numeric fields is unparseable.
		{"1.2", [3]int{}, false},
		{"1.2.3.4", [3]int{}, false},
		{"1.2.x", [3]int{}, false},
		{"v1.2.3-5-gabc1234", [3]int{}, false},
		{"dev", [3]int{}, false},
		{"", [3]int{}, false},
		{"99999999999999999999.0.0", [3]int{}, false}, // overflows int
	}
	for _, tt := range tests {
		parts, ok := parseReleaseVersion(tt.in)
		if ok != tt.wantOK || (ok && parts != tt.wantParts) {
			t.Errorf("parseReleaseVersion(%q) = %v,%v want %v,%v", tt.in, parts, ok, tt.wantParts, tt.wantOK)
		}
	}
}

func TestCompareReleaseVersions(t *testing.T) {
	tests := []struct {
		a, b        string
		wantCmp     int
		wantOrdered bool
	}{
		{"1.0.0", "2.0.0", -1, true},
		{"2.0.0", "1.0.0", 1, true},
		{"2.0.0", "2.0.0", 0, true},
		{"v2.0.0", "2.0.0", 0, true}, // leading-v normalized
		{"1.2.3", "1.3.0", -1, true},
		{"1.2.3", "1.2.4", -1, true},
		{"1.10.0", "1.9.0", 1, true}, // numeric, not lexical
		// Unorderable when either side is not a clean release version.
		{"dev", "1.0.0", 0, false},
		{"v0.0.0-20230101000000-abcdef123456", "1.0.0", 0, false},
		{"1.0.0", "", 0, false},
	}
	for _, tt := range tests {
		cmp, ordered := compareReleaseVersions(tt.a, tt.b)
		if ordered != tt.wantOrdered || (ordered && cmp != tt.wantCmp) {
			t.Errorf("compareReleaseVersions(%q,%q) = %d,%v want %d,%v", tt.a, tt.b, cmp, ordered, tt.wantCmp, tt.wantOrdered)
		}
	}
}

// --- Task 1: build-info "real module version" predicate --------------------

func TestIsRealModuleVersion(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"(devel)", false},
		{"v1.2.3", true},
		{"v0.0.0-20230101000000-abcdef123456", true}, // commit-pinned pseudo-version
	}
	for _, tt := range tests {
		if got := isRealModuleVersion(tt.in); got != tt.want {
			t.Errorf("isRealModuleVersion(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

// --- Task 1: the pure classifier, every branch -----------------------------

func TestClassifyInstallMethod(t *testing.T) {
	const goBin = "/home/user/go/bin"
	tests := []struct {
		name             string
		buildInfoVersion string
		injectedVersion  string
		binaryDir        string
		goBinDir         string
		want             installMethod
	}{
		{
			name:             "go install @vX.Y.Z (build-info semver)",
			buildInfoVersion: "v1.2.3",
			injectedVersion:  "",
			binaryDir:        goBin,
			goBinDir:         goBin,
			want:             installMethodGoInstall,
		},
		{
			name:             "go install commit-pinned pseudo-version",
			buildInfoVersion: "v0.0.0-20230101000000-abcdef123456",
			injectedVersion:  "",
			binaryDir:        "/usr/local/bin",
			goBinDir:         goBin,
			want:             installMethodGoInstall,
		},
		{
			name:             "make install: (devel) build-info, dirty injected, binary under go bin dir",
			buildInfoVersion: "(devel)",
			injectedVersion:  "v1.2.3-5-gabc1234-dirty",
			binaryDir:        goBin,
			goBinDir:         goBin,
			want:             installMethodGoInstall,
		},
		{
			name:             "make install: (devel) build-info, empty injected, binary under go bin dir",
			buildInfoVersion: "(devel)",
			injectedVersion:  "",
			binaryDir:        goBin,
			goBinDir:         goBin,
			want:             installMethodGoInstall,
		},
		{
			name:             "go bin dir match survives trailing-slash / unclean paths",
			buildInfoVersion: "(devel)",
			injectedVersion:  "",
			binaryDir:        goBin + "/",
			goBinDir:         "/home/user/go/bin/../bin",
			want:             installMethodGoInstall,
		},
		{
			name:             "clean tag outside go bin dir -> tarball",
			buildInfoVersion: "(devel)",
			injectedVersion:  "1.2.3",
			binaryDir:        "/usr/local/bin",
			goBinDir:         goBin,
			want:             installMethodTarball,
		},
		{
			name:             "clean tag with leading v outside go bin dir -> tarball",
			buildInfoVersion: "",
			injectedVersion:  "v2.0.1",
			binaryDir:        "/opt/agentico/bin",
			goBinDir:         goBin,
			want:             installMethodTarball,
		},
		{
			name:             "refuse: dev injected, no build-info, outside go bin dir",
			buildInfoVersion: "(devel)",
			injectedVersion:  "dev",
			binaryDir:        "/tmp/build",
			goBinDir:         goBin,
			want:             installMethodDevBuild,
		},
		{
			name:             "refuse: bare SHA injected",
			buildInfoVersion: "",
			injectedVersion:  "abc1234",
			binaryDir:        "/tmp/build",
			goBinDir:         goBin,
			want:             installMethodDevBuild,
		},
		{
			name:             "refuse: -dirty injected",
			buildInfoVersion: "(devel)",
			injectedVersion:  "1.2.3-dirty",
			binaryDir:        "/tmp/build",
			goBinDir:         goBin,
			want:             installMethodDevBuild,
		},
		{
			name:             "refuse: git-describe suffix injected",
			buildInfoVersion: "(devel)",
			injectedVersion:  "v1.2.3-5-gabc1234",
			binaryDir:        "/tmp/build",
			goBinDir:         goBin,
			want:             installMethodDevBuild,
		},
		{
			name:             "refuse: everything empty",
			buildInfoVersion: "",
			injectedVersion:  "",
			binaryDir:        "/tmp/build",
			goBinDir:         goBin,
			want:             installMethodDevBuild,
		},
		{
			name:             "empty dirs never falsely match as go-install",
			buildInfoVersion: "(devel)",
			injectedVersion:  "",
			binaryDir:        "",
			goBinDir:         "",
			want:             installMethodDevBuild,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyInstallMethod(tt.buildInfoVersion, tt.injectedVersion, tt.binaryDir, tt.goBinDir)
			if got != tt.want {
				t.Errorf("classifyInstallMethod(%q, %q, %q, %q) = %v, want %v",
					tt.buildInfoVersion, tt.injectedVersion, tt.binaryDir, tt.goBinDir, got, tt.want)
			}
		})
	}
}

// --- Task 2: env-resolved Go bin dir (no `go` toolchain invocation) --------

func TestResolveGoBinDir(t *testing.T) {
	const home = "/home/user"
	homeOK := func() (string, error) { return home, nil }

	multi := "/first/gopath" + string(filepath.ListSeparator) + "/second/gopath"

	tests := []struct {
		name string
		env  map[string]string
		home func() (string, error)
		want string
	}{
		{
			name: "GOBIN wins when set",
			env:  map[string]string{"GOBIN": "/explicit/gobin", "GOPATH": "/ignored"},
			home: homeOK,
			want: "/explicit/gobin",
		},
		{
			name: "first GOPATH entry + /bin when GOBIN unset",
			env:  map[string]string{"GOPATH": "/single/gopath"},
			home: homeOK,
			want: filepath.Join("/single/gopath", "bin"),
		},
		{
			name: "first of a multi-entry GOPATH",
			env:  map[string]string{"GOPATH": multi},
			home: homeOK,
			want: filepath.Join("/first/gopath", "bin"),
		},
		{
			name: "~/go/bin fallback when GOBIN and GOPATH unset",
			env:  map[string]string{},
			home: homeOK,
			want: filepath.Join(home, "go", "bin"),
		},
		{
			name: "empty when nothing resolvable",
			env:  map[string]string{},
			home: func() (string, error) { return "", errResolveHome },
			want: "",
		},
		{
			name: "whitespace-only GOBIN/GOPATH ignored, falls to home",
			env:  map[string]string{"GOBIN": "  ", "GOPATH": "   "},
			home: homeOK,
			want: filepath.Join(home, "go", "bin"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(k string) string { return tt.env[k] }
			if got := resolveGoBinDir(getenv, tt.home); got != tt.want {
				t.Errorf("resolveGoBinDir() = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- Task 2: production gatherer drives the classifier on real inputs -------

// The `go test` binary is inherently a development build: build info reports
// "(devel)", no release version is injected via ldflags, and it lives in a temp
// build dir rather than the Go bin dir. So the real-input gatherer chain must
// classify it as a dev build, proving detection wires real signals into the
// Task 1 classifier without simulation.
func TestGatherInstallMethodClassifiesTestBinaryAsDevBuild(t *testing.T) {
	if got := gatherInstallMethod(); got != installMethodDevBuild {
		t.Fatalf("gatherInstallMethod() = %v, want installMethodDevBuild for the test binary", got)
	}
}

// gatherInstallInputs must read real signals: a non-empty binary directory (the
// executable always resolves under test) and the raw injected version straight
// from the tui accessor, kept separate from the collapsed GetVersion.
func TestGatherInstallInputsReadsRealSignals(t *testing.T) {
	in := gatherInstallInputs()
	if in.binaryDir == "" {
		t.Error("gatherInstallInputs() binaryDir is empty; expected a resolved executable directory")
	}
	if in.injectedVersion != tui.InjectedVersion() {
		t.Errorf("injectedVersion = %q, want raw tui.InjectedVersion() %q", in.injectedVersion, tui.InjectedVersion())
	}
	if in.buildInfoVersion != "(devel)" {
		t.Errorf("buildInfoVersion = %q, want %q for the test binary", in.buildInfoVersion, "(devel)")
	}
}
