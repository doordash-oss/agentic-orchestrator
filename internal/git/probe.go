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
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// ProbeTimeout bounds every read-only git probe subprocess (freshness and
// cleanliness status/rev-parse/rev-list) so a hung git — cold cache on a huge
// worktree, network filesystem, index.lock contention — degrades to an
// indeterminate answer instead of stalling the caller. Overridable in tests.
var ProbeTimeout = 3 * time.Second

// ErrProbeTimeout reports that a git probe exceeded ProbeTimeout. Callers must
// treat it as indeterminate, never as a clean or in-sync worktree.
var ErrProbeTimeout = errors.New("git probe timed out")

// runProbe runs `git <args>` bounded by ProbeTimeout, returning its stdout and
// whether the bound was hit. The whole process group is killed on timeout so a
// git that spawned helpers cannot outlive the probe.
func runProbe(args ...string) (out []byte, timedOut bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), ProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if killErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); !errors.Is(killErr, syscall.ESRCH) {
			return killErr
		}
		return os.ErrProcessDone
	}
	// Backstop: stop waiting on inherited pipes if a descendant survives the kill.
	cmd.WaitDelay = time.Second
	out, err = cmd.Output()
	return out, errors.Is(ctx.Err(), context.DeadlineExceeded), err
}
