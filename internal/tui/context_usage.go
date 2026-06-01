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

package tui

import (
	"fmt"

	"charm.land/lipgloss/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

type smartZoneContextUsage struct {
	fillTokens      int
	thresholdTokens int
	pct             int
}

func emptySmartZoneContextUsage() smartZoneContextUsage {
	return smartZoneContextUsage{fillTokens: -1, pct: -1}
}

func newSmartZoneContextUsage(fillTokens, thresholdTokens int) smartZoneContextUsage {
	if fillTokens < 0 || thresholdTokens <= 0 {
		return emptySmartZoneContextUsage()
	}
	return smartZoneContextUsage{
		fillTokens:      fillTokens,
		thresholdTokens: thresholdTokens,
		pct:             fillTokens * 100 / thresholdTokens,
	}
}

func smartZoneContextUsageForSession(sess session.SessionView) smartZoneContextUsage {
	if sess == nil {
		return emptySmartZoneContextUsage()
	}
	return newSmartZoneContextUsage(sess.ContextFillTokens(), sess.ContextHandoffThresholdTokens())
}

func (u smartZoneContextUsage) available() bool {
	return u.fillTokens >= 0 && u.thresholdTokens > 0 && u.pct >= 0
}

func (u smartZoneContextUsage) closerThan(other smartZoneContextUsage) bool {
	if !u.available() {
		return false
	}
	if !other.available() {
		return true
	}
	return int64(u.fillTokens)*int64(other.thresholdTokens) > int64(other.fillTokens)*int64(u.thresholdTokens)
}

func smartZoneContextUsageText(u smartZoneContextUsage) string {
	if !u.available() {
		return MutedStyle.Render("Calculating...")
	}
	return smartZoneContextUsageStyle(u).Render(formatSmartZoneContextUsage(u))
}

func formatSmartZoneContextUsage(u smartZoneContextUsage) string {
	return fmt.Sprintf("%s / %s (%d%%)", formatTokenK(u.fillTokens), formatTokenK(u.thresholdTokens), u.pct)
}

func formatTokenK(tokens int) string {
	if tokens < 0 {
		tokens = 0
	}
	return fmt.Sprintf("%dK", tokens/1000)
}

func smartZoneContextUsageStyle(u smartZoneContextUsage) lipgloss.Style {
	switch {
	case !u.available():
		return MutedStyle
	case u.pct >= 90:
		return ErrorStyle
	case u.pct >= 75:
		return WarningStyle
	default:
		return SuccessStyle
	}
}
