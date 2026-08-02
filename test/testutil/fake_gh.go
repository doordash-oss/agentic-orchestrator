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

package testutil

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fakeGHLogEnv = "AGENTICO_TEST_FAKE_GH_LOG"

// FakeGHConfig configures the behavior and environment of an installed gh test double.
type FakeGHConfig struct {
	Behavior string
	Env      map[string]string
}

// FakeGH records invocations made to an installed gh test double.
type FakeGH struct {
	LogPath string
}

// InstallFakeGH installs a configurable gh test double at the front of PATH.
func InstallFakeGH(t testing.TB, config FakeGHConfig) FakeGH {
	t.Helper()
	binDir := t.TempDir()
	fixture := FakeGH{LogPath: filepath.Join(t.TempDir(), "gh.log")}
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$" + fakeGHLogEnv + "\"\n" + config.Behavior
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv(fakeGHLogEnv, fixture.LogPath)
	for key, value := range config.Env {
		t.Setenv(key, value)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return fixture
}

// Clear removes all recorded invocations from the fake gh log.
func (f FakeGH) Clear(t testing.TB) {
	t.Helper()
	if err := os.WriteFile(f.LogPath, nil, 0o644); err != nil {
		t.Fatalf("clear fake gh log: %v", err)
	}
}

// Invocations returns the command lines recorded by the fake gh executable.
func (f FakeGH) Invocations(t testing.TB) []string {
	t.Helper()
	data, err := os.ReadFile(f.LogPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read fake gh log: %v", err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// InvocationCount returns the number of command lines recorded by the fake gh executable.
func (f FakeGH) InvocationCount(t testing.TB) int {
	t.Helper()
	return len(f.Invocations(t))
}
