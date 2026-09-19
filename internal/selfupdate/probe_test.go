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
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// skipUnsupportedProbeOS keeps the real-process probe tests on the two
// platforms this feature supports.
func skipUnsupportedProbeOS(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skipf("probe tests require darwin or linux, not %s", runtime.GOOS)
	}
}

// writeProbeScript creates an executable /bin/sh script. The shebang is an
// absolute path so kernel resolution never depends on the child environment.
func writeProbeScript(t *testing.T, dir, body string) string {
	t.Helper()
	return writeExecutableFile(t, dir, "probe-subject.sh", 0o755, []byte("#!/bin/sh\n"+body+"\n"))
}

func TestProbeSuccess(t *testing.T) {
	skipUnsupportedProbeOS(t)
	script := writeProbeScript(t, t.TempDir(), `echo 'agentico v1.2.3'`)
	res, err := ProbeStagedExecutable(context.Background(), script, "1.2.3")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Version != "1.2.3" {
		t.Errorf("version = %q, want %q", res.Version, "1.2.3")
	}
	if res.Banner != "agentico v1.2.3" {
		t.Errorf("banner = %q, want %q", res.Banner, "agentico v1.2.3")
	}
	if want := mustDigest(t, script); res.ExecutableDigest != want {
		t.Errorf("executable digest = %q, want %q", res.ExecutableDigest, want)
	}
}

func TestProbeSuccessWithRevision(t *testing.T) {
	skipUnsupportedProbeOS(t)
	script := writeProbeScript(t, t.TempDir(), `echo 'agentico v1.2.3 (revision abc123def)'`)
	res, err := ProbeStagedExecutable(context.Background(), script, "1.2.3")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Version != "1.2.3" {
		t.Errorf("version = %q, want %q", res.Version, "1.2.3")
	}
	if res.Banner != "agentico v1.2.3 (revision abc123def)" {
		t.Errorf("banner = %q, want %q", res.Banner, "agentico v1.2.3 (revision abc123def)")
	}
}

func TestProbeWrongVersion(t *testing.T) {
	skipUnsupportedProbeOS(t)
	script := writeProbeScript(t, t.TempDir(), `echo 'agentico v1.2.4'`)
	_, err := ProbeStagedExecutable(context.Background(), script, "1.2.3")
	if err == nil {
		t.Fatal("expected error for wrong version banner, got nil")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Errorf("error %q does not mention version", err.Error())
	}
}

func TestProbeAdditionalOutput(t *testing.T) {
	skipUnsupportedProbeOS(t)
	script := writeProbeScript(t, t.TempDir(), `echo 'agentico v1.2.3'; echo 'extra line'`)
	_, err := ProbeStagedExecutable(context.Background(), script, "1.2.3")
	if err == nil {
		t.Fatal("expected error for additional output, got nil")
	}
}

func TestProbeStderrOutput(t *testing.T) {
	skipUnsupportedProbeOS(t)
	script := writeProbeScript(t, t.TempDir(), `echo 'agentico v1.2.3'; echo boom >&2`)
	_, err := ProbeStagedExecutable(context.Background(), script, "1.2.3")
	if err == nil {
		t.Fatal("expected error for stderr output, got nil")
	}
	if !strings.Contains(err.Error(), "stderr") {
		t.Errorf("error %q does not mention stderr", err.Error())
	}
}

func TestProbeNonzeroExit(t *testing.T) {
	skipUnsupportedProbeOS(t)
	script := writeProbeScript(t, t.TempDir(), `echo 'agentico v1.2.3'; exit 3`)
	_, err := ProbeStagedExecutable(context.Background(), script, "1.2.3")
	if err == nil {
		t.Fatal("expected error for nonzero exit, got nil")
	}
	if !strings.Contains(err.Error(), "exit") {
		t.Errorf("error %q does not mention exit", err.Error())
	}
}

func TestProbeEmptyOutput(t *testing.T) {
	skipUnsupportedProbeOS(t)
	script := writeProbeScript(t, t.TempDir(), `exit 0`)
	_, err := ProbeStagedExecutable(context.Background(), script, "1.2.3")
	if err == nil {
		t.Fatal("expected error for empty output, got nil")
	}
}

func TestProbeMalformedBanner(t *testing.T) {
	skipUnsupportedProbeOS(t)
	script := writeProbeScript(t, t.TempDir(), `echo 'not agentico'`)
	_, err := ProbeStagedExecutable(context.Background(), script, "1.2.3")
	if err == nil {
		t.Fatal("expected error for malformed banner, got nil")
	}
}

func TestProbeTimeout(t *testing.T) {
	skipUnsupportedProbeOS(t)
	oldTimeout := probeTimeout
	probeTimeout = 300 * time.Millisecond
	defer func() { probeTimeout = oldTimeout }()

	script := writeProbeScript(t, t.TempDir(), `sleep 5`)
	start := time.Now()
	_, err := ProbeStagedExecutable(context.Background(), script, "1.2.3")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "deadline") && !strings.Contains(msg, "timeout") && !strings.Contains(msg, "signal") {
		t.Errorf("error %q does not mention deadline/timeout/signal", msg)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("probe took %s to fail; child was not reaped promptly", elapsed)
	}
}

func TestProbeOutputOverflow(t *testing.T) {
	skipUnsupportedProbeOS(t)
	oldLimit := probeMaxOutputBytes
	probeMaxOutputBytes = 16
	defer func() { probeMaxOutputBytes = oldLimit }()

	script := writeProbeScript(t, t.TempDir(), `yes | head -c 100`)
	start := time.Now()
	_, err := ProbeStagedExecutable(context.Background(), script, "1.2.3")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error for output overflow, got nil")
	}
	if !strings.Contains(err.Error(), "output limit") {
		t.Errorf("error %q does not mention output limit", err.Error())
	}
	if elapsed > 2*time.Second {
		t.Fatalf("probe took %s to fail; child was not reaped promptly", elapsed)
	}
}

func TestProbeCancellation(t *testing.T) {
	skipUnsupportedProbeOS(t)
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(100*time.Millisecond, cancel)
	defer timer.Stop()
	defer cancel()

	script := writeProbeScript(t, t.TempDir(), `sleep 5`)
	_, err := ProbeStagedExecutable(ctx, script, "1.2.3")
	if err == nil {
		t.Fatal("expected error for canceled context, got nil")
	}
}

func TestProbeEnvironmentIsolation(t *testing.T) {
	skipUnsupportedProbeOS(t)
	t.Setenv("GITHUB_TOKEN", "sekrit")
	t.Setenv(HandoffEnvVar, "{}")
	t.Setenv("AGENTICO_UPDATES", "1")
	t.Setenv("ANTHROPIC_API_KEY", "sekrit")

	script := writeProbeScript(t, t.TempDir(), `if [ -n "$GITHUB_TOKEN" ] || [ -n "$AGENTICO_SELFUPDATE_HANDOFF" ] || [ -n "$AGENTICO_UPDATES" ] || [ -n "$ANTHROPIC_API_KEY" ]; then echo leaked >&2; exit 9; fi; echo 'agentico v1.2.3'`)
	res, err := ProbeStagedExecutable(context.Background(), script, "1.2.3")
	if err != nil {
		t.Fatalf("probe leaked environment or failed: %v", err)
	}
	if res.Version != "1.2.3" {
		t.Errorf("version = %q, want %q", res.Version, "1.2.3")
	}
}

func TestProbeWorkingDirIsolation(t *testing.T) {
	skipUnsupportedProbeOS(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if strings.Contains(cwd, "'") {
		t.Skipf("cannot quote cwd %q in a shell single-quoted string", cwd)
	}

	script := writeProbeScript(t, t.TempDir(), "if [ \"$PWD\" = '"+cwd+"' ]; then exit 8; fi; echo 'agentico v1.2.3'")
	res, err := ProbeStagedExecutable(context.Background(), script, "1.2.3")
	if err != nil {
		t.Fatalf("probe ran in the test working dir or failed: %v", err)
	}
	if res.Version != "1.2.3" {
		t.Errorf("version = %q, want %q", res.Version, "1.2.3")
	}
}

func TestProbeRunsExactlyOnce(t *testing.T) {
	skipUnsupportedProbeOS(t)
	dir := t.TempDir()
	// The counter lives beside the script, not in the child's HOME: the probe
	// removes its isolated HOME directory on every exit, which would destroy
	// the evidence.
	script := writeProbeScript(t, dir, `echo x >> "$(dirname "$0")/calls"`+"\n"+`echo 'agentico v1.2.3'`)
	_, err := ProbeStagedExecutable(context.Background(), script, "1.2.3")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	calls, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil {
		t.Fatalf("read calls counter: %v", err)
	}
	if got := strings.Count(string(calls), "\n"); got != 1 {
		t.Fatalf("probe invoked the executable %d times, want exactly 1 (calls: %q)", got, string(calls))
	}
}
