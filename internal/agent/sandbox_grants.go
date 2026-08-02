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

// Sandbox self-expansion: the verification sandbox mitigates host mutation
// by planner-authored commands, not exfiltration. When the OS denies a write
// on a non-protected path (toolchain caches, app-support dirs, temp space),
// the executor grants the minimal containing root, retries once, and
// persists the grant next to the testing contract so later runs — including
// baselines — start with it. Every grant is evidence-based (an actual OS
// denial), minimal, audited in run evidence, and never touches the
// protected-path denylist below.

package agent

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
)

// maxSandboxGrantsPerItem bounds the grant-and-retry loop for one item.
const maxSandboxGrantsPerItem = 3

type sandboxGrant struct {
	Root       string `yaml:"root"`
	ItemID     string `yaml:"item_id"`
	DeniedPath string `yaml:"denied_path"`
}

// sandboxUnsandboxedDisposition records a ladder-proven fact: the item's
// command fails under the verification sandbox and passes without it, so
// later runs execute it unsandboxed directly.
type sandboxUnsandboxedDisposition struct {
	ItemID string `yaml:"item_id"`
	Reason string `yaml:"reason"`
}

type sandboxGrantsFile struct {
	Version     int                             `yaml:"version"`
	Grants      []sandboxGrant                  `yaml:"grants"`
	Unsandboxed []sandboxUnsandboxedDisposition `yaml:"unsandboxed,omitempty"`
}

// loadUnsandboxedDispositions returns the item IDs recorded as requiring
// unsandboxed execution.
func loadUnsandboxedDispositions(contractPath string) map[string]bool {
	file, ok := readSandboxGrantsFile(contractPath)
	if !ok {
		return nil
	}
	items := make(map[string]bool, len(file.Unsandboxed))
	for _, disposition := range file.Unsandboxed {
		if id := strings.TrimSpace(disposition.ItemID); id != "" {
			items[id] = true
		}
	}
	return items
}

// recordUnsandboxedDisposition persists an unsandboxed-execution disposition,
// deduplicated by item ID.
func recordUnsandboxedDisposition(contractPath string, disposition sandboxUnsandboxedDisposition) error {
	path := sandboxGrantsPath(contractPath)
	if path == "" {
		return nil
	}
	file, _ := readSandboxGrantsFile(contractPath)
	disposition.ItemID = strings.TrimSpace(disposition.ItemID)
	for _, existing := range file.Unsandboxed {
		if strings.TrimSpace(existing.ItemID) == disposition.ItemID {
			return nil
		}
	}
	file.Unsandboxed = append(file.Unsandboxed, disposition)
	return writeSandboxGrantsFile(path, file)
}

func readSandboxGrantsFile(contractPath string) (sandboxGrantsFile, bool) {
	var file sandboxGrantsFile
	path := sandboxGrantsPath(contractPath)
	if path == "" {
		return file, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return file, false
	}
	if yaml.Unmarshal(data, &file) != nil {
		return sandboxGrantsFile{}, false
	}
	return file, true
}

func writeSandboxGrantsFile(path string, file sandboxGrantsFile) error {
	file.Version = 1
	data, err := yaml.Marshal(file)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// protectedHomeComponents are top-level home entries that hold credentials,
// keys, or harness state. A denied write there is the sandbox doing its job;
// it gates to the user instead of self-expanding. The orchestrator state
// parents (permission.StateParentComponents) are protected separately with a
// worktrees carve-out.
var protectedHomeComponents = map[string]bool{
	".aws":             true,
	".azure":           true,
	".boto":            true,
	".claude":          true,
	".claude.json":     true,
	".codex":           true,
	".docker":          true,
	".gemini":          true,
	".git-credentials": true,
	".gitconfig":       true,
	".gnupg":           true,
	".kube":            true,
	".netrc":           true,
	".npmrc":           true,
	".oci":             true,
	".password-store":  true,
	".ssh":             true,
}

// protectedConfigChildren are ~/.config subtrees that hold credentials.
var protectedConfigChildren = map[string]bool{
	"gcloud": true,
	"gh":     true,
	"git":    true,
	"op":     true,
}

// grantableLibraryChildren are the macOS ~/Library subtrees apps write at
// runtime; grants are per-app (one more component below these).
var grantableLibraryChildren = map[string]bool{
	"Application Support":     true,
	"Caches":                  true,
	"HTTPStorages":            true,
	"Logs":                    true,
	"Preferences":             true,
	"Saved Application State": true,
	"WebKit":                  true,
}

var grantableTempRoots = []string{"/private/var/tmp", "/var/tmp", "/private/tmp", "/tmp"}

// isStateParentHomeComponent reports whether a first-level home component is
// an orchestrator state parent, using the guardrail's shared definition.
func isStateParentHomeComponent(name string) bool {
	for _, s := range permission.StateParentComponents {
		if name == s {
			return true
		}
	}
	return false
}

// sandboxGrantRootForDeniedPath derives the minimal directory to open for a
// write the OS denied, or reports the path non-grantable. Grantable roots:
// non-protected dot-directories under home (per-app for ~/.config and
// ~/Library), and first-level entries under shared temp roots. The function
// is idempotent on its own outputs, which lets persisted grants be
// re-validated on load.
func sandboxGrantRootForDeniedPath(home, denied string) (string, bool) {
	denied = filepath.Clean(strings.TrimSpace(denied))
	if !filepath.IsAbs(denied) {
		return "", false
	}
	home = filepath.Clean(strings.TrimSpace(home))
	if home != "" && home != string(filepath.Separator) && strings.HasPrefix(denied, home+string(filepath.Separator)) {
		parts := strings.Split(strings.TrimPrefix(denied, home+string(filepath.Separator)), string(filepath.Separator))
		first := parts[0]
		switch {
		case isStateParentHomeComponent(first):
			// Feature checkouts live under <state parent>/worktrees/<feature>;
			// grants there are per-feature. Everything else under a state
			// parent (features/, config, tokens) is protected.
			if len(parts) >= 3 && parts[1] == "worktrees" {
				return filepath.Join(home, first, "worktrees", parts[2]), true
			}
			return "", false
		case protectedHomeComponents[first]:
			return "", false
		case first == ".config":
			if len(parts) >= 2 && !protectedConfigChildren[parts[1]] {
				return filepath.Join(home, ".config", parts[1]), true
			}
			return "", false
		case first == "Library":
			if len(parts) >= 3 && grantableLibraryChildren[parts[1]] {
				return filepath.Join(home, "Library", parts[1], parts[2]), true
			}
			return "", false
		case strings.HasPrefix(first, "."):
			return filepath.Join(home, first), true
		}
		return "", false
	}
	for _, tempRoot := range grantableTempRoots {
		if strings.HasPrefix(denied, tempRoot+string(filepath.Separator)) {
			parts := strings.Split(strings.TrimPrefix(denied, tempRoot+string(filepath.Separator)), string(filepath.Separator))
			if parts[0] != "" {
				return filepath.Join(tempRoot, parts[0]), true
			}
		}
	}
	return "", false
}

// toolchainCacheRoots lists well-known package-manager and build caches that
// are safe to keep writable up front, so the common cases never hit a denial
// round at all. Callers existence-filter the list.
func toolchainCacheRoots(home string) []string {
	if strings.TrimSpace(home) == "" {
		return nil
	}
	relative := []string{
		".npm", ".yarn", ".pnpm-store",
		filepath.Join(".bun", "install", "cache"),
		filepath.Join(".cargo", "registry"), filepath.Join(".cargo", "git"),
		".gradle", filepath.Join(".m2", "repository"),
		filepath.Join(".nuget", "packages"), filepath.Join(".ivy2", "cache"),
		".pub-cache",
	}
	roots := make([]string, 0, len(relative)+1)
	for _, rel := range relative {
		roots = append(roots, filepath.Join(home, rel))
	}
	roots = append(roots, filepath.Join("/private/var/tmp", "_bazel_"+filepath.Base(home)))
	return roots
}

// sandboxEscalationSafe reports whether a command whose sandboxed run was
// denied on deniedPath may be retried unsandboxed. Escalation is unsafe
// exactly when the sandbox is the only wall: paths under the user's home
// (grantable ones were already granted upstream) and paths whose nearest
// existing ancestor the user can write. Root-owned system paths stay
// protected by unix permissions either way, and a path-less denial is a
// process-level restriction (GUI, nested sandbox) with nothing to protect.
func sandboxEscalationSafe(home, deniedPath string) bool {
	deniedPath = strings.TrimSpace(deniedPath)
	if deniedPath == "" {
		return true
	}
	deniedPath = filepath.Clean(deniedPath)
	home = filepath.Clean(strings.TrimSpace(home))
	if home != "" && home != string(filepath.Separator) &&
		(deniedPath == home || strings.HasPrefix(deniedPath, home+string(filepath.Separator))) {
		return false
	}
	dir := deniedPath
	for {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return !pathWritableByUser(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return true
		}
		dir = parent
	}
}

func sandboxGrantsPath(contractPath string) string {
	contractPath = strings.TrimSpace(contractPath)
	if contractPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(contractPath), "sandbox-grants.yaml")
}

// loadSandboxGrantRoots returns the persisted grant roots that still pass
// the grantability rules, so a hand-edited grants file cannot open a
// protected path.
func loadSandboxGrantRoots(contractPath string) []string {
	path := sandboxGrantsPath(contractPath)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var file sandboxGrantsFile
	if yaml.Unmarshal(data, &file) != nil {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	seen := make(map[string]bool, len(file.Grants))
	var roots []string
	for _, grant := range file.Grants {
		root := filepath.Clean(strings.TrimSpace(grant.Root))
		if seen[root] {
			continue
		}
		if derived, ok := sandboxGrantRootForDeniedPath(home, root); !ok || derived != root {
			continue
		}
		seen[root] = true
		roots = append(roots, root)
	}
	return roots
}

// appendSandboxGrant records a grant next to the testing contract. Grants
// deduplicate by root. An empty contract path (no durable contract) keeps
// the grant in-memory only.
func appendSandboxGrant(contractPath string, grant sandboxGrant) error {
	path := sandboxGrantsPath(contractPath)
	if path == "" {
		return nil
	}
	file, _ := readSandboxGrantsFile(contractPath)
	grant.Root = filepath.Clean(strings.TrimSpace(grant.Root))
	for _, existing := range file.Grants {
		if filepath.Clean(existing.Root) == grant.Root {
			return nil
		}
	}
	file.Grants = append(file.Grants, grant)
	return writeSandboxGrantsFile(path, file)
}
