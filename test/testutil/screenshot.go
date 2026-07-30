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
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// ScreenshotChromeEnv overrides the headless-rendering binary for
// RenderTerminalPNG. When unset, the renderer is discovered portably.
const ScreenshotChromeEnv = "AGENTICO_SCREENSHOT_CHROME"

// screenshotRendererCandidates are probed in order when no explicit
// override is configured, from platform-specific bundle paths to PATH
// discovery, so captures work on macOS and Linux without code changes.
var screenshotRendererCandidates = []string{
	"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	"chromium",
	"chromium-browser",
	"google-chrome",
	"google-chrome-stable",
	"microsoft-edge",
}

// RendererPath resolves the headless-rendering binary used for visual
// evidence captures: the ScreenshotChromeEnv override first, then the
// candidate list. An error names every probed location so a CI/test author
// can set the override directly.
func RendererPath() (string, error) {
	if override := strings.TrimSpace(os.Getenv(ScreenshotChromeEnv)); override != "" {
		if _, err := os.Stat(override); err != nil {
			return "", fmt.Errorf("%s points at %q: %w", ScreenshotChromeEnv, override, err)
		}
		return override, nil
	}
	for _, candidate := range screenshotRendererCandidates {
		if filepath.IsAbs(candidate) {
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
			continue
		}
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("no headless Chrome/Chromium renderer found (probed %v); set %s",
		screenshotRendererCandidates, ScreenshotChromeEnv)
}

// RenderTerminalPNG converts ANSI-styled terminal text to a PNG at
// width x height CSS pixels with the shared 12px/16px body style. It is the
// single headless renderer for every visual-evidence capture in the repo:
// the autoreview_screenshots-tagged TUI helper delegates to
// RenderTerminalPNGStyled.
//
// The 12px font budgets for the widest monospace fallback the headless
// renderer may pick, not just Menlo: at a 0.66em advance (≈7.92px) a full
// 140-column terminal line is ~140 × 7.92px ≈ 1109px, plus 40px padding ≈
// 1149px, still inside the standard 1200px-wide evidence capture; 42 rows
// at 16px plus 48px padding ≈ 720px fits the 800px height. Pair this
// budget with AssertCaptureUncropped on every capture so a fallback font
// outside the budget fails the test instead of shipping a clipped capture.
// The width/height parameters only size the screenshot viewport; the body
// style is identical at every size.
func RenderTerminalPNG(ansi, pngPath string, width, height int) error {
	return RenderTerminalPNGStyled(ansi, pngPath, width, height, 12, 16)
}

// RenderTerminalPNGStyled converts ANSI-styled terminal text to an HTML
// page, renders it via the resolved headless Chrome/Chromium binary, and
// saves the PNG to pngPath sized width x height CSS pixels with fontPx /
// linePx body styling (padding 24px vertical, 20px horizontal at every
// size). The output uses a dark background matching the terminal palette.
func RenderTerminalPNGStyled(ansi, pngPath string, width, height, fontPx, linePx int) error {
	renderer, err := RendererPath()
	if err != nil {
		return err
	}
	html := ansiToHTML(ansi)
	body := fmt.Sprintf("<!doctype html><html><head><meta charset='utf-8'><style>"+
		"html,body{margin:0;background:#1e1e2e;}"+
		"body{padding:24px 20px;}"+
		"pre{font-family:'Menlo','SF Mono','Cascadia Mono',monospace;font-size:%dpx;line-height:%dpx;color:#cdd6f4;white-space:pre;}"+
		"</style></head><body><pre>%s</pre></body></html>", fontPx, linePx, html)
	tmp, err := os.CreateTemp("", "screenshot-*.html")
	if err != nil {
		return fmt.Errorf("create temp html: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(body); err != nil {
		return fmt.Errorf("write html: %w", err)
	}
	tmp.Close()
	if err := os.MkdirAll(filepath.Dir(pngPath), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	cmd := exec.Command(renderer,
		"--headless",
		"--disable-gpu",
		"--screenshot="+pngPath,
		fmt.Sprintf("--window-size=%d,%d", width, height),
		"--force-device-scale-factor=1",
		"--default-background-color=00000000",
		"file://"+tmp.Name(),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("chrome screenshot: %w\n%s", err, out)
	}
	return nil
}

// AssertCaptureUncropped fails the capture when any glyph ink reaches the
// outer ring of the bitmap. Clip artifacts (a cut-off wordmark, a truncated
// right-hand help cluster, a clipped panel border or footer) all present
// identically: non-background pixels at the viewport edge. Any ink inside
// the ring therefore proves the terminal grid did not fit the capture
// viewport. The body background (#1e1e2e) is compared per pixel; a small
// ring width keeps antialiased edge-of-glyph artifacts from producing false
// positives on legitimately fitted captures.
func AssertCaptureUncropped(pngPath string) error {
	f, err := os.Open(pngPath)
	if err != nil {
		return fmt.Errorf("open capture: %w", err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return fmt.Errorf("decode capture: %w", err)
	}
	b := img.Bounds()
	const ring = 2
	isBackground := func(x, y int) bool {
		r, g, bl, a := img.At(x, y).RGBA()
		if a>>8 < 200 {
			// Transparent regions (background-color flag) count as clear.
			return true
		}
		// Body background rgb(30,30,46) with a small antialiasing tolerance.
		return closeByte(r>>8, 30) && closeByte(g>>8, 30) && closeByte(bl>>8, 46)
	}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			inner := x >= b.Min.X+ring && x < b.Max.X-ring && y >= b.Min.Y+ring && y < b.Max.Y-ring
			if inner {
				continue
			}
			if !isBackground(x, y) {
				return fmt.Errorf("capture %s is clipped: ink at bitmap edge (%d,%d) of %dx%d; the rendered terminal grid does not fit the viewport",
					pngPath, x, y, b.Dx(), b.Dy())
			}
		}
	}
	return nil
}

func closeByte(v, want uint32) bool {
	diff := int(v) - int(want)
	return diff >= -12 && diff <= 12
}

var sgrRe = regexp.MustCompile(`\x1b\[([0-9;]*)m`)

func ansiToHTML(s string) string {
	var out strings.Builder
	var fg, bg string
	bold := false
	reset := func() { fg = ""; bg = ""; bold = false }
	open := false
	closeSpan := func() {
		if open {
			out.WriteString("</span>")
			open = false
		}
	}
	emit := func() {
		closeSpan()
		style := ""
		if bold {
			style += "font-weight:bold;"
		}
		if fg != "" {
			style += "color:" + fg + ";"
		}
		if bg != "" {
			style += "background:" + bg + ";"
		}
		if style != "" {
			out.WriteString("<span style='" + style + "'>")
			open = true
		}
	}
	pos := 0
	for _, m := range sgrRe.FindAllStringSubmatchIndex(s, -1) {
		out.WriteString(escapeHTML(s[pos:m[0]]))
		pos = m[1]
		codes := strings.Split(s[m[2]:m[3]], ";")
		emit()
		i := 0
		for i < len(codes) {
			c := codes[i]
			switch c {
			case "", "0":
				reset()
			case "1":
				bold = true
			case "39":
				fg = ""
			case "49":
				bg = ""
			default:
				n := atoi(c)
				switch {
				case n >= 30 && n <= 37:
					fg = basicColor(n - 30)
				case n == 38:
					if i+1 < len(codes) && codes[i+1] == "2" && i+4 < len(codes) {
						fg = fmt.Sprintf("rgb(%s,%s,%s)", codes[i+2], codes[i+3], codes[i+4])
						i += 4
					} else if i+1 < len(codes) && codes[i+1] == "5" && i+2 < len(codes) {
						fg = color256(codes[i+2])
						i += 2
					}
				case n >= 40 && n <= 47:
					bg = basicColor(n - 40)
				case n == 48:
					if i+1 < len(codes) && codes[i+1] == "2" && i+4 < len(codes) {
						bg = fmt.Sprintf("rgb(%s,%s,%s)", codes[i+2], codes[i+3], codes[i+4])
						i += 4
					} else if i+1 < len(codes) && codes[i+1] == "5" && i+2 < len(codes) {
						bg = color256(codes[i+2])
						i += 2
					}
				case n >= 90 && n <= 97:
					fg = basicColor(n - 90 + 8)
				case n >= 100 && n <= 107:
					bg = basicColor(n - 100 + 8)
				}
			}
			i++
		}
		emit()
	}
	out.WriteString(escapeHTML(s[pos:]))
	closeSpan()
	return out.String()
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func basicColor(i int) string {
	pal := []string{
		"#45475a", "#f38ba8", "#a6e3a1", "#f9e2af", "#89b4fa", "#f5c2e7", "#94e2d5", "#bac2de",
		"#585b70", "#f38ba8", "#a6e3a1", "#f9e2af", "#89b4fa", "#f5c2e7", "#94e2d5", "#a6adc8",
	}
	if i >= 0 && i < len(pal) {
		return pal[i]
	}
	return "#cdd6f4"
}

func color256(n string) string {
	idx := atoi(n)
	if idx < 16 {
		return basicColor(idx)
	}
	if idx >= 232 {
		v := 8 + (idx-232)*10
		return fmt.Sprintf("rgb(%d,%d,%d)", v, v, v)
	}
	idx -= 16
	r := idx / 36
	g := (idx / 6) % 6
	b := idx % 6
	scale := []int{0, 95, 135, 175, 215, 255}
	return fmt.Sprintf("rgb(%d,%d,%d)", scale[r], scale[g], scale[b])
}
