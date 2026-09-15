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

//go:build darwin

package clone

import (
	"strconv"

	"golang.org/x/sys/unix"
)

// processStartIdentity returns the process start time (from
// kern.proc.pid) as an opaque identity token used to detect PID reuse
// before any recovery-time signaling.
func processStartIdentity(pid int) string {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return ""
	}
	start := info.Proc.P_starttime
	return strconv.FormatInt(int64(start.Sec), 10) + ":" + strconv.FormatInt(int64(start.Usec), 10)
}
