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
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// testingContractHeadResolver resolves the immutable commit currently checked
// out in a repository worktree. Keeping this seam narrower than the general
// git adapters lets contract anchoring remain deterministic and cheap to test.
type testingContractHeadResolver func(worktreePath string) (string, error)

// EnsureTestingContractBaseCommits captures the repository HEAD for every
// contract repository that does not already have an anchor. Existing anchors
// are immutable: later implementation iterations preserve them even when the
// candidate worktree HEAD advances.
//
// Updates are transactional. If any missing anchor cannot be resolved, the
// contract is left unchanged rather than recording a partially anchored
// multi-repository verification scope.
func EnsureTestingContractBaseCommits(contract *TestingContract, repos []feature.FeatureRepo, resolver testingContractHeadResolver) error {
	if contract == nil {
		return fmt.Errorf("ensuring testing contract base commits: contract is nil")
	}
	if resolver == nil {
		resolver = resolveTestingContractWorktreeHEAD
	}

	baseCommits := make(map[string]string, len(contract.BaseCommits)+len(repos))
	for repo, commit := range contract.BaseCommits {
		baseCommits[repo] = commit
	}

	for _, repo := range repos {
		name := strings.TrimSpace(repo.Name)
		if name == "" {
			return fmt.Errorf("ensuring testing contract base commits: repository name is empty")
		}
		if strings.TrimSpace(baseCommits[name]) != "" {
			continue
		}

		worktreePath := strings.TrimSpace(repo.WorktreePath)
		if worktreePath == "" {
			return fmt.Errorf("ensuring testing contract base commits: repository %q has no worktree path", name)
		}
		commit, err := resolver(worktreePath)
		if err != nil {
			return fmt.Errorf("ensuring testing contract base commits: resolving HEAD for repository %q: %w", name, err)
		}
		commit = strings.TrimSpace(commit)
		if commit == "" {
			return fmt.Errorf("ensuring testing contract base commits: resolving HEAD for repository %q returned an empty commit", name)
		}
		baseCommits[name] = commit
	}

	if len(baseCommits) > 0 {
		contract.BaseCommits = baseCommits
	}
	return nil
}

func resolveTestingContractWorktreeHEAD(worktreePath string) (string, error) {
	return resolveTestingContractWorktreeHEADWithRunner(NewExecCommandRunner(), worktreePath)
}

func resolveTestingContractWorktreeHEADWithRunner(runner ports.CommandRunner, worktreePath string) (string, error) {
	if runner == nil {
		return "", fmt.Errorf("git command runner is nil")
	}
	var stderr bytes.Buffer
	out, err := runner.Run(
		context.Background(),
		"git",
		[]string{"-C", worktreePath, "rev-parse", "--verify", "HEAD^{commit}"},
		ports.CommandOpts{Stderr: &stderr},
	)
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(string(out))
		}
		if detail == "" {
			return "", fmt.Errorf("git rev-parse HEAD: %w", err)
		}
		return "", fmt.Errorf("git rev-parse HEAD: %s: %w", detail, err)
	}
	commit := strings.TrimSpace(string(out))
	if commit == "" {
		return "", fmt.Errorf("git rev-parse HEAD returned empty output")
	}
	return commit, nil
}
