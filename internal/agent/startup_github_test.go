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
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
)

func isolateGitHubAuth(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("GH_PATH", "/nonexistent/gh")
}

func TestCheckRequiredToolsWarnsWithoutGitHubCredentials(t *testing.T) {
	isolateGitHubAuth(t)
	hard, soft := checkRequiredTools(func(string) (string, error) {
		return "/usr/bin/git", nil
	})
	if len(hard) != 0 {
		t.Fatalf("hard = %v; want none when git is present", hard)
	}
	found := false
	for _, issue := range soft {
		if issue.Code == errcat.GithubCredentialsMissing {
			found = true
		}
	}
	if !found {
		t.Fatalf("soft = %v; want a GitHub credentials warning", soft)
	}
}

func TestCheckRequiredToolsAcceptsEnvToken(t *testing.T) {
	isolateGitHubAuth(t)
	t.Setenv("GH_TOKEN", "env-token")
	_, soft := checkRequiredTools(func(string) (string, error) {
		return "/usr/bin/git", nil
	})
	for _, issue := range soft {
		if issue.Code == errcat.GithubCredentialsMissing {
			t.Fatalf("soft = %v; want no credentials warning with GH_TOKEN set", soft)
		}
	}
}
