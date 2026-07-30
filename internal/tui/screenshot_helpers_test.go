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

//go:build autoreview_screenshots

// This file provides headless screenshot plumbing for visual-evidence tests.
// It is excluded from normal builds by the build tag. The actual ANSI→HTML→
// PNG rendering is shared with every other capture path through
// test/testutil.RenderTerminalPNGStyled; this helper only pins the modal
// framing (1440x900 window, 15px/20px body) the autoreview evidence expects.
package tui

import (
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// renderScreenshot converts ANSI text to a 1440x900 PNG with the shared
// headless renderer at the autoreview evidence's body styling.
func renderScreenshot(ansi, pngPath string) error {
	return testutil.RenderTerminalPNGStyled(ansi, pngPath, 1440, 900, 15, 20)
}
