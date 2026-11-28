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

package codex

import (
	"os"
	"path/filepath"
	"strings"
)

// sandboxWritableRoots maps file roots to their directories. Codex treats
// every writable root as a directory (it mounts a read-only .git inside each),
// so a root that is a file breaks every sandboxed command once it exists.
// A missing root counts as a file when its name has an extension.
func sandboxWritableRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	seen := make(map[string]bool, len(roots))
	for _, root := range roots {
		if isFileRoot(root) {
			root = filepath.Dir(root)
		}
		if !seen[root] {
			seen[root] = true
			out = append(out, root)
		}
	}
	return out
}

func isFileRoot(root string) bool {
	info, err := os.Stat(root)
	if err == nil {
		return !info.IsDir()
	}
	base := filepath.Base(root)
	return !strings.HasPrefix(base, ".") && filepath.Ext(base) != ""
}
