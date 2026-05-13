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
	"os"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// iconSet defines the icons used throughout the TUI.
type iconSet struct {
	Created      string
	Researching  string
	Planning     string
	Implementing string
	Ready        string
	Done         string
	Failed       string
	Blocked      string
	Interrupted  string
}

var icons iconSet

func init() {
	if hasNerdFont() {
		icons = iconSet{
			Created:      "\U000f0130", // nf-md-circle_outline
			Researching:  "\U000f0349", // nf-md-magnify
			Planning:     "\U000f03eb", // nf-md-pencil
			Implementing: "\U000f0174", // nf-md-code_braces
			Ready:        "\U000f03e4", // nf-md-pause_circle_outline
			Done:         "\U000f0134", // nf-md-check_circle
			Failed:       "\U000f0159", // nf-md-close_circle
			Blocked:      "\U000f033e", // nf-md-lock
			Interrupted:  "\U000f0446", // nf-md-stop_circle
		}
	} else {
		icons = iconSet{
			Created:      "\u25cb", // ○
			Researching:  "\u25b6", // ▶
			Planning:     "\u25b6", // ▶
			Implementing: "\u25b6", // ▶
			Ready:        "\u25cb", // ○
			Done:         "\u2713", // ✓
			Failed:       "\u2717", // ✗
			Blocked:      "\u25a0", // ■
			Interrupted:  "\u25a0", // ■
		}
	}
}

// hasNerdFont checks the NERD_FONT environment variable or terminal heuristics.
func hasNerdFont() bool {
	if v := os.Getenv("NERD_FONT"); v != "" {
		return v == "1" || strings.EqualFold(v, "true")
	}
	// Heuristic: some terminals bundle Nerd Fonts
	termProgram := strings.ToLower(os.Getenv("TERM_PROGRAM"))
	return strings.Contains(termProgram, "wezterm") ||
		strings.Contains(termProgram, "kitty")
}

func pipelineProfileIcon(profile feature.PipelineProfile) string {
	switch profile {
	case feature.PipelineMedium:
		return "⚡"
	case feature.PipelineLarge:
		return "🔬"
	case feature.PipelineMoonshot:
		return "🚀"
	default:
		return ""
	}
}
