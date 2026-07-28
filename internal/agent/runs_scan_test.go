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

package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestRunsModel_ScanDetectsVariableBasedJoins is a meta-test that pins the
// scanner's ability to catch variable-based phase segments — the exact false
// negative the iteration-1 reviewer flagged. We feed the scanner synthetic
// source containing representative join shapes and confirm it flags every
// feature-root violation while leaving genuinely safe paths (PID files,
// sub-paths already rooted in ActiveRunDir) alone.
func TestRunsModel_ScanDetectsVariableBasedJoins(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want bool // true = should be flagged
	}{
		{
			name: "literal phase at feature root",
			src:  `package p; import "path/filepath"; func f(stateDir, featureID string) string { return filepath.Join(stateDir, featureID, "research") }`,
			want: true,
		},
		{
			name: "variable phase at feature root (phase)",
			src:  `package p; import "path/filepath"; func f(stateDir, featureID, phase string) string { return filepath.Join(stateDir, featureID, phase) }`,
			want: true,
		},
		{
			name: "cycle prefix method call",
			src:  `package p; import "path/filepath"; type F struct{}; func (F) CyclePrefix() string { return "" }; func f(stateDir, featureID string, ff F) string { return filepath.Join(stateDir, featureID, ff.CyclePrefix()) }`,
			want: true,
		},
		{
			name: "cycle prefix variable",
			src:  `package p; import "path/filepath"; func f(stateDir, featureID, cyclePrefix string) string { return filepath.Join(stateDir, featureID, cyclePrefix) }`,
			want: true,
		},
		{
			name: "refPrefix variable with phase literal",
			src:  `package p; import "path/filepath"; func f(stateDir, featureID, refPrefix string) string { return filepath.Join(stateDir, featureID, refPrefix, "inquire", "qa-answers.md") }`,
			want: true,
		},
		{
			name: "Sprintf cycle-N",
			src:  `package p; import ("fmt"; "path/filepath"); func f(stateDir, featureID string, n int) string { return filepath.Join(stateDir, featureID, fmt.Sprintf("rebase-%d", n)) }`,
			want: true,
		},
		{
			name: "phase DirName call",
			src:  `package p; import "path/filepath"; type P int; func (P) DirName() string { return "" }; func f(stateDir, featureID string, p P) string { return filepath.Join(stateDir, featureID, p.DirName()) }`,
			want: true,
		},
		{
			// Pinned by the iteration-3 reviewer: the exact TUI shape
			// `filepath.Join(baseDir, fid, phase.DirName())` must trip the
			// scanner. If argLooksLikeFeatureID regresses to only match
			// "*ID" / ".ID", the four reviewer-cited TUI sites
			// (handlePhaseEvent plan/implement + research, handleSessionDone
			// save-output, logPhaseError call surface) could rot back to
			// feature-root joins while this test still stays green.
			name: "TUI fid shape with phase.DirName",
			src:  `package p; import "path/filepath"; type P int; func (P) DirName() string { return "" }; func f(baseDir, fid string, p P) string { return filepath.Join(baseDir, fid, p.DirName()) }`,
			want: true,
		},
		{
			name: "TUI fid shape with phase literal",
			src:  `package p; import "path/filepath"; func f(baseDir, fid string) string { return filepath.Join(baseDir, fid, "research") }`,
			want: true,
		},
		{
			name: "camelCase featureId with phase variable",
			src:  `package p; import "path/filepath"; func f(baseDir, featureId, phase string) string { return filepath.Join(baseDir, featureId, phase) }`,
			want: true,
		},
		{
			name: "safe: fid only (feature-root PID dir)",
			src:  `package p; import "path/filepath"; func f(baseDir, fid string) string { return filepath.Join(baseDir, fid, "feature.yaml") }`,
			want: false,
		},
		{
			name: "safe: feature.yaml at feature root",
			src:  `package p; import "path/filepath"; func f(stateDir, featureID string) string { return filepath.Join(stateDir, featureID, "feature.yaml") }`,
			want: false,
		},
		{
			name: "safe: session.pid at feature root",
			src:  `package p; import "path/filepath"; func f(stateDir, featureID string) string { return filepath.Join(stateDir, featureID, "session.pid") }`,
			want: false,
		},
		{
			name: "safe: session-<repo>.pid builder",
			src:  `package p; import ("fmt"; "path/filepath"); func f(stateDir, featureID, repo string) string { return filepath.Join(stateDir, featureID, fmt.Sprintf("session-%s.pid", repo)) }`,
			want: false,
		},
		{
			name: "safe: description-review.md at feature root",
			src:  `package p; import "path/filepath"; func f(stateDir, featureID string) string { return filepath.Join(stateDir, featureID, "description-review.md") }`,
			want: false,
		},
		{
			name: "safe: only stateDir + featureID (feature-root PID dir)",
			src:  `package p; import "path/filepath"; func f(stateDir, featureID string) string { return filepath.Join(stateDir, featureID) }`,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			node, err := parser.ParseFile(fset, "synthetic.go", tc.src, 0)
			if err != nil {
				t.Fatalf("parse synthetic src: %v", err)
			}
			var flagged bool
			ast.Inspect(node, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "filepath" || sel.Sel.Name != "Join" {
					return true
				}
				if len(call.Args) < 3 {
					return true
				}
				if !argLooksLikeStateDir(call.Args[0]) {
					return true
				}
				if !argLooksLikeFeatureID(call.Args[1]) {
					return true
				}
				for i := 2; i < len(call.Args); i++ {
					if !isFeatureRootSafe(call.Args[i]) {
						flagged = true
						return true
					}
				}
				return true
			})
			if flagged != tc.want {
				t.Errorf("case %q: flagged=%v, want=%v", tc.name, flagged, tc.want)
			}
		})
	}
}

// TestRunsModel_NoFeatureRootArtifactAccess scans every non-test .go file
// under internal/agent/, internal/orchestrator/, and internal/tui/ and
// fails if any filepath.Join callsite still joins
// (stateDir, featureID, <something>, …) directly at the feature root —
// the runs-first layout (Phase 1) requires every phase/cycle
// artifact to route through ActiveRunDir or a helper that nests inside
// runs/run-NNN/.
//
// internal/tui is scanned because the TUI writes artifacts and error
// logs directly (handleSessionDone's save-output path, logPhaseError
// helper, publish/rebase/review-comments error surfaces). The
// iteration-2 reviewer caught that the TUI still recreated flat
// <feature>/<phase>/ paths after agent/orchestrator had been fixed, so
// the scanner must cover it to prevent future regressions.
//
// The detector is intentionally STRICT: any join with a state-dir-shaped
// arg followed by a feature-id-shaped arg and one or more later args is
// a violation unless every later arg is on the feature-root allowlist.
// The allowlist covers only the files/dirs that the roadmap explicitly
// pins at the feature root (not a run): feature.yaml, events.jsonl,
// images, attachments, description-review.md, observe-summary.yaml, and
// the PID-file family (session.pid / session-<repo>.pid / session-<suffix>).
//
// Anything else — phase literals, `f.CyclePrefix()`, `cyclePrefix`,
// `cycleDirName`, `refPrefix`, `phase`, `phaseKey`, `fmt.Sprintf("rebase-%d", …)`,
// and so on — triggers a violation. Variable-based joins are caught
// precisely because we do NOT limit condition 3 to string literals.
func TestRunsModel_NoFeatureRootArtifactAccess(t *testing.T) {
	dirs := []string{
		filepath.Join("..", "agent"),
		filepath.Join("..", "orchestrator"),
		filepath.Join("..", "tui"),
	}

	// artifacts.go is the authoritative source of RUN-aware path helpers —
	// it is the only file allowed to reference "runs" directly when building
	// filepath.Join(stateDir, featureID, "runs", ...). Every other file
	// must route through the helpers (RunDir / ActiveRunDir / PhaseDir / …).
	var violations []string
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read dir %s: %v", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			// artifacts.go owns the run-path helpers; anything it joins is by
			// definition the helper body.
			if dir == filepath.Join("..", "agent") && name == "artifacts.go" {
				continue
			}
			path := filepath.Join(dir, name)
			violations = append(violations, scanFile(t, path)...)
		}
	}

	if len(violations) > 0 {
		sort.Strings(violations)
		t.Errorf("%d feature-root artifact join(s) found — must route through ActiveRunDir / run-aware helpers:\n  %s",
			len(violations),
			strings.Join(violations, "\n  "))
	}
}

// scanFile walks a .go file's AST and collects feature-root filepath.Join
// violations as "<file>:<line>:<col>" strings.
func scanFile(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var violations []string
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "filepath" || sel.Sel.Name != "Join" {
			return true
		}
		// Need at least (stateDir, featureID, something) to be suspicious.
		if len(call.Args) < 3 {
			return true
		}
		// Condition 1: first arg is a state-dir-shaped identifier / selector.
		if !argLooksLikeStateDir(call.Args[0]) {
			return true
		}
		// Condition 2: second arg is a feature-id-shaped identifier / selector.
		if !argLooksLikeFeatureID(call.Args[1]) {
			return true
		}
		// Condition 3: every later arg MUST be a feature-root-safe expression.
		// A single non-safe arg flags the whole call. This catches literal
		// phase names, phase-/cycle- prefixes, AND
		// variable-based joins (cyclePrefix, refPrefix, cycleDirName,
		// phaseKey, phase.DirName(), f.CyclePrefix(), …) — the whole
		// family the reviewer flagged as false negatives in iteration 1.
		safe := true
		for i := 2; i < len(call.Args); i++ {
			if !isFeatureRootSafe(call.Args[i]) {
				safe = false
				break
			}
		}
		if !safe {
			violations = append(violations, fset.Position(call.Pos()).String())
		}
		return true
	})
	return violations
}

// argLooksLikeStateDir returns true if the AST argument's tail identifier
// matches a state-dir naming convention we expect: anything ending in
// "StateDir" / "stateDir" / "BaseDir" / "baseDir".
func argLooksLikeStateDir(e ast.Expr) bool {
	name := tailIdent(e)
	if name == "" {
		return false
	}
	lower := strings.ToLower(name)
	return lower == "statedir" || lower == "basedir" ||
		strings.HasSuffix(lower, "statedir") || strings.HasSuffix(lower, "basedir")
}

// argLooksLikeFeatureID returns true if the AST argument's tail identifier
// matches a feature-id naming convention. The match is case-insensitive so
// common lowercase forms used across the codebase (fid, featureId,
// featureID, f.ID, cfg.Feature.ID, …) are all flagged. Literal feature-id
// arguments are rejected to avoid false positives on general helper calls.
//
// Recognized shapes (lowercase comparison):
//   - Selectors whose tail is "id" (e.g. f.ID, cfg.Feature.ID).
//   - Identifiers equal to "fid" (the TUI's canonical loop var from
//     eventFID). The iteration-3 reviewer flagged that missing this form
//     let regressions in handlePhaseEvent / handleSessionDone /
//     logPhaseError slip through this guard.
//   - Identifiers ending in "id" but longer than 2 chars (featureid,
//     featureId, ffid, targetFid, …). The length check keeps us from
//     flagging unrelated 2-char names; there is no real 3-char "…id" token
//     on the codebase today that isn't a feature-ID alias.
func argLooksLikeFeatureID(e ast.Expr) bool {
	if sel, ok := e.(*ast.SelectorExpr); ok {
		if strings.EqualFold(sel.Sel.Name, "id") {
			return true
		}
	}
	name := tailIdent(e)
	if name == "" {
		return false
	}
	lower := strings.ToLower(name)
	if lower == "fid" || lower == "featureid" {
		return true
	}
	return strings.HasSuffix(lower, "id") && len(lower) > 2
}

// tailIdent returns the last identifier name in a selector chain or the
// identifier itself for a plain *ast.Ident; returns "" for anything else.
func tailIdent(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return v.Sel.Name
	default:
		return ""
	}
}

// isFeatureRootSafe returns true when the expression can legitimately live
// at the feature root (NOT inside runs/run-NNN/). The allowlist is the
// set the roadmap fixed at feature scope: feature.yaml, events.jsonl,
// images/, attachments/, description-review.md, observe-summary.yaml, and
// the PID-file family.
//
// Accepts:
//   - String literals whose value is in the allowlist or starts with
//     "session" (covers session.pid, session-<repo>.pid, session-<suffix>).
//   - fmt.Sprintf("session-...", ...) — the PID-file builder pattern.
//
// Anything else — variable identifiers (phase, phaseKey, refPrefix,
// cyclePrefix, cycleDirName, ...), selector calls (f.CyclePrefix(),
// phase.DirName()), fmt.Sprintf("rebase-%d", n), etc. — is considered
// unsafe and flags a violation when paired with (stateDir, featureID).
func isFeatureRootSafe(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return false
		}
		raw := stripQuotes(v.Value)
		return isAllowedFeatureRootName(raw)
	case *ast.CallExpr:
		// Only fmt.Sprintf("session-..."/"session.pid", ...) is allowlisted.
		sel, ok := v.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "fmt" || sel.Sel.Name != "Sprintf" {
			return false
		}
		if len(v.Args) < 1 {
			return false
		}
		lit, ok := v.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return false
		}
		raw := stripQuotes(lit.Value)
		return strings.HasPrefix(raw, "session-") || raw == "session.pid"
	default:
		return false
	}
}

func stripQuotes(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '`') {
		return s[1 : len(s)-1]
	}
	return s
}

// isAllowedFeatureRootName lists filenames/dirs that the roadmap pins at
// the feature root (outside any run).
func isAllowedFeatureRootName(s string) bool {
	switch s {
	case "feature.yaml",
		"events.jsonl",
		"images",
		"attachments",
		"description-review.md",
		"observe-summary.yaml",
		"session.pid":
		return true
	}
	// PID-file family: session-<anything>[.pid] — allow any literal starting
	// with "session-" since they are feature-scoped session markers.
	return strings.HasPrefix(s, "session-")
}
