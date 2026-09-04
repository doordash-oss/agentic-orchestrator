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
	"syscall"
	"time"
)

// HeadProbeTimeout bounds HasHead invocations.
var HeadProbeTimeout = 5 * time.Second

// HasHead reports whether the git repository at dir has at least one
// commit on HEAD. A repository without commits (an unborn branch, e.g.
// right after cloning an empty remote) is still a valid repository but
// cannot start feature work.
func HasHead(dir string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), HeadProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--verify", "HEAD")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return os.ErrProcessDone
	}
	cmd.WaitDelay = time.Second
	return cmd.Run() == nil
}
