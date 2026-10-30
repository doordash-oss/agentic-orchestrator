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
	"regexp"
	"strconv"
	"strings"
	"time"
)

// UsageLimit is a subscription usage limit reported by a provider CLI
// together with the moment the provider says the limit resets.
type UsageLimit struct {
	// Provider names the CLI that reported the limit: "claude" or "codex".
	Provider string
	// ResetAt is the reset moment the provider reported.
	ResetAt time.Time
}

var (
	// Claude Code: "You've hit your 5-hour limit · resets 4:30am (America/Los_Angeles)",
	// "You've hit your weekly limit · resets Nov 3, 9am (Europe/Rome)".
	claudeUsageLimitPattern = regexp.MustCompile(`(?i)limit(?: reached)?\s*[·∙•|-]\s*resets\s+([^()\n"]+?)\s*(?:\(([^()\s]+)\))?\s*(?:$|[\n".])`)
	// Older Claude Code: "Claude AI usage limit reached|1761728400".
	claudeLegacyUsageLimitPattern = regexp.MustCompile(`(?i)claude ai usage limit reached\|(\d{9,11})`)
	// Codex: "You've hit your usage limit. ... or try again at 11:30 AM.",
	// "... try again at Oct 29th, 2026 12:12 AM.".
	codexUsageLimitPattern = regexp.MustCompile(`(?is)hit your usage limit.*?try again at ([^\n."]+)`)

	ordinalSuffixPattern = regexp.MustCompile(`(\d)(st|nd|rd|th)\b`)
	meridiemSpacePattern = regexp.MustCompile(`\s+(am|pm)\b`)
)

// Layouts accepted for a provider's reset time after normalization
// (lowercase, no ordinals, no commas, no "at", meridiem glued to the time).
var (
	usageLimitClockLayouts = []string{"3:04pm", "3pm", "15:04"}
	usageLimitDayLayouts   = []string{"Jan 2 3:04pm", "Jan 2 3pm", "Jan 2 15:04"}
	usageLimitDateLayouts  = []string{"Jan 2 2006 3:04pm", "Jan 2 2006 3pm", "Jan 2 2006 15:04"}
)

// ParseUsageLimit recognizes the Claude Code and Codex usage-limit errors
// and resolves the reset time they report. Codex prints wall-clock times in
// the machine's zone, so now's location stands in for it; Claude names its
// zone explicitly. A time without a date resolves to its next occurrence
// after now. ok is false when text is not a usage-limit error or carries no
// reset time Agentico can read.
func ParseUsageLimit(text string, now time.Time) (UsageLimit, bool) {
	if m := claudeLegacyUsageLimitPattern.FindStringSubmatch(text); m != nil {
		secs, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return UsageLimit{}, false
		}
		return UsageLimit{Provider: "claude", ResetAt: time.Unix(secs, 0).In(now.Location())}, true
	}
	if m := claudeUsageLimitPattern.FindStringSubmatch(text); m != nil {
		loc := now.Location()
		if m[2] != "" {
			named, err := time.LoadLocation(m[2])
			if err != nil {
				return UsageLimit{}, false
			}
			loc = named
		}
		resetAt, ok := resolveResetTime(m[1], now, loc)
		if !ok {
			return UsageLimit{}, false
		}
		return UsageLimit{Provider: "claude", ResetAt: resetAt}, true
	}
	if m := codexUsageLimitPattern.FindStringSubmatch(text); m != nil {
		resetAt, ok := resolveResetTime(m[1], now, now.Location())
		if !ok {
			return UsageLimit{}, false
		}
		return UsageLimit{Provider: "codex", ResetAt: resetAt}, true
	}
	return UsageLimit{}, false
}

// resolveResetTime parses a provider's reset phrase in loc and anchors any
// missing date or year to now.
func resolveResetTime(raw string, now time.Time, loc *time.Location) (time.Time, bool) {
	spec := strings.ToLower(strings.TrimSpace(raw))
	spec = ordinalSuffixPattern.ReplaceAllString(spec, "$1")
	spec = strings.ReplaceAll(spec, ",", " ")
	spec = strings.ReplaceAll(spec, " at ", " ")
	spec = strings.Join(strings.Fields(spec), " ")
	spec = meridiemSpacePattern.ReplaceAllString(spec, "$1")
	local := now.In(loc)

	for _, layout := range usageLimitClockLayouts {
		if t, err := time.Parse(layout, spec); err == nil {
			candidate := time.Date(local.Year(), local.Month(), local.Day(), t.Hour(), t.Minute(), 0, 0, loc)
			if !candidate.After(now) {
				candidate = candidate.AddDate(0, 0, 1)
			}
			return candidate, true
		}
	}
	for _, layout := range usageLimitDayLayouts {
		if t, err := time.Parse(layout, spec); err == nil {
			candidate := time.Date(local.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, loc)
			// A date more than a day behind now belongs to next year (a
			// December limit that resets in January).
			if candidate.Before(now.Add(-24 * time.Hour)) {
				candidate = candidate.AddDate(1, 0, 0)
			}
			return candidate, true
		}
	}
	for _, layout := range usageLimitDateLayouts {
		if t, err := time.Parse(layout, spec); err == nil {
			return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, loc), true
		}
	}
	return time.Time{}, false
}
