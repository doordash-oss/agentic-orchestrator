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
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// ReadinessProber resolves and checks Git once per catalog scan. It is safe
// for concurrent repository inspections and never changes process PATH.
type ReadinessProber struct{ executable string }

// NewReadinessProber detects shared installation failures before every repo
// pays for the same failing command (for example an unaccepted Xcode license).
func NewReadinessProber(ctx context.Context) (*ReadinessProber, error) {
	executable, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("locate git: %w", err)
	}
	prober := &ReadinessProber{executable: executable}
	if _, err := prober.run(ctx, "--version"); err != nil {
		return nil, err
	}
	return prober, nil
}

// Inspect distinguishes a usable unborn checkout from a failed Git command.
// No result is cached: a replaced checkout must acquire a new identity.
func (p *ReadinessProber) Inspect(ctx context.Context, dir string) (RepoIdentity, bool, error) {
	out, err := p.run(ctx, "-C", dir, "rev-parse", "--show-toplevel", "--git-common-dir")
	if err != nil {
		return RepoIdentity{}, false, err
	}
	identity, ok := repoIdentityFromOutput(out)
	if !ok {
		return RepoIdentity{}, false, fmt.Errorf("%s: could not resolve repository identity for %s", p.executable, dir)
	}
	_, err = p.run(ctx, "-C", dir, "rev-parse", "--verify", "--quiet", "HEAD")
	if err == nil {
		return identity, true, nil
	}
	var exit *exec.ExitError
	// --quiet uses exit 1 for an unresolved HEAD; execution/usage failures use
	// other statuses and must never be presented as an offer to create a commit.
	if errors.As(err, &exit) && exit.ExitCode() == 1 && ctx.Err() == nil {
		return identity, false, nil
	}
	return RepoIdentity{}, false, err
}

func (p *ReadinessProber) run(parent context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.executable, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return os.ErrProcessDone
	}
	cmd.WaitDelay = time.Second
	var stdout, stderr probeOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, fmt.Errorf("%s %s: %w: %s", p.executable, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// Bound provider-controlled output while continuing to drain the pipes.
type probeOutput struct{ buffer bytes.Buffer }

func (b *probeOutput) Bytes() []byte  { return b.buffer.Bytes() }
func (b *probeOutput) String() string { return b.buffer.String() }

func (b *probeOutput) Write(p []byte) (int, error) {
	n := len(p)
	const limit = 16 * 1024
	if remaining := limit - b.buffer.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.buffer.Write(p)
	}
	return n, nil
}
