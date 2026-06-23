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

package session

import (
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// TestOpenCodeSessionEstablishmentPersistsSessionIDToPIDFile proves the OpenCode
// session id reaches the PID file. The initial PID file (written by Session.Start
// before the ACP handshake) has no session id; once session/new establishes the
// ACP session, the protocol emits a system-init message that drives the session
// layer's existing PID-file session-id update. The session id is captured via the
// onMessage callback, which fires after the in-loop PID update but before the
// readMessages cleanup removes the file.
func TestOpenCodeSessionEstablishmentPersistsSessionIDToPIDFile(t *testing.T) {
	dir := t.TempDir()

	// A live, short-lived process so the PID-file update has a real PID to record
	// and the readMessages cleanup's process.Wait() returns promptly.
	cmd := exec.Command("sleep", "1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start process: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })

	s, p := newOpenCodeSession(t)
	const sessionNewID = 701
	p.SetRequestIDsForTest(0, sessionNewID, 0, 0)

	s.pidDir = dir
	s.repoName = "repo"
	s.featureID = "feat-1"
	s.phase = feature.PhaseImplement
	s.process = cmd
	s.startedAt = time.Now()

	// The initial PID file Session.Start writes before the handshake: no session id.
	if err := WritePIDFile(dir, PIDFile{
		PID: pid, RepoName: "repo", FeatureID: "feat-1", Phase: feature.PhaseImplement.String(),
	}); err != nil {
		t.Fatalf("write initial PID file: %v", err)
	}

	var captured string
	onMessage := func(msg llm.SDKMessage) {
		if msg.Init == nil {
			return
		}
		if pf, err := ReadPIDFile(filepath.Join(dir, PIDFileName("repo"))); err == nil {
			captured = pf.SessionID
		}
	}

	runSessionWithStdoutLines(t, s, []string{
		`{"jsonrpc":"2.0","id":701,"result":{"sessionId":"ses_real"}}`,
	}, onMessage)

	if captured != "ses_real" {
		t.Fatalf("PID file SessionID after session/new = %q, want ses_real (captured from ACP session establishment)", captured)
	}
	// The protocol exposes the same identity for session views and cache scoping.
	if got := s.SessionID(); got != "ses_real" {
		t.Fatalf("session.SessionID() = %q, want ses_real", got)
	}
}
