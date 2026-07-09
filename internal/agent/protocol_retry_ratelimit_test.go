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
	"testing"
	"time"
)

var rlViolations = []ProtocolViolation{{Artifact: PhaseCompleteFile, Reason: "missing"}}

// noJitter is a deterministic jitter source (fraction 0 => nominal delay).
func noJitterPolicy() RateLimitRetryPolicy {
	return RateLimitRetryPolicy{
		Enabled:    true,
		MaxRetries: 6,
		BaseDelay:  time.Second,
		MaxDelay:   time.Hour,
		Multiplier: 2.0,
		Jitter:     0,
	}.WithJitterFn(func() float64 { return 0 })
}

func researcherSidecar(activeRun, consecutive int, rateLimited bool) *ProtocolRetrySidecar {
	return &ProtocolRetrySidecar{
		Role:        RoleResearcher,
		ActiveRun:   activeRun,
		Consecutive: consecutive,
		RateLimited: rateLimited,
	}
}

func decideRL(sidecar *ProtocolRetrySidecar, isRateLimit bool, policy RateLimitRetryPolicy) ProtocolRetryDecision {
	return DecideProtocolRetryWithRateLimit(
		RoleResearcher, "/dir", 1, sidecar, rlViolations,
		DefaultMaxConsecutiveProtocolViolations, isRateLimit, policy,
	)
}

func TestDecideProtocolRetry_NonRateLimit_UnchangedCap3(t *testing.T) {
	t.Parallel()
	policy := noJitterPolicy()

	// consecutive 1 and 2 retry, delay always zero.
	for _, prior := range []int{0, 1} {
		var sc *ProtocolRetrySidecar
		if prior > 0 {
			sc = researcherSidecar(1, prior, false)
		}
		got := decideRL(sc, false, policy)
		if got.Action != ProtocolRetryActionRetry {
			t.Fatalf("prior=%d Action = %v, want Retry", prior, got.Action)
		}
		if got.RetryDelay != 0 {
			t.Errorf("prior=%d RetryDelay = %v, want 0 (non-rate-limit is immediate)", prior, got.RetryDelay)
		}
		if got.NewSidecar.RateLimited {
			t.Errorf("prior=%d NewSidecar.RateLimited = true, want false", prior)
		}
	}

	// third consecutive terminates at the default cap.
	got := decideRL(researcherSidecar(1, 2, false), false, policy)
	if got.Action != ProtocolRetryActionTerminal {
		t.Fatalf("Action = %v, want Terminal at cap %d", got.Action, DefaultMaxConsecutiveProtocolViolations)
	}
}

func TestDecideProtocolRetry_RateLimit_UsesLargerCap(t *testing.T) {
	t.Parallel()
	policy := noJitterPolicy() // MaxRetries=6

	// consecutive 3, 4, 5 still retry (past the default cap of 3).
	for _, prior := range []int{2, 3, 4} {
		got := decideRL(researcherSidecar(1, prior, true), true, policy)
		if got.Action != ProtocolRetryActionRetry {
			t.Errorf("prior=%d Action = %v, want Retry under rate-limit cap 6", prior, got.Action)
		}
		if !got.NewSidecar.RateLimited {
			t.Errorf("prior=%d NewSidecar.RateLimited = false, want true", prior)
		}
	}

	// sixth consecutive hits the rate-limit cap and terminates.
	got := decideRL(researcherSidecar(1, 5, true), true, policy)
	if got.Action != ProtocolRetryActionTerminal {
		t.Fatalf("Action = %v, want Terminal at rate-limit cap 6", got.Action)
	}
	if got.RetryDelay != 0 {
		t.Errorf("terminal RetryDelay = %v, want 0", got.RetryDelay)
	}
}

func TestDecideProtocolRetry_ExponentialGrowth(t *testing.T) {
	t.Parallel()
	policy := noJitterPolicy() // base 1s, mult 2, no jitter

	cases := []struct {
		prior int
		want  time.Duration
	}{
		{prior: 0, want: 1 * time.Second},
		{prior: 1, want: 2 * time.Second},
		{prior: 2, want: 4 * time.Second},
		{prior: 3, want: 8 * time.Second},
	}
	for _, c := range cases {
		var sc *ProtocolRetrySidecar
		if c.prior > 0 {
			sc = researcherSidecar(1, c.prior, true)
		}
		got := decideRL(sc, true, policy)
		if got.RetryDelay != c.want {
			t.Errorf("prior=%d RetryDelay = %v, want %v", c.prior, got.RetryDelay, c.want)
		}
	}
}

func TestDecideProtocolRetry_MaxDelayClamp(t *testing.T) {
	t.Parallel()
	policy := RateLimitRetryPolicy{
		Enabled:    true,
		MaxRetries: 20,
		BaseDelay:  time.Second,
		MaxDelay:   5 * time.Second,
		Multiplier: 2.0,
		Jitter:     0,
	}.WithJitterFn(func() float64 { return 0 })

	// prior=4 => 1s*2^4 = 16s, clamped to 5s.
	got := decideRL(researcherSidecar(1, 4, true), true, policy)
	if got.RetryDelay != 5*time.Second {
		t.Errorf("RetryDelay = %v, want clamp to 5s", got.RetryDelay)
	}
}

func TestDecideProtocolRetry_JitterBounds(t *testing.T) {
	t.Parallel()
	// Deterministic jitter source cycling extreme and mid fractions.
	fracs := []float64{-1, -0.5, 0, 0.5, 1, 0.9, -0.9}
	i := 0
	policy := RateLimitRetryPolicy{
		Enabled:    true,
		MaxRetries: 10,
		BaseDelay:  10 * time.Second,
		MaxDelay:   time.Hour,
		Multiplier: 2.0,
		Jitter:     0.2,
	}.WithJitterFn(func() float64 {
		f := fracs[i%len(fracs)]
		i++
		return f
	})

	nominal := 10 * time.Second // prior=0
	lo := time.Duration(float64(nominal) * 0.8)
	hi := time.Duration(float64(nominal) * 1.2)
	for n := 0; n < len(fracs); n++ {
		got := decideRL(nil, true, policy)
		if got.RetryDelay < lo || got.RetryDelay > hi {
			t.Errorf("iter %d RetryDelay = %v, want within [%v, %v]", n, got.RetryDelay, lo, hi)
		}
	}
}

func TestDecideProtocolRetry_PolicyDisabled(t *testing.T) {
	t.Parallel()
	policy := noJitterPolicy()
	policy.Enabled = false

	// Even though isRateLimit is true, a disabled policy behaves like the
	// default immediate path: cap 3, zero delay, sidecar not rate-limited.
	got := decideRL(researcherSidecar(1, 2, false), true, policy)
	if got.Action != ProtocolRetryActionTerminal {
		t.Errorf("Action = %v, want Terminal at default cap 3 when disabled", got.Action)
	}
	got = decideRL(nil, true, policy)
	if got.RetryDelay != 0 {
		t.Errorf("RetryDelay = %v, want 0 when disabled", got.RetryDelay)
	}
	if got.NewSidecar.RateLimited {
		t.Error("NewSidecar.RateLimited = true, want false when disabled")
	}
}

func TestDecideProtocolRetry_ZeroValuesFallBackToDefaults(t *testing.T) {
	t.Parallel()
	// Enabled but everything else zero: withDefaults should supply sane
	// values so a retry is produced with a positive delay and the default
	// rate-limit budget (6) rather than terminating immediately.
	policy := RateLimitRetryPolicy{Enabled: true}.WithJitterFn(func() float64 { return 0 })

	got := decideRL(nil, true, policy)
	if got.Action != ProtocolRetryActionRetry {
		t.Fatalf("Action = %v, want Retry", got.Action)
	}
	if got.RetryDelay <= 0 {
		t.Errorf("RetryDelay = %v, want positive default base delay", got.RetryDelay)
	}

	// consecutive 5 still retries under the default cap of 6.
	got = decideRL(researcherSidecar(1, 4, true), true, policy)
	if got.Action != ProtocolRetryActionRetry {
		t.Errorf("Action = %v, want Retry under default cap 6", got.Action)
	}
}

func TestDecideProtocolRetry_SidecarRateLimitedPersisted(t *testing.T) {
	t.Parallel()
	policy := noJitterPolicy()

	got := decideRL(researcherSidecar(1, 1, true), true, policy)
	if got.Consecutive != 2 {
		t.Errorf("Consecutive = %d, want 2 (streak continuity)", got.Consecutive)
	}
	if !got.NewSidecar.RateLimited {
		t.Error("NewSidecar.RateLimited = false, want true")
	}
}

func TestDecideProtocolRetry_SidecarRateLimitedRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	want := ProtocolRetrySidecar{
		Role:        RoleResearcher,
		ActiveRun:   3,
		Consecutive: 2,
		RateLimited: true,
		UpdatedAt:   time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := WriteProtocolRetrySidecar(dir, want); err != nil {
		t.Fatalf("WriteProtocolRetrySidecar() error = %v", err)
	}
	got, err := ReadProtocolRetrySidecar(dir)
	if err != nil {
		t.Fatalf("ReadProtocolRetrySidecar() error = %v", err)
	}
	if got == nil || !got.RateLimited {
		t.Fatalf("round-trip RateLimited = %v, want true", got)
	}
}
