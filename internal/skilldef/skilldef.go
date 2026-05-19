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

package skilldef

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

	skillsFS "github.com/doordash-oss/agentic-orchestrator/skills"
)

// SkillDef represents a single skill definition parsed from an embedded SKILL.md file.
type SkillDef struct {
	Name        string
	Description string
	Topics      string // comma-separated keywords/topics for discovery
	License     string
	Body        string
}

var (
	parseOnce        sync.Once
	parsedSkills     map[string]SkillDef
	rawSkillContents map[string][]byte
	parseErr         error
)

// ParseEmbedded reads all SKILL.md files from the embedded skills FS and
// returns a map of skill name -> definition. The result is cached after the first call.
func ParseEmbedded() (map[string]SkillDef, error) {
	parseOnce.Do(func() {
		parsedSkills, rawSkillContents, parseErr = doParseEmbedded()
	})
	return parsedSkills, parseErr
}

func doParseEmbedded() (map[string]SkillDef, map[string][]byte, error) {
	defs := make(map[string]SkillDef)
	rawContents := make(map[string][]byte)

	entries, err := fs.ReadDir(skillsFS.FS, ".")
	if err != nil {
		return nil, nil, fmt.Errorf("reading embedded skills: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		dirName := entry.Name()
		skillPath := path.Join(dirName, "SKILL.md")

		data, err := fs.ReadFile(skillsFS.FS, skillPath)
		if err != nil {
			// Skip directories without SKILL.md
			continue
		}

		def, err := parseSkillFile(string(data), dirName)
		if err != nil {
			log.Printf("skilldef: skipping %s: %v", dirName, err)
			continue
		}

		defs[def.Name] = def
		rawContents[def.Name] = data
	}

	return defs, rawContents, nil
}

// parseSkillFile extracts the skill definition from a markdown file with YAML
// frontmatter. The frontmatter must contain a description field. If name is
// empty, dirName is used as fallback.
func parseSkillFile(content, dirName string) (SkillDef, error) {
	if !strings.HasPrefix(content, "---") {
		return SkillDef{}, fmt.Errorf("missing frontmatter")
	}

	rest := content[3:]
	frontmatter, body, found := strings.Cut(rest, "---")
	if !found {
		return SkillDef{}, fmt.Errorf("unterminated frontmatter")
	}
	body = strings.TrimSpace(body)

	fm := parseFrontmatterFields(frontmatter)

	name := fm["name"]
	if name == "" {
		name = dirName
	}

	description := fm["description"]
	if description == "" {
		return SkillDef{}, fmt.Errorf("missing description in frontmatter")
	}

	return SkillDef{
		Name:        name,
		Description: description,
		Topics:      fm["topics"],
		License:     fm["license"],
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

// ReadBody returns the frontmatter-stripped body of the named skill.
// Returns an error if the skill is not found in the embedded FS.
func ReadBody(name string) (string, error) {
	defs, err := ParseEmbedded()
	if err != nil {
		return "", fmt.Errorf("parsing embedded skills: %w", err)
	}
	def, ok := defs[name]
	if !ok {
		return "", fmt.Errorf("skill %q not found in embedded skills", name)
	}
	return def.Body, nil
}

// ReconcileSkills mirrors every embedded skill directory to disk under
// skillsDir. For each top-level skill directory it walks the full subtree
// and writes every file (SKILL.md plus any companion assets such as
// user-guide/*.md). Files whose content already matches on disk are
// skipped; writes use atomic temp-file + rename.
//
// A content hash of the embedded FS is persisted to a sibling stamp file
// after a successful reconcile. If the next call finds a matching stamp,
// the full walk is skipped — the common steady-state path where the binary
// hasn't been rebuilt. To force a re-reconcile (e.g. after manually editing
// or deleting an on-disk file), delete the stamp file.
func ReconcileSkills(skillsDir string) error {
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return fmt.Errorf("creating skills dir %s: %w", skillsDir, err)
	}

	embedHash := embeddedSkillsHash()
	stampPath := stampPathFor(skillsDir)
	if existing, err := os.ReadFile(stampPath); err == nil && bytes.Equal(existing, []byte(embedHash)) {
		return nil
	}

	entries, err := fs.ReadDir(skillsFS.FS, ".")
	if err != nil {
		return fmt.Errorf("reading embedded skills FS: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dirName := entry.Name()
		walkErr := fs.WalkDir(skillsFS.FS, dirName, func(embeddedPath string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return os.MkdirAll(filepath.Join(skillsDir, embeddedPath), 0o755)
			}
			data, readErr := fs.ReadFile(skillsFS.FS, embeddedPath)
			if readErr != nil {
				return fmt.Errorf("reading embedded %s: %w", embeddedPath, readErr)
			}
			return reconcileFile(filepath.Join(skillsDir, embeddedPath), data)
		})
		if walkErr != nil {
			return fmt.Errorf("reconciling skill %s: %w", dirName, walkErr)
		}
	}

	if err := writeStamp(stampPath, []byte(embedHash)); err != nil {
		// Stamp write failure is non-fatal: the next launch will simply
		// re-run the walk and find everything already in sync.
		log.Printf("skilldef: writing reconcile stamp: %v", err)
	}
	return nil
}

// NeedsReconcile reports whether ReconcileSkills would do real work for
// skillsDir — i.e. whether the on-disk stamp is missing or does not match
// the embedded FS hash. Callers can use this to surface a progress
// indicator only when the upcoming reconcile is going to be slow.
func NeedsReconcile(skillsDir string) bool {
	embed := []byte(embeddedSkillsHash())
	existing, err := os.ReadFile(stampPathFor(skillsDir))
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
	embeddedSkillsHashOnce  sync.Once
	embeddedSkillsHashValue string
)

// embeddedSkillsHash returns a deterministic hex digest of the embedded
// skills FS — every file path and its content contribute to the hash. The
// result is cached per process; the embedded FS is immutable.
func embeddedSkillsHash() string {
	embeddedSkillsHashOnce.Do(func() {
		h := sha256.New()
		var paths []string
		_ = fs.WalkDir(skillsFS.FS, ".", func(p string, d fs.DirEntry, err error) error {
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
			data, err := fs.ReadFile(skillsFS.FS, p)
			if err != nil {
				continue
			}
			fmt.Fprintf(h, "%s\x00%d\x00", p, len(data))
			h.Write(data)
			h.Write([]byte{0})
		}
		embeddedSkillsHashValue = hex.EncodeToString(h.Sum(nil))
	})
	return embeddedSkillsHashValue
}

// reconcileFile writes data to target atomically, skipping if the content matches.
func reconcileFile(target string, data []byte) error {
	existing, err := os.ReadFile(target)
	if err == nil && bytes.Equal(existing, data) {
		return nil
	}

	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating dir %s: %w", dir, err)
	}

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

// BuildPreamble returns a markdown section listing the given skills as a table.
// Only skills whose names appear in skillNames are included. If skillNames is
// empty the function returns "". The result is NOT cached.
func BuildPreamble(skillsDir string, skillNames []string) string {
	if len(skillNames) == 0 {
		return ""
	}

	defs, err := ParseEmbedded()
	if err != nil {
		log.Printf("skilldef: building preamble: %v", err)
		return ""
	}

	// Collect only the requested skills that actually exist.
	sort.Strings(skillNames)

	var sb strings.Builder
	sb.WriteString("## Available Skills\n\n")
	sb.WriteString("IMPORTANT: Before starting any task, scan the table below and read the SKILL.md file for every skill whose topics match your task. Skills contain methodology and quality guidelines that significantly improve your output.\n\n")
	sb.WriteString("| Skill | Description | Topics | Path |\n")
	sb.WriteString("|-------|-------------|--------|------|\n")

	wrote := 0
	for _, name := range skillNames {
		def, ok := defs[name]
		if !ok {
			continue
		}
		skillPath := filepath.Join(skillsDir, name, "SKILL.md")
		topics := def.Topics
		if topics == "" {
			topics = "—"
		}
		fmt.Fprintf(&sb, "| %s | %s | %s | %s |\n", def.Name, def.Description, topics, skillPath)
		wrote++
	}

	if wrote == 0 {
		return ""
	}

	sb.WriteString("\nTo use a skill, read the SKILL.md file at the listed path for detailed instructions.\n")

	return sb.String()
}
