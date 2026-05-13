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

// Package agents provides embedded agent definitions shipped with the binary.
// These markdown files define subagent personas (description, tools, model,
// system prompt) that are passed to the Claude CLI via the --agents flag.
package agents

import "embed"

// FS contains all agent definition markdown files.
//
//go:embed *.md
var FS embed.FS
