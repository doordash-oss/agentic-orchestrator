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
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestValidateServerName(t *testing.T) {
	t.Parallel()
	valid := []string{"frothy-macchiato", "a", strings.Repeat("x", MaxServerNameLength), "my server 01"}
	for _, name := range valid {
		if err := ValidateServerName(name); err != nil {
			t.Errorf("ValidateServerName(%q) error = %v; want nil", name, err)
		}
	}
	invalid := []string{
		"",
		"   ",
		strings.Repeat("x", MaxServerNameLength+1),
		"bad\nname",
		"bad\tname",
		"bad\x00name",
		"bad\x7fname",
	}
	for _, name := range invalid {
		if err := ValidateServerName(name); err == nil {
			t.Errorf("ValidateServerName(%q) = nil; want error", name)
		}
	}
}

func TestGenerateServerNameShape(t *testing.T) {
	t.Parallel()
	for i := 0; i < 20; i++ {
		name, err := GenerateServerName()
		if err != nil {
			t.Fatalf("GenerateServerName() error = %v", err)
		}
		parts := strings.Split(name, "-")
		if len(parts) < 2 {
			t.Fatalf("GenerateServerName() = %q; want adjective-coffee shape", name)
		}
		if err := ValidateServerName(name); err != nil {
			t.Fatalf("GenerateServerName() = %q fails validation: %v", name, err)
		}
	}
}

func TestEnsureServerNameGeneratesPersistsOwnerOnlyStableName(t *testing.T) {
	t.Parallel()
	runtimeDir := t.TempDir()

	first, err := EnsureServerName(runtimeDir)
	if err != nil {
		t.Fatalf("EnsureServerName() error = %v", err)
	}
	if err := ValidateServerName(first); err != nil {
		t.Fatalf("EnsureServerName() = %q fails validation: %v", first, err)
	}
	info, err := os.Stat(ServerNamePath(runtimeDir))
	if err != nil {
		t.Fatalf("stat server name file: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("server name file perms = %o; want 0600", info.Mode().Perm())
	}

	// A restart against the same runtime dir must reuse the persisted name.
	second, err := EnsureServerName(runtimeDir)
	if err != nil {
		t.Fatalf("EnsureServerName() second call error = %v", err)
	}
	if second != first {
		t.Fatalf("EnsureServerName() = %q after %q; want stable persisted name", second, first)
	}
}

func TestEnsureServerNameRegeneratesCorruptFile(t *testing.T) {
	t.Parallel()
	runtimeDir := t.TempDir()
	if err := os.WriteFile(ServerNamePath(runtimeDir), []byte("bad\nname\n"), 0o600); err != nil {
		t.Fatalf("write corrupt name file: %v", err)
	}
	name, err := EnsureServerName(runtimeDir)
	if err != nil {
		t.Fatalf("EnsureServerName() error = %v", err)
	}
	if err := ValidateServerName(name); err != nil {
		t.Fatalf("EnsureServerName() = %q fails validation: %v", name, err)
	}
	data, err := os.ReadFile(ServerNamePath(runtimeDir))
	if err != nil {
		t.Fatalf("read regenerated name file: %v", err)
	}
	if strings.TrimSpace(string(data)) != name {
		t.Fatalf("persisted name = %q; want regenerated %q", strings.TrimSpace(string(data)), name)
	}
}

func TestResolveServerNamePrecedence(t *testing.T) {
	t.Parallel()
	runtimeDir := t.TempDir()

	persisted, err := ResolveServerName("", "", runtimeDir)
	if err != nil {
		t.Fatalf("ResolveServerName(flagless) error = %v", err)
	}
	if err := ValidateServerName(persisted); err != nil {
		t.Fatalf("ResolveServerName(flagless) = %q fails validation: %v", persisted, err)
	}

	// Config override wins over the persisted name without rewriting it.
	configured, err := ResolveServerName("", "config-name", runtimeDir)
	if err != nil {
		t.Fatalf("ResolveServerName(config) error = %v", err)
	}
	if configured != "config-name" {
		t.Fatalf("ResolveServerName(config) = %q; want config-name", configured)
	}

	// Flag override wins over both, again without rewriting the file.
	flagged, err := ResolveServerName("  flag-name  ", "config-name", runtimeDir)
	if err != nil {
		t.Fatalf("ResolveServerName(flag) error = %v", err)
	}
	if flagged != "flag-name" {
		t.Fatalf("ResolveServerName(flag) = %q; want flag-name (trimmed)", flagged)
	}
	data, err := os.ReadFile(ServerNamePath(runtimeDir))
	if err != nil {
		t.Fatalf("read persisted name file: %v", err)
	}
	if strings.TrimSpace(string(data)) != persisted {
		t.Fatalf("persisted name = %q; overrides must not rewrite %q", strings.TrimSpace(string(data)), persisted)
	}

	// Once overrides are removed the persisted generated name returns.
	again, err := ResolveServerName("", "", runtimeDir)
	if err != nil {
		t.Fatalf("ResolveServerName(after overrides removed) error = %v", err)
	}
	if again != persisted {
		t.Fatalf("ResolveServerName(after overrides removed) = %q; want %q", again, persisted)
	}
}

func TestResolveServerNameRejectsInvalidExplicitOverrides(t *testing.T) {
	t.Parallel()
	runtimeDir := t.TempDir()
	if _, err := ResolveServerName("bad\nname", "", runtimeDir); err == nil {
		t.Error("ResolveServerName with invalid flag value succeeded; want error")
	}
	if _, err := ResolveServerName("", strings.Repeat("n", MaxServerNameLength+1), runtimeDir); err == nil {
		t.Error("ResolveServerName with invalid config value succeeded; want error")
	}
	// A valid flag override shields an invalid config value (flag wins).
	name, err := ResolveServerName("flag-name", "bad\nname", runtimeDir)
	if err != nil || name != "flag-name" {
		t.Errorf("ResolveServerName(flag shields config) = %q, %v; want flag-name, nil", name, err)
	}
}
