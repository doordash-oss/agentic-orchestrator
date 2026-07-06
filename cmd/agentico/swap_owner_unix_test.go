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

//go:build unix

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// On unix, fileOwnerIDs reports the uid/gid that own a freshly created file —
// the invoking user — so the swap can preserve ownership.
func TestFileOwnerIDsUnix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	uid, gid, ok := fileOwnerIDs(info)
	if !ok {
		t.Fatal("fileOwnerIDs must report ok on unix")
	}
	// A file you create is owned by your uid. The gid is intentionally not pinned
	// to os.Getgid(): BSD/macOS assigns a new file the *parent directory's* group
	// rather than the process group, so we only assert it is read as a valid
	// non-negative id.
	if uid != os.Getuid() {
		t.Errorf("fileOwnerIDs uid = %d, want %d", uid, os.Getuid())
	}
	if gid < 0 {
		t.Errorf("fileOwnerIDs gid = %d, want a valid gid", gid)
	}
}
