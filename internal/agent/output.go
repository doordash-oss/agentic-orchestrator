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
	"os"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// readSessionOutput reads the output from a completed session using a 3-tier
// fallback: response file -> assistant text -> last result message.
func readSessionOutput(responsePath string, sess ports.SessionHandle) string {
	if data, err := os.ReadFile(responsePath); err == nil && len(data) > 0 {
		return string(data)
	}
	if text := sess.MessageLog().AssistantText(); text != "" {
		return text
	}
	if rm := sess.MessageLog().LastResultMessage(); rm != nil && rm.Result != "" {
		return rm.Result
	}
	return ""
}
