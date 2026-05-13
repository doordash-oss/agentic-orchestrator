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

// Command genkeybindings produces docs/keybindings.md from the shared
// HelpSection data in the tui package. Run via: go generate ./internal/tui/...
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/tui"
)

func main() {
	var b strings.Builder

	fmt.Fprintf(&b, "# Agentic Orchestrator Keybinding Reference\n\n")
	fmt.Fprintf(&b, "_Auto-generated on %s. Do not edit manually._\n\n", time.Now().Format("2006-01-02"))
	fmt.Fprintf(&b, "_Run `go generate ./internal/tui/...` to regenerate._\n\n")

	sections := []tui.HelpSection{
		tui.DashboardLeftSection,
		tui.DetailSection,
		tui.GeneralSection,
		tui.AttachSection,
		tui.WizardSection,
		tui.ConfirmationSection,
	}

	for _, s := range sections {
		fmt.Fprintf(&b, "## %s\n\n", s.Title)
		b.WriteString("| Key | Action |\n")
		b.WriteString("|-----|--------|\n")
		for _, kb := range s.Bindings {
			fmt.Fprintf(&b, "| `%s` | %s |\n", kb.Key, kb.Desc)
		}
		b.WriteString("\n")
	}

	// go generate runs from the package directory (internal/tui/),
	// so navigate up to the repo root for the docs/ output.
	docsDir := filepath.Join("..", "..", "docs")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating docs dir: %v\n", err)
		os.Exit(1)
	}
	outPath := filepath.Join(docsDir, "keybindings.md")
	if err := os.WriteFile(outPath, []byte(b.String()), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing keybindings.md: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Generated docs/keybindings.md")
}
