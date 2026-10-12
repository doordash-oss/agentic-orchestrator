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
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// publication errors. errDestinationExists means another actor already won
// the destination name (their data stays intact); errNoReplaceUnsupported
// means the platform or filesystem cannot atomically publish without
// replacement, in which case publication fails safely instead of falling
// back to a replacing rename.
var (
	errDestinationExists      = errors.New("clone: destination already exists")
	errNoReplaceUnsupported   = errors.New("clone: atomic no-replace rename unsupported")
	errRootIdentityMismatch   = errors.New("clone: root identity mismatch")
	errStagingOwnershipFailed = errors.New("clone: staging ownership could not be verified")
)

// dirIdentity is the filesystem identity (device + inode) of a directory.
// Identity-bound operations compare this before and after async gaps so a
// path or symlink swap cannot redirect writes, publication or cleanup into
// another actor's tree.
type dirIdentity struct {
	Device uint64
	Inode  uint64
}

// openRootDir opens the directory at path with O_DIRECTORY|O_NOFOLLOW so
// the returned handle pins the directory inode: later path or symlink swaps
// cannot redirect operations performed through this handle.
func openRootDir(path string) (*os.File, dirIdentity, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, dirIdentity{}, fmt.Errorf("open root directory: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = f.Close()
		return nil, dirIdentity{}, fmt.Errorf("stat root directory: %w", err)
	}
	return f, dirIdentity{Device: uint64(stat.Dev), Inode: uint64(stat.Ino)}, nil
}

// sameDirIdentity reports whether the open directory handle still refers to
// the expected filesystem identity.
func sameDirIdentity(f *os.File, want dirIdentity) bool {
	if f == nil {
		return false
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &stat); err != nil {
		return false
	}
	return uint64(stat.Dev) == want.Device && uint64(stat.Ino) == want.Inode
}

// renameNoReplaceDir renames oldName to newName within the directory held
// by dir, using the platform's native atomic no-replace behavior. oldName
// and newName are single child names inside dir; both must live in the same
// directory (staging and destination share the clone root by construction).
// There is deliberately no replacing fallback.
func renameNoReplaceDir(dir *os.File, oldName, newName string) error {
	if dir == nil {
		return errNoReplaceUnsupported
	}
	dirfd := int(dir.Fd())
	err := renameNoReplace(dirfd, oldName, newName)
	if err != nil {
		return classifyRenameError(err)
	}
	return nil
}

func classifyRenameError(err error) error {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return err
	}
	// ENOTSUP and EOPNOTSUPP alias on Linux but differ on Darwin. Boolean
	// comparisons preserve both platforms' errors without duplicate cases.
	switch {
	case errno == syscall.EEXIST || errno == syscall.ENOTEMPTY:
		return errDestinationExists
	case errno == syscall.ENOSYS || errno == syscall.EINVAL || errno == syscall.ENOTSUP || errno == syscall.EOPNOTSUPP:
		return fmt.Errorf("%w: %v", errNoReplaceUnsupported, errno)
	}
	return err
}

func bytePtr(s string) (unsafe.Pointer, error) {
	p, err := syscall.BytePtrFromString(s)
	if err != nil {
		return nil, err
	}
	return unsafe.Pointer(p), nil
}

// unixOpenat creates (exclusively) and opens a child file of dirfd.
func unixOpenat(dirfd int, name string) (int, error) {
	return unix.Openat(dirfd, name, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY, 0o600)
}

// unixUnlinkat removes a child of dirfd.
func unixUnlinkat(dirfd int, name string) error {
	return unix.Unlinkat(dirfd, name, 0)
}
