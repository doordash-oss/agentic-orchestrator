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
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		return 0, 0, "", err
	}
	birth := strconv.FormatInt(stat.Btim.Sec, 10) + ":" + strconv.FormatInt(stat.Btim.Nsec, 10)
	return uint64(stat.Dev), uint64(stat.Ino), birth, nil
}
