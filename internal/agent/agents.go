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

import "github.com/doordash-oss/agentic-orchestrator/internal/agentdef"

// agentsJSONForNames returns the JSON string for the --agents CLI flag, built
// from the selected embedded agent markdown files.
func agentsJSONForNames(agentNames []string) (string, error) {
	return agentdef.JSONForNames(agentNames)
}
