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

package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// newRegistryFixture builds a registry dir under a fresh parent plus the
// runtime identity of a server publishing into it.
func newRegistryFixture(t *testing.T) (string, RuntimeIdentity) {
	t.Helper()
	parent := t.TempDir()
	registryDir := RegistryDir(parent)
	runtimeDir := filepath.Join(parent, "runtime-a")
	identity := RuntimeIdentity{
		RuntimeDir: runtimeDir,
		StateDir:   filepath.Join(runtimeDir, "features"),
		Config:     filepath.Join(runtimeDir, "config.yaml"),
	}
	if err := os.MkdirAll(identity.StateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	return registryDir, identity
}

func readRegistryEntry(t *testing.T, registryDir string, identity RuntimeIdentity) DiscoveryRecord {
	t.Helper()
	data, err := os.ReadFile(RegistryEntryPath(registryDir, identity.RuntimeDir))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var rec DiscoveryRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	return rec
}

func registryEntries(t *testing.T, registryDir string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(registryDir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	var out []os.DirEntry
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e)
		}
	}
	return out
}

func TestPublishRegistryEntryWritesVerbatimRecord(t *testing.T) {
	t.Parallel()
	registryDir, identity := newRegistryFixture(t)
	rec := newDiscoveryRecord(identity, NewLaunchPolicy([]string{providerCodex}, false))
	rec.AuthToken = "secret-token"
	rec.Name = "alpha"
	rec.PID = os.Getpid()

	if err := PublishRegistryEntry(registryDir, rec); err != nil {
		t.Fatalf("PublishRegistryEntry() error = %v", err)
	}

	got := readRegistryEntry(t, registryDir, identity)
	if !reflect.DeepEqual(got, rec) {
		t.Errorf("registry entry = %+v; want verbatim copy %+v", got, rec)
	}
	if got.Name != "alpha" {
		t.Errorf("registry entry name = %q; want alpha", got.Name)
	}
	if n := len(registryEntries(t, registryDir)); n != 1 {
		t.Fatalf("registry entries = %d; want exactly 1", n)
	}

	info, err := os.Stat(RegistryEntryPath(registryDir, identity.RuntimeDir))
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("entry permissions = %o; want 600", perm)
	}
	dirInfo, err := os.Stat(registryDir)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("registry dir permissions = %o; want 700", perm)
	}
}

func TestPublishRegistryEntryTwoServersCoexist(t *testing.T) {
	t.Parallel()
	registryDir, identityA := newRegistryFixture(t)
	recA := newDiscoveryRecord(identityA, NewLaunchPolicy(nil, false))
	recA.Name = "alpha"

	runtimeDirB := filepath.Join(filepath.Dir(identityA.RuntimeDir), "runtime-b")
	identityB := RuntimeIdentity{
		RuntimeDir: runtimeDirB,
		StateDir:   filepath.Join(runtimeDirB, "features"),
		Config:     filepath.Join(runtimeDirB, "config.yaml"),
	}
	recB := newDiscoveryRecord(identityB, NewLaunchPolicy(nil, false))
	recB.Name = "beta"

	if err := PublishRegistryEntry(registryDir, recA); err != nil {
		t.Fatalf("PublishRegistryEntry(A) error = %v", err)
	}
	if err := PublishRegistryEntry(registryDir, recB); err != nil {
		t.Fatalf("PublishRegistryEntry(B) error = %v", err)
	}
	if n := len(registryEntries(t, registryDir)); n != 2 {
		t.Fatalf("registry entries = %d; want 2 (one per runtime)", n)
	}
	if got := readRegistryEntry(t, registryDir, identityA); got.Name != "alpha" {
		t.Errorf("entry A name = %q; want alpha", got.Name)
	}
	if got := readRegistryEntry(t, registryDir, identityB); got.Name != "beta" {
		t.Errorf("entry B name = %q; want beta", got.Name)
	}
}

func TestPublishRegistryEntryRestartOverwritesInPlace(t *testing.T) {
	t.Parallel()
	registryDir, identity := newRegistryFixture(t)
	first := newDiscoveryRecord(identity, NewLaunchPolicy(nil, false))
	first.Name = "alpha"
	if err := PublishRegistryEntry(registryDir, first); err != nil {
		t.Fatalf("PublishRegistryEntry(first) error = %v", err)
	}

	second := newDiscoveryRecord(identity, NewLaunchPolicy(nil, false))
	second.Name = "alpha-renamed"
	second.PID = 4242
	if err := PublishRegistryEntry(registryDir, second); err != nil {
		t.Fatalf("PublishRegistryEntry(second) error = %v", err)
	}

	if n := len(registryEntries(t, registryDir)); n != 1 {
		t.Fatalf("registry entries = %d; want 1 (restart reuses the entry)", n)
	}
	got := readRegistryEntry(t, registryDir, identity)
	if !reflect.DeepEqual(got, second) {
		t.Errorf("registry entry = %+v; want overwritten %+v", got, second)
	}
	// Atomicity: no temp files survive a successful publish.
	all, err := os.ReadDir(registryDir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, e := range all {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("stray temp file %q after publish", e.Name())
		}
	}
}

func TestPublishRegistryEntrySelfHealsDirPermissions(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	registryDir, identity := newRegistryFixture(t)
	if err := os.MkdirAll(registryDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	rec := newDiscoveryRecord(identity, NewLaunchPolicy(nil, false))
	if err := PublishRegistryEntry(registryDir, rec); err != nil {
		t.Fatalf("PublishRegistryEntry() error = %v", err)
	}
	dirInfo, err := os.Stat(registryDir)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("registry dir permissions = %o; want self-healed 700", perm)
	}
}

func TestPublishRegistryEntryUnwritableParentFailsCleanly(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	if os.Geteuid() == 0 {
		t.Skip("root ignores permission bits")
	}
	_, identity := newRegistryFixture(t)
	regDir := RegistryDir(parent)
	rec := newDiscoveryRecord(identity, NewLaunchPolicy(nil, false))
	if err := PublishRegistryEntry(regDir, rec); err == nil {
		t.Fatal("PublishRegistryEntry() error = nil; want unwritable parent failure")
	}
}

func TestPublishRegistryEntryRejectsEmptyArgs(t *testing.T) {
	t.Parallel()
	rec := newDiscoveryRecord(RuntimeIdentity{}, NewLaunchPolicy(nil, false))
	if err := PublishRegistryEntry("", rec); err == nil {
		t.Fatal("PublishRegistryEntry() error = nil; want empty registry dir failure")
	}
	registryDir, _ := newRegistryFixture(t)
	if err := PublishRegistryEntry(registryDir, rec); err == nil {
		t.Fatal("PublishRegistryEntry() error = nil; want empty runtime dir failure")
	}
}

func TestRemoveRegistryEntry(t *testing.T) {
	t.Parallel()
	registryDir, identity := newRegistryFixture(t)
	rec := newDiscoveryRecord(identity, NewLaunchPolicy(nil, false))
	if err := PublishRegistryEntry(registryDir, rec); err != nil {
		t.Fatalf("PublishRegistryEntry() error = %v", err)
	}
	if err := RemoveRegistryEntry(registryDir, identity.RuntimeDir); err != nil {
		t.Fatalf("RemoveRegistryEntry() error = %v", err)
	}
	if n := len(registryEntries(t, registryDir)); n != 0 {
		t.Fatalf("registry entries = %d; want 0 after removal", n)
	}
	// Idempotent: a second removal (e.g. crash-then-shutdown) is not an error.
	if err := RemoveRegistryEntry(registryDir, identity.RuntimeDir); err != nil {
		t.Fatalf("RemoveRegistryEntry(second) error = %v", err)
	}
}
