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
	"strconv"
	"strings"
)

// NormalizeVersion trims surrounding whitespace and a single leading "v" so
// ldflags/build-info versions and GitHub tags compare on equal footing.
func NormalizeVersion(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

// SameVersion reports whether two version strings are equal after
// normalization. A "dev" current never equals a release tag.
func SameVersion(a, b string) bool {
	return NormalizeVersion(a) == NormalizeVersion(b)
}

// IsRealModuleVersion reports whether a build-info Main.Version is a genuine
// module version. The Go toolchain only sets Main.Version to a tag or a
// pseudo-version when the binary was installed from the module proxy; local
// builds report "(devel)" (and tests/older toolchains may report ""). Trusting
// the toolchain here means commit-pinned `go install` (a pseudo-version) is
// correctly treated as go-install.
func IsRealModuleVersion(v string) bool {
	return v != "" && v != "(devel)"
}

// IsReleaseVersion reports whether v is a clean MAJOR.MINOR.PATCH release
// version after trimming surrounding whitespace and a single leading "v". It
// is deliberately conservative: any git-describe suffix (v1.2.3-5-gabc1234),
// -dirty marker, bare SHA, "dev", or empty string is NOT a release, so
// classifiers refuse rather than risk a wrong comparison. It delegates to
// ParseReleaseVersion so every caller shares one definition of "clean release
// version".
func IsReleaseVersion(v string) bool {
	_, ok := ParseReleaseVersion(v)
	return ok
}

// ParseReleaseVersion parses a clean MAJOR.MINOR.PATCH version into its three
// numeric components after trimming surrounding whitespace and a single
// leading "v". ok is false for anything that is not exactly three all-digit
// fields (a git-describe suffix, -dirty marker, bare SHA, "dev", go-install
// pseudo-version, or empty string) and for a field too large to fit an int,
// so callers never attempt to order versions they cannot reason about.
func ParseReleaseVersion(v string) (parts [3]int, ok bool) {
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

// CompareReleaseVersions orders two release versions numerically. cmp is -1,
// 0, or 1 for a<b, a==b, a>b, and ordered is true only when BOTH parse as
// clean MAJOR.MINOR.PATCH versions. When either side is not a clean release
// version (a go-install pseudo-version, a dev build, an empty string),
// ordered is false and the caller falls back to plain inequality rather than
// guessing an order.
func CompareReleaseVersions(a, b string) (cmp int, ordered bool) {
	pa, oka := ParseReleaseVersion(a)
	pb, okb := ParseReleaseVersion(b)
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

// SameDir reports whether two directories refer to the same location after
// path normalization. It is pure (filepath.Clean does no I/O); any symlink
// resolution must already have happened in the caller. An empty goBinDir
// never matches, so a binary with no resolvable directory is not mistaken for
// a go-install.
func SameDir(binaryDir, goBinDir string) bool {
	if binaryDir == "" || goBinDir == "" {
		return false
	}
	return filepath.Clean(binaryDir) == filepath.Clean(goBinDir)
}

// IsHomebrewPath reports whether the symlink-resolved binary path sits inside
// a Homebrew Cellar (<prefix>/Cellar/..., formula pours) or Caskroom
// (<prefix>/Caskroom/..., cask binary stanzas). Both segments are
// prefix-agnostic, covering /opt/homebrew, /usr/local, and Linuxbrew.
// Symlink resolution must already have happened in the caller so this stays
// pure and table-testable.
func IsHomebrewPath(resolvedBinaryPath string) bool {
	if resolvedBinaryPath == "" {
		return false
	}
	sep := string(os.PathSeparator)
	return strings.Contains(resolvedBinaryPath, sep+"Cellar"+sep) ||
		strings.Contains(resolvedBinaryPath, sep+"Caskroom"+sep)
}

// ResolveGoBinDir resolves the Go bin directory from the environment without
// invoking the `go` toolchain: GOBIN if set, else the first GOPATH entry +
// "/bin", else ~/go/bin. The getenv and home-dir lookups are injected so the
// precedence is unit-testable. It returns "" only when none of the three are
// resolvable.
func ResolveGoBinDir(getenv func(string) string, homeDir func() (string, error)) string {
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
