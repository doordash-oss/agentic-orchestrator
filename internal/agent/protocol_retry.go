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

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	ProtocolRetrySidecarFile                = ".protocol-retry.yaml"
	DefaultMaxConsecutiveProtocolViolations = 3
)

// ProtocolRetryAction is the bounded retry decision returned after contract validation.
type ProtocolRetryAction int

const (
	ProtocolRetryActionSucceed ProtocolRetryAction = iota
	ProtocolRetryActionRetry
	ProtocolRetryActionTerminal
)

// ProtocolRetrySidecar is the on-disk retry state colocated with phase_complete.
type ProtocolRetrySidecar struct {
	Role          Role      `yaml:"role"`
	ActiveRun     int       `yaml:"active_run"`
	Consecutive   int       `yaml:"consecutive"`
	LastViolation string    `yaml:"last_violation"`
	UpdatedAt     time.Time `yaml:"updated_at"`
}

// ProtocolRetryDecision tells the caller whether to succeed, retry, or fail terminally.
type ProtocolRetryDecision struct {
	Action         ProtocolRetryAction
	Consecutive    int
	FormattedError string
	NewSidecar     *ProtocolRetrySidecar
}

// ReadProtocolRetrySidecar reads a retry sidecar from dir. Missing files are not errors.
func ReadProtocolRetrySidecar(dir string) (*ProtocolRetrySidecar, error) {
	return ReadProtocolRetrySidecarAt(dir, ProtocolRetrySidecarFile)
}

// ReadProtocolRetrySidecarAt reads a retry sidecar with the supplied filename from dir.
func ReadProtocolRetrySidecarAt(dir, filename string) (*ProtocolRetrySidecar, error) {
	path := filepath.Join(dir, protocolRetrySidecarFilename(filename))
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading protocol retry sidecar: %w", err)
	}

	var sidecar ProtocolRetrySidecar
	if err := yaml.Unmarshal(data, &sidecar); err != nil {
		return nil, fmt.Errorf("parsing protocol retry sidecar: %w", err)
	}
	return &sidecar, nil
}

// WriteProtocolRetrySidecar writes a retry sidecar atomically under dir.
func WriteProtocolRetrySidecar(dir string, sidecar ProtocolRetrySidecar) error {
	return WriteProtocolRetrySidecarAt(dir, ProtocolRetrySidecarFile, sidecar)
}

// WriteProtocolRetrySidecarAt writes a retry sidecar atomically under dir with the supplied filename.
func WriteProtocolRetrySidecarAt(dir, filename string, sidecar ProtocolRetrySidecar) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating protocol retry sidecar dir: %w", err)
	}

	data, err := yaml.Marshal(sidecar)
	if err != nil {
		return fmt.Errorf("marshalling protocol retry sidecar: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".protocol-retry-*.yaml.tmp")
	if err != nil {
		return fmt.Errorf("creating protocol retry sidecar temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing protocol retry sidecar temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing protocol retry sidecar temp file: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(dir, protocolRetrySidecarFilename(filename))); err != nil {
		return fmt.Errorf("renaming protocol retry sidecar temp file: %w", err)
	}
	cleanup = false
	return nil
}

// DeleteProtocolRetrySidecar removes the retry sidecar. Missing files are not errors.
func DeleteProtocolRetrySidecar(dir string) error {
	return DeleteProtocolRetrySidecarAt(dir, ProtocolRetrySidecarFile)
}

// DeleteProtocolRetrySidecarAt removes the retry sidecar with the supplied filename.
func DeleteProtocolRetrySidecarAt(dir, filename string) error {
	err := os.Remove(filepath.Join(dir, protocolRetrySidecarFilename(filename)))
	if err == nil || os.IsNotExist(err) {
		return nil
	}
	return fmt.Errorf("removing protocol retry sidecar: %w", err)
}

func protocolRetrySidecarFilename(filename string) string {
	if filename == "" {
		return ProtocolRetrySidecarFile
	}
	return filename
}

// RemovePhaseCompleteMarker removes only the phase_complete marker. Missing files are not errors.
func RemovePhaseCompleteMarker(dir string) error {
	err := os.Remove(filepath.Join(dir, PhaseCompleteFile))
	if err == nil || os.IsNotExist(err) {
		return nil
	}
	return fmt.Errorf("removing phase_complete marker: %w", err)
}

// DecideProtocolRetry computes the bounded retry action for protocol violations.
func DecideProtocolRetry(
	role Role,
	phaseDir string,
	currentActiveRun int,
	sidecar *ProtocolRetrySidecar,
	violations []ProtocolViolation,
	maxConsecutive int,
) ProtocolRetryDecision {
	if len(violations) == 0 {
		return ProtocolRetryDecision{Action: ProtocolRetryActionSucceed}
	}

	consecutive := 1
	if sidecar != nil && sidecar.ActiveRun == currentActiveRun && sidecar.Role == role {
		consecutive = sidecar.Consecutive + 1
	}

	cap := maxConsecutive
	if cap <= 0 {
		cap = DefaultMaxConsecutiveProtocolViolations
	}

	action := ProtocolRetryActionRetry
	if consecutive >= cap {
		action = ProtocolRetryActionTerminal
	}

	lastViolation := JoinProtocolViolations(violations)
	return ProtocolRetryDecision{
		Action:         action,
		Consecutive:    consecutive,
		FormattedError: FormatSingleShotProtocolViolationError(role, phaseDir, violations),
		NewSidecar: &ProtocolRetrySidecar{
			Role:          role,
			ActiveRun:     currentActiveRun,
			Consecutive:   consecutive,
			LastViolation: lastViolation,
			UpdatedAt:     time.Now().UTC().Round(0),
		},
	}
}

// FormatSingleShotProtocolViolationError renders the canonical protocol violation LastError.
func FormatSingleShotProtocolViolationError(role Role, dir string, violations []ProtocolViolation) string {
	if dir == "" {
		dir = "<unresolved>"
	}
	reason := JoinProtocolViolations(violations)
	if strings.TrimSpace(reason) == "" {
		reason = "contract validation failed"
	}
	return fmt.Sprintf("protocol violation: %s @ %s: %s", role, dir, reason)
}
