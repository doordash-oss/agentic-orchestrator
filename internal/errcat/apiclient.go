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

package errcat

import "strings"

// `agentico api` helper codes. The helper renders one of these on stderr
// when it cannot reach this machine's server through its discovery file.
// Their text names the runtime directory searched and never the token.
const (
	// DiscoveryMissing reports that the runtime directory holds no
	// discovery file.
	DiscoveryMissing Code = "discovery_missing"
	// DiscoveryUntrusted reports a discovery file that is not an
	// owner-only regular file owned by the caller, or that is unusable.
	DiscoveryUntrusted Code = "discovery_untrusted"
	// ServerUnreachable reports a transport failure reaching the server
	// the discovery file names.
	ServerUnreachable Code = "server_unreachable"
)

// RuntimeDirParams carries the runtime directory the helper searched for
// the discovery file.
type RuntimeDirParams struct {
	RuntimeDir string
}

func (RuntimeDirParams) params() {}

// runtimeDirTemplate renders format with the searched runtime directory,
// or "" when the params are absent or blank so the static text applies.
func runtimeDirTemplate(format string) func(Params) string {
	return func(p Params) string {
		params, ok := p.(RuntimeDirParams)
		if !ok || strings.TrimSpace(params.RuntimeDir) == "" {
			return ""
		}
		return strings.ReplaceAll(format, "{dir}", params.RuntimeDir)
	}
}
