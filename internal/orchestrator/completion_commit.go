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

package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// commitSingleShotOutcome resolves the phase contract, enforces read-only
// repository boundaries, validates the artifacts, and finally publishes the
// harness-owned completion receipt.
func (o *Orchestrator) commitSingleShotOutcome(
	featureID string,
	sessionID string,
	phase feature.Phase,
	intent llm.CompletionIntent,
) ([]agent.ProtocolViolation, error) {
	if o == nil || o.deps.Lifecycle == nil {
		return nil, fmt.Errorf("committing %s outcome: lifecycle is not configured", phase)
	}
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return nil, fmt.Errorf("committing %s outcome: loading feature: %w", phase, err)
	}

	role, artifactDir, repoNames, err := o.singleShotCompletionContract(f, sessionID, phase)
	if err != nil {
		return nil, err
	}
	mutationViolations, err := agent.EnforceReadOnlyRepoMutations(
		context.Background(),
		o.deps.CmdRunner,
		f,
		phase,
		artifactDir,
		repoNames...,
	)
	if err != nil {
		return nil, fmt.Errorf("committing %s outcome: enforcing read-only repositories: %w", phase, err)
	}
	if len(mutationViolations) > 0 {
		return mutationViolations, nil
	}

	_, _, violations, err := agent.CommitPhaseOutcome(agent.CompletionCommitInput{
		Phase:       phase,
		Role:        role,
		ArtifactDir: artifactDir,
		SessionID:   sessionID,
		Intent:      intent,
	})
	return violations, err
}

func (o *Orchestrator) singleShotCompletionContract(
	f *feature.Feature,
	sessionID string,
	phase feature.Phase,
) (agent.Role, string, []string, error) {
	baseDir := o.stateDir()
	if baseDir == "" {
		return "", "", nil, fmt.Errorf("resolving %s completion contract: state directory is empty", phase)
	}
	switch phase {
	case feature.PhaseKnowledgeBase:
		repoName := o.repoNameForKBSession(sessionID)
		if repoName == "" {
			return "", "", nil, fmt.Errorf("resolving knowledge base completion contract: repo name is missing from session %q", sessionID)
		}
		if f.IsChild() {
			return agent.RoleKnowledgeBaseBuilder, feature.ChildKBWorkspaceDir(baseDir, f.ID, repoName), []string{repoName}, nil
		}
		return agent.RoleKnowledgeBaseBuilder, agent.KBStateDir(baseDir, repoName), []string{repoName}, nil
	case feature.PhaseInquire, feature.PhaseResearch, feature.PhaseDesign:
		role, ok := artifactPhaseRole(phase)
		if !ok {
			return "", "", nil, fmt.Errorf("resolving %s completion contract: role is not registered", phase)
		}
		dir := filepath.Join(agent.ActiveRunDir(baseDir, f), phase.DirName())
		return role, dir, nil, nil
	default:
		return "", "", nil, fmt.Errorf("resolving completion contract: phase %s is not single-shot", phase)
	}
}

func (o *Orchestrator) repoNameForKBSession(sessionID string) string {
	if o != nil && o.deps.Sessions != nil {
		if sess := o.deps.Sessions.GetSession(sessionID); sess != nil && sess.RepoName() != "" {
			return sess.RepoName()
		}
	}
	return agent.RepoNameFromKBSession(sessionID)
}
