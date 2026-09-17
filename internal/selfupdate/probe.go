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

package selfupdate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	ProbeTimeout        = 10 * time.Second
	ProbeMaxOutputBytes = 4096 // bounded captured stdout and stderr
)

// probeTimeout and probeMaxOutputBytes are test seams over the exported
// bounds; production always uses the constants.
var (
	probeTimeout        = ProbeTimeout
	probeMaxOutputBytes = ProbeMaxOutputBytes
)

// probeWaitDelay bounds how long the probe's capture pipes may outlive the
// probed child (an orphaned grandchild can inherit the write end).
const probeWaitDelay = 250 * time.Millisecond

// ProbeResult is the outcome of one successful staged-executable probe.
type ProbeResult struct {
	// Version is the exact version token parsed from the banner.
	Version string
	// Banner is the exact single-line stdout banner.
	Banner string
	// ExecutableDigest is the sha256 hex digest of the probed executable
	// bytes as recorded after the probe (unchanged by best-effort quarantine
	// removal on darwin).
	ExecutableDigest string
}

// ProbeStagedExecutable runs path with "--version" exactly once and requires
// exit code zero and the existing single-line "agentico v<expectedVersion>"
// banner (with the optional " (revision ...)" suffix) on stdout, with no
// stderr output and no additional output. The child runs with a ten-second
// timeout, bounded captured stdout/stderr, an isolated private working
// directory, and an explicit minimal environment (PATH and HOME only) that
// never inherits credentials, provider variables, fixture hooks, or handoff
// metadata. It also never inherits update leases or runtime/listener
// descriptors: no extra file descriptors are passed to the child.
func ProbeStagedExecutable(ctx context.Context, path, expectedVersion string) (ProbeResult, error) {
	if ctx == nil {
		return ProbeResult{}, errors.New("probe requires a non-nil context")
	}

	preDigest, err := DigestFile(path)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("probe pre-digest %s: %w", path, err)
	}

	// Best effort with every error ignored: a missing or unsupported
	// quarantine xattr must never block the probe, and removal never alters
	// the file bytes, which the post-probe digest comparison verifies.
	_ = removeQuarantine(path)

	tmpDir, err := os.MkdirTemp("", "agentico-probe-")
	if err != nil {
		return ProbeResult{}, fmt.Errorf("create probe working dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	timeout := probeTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("open probe stdin: %w", err)
	}
	defer devNull.Close()

	stdoutCap := &boundedWriter{limit: probeMaxOutputBytes}
	stderrCap := &boundedWriter{limit: probeMaxOutputBytes}

	cmd := exec.CommandContext(probeCtx, path, "--version")
	// The environment is built from scratch, never from os.Environ: the
	// child must not inherit credentials, provider configuration, fixture
	// hooks, or handoff metadata. PATH keeps basic tool resolution; HOME
	// anchors the private working directory.
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + tmpDir}
	cmd.Dir = tmpDir
	cmd.Stdin = devNull
	cmd.Stdout = stdoutCap
	cmd.Stderr = stderrCap
	// WaitDelay bounds the capture pipes: a killed shell whose orphaned
	// grandchild still holds the write end can never keep the probe (and its
	// reaping Wait) blocked indefinitely.
	cmd.WaitDelay = probeWaitDelay

	if err := cmd.Start(); err != nil {
		return ProbeResult{}, fmt.Errorf("start probe %s: %w", path, err)
	}
	waitErr := cmd.Wait()

	if err := probeCtx.Err(); err != nil {
		return ProbeResult{}, fmt.Errorf("probe %s --version was terminated before completion: %w", path, err)
	}
	if stdoutCap.overflow || stderrCap.overflow {
		return ProbeResult{}, fmt.Errorf("probe output exceeded the %d-byte output limit", probeMaxOutputBytes)
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return ProbeResult{}, fmt.Errorf("probe exited with code %d", exitErr.ExitCode())
		}
		if errors.Is(waitErr, exec.ErrWaitDelay) {
			return ProbeResult{}, errors.New("probe output streams did not close after exit")
		}
		return ProbeResult{}, fmt.Errorf("probe %s --version: %w", path, waitErr)
	}
	if stderrCap.buf.Len() != 0 {
		return ProbeResult{}, fmt.Errorf("probe stderr was not empty: %q", stderrCap.buf.String())
	}

	banner, ok := probeBannerLine(stdoutCap.buf.String())
	if !ok {
		if stdoutCap.buf.Len() == 0 {
			return ProbeResult{}, errors.New("probe stdout was empty; want a version banner")
		}
		return ProbeResult{}, fmt.Errorf("probe stdout %q is not a single line", stdoutCap.buf.String())
	}
	version, ok := matchProbeBanner(banner, expectedVersion)
	if !ok {
		return ProbeResult{}, fmt.Errorf("probe stdout banner %q does not report expected version %q", banner, expectedVersion)
	}

	postDigest, err := DigestFile(path)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("probe post-digest %s: %w", path, err)
	}
	if postDigest != preDigest {
		return ProbeResult{}, fmt.Errorf("probed executable %s changed during the probe: pre-digest %s, post-digest %s", path, preDigest, postDigest)
	}
	return ProbeResult{Version: version, Banner: banner, ExecutableDigest: postDigest}, nil
}

// errProbeOutputOverflow marks a captured stream that wrote past its bound.
var errProbeOutputOverflow = errors.New("probe output exceeded the capture limit")

// boundedWriter accepts at most limit bytes and then reports overflow, so
// output excess is detected instead of silently truncated.
type boundedWriter struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if w.buf.Len()+len(p) > w.limit {
		w.overflow = true
		if room := w.limit - w.buf.Len(); room > 0 {
			w.buf.Write(p[:room])
		}
		// Report full consumption so io.Copy stops instead of retrying.
		return len(p), errProbeOutputOverflow
	}
	return w.buf.Write(p)
}

// probeBannerLine trims one trailing newline (LF or CRLF) and rejects
// anything that is not exactly one non-empty line.
func probeBannerLine(out string) (string, bool) {
	line := out
	if strings.HasSuffix(line, "\n") {
		line = line[:len(line)-1]
	}
	if strings.HasSuffix(line, "\r") {
		line = line[:len(line)-1]
	}
	if line == "" || strings.ContainsAny(line, "\n\r") {
		return "", false
	}
	return line, true
}

// matchProbeBanner matches the buildinfo.VersionLine grammar strictly:
// "agentico v<expectedVersion>" with an optional " (revision <token>)"
// suffix whose token is non-empty and whitespace-free. The version token
// must equal expectedVersion exactly.
func matchProbeBanner(banner, expectedVersion string) (string, bool) {
	prefix := "agentico v" + expectedVersion
	if banner == prefix {
		return expectedVersion, true
	}
	rest, ok := strings.CutPrefix(banner, prefix)
	if !ok {
		return "", false
	}
	rest, ok = strings.CutPrefix(rest, " (revision ")
	if !ok {
		return "", false
	}
	rest, ok = strings.CutSuffix(rest, ")")
	if !ok {
		return "", false
	}
	if rest == "" || strings.ContainsAny(rest, " \t\n\r") {
		return "", false
	}
	return expectedVersion, true
}
