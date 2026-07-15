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

package git

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInitRepositoryCreatesGitRepo(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "new-repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := InitRepository(dir); err != nil {
		t.Fatalf("InitRepository() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("expected .git after InitRepository: %v", err)
	}
}

func TestInitRepositoryFailsOnMissingDirectory(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	if err := InitRepository(dir); err == nil {
		t.Fatal("InitRepository() error = nil; want failure for missing directory")
	}
}
