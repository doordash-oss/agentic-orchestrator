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

// Publication errors. ErrDestinationExists means another actor already won
// the destination name (their data stays intact); ErrNoReplaceUnsupported
// means the platform or filesystem cannot atomically publish without
// replacement, in which case publication fails safely instead of falling
// back to a replacing rename.
var (
	ErrDestinationExists      = errors.New("clone: destination already exists")
	ErrNoReplaceUnsupported   = errors.New("clone: atomic no-replace rename unsupported")
	ErrRootIdentityMismatch   = errors.New("clone: root identity mismatch")
	ErrStagingOwnershipFailed = errors.New("clone: staging ownership could not be verified")
)

// DirIdentity is the filesystem identity (device + inode) of a directory.
// Identity-bound operations compare this before and after async gaps so a
// path or symlink swap cannot redirect writes, publication or cleanup into
// another actor's tree.
type DirIdentity struct {
	Device uint64
	Inode  uint64
}

// OpenRootDir opens the directory at path with O_DIRECTORY|O_NOFOLLOW so
// the returned handle pins the directory inode: later path or symlink swaps
// cannot redirect operations performed through this handle.
func OpenRootDir(path string) (*os.File, DirIdentity, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, DirIdentity{}, fmt.Errorf("open root directory: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = f.Close()
		return nil, DirIdentity{}, fmt.Errorf("stat root directory: %w", err)
	}
	return f, DirIdentity{Device: uint64(stat.Dev), Inode: uint64(stat.Ino)}, nil
}

// SameDirIdentity reports whether the open directory handle still refers to
// the expected filesystem identity.
func SameDirIdentity(f *os.File, want DirIdentity) bool {
	if f == nil {
		return false
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &stat); err != nil {
		return false
	}
	return uint64(stat.Dev) == want.Device && uint64(stat.Ino) == want.Inode
}

// RenameNoReplaceDir renames oldName to newName within the directory held
// by dir, using the platform's native atomic no-replace behavior. oldName
// and newName are single child names inside dir; both must live in the same
// directory (staging and destination share the clone root by construction).
// There is deliberately no replacing fallback.
func RenameNoReplaceDir(dir *os.File, oldName, newName string) error {
	if dir == nil {
		return ErrNoReplaceUnsupported
	}
	dirfd := int(dir.Fd())
	err := renameNoReplace(dirfd, oldName, newName)
	if err != nil {
		return classifyRenameError(err)
	}
	return nil
}

// RenameNoReplace is the path-based form used by tests: it opens the shared
// parent directory with O_NOFOLLOW and performs the identity-bound rename.
func RenameNoReplace(oldPath, newPath string) error {
	parent := parentDir(oldPath)
	if parentDir(newPath) != parent {
		return errors.New("clone: no-replace rename requires a shared parent directory")
	}
	dir, _, err := OpenRootDir(parent)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return RenameNoReplaceDir(dir, filepathBase(oldPath), filepathBase(newPath))
}

func classifyRenameError(err error) error {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return err
	}
	switch errno {
	case syscall.EEXIST, syscall.ENOTEMPTY:
		return ErrDestinationExists
	case syscall.ENOSYS, syscall.EINVAL, syscall.ENOTSUP, syscall.EOPNOTSUPP:
		return fmt.Errorf("%w: %v", ErrNoReplaceUnsupported, errno)
	}
	return err
}

func parentDir(path string) string {
	idx := lastIndexByte(path, '/')
	if idx < 0 {
		return "."
	}
	if idx == 0 {
		return "/"
	}
	return path[:idx]
}

func filepathBase(path string) string {
	idx := lastIndexByte(path, '/')
	if idx < 0 {
		return path
	}
	return path[idx+1:]
}

func lastIndexByte(s string, b byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == b {
			return i
		}
	}
	return -1
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
