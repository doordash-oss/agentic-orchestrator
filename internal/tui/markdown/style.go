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
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
)

// Catppuccin hex values pulled from internal/tui/styles.go. Kept in sync
// with the semantic palette there (colorBrand, colorInfo, colorPeach, etc.).
//
// Latte = light variant, Mocha = dark variant.
var (
	latteBrand   = "#8839ef"
	latteInfo    = "#1e66f5"
	latteActive  = "#179299"
	lattePeach   = "#fe640b"
	latteSubtext = "#6c6f85"
	latteOverlay = "#9ca0b0"
	latteText    = "#4c4f69"
	latteError   = "#d20f39"

	mochaBrand   = "#cba6f7"
	mochaInfo    = "#89b4fa"
	mochaActive  = "#94e2d5"
	mochaPeach   = "#fab387"
	mochaSubtext = "#a6adc8"
	mochaOverlay = "#6c7086"
	mochaText    = "#cdd6f4"
	mochaError   = "#f38ba8"
)

// buildStyle returns a glamour StyleConfig that harmonizes with the rest of
// the TUI palette. We start from glamour's well-tested dark/light base and
// override only the elements that differ in our theme — headings, links,
// inline code, block quotes, bullet markers.
//
// Chroma (syntax-highlighted code blocks) is left at glamour's defaults: they
// already look good against catppuccin and reimplementing a chroma theme
// would be a lot of code for minimal gain.
func buildStyle(dark bool) ansi.StyleConfig {
	if dark {
		return withCatppuccinOverrides(styles.DarkStyleConfig, mochaPalette())
	}
	return withCatppuccinOverrides(styles.LightStyleConfig, lattePalette())
}

type palette struct {
	brand   string
	info    string
	peach   string
	active  string
	subtext string
	overlay string
	text    string
	error_  string
}

func mochaPalette() palette {
	return palette{
		brand: mochaBrand, info: mochaInfo, peach: mochaPeach,
		active: mochaActive, subtext: mochaSubtext, overlay: mochaOverlay,
		text: mochaText, error_: mochaError,
	}
}

func lattePalette() palette {
	return palette{
		brand: latteBrand, info: latteInfo, peach: lattePeach,
		active: latteActive, subtext: latteSubtext, overlay: latteOverlay,
		text: latteText, error_: latteError,
	}
}

func withCatppuccinOverrides(base ansi.StyleConfig, p palette) ansi.StyleConfig {
	s := base

	// No document margin — the caller (attach view) handles its own indentation.
	zero := uint(0)
	s.Document.Margin = &zero
	s.Document.BlockPrefix = ""
	s.Document.BlockSuffix = ""

	// Headings in brand color (mauve/purple), bold. Override the inverse
	// "block background" H1 from glamour's default — it doesn't fit the TUI.
	s.Heading = ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			BlockSuffix: "\n",
			Color:       ptr(p.brand),
			Bold:        ptr(true),
		},
	}
	s.H1 = ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Prefix: "# ",
			Color:  ptr(p.brand),
			Bold:   ptr(true),
		},
	}
	s.H2 = ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Prefix: "## "}}
	s.H3 = ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Prefix: "### "}}
	s.H4 = ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Prefix: "#### "}}
	s.H5 = ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Prefix: "##### "}}
	s.H6 = ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Prefix: "###### "}}

	// Inline code in peach.
	s.Code = ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Prefix: " ",
			Suffix: " ",
			Color:  ptr(p.peach),
		},
	}

	// Block quote: teal left bar.
	indent := uint(1)
	s.BlockQuote = ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{Color: ptr(p.active)},
		Indent:         &indent,
		IndentToken:    ptr("│ "),
	}

	// Lists: subtext-colored bullets.
	s.Item = ansi.StylePrimitive{BlockPrefix: "• ", Color: ptr(p.subtext)}
	s.Enumeration = ansi.StylePrimitive{BlockPrefix: ". ", Color: ptr(p.subtext)}

	// Links: info (blue) underlined for the URL, brand for the text.
	s.Link = ansi.StylePrimitive{Color: ptr(p.info), Underline: ptr(true)}
	s.LinkText = ansi.StylePrimitive{Color: ptr(p.brand), Bold: ptr(true)}

	// Horizontal rule in overlay color.
	s.HorizontalRule = ansi.StylePrimitive{
		Color:  ptr(p.overlay),
		Format: "\n─────\n",
	}

	// CodeBlock margin to 0 (caller handles indentation). Keep glamour's
	// chroma settings intact.
	s.CodeBlock.Margin = &zero

	return s
}

func ptr[T any](v T) *T { return &v }
