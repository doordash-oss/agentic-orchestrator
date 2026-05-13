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

package guidelinedef

import (
	"bytes"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"

	guidelinesFS "github.com/doordash-oss/agentic-orchestrator/guidelines"
)

// GuidelineDef represents a single language guideline parsed from index.md.
type GuidelineDef struct {
	Name        string
	Description string
	Language    string // display language name (e.g. "Go", "Python")
	Body        string
}

var (
	parseOnce            sync.Once
	parsedGuidelines     map[string]GuidelineDef
	rawGuidelineContents map[string][]byte
	parseErr             error
)

// ParseEmbedded reads all index.md files from the embedded guidelines FS and
// returns a map of guideline name -> definition. The result is cached after the first call.
func ParseEmbedded() (map[string]GuidelineDef, error) {
	parseOnce.Do(func() {
		parsedGuidelines, rawGuidelineContents, parseErr = doParseEmbedded()
	})
	return parsedGuidelines, parseErr
}

func doParseEmbedded() (map[string]GuidelineDef, map[string][]byte, error) {
	defs := make(map[string]GuidelineDef)
	rawContents := make(map[string][]byte)

	entries, err := fs.ReadDir(guidelinesFS.FS, ".")
	if err != nil {
		return nil, nil, fmt.Errorf("reading embedded guidelines: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		dirName := entry.Name()
		guidelinePath := path.Join(dirName, "index.md")

		data, err := fs.ReadFile(guidelinesFS.FS, guidelinePath)
		if err != nil {
			// Skip directories without index.md
			continue
		}

		def, err := parseGuidelineFile(string(data), dirName)
		if err != nil {
			log.Printf("guidelinedef: skipping %s: %v", dirName, err)
			continue
		}

		defs[def.Name] = def
		rawContents[def.Name] = data
	}

	return defs, rawContents, nil
}

// parseGuidelineFile extracts the guideline definition from a markdown file with YAML
// frontmatter. The frontmatter must contain a description field. If name is
// empty, dirName is used as fallback. Language defaults to titlecase of name.
func parseGuidelineFile(content, dirName string) (GuidelineDef, error) {
	if !strings.HasPrefix(content, "---") {
		return GuidelineDef{}, fmt.Errorf("missing frontmatter")
	}

	rest := content[3:]
	frontmatter, body, found := strings.Cut(rest, "---")
	if !found {
		return GuidelineDef{}, fmt.Errorf("unterminated frontmatter")
	}
	body = strings.TrimSpace(body)

	fm := parseFrontmatterFields(frontmatter)

	name := fm["name"]
	if name == "" {
		name = dirName
	}

	description := fm["description"]
	if description == "" {
		return GuidelineDef{}, fmt.Errorf("missing description in frontmatter")
	}

	language := fm["language"]
	if language == "" {
		language = titleCase(name)
	}

	return GuidelineDef{
		Name:        name,
		Description: description,
		Language:    language,
		Body:        body,
	}, nil
}

// parseFrontmatterFields does a simple key: value parse of YAML-like frontmatter.
// Only handles single-line scalar values (no nested structures).
func parseFrontmatterFields(fm string) map[string]string {
	fields := make(map[string]string)
	for line := range strings.SplitSeq(fm, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if key != "" {
			fields[key] = val
		}
	}
	return fields
}

// titleCase returns s with the first letter uppercased.
func titleCase(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// ReconcileGuidelines writes all embedded guideline files to disk under guidelinesDir.
// For each language directory, it writes index.md and any sub-files (category
// index.md files and leaf topic files). Files whose content already matches on disk
// are skipped. Writes use atomic temp-file + rename.
func ReconcileGuidelines(guidelinesDir string) error {
	defs, err := ParseEmbedded()
	if err != nil {
		return fmt.Errorf("parsing embedded guidelines: %w", err)
	}

	if err := os.MkdirAll(guidelinesDir, 0o755); err != nil {
		return fmt.Errorf("creating guidelines dir %s: %w", guidelinesDir, err)
	}

	for name := range defs {
		// Walk the entire embedded directory for this language, writing all files.
		err := fs.WalkDir(guidelinesFS.FS, name, func(embeddedPath string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				dirPath := filepath.Join(guidelinesDir, embeddedPath)
				return os.MkdirAll(dirPath, 0o755)
			}

			data, readErr := fs.ReadFile(guidelinesFS.FS, embeddedPath)
			if readErr != nil {
				return fmt.Errorf("reading embedded %s: %w", embeddedPath, readErr)
			}

			return reconcileFile(filepath.Join(guidelinesDir, embeddedPath), data)
		})
		if err != nil {
			return fmt.Errorf("reconciling guideline %s: %w", name, err)
		}
	}

	return nil
}

// reconcileFile writes data to target atomically, skipping if the content matches.
func reconcileFile(target string, data []byte) error {
	// Skip if existing file already matches.
	existing, err := os.ReadFile(target)
	if err == nil && bytes.Equal(existing, data) {
		return nil
	}

	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating dir %s: %w", dir, err)
	}

	// Atomic write: temp file + rename.
	tmp, err := os.CreateTemp(dir, ".tmp.*")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("writing temp file %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("closing temp file %s: %w", tmpName, err)
	}

	if err := os.Rename(tmpName, target); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("renaming %s to %s: %w", tmpName, target, err)
	}
	return nil
}

// BuildPreamble returns a markdown section listing all available language guidelines
// as a discovery table. Unlike skills, all guidelines are always listed (no filter
// parameter). Returns "" if no guidelines exist.
func BuildPreamble(guidelinesDir string) string {
	defs, err := ParseEmbedded()
	if err != nil {
		log.Printf("guidelinedef: building preamble: %v", err)
		return ""
	}

	if len(defs) == 0 {
		return ""
	}

	// Collect names and sort alphabetically.
	names := make([]string, 0, len(defs))
	for name := range defs {
		names = append(names, name)
	}
	sort.Strings(names)

	var sb strings.Builder
	sb.WriteString("## Available Language Guidelines\n\n")
	sb.WriteString("IMPORTANT: Before writing or reviewing code, you MUST consult the language guidelines for every language relevant to the target repository. Guidelines are organized as a hierarchy — the top-level index.md is an index that links to category-specific files containing the actual rules.\n\n")
	sb.WriteString("| Language | Description | Path |\n")
	sb.WriteString("|----------|-------------|------|\n")

	for _, name := range names {
		def := defs[name]
		guidelinePath := filepath.Join(guidelinesDir, name, "index.md")
		fmt.Fprintf(&sb, "| %s | %s | %s |\n", def.Language, def.Description, guidelinePath)
	}

	sb.WriteString("\n### How to use guidelines\n\n")
	sb.WriteString("1. Read the index.md for the target language — it contains a **Category Index** table.\n")
	sb.WriteString("2. For each category in the index, check the **\"When to Read\"** column against your current task.\n")
	sb.WriteString("3. Read the `index.md` file for every matching category (e.g. `error-handling/index.md`, `concurrency/index.md`).\n")
	sb.WriteString("4. If an index.md references deeper topic files relevant to your work, read those too.\n\n")
	sb.WriteString("Do NOT stop at the top-level index.md — it is an index, not the guidelines themselves.\n\n")
	sb.WriteString("**Tip**: Read the `guideline-reader` skill from the Available Skills table for detailed navigation instructions.\n")

	return sb.String()
}

// BuildInlineContent reads all index.md files from disk under guidelinesDir
// and formats them as inline content blocks. Returns "" if guidelinesDir is empty
// or no files are found. Used by the in-implement reviewer which has no tool access.
func BuildInlineContent(guidelinesDir string) string {
	if guidelinesDir == "" {
		return ""
	}

	defs, err := ParseEmbedded()
	if err != nil {
		log.Printf("guidelinedef: building inline content: %v", err)
		return ""
	}

	if len(defs) == 0 {
		return ""
	}

	// Collect names and sort alphabetically.
	names := make([]string, 0, len(defs))
	for name := range defs {
		names = append(names, name)
	}
	sort.Strings(names)

	var sb strings.Builder
	sb.WriteString("## Language Guidelines\n")

	wrote := 0
	for _, name := range names {
		def := defs[name]

		// Read from disk — may be more current than embedded if user edited.
		guidelinePath := filepath.Join(guidelinesDir, name, "index.md")
		data, err := os.ReadFile(guidelinePath)
		if err != nil {
			// Fall back to embedded body.
			if def.Body != "" {
				fmt.Fprintf(&sb, "\n### %s\n%s\n", def.Language, def.Body)
				wrote++
			}
			continue
		}

		// Parse the on-disk file to get the body (strips frontmatter).
		diskDef, parseErr := parseGuidelineFile(string(data), name)
		if parseErr != nil {
			if def.Body != "" {
				fmt.Fprintf(&sb, "\n### %s\n%s\n", def.Language, def.Body)
				wrote++
			}
			continue
		}

		fmt.Fprintf(&sb, "\n### %s\n%s\n", diskDef.Language, diskDef.Body)
		wrote++
	}

	if wrote == 0 {
		return ""
	}

	return sb.String()
}
