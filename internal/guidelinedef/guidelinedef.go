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
	"crypto/sha256"
	"encoding/hex"
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
//
// A content hash of the embedded FS is persisted to a sibling stamp file
// after a successful reconcile. If the next call finds a matching stamp,
// the full walk (~291 files) is skipped — the common steady-state path
// where the binary hasn't been rebuilt. To force a re-reconcile (e.g.
// after manually editing or deleting an on-disk file), delete the stamp.
func ReconcileGuidelines(guidelinesDir string) error {
	defs, err := ParseEmbedded()
	if err != nil {
		return fmt.Errorf("parsing embedded guidelines: %w", err)
	}

	if err := os.MkdirAll(guidelinesDir, 0o755); err != nil {
		return fmt.Errorf("creating guidelines dir %s: %w", guidelinesDir, err)
	}

	embedHash := embeddedGuidelinesHash()
	stampPath := stampPathFor(guidelinesDir)
	if existing, err := os.ReadFile(stampPath); err == nil && bytes.Equal(existing, []byte(embedHash)) {
		return nil
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

	if err := writeStamp(stampPath, []byte(embedHash)); err != nil {
		// Stamp write failure is non-fatal: the next launch will simply
		// re-run the walk and find everything already in sync.
		log.Printf("guidelinedef: writing reconcile stamp: %v", err)
	}
	return nil
}

// NeedsReconcile reports whether ReconcileGuidelines would do real work
// for guidelinesDir — i.e. whether the on-disk stamp is missing or does
// not match the embedded FS hash. Callers can use this to surface a
// progress indicator only when the upcoming reconcile is going to be slow.
func NeedsReconcile(guidelinesDir string) bool {
	embed := []byte(embeddedGuidelinesHash())
	existing, err := os.ReadFile(stampPathFor(guidelinesDir))
	return err != nil || !bytes.Equal(existing, embed)
}

// stampPathFor returns the sibling stamp path for a target directory.
// Storing the stamp outside the target dir keeps the directory listing
// (and tests that count entries) free of bookkeeping artifacts.
func stampPathFor(targetDir string) string {
	return filepath.Join(filepath.Dir(targetDir), "."+filepath.Base(targetDir)+"-stamp")
}

// writeStamp writes data to path atomically via temp file + rename.
func writeStamp(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating stamp dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".stamp.*")
	if err != nil {
		return fmt.Errorf("creating stamp temp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("writing stamp temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("closing stamp temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("renaming stamp: %w", err)
	}
	return nil
}

var (
	embeddedGuidelinesHashOnce  sync.Once
	embeddedGuidelinesHashValue string
)

// embeddedGuidelinesHash returns a deterministic hex digest of the embedded
// guidelines FS — every file path and its content contribute to the hash.
// The result is cached per process; the embedded FS is immutable.
func embeddedGuidelinesHash() string {
	embeddedGuidelinesHashOnce.Do(func() {
		h := sha256.New()
		var paths []string
		_ = fs.WalkDir(guidelinesFS.FS, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			paths = append(paths, p)
			return nil
		})
		sort.Strings(paths)
		for _, p := range paths {
			data, err := fs.ReadFile(guidelinesFS.FS, p)
			if err != nil {
				continue
			}
			fmt.Fprintf(h, "%s\x00%d\x00", p, len(data))
			h.Write(data)
			h.Write([]byte{0})
		}
		embeddedGuidelinesHashValue = hex.EncodeToString(h.Sum(nil))
	})
	return embeddedGuidelinesHashValue
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
