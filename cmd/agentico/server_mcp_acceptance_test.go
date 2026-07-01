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
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServerBinaryServesMCPDiscoveryAndTools(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake codex CLI fixture is POSIX shell based")
	}
	repoRoot := agenticoRepoRoot(t)
	bin := filepath.Join(t.TempDir(), "agentico")
	build := exec.Command("go", "build", "-o", bin, "./cmd/agentico")
	build.Dir = repoRoot
	build.Env = os.Environ()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/agentico error = %v\n%s", err, out)
	}

	runtimeDir := t.TempDir()
	configPath := filepath.Join(runtimeDir, "config.yaml")
	stateDir := filepath.Join(runtimeDir, "features")
	fakeBinDir := t.TempDir()
	writeFakeCodexCLI(t, fakeBinDir)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, bin,
		"server",
		"--config", configPath,
		"--state-dir", stateDir,
		"--providers", "codex",
		"--dangerously-skip-permissions",
	)
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(),
		"PATH="+fakeBinDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOME="+filepath.Join(runtimeDir, "home"),
		"CODEX_HOME="+filepath.Join(runtimeDir, "codex-home"),
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("agentico server start error = %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			select {
			case <-done:
			case <-time.After(time.Second):
			}
		}
	})

	rec := waitForDiscoveryRecord(t, runtimeDir, done, &stderr)
	if rec.MCP.Transport != "streamable_http" || rec.MCP.Path != serverruntime.MCPEndpointPath || rec.MCP.RESTAPIVersion != serverruntime.APIVersion {
		t.Fatalf("discovery MCP metadata = %+v; want streamable HTTP %s adapting REST %s", rec.MCP, serverruntime.MCPEndpointPath, serverruntime.APIVersion)
	}
	if rec.MCP.Endpoint != strings.TrimRight(rec.BaseURL, "/")+serverruntime.MCPEndpointPath {
		t.Fatalf("discovery MCP endpoint = %q; want base URL /mcp from %q", rec.MCP.Endpoint, rec.BaseURL)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "agentico-acceptance", Version: "v1.0.0"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:             rec.MCP.Endpoint,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("MCP initialize against built server error = %v\nserver stderr:\n%s", err, stderr.String())
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Fatalf("MCP session Close() error = %v", err)
		}
	})
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("MCP tools/list against built server error = %v", err)
	}
	if !mcpToolsContain(tools.Tools, "runtime_health_get") || !mcpToolsContain(tools.Tools, "feature_list") {
		t.Fatalf("MCP tools/list names = %v; want runtime_health_get and feature_list", mcpToolNames(tools.Tools))
	}
}

func agenticoRepoRoot(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	return filepath.Clean(filepath.Join(cwd, "..", ".."))
}

func writeFakeCodexCLI(t *testing.T, dir string) {
	t.Helper()
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "codex 0.116.0"
  exit 0
fi
if [ "$1" = "login" ] && [ "$2" = "status" ]; then
  echo "Logged in using test credentials"
  exit 0
fi
if [ "$1" = "debug" ] && [ "$2" = "models" ]; then
  cat <<'JSON'
{"models":[{"slug":"gpt-5.4","display_name":"GPT-5.4","visibility":"list","supported_in_api":true,"context_window":272000}]}
JSON
  exit 0
fi
echo "unexpected codex args: $*" >&2
exit 1
`
	path := filepath.Join(dir, "codex")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile(fake codex) error = %v", err)
	}
}

func waitForDiscoveryRecord(t *testing.T, runtimeDir string, done <-chan error, stderr *bytes.Buffer) serverruntime.DiscoveryRecord {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("agentico server exited before discovery: %v\nserver stderr:\n%s", err, stderr.String())
		default:
		}
		rec, err := serverruntime.ReadDiscovery(runtimeDir)
		if err == nil && rec.BaseURL != "" && rec.MCP.Endpoint != "" {
			return rec
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for discovery record: lastErr=%v\nserver stderr:\n%s", lastErr, stderr.String())
	return serverruntime.DiscoveryRecord{}
}

func mcpToolsContain(tools []*mcp.Tool, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func mcpToolNames(tools []*mcp.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}
