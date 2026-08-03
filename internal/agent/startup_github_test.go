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
	"strings"
	"testing"
)

func isolateGitHubAuth(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("GH_PATH", "/nonexistent/gh")
}

func TestCheckRequiredToolsWarnsWithoutGitHubCredentials(t *testing.T) {
	isolateGitHubAuth(t)
	_, warnings := CheckRequiredTools()
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "no GitHub credentials") {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings = %v; want a no-GitHub-credentials warning", warnings)
	}
}

func TestCheckRequiredToolsAcceptsEnvToken(t *testing.T) {
	isolateGitHubAuth(t)
	t.Setenv("GH_TOKEN", "env-token")
	_, warnings := CheckRequiredTools()
	for _, w := range warnings {
		if strings.Contains(w, "GitHub credentials") {
			t.Fatalf("warnings = %v; want no credentials warning with GH_TOKEN set", warnings)
		}
	}
}
