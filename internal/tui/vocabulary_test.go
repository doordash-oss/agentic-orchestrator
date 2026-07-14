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
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestUserVisibleLiveSessionVocabulary(t *testing.T) {
	t.Parallel()

	retiredGoCopy := []string{
		"Attach to session",
		"Attach to agent session",
		"Detach from session",
		"attach to review",
		"[a] attach",
		"Attach unavailable",
		"active sessions to attach",
		"Finish/Stop/Detach",
		"or Detach?",
		" Detach ",
		" Detach —",
	}

	for _, path := range []string{"help.go", "help_data.go", "keys.go", "attach.go", "detail.go", "api_app.go"} {
		t.Run(path, func(t *testing.T) {
			assertGoStringLiteralsDoNotContain(t, path, retiredGoCopy)
		})
	}

	retiredDocCopy := []string{
		"Attach to watch",
		"Attach to live session",
		"attach to session",
		"attach to its live agent session",
		"Attach to session",
		"Attach to agent session",
		"Attach View",
		"attach view",
		"Detach from session",
		"Detach**",
		"**Detach",
		"detach to let",
		"detach without finishing",
		"While attached",
	}
	for _, path := range []string{
		filepath.Join("..", "..", "README.md"),
		filepath.Join("..", "..", "docs", "keybindings.md"),
		filepath.Join("..", "..", "skills", "chat", "user-guide", "getting-started.md"),
		filepath.Join("..", "..", "skills", "chat", "user-guide", "permissions.md"),
		filepath.Join("..", "..", "skills", "chat", "user-guide", "post-publish.md"),
		filepath.Join("..", "..", "skills", "chat", "user-guide", "tui-navigation.md"),
	} {
		t.Run(path, func(t *testing.T) {
			assertFileDoesNotContain(t, path, retiredDocCopy)
		})
	}
}

func TestLiveSessionVocabularyGuardrailAllowsInternalAttachTerms(t *testing.T) {
	t.Parallel()

	allowedTerms := map[string][]string{
		"attach.go": {
			"AttachModel",
			"session.AttachCh()",
		},
		filepath.Join("..", "..", "README.md"): {
			"attaching files",
		},
		filepath.Join("..", "ports", "session.go"): {
			"AttachCh()",
			"Attach(sessionID string)",
			"Detach()",
		},
	}

	for path, terms := range allowedTerms {
		t.Run(path, func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}
			for _, term := range terms {
				if !strings.Contains(string(data), term) {
					t.Errorf("%s missing allowed internal/file-attachment term %q", path, term)
				}
			}
		})
	}
}

func TestKeybindingReferenceUsesSharedWatchVocabulary(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "docs", "keybindings.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	text := string(data)

	for _, want := range []string{
		"| `a` | Watch active work; Answer, Approve, or Review when prompted |",
		"## Watch View",
		"| `ctrl+]/ctrl+x/esc` | Stop watching and return to dashboard |",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("%s missing generated keybinding row %q", path, want)
		}
	}
	for _, retired := range []string{"Attach to agent session", "Detach from session", "## Attach View"} {
		if strings.Contains(text, retired) {
			t.Errorf("%s contains retired generated keybinding copy %q", path, retired)
		}
	}
}

func assertGoStringLiteralsDoNotContain(t *testing.T, path string, retired []string) {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	ast.Inspect(file, func(node ast.Node) bool {
		lit, ok := node.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			t.Fatalf("unquoting string in %s: %v", path, err)
		}
		for _, retiredCopy := range retired {
			if strings.Contains(value, retiredCopy) {
				pos := fset.Position(lit.Pos())
				t.Errorf("%s contains retired user-facing live-session copy %q in %q", pos, retiredCopy, value)
			}
		}
		return true
	})
}

func assertFileDoesNotContain(t *testing.T, path string, retired []string) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	for _, retiredCopy := range retired {
		if strings.Contains(string(data), retiredCopy) {
			t.Errorf("%s contains retired user-facing live-session copy %q", path, retiredCopy)
		}
	}
}
