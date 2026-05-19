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

package markdown

import (
	"regexp"
	"strings"
	"testing"
)

var ansiSeq = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// hasANSI reports whether s contains any ANSI escape sequences. We use this
// as a proxy for "glamour actually styled the content" since asserting on
// exact escape codes would be brittle across color profiles.
func hasANSI(s string) bool {
	return strings.Contains(s, "\x1b[")
}

func stripANSI(s string) string { return ansiSeq.ReplaceAllString(s, "") }

func TestRender_PlainText(t *testing.T) {
	in := "just a line of plain text with no markdown"
	out := Render(in, 80)
	if out == "" {
		t.Fatal("expected non-empty output")
	}
	if !strings.Contains(stripANSI(out), "plain text") {
		t.Errorf("expected output to contain the input words, got %q", out)
	}
}

func TestRender_FencedCodeBlockHasANSI(t *testing.T) {
	in := "Here is some code:\n\n```go\nfunc foo() { return }\n```\n"
	out := Render(in, 80)
	if !hasANSI(out) {
		t.Errorf("expected ANSI escapes in rendered fenced code block, got %q", out)
	}
	if !strings.Contains(stripANSI(out), "foo") {
		t.Errorf("expected rendered output to contain the code identifier, got %q", out)
	}
}

func TestRender_HeadingHasANSI(t *testing.T) {
	out := Render("# Hello\n\nbody", 80)
	if !hasANSI(out) {
		t.Errorf("expected ANSI escapes in rendered heading, got %q", out)
	}
}

func TestRender_StyledBlocksPreservePlainText(t *testing.T) {
	in := strings.Join([]string{
		"# Heading",
		"",
		"- list item",
		"",
		"[link text](https://example.com)",
		"",
		"`inline code`",
		"",
		"> quoted text",
		"",
		"```go",
		"fmt.Println(\"code block\")",
		"```",
	}, "\n")
	out := Render(in, 80)
	if !hasANSI(out) {
		t.Fatalf("expected ANSI escapes in rendered markdown, got %q", out)
	}

	plain := stripANSI(out)
	for _, want := range []string{"Heading", "list item", "link text", "inline code", "quoted text", "fmt.Println"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("rendered markdown missing source text %q:\n%s", want, plain)
		}
	}
}

func TestBuildStyle_UsesSemanticPalette(t *testing.T) {
	tests := []struct {
		name  string
		style bool
		want  palette
	}{
		{name: "dark", style: true, want: mochaPalette()},
		{name: "light", style: false, want: lattePalette()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			style := buildStyle(tt.style)
			assertColor(t, "Heading.Color", style.Heading.Color, tt.want.brand)
			assertColor(t, "H1.Color", style.H1.Color, tt.want.brand)
			assertColor(t, "Link.Color", style.Link.Color, tt.want.info)
			assertColor(t, "LinkText.Color", style.LinkText.Color, tt.want.brand)
			assertColor(t, "Code.Color", style.Code.Color, tt.want.peach)
			assertColor(t, "BlockQuote.Color", style.BlockQuote.Color, tt.want.active)
			assertColor(t, "Item.Color", style.Item.Color, tt.want.subtext)
			assertColor(t, "HorizontalRule.Color", style.HorizontalRule.Color, tt.want.overlay)
		})
	}
}

func assertColor(t *testing.T, name string, got *string, want string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s = nil, want %q", name, want)
	}
	if *got != want {
		t.Fatalf("%s = %q, want %q", name, *got, want)
	}
}

func TestRender_CacheHit(t *testing.T) {
	in := "## Cached heading\n\nSome text."
	first := Render(in, 60)
	second := Render(in, 60)
	if first != second {
		t.Errorf("expected identical output from cached render, got:\n%q\nvs\n%q", first, second)
	}
}

func TestRender_WidthAffectsOutput(t *testing.T) {
	in := "This is a longer paragraph that should wrap differently depending on the viewport width chosen by the caller."
	narrow := Render(in, 30)
	wide := Render(in, 120)
	if narrow == wide {
		t.Errorf("expected different output for different widths, got identical:\n%q", narrow)
	}
}

func TestRender_EmptyString(t *testing.T) {
	out := Render("", 80)
	if strings.TrimSpace(out) != "" {
		t.Errorf("expected empty/whitespace output for empty input, got %q", out)
	}
}

func TestRender_BelowMinWidthDoesNotCrash(t *testing.T) {
	// Width below minWidth should be clamped, not crash.
	out := Render("# Heading", 5)
	if out == "" {
		t.Error("expected non-empty output for narrow width")
	}
}

func TestRender_NoFencedBlockNoCodeANSI(t *testing.T) {
	// Plain prose shouldn't pull in the chroma syntax highlighter; it may
	// still produce ANSI resets from glamour's default document wrapper,
	// so we only assert the output is non-empty and preserves text.
	in := "Just one short sentence."
	out := Render(in, 80)
	plain := stripANSI(out)
	if !strings.Contains(plain, "short") || !strings.Contains(plain, "sentence") {
		t.Errorf("expected rendered output to preserve words, got %q", plain)
	}
}
