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
	"strings"
	"testing"
)

func TestReadinessProberDistinguishesUnbornAndCommittedCheckouts(t *testing.T) {
	dir := t.TempDir()
	runTestGit(t, dir, "init", "-b", "main")
	prober, err := NewReadinessProber(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	identity, hasHead, err := prober.Inspect(context.Background(), dir)
	if err != nil || hasHead || identity.Path == "" {
		t.Fatalf("unborn: identity=%+v head=%v err=%v", identity, hasHead, err)
	}
	committed := initIdentityRepo(t)
	identity, hasHead, err = prober.Inspect(context.Background(), committed)
	if err != nil || !hasHead || identity.Path == "" {
		t.Fatalf("committed: identity=%+v head=%v err=%v", identity, hasHead, err)
	}
}

func TestReadinessProberReportsHeadExecutionFailure(t *testing.T) {
	dir := initIdentityRepo(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	t.Setenv("AGENTICO_REAL_TEST_GIT", realGit)
	// Installation and identity checks succeed; only HEAD execution fails.
	script := "#!/bin/sh\ncase \"$*\" in *--verify*) echo 'Git execution failed' >&2; exit 69;; esac\nexec \"$AGENTICO_REAL_TEST_GIT\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	prober, err := NewReadinessProber(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	identity, hasHead, err := prober.Inspect(context.Background(), dir)
	if err == nil || !strings.Contains(err.Error(), "Git execution failed") || hasHead || identity.Path != "" {
		t.Fatalf("failure must not become unborn: %+v %v %v", identity, hasHead, err)
	}
}

func TestReadinessProberBoundsSubprocessOutput(t *testing.T) {
	prober := &ReadinessProber{executable: "/bin/sh"}
	output, err := prober.run(context.Background(), "-c", "printf '%20000s' x")
	if err != nil {
		t.Fatal(err)
	}
	if len(output) != 16*1024 {
		t.Fatalf("subprocess output size = %d, want 16384", len(output))
	}
}
