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

package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	gitpkg "github.com/doordash-oss/agentic-orchestrator/internal/git"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
)

func TestServerMutationTargetCreateFeatureReturnsUnavailableBranchProbeWarning(t *testing.T) {
	runtimeDir := t.TempDir()
	repoPath := filepath.Join(runtimeDir, testRepoAName)
	initMutationGitRepo(t, repoPath)
	mutationGitOutput(t, repoPath, "remote", "add", "origin", filepath.Join(runtimeDir, "offline.git"))

	cfg := config.NewDefault()
	cfg.Repos[testRepoAName] = config.RepoConfig{Path: repoPath}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	manager.BranchProbeOptions.Runner = gitpkg.BranchProbeRunnerFunc(
		func(_ context.Context, _ string, args []string, _ int) gitpkg.BranchProbeCommandResult {
			if len(args) > 0 && args[0] == "show-ref" {
				return gitpkg.BranchProbeCommandResult{ExitCode: 1}
			}
			return gitpkg.BranchProbeCommandResult{
				ExitCode: 128, Diagnostics: "fatal: https://user:secret@example.invalid/repo unavailable\x1b[31m",
				Err: errors.New("exit status 128"),
			}
		},
	)
	target := newRESTCreateFeatureTarget(store, manager, cfg, filepath.Join(runtimeDir, "config.yaml"))

	response, err := target.CreateFeature(serverruntime.CreateFeatureRequest{
		Name: "Offline branch warning", Repos: []string{testRepoAName},
	})
	if err != nil {
		t.Fatalf("CreateFeature() error = %v", err)
	}
	if len(response.Warnings) != 1 {
		t.Fatalf("CreateFeature().Warnings = %+v, want one warning", response.Warnings)
	}
	warning := response.Warnings[0]
	if warning.Code != string(errcat.BranchCollisionProbeUnavailable) || warning.Class != serverruntime.ErrorClassWarning {
		t.Fatalf("warning = %+v, want %s warning class", warning, errcat.BranchCollisionProbeUnavailable)
	}
	if warning.Context == nil || len(warning.Context.Repositories) != 1 || warning.Context.Repositories[0].Name != testRepoAName {
		t.Fatalf("warning repository context = %+v, want %q", warning.Context, testRepoAName)
	}
	if strings.Contains(warning.Diagnostics, "secret") || strings.ContainsRune(warning.Diagnostics, '\x1b') {
		t.Fatalf("warning diagnostics were not redacted: %q", warning.Diagnostics)
	}
}
