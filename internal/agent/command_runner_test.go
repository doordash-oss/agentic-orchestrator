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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// Compile-time interface check.
var _ ports.CommandRunner = (*execCommandRunner)(nil)

func TestExecCommandRunner_Run_Success(t *testing.T) {
	r := &execCommandRunner{}
	out, err := r.Run(context.Background(), "echo", []string{"hello"}, ports.CommandOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestExecCommandRunner_Run_NonZeroExit(t *testing.T) {
	r := &execCommandRunner{}
	_, err := r.Run(context.Background(), "sh", []string{"-c", "exit 1"}, ports.CommandOpts{})
	if err == nil {
		t.Fatal("expected error for non-zero exit, got nil")
	}
}

func TestExecCommandRunner_Run_Timeout(t *testing.T) {
	r := &execCommandRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := r.Run(ctx, "sleep", []string{"10"}, ports.CommandOpts{})
	if err == nil {
		t.Fatal("expected error for timed-out command, got nil")
	}
}

func TestExecCommandRunner_Run_EnvPropagation(t *testing.T) {
	r := &execCommandRunner{}
	out, err := r.Run(context.Background(), "env", nil, ports.CommandOpts{
		Env: []string{"TEST_AGENTIC_VAR=hello_world"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(out), "TEST_AGENTIC_VAR=hello_world") {
		t.Errorf("env output does not contain expected var:\n%s", out)
	}
}

func TestExecCommandRunner_Run_WorkDir(t *testing.T) {
	r := &execCommandRunner{}
	dir := t.TempDir()
	out, err := r.Run(context.Background(), "pwd", nil, ports.CommandOpts{Dir: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := strings.TrimSpace(string(out))
	// Resolve symlinks on both sides: on macOS `pwd` may return
	// /private/var/... while t.TempDir() returns /var/... (a symlink).
	wantResolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks on expected dir: %v", err)
	}
	gotResolved, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("eval symlinks on actual dir: %v", err)
	}
	if gotResolved != wantResolved {
		t.Errorf("got working dir %q (resolved %q), want %q (resolved %q)", got, gotResolved, dir, wantResolved)
	}
}

func TestExecCommandRunner_Run_Stdin(t *testing.T) {
	r := &execCommandRunner{}
	input := "piped input data"
	out, err := r.Run(context.Background(), "cat", nil, ports.CommandOpts{
		Stdin: strings.NewReader(input),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := string(out); got != input {
		t.Errorf("got %q, want %q", got, input)
	}
}

func TestExecCommandRunner_Run_Stderr(t *testing.T) {
	r := &execCommandRunner{}
	var stderr bytes.Buffer
	out, err := r.Run(context.Background(), "sh", []string{"-c", "echo errdata >&2; echo stdoutdata"}, ports.CommandOpts{
		Stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(out), "stdoutdata") {
		t.Errorf("stdout should contain stdoutdata, got %q", out)
	}
	if !strings.Contains(stderr.String(), "errdata") {
		t.Errorf("stderr should contain errdata, got %q", stderr.String())
	}
}
