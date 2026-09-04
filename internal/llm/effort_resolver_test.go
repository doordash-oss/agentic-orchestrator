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

package llm_test

import (
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func TestResolveEffortAutoFromEmpty(t *testing.T) {
	caps := []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh}
	eff, src := llm.ResolveEffort("", caps, llm.EffortHigh)
	if eff != llm.EffortHigh || src != llm.EffortSourceAuto {
		t.Errorf("empty configured: got (%s, %s), want (high, auto)", eff, src)
	}
}

func TestResolveEffortAutoExplicit(t *testing.T) {
	caps := []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh}
	eff, src := llm.ResolveEffort(llm.EffortAuto, caps, llm.EffortMedium)
	if eff != llm.EffortMedium || src != llm.EffortSourceAuto {
		t.Errorf("auto configured: got (%s, %s), want (medium, auto)", eff, src)
	}
}

func TestResolveEffortExplicitSupported(t *testing.T) {
	caps := []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh}
	eff, src := llm.ResolveEffort(llm.EffortMedium, caps, llm.EffortHigh)
	if eff != llm.EffortMedium || src != llm.EffortSourceExplicit {
		t.Errorf("explicit supported: got (%s, %s), want (medium, explicit)", eff, src)
	}
}

func TestResolveEffortExplicitMaxSupported(t *testing.T) {
	caps := []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortMax}
	eff, src := llm.ResolveEffort(llm.EffortMax, caps, llm.EffortHigh)
	if eff != llm.EffortMax || src != llm.EffortSourceExplicit {
		t.Errorf("explicit max supported: got (%s, %s), want (max, explicit)", eff, src)
	}
}

func TestResolveEffortCapabilityDrift(t *testing.T) {
	caps := []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh}
	eff, src := llm.ResolveEffort(llm.EffortMax, caps, llm.EffortMedium)
	if eff != llm.EffortMedium || src != llm.EffortSourceAuto {
		t.Errorf("drift: got (%s, %s), want (medium, auto) — max not in caps, fallback to pipeline", eff, src)
	}
}

func TestResolveEffortExplicitNoCapabilities(t *testing.T) {
	eff, src := llm.ResolveEffort(llm.EffortHigh, nil, llm.EffortMedium)
	if eff != llm.EffortMedium || src != llm.EffortSourceAuto {
		t.Errorf("no caps: got (%s, %s), want (medium, auto) — model lacks effort control", eff, src)
	}
}

func TestResolveEffortFromString(t *testing.T) {
	caps := []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh}
	eff, src := llm.ResolveEffortFromString("high", caps, llm.EffortLow)
	if eff != llm.EffortHigh || src != llm.EffortSourceExplicit {
		t.Errorf("from string: got (%s, %s), want (high, explicit)", eff, src)
	}
	eff, src = llm.ResolveEffortFromString("", caps, llm.EffortMedium)
	if eff != llm.EffortMedium || src != llm.EffortSourceAuto {
		t.Errorf("from empty string: got (%s, %s), want (medium, auto)", eff, src)
	}
}

func TestEffortDrifted(t *testing.T) {
	caps := []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh}
	if !llm.EffortDrifted(llm.EffortMax, caps) {
		t.Error("max not in [low,medium,high] caps: expected drifted")
	}
	if llm.EffortDrifted(llm.EffortAuto, caps) {
		t.Error("auto should not be drifted")
	}
	if llm.EffortDrifted("", caps) {
		t.Error("empty should not be drifted")
	}
	if llm.EffortDrifted(llm.EffortHigh, caps) {
		t.Error("high in caps should not be drifted")
	}
	if llm.EffortDrifted(llm.EffortHigh, nil) {
		t.Error("high with nil caps should not be drifted (model lacks effort control, not drift)")
	}
	if llm.EffortDrifted(llm.EffortLevel("invalid"), caps) {
		t.Error("invalid effort should not be drifted (it's malformed, not drift)")
	}
}

func TestConfigFieldForRole(t *testing.T) {
	cases := []struct {
		role llm.PhaseRole
		want string
	}{
		{llm.PhaseInquiry, "Inquiry"},
		{llm.PhaseResearch, "Research"},
		{llm.PhasePlanning, "Planning"},
		{llm.PhaseImplementation, "Implementation"},
		{llm.PhaseReview, "Review"},
		{llm.PhaseChat, "Utilities"},
		{llm.PhaseKBBuild, "KBBuild"},
	}
	for _, c := range cases {
		got := llm.ConfigFieldForRole(c.role)
		if got != c.want {
			t.Errorf("ConfigFieldForRole(%s): got %q, want %q", c.role, got, c.want)
		}
	}
}
