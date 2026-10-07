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
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const trustedDiscoveryToken = "trusted-discovery-secret-token"

func publishTrustedFixture(t *testing.T, mutate func(*DiscoveryRecord)) string {
	t.Helper()
	dir := t.TempDir()
	rec := DiscoveryRecord{BaseURL: testDiscoveryBaseURL, AuthToken: trustedDiscoveryToken}
	if mutate != nil {
		mutate(&rec)
	}
	if err := PublishDiscovery(dir, rec); err != nil {
		t.Fatalf("PublishDiscovery() error = %v", err)
	}
	return dir
}

func TestReadTrustedDiscoveryAcceptsOwnerOnlyRecord(t *testing.T) {
	t.Parallel()
	dir := publishTrustedFixture(t, nil)
	rec, err := ReadTrustedDiscovery(dir)
	if err != nil {
		t.Fatalf("ReadTrustedDiscovery() error = %v", err)
	}
	if rec.BaseURL != testDiscoveryBaseURL || rec.AuthToken != trustedDiscoveryToken {
		t.Fatalf("record = %+v; want the published base_url and token", rec)
	}
}

func TestReadTrustedDiscoveryReportsMissingFile(t *testing.T) {
	t.Parallel()
	_, err := ReadTrustedDiscovery(t.TempDir())
	if !errors.Is(err, ErrDiscoveryMissing) {
		t.Fatalf("error = %v; want ErrDiscoveryMissing", err)
	}
	if errors.Is(err, ErrDiscoveryUntrusted) {
		t.Fatalf("error = %v; a missing file must not also read as untrusted", err)
	}
}

func TestReadTrustedDiscoveryRejectsUnsafeFiles(t *testing.T) {
	t.Parallel()
	cases := map[string]func(t *testing.T) string{
		"group readable": func(t *testing.T) string {
			dir := publishTrustedFixture(t, nil)
			chmodDiscovery(t, dir, 0o640)
			return dir
		},
		"world readable": func(t *testing.T) string {
			dir := publishTrustedFixture(t, nil)
			chmodDiscovery(t, dir, 0o604)
			return dir
		},
		"symlink": func(t *testing.T) string {
			target := publishTrustedFixture(t, nil)
			dir := t.TempDir()
			if err := os.Symlink(DiscoveryPath(target), DiscoveryPath(dir)); err != nil {
				t.Fatalf("Symlink() error = %v", err)
			}
			return dir
		},
		"directory": func(t *testing.T) string {
			dir := t.TempDir()
			if err := os.Mkdir(DiscoveryPath(dir), 0o700); err != nil {
				t.Fatalf("Mkdir() error = %v", err)
			}
			return dir
		},
		"malformed json": func(t *testing.T) string {
			dir := t.TempDir()
			if err := os.WriteFile(DiscoveryPath(dir), []byte(`{"auth_token":"`+trustedDiscoveryToken), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			return dir
		},
		"empty token": func(t *testing.T) string {
			return publishTrustedFixture(t, func(rec *DiscoveryRecord) { rec.AuthToken = "  " })
		},
		"unusable base url": func(t *testing.T) string {
			return publishTrustedFixture(t, func(rec *DiscoveryRecord) { rec.BaseURL = "ftp://example" })
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := ReadTrustedDiscovery(setup(t))
			if !errors.Is(err, ErrDiscoveryUntrusted) {
				t.Fatalf("error = %v; want ErrDiscoveryUntrusted", err)
			}
			if strings.Contains(err.Error(), trustedDiscoveryToken) {
				t.Fatalf("error %q leaks the token", err)
			}
		})
	}
}

// TestReadTrustedDiscoveryRejectsForeignOwner swaps the owner seam because
// a test cannot chown a file to another user. Not parallel: it mutates a
// package-level seam.
func TestReadTrustedDiscoveryRejectsForeignOwner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ownership is not checked on windows")
	}
	dir := publishTrustedFixture(t, nil)
	prev := discoveryOwnerUID
	discoveryOwnerUID = func() int { return os.Geteuid() + 1 }
	t.Cleanup(func() { discoveryOwnerUID = prev })

	_, err := ReadTrustedDiscovery(dir)
	if !errors.Is(err, ErrDiscoveryUntrusted) {
		t.Fatalf("error = %v; want ErrDiscoveryUntrusted for a foreign-owned file", err)
	}
	if !strings.Contains(err.Error(), "owned by uid") {
		t.Fatalf("error = %v; want the ownership reason", err)
	}
}

func chmodDiscovery(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(filepath.Clean(DiscoveryPath(dir)), mode); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
}
