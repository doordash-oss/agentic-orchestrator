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

//go:build linux

package clone

import (
	"os"
	"strconv"
	"strings"
)

// processStartIdentity returns the kernel process start time (field 22 of
// /proc/<pid>/stat, in clock ticks since boot) as an opaque identity token
// used to detect PID reuse before any recovery-time signaling.
func processStartIdentity(pid int) string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	// The comm field can contain spaces and parentheses; fields are
	// safely split only after the final ')'.
	idx := strings.LastIndexByte(string(data), ')')
	if idx < 0 || idx+2 > len(data) {
		return ""
	}
	fields := strings.Fields(string(data)[idx+2:])
	// fields[0] is state (field 3), so field N (1-based) is fields[N-3].
	if len(fields) < 20 {
		return ""
	}
	return fields[19]
}
