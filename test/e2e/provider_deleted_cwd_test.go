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

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/claude"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/codex"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/opencode"
)

// The server can outlive the temporary directory it was launched from. Exercise
// the real provider subprocess paths, including Claude's streaming catalog.
func TestProviderProbesRecoverDeletedWorkingDirectory(t *testing.T) {
	if testing.Short() {
		t.Skip("launches provider CLI subprocesses")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	script := `#!/bin/sh
# Fail like real CLIs when their inherited working directory was removed.
probe_cwd=$(/bin/pwd -P) || exit 1
[ "$probe_cwd" = "$AGENTICO_PROBE_EXPECTED_CWD" ] || exit 1
case "$1" in
 --version) echo '2.1.100' ;;
 auth) echo '{"loggedIn":true,"authMethod":"oauth"}' ;;
 login) echo 'Logged in using ChatGPT' >&2 ;;
 models) echo 'anthropic/claude-sonnet-4-5' ;;
 -p)
  read -r request
  echo '{"type":"control_response","response":{"request_id":"agentico-model-catalog","subtype":"success","response":{"models":[{"value":"sonnet","displayName":"Sonnet"}]}}}'
  ;;
 *) exit 2 ;;
esac
`
	binary := filepath.Join(home, "provider-stub")
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	launchDir := filepath.Join(home, "launch")
	if err := os.Mkdir(launchDir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(launchDir)

	providers := []llm.LLMProvider{claude.NewProvider(binary), codex.NewProvider(binary), opencode.New(binary, nil)}
	for _, deleted := range []bool{false, true} {
		name, expectedDir := "existing", launchDir
		if deleted {
			name, expectedDir = "deleted", home
			if err := os.Remove(launchDir); err != nil {
				t.Fatal(err)
			}
		}
		expectedDir, err := filepath.EvalSymlinks(expectedDir)
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("AGENTICO_PROBE_EXPECTED_CWD", expectedDir)
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			for _, p := range providers {
				t.Run(p.Name(), func(t *testing.T) {
					if version, err := p.VersionInfo(); err != nil || version != "2.1.100" {
						t.Errorf("version = %q, err = %v", version, err)
					}
					if status := p.CheckReadiness(ctx); !status.Ready {
						t.Errorf("readiness = %+v", status)
					}
				})
			}
			models, err := providers[0].(*claude.Provider).DiscoverModelCatalog(ctx)
			if err != nil || len(models) != 1 {
				t.Errorf("Claude catalog = %+v, err = %v", models, err)
			}
			models, err = providers[2].(*opencode.Provider).DiscoverModelCatalog(ctx)
			if err != nil || len(models) != 1 {
				t.Errorf("OpenCode catalog = %+v, err = %v", models, err)
			}
		})
	}
	// Recovery applies only to children; never mutate the server's global cwd.
	if cwd, err := os.Getwd(); err == nil {
		if _, err := os.Stat(cwd); err == nil {
			t.Fatal("provider probes changed the server working directory")
		}
	}
}
