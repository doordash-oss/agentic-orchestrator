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

package session

import (
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// PermissionHandler, ToolPermissionRequest, and PermissionDecision alias the
// canonical port types. Session keeps the aliases so existing callers compile.
type (
	PermissionHandler     = ports.PermissionHandler
	ToolPermissionRequest = ports.ToolPermissionRequest
	PermissionDecision    = ports.PermissionDecision
)

// Concrete handlers now live in internal/permission. These aliases preserve
// source compatibility for tests and callers that still reference session.*
// directly, without forcing a second migration. Domain code should prefer
// importing the permission package directly.
type (
	AutoApproveHandler = permission.AutoApproveHandler
	AcceptEditsHandler = permission.AcceptEditsHandler
	AMAHandler         = permission.AMAHandler
	ReadOnlyHandler    = permission.ReadOnlyHandler
	DenyAllHandler     = permission.DenyAllHandler
)
