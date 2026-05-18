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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// skipDirs contains directory names that should be excluded from recursive scanning.
var skipDirs = map[string]bool{
	"vendor":       true,
	"node_modules": true,
	".git":         true,
	"dist":         true,
	"build":        true,
	"__pycache__":  true,
	".cache":       true,
	".venv":        true,
}

// extractDependencies reads dependency manifests and returns dependency names
// and the module path used by the codebase index for import resolution.
func extractDependencies(repoPath string) (deps []string, modulePath string) {
	if data, err := os.ReadFile(filepath.Join(repoPath, "go.mod")); err == nil {
		return parseGoMod(string(data))
	}

	if data, err := os.ReadFile(filepath.Join(repoPath, "package.json")); err == nil {
		return parsePackageJSON(data)
	}

	if data, err := os.ReadFile(filepath.Join(repoPath, "pyproject.toml")); err == nil {
		deps = parsePyprojectTOML(string(data))
		if len(deps) > 0 {
			return deps, ""
		}
	}
	if data, err := os.ReadFile(filepath.Join(repoPath, "requirements.txt")); err == nil {
		return parseRequirementsTxt(string(data)), ""
	}

	return nil, ""
}

func parseGoMod(content string) (deps []string, modulePath string) {
	inRequire := false
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "module ") {
			modulePath = strings.TrimPrefix(line, "module ")
			continue
		}

		if line == "require (" {
			inRequire = true
			continue
		}
		if inRequire && line == ")" {
			inRequire = false
			continue
		}

		if inRequire {
			fields := strings.Fields(line)
			if len(fields) >= 1 {
				deps = appendDepParts(deps, fields[0])
			}
		} else if strings.HasPrefix(line, "require ") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				deps = appendDepParts(deps, fields[1])
			}
		}
	}
	return deps, modulePath
}

func appendDepParts(deps []string, depPath string) []string {
	parts := strings.Split(depPath, "/")
	if len(parts) >= 2 {
		twoSeg := parts[len(parts)-2] + "/" + parts[len(parts)-1]
		for range 3 {
			deps = append(deps, twoSeg)
		}
	}
	lastSeg := parts[len(parts)-1]
	for range 3 {
		deps = append(deps, lastSeg)
	}
	return deps
}

func parsePackageJSON(data []byte) (deps []string, modulePath string) {
	var pkg struct {
		Name            string                 `json:"name"`
		Dependencies    map[string]interface{} `json:"dependencies"`
		DevDependencies map[string]interface{} `json:"devDependencies"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil, ""
	}
	modulePath = pkg.Name

	for name := range pkg.Dependencies {
		clean := stripScopePrefix(name)
		for range 3 {
			deps = append(deps, clean)
		}
	}
	for name := range pkg.DevDependencies {
		clean := stripScopePrefix(name)
		for range 3 {
			deps = append(deps, clean)
		}
	}
	return deps, modulePath
}

func stripScopePrefix(name string) string {
	if strings.HasPrefix(name, "@") {
		if idx := strings.Index(name, "/"); idx >= 0 {
			return name[idx+1:]
		}
	}
	return name
}

func parsePyprojectTOML(content string) []string {
	var deps []string
	inDeps := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "dependencies") && strings.Contains(trimmed, "[") {
			inDeps = true
			if idx := strings.Index(trimmed, "["); idx >= 0 {
				rest := trimmed[idx:]
				deps = append(deps, extractPyDepsFromBrackets(rest)...)
			}
			if strings.Contains(trimmed, "]") {
				inDeps = false
			}
			continue
		}
		if inDeps {
			if strings.Contains(trimmed, "]") {
				deps = append(deps, extractPyDepsFromBrackets(trimmed)...)
				inDeps = false
				continue
			}
			deps = append(deps, extractPyDepsFromBrackets(trimmed)...)
		}
	}
	return deps
}

func extractPyDepsFromBrackets(line string) []string {
	var deps []string
	line = strings.NewReplacer("[", "", "]", "").Replace(line)
	for _, part := range strings.Split(line, ",") {
		part = strings.TrimSpace(part)
		part = strings.Trim(part, "\"'")
		if part == "" {
			continue
		}
		for _, sep := range []string{">=", "<=", "==", "!=", "~=", ">", "<"} {
			if idx := strings.Index(part, sep); idx >= 0 {
				part = part[:idx]
			}
		}
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		for range 3 {
			deps = append(deps, part)
		}
	}
	return deps
}

func parseRequirementsTxt(content string) []string {
	var deps []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		name := line
		for _, sep := range []string{">=", "<=", "==", "!=", "~=", ">", "<", "["} {
			if idx := strings.Index(name, sep); idx >= 0 {
				name = name[:idx]
			}
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		for range 3 {
			deps = append(deps, name)
		}
	}
	return deps
}
