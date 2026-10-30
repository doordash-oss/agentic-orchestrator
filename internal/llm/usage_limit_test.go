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

package llm

import (
	"testing"
	"time"
)

func TestParseUsageLimit(t *testing.T) {
	t.Parallel()
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	rome, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-10-29 08:15 in Los Angeles; Codex prints in this machine-local zone.
	now := time.Date(2026, time.October, 29, 8, 15, 0, 0, la)

	cases := []struct {
		name     string
		text     string
		provider string
		want     time.Time
	}{
		{
			name:     "claude five-hour limit later today",
			text:     "waiting for implementer outcome: You've hit your 5-hour limit · resets 11:10am (America/Los_Angeles)",
			provider: "claude",
			want:     time.Date(2026, time.October, 29, 11, 10, 0, 0, la),
		},
		{
			name:     "claude reset time already passed today rolls to tomorrow",
			text:     "You've hit your 5-hour limit · resets 4:30am (America/Los_Angeles)",
			provider: "claude",
			want:     time.Date(2026, time.October, 30, 4, 30, 0, 0, la),
		},
		{
			name:     "claude hour without minutes in another zone",
			text:     "You’ve hit your limit · resets 9pm (Europe/Rome)",
			provider: "claude",
			want:     time.Date(2026, time.October, 29, 21, 0, 0, 0, rome),
		},
		{
			name:     "claude weekly limit with date",
			text:     "You've hit your weekly limit · resets Nov 3, 9am (America/Los_Angeles)",
			provider: "claude",
			want:     time.Date(2026, time.November, 3, 9, 0, 0, 0, la),
		},
		{
			name:     "claude legacy epoch form",
			text:     "Claude AI usage limit reached|1761760800",
			provider: "claude",
			want:     time.Unix(1761760800, 0),
		},
		{
			name:     "codex same-day time",
			text:     "session failed: You’ve hit your usage limit. Upgrade to Pro (https://chatgpt.com/explore/pro), visit https://chatgpt.com/settings/usage to purchase more credits or try again at 11:30 AM.",
			provider: "codex",
			want:     time.Date(2026, time.October, 29, 11, 30, 0, 0, la),
		},
		{
			name:     "codex full date",
			text:     "You’ve hit your usage limit. Upgrade to Pro (https://chatgpt.com/explore/pro), visit https://chatgpt.com/settings/usage to purchase more credits or try again at Oct 30th, 2026 12:12 AM.",
			provider: "codex",
			want:     time.Date(2026, time.October, 30, 0, 12, 0, 0, la),
		},
		{
			name:     "codex evening time",
			text:     "waiting for implementer outcome: You've hit your usage limit. Upgrade to Plus to continue using Codex (https://openai.com/chatgpt/pricing), or try again at 6:43 PM.",
			provider: "codex",
			want:     time.Date(2026, time.October, 29, 18, 43, 0, 0, la),
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ParseUsageLimit(tc.text, now)
			if !ok {
				t.Fatalf("ParseUsageLimit(%q) did not match", tc.text)
			}
			if got.Provider != tc.provider {
				t.Errorf("provider = %q, want %q", got.Provider, tc.provider)
			}
			if !got.ResetAt.Equal(tc.want) {
				t.Errorf("ResetAt = %s, want %s", got.ResetAt, tc.want)
			}
		})
	}
}

func TestParseUsageLimitRejectsOtherErrors(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.October, 29, 8, 15, 0, 0, time.UTC)
	for _, text := range []string{
		"",
		"Selected model is at capacity. Please try a different model.",
		"rate limit exceeded (429)",
		"You've hit your usage limit. Upgrade to Pro or try again later.",
		"You've hit your 5-hour limit · resets soon (America/Los_Angeles)",
		"You've hit your 5-hour limit · resets 4:30am (Not/AZone)",
	} {
		if got, ok := ParseUsageLimit(text, now); ok {
			t.Errorf("ParseUsageLimit(%q) = %+v, want no match", text, got)
		}
	}
}
