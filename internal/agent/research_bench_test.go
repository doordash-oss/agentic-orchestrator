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

package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// BenchmarkBuildCodebaseIndex measures index build time on the real agentic repo.
func BenchmarkBuildCodebaseIndex(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in -short mode")
	}
	repoPath, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		b.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repoPath, "CLAUDE.md")); err != nil {
		b.Skipf("agentic repo not found at %s", repoPath)
	}

	b.ReportAllocs()
	for range b.N {
		idx, err := BuildCodebaseIndex(repoPath, 10*time.Second)
		if err != nil {
			b.Fatal(err)
		}
		if len(idx.Symbols) == 0 {
			b.Fatal("empty symbols")
		}
	}
}

// BenchmarkSaveLoadCodebaseIndex measures persistence performance.
func BenchmarkSaveLoadCodebaseIndex(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in -short mode")
	}
	repoPath, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		b.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repoPath, "CLAUDE.md")); err != nil {
		b.Skipf("agentic repo not found at %s", repoPath)
	}

	idx, err := BuildCodebaseIndex(repoPath, 10*time.Second)
	if err != nil {
		b.Fatal(err)
	}

	dir := b.TempDir()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := SaveCodebaseIndex(dir, idx); err != nil {
			b.Fatal(err)
		}
		loaded, err := LoadCodebaseIndex(dir)
		if err != nil {
			b.Fatal(err)
		}
		if len(loaded.Symbols) == 0 {
			b.Fatal("empty symbols after load")
		}
	}
}
