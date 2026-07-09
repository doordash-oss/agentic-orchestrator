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
	"math"
	"math/rand"
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

// KBProtocolRetrySidecarFilename returns the feature-scoped KB retry sidecar filename.
func KBProtocolRetrySidecarFilename(featureID string) string {
	return ".protocol-retry-" + featureID + ".yaml"
}

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
	// RateLimited records whether the current violation streak was classified
	// as an upstream rate-limit failure (and therefore governed by the
	// larger rate-limit budget and exponential backoff).
	RateLimited bool `yaml:"rate_limited,omitempty"`
}

// ProtocolRetryDecision tells the caller whether to succeed, retry, or fail terminally.
type ProtocolRetryDecision struct {
	Action         ProtocolRetryAction
	Consecutive    int
	FormattedError string
	NewSidecar     *ProtocolRetrySidecar
	// RetryDelay is how long the caller should wait before restarting the
	// phase. It is non-zero only for rate-limit-classified retries under an
	// enabled RateLimitRetryPolicy; all other retries are immediate.
	RetryDelay time.Duration
}

// RateLimitRetryPolicy tunes the bounded exponential backoff applied to
// rate-limit-classified protocol violations. The zero value is inert; callers
// build one via DefaultRateLimitRetryPolicy or from config. Numeric zero
// fields fall back to defaults at decision time so partially-specified
// policies stay safe.
type RateLimitRetryPolicy struct {
	Enabled    bool
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
	Multiplier float64
	Jitter     float64

	// jitterFn, when set, replaces the default RNG for jitter. It must return
	// a value in [-1, 1]. Test-only seam for deterministic backoff assertions.
	jitterFn func() float64
}

// DefaultRateLimitRetryPolicy returns the built-in backoff policy: enabled, a
// 6-attempt budget, 15s base doubling up to a 5m cap, with 20% jitter.
func DefaultRateLimitRetryPolicy() RateLimitRetryPolicy {
	return RateLimitRetryPolicy{
		Enabled:    true,
		MaxRetries: 6,
		BaseDelay:  15 * time.Second,
		MaxDelay:   5 * time.Minute,
		Multiplier: 2.0,
		Jitter:     0.2,
	}
}

// WithJitterFn returns a copy of the policy with a deterministic jitter
// source. Intended for tests.
func (p RateLimitRetryPolicy) WithJitterFn(fn func() float64) RateLimitRetryPolicy {
	p.jitterFn = fn
	return p
}

// withDefaults fills numeric zero/invalid fields from the built-in defaults,
// leaving Enabled and the jitter seam untouched.
func (p RateLimitRetryPolicy) withDefaults() RateLimitRetryPolicy {
	d := DefaultRateLimitRetryPolicy()
	if p.MaxRetries <= 0 {
		p.MaxRetries = d.MaxRetries
	}
	if p.BaseDelay <= 0 {
		p.BaseDelay = d.BaseDelay
	}
	if p.MaxDelay <= 0 {
		p.MaxDelay = d.MaxDelay
	}
	if p.MaxDelay < p.BaseDelay {
		p.MaxDelay = p.BaseDelay
	}
	if p.Multiplier <= 1 {
		p.Multiplier = d.Multiplier
	}
	if p.Jitter < 0 {
		p.Jitter = 0
	}
	return p
}

// backoffDelay computes the delay for the given 1-based consecutive attempt:
// base * multiplier^(consecutive-1), clamped to MaxDelay, then randomized by
// +/- Jitter. Assumes withDefaults has been applied.
func (p RateLimitRetryPolicy) backoffDelay(consecutive int) time.Duration {
	if consecutive < 1 {
		consecutive = 1
	}
	grown := float64(p.BaseDelay) * math.Pow(p.Multiplier, float64(consecutive-1))
	maxD := float64(p.MaxDelay)
	if grown > maxD || math.IsInf(grown, 1) {
		grown = maxD
	}
	if p.Jitter > 0 {
		grown *= 1 + p.Jitter*p.jitterFraction()
	}
	if grown < 0 {
		grown = 0
	}
	return time.Duration(grown)
}

func (p RateLimitRetryPolicy) jitterFraction() float64 {
	if p.jitterFn != nil {
		f := p.jitterFn()
		if f < -1 {
			return -1
		}
		if f > 1 {
			return 1
		}
		return f
	}
	return rand.Float64()*2 - 1 //nolint:gosec // jitter does not need crypto-grade randomness
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

// DecideProtocolRetry computes the bounded retry action for protocol
// violations using the default immediate-retry policy (no backoff). Callers
// that want rate-limit-aware backoff use DecideProtocolRetryWithRateLimit.
func DecideProtocolRetry(
	role Role,
	phaseDir string,
	currentActiveRun int,
	sidecar *ProtocolRetrySidecar,
	violations []ProtocolViolation,
	maxConsecutive int,
) ProtocolRetryDecision {
	return decideProtocolRetry(role, phaseDir, currentActiveRun, sidecar, violations, maxConsecutive, false, RateLimitRetryPolicy{})
}

// DecideProtocolRetryWithRateLimit is DecideProtocolRetry plus rate-limit
// awareness. When isRateLimit is true and policy.Enabled is set, the decision
// uses the policy's larger MaxRetries budget as the cap and returns an
// exponential RetryDelay; otherwise it behaves identically to
// DecideProtocolRetry (immediate retry, defaultMaxConsecutive cap).
func DecideProtocolRetryWithRateLimit(
	role Role,
	phaseDir string,
	currentActiveRun int,
	sidecar *ProtocolRetrySidecar,
	violations []ProtocolViolation,
	defaultMaxConsecutive int,
	isRateLimit bool,
	policy RateLimitRetryPolicy,
) ProtocolRetryDecision {
	return decideProtocolRetry(role, phaseDir, currentActiveRun, sidecar, violations, defaultMaxConsecutive, isRateLimit, policy)
}

func decideProtocolRetry(
	role Role,
	phaseDir string,
	currentActiveRun int,
	sidecar *ProtocolRetrySidecar,
	violations []ProtocolViolation,
	maxConsecutive int,
	isRateLimit bool,
	policy RateLimitRetryPolicy,
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

	// Rate-limit failures get a larger budget and exponential backoff, but
	// only when the policy is enabled. A disabled policy degrades to the
	// default immediate-retry path even for rate-limit classifications.
	rateLimited := isRateLimit && policy.Enabled
	var retryDelay time.Duration
	if rateLimited {
		pol := policy.withDefaults()
		cap = pol.MaxRetries
		retryDelay = pol.backoffDelay(consecutive)
	}

	action := ProtocolRetryActionRetry
	if consecutive >= cap {
		action = ProtocolRetryActionTerminal
		// No point waiting before a terminal failure.
		retryDelay = 0
	}

	lastViolation := JoinProtocolViolations(violations)
	return ProtocolRetryDecision{
		Action:         action,
		Consecutive:    consecutive,
		FormattedError: FormatSingleShotProtocolViolationError(role, phaseDir, violations),
		RetryDelay:     retryDelay,
		NewSidecar: &ProtocolRetrySidecar{
			Role:          role,
			ActiveRun:     currentActiveRun,
			Consecutive:   consecutive,
			LastViolation: lastViolation,
			UpdatedAt:     time.Now().UTC().Round(0),
			RateLimited:   rateLimited,
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
