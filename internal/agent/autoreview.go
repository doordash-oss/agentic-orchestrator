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
	"context"

	"github.com/doordash-oss/agentic-orchestrator/internal/autoreview"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// autoReviewBashToolName is the canonical Bash tool name the decorator matches.
const autoReviewBashToolName = "Bash"

// autoReviewEligibleCommands is the exact, byte-for-byte set of Bash commands
// the automatic Bash-review decorator may send to a reviewer. The command
// extracted from the canonical Bash input is compared without trimming,
// normalizing, splitting, or variant tolerance.
var autoReviewEligibleCommands = map[string]bool{
	"go test ./...":      true,
	"git status --short": true,
}

func autoReviewEligibleCommand(command string) bool {
	return autoReviewEligibleCommands[command]
}

// autoReviewPermissionDecorator wraps the fully composed session permission
// policy. It asks the existing handler first and returns every non-empty
// decision or existing error unchanged. Only an empty decision for canonical
// Bash, when the session's snapshotted flag is enabled and reviewer is usable,
// permits a single hidden classification. A successful ALLOW is the only new
// automatic decision; DEFER and every failure return the same empty
// human-deferral decision. The decorator sits outside the CachingHandler, so
// its allow bypasses the cache and creates no remembered rule, cache entry, or
// audit event. The hidden reviewer is launched via autoreview.Classify (not
// BuildSession), so it is never decorated and cannot recurse.
type autoReviewPermissionDecorator struct {
	inner         ports.PermissionHandler
	reviewer      autoreview.Reviewer
	workDir       string
	writableRoots []string
}

func (d *autoReviewPermissionDecorator) CanUseTool(req ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	decision, err := d.inner.CanUseTool(req)
	if err != nil {
		return decision, err
	}
	if decision.Behavior != "" {
		return decision, nil
	}
	if d.reviewer.Provider == nil || req.ToolName != autoReviewBashToolName {
		return decision, nil
	}
	command := permission.ExtractBashCommand(req.Input)
	if !autoReviewEligibleCommand(command) {
		return decision, nil
	}
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	result, ok := autoreview.Classify(ctx, d.reviewer, autoreview.ClassifyRequest{
		ToolName:      req.ToolName,
		Command:       command,
		WorkDir:       d.workDir,
		WritableRoots: d.writableRoots,
	})
	if !ok || result != autoreview.Allow {
		return decision, nil
	}
	return ports.PermissionDecision{Behavior: permission.DecisionAllow}, nil
}
