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
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

// isReviewHelper reports whether opts.PermHandler indicates a bounded review
// or validator helper run. New bounded helpers use BoundedHelperArtifactHandler;
// a few legacy paths still use ReviewFeedbackHandler or ReadOnlyHandler. Tests
// treat all of them as "review session".
func isReviewHelper(h interface{}) bool {
	switch typed := h.(type) {
	case *permission.SizeGuardHandler:
		return isReviewHelper(typed.Inner)
	case *permission.BoundedHelperArtifactHandler:
		return true
	case *permission.ReviewFeedbackHandler:
		return true
	case *permission.ReadOnlyHandler:
		return true
	}
	return false
}

func requireOnlyAgenticoBinEnv(t *testing.T, env []string) string {
	t.Helper()
	if len(env) != 1 {
		t.Fatalf("env = %v, want only AGENTICO_BIN", env)
	}
	value, ok := strings.CutPrefix(env[0], "AGENTICO_BIN=")
	if !ok || value == "" {
		t.Fatalf("env = %v, want AGENTICO_BIN entry", env)
	}
	if !filepath.IsAbs(value) {
		t.Fatalf("AGENTICO_BIN = %q, want absolute path", value)
	}
	return value
}

// mockBuildSession returns a BuildSession function that returns mock bash
// scripts. Regular interactive runs use agentScript; bounded review-helper
// runs (identified by isReviewHelper) use reviewScript when provided.
func mockBuildSession(agentScript, reviewScript string) func(BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
	return func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		script := agentScript
		if isReviewHelper(opts.PermHandler) && reviewScript != "" {
			script = reviewScript
		}
		cmd := []string{"bash", script}
		sessOpts := &session.SessionOpts{
			PIDDir:        opts.PIDDir,
			PermHandler:   opts.PermHandler,
			InitialPrompt: opts.Prompt,
			RepoName:      opts.RepoName,
			LogPath:       opts.LogPath,
		}
		return cmd, nil, sessOpts, nil
	}
}

// capturingBuildSession returns a mockBuildSession that also captures
// all BuildSessionOpts passed to it for test assertions.
func capturingBuildSession(agentScript, reviewScript string) (
	func(BuildSessionOpts) ([]string, []string, *session.SessionOpts, error),
	*[]BuildSessionOpts,
) {
	var mu sync.Mutex
	var captured []BuildSessionOpts
	mock := mockBuildSession(agentScript, reviewScript)
	bs := func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		optsCopy := opts
		optsCopy.AllowedTools = append([]string(nil), opts.AllowedTools...)
		optsCopy.DisallowedTools = append([]string(nil), opts.DisallowedTools...)
		optsCopy.AdditionalDirs = append([]string(nil), opts.AdditionalDirs...)
		optsCopy.WritableRoots = append([]string(nil), opts.WritableRoots...)
		if opts.AgentNames != nil {
			optsCopy.AgentNames = append([]string{}, opts.AgentNames...)
		}
		mu.Lock()
		captured = append(captured, optsCopy)
		mu.Unlock()
		return mock(opts)
	}
	return bs, &captured
}

// mockBuildSessionByModel dispatches to different scripts based on opts.Model.
func mockBuildSessionByModel(scripts map[string]string) func(BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
	return func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		script := scripts[opts.Model]
		if script == "" {
			for _, s := range scripts {
				script = s
				break
			}
		}
		cmd := mockSessionCommand(script)
		sessOpts := &session.SessionOpts{
			PIDDir:        opts.PIDDir,
			PermHandler:   opts.PermHandler,
			InitialPrompt: opts.Prompt,
			RepoName:      opts.RepoName,
			LogPath:       opts.LogPath,
		}
		return cmd, nil, sessOpts, nil
	}
}

func mockSessionCommand(script string) []string {
	return []string{"bash", "-c", `read -r -t 5 _agentic_init || true; exec bash "$1"`, "agentic-mock-session", script}
}

// capturingBuildSessionByModel wraps mockBuildSessionByModel and captures all
// BuildSessionOpts passed to it.
func capturingBuildSessionByModel(scripts map[string]string) (
	func(BuildSessionOpts) ([]string, []string, *session.SessionOpts, error),
	*[]BuildSessionOpts,
) {
	var mu sync.Mutex
	var captured []BuildSessionOpts
	inner := mockBuildSessionByModel(scripts)
	wrapper := func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		optsCopy := opts
		optsCopy.AllowedTools = append([]string(nil), opts.AllowedTools...)
		optsCopy.DisallowedTools = append([]string(nil), opts.DisallowedTools...)
		optsCopy.AdditionalDirs = append([]string(nil), opts.AdditionalDirs...)
		optsCopy.WritableRoots = append([]string(nil), opts.WritableRoots...)
		if opts.AgentNames != nil {
			optsCopy.AgentNames = append([]string{}, opts.AgentNames...)
		}
		mu.Lock()
		captured = append(captured, optsCopy)
		mu.Unlock()
		return inner(opts)
	}
	return wrapper, &captured
}

func assertExplicitEmptyAgentNames(t *testing.T, got []string) {
	t.Helper()
	if !reflect.DeepEqual(got, []string{}) {
		t.Fatalf("AgentNames = %#v, want explicit empty []string{}", got)
	}
}

// assertExplorationAgentNames asserts a bounded helper (validator/reviewer) was
// given the shared exploration sub-agent set, matching the research-phase
// treatment.
func assertExplorationAgentNames(t *testing.T, got []string) {
	t.Helper()
	if !reflect.DeepEqual(got, explorationAgentNames()) {
		t.Fatalf("AgentNames = %#v, want exploration set %#v", got, explorationAgentNames())
	}
}

type fakeGitRunner struct {
	head string
	log  string
}

func (r *fakeGitRunner) Run(ctx context.Context, name string, args []string, opts ports.CommandOpts) ([]byte, error) {
	if name != "git" {
		return nil, fmt.Errorf("unexpected command %q", name)
	}
	if len(args) == 2 && args[0] == "rev-parse" && args[1] == "HEAD" {
		return []byte(r.head + "\n"), nil
	}
	if len(args) == 3 && args[0] == "log" && args[1] == "--oneline" {
		return []byte(r.log), nil
	}
	return nil, fmt.Errorf("unexpected git args %v in %s", args, opts.Dir)
}
