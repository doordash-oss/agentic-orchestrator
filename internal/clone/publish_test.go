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

package clone

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRenameNoReplacePublishesAtomically(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	staging := filepath.Join(dir, "staging")
	dst := filepath.Join(dir, "dst")
	if err := os.MkdirAll(filepath.Join(staging, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RenameNoReplace(staging, dst); err != nil {
		t.Fatalf("RenameNoReplace: %v", err)
	}
	if _, err := os.Lstat(staging); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("staging still present: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dst, ".git", "HEAD"))
	if err != nil || string(data) != "ref: refs/heads/main\n" {
		t.Fatalf("published content wrong: %v %q", err, data)
	}
}

func TestRenameNoReplaceRefusesExistingDestination(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	staging := filepath.Join(dir, "staging")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}

	// A populated destination.
	populated := filepath.Join(dir, "populated")
	if err := os.MkdirAll(populated, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(populated, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RenameNoReplace(staging, populated); !errors.Is(err, ErrDestinationExists) {
		t.Errorf("populated destination: err = %v, want ErrDestinationExists", err)
	}
	if _, err := os.Stat(filepath.Join(populated, "keep.txt")); err != nil {
		t.Errorf("existing destination data damaged: %v", err)
	}
	if _, err := os.Lstat(staging); err != nil {
		t.Errorf("staging removed on refused publication: %v", err)
	}

	// An empty directory destination is still never replaced.
	empty := filepath.Join(dir, "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RenameNoReplace(staging, empty); !errors.Is(err, ErrDestinationExists) {
		t.Errorf("empty destination: err = %v, want ErrDestinationExists", err)
	}
	if _, err := os.Lstat(empty); err != nil {
		t.Errorf("empty destination removed: %v", err)
	}

	// A symlink destination (even dangling) is never replaced or followed.
	link := filepath.Join(dir, "link")
	if err := os.Symlink(filepath.Join(dir, "nowhere"), link); err != nil {
		t.Fatal(err)
	}
	if err := RenameNoReplace(staging, link); !errors.Is(err, ErrDestinationExists) {
		t.Errorf("symlink destination: err = %v, want ErrDestinationExists", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("symlink destination removed: %v", err)
	}
}

func TestOpenRootDirPinsIdentityAcrossSymlinkSwap(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	handle, identity, err := OpenRootDir(realDir)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()

	// Swap the path with a symlink to a different tree: the pinned handle
	// keeps referring to the original directory.
	other := filepath.Join(dir, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(realDir, filepath.Join(dir, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, realDir); err != nil {
		t.Fatal(err)
	}
	if !SameDirIdentity(handle, identity) {
		t.Error("pinned handle lost its identity after a symlink swap")
	}
}
