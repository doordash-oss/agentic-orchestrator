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

package testutil

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderTerminalPNGFitsJourneyGrid renders a worst-case 140x42 terminal
// frame (every column occupied, including wide border glyphs) and asserts
// the capture is not clipped. Skips when no headless renderer exists.
func TestRenderTerminalPNGFitsJourneyGrid(t *testing.T) {
	if _, err := RendererPath(); err != nil {
		t.Skipf("no headless renderer: %v", err)
	}
	var sb strings.Builder
	for row := 0; row < 42; row++ {
		sb.WriteString(strings.Repeat("M", 140))
		sb.WriteString("\n")
	}
	pngPath := filepath.Join(t.TempDir(), "fit-1200x800.png")
	if err := RenderTerminalPNG(sb.String(), pngPath, 1200, 800); err != nil {
		t.Fatalf("RenderTerminalPNG: %v", err)
	}
	if err := AssertCaptureUncropped(pngPath); err != nil {
		t.Fatalf("140x42 grid did not fit the 1200x800 capture: %v", err)
	}
}
