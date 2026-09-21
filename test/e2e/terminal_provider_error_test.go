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
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

func TestTerminalProviderErrorEndsKeepAlivePhase(t *testing.T) {
	if testing.Short() {
		t.Skip("launches a keep-alive provider subprocess")
	}
	mgr := session.NewManager(make(chan interface{}, 10))
	t.Cleanup(mgr.Shutdown)
	// Emit the same terminal result as OpenCode's failed ACP prompt, then
	// keep the process alive until the phase waiter closes its input.
	command := []string{"sh", "-c", `printf '%s\n' '{"type":"result","subtype":"error","is_error":true,"result":"Model only supports text input; received unsupported content type image_url."}'; cat >/dev/null`}
	sess, err := mgr.StartSession("terminal-error", "feature-error", feature.PhaseImplement,
		command, t.TempDir(), nil, &session.SessionOpts{TurnMode: ports.TurnModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	// Shutdown only stops active sessions; also explicitly own cleanup of a
	// failed session whose underlying process has not exited yet.
	t.Cleanup(func() { _ = sess.Stop() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		select {
		case msg := <-sess.AttachCh():
			if msg.Result == nil {
				continue
			}
			if got := sess.Status(); got != ports.SessionFailed {
				t.Errorf("terminal error status = %v, want Failed (no help prompt)", got)
			}
			select {
			case <-sess.Done():
				t.Fatal("fixture exited before the waiter could handle the error")
			default:
			}
			result := agent.WaitForPhaseOutcome(sess, agent.PhaseOutcomeWaitOptions{Ctx: ctx})
			if result.Status != "FAILED" || result.Err == nil || !strings.Contains(result.Err.Error(), "Model only supports text input") {
				t.Fatalf("phase result = %+v, want terminal failure with model diagnostic", result)
			}
			select {
			case <-sess.Done():
			case <-ctx.Done():
				t.Fatal("provider process was not stopped")
			}
			if got := sess.Status(); got != ports.SessionFailed {
				t.Fatalf("clean transport exit overwrote terminal failure: %v", got)
			}
			if got := mgr.ActiveSessions(); len(got) != 0 {
				t.Fatalf("failed phase still has active sessions: %v", got)
			}
			return
		case <-ctx.Done():
			t.Fatal("provider did not emit its terminal result")
		}
	}
}
