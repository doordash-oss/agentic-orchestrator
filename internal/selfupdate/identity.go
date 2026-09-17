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

package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// FileIdentity is the inode-level identity of a file. Dev/Ino/Size survive
// renames within one filesystem and change when the path is replaced with a
// different inode, which is the replacement signal this package relies on.
type FileIdentity struct {
	Dev  uint64
	Ino  uint64
	Mode uint32 // permission bits only (info.Mode().Perm())
	UID  int
	GID  int
	Size int64
}

// SameFile compares device, inode and size; permission changes do not replace a file.
func (id FileIdentity) SameFile(other FileIdentity) bool {
	return id.Dev == other.Dev && id.Ino == other.Ino && id.Size == other.Size
}

// identityFromInfo extracts inode identity plus permission and ownership bits
// from a FileInfo. Non-stat backends yield zero UID/GID/Dev/Ino.
func identityFromInfo(info os.FileInfo) FileIdentity {
	id := FileIdentity{
		Mode: uint32(info.Mode().Perm()),
		Size: info.Size(),
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		id.Dev = uint64(st.Dev)
		id.Ino = uint64(st.Ino)
		id.UID = int(st.Uid)
		id.GID = int(st.Gid)
	}
	return id
}

// StatFile returns the inode identity of path. It refuses anything that is
// not a regular file so directories, symlinks, and devices can never be
// mistaken for an installed executable.
func StatFile(path string) (FileIdentity, error) {
	info, err := os.Stat(path)
	if err != nil {
		return FileIdentity{}, fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return FileIdentity{}, fmt.Errorf("stat %s: not a regular file", path)
	}
	return identityFromInfo(info), nil
}

// DigestFile returns the sha256 hex digest of the file's current bytes.
func DigestFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s for digest: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("digest %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Executable is the boot-resolved identity of an installed binary. Path is
// canonical so it remains the stable lease key across inode replacement.
type Executable struct {
	Path   string
	ID     FileIdentity
	Digest string
}

// CaptureExecutable captures the identity of the currently running binary.
func CaptureExecutable() (Executable, error) {
	path, err := os.Executable()
	if err != nil {
		return Executable{}, fmt.Errorf("resolve running executable: %w", err)
	}
	return CaptureExecutableAt(path)
}

// CaptureExecutableAt canonicalizes path (symlinks resolved) and captures its
// identity and digest. Later verification compares against this snapshot
// instead of rediscovering a potentially replaced executable.
func CaptureExecutableAt(path string) (Executable, error) {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return Executable{}, fmt.Errorf("canonicalize %s: %w", path, err)
	}
	id, err := StatFile(canonical)
	if err != nil {
		return Executable{}, err
	}
	digest, err := DigestFile(canonical)
	if err != nil {
		return Executable{}, err
	}
	return Executable{Path: canonical, ID: id, Digest: digest}, nil
}

// PathStillMatches reports whether the file at e.Path is still the exact
// inode captured in e.ID. Mode is deliberately excluded: chmod alone does not
// invalidate an executable identity.
func (e Executable) PathStillMatches() (bool, error) {
	id, err := StatFile(e.Path)
	if err != nil {
		return false, err
	}
	return id.SameFile(e.ID), nil
}
