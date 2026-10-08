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

package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// Shell tools do not report file changes. Observe actual repository contents
// while a turn uses a shell, so Python, formatters and redirected writes take
// the same durable display path as native edit tools. Never execute or infer
// a patch from command text: it is used only to discover repository roots.
const (
	shellDiffInterval = 750 * time.Millisecond
	shellDiffMaxFile  = 512 * 1024
	shellDiffMaxBytes = 64 * 1024 * 1024
	shellDiffMaxFiles = 4096
	shellDiffMaxRepos = 4
)

type shellFile struct {
	content  []byte
	size     int64
	modified time.Time
}
type shellRepository struct {
	root  string
	files map[string]shellFile
}
type shellDiffObserver struct {
	mu     sync.Mutex
	repos  map[string]*shellRepository
	stop   chan struct{}
	turn   string
	closed bool
}

var shellAbsolutePath = regexp.MustCompile(`/[^\s'"` + "`" + `<>;|(){}\\]+`)

func repositoryRoot(path string) string {
	if !filepath.IsAbs(path) {
		return ""
	}
	path = filepath.Clean(path)
	for path != "/" && path != "." {
		if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
			// macOS exposes the same temporary worktree through /var and
			// /private/var. One canonical root prevents duplicate cards.
			root, err := filepath.EvalSymlinks(path)
			if err == nil {
				return root
			}
			return ""
		}
		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
	}
	return ""
}

func shellRoots(workDir string, block llm.ContentBlock) []string {
	var input map[string]any
	if json.Unmarshal(block.Input, &input) != nil {
		return nil
	}
	candidates := []string{workDir}
	for _, key := range []string{"cwd", "workdir", "working_directory"} {
		if value, ok := input[key].(string); ok && value != "" {
			candidates = append(candidates, value)
		}
	}
	if command, ok := input["command"].(string); ok {
		candidates = append(candidates, shellAbsolutePath.FindAllString(command, 32)...)
	}
	roots := []string{}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		root := repositoryRoot(candidate)
		if root != "" && !seen[root] {
			seen[root] = true
			roots = append(roots, root)
		}
	}
	return roots
}

// Listing uses Git's ignore rules; only bounded regular UTF-8 source files
// are read. No symlink targets, ignored artifacts, private keys or env files.
func shellSourcePath(root, path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if base == ".env" || strings.HasPrefix(base, ".env.") || strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") || strings.Contains(base, "credentials") {
		return false
	}
	full := filepath.Join(root, path)
	real, err := filepath.EvalSymlinks(full)
	canonicalRoot, rootErr := filepath.EvalSymlinks(root)
	return err == nil && rootErr == nil && real == filepath.Join(canonicalRoot, path)
}

func gitOutput(dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// Commands are read-only and must not refresh the index or invoke helpers.
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	var out limitedDiffBuffer
	cmd.Stdout = &out
	err := cmd.Run()
	return out.Bytes(), err
}

type limitedDiffBuffer struct{ bytes.Buffer }

func (b *limitedDiffBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 8*1024*1024 {
		return 0, os.ErrInvalid
	}
	return b.Buffer.Write(p)
}

func (r *shellRepository) snapshot() (map[string]shellFile, bool) {
	output, err := gitOutput(r.root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, false
	}
	paths := strings.Split(string(output), "\x00")
	if len(paths) > shellDiffMaxFiles {
		return nil, false
	}
	root, err := os.OpenRoot(r.root)
	if err != nil {
		return nil, false
	}
	defer root.Close()
	next := map[string]shellFile{}
	bytesRead := 0
	for _, path := range paths {
		if path == "" || filepath.IsAbs(path) || strings.HasPrefix(path, "../") {
			continue
		}
		full := filepath.Join(r.root, path)
		info, err := os.Lstat(full)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, false
		}
		if !info.Mode().IsRegular() || info.Size() > shellDiffMaxFile || !shellSourcePath(r.root, path) {
			continue
		}
		bytesRead += int(info.Size())
		if bytesRead > shellDiffMaxBytes {
			return nil, false
		}
		if previous, ok := r.files[path]; ok && previous.size == info.Size() && previous.modified.Equal(info.ModTime()) {
			next[path] = previous
			continue
		}
		file, err := root.Open(path)
		if err != nil {
			return nil, false
		}
		content, err := io.ReadAll(io.LimitReader(file, shellDiffMaxFile+1))
		after, statErr := file.Stat()
		file.Close()
		if err != nil || statErr != nil {
			return nil, false
		}
		if len(content) > shellDiffMaxFile || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
			return nil, false
		}
		if bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content) {
			continue
		}
		next[path] = shellFile{content: content, size: info.Size(), modified: info.ModTime()}
	}
	return next, true
}

func shellPatch(path string, before, after []byte, operation string) *llm.FileChangeEvent {
	if bytes.Equal(before, after) {
		return nil
	}
	dir, err := os.MkdirTemp("", "agentico-diff-")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(dir)
	a, b := filepath.Join(dir, "before"), filepath.Join(dir, "after")
	if os.WriteFile(a, before, 0600) != nil || os.WriteFile(b, after, 0600) != nil {
		return nil
	}
	out, err := gitOutput(dir, "diff", "--no-index", "--no-ext-diff", "--no-textconv", "--no-color", "--unified=2", "--", a, b)
	if err != nil {
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
			return nil
		}
	}
	lines := strings.Split(string(out), "\n")
	change := &llm.FileChangeEvent{Path: path, Operation: operation, HasDiffPatch: true}
	var patch []string
	started := false
	for _, line := range lines {
		if strings.HasPrefix(line, "@@") {
			started = true
		}
		if !started {
			continue
		}
		if strings.HasPrefix(line, "+") {
			change.AddedLines++
		}
		if strings.HasPrefix(line, "-") {
			change.RemovedLines++
		}
		if len(patch) < 80 {
			patch = append(patch, line)
		}
	}
	change.Detail = strings.Join(patch, "\n")
	if change.Detail == "" {
		return nil
	}
	return change
}

func (r *shellRepository) changes() []llm.FileChangeEvent {
	next, ok := r.snapshot()
	if !ok {
		return nil
	}
	paths := map[string]bool{}
	for path := range r.files {
		paths[path] = true
	}
	for path := range next {
		paths[path] = true
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	var changes []llm.FileChangeEvent
	for _, path := range ordered {
		old, was := r.files[path]
		current, is := next[path]
		// A file becoming excluded, binary, symlinked or oversized is not deleted.
		if was && !is {
			if _, err := os.Lstat(filepath.Join(r.root, path)); !os.IsNotExist(err) {
				continue
			}
		}
		operation := "update"
		if !was {
			operation = "add"
		} else if !is {
			operation = "delete"
		}
		if change := shellPatch(filepath.Join(r.root, path), old.content, current.content, operation); change != nil {
			changes = append(changes, *change)
		}
	}
	r.files = next
	return changes
}

func (o *generationObserver) observeShellChanges(sessionID string, msg llm.SDKMessage) {
	o.diff.mu.Lock()
	defer o.diff.mu.Unlock()
	if o.diff.closed {
		return
	}
	c := o.c
	c.mu.Lock()
	valid := sessionID == c.sessionID && o.generation == c.conv.Generation
	turn := c.currentTurnLocked()
	c.mu.Unlock()
	if !valid || turn == "" {
		return
	}
	if o.diff.turn != turn {
		o.stopShellDiffLocked()
		o.diff.turn = turn
	}
	if msg.Assistant != nil {
		for _, block := range msg.Assistant.Message.Content {
			if !block.IsToolUse() || (block.Name != "Bash" && block.Name != "shell_command" && block.Name != "exec_command") {
				continue
			}
			if o.diff.repos == nil {
				o.diff.repos = map[string]*shellRepository{}
			}
			for _, root := range shellRoots(c.opts.WorkDir, block) {
				if o.diff.repos[root] != nil || len(o.diff.repos) >= shellDiffMaxRepos {
					continue
				}
				repo := &shellRepository{root: root}
				if files, ok := repo.snapshot(); ok {
					repo.files = files
					o.diff.repos[root] = repo
				}
			}
			if o.diff.stop == nil && len(o.diff.repos) > 0 {
				o.diff.stop = make(chan struct{})
				go o.pollShellDiff(sessionID, turn, o.diff.stop)
			}
		}
	}
	if msg.User != nil || msg.Result != nil {
		o.captureShellDiffLocked(sessionID, turn)
	}
	if msg.Result != nil && msg.Origin.Kind != llm.EventOriginTask {
		o.stopShellDiffLocked()
	}
}

func (o *generationObserver) captureShellDiffLocked(sessionID, turn string) {
	var changes []llm.FileChangeEvent
	for _, repo := range o.diff.repos {
		changes = append(changes, repo.changes()...)
	}
	if len(changes) == 0 {
		return
	}
	c := o.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if sessionID != c.sessionID || o.generation != c.conv.Generation || c.currentTurnLocked() != turn {
		return
	}
	c.failWriteLocked(c.appendProviderLocked(o.generation, KindToolResult, ContentData{FileChanges: changes, ObservedFiles: true}, "", ""))
}

func (o *generationObserver) pollShellDiff(sessionID, turn string, stop chan struct{}) {
	ticker := time.NewTicker(shellDiffInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			o.diff.mu.Lock()
			if o.diff.stop != stop {
				o.diff.mu.Unlock()
				return
			}
			o.c.mu.Lock()
			live := o.c.conv.Generation == o.generation && o.c.sessionID == sessionID && o.c.currentTurnLocked() == turn && o.c.lifecycle != LifecycleStopped && o.c.lifecycle != LifecycleIdle && o.c.lifecycle != LifecycleFailed
			o.c.mu.Unlock()
			if !live {
				o.stopShellDiffLocked()
				o.diff.mu.Unlock()
				return
			}
			o.captureShellDiffLocked(sessionID, turn)
			o.diff.mu.Unlock()
		}
	}
}
func (o *generationObserver) stopShellDiffLocked() {
	if o.diff.stop != nil {
		close(o.diff.stop)
		o.diff.stop = nil
	}
	o.diff.repos = nil
}
func (o *generationObserver) closeShellDiff() {
	o.diff.mu.Lock()
	defer o.diff.mu.Unlock()
	o.diff.closed = true
	o.stopShellDiffLocked()
}
