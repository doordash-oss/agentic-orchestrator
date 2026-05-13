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

package feature

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Tag values describing a feature's nature. Downstream phase prompt builders
// branch on these to promote matching utility skills from soft-advertisement
// to mandatory reads.
const (
	TagFrontend = "frontend"
	TagBackend  = "backend"
	TagCLI      = "cli"
	TagInfra    = "infra"
	TagDocs     = "docs"
)

// tagKeywords maps each tag to description keywords that imply it. Matching is
// case-insensitive and whole-word (to avoid "backend" matching "backendpool").
var tagKeywords = map[string][]string{
	TagFrontend: {
		"frontend", "front-end", "front end", "ui", "ux", "tui", "gui",
		"desktop app", "native app", "web app", "webapp", "webui",
		"component", "components", "dashboard", "screen", "screens",
		"visual", "design", "layout", "styling", "theme", "palette",
		"typography", "accessibility", "a11y",
		"react", "vue", "svelte", "solid", "angular", "preact",
		"tailwind", "shadcn", "radix", "chakra", "mui", "bootstrap",
		"css", "scss", "sass", "html",
		"bubbletea", "lipgloss", "ink",
		"figma", "mockup", "wireframe",
		"wails", "tauri", "electron",
	},
	TagBackend: {
		"backend", "back-end", "back end", "api", "server", "service",
		"microservice", "database", "db", "sql", "grpc", "rest", "graphql",
		"endpoint", "endpoints", "handler", "middleware",
	},
	TagCLI: {
		"cli", "command-line", "command line", "terminal", "shell",
		"subcommand", "flag", "argv",
	},
	TagInfra: {
		"infra", "infrastructure", "deploy", "deployment", "ci", "cd",
		"pipeline", "kubernetes", "k8s", "docker", "helm", "terraform",
		"ansible", "release", "goreleaser",
	},
	TagDocs: {
		"documentation", "docs", "readme", "guide", "tutorial",
	},
}

// frontendFileExts are extensions whose presence in a repo strongly implies
// the repo has a frontend surface.
var frontendFileExts = []string{
	".tsx", ".jsx", ".vue", ".svelte", ".css", ".scss", ".sass",
}

// Classify infers tags for a feature from its description, attached images,
// and repo contents. Tags are returned sorted and deduplicated.
//
// The classifier is deliberately generous: a single signal is enough to add
// a tag. False positives cost a small amount of context (a mandatory skill
// read); false negatives cost the user a bad UI. The former is cheap to fix,
// the latter takes 30 hours and $900 of compute.
func Classify(description string, images []string, repos []FeatureRepo) []string {
	seen := map[string]struct{}{}

	add := func(tag string) {
		seen[tag] = struct{}{}
	}

	desc := strings.ToLower(description)
	for tag, keywords := range tagKeywords {
		for _, kw := range keywords {
			if containsWord(desc, kw) {
				add(tag)
				break
			}
		}
	}

	// Images attached at feature creation are almost always UI references
	// (mockups, screenshots, design comps). Treat as a frontend signal.
	if len(images) > 0 {
		add(TagFrontend)
	}

	// Repo content signals. Walk two levels deep, stop early once a frontend
	// signal is found per repo.
	for _, r := range repos {
		if r.Path == "" {
			continue
		}
		if hasFrontendFiles(r.Path) {
			add(TagFrontend)
		}
	}

	tags := make([]string, 0, len(seen))
	for t := range seen {
		tags = append(tags, t)
	}
	sort.Strings(tags)
	return tags
}

// containsWord reports whether haystack contains needle as a whole word or
// whole phrase. haystack is assumed already lowercased; needle must be
// lowercase.
func containsWord(haystack, needle string) bool {
	idx := 0
	for {
		hit := strings.Index(haystack[idx:], needle)
		if hit < 0 {
			return false
		}
		start := idx + hit
		end := start + len(needle)
		if isWordBoundary(haystack, start-1) && isWordBoundary(haystack, end) {
			return true
		}
		idx = start + 1
	}
}

func isWordBoundary(s string, pos int) bool {
	if pos < 0 || pos >= len(s) {
		return true
	}
	c := s[pos]
	switch {
	case c >= 'a' && c <= 'z':
		return false
	case c >= '0' && c <= '9':
		return false
	case c == '_':
		return false
	}
	return true
}

// hasFrontendFiles reports whether repoPath contains frontend-typical files.
// It scans a shallow slice of the tree (top-level + a few well-known dirs)
// and stops at the first match. It does not walk node_modules.
func hasFrontendFiles(repoPath string) bool {
	// Fast-path: check for a package.json at the root.
	if _, err := os.Stat(filepath.Join(repoPath, "package.json")); err == nil {
		// package.json alone is not conclusive (backend JS uses it too), but
		// combined with any of the frontend extensions anywhere in the repo,
		// it's a strong signal. Scan a shallow slice.
		if scanForFrontendExts(repoPath, 3) {
			return true
		}
	}
	// Fallback: shallow scan for frontend extensions without package.json
	// (e.g. a standalone CSS/HTML project or a TUI-in-Go with lipgloss).
	return scanForFrontendExts(repoPath, 2)
}

func scanForFrontendExts(root string, maxDepth int) bool {
	var found bool
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		depth := strings.Count(rel, string(filepath.Separator))
		if d.IsDir() {
			name := d.Name()
			if depth == 0 && name == "" {
				return nil
			}
			if name == "node_modules" || name == ".git" || name == "vendor" ||
				name == "dist" || name == "build" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			if depth >= maxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		for _, want := range frontendFileExts {
			if ext == want {
				found = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	return found
}

// HasTag reports whether the feature carries the given tag.
func (f *Feature) HasTag(tag string) bool {
	for _, t := range f.Tags {
		if t == tag {
			return true
		}
	}
	return false
}
