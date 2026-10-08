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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/claudeconfig"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/claude"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/internal/workadmission"
)

const (
	claudeLiveTokenEnv     = "CLAUDE_CODE_OAUTH_TOKEN"
	claudeLiveTokenFileEnv = "AGENTIC_CLAUDE_LIVE_TOKEN_FILE"
)

// tokenClaudeProvider is the installed Claude CLI with an OAuth token added
// to the spawned child's environment only. Under a temporary
// CLAUDE_CONFIG_DIR the CLI cannot reach the login stored for the user's
// real configuration, and the token never enters this process's
// environment, logs or test output.
type tokenClaudeProvider struct {
	*claude.Provider
	token string
}

func (p tokenClaudeProvider) BuildCommand(opts llm.CommandBuildOpts) ([]string, []string, error) {
	cmd, env, err := p.Provider.BuildCommand(opts)
	if err != nil {
		return nil, nil, err
	}
	return cmd, append(env, claudeLiveTokenEnv+"="+p.token), nil
}

// claudeLiveToken returns the OAuth token from CLAUDE_CODE_OAUTH_TOKEN, else
// from the 0600 file AGENTIC_CLAUDE_LIVE_TOKEN_FILE names (default
// ~/.config/agentico/claude-live-oauth-token). ok=false means neither
// source exists.
func claudeLiveToken(t *testing.T) (token string, ok bool) {
	t.Helper()
	if token := strings.TrimSpace(os.Getenv(claudeLiveTokenEnv)); token != "" {
		return token, true
	}
	path := os.Getenv(claudeLiveTokenFileEnv)
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		path = filepath.Join(home, ".config", "agentico", "claude-live-oauth-token")
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false
	}
	if err != nil {
		t.Fatalf("stat Claude live token file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("Claude live token file %s must be mode 0600, is %o", path, info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Claude live token file: %v", err)
	}
	token = strings.TrimSpace(string(data))
	if token == "" {
		t.Fatalf("Claude live token file %s is empty", path)
	}
	return token, true
}

// TestClaudeResumeLive proves the installed Claude CLI accepts a native
// session rebuilt from the durable transcript: a first generation is told a
// fact, the process ends, and the second generation resumes the rebuilt
// session under the pre-assigned id and answers from that history. All
// Claude state lives under a temporary CLAUDE_CONFIG_DIR.
func TestClaudeResumeLive(t *testing.T) {
	if testing.Short() || (os.Getenv("AGENTIC_CLAUDE_LIVE") != "1" && os.Getenv("AGENTIC_CLAUDE_RESUME_LIVE") != "1") {
		t.Skip("set AGENTIC_CLAUDE_LIVE=1 without -short to resume a live Claude against a rebuilt session")
	}
	token, ok := claudeLiveToken(t)
	if !ok {
		t.Skip("set CLAUDE_CODE_OAUTH_TOKEN or place a 0600 token file at $AGENTIC_CLAUDE_LIVE_TOKEN_FILE (default ~/.config/agentico/claude-live-oauth-token)")
	}
	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	configDir := t.TempDir()
	t.Setenv(claudeconfig.EnvConfigDir, configDir)

	provider := claudeCatalogProvider(t, "claude")
	registry := llm.NewRegistry()
	registry.Register(tokenClaudeProvider{Provider: provider, token: token})
	model := ""
	for _, id := range registry.EligibleModelsForPhase(llm.PhaseChat)["claude"] {
		if strings.Contains(strings.ToLower(id), "haiku") {
			model = id
			break
		}
	}
	if model == "" {
		t.Fatalf("no chat-eligible Haiku model in %v", registry.EligibleModelsForPhase(llm.PhaseChat)["claude"])
	}

	h := &supervisorHarness{t: t, runtimeDir: t.TempDir(), claudeConfigDir: configDir, harness: "claude", registry: registry, model: model}
	h.stateDir = filepath.Join(h.runtimeDir, "state")
	if err := os.MkdirAll(h.stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	h.sessions = session.NewManager(nil)
	h.store = feature.NewStore(h.stateDir)
	h.runner = agent.NewPhaseRunner(h.sessions, h.store, h.stateDir)
	h.runner.Registry = registry
	h.runner.Config = config.NewDefault()
	h.runner.SkillsDir = filepath.Join(h.runtimeDir, "skills")
	h.admission = workadmission.New(workadmission.Options{})
	h.start()
	t.Cleanup(func() {
		h.stopServer()
		_ = h.coord.Close()
		h.sessions.Shutdown()
	})

	var nonce [4]byte
	_, _ = rand.Read(nonce[:])
	codeword := "periwinkle-" + hex.EncodeToString(nonce[:])
	h.chooseSettings()
	h.send("Remember this for later: the secret codeword is "+codeword+". Reply with just OK.", "live-1")
	h.waitLive("first reply", func(st server.SupervisorState) bool { return st.Lifecycle == server.SupervisorLifecycleIdle })
	h.do(http.MethodPost, "/api/v1/supervisor/end", map[string]any{}, http.StatusOK, nil)
	h.waitLive("stopped", func(st server.SupervisorState) bool { return st.Lifecycle == server.SupervisorLifecycleStopped })

	h.send("What is the secret codeword I told you earlier? Reply with just the codeword.", "live-2")
	st := h.waitLive("resumed reply", func(st server.SupervisorState) bool {
		return st.Lifecycle == server.SupervisorLifecycleIdle && st.LastTurnOutcome != server.SupervisorTurnOutcomeNone
	})
	if st.LastTurnOutcome != server.SupervisorTurnOutcomeCompleted {
		t.Fatalf("resumed turn outcome = %s", st.LastTurnOutcome)
	}
	items := h.transcript("").Items
	reply := ""
	for _, rec := range items {
		if rec.Kind == server.SupervisorRecordKindAssistant && rec.Generation == st.Generation {
			reply += recordText(rec)
		}
	}
	if !strings.Contains(reply, codeword) {
		t.Fatalf("resumed reply %q does not contain the remembered codeword %q", reply, codeword)
	}

	native := h.nativeSessionID()
	if native == "" {
		t.Fatal("no native session id was persisted")
	}
	if got := initSessionID(t, h.generationDir(st)); got != native {
		t.Fatalf("resumed init session id = %q, want the pre-assigned %q", got, native)
	}
	if _, err := os.Stat(h.nativeSessionFile(native)); err != nil {
		t.Fatalf("rebuilt session file under the temporary config dir: %v", err)
	}
	// The real configuration never received the rebuilt project directory.
	realProjects := claudeconfig.ProjectsDir(filepath.Join(realHome, ".claude"), h.stateDir)
	if _, err := os.Stat(realProjects); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a session was written under the real Claude configuration: %s (%v)", realProjects, err)
	}
}

// waitLive polls the state with a deadline sized for live inference.
func (h *supervisorHarness) waitLive(what string, cond func(server.SupervisorState) bool) server.SupervisorState {
	h.t.Helper()
	deadline := time.Now().Add(4 * time.Minute)
	for {
		st := h.state()
		if cond(st) {
			return st
		}
		if st.Lifecycle == server.SupervisorLifecycleFailed {
			h.t.Fatalf("supervisor failed while waiting for %s: %+v", what, st.Failure)
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s; last state %+v", what, st)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// nativeSessionID reads the conversation's persisted native session id.
func (h *supervisorHarness) nativeSessionID() string {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.stateDir, "supervisor", "conversation.json"))
	if err != nil {
		h.t.Fatal(err)
	}
	var conv struct {
		NativeSessionID string `json:"native_session_id"`
	}
	if err := json.Unmarshal(data, &conv); err != nil {
		h.t.Fatal(err)
	}
	return conv.NativeSessionID
}

// initSessionID reads the session id the running harness reported at init,
// which the session records in the generation's PID file.
func initSessionID(t *testing.T, genDir string) string {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(genDir, "session*.pid"))
	if len(matches) != 1 {
		t.Fatalf("PID files in %s = %v", genDir, matches)
	}
	pf, err := session.ReadPIDFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	return pf.SessionID
}
