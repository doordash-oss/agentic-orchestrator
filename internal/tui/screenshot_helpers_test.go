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

// This file provides reusable ANSI-to-HTML rendering and headless Chrome
// screenshot plumbing for visual-evidence tests. It is excluded from normal
// builds by the build tag. Test files that need screenshots call
// renderScreenshot with the ANSI text and output path; this helper handles
// HTML conversion, temp file management, and Chrome invocation.

package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// renderScreenshot converts ANSI text to an HTML page, renders it via
// headless Chrome, and saves the PNG to pngPath. The output is sized
// 1440x900 with a dark background matching the terminal palette.
func renderScreenshot(ansi, pngPath string) error {
	html := ansiToHTML(ansi)
	body := "<!doctype html><html><head><meta charset='utf-8'><style>" +
		"body{margin:0;background:#1e1e2e;padding:24px 28px;}" +
		"pre{font-family:'Menlo','SF Mono','Cascadia Mono',monospace;font-size:15px;line-height:20px;color:#cdd6f4;white-space:pre;}" +
		"</style></head><body><pre>" + html + "</pre></body></html>"
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

	chrome := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	if _, err := os.Stat(chrome); err != nil {
		return fmt.Errorf("chrome not found: %w", err)
	}

	cmd := exec.Command(chrome,
		"--headless",
		"--disable-gpu",
		"--screenshot="+pngPath,
		"--window-size=1440,900",
		"--force-device-scale-factor=1",
		"--default-background-color=00000000",
		"file://"+tmp.Name(),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("chrome screenshot: %w\n%s", err, out)
	}
	return nil
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
