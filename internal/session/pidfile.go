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

// FindPIDFiles scans the bounded set of directories where sessions persist PID
// files: the state directory itself, each immediate feature directory, and the
// feature's immediate child directories used by the legacy phase layout.
//
// Feature trees also contain arbitrary run artifacts, work products, and copied
// fixtures. Recursing into those trees is both unbounded and incorrect: a copied
// session PID fixture is not a live session owned by this runtime.
func FindPIDFiles(featuresDir string) ([]PIDFile, error) {
	var results []PIDFile
	entries, err := os.ReadDir(featuresDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading session state directory: %w", err)
	}

	results = appendPIDFilesInDir(results, featuresDir, entries)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		featureDir := filepath.Join(featuresDir, entry.Name())
		featureEntries, err := os.ReadDir(featureDir)
		if err != nil {
			// Match the recovery scanner's best-effort behavior: one unreadable
			// feature must not hide recoverable sessions from every other feature.
			continue
		}
		results = appendPIDFilesInDir(results, featureDir, featureEntries)
		for _, featureEntry := range featureEntries {
			if !featureEntry.IsDir() || !isLegacyPhaseDir(featureEntry.Name()) {
				continue
			}
			legacySessionDir := filepath.Join(featureDir, featureEntry.Name())
			legacyEntries, err := os.ReadDir(legacySessionDir)
			if err != nil {
				continue
			}
			results = appendPIDFilesInDir(results, legacySessionDir, legacyEntries)
		}
	}

	return results, nil
}

func appendPIDFilesInDir(results []PIDFile, dir string, entries []os.DirEntry) []PIDFile {
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "session") || !strings.HasSuffix(name, ".pid") {
			continue
		}
		pf, err := ReadPIDFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		pf.Dir = dir
		results = append(results, *pf)
	}
	return results
}

func isLegacyPhaseDir(name string) bool {
	switch name {
	case "knowledgebase", "inquire", "research", "design", "plan", "implement", "review", "publish":
		return true
	default:
		return false
	}
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
