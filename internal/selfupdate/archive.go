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
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// Hard safety limits for release archives. Every tar entry (files,
// directories, and metadata) counts toward the entry limit, and every
// decompressed byte — entry payloads, discarded bytes, tar headers, padding,
// and zero blocks alike — counts toward the expanded-byte limit, so a hostile
// archive can never spend resources beyond these bounds before it is
// rejected.
const (
	ArchiveMaxEntries       = 128
	ArchiveMaxExpandedBytes = 256 << 20
	ExecutableEntryName     = "agentico"
)

// Test seams over the archive limits: the exported constants stay fixed
// documentation while tests shrink the working copies to exercise the
// boundary checks cheaply.
var (
	archiveMaxEntries       = int64(ArchiveMaxEntries)
	archiveMaxExpandedBytes = int64(ArchiveMaxExpandedBytes)
)

// errArchiveExpanded is the sentinel surfaced when actual decompressed
// consumption crosses the expanded-byte budget. It is a sentinel (not a
// formatted error) so it survives io.Copy/io.ReadFull wrapping and is mapped
// to the user-facing message once, at the top of the extraction loop.
var errArchiveExpanded = errors.New("release archive expanded budget exceeded")

// budgetedReader wraps the decompressed gzip stream and fails the moment
// cumulative consumption crosses the remaining budget. The zero check must
// precede the underlying read: io.ReadFull swallows an error returned
// alongside a full block, so the overflow is caught on the next read instead.
type budgetedReader struct {
	r         io.Reader
	remaining int64
}

func (b *budgetedReader) Read(p []byte) (int, error) {
	if b.remaining < 0 {
		return 0, errArchiveExpanded
	}
	n, err := b.r.Read(p)
	b.remaining -= int64(n)
	if b.remaining < 0 {
		return n, errArchiveExpanded
	}
	return n, err
}

// ExtractExecutable streams the tar.gz release archive at archivePath and
// writes its single accepted executable entry to destPath, returning the
// sha256 hex digest of the extracted bytes. Every safety rule is enforced
// before, during, and after streaming: any rejection removes a partially
// written destPath and never touches a file this call did not create.
func ExtractExecutable(archivePath, destPath string) (string, error) {
	var created bool
	digest, err := extractExecutable(archivePath, destPath, &created)
	if err != nil {
		if created {
			_ = os.Remove(destPath)
		}
		return "", err
	}
	return digest, nil
}

func extractExecutable(archivePath, destPath string, created *bool) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("open release archive %s: %w", archivePath, err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("malformed release archive: %w", err)
	}
	defer gz.Close()
	br := &budgetedReader{r: gz, remaining: archiveMaxExpandedBytes}
	tr := tar.NewReader(br)

	entries := int64(0)
	seenExecutable := false
	var digest string

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if errors.Is(err, errArchiveExpanded) {
				return "", fmt.Errorf("release archive expands beyond the %d-MiB limit", archiveMaxExpandedBytes>>20)
			}
			return "", fmt.Errorf("malformed release archive: %w", err)
		}
		entries++
		if entries > archiveMaxEntries {
			return "", fmt.Errorf("release archive exceeds the %d-entry limit", archiveMaxEntries)
		}
		name := hdr.Name
		if hdr.Typeflag == tar.TypeDir {
			// Tar directory entries conventionally carry a trailing slash;
			// the clean-path rule applies to the logical name.
			name = strings.TrimSuffix(name, "/")
		}
		if err := validateArchiveEntryName(name); err != nil {
			return "", err
		}
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			return "", fmt.Errorf("release archive entry %q is a symlink/hard link/special file", name)
		}
		if hdr.Size > br.remaining {
			return "", fmt.Errorf("release archive declares more than the %d-MiB expanded limit", archiveMaxExpandedBytes>>20)
		}
		mode := hdr.FileInfo().Mode()
		if mode&0o111 == 0 {
			// Non-executable files are validated but never materialized:
			// their bytes are discarded while still consuming budget.
			if _, err := io.Copy(io.Discard, tr); err != nil {
				if errors.Is(err, errArchiveExpanded) {
					return "", fmt.Errorf("release archive expands beyond the %d-MiB limit", archiveMaxExpandedBytes>>20)
				}
				return "", fmt.Errorf("malformed release archive: %w", err)
			}
			continue
		}
		if name != ExecutableEntryName {
			return "", fmt.Errorf("unexpected executable entry %q", name)
		}
		if seenExecutable {
			return "", fmt.Errorf("release archive carries a duplicate executable entry %q", name)
		}
		seenExecutable = true
		out, err := os.OpenFile(destPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
		if err != nil {
			return "", fmt.Errorf("create extracted executable %s: %w", destPath, err)
		}
		*created = true
		h := sha256.New()
		// A hard cap on bytes pulled for this entry on top of the budgeted
		// stream: the declared-size check already passed, so this is defense
		// in depth keeping the copy inside the remaining budget.
		n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(tr, br.remaining))
		if err != nil {
			_ = out.Close()
			if errors.Is(err, errArchiveExpanded) {
				return "", fmt.Errorf("release archive expands beyond the %d-MiB limit", archiveMaxExpandedBytes>>20)
			}
			return "", fmt.Errorf("malformed release archive: %w", err)
		}
		if n != hdr.Size {
			_ = out.Close()
			return "", fmt.Errorf("malformed release archive: executable entry %q was truncated", name)
		}
		// Explicit chmod: the O_CREAT mode is umask-masked, and the extracted
		// executable must carry exactly the entry's permission bits.
		if err := out.Chmod(mode.Perm()); err != nil {
			_ = out.Close()
			return "", fmt.Errorf("chmod extracted executable %s: %w", destPath, err)
		}
		if err := out.Close(); err != nil {
			return "", fmt.Errorf("close extracted executable %s: %w", destPath, err)
		}
		if err := SyncFile(destPath); err != nil {
			return "", err
		}
		digest = hex.EncodeToString(h.Sum(nil))
	}

	if !seenExecutable {
		return "", fmt.Errorf("release archive carries no executable entry")
	}
	if err := rejectTrailingArchiveData(gz, f); err != nil {
		return "", err
	}
	return digest, nil
}

// validateArchiveEntryName enforces that name is a clean relative path that
// can never escape the archive root: no absolute names, no ".." elements, no
// backslashes, and no non-clean forms.
func validateArchiveEntryName(name string) error {
	if name == "" {
		return fmt.Errorf("release archive entry has an empty name")
	}
	if strings.HasPrefix(name, "/") {
		return fmt.Errorf("release archive entry %q has an absolute name", name)
	}
	if strings.Contains(name, `\`) {
		return fmt.Errorf("release archive entry %q contains a backslash", name)
	}
	if path.Clean(name) != name {
		return fmt.Errorf("release archive entry %q is not a clean relative path", name)
	}
	for _, elem := range strings.Split(name, "/") {
		if elem == ".." {
			return fmt.Errorf("release archive entry %q escapes the archive root", name)
		}
	}
	return nil
}

// rejectTrailingArchiveData rejects any leftover after the tar stream ends.
// The tar reader stops at the two zero blocks; a clean release tar.gz has the
// gzip member and the underlying file end exactly there. After that point
// anything but io.EOF from the gzip stream — extra member bytes or data the
// reader rejects — is trailing data, and once gzip reports EOF the whole file
// must have been consumed. Reading one byte at a time keeps a hostile
// multi-gigabyte tail from being materialized just to detect it.
func rejectTrailingArchiveData(gz *gzip.Reader, f *os.File) error {
	if _, err := gz.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		return fmt.Errorf("release archive carries trailing data")
	}
	if _, err := f.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		return fmt.Errorf("release archive carries trailing data")
	}
	return nil
}
