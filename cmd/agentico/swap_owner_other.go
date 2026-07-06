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

//go:build !unix

package main

import "io/fs"

// fileOwnerIDs reports no ownership information on non-unix platforms, where the
// binary swap falls back to default ownership. The release matrix is unix-only;
// this keeps the package buildable for any GOOS without a Windows-specific path.
func fileOwnerIDs(_ fs.FileInfo) (uid, gid int, ok bool) {
	return 0, 0, false
}
