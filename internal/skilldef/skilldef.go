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
func ReconcileSkills(skillsDir string) error {
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return fmt.Errorf("creating skills dir %s: %w", skillsDir, err)
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

	return nil
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

// BuildRequiredInstruction returns a strong-verb instruction block naming the
// SKILL.md files the agent MUST read before starting the task. Unlike
// BuildPreamble (soft advertisement), this block tells the agent which
// specific files are mandatory — parity with the imperative used by
// phase-primary RoleSpec system prompts.
//
// Returns "" when skillsDir is empty or skillNames is empty / contains no
// known skills.
func BuildRequiredInstruction(skillsDir string, skillNames []string) string {
	if skillsDir == "" || len(skillNames) == 0 {
		return ""
	}

	defs, err := ParseEmbedded()
	if err != nil {
		log.Printf("skilldef: building required instruction: %v", err)
		return ""
	}

	sorted := append([]string(nil), skillNames...)
	sort.Strings(sorted)

	var sb strings.Builder
	sb.WriteString("## Required Skills For This Feature\n\n")
	sb.WriteString("IMPORTANT: Based on this feature's nature, the following skill(s) are MANDATORY reading. Before producing any output, you MUST read each SKILL.md file listed below in full and follow its methodology. These are not optional suggestions — they encode quality requirements this feature will be evaluated against.\n\n")

	wrote := 0
	for _, name := range sorted {
		def, ok := defs[name]
		if !ok {
			continue
		}
		skillPath := filepath.Join(skillsDir, name, "SKILL.md")
		fmt.Fprintf(&sb, "- **%s** — %s\n  Read: %s\n", def.Name, def.Description, skillPath)
		wrote++
	}

	if wrote == 0 {
		return ""
	}

	return sb.String()
}
