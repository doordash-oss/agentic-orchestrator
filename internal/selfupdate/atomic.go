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
	"fmt"
	"os"
	"path/filepath"
)

// writeFileAtomic writes data to path with owner-only permissions through a
// randomly named O_EXCL temp file in the same directory followed by a rename,
// so readers never observe a partial write and concurrent writers never
// corrupt each other's bytes. When durable is true the temp file is fsynced
// before the rename and the parent directory is fsynced after it, so the
// write survives a crash; when durable is false neither sync runs and the
// rename alone publishes the bytes. The final file is chmod-repaired to 0600
// to self-heal mode drift; a failure after the rename leaves the new bytes
// in place and surfaces the error, never a half-written file.
func writeFileAtomic(path string, data []byte, durable bool) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write temp file: %w", err)
	}
	if durable {
		if err := f.Sync(); err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
			return fmt.Errorf("sync temp file: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("commit %s: %w", path, err)
	}
	if durable {
		if err := SyncDir(filepath.Dir(path)); err != nil {
			return fmt.Errorf("sync dir of %s: %w", path, err)
		}
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("repair permissions of %s: %w", path, err)
	}
	return nil
}
