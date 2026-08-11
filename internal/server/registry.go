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

package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// registryDirName is the central registry directory under the resolved
// runtime parent. Every server publishes a full copy of its discovery record
// here so desktop clients can enumerate all live servers, independent of any
// individual --state-dir.
const registryDirName = "servers"

// RegistryDir returns the central registry directory under the resolved
// runtime parent.
func RegistryDir(runtimeParent string) string {
	return filepath.Join(runtimeParent, registryDirName)
}

// RegistryEntryName returns the stable per-runtime key for the registry
// entry owned by the server running from runtimeDir. Runtime dirs are
// canonicalized (symlinks resolved, matching canonicalizeStateDir on the CLI
// side and the desktop gateway) so the same runtime always reuses one entry
// and restarts overwrite in place.
func RegistryEntryName(runtimeDir string) string {
	canonical := runtimeDir
	if resolved, err := filepath.EvalSymlinks(runtimeDir); err == nil {
		canonical = resolved
	}
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])[:32] + ".json"
}

// RegistryEntryPath is the full path of the registry entry for runtimeDir.
func RegistryEntryPath(registryDir, runtimeDir string) string {
	return filepath.Join(registryDir, RegistryEntryName(runtimeDir))
}

// PublishRegistryEntry writes rec (the verbatim per-runtime discovery
// record, token and name included) into the central registry directory. The
// directory is created owner-only (0700) and self-healed on every publish;
// the entry is written atomically (PID-suffixed temp file, O_EXCL/0600,
// rename, post-rename chmod repair), mirroring PublishDiscovery.
func PublishRegistryEntry(registryDir string, rec DiscoveryRecord) error {
	if registryDir == "" {
		return errors.New("registry dir is empty")
	}
	if rec.Runtime.RuntimeDir == "" {
		return errors.New("registry record runtime dir is empty")
	}
	if err := os.MkdirAll(registryDir, 0o700); err != nil {
		return fmt.Errorf("create registry dir: %w", err)
	}
	if err := os.Chmod(registryDir, 0o700); err != nil {
		return fmt.Errorf("repair registry dir permissions: %w", err)
	}
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rec); err != nil {
		return fmt.Errorf("marshal registry entry: %w", err)
	}

	tmp, err := os.OpenFile(
		filepath.Join(registryDir, fmt.Sprintf(".agentico-registry-%d-%s.tmp", os.Getpid(), RegistryEntryName(rec.Runtime.RuntimeDir))),
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return fmt.Errorf("create registry temp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(body.Bytes()); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write registry temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close registry temp: %w", err)
	}
	path := RegistryEntryPath(registryDir, rec.Runtime.RuntimeDir)
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("commit registry entry: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod registry entry: %w", err)
	}
	return nil
}

// RemoveRegistryEntry deletes the registry entry owned by runtimeDir. A
// missing entry is not an error: graceful shutdown is idempotent.
func RemoveRegistryEntry(registryDir, runtimeDir string) error {
	if registryDir == "" {
		return errors.New("registry dir is empty")
	}
	if err := os.Remove(RegistryEntryPath(registryDir, runtimeDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove registry entry: %w", err)
	}
	return nil
}
