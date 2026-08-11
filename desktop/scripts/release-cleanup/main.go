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

// Command release-cleanup removes a validated detached release workspace by
// traversing directory file descriptors. It never recursively resolves the
// workspace pathname after opening the expected inode.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "release cleanup failed: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) != 3 {
		return errors.New("usage: release-cleanup PATH DEVICE INODE")
	}
	path, err := filepath.Abs(args[0])
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}
	device, err := strconv.ParseUint(args[1], 10, 64)
	if err != nil {
		return fmt.Errorf("parse workspace device: %w", err)
	}
	inode, err := strconv.ParseUint(args[2], 10, 64)
	if err != nil {
		return fmt.Errorf("parse workspace inode: %w", err)
	}
	parentFD, err := unix.Open(filepath.Dir(path), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open workspace parent: %w", err)
	}
	defer unix.Close(parentFD)

	name := filepath.Base(path)
	rootFD, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open workspace root: %w", err)
	}
	defer unix.Close(rootFD)
	root, err := statFD(rootFD)
	if err != nil {
		return err
	}
	if uint64(root.Dev) != device || uint64(root.Ino) != inode {
		return errors.New("workspace root changed before inode-bound cleanup")
	}
	if _, err := fmt.Fprintln(stdout, "ready"); err != nil {
		return fmt.Errorf("signal cleanup readiness: %w", err)
	}
	// Production gives the helper an already-closed stdin. Tests may keep it
	// open to swap a path after the validated inode is held.
	var handshake [1]byte
	if _, err := stdin.Read(handshake[:]); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("wait for cleanup continuation: %w", err)
	}

	if err := removeContents(rootFD); err != nil {
		return err
	}
	changed, err := unlinkOpenedDirectory(parentFD, name, root)
	if err != nil {
		return fmt.Errorf("remove validated workspace root: %w", err)
	}
	if changed {
		return errors.New("workspace root changed during inode-bound cleanup")
	}
	return nil
}

func removeContents(directoryFD int) error {
	if err := unix.Fchmod(directoryFD, 0o700); err != nil {
		return fmt.Errorf("make validated directory writable: %w", err)
	}
	entries, err := readDirectory(directoryFD)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		before, err := statAt(directoryFD, name)
		if err != nil {
			return fmt.Errorf("stat validated child %q: %w", name, err)
		}
		if before.Mode&unix.S_IFMT != unix.S_IFDIR {
			if err := unix.Unlinkat(directoryFD, name, 0); err != nil {
				return fmt.Errorf("remove validated non-directory %q: %w", name, err)
			}
			continue
		}

		if err := removeDirectory(directoryFD, name, before); err != nil {
			return err
		}
	}
	return nil
}

func removeDirectory(parentFD int, name string, before *unix.Stat_t) error {
	childFD, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open validated directory %q: %w", name, err)
	}
	defer unix.Close(childFD)
	opened, err := statFD(childFD)
	if err != nil {
		return fmt.Errorf("stat validated directory %q: %w", name, err)
	}
	if opened.Dev != before.Dev || opened.Ino != before.Ino {
		return fmt.Errorf("validated directory %q changed while opening", name)
	}
	if err := removeContents(childFD); err != nil {
		return fmt.Errorf("clean validated directory %q: %w", name, err)
	}
	changed, err := unlinkOpenedDirectory(parentFD, name, opened)
	if err != nil {
		return fmt.Errorf("remove validated directory %q: %w", name, err)
	}
	if changed {
		return fmt.Errorf("validated directory %q changed during cleanup", name)
	}
	return nil
}

func unlinkOpenedDirectory(parentFD int, requestedName string, opened *unix.Stat_t) (bool, error) {
	current, err := statAt(parentFD, requestedName)
	if err == nil && sameDirectory(current, opened) {
		return false, unix.Unlinkat(parentFD, requestedName, unix.AT_REMOVEDIR)
	}
	if err != nil && !errors.Is(err, unix.ENOENT) {
		return false, err
	}
	entries, err := readDirectory(parentFD)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		candidate, err := statAt(parentFD, entry.Name())
		if err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			return false, err
		}
		if !sameDirectory(candidate, opened) {
			continue
		}
		if err := unix.Unlinkat(parentFD, entry.Name(), unix.AT_REMOVEDIR); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, errors.New("opened directory no longer has a validated name in its parent")
}

func sameDirectory(actual, expected *unix.Stat_t) bool {
	return actual.Mode&unix.S_IFMT == unix.S_IFDIR && actual.Dev == expected.Dev && actual.Ino == expected.Ino
}

func readDirectory(directoryFD int) ([]os.DirEntry, error) {
	duplicate, err := unix.Dup(directoryFD)
	if err != nil {
		return nil, fmt.Errorf("duplicate directory handle: %w", err)
	}
	file := os.NewFile(uintptr(duplicate), "validated-release-directory")
	if file == nil {
		unix.Close(duplicate)
		return nil, errors.New("wrap duplicated directory handle")
	}
	if _, err := unix.Seek(duplicate, 0, 0); err != nil {
		file.Close()
		return nil, fmt.Errorf("rewind duplicated directory handle: %w", err)
	}
	entries, readErr := file.ReadDir(-1)
	closeErr := file.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read validated directory: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close duplicated directory handle: %w", closeErr)
	}
	return entries, nil
}

func statFD(fd int) (*unix.Stat_t, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, fmt.Errorf("stat open directory: %w", err)
	}
	return &stat, nil
}

func statAt(directoryFD int, name string) (*unix.Stat_t, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(directoryFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	return &stat, nil
}
