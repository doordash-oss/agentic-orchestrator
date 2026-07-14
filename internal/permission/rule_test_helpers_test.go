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

package permission

// Bash tool-pattern literals shared across test files.
const (
	patternBashLSExact = "Bash(ls -la)"
	patternBashRm      = "Bash(rm *)"
	patternBashEcho    = "Bash(echo *)"
	patternBashNpmTest = "Bash(npm test *)"
	patternBashGoTest  = "Bash(go test *)"
)
