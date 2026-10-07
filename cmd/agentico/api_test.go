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
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
)

const apiTestToken = "api-helper-secret-token-7f3a91"

// publishAPIDiscovery publishes an owner-only discovery record for baseURL
// into runtimeDir, the way a running server does.
func publishAPIDiscovery(t *testing.T, runtimeDir, baseURL, token string) {
	t.Helper()
	if err := serverruntime.PublishDiscovery(runtimeDir, serverruntime.DiscoveryRecord{BaseURL: baseURL, AuthToken: token}); err != nil {
		t.Fatalf("PublishDiscovery() error = %v", err)
	}
}

// runAPICommand runs `agentico api args...` in process.
func runAPICommand(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runArgs(append([]string{cliSubcommandAPI}, args...), &stdout, &stderr, failingServerLauncher(t), failingUpdater(t))
	assertNoAPIToken(t, stdout.String(), stderr.String())
	return code, stdout.String(), stderr.String()
}

func assertNoAPIToken(t *testing.T, outputs ...string) {
	t.Helper()
	for _, out := range outputs {
		if strings.Contains(out, apiTestToken) {
			t.Fatalf("output leaks the bearer token:\n%s", out)
		}
	}
}

// recordedRequest is what a recording test server saw.
type recordedRequest struct {
	method, path, rawQuery, body string
	header                       http.Header
}

// recordingServer answers every request with status and body and records
// the last request it saw.
func recordingServer(t *testing.T, status int, body string) (*httptest.Server, func() recordedRequest) {
	t.Helper()
	var (
		mu   sync.Mutex
		last recordedRequest
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		mu.Lock()
		last = recordedRequest{method: r.Method, path: r.URL.Path, rawQuery: r.URL.RawQuery, body: string(data), header: r.Header.Clone()}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() recordedRequest {
		mu.Lock()
		defer mu.Unlock()
		return last
	}
}

// newAPIRuntime publishes a discovery record for srv into a fresh runtime
// directory and returns it.
func newAPIRuntime(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	dir := t.TempDir()
	publishAPIDiscovery(t, dir, srv.URL, apiTestToken)
	return dir
}

// realAPIServer serves the production handler, authenticated by
// apiTestToken, with an injectable legacy event channel.
func realAPIServer(t *testing.T) (*httptest.Server, chan interface{}) {
	t.Helper()
	events := make(chan interface{}, 4)
	handler := serverruntime.NewHandler(serverruntime.HandlerOptions{
		AuthToken:             apiTestToken,
		Events:                events,
		DisableHostValidation: true,
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, events
}

func wantCLIHeading(t *testing.T, stderr, code string) {
	t.Helper()
	if got := firstLine(t, stderr); !strings.HasPrefix(got, "error["+code+"]: ") {
		t.Fatalf("stderr first line = %q; want the %s heading:\n%s", got, code, stderr)
	}
}

func TestAPIGetHealthPrintsBodyVerbatim(t *testing.T) {
	t.Parallel()
	srv, _ := realAPIServer(t)
	code, stdout, stderr := runAPICommand(t, "--runtime-dir", newAPIRuntime(t, srv), "GET", "/api/v1/health")
	if code != 0 {
		t.Fatalf("code = %d; want 0\nstderr: %s", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q; want empty", stderr)
	}
	if !strings.Contains(stdout, `"status":"ok"`) {
		t.Fatalf("stdout = %q; want the health body", stdout)
	}
}

func TestAPIPostSendsBearerTrustedHeaderAndJSONBody(t *testing.T) {
	t.Parallel()
	const created = `{"id":"feat-x","name":"x"}` + "\n"
	srv, last := recordingServer(t, http.StatusCreated, created)
	code, stdout, stderr := runAPICommand(t, "--runtime-dir", newAPIRuntime(t, srv), "post", "/api/v1/features", `{"name":"x"}`)
	if code != 0 {
		t.Fatalf("code = %d; want 0\nstderr: %s", code, stderr)
	}
	if stdout != created {
		t.Fatalf("stdout = %q; want the 201 body verbatim", stdout)
	}
	req := last()
	if req.method != http.MethodPost || req.path != "/api/v1/features" || req.body != `{"name":"x"}` {
		t.Fatalf("request = %s %s %q; want POST /api/v1/features with the JSON body", req.method, req.path, req.body)
	}
	for header, want := range map[string]string{
		"Authorization":     "Bearer " + apiTestToken,
		"X-Agentico-Client": "local",
		"Accept":            "application/json",
		"Content-Type":      "application/json",
	} {
		if got := req.header.Get(header); got != want {
			t.Errorf("header %s = %q; want %q", header, got, want)
		}
	}
}

func TestAPIGetForwardsQueryAndOmitsContentType(t *testing.T) {
	t.Parallel()
	srv, last := recordingServer(t, http.StatusOK, `[]`)
	code, _, stderr := runAPICommand(t, "--runtime-dir", newAPIRuntime(t, srv), "GET", "/api/v1/features?status=running")
	if code != 0 {
		t.Fatalf("code = %d; want 0\nstderr: %s", code, stderr)
	}
	req := last()
	if req.rawQuery != "status=running" {
		t.Fatalf("query = %q; want status=running", req.rawQuery)
	}
	if got := req.header.Get("Content-Type"); got != "" {
		t.Fatalf("Content-Type = %q; want none without a body", got)
	}
	if req.header.Get("Authorization") != "Bearer "+apiTestToken {
		t.Fatalf("Authorization header missing on GET")
	}
}

func TestAPINon2xxPrintsEnvelopeToStdoutAndExits1(t *testing.T) {
	t.Parallel()
	const envelope = `{"error":{"code":"conflict","title":"Conflict"}}`
	srv, _ := recordingServer(t, http.StatusConflict, envelope)
	code, stdout, stderr := runAPICommand(t, "--runtime-dir", newAPIRuntime(t, srv), "POST", "/api/v1/features/f1/actions/start")
	if code != 1 {
		t.Fatalf("code = %d; want 1", code)
	}
	if stdout != envelope || stderr != "" {
		t.Fatalf("stdout = %q stderr = %q; want the envelope on stdout only", stdout, stderr)
	}
}

func TestAPIRealServerRejectionPrintsCanonicalEnvelope(t *testing.T) {
	t.Parallel()
	srv, _ := realAPIServer(t)
	dir := t.TempDir()
	publishAPIDiscovery(t, dir, srv.URL, "a-stale-token")
	code, stdout, stderr := runAPICommand(t, "--runtime-dir", dir, "GET", "/api/v1/features")
	if code != 1 {
		t.Fatalf("code = %d; want 1", code)
	}
	if !strings.Contains(stdout, `"code":"unauthorized"`) || stderr != "" {
		t.Fatalf("stdout = %q stderr = %q; want the unauthorized envelope on stdout", stdout, stderr)
	}
	if strings.Contains(stdout+stderr, "a-stale-token") {
		t.Fatalf("output leaks the discovery token")
	}
}

func TestAPIDiscoveryFailuresRenderTheirOwnCodes(t *testing.T) {
	t.Parallel()
	srv, _ := recordingServer(t, http.StatusOK, `{}`)
	cases := []struct {
		name string
		mode os.FileMode
		want string
	}{
		{name: "missing", want: "discovery_missing"},
		{name: "group readable", mode: 0o640, want: "discovery_untrusted"},
		{name: "world readable", mode: 0o604, want: "discovery_untrusted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if tc.mode != 0 {
				publishAPIDiscovery(t, dir, srv.URL, apiTestToken)
				if err := os.Chmod(serverruntime.DiscoveryPath(dir), tc.mode); err != nil {
					t.Fatalf("Chmod() error = %v", err)
				}
			}
			code, stdout, stderr := runAPICommand(t, "--runtime-dir", dir, "GET", "/api/v1/health")
			if code != 1 || stdout != "" {
				t.Fatalf("code = %d stdout = %q; want 1 and empty stdout", code, stdout)
			}
			wantCLIHeading(t, stderr, tc.want)
			if !strings.Contains(stderr, "hint: ") || !strings.Contains(stderr, dir) {
				t.Fatalf("stderr = %q; want a hint naming the runtime directory %s", stderr, dir)
			}
		})
	}
}

func TestAPIUnreachableServerRendersServerUnreachable(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.NotFoundHandler())
	dir := newAPIRuntime(t, srv)
	srv.Close()
	code, stdout, stderr := runAPICommand(t, "--runtime-dir", dir, "GET", "/api/v1/health")
	if code != 1 || stdout != "" {
		t.Fatalf("code = %d stdout = %q; want 1 and empty stdout", code, stdout)
	}
	wantCLIHeading(t, stderr, "server_unreachable")
	if !strings.Contains(stderr, dir) {
		t.Fatalf("stderr = %q; want it to name the runtime directory", stderr)
	}
}

// TestAPIRuntimeDirResolutionOrder pins flag over environment over the
// default home runtime directory. Not parallel: it sets HOME and the
// runtime-directory variable.
func TestAPIRuntimeDirResolutionOrder(t *testing.T) {
	serve := func(who string) *httptest.Server {
		srv, _ := recordingServer(t, http.StatusOK, `{"who":"`+who+`"}`)
		return srv
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	defaultDir := filepath.Join(home, ".agentic-orchestrator")
	publishAPIDiscovery(t, defaultDir, serve("default").URL, apiTestToken)
	envDir := newAPIRuntime(t, serve("env"))
	flagDir := newAPIRuntime(t, serve("flag"))

	t.Setenv(agent.RuntimeDirEnv, envDir)
	if _, stdout, stderr := runAPICommand(t, "--runtime-dir", flagDir, "GET", "/api/v1/health"); stdout != `{"who":"flag"}` {
		t.Fatalf("with flag: stdout = %q stderr = %q; want the flag runtime", stdout, stderr)
	}
	if _, stdout, stderr := runAPICommand(t, "GET", "/api/v1/health"); stdout != `{"who":"env"}` {
		t.Fatalf("with env: stdout = %q stderr = %q; want the env runtime", stdout, stderr)
	}
	t.Setenv(agent.RuntimeDirEnv, "")
	if _, stdout, stderr := runAPICommand(t, "GET", "/api/v1/health"); stdout != `{"who":"default"}` {
		t.Fatalf("default: stdout = %q stderr = %q; want the default runtime", stdout, stderr)
	}
}

func TestAPIFlagsMayFollowPositionals(t *testing.T) {
	t.Parallel()
	srv, last := recordingServer(t, http.StatusOK, `{}`)
	dir := newAPIRuntime(t, srv)
	code, _, stderr := runAPICommand(t, "PATCH", "/api/v1/features/f1/config", `{"a":1}`, "--runtime-dir", dir, "--timeout=5s")
	if code != 0 {
		t.Fatalf("code = %d; want 0\nstderr: %s", code, stderr)
	}
	if req := last(); req.method != http.MethodPatch || req.body != `{"a":1}` {
		t.Fatalf("request = %s %q; want PATCH with the body", req.method, req.body)
	}
}

func TestAPIInvalidUsageNeverReachesTheServer(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("server reached on an invalid-usage path")
	}))
	t.Cleanup(srv.Close)
	dir := newAPIRuntime(t, srv)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"invalid method", []string{"FETCH", "/api/v1/health"}, "unsupported method"},
		{"relative path", []string{"GET", "api/v1/health"}, "must start with /api/v1/"},
		{"non api path", []string{"GET", "/health"}, "must start with /api/v1/"},
		{"absolute url", []string{"GET", "http://evil.example/api/v1/health"}, "must start with /api/v1/"},
		{"body on get", []string{"GET", "/api/v1/features", `{"a":1}`}, "GET requests take no body"},
		{"malformed json", []string{"POST", "/api/v1/features", `{"name":`}, "not valid JSON"},
		{"missing path", []string{"GET"}, "requires METHOD and PATH"},
		{"extra argument", []string{"POST", "/api/v1/features", `{}`, `{}`}, "at most METHOD, PATH and one JSON body"},
		{"bad timeout", []string{"--timeout", "soon", "GET", "/api/v1/health"}, "invalid --timeout"},
		{"zero timeout", []string{"--timeout", "0s", "GET", "/api/v1/health"}, "invalid --timeout"},
		{"unknown flag", []string{"--bogus", "GET", "/api/v1/health"}, "unknown api flag: --bogus"},
		{"stream without timeout", []string{"GET", "/api/v1/events"}, "requires --timeout"},
		{"stream with post", []string{"--timeout", "1s", "POST", "/api/v1/events"}, "stream paths accept only GET"},
		{"after on one-shot path", []string{"--after", "3", "GET", "/api/v1/features"}, "--after applies only to stream paths"},
		{"non-numeric after", []string{"--timeout", "1s", "--after", "x", "GET", "/api/v1/events"}, "invalid --after"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			code, stdout, stderr := runAPICommand(t, append([]string{"--runtime-dir", dir}, tc.args...)...)
			if code != 1 || stdout != "" {
				t.Fatalf("code = %d stdout = %q; want 1 and empty stdout", code, stdout)
			}
			wantCLIHeading(t, stderr, "invalid_usage")
			if !strings.Contains(stderr, tc.want) {
				t.Fatalf("stderr = %q; want it to mention %q", stderr, tc.want)
			}
		})
	}
}

func TestAPIHelpPrintsSubcommandUsage(t *testing.T) {
	t.Parallel()
	for _, flag := range []string{"--help", "-h"} {
		code, stdout, stderr := runAPICommand(t, flag)
		if code != 0 || stderr != "" {
			t.Fatalf("%s: code = %d stderr = %q; want 0 and empty stderr", flag, code, stderr)
		}
		for _, want := range []string{"Usage: agentico api", "--runtime-dir", "--timeout", "--after", agent.RuntimeDirEnv} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("%s: usage %q does not mention %q", flag, stdout, want)
			}
		}
	}
	var stdout bytes.Buffer
	printUsage(&stdout)
	if !strings.Contains(stdout.String(), "agentico api ") {
		t.Fatalf("top-level usage does not list the api subcommand:\n%s", stdout.String())
	}
}

func TestAPIStreamPathRecognition(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]bool{
		"/api/v1/events":                          true,
		"/api/v1/events?after=3":                  true,
		"/api/v1/supervisor/events":               true,
		"/api/v1/sessions/s-1/output/stream":      true,
		"/api/v1/sessions/s-1/output/stream?from": true,
		"/api/v1/sessions//output/stream":         false,
		"/api/v1/sessions/a/b/output/stream":      false,
		"/api/v1/events/extra":                    false,
		"/api/v1/features":                        false,
	} {
		if got := isAPIStreamPath(path); got != want {
			t.Errorf("isAPIStreamPath(%q) = %v; want %v", path, got, want)
		}
	}
}

// streamResult is a running `agentico api` stream whose stdout is a pipe,
// so the test observes each line as soon as the command flushes it.
type streamResult struct {
	lines chan string
	done  chan int
	mu    sync.Mutex
	all   []string
	err   bytes.Buffer
}

func startAPIStream(t *testing.T, args ...string) *streamResult {
	t.Helper()
	pr, pw := io.Pipe()
	res := &streamResult{lines: make(chan string, 64), done: make(chan int, 1)}
	go func() {
		code := runArgs(append([]string{cliSubcommandAPI}, args...), pw, &res.err, failingServerLauncher(t), failingUpdater(t))
		_ = pw.Close()
		res.done <- code
	}()
	go func() {
		scanner := bufio.NewScanner(pr)
		for scanner.Scan() {
			res.mu.Lock()
			res.all = append(res.all, scanner.Text())
			res.mu.Unlock()
			res.lines <- scanner.Text()
		}
		close(res.lines)
	}()
	return res
}

func (r *streamResult) next(t *testing.T) string {
	t.Helper()
	select {
	case line, ok := <-r.lines:
		if !ok {
			t.Fatalf("stream closed before the next line; stderr: %s", r.err.String())
		}
		return line
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a stream line")
		return ""
	}
}

// wait drains the stream and returns the exit code and every printed line.
func (r *streamResult) wait(t *testing.T) (int, []string) {
	t.Helper()
	select {
	case code := <-r.done:
		for range r.lines {
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		assertNoAPIToken(t, strings.Join(r.all, "\n"), r.err.String())
		return code, r.all
	case <-time.After(15 * time.Second):
		t.Fatal("stream did not exit")
		return 0, nil
	}
}

func TestAPIStreamPrintsConnectedAndPublishedEventsUntilTimeout(t *testing.T) {
	t.Parallel()
	srv, events := realAPIServer(t)
	dir := newAPIRuntime(t, srv)
	started := time.Now()
	stream := startAPIStream(t, "--runtime-dir", dir, "--timeout", "2s", "GET", "/api/v1/events?heartbeat_ms=10")

	if first := stream.next(t); !strings.Contains(first, `"kind":"connected"`) {
		t.Fatalf("first line = %q; want the connected event payload", first)
	}
	select {
	case <-stream.done:
		t.Fatal("command returned before the connected line was consumed; output is not line-flushed")
	default:
	}
	events <- ports.Event{Type: ports.PhaseCompleted, FeatureID: "feat-stream", Phase: feature.PhaseImplement}
	if line := stream.next(t); !strings.Contains(line, `"kind":"lifecycle.updated"`) || !strings.Contains(line, `"feat-stream"`) {
		t.Fatalf("second line = %q; want the published lifecycle event", line)
	}

	code, lines := stream.wait(t)
	if code != 0 {
		t.Fatalf("code = %d; want 0 at timeout\nstderr: %s", code, stream.err.String())
	}
	if elapsed := time.Since(started); elapsed < 2*time.Second {
		t.Fatalf("command returned after %v; want it to stream until the 2s timeout", elapsed)
	}
	for _, line := range lines {
		if strings.Contains(line, "heartbeat") {
			t.Fatalf("heartbeat printed: %q", line)
		}
	}
}

func TestAPIStreamStaleCursorSurfacesStreamReset(t *testing.T) {
	t.Parallel()
	srv, _ := realAPIServer(t)
	dir := newAPIRuntime(t, srv)
	stream := startAPIStream(t, "--runtime-dir", dir, "--timeout", "500ms", "--after", "999", "GET", "/api/v1/events")
	if first := stream.next(t); !strings.Contains(first, `"kind":"stream.reset"`) {
		t.Fatalf("first line = %q; want the stream.reset event", first)
	}
	if code, _ := stream.wait(t); code != 0 {
		t.Fatalf("code = %d; want 0\nstderr: %s", code, stream.err.String())
	}
}

func TestAPIStreamForwardsCursorAndExitsOnServerClose(t *testing.T) {
	t.Parallel()
	var (
		mu  sync.Mutex
		got *http.Request
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = r.Clone(r.Context())
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, ": comment\n\n")
		_, _ = fmt.Fprint(w, "event: heartbeat\ndata: {\"kind\":\"heartbeat\"}\n\n")
		_, _ = fmt.Fprint(w, "id: 8\r\nevent: record\r\ndata: {\"kind\":\"record\",\r\ndata: \"seq\":8}\r\n\r\n")
		_, _ = fmt.Fprint(w, "event: state\ndata: {\"kind\":\"state\"}\n\n")
	}))
	t.Cleanup(srv.Close)
	dir := newAPIRuntime(t, srv)
	started := time.Now()
	stream := startAPIStream(t, "--runtime-dir", dir, "--timeout", "30s", "--after", "7", "GET", "/api/v1/supervisor/events")
	code, lines := stream.wait(t)
	if code != 0 {
		t.Fatalf("code = %d; want 0 on server close\nstderr: %s", code, stream.err.String())
	}
	if time.Since(started) > 10*time.Second {
		t.Fatal("command waited for the timeout instead of exiting on server close")
	}
	want := []string{`{"kind":"record", "seq":8}`, `{"kind":"state"}`}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lines = %q; want %q", lines, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if got.Header.Get("Last-Event-ID") != "7" {
		t.Fatalf("Last-Event-ID = %q; want the --after cursor", got.Header.Get("Last-Event-ID"))
	}
	if got.Header.Get("Authorization") != "Bearer "+apiTestToken {
		t.Fatal("stream request carries no bearer header")
	}
	if strings.Contains(got.URL.RawQuery, "access_token") || strings.Contains(got.URL.RawQuery, apiTestToken) {
		t.Fatalf("stream request carries a query token: %q", got.URL.RawQuery)
	}
}

func TestAPIStreamNon2xxPrintsEnvelope(t *testing.T) {
	t.Parallel()
	srv, _ := realAPIServer(t)
	dir := t.TempDir()
	publishAPIDiscovery(t, dir, srv.URL, "a-stale-token")
	code, stdout, stderr := runAPICommand(t, "--runtime-dir", dir, "--timeout", "2s", "GET", "/api/v1/events")
	if code != 1 || stderr != "" || !strings.Contains(stdout, `"code":"unauthorized"`) {
		t.Fatalf("code = %d stdout = %q stderr = %q; want the unauthorized envelope on stdout and exit 1", code, stdout, stderr)
	}
}
