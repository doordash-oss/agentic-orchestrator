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

package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// IdentityProbeTimeout bounds ResolveRepoIdentity invocations.
var IdentityProbeTimeout = 5 * time.Second

// RepoIdentity is the server-resolved identity of a repository checkout.
// Path is the canonical checkout path and CommonDir the resolved Git common
// directory, so linked worktrees that share a common directory stay
// distinguishable from their main checkout. Device and Inode pin the Git
// common directory itself: replacing a checkout, or replacing the Git
// repository at the same path, invalidates the prior identity instead of
// silently adopting the replacement. BirthTime, when the filesystem exposes
// it, also distinguishes replacements that reuse the deleted directory's inode.
// Ordinary commits, branch checkouts, discovery refreshes and reconnects leave
// the identity unchanged.
type RepoIdentity struct {
	Path      string
	CommonDir string
	Device    uint64
	Inode     uint64
	BirthTime string
}

// Equal reports whether two identities describe the same repository
// checkout on the same server.
func (a RepoIdentity) Equal(b RepoIdentity) bool {
	return a == b
}

// ResolveRepoIdentity resolves the identity of the git repository checkout at
// dir. It reports false when dir is not a usable work tree (for example a
// bare repository, a broken .git pointer, or a missing git executable); the
// caller decides how to surface that to the user.
func ResolveRepoIdentity(dir string) (RepoIdentity, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), IdentityProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel", "--git-common-dir")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return os.ErrProcessDone
	}
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return RepoIdentity{}, false
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		return RepoIdentity{}, false
	}
	toplevel := resolveCanonicalPath(strings.TrimSpace(lines[0]), "")
	if toplevel == "" {
		return RepoIdentity{}, false
	}
	commonDir := resolveCanonicalPath(strings.TrimSpace(lines[1]), toplevel)
	if commonDir == "" {
		return RepoIdentity{}, false
	}
	device, inode, birthTime, err := StatRepoDirectory(commonDir)
	if err != nil {
		return RepoIdentity{}, false
	}
	return RepoIdentity{
		Path:      toplevel,
		CommonDir: commonDir,
		Device:    device,
		Inode:     inode,
		BirthTime: birthTime,
	}, true
}

// resolveCanonicalPath resolves raw into a canonical absolute path. Relative
// Git paths (for example the ".git" common directory of a main worktree) are
// resolved against base, which must be the checkout's canonical path.
func resolveCanonicalPath(raw, base string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsRune(raw, 0) {
		return ""
	}
	joined := raw
	if !filepath.IsAbs(joined) {
		if base == "" {
			return ""
		}
		joined = filepath.Join(base, raw)
	}
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return ""
	}
	if resolved == "" || !filepath.IsAbs(resolved) {
		return ""
	}
	return resolved
}

// FormatIdentityDevice renders a device id as the decimal text used on the
// wire so 64-bit values stay exact in every client language.
func FormatIdentityDevice(v uint64) string {
	return strconv.FormatUint(v, 10)
}

// FormatIdentityInode renders an inode as decimal wire text.
func FormatIdentityInode(v uint64) string {
	return strconv.FormatUint(v, 10)
}
