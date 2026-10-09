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

package testutil_test

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/askuser"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// envValue returns the value of key in env, or "".
func envValue(env []string, key string) string {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// childSessionID returns the one child session the fake minted.
func childSessionID(t *testing.T, script string) string {
	t.Helper()
	for _, s := range testutil.FakeOpenCodeSessions(t, script) {
		if s.Kind == "child" {
			return s.ID
		}
	}
	t.Fatal("fake minted no child session")
	return ""
}

// httpPosts returns the recorded authenticated POSTs whose path has prefix.
func httpPosts(t *testing.T, script, prefix string) []testutil.FakeOpenCodeHTTPRequest {
	t.Helper()
	var out []testutil.FakeOpenCodeHTTPRequest
	for _, r := range testutil.FakeOpenCodeHTTP(t, script) {
		if r.Method == "POST" && strings.HasPrefix(r.Path, prefix) {
			out = append(out, r)
		}
	}
	return out
}

func controlCount(s *openCodeSession) int {
	s.obs.mu.Lock()
	defer s.obs.mu.Unlock()
	n := 0
	for _, m := range s.obs.msgs {
		if m.ControlRequest != nil {
			n++
		}
	}
	return n
}

func TestOpenCodeBridgeLaunch(t *testing.T) {
	t.Run("interactive launch carries the server flags", func(t *testing.T) {
		s := mustStartOpenCodeSession(t, "hello", openCodeSessionOpts{interactive: true})
		s.obs.wait(t, "result", isResult)
		argv := testutil.FakeOpenCodeArgv(t, s.script)
		if len(argv) < 5 || argv[0] != "acp" || argv[len(argv)-4] != "--port" || argv[len(argv)-2] != "--hostname" || argv[len(argv)-1] != "127.0.0.1" {
			t.Fatalf("argv = %q, want acp ... --port <n> --hostname 127.0.0.1", argv)
		}
		if port, err := strconv.Atoi(argv[len(argv)-3]); err != nil || port <= 0 {
			t.Fatalf("port = %q", argv[len(argv)-3])
		}
		envs := testutil.FakeOpenCodeEnvs(t, s.script)
		if len(envs) != 1 || envs[0].Env["OPENCODE_SERVER_PASSWORD"] != testutil.FakeOpenCodeRedacted || envs[0].Env["OPENCODE_ENABLE_QUESTION_TOOL"] != "1" {
			t.Fatalf("recorded env = %+v", envs)
		}
		password := envValue(s.env, "OPENCODE_SERVER_PASSWORD")
		if len(password) < 32 {
			t.Fatalf("server password %d chars, want a random credential", len(password))
		}
		var sawAuthed bool
		for _, r := range testutil.FakeOpenCodeHTTP(t, s.script) {
			sawAuthed = sawAuthed || (r.Path == "/event" && r.Auth == "ok")
		}
		if !sawAuthed {
			t.Fatal("adapter never opened the authenticated event stream")
		}
		assertNoSecret(t, password, filepath.Dir(s.script), s.logPath)
	})

	t.Run("non-interactive launch is unchanged", func(t *testing.T) {
		s := mustStartOpenCodeSession(t, "hello", openCodeSessionOpts{})
		s.obs.wait(t, "result", isResult)
		if argv := testutil.FakeOpenCodeArgv(t, s.script); strings.Join(argv, " ") != "acp" {
			t.Fatalf("argv = %q, want [acp]", argv)
		}
		env := testutil.FakeOpenCodeEnvs(t, s.script)[0].Env
		if _, ok := env["OPENCODE_SERVER_PASSWORD"]; ok {
			t.Fatal("non-interactive launch set OPENCODE_SERVER_PASSWORD")
		}
		if _, ok := env["OPENCODE_ENABLE_QUESTION_TOOL"]; ok {
			t.Fatal("non-interactive launch set OPENCODE_ENABLE_QUESTION_TOOL")
		}
		if n := len(testutil.FakeOpenCodeHTTP(t, s.script)); n != 0 {
			t.Fatalf("non-interactive session made %d HTTP requests", n)
		}
	})

	t.Run("port is free before launch", func(t *testing.T) {
		prov := testutil.NewFakeOpenCodeProvider(t, testutil.WriteFakeOpenCodeScript(t, testutil.FakeOpenCodeScript{}))
		args, _, err := prov.BuildCommand(llm.CommandBuildOpts{Model: testutil.FakeOpenCodeModel, Interactive: true})
		if err != nil {
			t.Fatal(err)
		}
		l, err := net.Listen("tcp", "127.0.0.1:"+args[len(args)-3])
		if err != nil {
			t.Fatalf("allocated port is not free: %v", err)
		}
		_ = l.Close()
	})

	for _, tc := range []struct {
		name   string
		script testutil.FakeOpenCodeScript
		want   string
	}{
		{"server never starts", testutil.FakeOpenCodeScript{NoHTTP: true}, "HTTP server"},
		{"password rejected", testutil.FakeOpenCodeScript{RejectPassword: true}, "rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			s, err := startOpenCodeSession(t, "hello", openCodeSessionOpts{script: tc.script, interactive: true, timeout: 5 * time.Second})
			if err == nil {
				t.Fatal("handshake succeeded without a usable HTTP server")
			}
			if elapsed := time.Since(start); elapsed > 15*time.Second {
				t.Fatalf("handshake failure took %s", elapsed)
			}
			t.Logf("handshake error: %v", err)
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("handshake error = %v, want it to mention %q", err, tc.want)
			}
			password := envValue(s.env, "OPENCODE_SERVER_PASSWORD")
			if strings.Contains(err.Error(), password) {
				t.Fatal("handshake error leaks the server password")
			}
			assertNoSecret(t, password, filepath.Dir(s.script), s.logPath)
		})
	}
}

// assertNoSecret fails when secret appears in any file under dir or in the
// extra files.
func assertNoSecret(t *testing.T, secret, dir string, extra ...string) {
	t.Helper()
	files := append([]string(nil), extra...)
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err == nil && strings.Contains(string(data), secret) {
			t.Fatalf("%s contains the server password", f)
		}
	}
}

func TestOpenCodeBridgeChildPermission(t *testing.T) {
	for _, tc := range []struct {
		name, reply, verdict string
		answer               func(s *openCodeSession, id string) error
	}{
		{"allow", "once", "Child allowed", func(s *openCodeSession, id string) error { return s.sess.RespondToControl(id, true, "") }},
		{"deny", "reject", "Child denied", func(s *openCodeSession, id string) error { return s.sess.RespondToControl(id, false, "denied by user") }},
		{"remember", "always", "Child allowed", func(s *openCodeSession, id string) error {
			return s.proto.(llm.RememberingControlResponder).RespondToControlRemember(id)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := mustStartOpenCodeSession(t, "delegate "+testutil.FakeOpenCodeChildPermBash, openCodeSessionOpts{interactive: true})
			ctrl := s.obs.wait(t, "child control request", isControl)
			req := ctrl.ControlRequest
			child := childSessionID(t, s.script)
			if req.Request.ToolName != "Bash" || !strings.Contains(string(req.Request.Input), testutil.FakeOpenCodeBashCommand) {
				t.Fatalf("child request = %s %s", req.Request.ToolName, req.Request.Input)
			}
			if ctrl.Origin.Kind != llm.EventOriginTask || ctrl.Origin.ChildSessionID != child {
				t.Fatalf("child origin = %+v, want task origin for %s", ctrl.Origin, child)
			}
			if _, err := strconv.Atoi(req.RequestID); err == nil {
				t.Fatalf("bridged request id %q could collide with a JSON-RPC id", req.RequestID)
			}
			if err := tc.answer(s, req.RequestID); err != nil {
				t.Fatal(err)
			}
			s.obs.wait(t, "child verdict", func(m llm.SDKMessage) bool { return assistantText(m) == tc.verdict })
			posts := httpPosts(t, s.script, "/permission/")
			if len(posts) != 1 || !strings.Contains(string(posts[0].Body), `"reply":"`+tc.reply+`"`) || posts[0].Status != 200 {
				t.Fatalf("permission replies = %+v, want one %q", posts, tc.reply)
			}
			if n := controlCount(s); n != 1 {
				t.Fatalf("control requests = %d, want 1", n)
			}
		})
	}
}

func TestOpenCodeBridgeQuestions(t *testing.T) {
	for _, tc := range []struct {
		marker string
		child  bool
		reply  string
	}{
		{testutil.FakeOpenCodeChildAsk, true, "Child chose dev"},
		{testutil.FakeOpenCodeAsk, false, "You chose dev"},
	} {
		t.Run(tc.marker, func(t *testing.T) {
			s := mustStartOpenCodeSession(t, "ask "+tc.marker, openCodeSessionOpts{interactive: true})
			ctrl := s.obs.wait(t, "question", isControl)
			req := ctrl.ControlRequest
			if req.Request.ToolName != "AskUserQuestion" || !strings.Contains(string(req.Request.Input), testutil.FakeOpenCodeQuestion) {
				t.Fatalf("question = %s %s", req.Request.ToolName, req.Request.Input)
			}
			input, err := askuser.Parse(req.Request.Input)
			if err != nil || len(input.Questions) != 1 || len(input.Questions[0].Options) != 2 {
				t.Fatalf("question input = %s (%v)", req.Request.Input, err)
			}
			wantKind := llm.EventOriginRoot
			if tc.child {
				wantKind = llm.EventOriginTask
			}
			if ctrl.Origin.Kind != wantKind {
				t.Fatalf("origin = %+v, want %s", ctrl.Origin, wantKind)
			}
			if err := s.sess.RespondToAskUser(req.RequestID, resolveAskUser(t, req.Request.Input, map[string]string{"1": "dev"})); err != nil {
				t.Fatal(err)
			}
			s.obs.wait(t, "answer echo", func(m llm.SDKMessage) bool { return assistantText(m) == tc.reply })
			posts := httpPosts(t, s.script, "/question/")
			if len(posts) != 1 || !strings.HasSuffix(posts[0].Path, "/reply") || string(posts[0].Body) != `{"answers":[["dev"]]}` {
				t.Fatalf("question replies = %+v", posts)
			}
		})
	}

	t.Run("denied question rejects", func(t *testing.T) {
		s := mustStartOpenCodeSession(t, "ask "+testutil.FakeOpenCodeAsk, openCodeSessionOpts{interactive: true})
		ctrl := s.obs.wait(t, "question", isControl)
		if err := s.sess.RespondToControl(ctrl.ControlRequest.RequestID, false, "denied by user"); err != nil {
			t.Fatal(err)
		}
		s.obs.wait(t, "rejection echo", func(m llm.SDKMessage) bool { return assistantText(m) == "Question rejected" })
		posts := httpPosts(t, s.script, "/question/")
		if len(posts) != 1 || !strings.HasSuffix(posts[0].Path, "/reject") {
			t.Fatalf("question posts = %+v", posts)
		}
	})

	t.Run("root permission on the stream is left to ACP", func(t *testing.T) {
		s := mustStartOpenCodeSession(t, "run "+testutil.FakeOpenCodePermBash, openCodeSessionOpts{interactive: true})
		ctrl := s.obs.wait(t, "root request", isControl)
		if err := s.sess.RespondToControl(ctrl.ControlRequest.RequestID, true, ""); err != nil {
			t.Fatal(err)
		}
		s.obs.wait(t, "result", isResult)
		if n := controlCount(s); n != 1 {
			t.Fatalf("control requests = %d, want only the ACP one", n)
		}
		if posts := httpPosts(t, s.script, "/permission/"); len(posts) != 0 {
			t.Fatalf("root permission was answered over HTTP: %+v", posts)
		}
	})
}

func TestOpenCodeBridgeReconcile(t *testing.T) {
	t.Run("request published while disconnected", func(t *testing.T) {
		s := mustStartOpenCodeSession(t, "delegate "+testutil.FakeOpenCodeChildPermBash,
			openCodeSessionOpts{interactive: true, script: testutil.FakeOpenCodeScript{ChildWhileDisconnected: true}})
		ctrl := s.obs.wait(t, "reconciled child request", isControl)
		if err := s.sess.RespondToControl(ctrl.ControlRequest.RequestID, true, ""); err != nil {
			t.Fatal(err)
		}
		s.obs.wait(t, "child verdict", func(m llm.SDKMessage) bool { return assistantText(m) == "Child allowed" })
		var streams, lists int
		for _, r := range testutil.FakeOpenCodeHTTP(t, s.script) {
			if r.Path == "/event" && r.Status == 200 {
				streams++
			}
			if r.Method == "GET" && r.Path == "/permission" {
				lists++
			}
		}
		if streams < 2 || lists < 2 {
			t.Fatalf("event streams = %d, permission lists = %d; want a reconnect and a reconcile", streams, lists)
		}
		if n := controlCount(s); n != 1 {
			t.Fatalf("control requests = %d, want 1", n)
		}
	})

	t.Run("answered elsewhere", func(t *testing.T) {
		s := mustStartOpenCodeSession(t, "delegate "+testutil.FakeOpenCodeChildPermBash,
			openCodeSessionOpts{interactive: true, script: testutil.FakeOpenCodeScript{ResolveChildElsewhere: true}})
		ctrl := s.obs.wait(t, "child request", isControl)
		s.obs.wait(t, "turn released elsewhere", func(m llm.SDKMessage) bool { return assistantText(m) == "Child allowed" })
		s.obs.wait(t, "result", isResult)
		if err := s.sess.RespondToControl(ctrl.ControlRequest.RequestID, false, "late"); err != nil {
			t.Fatalf("late answer: %v", err)
		}
		if posts := httpPosts(t, s.script, "/permission/"); len(posts) != 0 {
			t.Fatalf("resolved request was answered again: %+v", posts)
		}
	})
}
