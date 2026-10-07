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
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm/clirun"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/opencode"
)

const (
	openCodeLiveEnvVar      = "AGENTIC_OPENCODE_LIVE"
	openCodeLiveModelEnvVar = "AGENTIC_OPENCODE_LIVE_MODEL"
	openCodeMinBinEnvVar    = "AGENTIC_OPENCODE_MIN_BIN"
	openCodeServerUser      = "opencode"
)

// openCodeMinVersion is the provider's enforced minimum as "x.y.z".
func openCodeMinVersion() string {
	v := (&opencode.Provider{}).MinVersion()
	return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
}

// openCodeMinBinCachePath is where `make opencode-min-bin` puts the pinned
// minimum release, relative to the repo root.
func openCodeMinBinCachePath(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, ".cache", "opencode", openCodeMinVersion(), "opencode")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root (go.mod) not found above the test directory")
		}
		dir = parent
	}
}

// resolveOpenCodeMinBin returns the pinned-minimum binary, from the override
// variable or the repo cache, or "" when neither holds one.
func resolveOpenCodeMinBin(t *testing.T) string {
	t.Helper()
	if bin := strings.TrimSpace(os.Getenv(openCodeMinBinEnvVar)); bin != "" {
		return bin
	}
	bin := openCodeMinBinCachePath(t)
	if _, err := os.Stat(bin); err != nil {
		return ""
	}
	return bin
}

func openCodeMinBinFetchHint() string {
	return fmt.Sprintf("fetch it with `make opencode-min-bin` (OpenCode %s from its GitHub release into .cache/opencode/) or point %s at an OpenCode %s binary",
		openCodeMinVersion(), openCodeMinBinEnvVar, openCodeMinVersion())
}

func openCodeBinaryVersion(t *testing.T, bin string, env []string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--version")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return clirun.ParseVersionOutput(out)
}

// openCodeSandbox is a temporary XDG layout holding copies of the user's
// OpenCode auth file and config, so a live run writes nothing to the real
// OpenCode home.
type openCodeSandbox struct {
	root string
	env  []string
}

func newOpenCodeSandbox(t *testing.T, copyUserState bool) *openCodeSandbox {
	t.Helper()
	root := t.TempDir()
	dirs := map[string]string{
		"XDG_DATA_HOME":   filepath.Join(root, "data"),
		"XDG_CONFIG_HOME": filepath.Join(root, "config"),
		"XDG_CACHE_HOME":  filepath.Join(root, "cache"),
		"XDG_STATE_HOME":  filepath.Join(root, "state"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(d, "opencode"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if copyUserState {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		realData := xdgDir("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
		realConfig := xdgDir("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
		copyIfExists(t, filepath.Join(realData, "opencode", "auth.json"), filepath.Join(dirs["XDG_DATA_HOME"], "opencode", "auth.json"))
		for _, name := range []string{"opencode.json", "opencode.jsonc", "config.json"} {
			copyIfExists(t, filepath.Join(realConfig, "opencode", name), filepath.Join(dirs["XDG_CONFIG_HOME"], "opencode", name))
		}
	}
	env := make([]string, 0, len(os.Environ())+len(dirs))
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if _, overridden := dirs[key]; overridden || strings.HasPrefix(key, "OPENCODE_") {
			continue
		}
		env = append(env, kv)
	}
	for k, v := range dirs {
		env = append(env, k+"="+v)
	}
	return &openCodeSandbox{root: root, env: env}
}

func xdgDir(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func copyIfExists(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// openCodeLiveModel resolves the live model: the override variable, else the
// CLI's configured small_model, else model. It skips when none is configured.
func openCodeLiveModel(t *testing.T, bin string, env []string) string {
	t.Helper()
	if m := strings.TrimSpace(os.Getenv(openCodeLiveModelEnvVar)); m != "" {
		return m
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "debug", "config")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s debug config: %v", bin, err)
	}
	var cfg struct {
		Model      string `json:"model"`
		SmallModel string `json:"small_model"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("decoding %s debug config: %v", bin, err)
	}
	if cfg.SmallModel != "" {
		return cfg.SmallModel
	}
	if cfg.Model != "" {
		return cfg.Model
	}
	t.Skipf("OpenCode has no configured small_model or model; set %s", openCodeLiveModelEnvVar)
	return ""
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// rawACP is an `opencode acp` process driven over raw JSON-RPC on stdio, with
// the embedded HTTP server pinned to a chosen loopback port and password.
type rawACP struct {
	t        *testing.T
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	baseURL  string
	password string
	exited   chan struct{}
	stderr   bytes.Buffer

	mu      sync.Mutex
	lines   []string
	nextID  int
	waiters map[int]chan json.RawMessage
	// onRequest answers agent-to-client requests; nil rejects them.
	onRequest func(method string, params json.RawMessage) any
	// onUpdate observes session/update notifications.
	onUpdate func(params json.RawMessage)
}

func startRawACP(t *testing.T, bin string, env []string, workDir string, overlay map[string]any) *rawACP {
	t.Helper()
	port := freeLoopbackPort(t)
	password := randomHex(t, 16)
	runEnv := append([]string{}, env...)
	runEnv = append(runEnv, "OPENCODE_SERVER_PASSWORD="+password)
	if overlay != nil {
		data, err := json.Marshal(overlay)
		if err != nil {
			t.Fatal(err)
		}
		runEnv = append(runEnv, "OPENCODE_CONFIG_CONTENT="+string(data))
	}
	cmd := exec.Command(bin, "acp", "--port", fmt.Sprint(port), "--hostname", "127.0.0.1")
	cmd.Dir = workDir
	cmd.Env = runEnv
	a := &rawACP{
		t:        t,
		cmd:      cmd,
		baseURL:  fmt.Sprintf("http://127.0.0.1:%d", port),
		password: password,
		exited:   make(chan struct{}),
		waiters:  map[int]chan json.RawMessage{},
	}
	cmd.Stderr = &lockedWriter{mu: &a.mu, w: &a.stderr}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	a.stdin = stdin
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go a.readLoop(stdout)
	go func() { _ = cmd.Wait(); close(a.exited) }()
	t.Cleanup(func() {
		_ = a.stdin.Close()
		select {
		case <-a.exited:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-a.exited
		}
	})
	return a
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func (a *rawACP) readLoop(stdout io.Reader) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Text()
		a.mu.Lock()
		a.lines = append(a.lines, "<- "+line)
		a.mu.Unlock()
		var env struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(line), &env) != nil {
			continue
		}
		hasID := len(env.ID) > 0 && string(env.ID) != "null"
		switch {
		case hasID && env.Method == "":
			var id int
			if json.Unmarshal(env.ID, &id) != nil {
				continue
			}
			a.mu.Lock()
			ch := a.waiters[id]
			delete(a.waiters, id)
			a.mu.Unlock()
			if ch != nil {
				if len(env.Error) > 0 && string(env.Error) != "null" {
					ch <- json.RawMessage(`{"__error":` + string(env.Error) + `}`)
				} else {
					ch <- env.Result
				}
			}
		case hasID:
			a.mu.Lock()
			handler := a.onRequest
			a.mu.Unlock()
			var result any = map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
			if handler != nil {
				result = handler(env.Method, env.Params)
			}
			a.write(map[string]any{"jsonrpc": "2.0", "id": env.ID, "result": result})
		case env.Method == "session/update":
			a.mu.Lock()
			handler := a.onUpdate
			a.mu.Unlock()
			if handler != nil {
				handler(env.Params)
			}
		}
	}
}

func (a *rawACP) write(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		a.t.Errorf("marshal ACP message: %v", err)
		return
	}
	a.mu.Lock()
	a.lines = append(a.lines, "-> "+string(data))
	a.mu.Unlock()
	_, _ = a.stdin.Write(append(data, '\n'))
}

// call sends a request and returns a channel that receives its result (or a
// {"__error": ...} wrapper).
func (a *rawACP) call(method string, params any) <-chan json.RawMessage {
	ch := make(chan json.RawMessage, 1)
	a.mu.Lock()
	a.nextID++
	id := a.nextID
	a.waiters[id] = ch
	a.mu.Unlock()
	a.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	return ch
}

func (a *rawACP) await(ch <-chan json.RawMessage, timeout time.Duration, what string) json.RawMessage {
	a.t.Helper()
	select {
	case res := <-ch:
		if bytes.HasPrefix(res, []byte(`{"__error":`)) {
			a.t.Fatalf("%s failed: %s", what, res)
		}
		return res
	case <-a.exited:
		a.t.Fatalf("%s: opencode exited: %s", what, a.stderrTail())
	case <-time.After(timeout):
		a.t.Fatalf("%s: no response within %s", what, timeout)
	}
	return nil
}

func (a *rawACP) stderrTail() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.stderr.String()
	if len(s) > 4000 {
		s = s[len(s)-4000:]
	}
	return s
}

func (a *rawACP) dump() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return strings.Join(a.lines, "\n")
}

// handshake runs initialize and session/new and returns the ACP session id.
func (a *rawACP) handshake(workDir string) string {
	a.t.Helper()
	a.await(a.call("initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{"fs": map[string]bool{"readTextFile": false, "writeTextFile": false}, "terminal": false},
		"clientInfo":         map[string]string{"name": "agentic-spike", "version": "0"},
	}), 2*time.Minute, "initialize")
	res := a.await(a.call("session/new", map[string]any{"cwd": workDir, "mcpServers": []any{}}), 2*time.Minute, "session/new")
	var out struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(res, &out); err != nil || out.SessionID == "" {
		a.t.Fatalf("session/new result %s: %v", res, err)
	}
	return out.SessionID
}

// httpStatus issues an HTTP request against the embedded server, with or
// without the server credentials.
func (a *rawACP) httpStatus(method, path string, body any, auth bool) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, a.baseURL+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		req.SetBasicAuth(openCodeServerUser, a.password)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data, nil
}

// waitHealthy polls the health route with credentials until it answers 200.
func (a *rawACP) waitHealthy(timeout time.Duration) {
	a.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		code, _, err := a.httpStatus(http.MethodGet, "/global/health", nil, true)
		if err == nil && code == http.StatusOK {
			return
		}
		select {
		case <-a.exited:
			a.t.Fatalf("opencode exited before its HTTP server answered: %s", a.stderrTail())
		default:
		}
		if time.Now().After(deadline) {
			a.t.Fatalf("HTTP server not healthy within %s (last code %d, err %v)", timeout, code, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// openCodeEvent is one decoded `GET /event` record.
type openCodeEvent struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
	raw        string
}

// streamEvents opens the authenticated event stream and delivers each event
// until ctx ends. The returned channel closes when the stream ends.
func (a *rawACP) streamEvents(ctx context.Context) (<-chan openCodeEvent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+"/event", nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(openCodeServerUser, a.password)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET /event: %s", resp.Status)
	}
	ch := make(chan openCodeEvent, 256)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
		for sc.Scan() {
			data, ok := strings.CutPrefix(sc.Text(), "data: ")
			if !ok {
				continue
			}
			var ev openCodeEvent
			if json.Unmarshal([]byte(data), &ev) != nil {
				continue
			}
			ev.raw = data
			select {
			case ch <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}
