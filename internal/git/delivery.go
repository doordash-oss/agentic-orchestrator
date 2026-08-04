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
	"os/exec"
	"strconv"
	"strings"
)

// PendingWork is local work measured against a delivery destination — the
// remote branch behind a pull request, or the base branch of a local merge.
type PendingWork struct {
	Commits          int
	DestinationAhead int
	Dirty            bool
}

// Pending reports whether local work has not reached the destination.
func (w PendingWork) Pending() bool { return w.Commits > 0 || w.Dirty }

// PendingAgainst measures work in worktreePath that has not reached dest. It
// reads local refs only — never fetching, never mutating — so it is safe in a
// side-effect-free preflight. ok is false when dest does not resolve, which is
// the honest answer for a missing remote-tracking ref.
func PendingAgainst(worktreePath, dest string) (PendingWork, bool) {
	if worktreePath == "" || dest == "" {
		return PendingWork{}, false
	}
	verify := exec.Command("git", "-C", worktreePath, "rev-parse", "--verify", "--quiet", dest+"^{commit}")
	if err := verify.Run(); err != nil {
		return PendingWork{}, false
	}
	out, err := exec.Command("git", "--no-optional-locks", "-C", worktreePath,
		"rev-list", "--left-right", "--count", "HEAD..."+dest).Output()
	if err != nil {
		return PendingWork{}, false
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 {
		return PendingWork{}, false
	}
	ahead, errAhead := strconv.Atoi(fields[0])
	behind, errBehind := strconv.Atoi(fields[1])
	if errAhead != nil || errBehind != nil {
		return PendingWork{}, false
	}
	return PendingWork{
		Commits:          ahead,
		DestinationAhead: behind,
		Dirty:            HasUncommittedChanges(worktreePath),
	}, true
}
