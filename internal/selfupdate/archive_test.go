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
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tarEntry describes one entry for the in-memory archive builder.
type tarEntry struct {
	name     string
	typeflag byte
	mode     int64
	content  []byte
	linkname string
	// size overrides the declared header size (it must still be >= the
	// written content length for a well-formed archive; use with care for
	// hostile declarations).
	size *int64
}

// buildTarGz assembles a tar.gz archive from entries.
func buildTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for _, e := range entries {
		size := int64(len(e.content))
		if e.size != nil {
			size = *e.size
		}
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: e.typeflag,
			Mode:     e.mode,
			Size:     size,
			Linkname: e.linkname,
		}
		if e.typeflag == tar.TypeDir {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header %q: %v", e.name, err)
		}
		if e.typeflag == tar.TypeReg || e.typeflag == tar.TypeRegA {
			if _, err := tw.Write(e.content); err != nil {
				t.Fatalf("write tar content %q: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	if _, err := zw.Write(raw.Bytes()); err != nil {
		t.Fatalf("gzip tar bytes: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	return out.Bytes()
}

func regEntry(name string, mode int64, content []byte) tarEntry {
	return tarEntry{name: name, typeflag: tar.TypeReg, mode: mode, content: content}
}

func defaultArchiveEntries() []tarEntry {
	return []tarEntry{
		{name: "agentico-1.2.3/", typeflag: tar.TypeDir, mode: 0o755},
		regEntry("agentico-1.2.3/README.md", 0o644, []byte("release notes\n")),
		regEntry("agentico-1.2.3/LICENSE.txt", 0o644, bytes.Repeat([]byte("x"), 64)),
		regEntry("agentico", 0o755, []byte("#!/bin/sh\ntrue\n")),
	}
}

// writeArchive writes the archive into dir and returns its path plus the
// destination path for the extracted executable.
func writeArchive(t *testing.T, dir string, data []byte) (archivePath, destPath string) {
	t.Helper()
	archivePath = filepath.Join(dir, "release.tar.gz")
	if err := os.WriteFile(archivePath, data, 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	return archivePath, filepath.Join(dir, "extracted-agentico")
}

func wantExecutableBytes() []byte {
	return []byte("#!/bin/sh\ntrue\n")
}

func TestArchiveExtractsOnlyExecutable(t *testing.T) {
	dir := t.TempDir()
	archivePath, destPath := writeArchive(t, dir, buildTarGz(t, defaultArchiveEntries()))

	digest, err := ExtractExecutable(archivePath, destPath)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	sum := sha256.Sum256(wantExecutableBytes())
	if digest != hex.EncodeToString(sum[:]) {
		t.Fatalf("digest = %s, want %s", digest, hex.EncodeToString(sum[:]))
	}
	info, err := os.Stat(destPath)
	if err != nil {
		t.Fatalf("stat extracted: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("extracted mode = %o, want 755", info.Mode().Perm())
	}
	// Non-executable entries are validated but never materialized.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "release.tar.gz" && e.Name() != "extracted-agentico" {
			t.Fatalf("archive materialized unexpected entry %q", e.Name())
		}
	}
}

func TestArchivePreservesEntryMode(t *testing.T) {
	dir := t.TempDir()
	entries := []tarEntry{regEntry("agentico", 0o711, wantExecutableBytes())}
	archivePath, destPath := writeArchive(t, dir, buildTarGz(t, entries))
	if _, err := ExtractExecutable(archivePath, destPath); err != nil {
		t.Fatalf("extract: %v", err)
	}
	info, err := os.Stat(destPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o711 {
		t.Fatalf("extracted mode = %o, want 711", info.Mode().Perm())
	}
}

func TestArchiveRejections(t *testing.T) {
	cases := []struct {
		name    string
		entries []tarEntry
		want    string
	}{
		{
			name: "traversal root",
			entries: []tarEntry{
				regEntry("../evil", 0o755, wantExecutableBytes()),
				regEntry("agentico", 0o755, wantExecutableBytes()),
			},
			want: "escapes the archive root",
		},
		{
			name: "traversal nested",
			entries: []tarEntry{
				regEntry("a/../../evil", 0o644, []byte("x")),
				regEntry("agentico", 0o755, wantExecutableBytes()),
			},
			want: "is not a clean relative path",
		},
		{
			name: "absolute name",
			entries: []tarEntry{
				regEntry("/etc/passwd", 0o644, []byte("x")),
				regEntry("agentico", 0o755, wantExecutableBytes()),
			},
			want: "absolute name",
		},
		{
			name: "empty name",
			entries: []tarEntry{
				regEntry("", 0o644, []byte("x")),
				regEntry("agentico", 0o755, wantExecutableBytes()),
			},
			want: "empty name",
		},
		{
			name: "backslash name",
			entries: []tarEntry{
				regEntry("..\\evil", 0o644, []byte("x")),
				regEntry("agentico", 0o755, wantExecutableBytes()),
			},
			want: "backslash",
		},
		{
			name: "symlink",
			entries: []tarEntry{
				{name: "agentico-link", typeflag: tar.TypeSymlink, mode: 0o777, linkname: "/etc/passwd"},
				regEntry("agentico", 0o755, wantExecutableBytes()),
			},
			want: "symlink/hard link/special file",
		},
		{
			name: "hard link",
			entries: []tarEntry{
				{name: "agentico-link", typeflag: tar.TypeLink, mode: 0o755, linkname: "agentico"},
				regEntry("agentico", 0o755, wantExecutableBytes()),
			},
			want: "symlink/hard link/special file",
		},
		{
			name: "char device",
			entries: []tarEntry{
				{name: "dev", typeflag: tar.TypeChar, mode: 0o644},
				regEntry("agentico", 0o755, wantExecutableBytes()),
			},
			want: "symlink/hard link/special file",
		},
		{
			name: "fifo",
			entries: []tarEntry{
				{name: "pipe", typeflag: tar.TypeFifo, mode: 0o644},
				regEntry("agentico", 0o755, wantExecutableBytes()),
			},
			want: "symlink/hard link/special file",
		},
		{
			name: "duplicate executable entries",
			entries: []tarEntry{
				regEntry("agentico", 0o755, wantExecutableBytes()),
				regEntry("agentico", 0o755, wantExecutableBytes()),
			},
			want: "duplicate executable entry",
		},
		{
			name: "unexpected executable entry",
			entries: []tarEntry{
				regEntry("evil-helper", 0o755, []byte("#!/bin/sh\ntrue\n")),
				regEntry("agentico", 0o755, wantExecutableBytes()),
			},
			want: `unexpected executable entry "evil-helper"`,
		},
		{
			name:    "missing executable entry",
			entries: []tarEntry{regEntry("README.md", 0o644, []byte("notes\n"))},
			want:    "no executable entry",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			archivePath, destPath := writeArchive(t, dir, buildTarGz(t, tc.entries))
			_, err := ExtractExecutable(archivePath, destPath)
			if err == nil {
				t.Fatalf("expected rejection (%s)", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.want)
			}
			if _, statErr := os.Lstat(destPath); !os.IsNotExist(statErr) {
				t.Fatalf("rejected archive left a destination file behind: %v", statErr)
			}
		})
	}
}

func TestArchiveEntryLimit(t *testing.T) {
	// Exactly at the limit: one executable plus 127 non-executable entries.
	entries := []tarEntry{regEntry("agentico", 0o755, wantExecutableBytes())}
	for i := 0; i < int(archiveMaxEntries)-1; i++ {
		entries = append(entries, regEntry(fmt.Sprintf("extra-%03d.txt", i), 0o644, []byte("x")))
	}
	dir := t.TempDir()
	archivePath, destPath := writeArchive(t, dir, buildTarGz(t, entries))
	if _, err := ExtractExecutable(archivePath, destPath); err != nil {
		t.Fatalf("archive at the entry limit must extract: %v", err)
	}

	// One beyond the limit is rejected.
	entries = append(entries, regEntry("one-too-many.txt", 0o644, []byte("x")))
	dir = t.TempDir()
	archivePath, destPath = writeArchive(t, dir, buildTarGz(t, entries))
	_, err := ExtractExecutable(archivePath, destPath)
	if err == nil || !strings.Contains(err.Error(), "entry limit") {
		t.Fatalf("expected entry-limit rejection, got %v", err)
	}
	if _, statErr := os.Lstat(destPath); !os.IsNotExist(statErr) {
		t.Fatalf("rejected archive left a destination file behind: %v", statErr)
	}
}

func TestArchiveExpandedBudget(t *testing.T) {
	restore := shrinkArchiveBudget(t, 2048, archiveMaxEntries)
	defer restore()

	// Oversized declaration: the header alone exceeds the remaining budget,
	// so the rejection fires before any payload byte is streamed.
	entries := []tarEntry{
		regEntry("blob.bin", 0o644, bytes.Repeat([]byte("x"), 4096)),
	}
	dir := t.TempDir()
	archivePath, destPath := writeArchive(t, dir, buildTarGz(t, entries))
	_, err := ExtractExecutable(archivePath, destPath)
	if err == nil || !strings.Contains(err.Error(), "declares more than") {
		t.Fatalf("expected declared-size rejection, got %v", err)
	}
	if _, statErr := os.Lstat(destPath); !os.IsNotExist(statErr) {
		t.Fatalf("oversized declaration left a destination file behind: %v", statErr)
	}

	// Actual streamed excess: every declaration fits the remaining budget,
	// but the aggregate decompressed consumption (payload plus tar block
	// overhead) crosses it mid-archive.
	entries = []tarEntry{
		regEntry("filler.bin", 0o644, bytes.Repeat([]byte("a"), 1536)),
		regEntry("agentico", 0o755, wantExecutableBytes()),
	}
	dir = t.TempDir()
	archivePath, destPath = writeArchive(t, dir, buildTarGz(t, entries))
	_, err = ExtractExecutable(archivePath, destPath)
	if err == nil || !isExpandedBudgetRejection(err) {
		t.Fatalf("expected expanded-budget rejection, got %v", err)
	}
	if _, statErr := os.Lstat(destPath); !os.IsNotExist(statErr) {
		t.Fatalf("budget violation left a destination file behind: %v", statErr)
	}
}

func TestArchiveDiscardedBytesCountTowardBudget(t *testing.T) {
	restore := shrinkArchiveBudget(t, 2048, archiveMaxEntries)
	defer restore()

	// A discarded non-executable payload consumes the whole budget on its
	// own: without counting discarded bytes, the small executable entry
	// (header, payload, padding, and the trailing zero blocks stay under the
	// same budget) would have extracted successfully.
	entries := []tarEntry{
		regEntry("discarded.bin", 0o644, bytes.Repeat([]byte("z"), 1536)),
		regEntry("agentico", 0o755, wantExecutableBytes()),
	}
	dir := t.TempDir()
	archivePath, destPath := writeArchive(t, dir, buildTarGz(t, entries))
	_, err := ExtractExecutable(archivePath, destPath)
	if err == nil || !isExpandedBudgetRejection(err) {
		t.Fatalf("expected discarded-bytes budget rejection, got %v", err)
	}
}

// isExpandedBudgetRejection accepts either budget-excess message: the
// sentinel can surface directly from a payload read or be caught by the
// declared-size check at the next header boundary, where the tar reader
// swallows it.
func isExpandedBudgetRejection(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "expands beyond") || strings.Contains(msg, "declares more than")
}

func shrinkArchiveBudget(t *testing.T, expanded int64, entries int64) func() {
	t.Helper()
	oldExpanded, oldEntries := archiveMaxExpandedBytes, archiveMaxEntries
	archiveMaxExpandedBytes, archiveMaxEntries = expanded, entries
	return func() {
		archiveMaxExpandedBytes, archiveMaxEntries = oldExpanded, oldEntries
	}
}

func TestArchiveTruncatedArchiveRejected(t *testing.T) {
	data := buildTarGz(t, defaultArchiveEntries())
	// Cut the gzip stream in half: decoding must fail as malformed.
	dir := t.TempDir()
	archivePath, destPath := writeArchive(t, dir, data[:len(data)/2])
	_, err := ExtractExecutable(archivePath, destPath)
	if err == nil || !strings.Contains(err.Error(), "malformed release archive") {
		t.Fatalf("expected malformed rejection, got %v", err)
	}
	if _, statErr := os.Lstat(destPath); !os.IsNotExist(statErr) {
		t.Fatalf("malformed archive left a destination file behind: %v", statErr)
	}
}

func TestArchiveTrailingDataRejected(t *testing.T) {
	data := buildTarGz(t, defaultArchiveEntries())
	tampered := append(append([]byte{}, data...), []byte("trailing garbage")...)
	dir := t.TempDir()
	archivePath, destPath := writeArchive(t, dir, tampered)
	_, err := ExtractExecutable(archivePath, destPath)
	if err == nil || !strings.Contains(err.Error(), "trailing data") {
		t.Fatalf("expected trailing-data rejection, got %v", err)
	}
	if _, statErr := os.Lstat(destPath); !os.IsNotExist(statErr) {
		t.Fatalf("trailing-data archive left a destination file behind: %v", statErr)
	}
}

func TestArchiveRefusesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	archivePath, destPath := writeArchive(t, dir, buildTarGz(t, defaultArchiveEntries()))
	if err := os.WriteFile(destPath, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ExtractExecutable(archivePath, destPath)
	if err == nil {
		t.Fatal("expected refusal when the destination already exists")
	}
	got, readErr := os.ReadFile(destPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "precious" {
		t.Fatalf("existing destination was modified: %q", string(got))
	}
}

func TestArchivePartialExtractionCleanedUp(t *testing.T) {
	restore := shrinkArchiveBudget(t, 4096, archiveMaxEntries)
	defer restore()

	// The executable extracts first, then a later entry violates the budget:
	// the partially written destination must be removed on failure.
	entries := []tarEntry{
		regEntry("agentico", 0o755, wantExecutableBytes()),
		regEntry("late-blob.bin", 0o644, bytes.Repeat([]byte("q"), 8192)),
	}
	dir := t.TempDir()
	archivePath, destPath := writeArchive(t, dir, buildTarGz(t, entries))
	_, err := ExtractExecutable(archivePath, destPath)
	if err == nil || !isExpandedBudgetRejection(err) {
		t.Fatalf("expected budget rejection after partial extraction, got %v", err)
	}
	if _, statErr := os.Lstat(destPath); !os.IsNotExist(statErr) {
		t.Fatalf("partial extraction was not cleaned up: %v", statErr)
	}
}

func TestArchiveExecutableAfterDiscards(t *testing.T) {
	// A realistic goreleaser-shaped layout: docs first, executable last.
	entries := []tarEntry{
		{name: "agentico-1.2.3/", typeflag: tar.TypeDir, mode: 0o755},
		regEntry("agentico-1.2.3/LICENSE.txt", 0o644, []byte("Apache-2.0\n")),
		regEntry("agentico-1.2.3/README.md", 0o644, []byte("agentico CLI\n")),
		regEntry("agentico-1.2.3/agentico", 0o644, []byte("decoy that is never materialized\n")),
		regEntry("agentico", 0o755, wantExecutableBytes()),
	}
	dir := t.TempDir()
	archivePath, destPath := writeArchive(t, dir, buildTarGz(t, entries))
	digest, err := ExtractExecutable(archivePath, destPath)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	sum := sha256.Sum256(wantExecutableBytes())
	if digest != hex.EncodeToString(sum[:]) {
		t.Fatalf("digest = %s, want %s", digest, hex.EncodeToString(sum[:]))
	}
	// The non-root non-executable "agentico-1.2.3/agentico" copy is ignored,
	// not materialized and not treated as a duplicate executable.
	got, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, wantExecutableBytes()) {
		t.Fatalf("extracted bytes are not the root executable entry")
	}
}

var _ io.Reader = (*budgetedReader)(nil)
