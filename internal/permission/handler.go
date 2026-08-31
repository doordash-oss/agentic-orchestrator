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

import "github.com/doordash-oss/agentic-orchestrator/internal/ports"

// CachingHandler wraps an inner PermissionHandler. When the inner handler
// defers a decision (empty Behavior), the CachingHandler checks cached rules.
type CachingHandler struct {
	Inner    ports.PermissionHandler
	Cache    *Cache
	RepoName string // repo scope for cache lookups
}

// CanUseTool implements ports.PermissionHandler.
func (h *CachingHandler) CanUseTool(req ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	// Let the inner handler decide first.
	decision, err := h.Inner.CanUseTool(req)
	if err != nil {
		return decision, err
	}
	// If the inner handler made a definitive decision, respect it.
	if decision.Behavior != "" {
		return decision, nil
	}
	// Inner handler deferred — check the cache.
	rule, found := h.Cache.Check(req.ToolName, req.Input, h.RepoName)
	if !found {
		return ports.PermissionDecision{}, nil // defer to desktop app
	}
	return ports.PermissionDecision{
		Behavior: rule.Effect,
		Reason:   "cached rule: " + rule.ToolPattern,
	}, nil
}
