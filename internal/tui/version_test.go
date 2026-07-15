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

package tui

import "testing"

// setBuildIdentity temporarily overrides the ldflags-injected build identity
// vars, restoring them when the test ends.
func setBuildIdentity(t *testing.T, v, rev string) {
	t.Helper()
	prevVersion, prevRevision := version, revision
	version, revision = v, rev
	t.Cleanup(func() { version, revision = prevVersion, prevRevision })
}

func TestGetRevisionPrefersInjectedValue(t *testing.T) {
	setBuildIdentity(t, "1.2.3", "981fa3290a2f2991f13ebd1d6c6f374f2a30bffe")
	if got := GetRevision(); got != "981fa3290a2f2991f13ebd1d6c6f374f2a30bffe" {
		t.Fatalf("GetRevision() = %q, want injected revision", got)
	}
}

func TestGetRevisionWithoutInjectionFallsBackToBuildInfo(t *testing.T) {
	setBuildIdentity(t, "", "")
	// Test binaries carry no ldflags injection and no vcs stamp, so the
	// accessor must degrade to empty rather than fabricating identity.
	if got := GetRevision(); got != "" {
		t.Fatalf("GetRevision() = %q, want \"\" in unstamped test binary", got)
	}
}

func TestVersionLineIncludesRevisionWhenKnown(t *testing.T) {
	setBuildIdentity(t, "v0.148.0-30-g981fa32", "981fa3290a2f2991f13ebd1d6c6f374f2a30bffe")
	want := "agentico vv0.148.0-30-g981fa32 (revision 981fa3290a2f2991f13ebd1d6c6f374f2a30bffe)"
	if got := VersionLine(); got != want {
		t.Fatalf("VersionLine() = %q, want %q", got, want)
	}
}

func TestVersionLineOmitsUnknownRevision(t *testing.T) {
	setBuildIdentity(t, "1.2.3", "")
	if got := VersionLine(); got != "agentico v1.2.3" {
		t.Fatalf("VersionLine() = %q, want %q", got, "agentico v1.2.3")
	}
}
