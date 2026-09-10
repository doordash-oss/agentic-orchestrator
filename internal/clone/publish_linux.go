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

//go:build linux

package clone

import "golang.org/x/sys/unix"

// renameNoReplace uses renameat2(RENAME_NOREPLACE) relative to the pinned
// root directory handle: the rename is atomic and never replaces an
// existing destination, including an empty directory.
func renameNoReplace(dirfd int, oldName, newName string) error {
	return unix.Renameat2(dirfd, oldName, dirfd, newName, unix.RENAME_NOREPLACE)
}
