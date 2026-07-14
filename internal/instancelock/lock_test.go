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

package instancelock

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireRejectsConcurrentHolderAndReleasesOnClose(t *testing.T) {
	runtimeDir := t.TempDir()
	stateDir := filepath.Join(runtimeDir, "features")
	configPath := filepath.Join(runtimeDir, "config.yaml")

	first, acquired, firstOwner, err := Acquire(runtimeDir, stateDir, configPath, "test-version")
	if err != nil {
		t.Fatalf("Acquire first: %v", err)
	}
	if !acquired {
		t.Fatal("Acquire first acquired = false, want true")
	}
	if firstOwner.PID != os.Getpid() {
		t.Fatalf("first owner pid = %d, want %d", firstOwner.PID, os.Getpid())
	}
	t.Cleanup(func() { _ = first.Close() })

	second, acquired, secondOwner, err := Acquire(runtimeDir, stateDir, configPath, "test-version")
	if err != nil {
		t.Fatalf("Acquire second: %v", err)
	}
	if acquired {
		_ = second.Close()
		t.Fatal("Acquire second acquired = true, want false while first lock is held")
	}
	if secondOwner.PID != firstOwner.PID {
		t.Fatalf("second owner pid = %d, want %d", secondOwner.PID, firstOwner.PID)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("Close first: %v", err)
	}

	third, acquired, thirdOwner, err := Acquire(runtimeDir, stateDir, configPath, "test-version")
	if err != nil {
		t.Fatalf("Acquire third: %v", err)
	}
	if !acquired {
		t.Fatal("Acquire third acquired = false, want true after release")
	}
	defer third.Close()
	if thirdOwner.PID != os.Getpid() {
		t.Fatalf("third owner pid = %d, want %d", thirdOwner.PID, os.Getpid())
	}
}

func TestInstanceLockAllowsDifferentRuntimeParents(t *testing.T) {
	firstRuntime := t.TempDir()
	secondRuntime := t.TempDir()

	first, acquired, _, err := Acquire(
		firstRuntime,
		filepath.Join(firstRuntime, "features"),
		filepath.Join(firstRuntime, "config.yaml"),
		"test-version",
	)
	if err != nil {
		t.Fatalf("Acquire(first) error = %v", err)
	}
	if !acquired {
		t.Fatal("Acquire(first) acquired = false; want true")
	}
	t.Cleanup(func() { _ = first.Close() })

	second, acquired, _, err := Acquire(
		secondRuntime,
		filepath.Join(secondRuntime, "features"),
		filepath.Join(secondRuntime, "config.yaml"),
		"test-version",
	)
	if err != nil {
		t.Fatalf("Acquire(second) error = %v", err)
	}
	if !acquired {
		t.Fatal("Acquire(second) acquired = false; want true for separate runtime parent")
	}
	t.Cleanup(func() { _ = second.Close() })
}

func TestReadOwnerMissingMetadata(t *testing.T) {
	owner, err := ReadOwner(t.TempDir())
	if err != nil {
		t.Fatalf("ReadOwner: %v", err)
	}
	if owner.PID != 0 {
		t.Fatalf("owner pid = %d, want zero value", owner.PID)
	}
}
