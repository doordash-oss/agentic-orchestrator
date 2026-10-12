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

package testutil

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
)

const sandboxExecBinary = "/usr/bin/sandbox-exec"

var (
	sandboxExecProbeOnce   sync.Once
	sandboxExecProbeReason string
)

// RequireSandboxExec skips t when this process cannot apply a macOS seatbelt
// sandbox — typically because it already runs inside one (Agentico's own QA
// validator runs `make test-fast` sandboxed, and sandbox-exec nesting fails
// with exit 71 "sandbox_apply: Operation not permitted"). The probe runs once
// per process. It is a no-op off darwin and when sandbox-exec is absent, so
// tests still run in full on dev machines and CI.
func RequireSandboxExec(t testing.TB) {
	t.Helper()
	if reason := sandboxExecUnusable(); reason != "" {
		t.Skipf("sandbox-exec unavailable in this environment (nested sandbox?): %s", reason)
	}
}

func sandboxExecUnusable() string {
	sandboxExecProbeOnce.Do(func() {
		if runtime.GOOS != "darwin" {
			return
		}
		if _, err := os.Stat(sandboxExecBinary); err != nil {
			return
		}
		out, err := exec.Command(sandboxExecBinary, "-p", "(version 1)(allow default)", "/usr/bin/true").CombinedOutput()
		if err != nil {
			sandboxExecProbeReason = strings.TrimSpace(err.Error() + " " + string(out))
		}
	})
	return sandboxExecProbeReason
}
