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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const readOnlyRepoBaselineFile = ".repo-readonly-baseline.json"

type readOnlyRepoBaseline struct {
	Version int                    `json:"version"`
	Repos   []readOnlyRepoSnapshot `json:"repos"`
}

type readOnlyRepoSnapshot struct {
	Name         string                  `json:"name"`
	WorktreePath string                  `json:"worktree_path"`
	Status       string                  `json:"status"`
	Diff         string                  `json:"diff"`
	Untracked    []readOnlyUntrackedFile `json:"untracked,omitempty"`
}

type readOnlyUntrackedFile struct {
	Path       string `json:"path"`
	Kind       string `json:"kind,omitempty"`
	Mode       uint32 `json:"mode"`
	SHA256     string `json:"sha256,omitempty"`
	Content    []byte `json:"content,omitempty"`
	LinkTarget string `json:"link_target,omitempty"`
}

const (
	readOnlyUntrackedRegular = "regular"
	readOnlyUntrackedSymlink = "symlink"
	readOnlyUntrackedOther   = "other"
)

// RecordReadOnlyRepoBaseline snapshots managed repo worktrees before a
// read-only role runs so mutations can be detected and restored later.
func RecordReadOnlyRepoBaseline(ctx context.Context, runner ports.CommandRunner, f *feature.Feature, phaseDir string, repoNames ...string) error {
	if f == nil || phaseDir == "" {
		return nil
	}
	snapshots, err := captureReadOnlyRepoSnapshots(ctx, runner, f, repoNames...)
	if err != nil {
		return err
	}
	if len(snapshots) == 0 {
		return nil
	}
	if err := os.MkdirAll(phaseDir, 0o755); err != nil {
		return fmt.Errorf("create read-only phase dir: %w", err)
	}
	data, err := json.MarshalIndent(readOnlyRepoBaseline{
		Version: 1,
		Repos:   snapshots,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal read-only repo baseline: %w", err)
	}
	if err := os.WriteFile(filepath.Join(phaseDir, readOnlyRepoBaselineFile), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write read-only repo baseline: %w", err)
	}
	return nil
}

// EnforceReadOnlyRepoMutations detects changes made by a read-only role,
// saves the attempted diff, restores the baseline, and returns violations.
func EnforceReadOnlyRepoMutations(ctx context.Context, runner ports.CommandRunner, f *feature.Feature, phase feature.Phase, phaseDir string, repoNames ...string) ([]ProtocolViolation, error) {
	if f == nil || phaseDir == "" || phase == feature.PhaseImplement {
		return nil, nil
	}
	current, err := captureReadOnlyRepoSnapshots(ctx, runner, f, repoNames...)
	if err != nil {
		return nil, err
	}
	if len(current) == 0 {
		return nil, nil
	}

	baseline, err := readReadOnlyRepoBaseline(phaseDir)
	if err != nil {
		return nil, err
	}
	baselineByRepo := make(map[string]readOnlyRepoSnapshot, len(baseline.Repos))
	for _, snap := range baseline.Repos {
		baselineByRepo[readOnlyRepoKey(snap.Name, snap.WorktreePath)] = snap
	}

	var violations []ProtocolViolation
	for _, snap := range current {
		base := baselineByRepo[readOnlyRepoKey(snap.Name, snap.WorktreePath)]
		if readOnlyRepoSnapshotsEqual(base, snap) {
			continue
		}
		patchPath, err := writeReadOnlyRepoMutationPatch(phaseDir, phase, base, snap)
		if err != nil {
			return nil, err
		}
		if err := restoreReadOnlyRepoBaseline(ctx, runner, snap, base); err != nil {
			return nil, err
		}
		violations = append(violations, ProtocolViolation{
			Artifact: fmt.Sprintf("target repo %s", snap.Name),
			Reason:   fmt.Sprintf("read-only %s phase modified target repo %s; saved attempted diff to %s and restored the managed worktree", phase.String(), snap.Name, patchPath),
		})
	}
	return violations, nil
}

func captureReadOnlyRepoSnapshots(ctx context.Context, runner ports.CommandRunner, f *feature.Feature, repoNames ...string) ([]readOnlyRepoSnapshot, error) {
	if f == nil {
		return nil, nil
	}
	filter := make(map[string]struct{}, len(repoNames))
	for _, name := range repoNames {
		if strings.TrimSpace(name) != "" {
			filter[name] = struct{}{}
		}
	}

	var snapshots []readOnlyRepoSnapshot
	for _, repo := range f.Repos {
		if len(filter) > 0 {
			if _, ok := filter[repo.Name]; !ok {
				continue
			}
		}
		if strings.TrimSpace(repo.WorktreePath) == "" {
			continue
		}
		snap, ok, err := captureReadOnlyRepoSnapshot(ctx, runner, repo)
		if err != nil {
			return nil, err
		}
		if ok {
			snapshots = append(snapshots, snap)
		}
	}
	sort.Slice(snapshots, func(i, j int) bool {
		return readOnlyRepoKey(snapshots[i].Name, snapshots[i].WorktreePath) < readOnlyRepoKey(snapshots[j].Name, snapshots[j].WorktreePath)
	})
	return snapshots, nil
}

func captureReadOnlyRepoSnapshot(ctx context.Context, runner ports.CommandRunner, repo feature.FeatureRepo) (readOnlyRepoSnapshot, bool, error) {
	worktreePath := filepath.Clean(repo.WorktreePath)
	info, err := os.Stat(worktreePath)
	if err != nil || !info.IsDir() {
		// pre-validation gate: not a repo yet is "skip", not fatal
		return readOnlyRepoSnapshot{}, false, nil //nolint:nilerr
	}
	inside, err := gitOutput(ctx, runner, worktreePath, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(string(inside)) != "true" { //nolint:goconst // coincidental match with the unix `true` command name used as an unrelated test stub; not the same concept
		// same gate: not a git worktree yet is "skip", not fatal
		return readOnlyRepoSnapshot{}, false, nil //nolint:nilerr
	}
	status, err := gitOutput(ctx, runner, worktreePath, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return readOnlyRepoSnapshot{}, false, fmt.Errorf("git status for %s: %w", repo.Name, err)
	}
	diff, err := gitOutput(ctx, runner, worktreePath, "diff", "--binary", "HEAD", "--")
	if err != nil {
		return readOnlyRepoSnapshot{}, false, fmt.Errorf("git diff for %s: %w", repo.Name, err)
	}
	untracked, err := captureReadOnlyUntrackedFiles(ctx, runner, worktreePath)
	if err != nil {
		return readOnlyRepoSnapshot{}, false, fmt.Errorf("capture untracked files for %s: %w", repo.Name, err)
	}
	name := repo.Name
	if name == "" {
		name = filepath.Base(worktreePath)
	}
	return readOnlyRepoSnapshot{
		Name:         name,
		WorktreePath: worktreePath,
		Status:       strings.TrimSpace(string(status)),
		Diff:         string(diff),
		Untracked:    untracked,
	}, true, nil
}

func captureReadOnlyUntrackedFiles(ctx context.Context, runner ports.CommandRunner, worktreePath string) ([]readOnlyUntrackedFile, error) {
	out, err := gitOutput(ctx, runner, worktreePath, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	raw := bytes.Split(out, []byte{0})
	files := make([]readOnlyUntrackedFile, 0, len(raw))
	for _, entry := range raw {
		if len(entry) == 0 {
			continue
		}
		rel := filepath.Clean(string(entry))
		if rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		path := filepath.Join(worktreePath, rel)
		info, err := os.Lstat(path)
		if err != nil {
			continue
		}
		file := readOnlyUntrackedFile{
			Path: filepath.ToSlash(rel),
			Mode: uint32(info.Mode()),
		}
		switch {
		case info.Mode().IsRegular():
			content, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			sum := sha256.Sum256(content)
			file.Kind = readOnlyUntrackedRegular
			file.SHA256 = hex.EncodeToString(sum[:])
			file.Content = content
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return nil, err
			}
			file.Kind = readOnlyUntrackedSymlink
			file.LinkTarget = target
		default:
			// Special entries are recorded so cleanup never removes a baseline
			// path that cannot be reconstructed safely (for example, a socket).
			file.Kind = readOnlyUntrackedOther
		}
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func restoreReadOnlyRepoBaseline(ctx context.Context, runner ports.CommandRunner, current, baseline readOnlyRepoSnapshot) error {
	worktreePath := current.WorktreePath
	if _, err := gitOutput(ctx, runner, worktreePath, "reset", "--hard", "HEAD"); err != nil {
		return fmt.Errorf("reset read-only repo %s: %w", current.Name, err)
	}
	baselinePaths := make(map[string]struct{}, len(baseline.Untracked))
	for _, file := range baseline.Untracked {
		baselinePaths[file.Path] = struct{}{}
	}
	// Remove only entries introduced during the read-only phase. A broad
	// `git clean` cannot distinguish those from pre-existing untracked
	// symlinks, sockets, or empty directories and can destroy user data that
	// the baseline does not know how to recreate.
	for _, file := range current.Untracked {
		if _, existed := baselinePaths[file.Path]; existed {
			continue
		}
		if err := removeIntroducedUntrackedPath(worktreePath, file.Path); err != nil {
			return fmt.Errorf("remove introduced untracked path %s: %w", file.Path, err)
		}
	}
	if strings.TrimSpace(baseline.Diff) != "" {
		if _, err := gitOutputWithStdin(ctx, runner, worktreePath, strings.NewReader(baseline.Diff), "apply", "--binary", "--whitespace=nowarn"); err != nil {
			return fmt.Errorf("restore baseline diff for %s: %w", current.Name, err)
		}
	}
	for _, file := range baseline.Untracked {
		rel := filepath.Clean(filepath.FromSlash(file.Path))
		if rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		path := filepath.Join(worktreePath, rel)
		kind := file.Kind
		if kind == "" { // Version 1 baselines contained regular files only.
			kind = readOnlyUntrackedRegular
		}
		switch kind {
		case readOnlyUntrackedRegular:
			if err := replaceWithRegularFile(path, file.Content, os.FileMode(file.Mode).Perm()); err != nil {
				return fmt.Errorf("restore untracked file %s: %w", file.Path, err)
			}
		case readOnlyUntrackedSymlink:
			if err := replaceWithSymlink(path, file.LinkTarget); err != nil {
				return fmt.Errorf("restore untracked symlink %s: %w", file.Path, err)
			}
		case readOnlyUntrackedOther:
			info, err := os.Lstat(path)
			if err != nil || uint32(info.Mode()) != file.Mode {
				return fmt.Errorf("cannot safely recreate special untracked entry %s", file.Path)
			}
		default:
			return fmt.Errorf("unknown baseline entry kind %q for %s", kind, file.Path)
		}
	}
	return nil
}

func removeIntroducedUntrackedPath(worktreePath, slashPath string) error {
	rel := filepath.Clean(filepath.FromSlash(slashPath))
	if rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("unsafe relative path")
	}
	path := filepath.Join(worktreePath, rel)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	for parent := filepath.Dir(path); parent != worktreePath && parent != filepath.Dir(parent); parent = filepath.Dir(parent) {
		if err := os.Remove(parent); err != nil {
			break // Non-empty or pre-existing parent directories are preserved.
		}
	}
	return nil
}

func replaceWithRegularFile(path string, content []byte, mode os.FileMode) error {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		if err := os.Remove(path); err != nil {
			return err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if mode == 0 {
		mode = 0o644
	}
	return os.WriteFile(path, content, mode)
}

func replaceWithSymlink(path, target string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.Symlink(target, path)
}

func writeReadOnlyRepoMutationPatch(phaseDir string, phase feature.Phase, baseline, current readOnlyRepoSnapshot) (string, error) {
	violationsDir := filepath.Join(phaseDir, "violations")
	if err := os.MkdirAll(violationsDir, 0o755); err != nil {
		return "", fmt.Errorf("create read-only repo violations dir: %w", err)
	}
	path, err := nextReadOnlyRepoMutationPatchPath(violationsDir, current.Name)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Agentico read-only phase repo mutation\n")
	fmt.Fprintf(&b, "# saved_at: %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "# phase: %s\n", phase.String())
	fmt.Fprintf(&b, "# repo: %s\n", current.Name)
	fmt.Fprintf(&b, "# worktree: %s\n\n", current.WorktreePath)
	if baseline.Status != "" {
		fmt.Fprintf(&b, "# baseline status\n%s\n\n", baseline.Status)
	}
	if current.Status != "" {
		fmt.Fprintf(&b, "# current status\n%s\n\n", current.Status)
	}
	if strings.TrimSpace(current.Diff) != "" {
		fmt.Fprintf(&b, "# current diff against HEAD\n%s\n", current.Diff)
	}
	if len(current.Untracked) > 0 {
		fmt.Fprintf(&b, "\n# current untracked files\n")
		for _, file := range current.Untracked {
			fmt.Fprintf(&b, "# %s sha256=%s bytes=%d\n", file.Path, file.SHA256, len(file.Content))
		}
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", fmt.Errorf("write read-only repo mutation patch: %w", err)
	}
	return path, nil
}

func readReadOnlyRepoBaseline(phaseDir string) (readOnlyRepoBaseline, error) {
	path := filepath.Join(phaseDir, readOnlyRepoBaselineFile)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return readOnlyRepoBaseline{Version: 1}, nil
	}
	if err != nil {
		return readOnlyRepoBaseline{}, fmt.Errorf("read read-only repo baseline: %w", err)
	}
	var baseline readOnlyRepoBaseline
	if err := json.Unmarshal(data, &baseline); err != nil {
		return readOnlyRepoBaseline{}, fmt.Errorf("parse read-only repo baseline: %w", err)
	}
	return baseline, nil
}

func readOnlyRepoSnapshotsEqual(a, b readOnlyRepoSnapshot) bool {
	if strings.TrimSpace(a.Status) != strings.TrimSpace(b.Status) || a.Diff != b.Diff {
		return false
	}
	if len(a.Untracked) != len(b.Untracked) {
		return false
	}
	for i := range a.Untracked {
		if a.Untracked[i].Path != b.Untracked[i].Path ||
			a.Untracked[i].Mode != b.Untracked[i].Mode ||
			a.Untracked[i].SHA256 != b.Untracked[i].SHA256 {
			return false
		}
	}
	return true
}

func nextReadOnlyRepoMutationPatchPath(dir, repoName string) (string, error) {
	slug := sanitizeReadOnlyRepoName(repoName)
	for i := 1; i < 1000; i++ {
		path := filepath.Join(dir, fmt.Sprintf("repo-mutation-%03d-%s.patch", i, slug))
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("too many read-only repo mutation patches in %s", dir)
}

// defaultReadOnlyRepoSlug is the fallback patch-filename slug used when a
// repo name is empty or sanitizes down to nothing (e.g. all-symbol names).
const defaultReadOnlyRepoSlug = "repo"

func sanitizeReadOnlyRepoName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return defaultReadOnlyRepoSlug
	}
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	if slug := strings.Trim(b.String(), "-"); slug != "" {
		return slug
	}
	return defaultReadOnlyRepoSlug
}

func readOnlyRepoKey(name, worktreePath string) string {
	return name + "\x00" + filepath.Clean(worktreePath)
}

func gitOutput(ctx context.Context, runner ports.CommandRunner, dir string, args ...string) ([]byte, error) {
	return gitOutputWithStdin(ctx, runner, dir, nil, args...)
}

func gitOutputWithStdin(ctx context.Context, runner ports.CommandRunner, dir string, stdin *strings.Reader, args ...string) ([]byte, error) {
	if runner == nil {
		runner = NewExecCommandRunner()
	}
	var stderr bytes.Buffer
	opts := ports.CommandOpts{Dir: dir, Stderr: &stderr}
	if stdin != nil {
		opts.Stdin = stdin
	}
	out, err := runner.Run(ctx, "git", args, opts)
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return out, fmt.Errorf("%w: %s", err, msg)
		}
		return out, err
	}
	return out, nil
}
