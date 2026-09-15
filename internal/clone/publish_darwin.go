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

//go:build darwin

package clone

import (
	"syscall"
)

// sysRenameatxNp is the macOS renameatx_np syscall trap (see
// sys/syscall.h in the macOS SDK). It is available from macOS 10.12; on
// older kernels the trap returns ENOSYS, which the caller classifies as
// unsupported so publication fails safely instead of replacing.
const sysRenameatxNp = 488

// renameExcl is RENAME_EXCL from sys/stdio.h: the rename fails with EEXIST
// if the destination exists rather than replacing it.
const renameExcl = 0x4

// renameNoReplace calls renameatx_np(AT_FDCWD-free, dirfd, old, dirfd, new,
// RENAME_EXCL) through the raw trap so the build stays cgo-free (releases
// set CGO_ENABLED=0). The rename is atomic, never replaces an existing
// destination, and is bound to the pinned root directory handle.
func renameNoReplace(dirfd int, oldName, newName string) error {
	oldPtr, err := bytePtr(oldName)
	if err != nil {
		return err
	}
	newPtr, err := bytePtr(newName)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall6(
		sysRenameatxNp,
		uintptr(dirfd), uintptr(oldPtr),
		uintptr(dirfd), uintptr(newPtr),
		uintptr(renameExcl), 0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}
