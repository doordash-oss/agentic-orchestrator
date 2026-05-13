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

package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"gopkg.in/yaml.v3"
)

// PIDFile aliases the canonical port type; the session package keeps the
// alias for source compatibility with existing callers.
type PIDFile = ports.PIDFile

var _ = time.Second // keep time import used elsewhere

// PIDFileName returns the PID file name for a given repo name.
// Always returns "session-<repoName>.pid".
func PIDFileName(repoName string) string {
	return "session-" + repoName + ".pid"
}

// WritePIDFile creates a PID file for a session.
func WritePIDFile(dir string, pf PIDFile) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating PID directory: %w", err)
	}
	data, err := yaml.Marshal(pf)
	if err != nil {
		return fmt.Errorf("marshaling PID file: %w", err)
	}
	path := filepath.Join(dir, PIDFileName(pf.RepoName))
	return os.WriteFile(path, data, 0o644)
}

// ReadPIDFile reads a PID file.
func ReadPIDFile(path string) (*PIDFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading PID file: %w", err)
	}
	var pf PIDFile
	if err := yaml.Unmarshal(data, &pf); err != nil {
		return nil, fmt.Errorf("parsing PID file: %w", err)
	}
	return &pf, nil
}

// RemovePIDFile removes a PID file.
func RemovePIDFile(dir string, repoName string) error {
	return os.Remove(filepath.Join(dir, PIDFileName(repoName)))
}

// FindPIDFiles scans all feature directories for session*.pid files.
func FindPIDFiles(featuresDir string) ([]PIDFile, error) {
	var results []PIDFile

	err := filepath.Walk(featuresDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		name := info.Name()
		if strings.HasPrefix(name, "session") && strings.HasSuffix(name, ".pid") {
			pf, err := ReadPIDFile(path)
			if err != nil {
				return nil
			}
			pf.Dir = filepath.Dir(path)
			results = append(results, *pf)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scanning for PID files: %w", err)
	}

	return results, nil
}

// isProcessRunning checks if a process with the given PID is still running.
func isProcessRunning(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, FindProcess always succeeds. Use signal 0 to check.
	err = process.Signal(syscall.Signal(0))
	return err == nil
}
