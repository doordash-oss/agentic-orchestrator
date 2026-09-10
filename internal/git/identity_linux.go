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
	"strconv"

	"golang.org/x/sys/unix"
)

// StatRepoDirectory reads the stable filesystem identity of a Git common
// directory. It also supports staging directories before clone publication.
func StatRepoDirectory(path string) (uint64, uint64, string, error) {
	var statx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, 0, unix.STATX_BASIC_STATS|unix.STATX_BTIME, &statx); err == nil && statx.Mask&unix.STATX_INO != 0 {
		birth := ""
		if statx.Mask&unix.STATX_BTIME != 0 {
			birth = strconv.FormatInt(statx.Btime.Sec, 10) + ":" + strconv.FormatUint(uint64(statx.Btime.Nsec), 10)
		}
		return unix.Mkdev(statx.Dev_major, statx.Dev_minor), statx.Ino, birth, nil
	}
	// Older kernels and filesystems without birth-time support retain the
	// device/inode identity. Never use ctime: normal Git operations change it.
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		return 0, 0, "", err
	}
	return uint64(stat.Dev), uint64(stat.Ino), "", nil
}
