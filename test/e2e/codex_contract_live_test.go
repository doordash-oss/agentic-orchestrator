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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/askuser"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/codex"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

// TestCodexContractLive exercises installed CLI/model behavior rather than a
// captured wire fixture. It consumes model inference and requires existing
// Codex authentication, so the everyday and subprocess suites skip it.
func TestCodexContractLive(t *testing.T) {
	if testing.Short() || os.Getenv("AGENTIC_CODEX_LIVE") != "1" {
		t.Skip("set AGENTIC_CODEX_LIVE=1 without -short to run live Codex compatibility")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	versionCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	version, err := exec.CommandContext(versionCtx, binary, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("Codex version: %v: %s", err, version)
	}
	t.Logf("CLI: %s", strings.TrimSpace(string(version)))
	modelList := os.Getenv("AGENTIC_CODEX_MODELS")
	if modelList == "" {
		modelList = defaultCodexContractModels(t)
	}
	var models []string
	for _, model := range strings.Split(modelList, ",") {
		if model = strings.TrimSpace(model); model != "" {
			models = append(models, model)
		}
	}
	if len(models) == 0 {
		t.Fatal("AGENTIC_CODEX_MODELS must contain at least one model")
	}
	for i, model := range models {
		resumeModel := models[(i+1)%len(models)]
		t.Run(model, func(t *testing.T) {
			workDir, stateDir := t.TempDir(), t.TempDir()
			marker := "developer-" + randomHex(t, 6)
			thread := runCodexContractStage(t, binary, model, "", "fresh", marker, workDir, stateDir)
			t.Run("resume_as_"+resumeModel, func(t *testing.T) {
				resumed := runCodexContractStage(t, binary, resumeModel, thread, "resumed", marker, workDir, stateDir)
				if resumed != thread {
					t.Fatalf("resume changed thread identity: got %q, want %q", resumed, thread)
				}
			})
		})
	}
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(buf)
}

// runCodexContractStage runs one stage of the contract. Both stages share
// the developer instructions (marker and file names): Codex keeps the
// instructions a thread was created with and does not apply replacements
// sent on thread/resume, so the resumed stage proves they persisted. Each
// stage gets a fresh fixture token and starts without the result file, so a
// resumed model must reread the fixture rather than replay history.
func runCodexContractStage(t *testing.T, binary, model, resumeID, stage, marker, workDir, stateDir string) string {
	t.Helper()
	fixtureToken := "fixture-" + randomHex(t, 6)
	const fixtureName, artifactName = "contract-input.txt", "contract-result.txt"
	if err := os.WriteFile(filepath.Join(workDir, fixtureName), []byte(fixtureToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(workDir, artifactName)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	instructions := fmt.Sprintf(`You are executing an isolated Agentico integration compatibility test.
Follow this exact procedure, sequentially, every time you are asked to run it, even if prior thread history already ran it.
1. Read %s with a shell/file tool. Its text is the fixture token; do not guess it.
2. Call ask_user with a choice question containing both %q and the fixture token. Offer exactly three options: Alpha, Beta, Gamma. Give Alpha confidence 0.9 and the sole recommendation; give Beta 0.6 and Gamma 0.2. Include useful descriptions. Wait for the answer.
3. Before writing %s, call complete_phase with status success. This deliberately tests the harness rejection path: the required artifact is absent. Do not create it before this first call.
4. After the completion tool returns its rejection, write %s containing exactly %s|<fixture token>|<chosen option label> followed by one newline. Use the actual user-selected label, without recommendation suffixes.
5. Call complete_phase with status success again. Do not emit outcome tags or replace a tool question with prose. Do not modify other files or use external services.`, fixtureName, marker, artifactName, artifactName, marker)
	prompt := "Run the compatibility procedure."
	if resumeID != "" {
		// The resumed thread holds a finished run whose token the model tends
		// to replay; the marker is deliberately not restated.
		prompt = "Run the compatibility procedure again from step 1. " + fixtureName + " was rewritten with a new fixture token and " +
			artifactName + " was removed; the previous run's token, answer and result no longer apply."
	}
	protocol := codex.NewProtocol(llm.ProtocolOpts{
		Model: model, WorkDir: workDir, StateDir: stateDir,
		SystemPrompt: instructions, InitialPrompt: prompt,
		WritableRoots: []string{workDir}, ResumeSessionID: resumeID,
		StructuredCompletion: true,
	})
	manager := session.NewManager(nil)
	t.Cleanup(manager.Shutdown)
	// Launch directly to preserve user auth/config without reconciling local
	// customization links. Overrides affect this subprocess only.
	command := []string{binary, "app-server", "-c", "web_search=disabled", "-c", "mcp_servers={}", "-c", "plugins={}"}
	sess, err := manager.StartSession("codex-live-"+stage, "codex-live", feature.PhaseInquire, command, workDir, nil, &ports.SessionOpts{
		Protocol: protocol, ProviderName: "codex", Model: model,
		TurnMode: ports.TurnModeInteractive, PIDDir: stateDir,
		LogPath:               filepath.Join(stateDir, stage+".jsonl"),
		StderrPath:            filepath.Join(stateDir, stage+".stderr"),
		CodexHandshakeTimeout: 45 * time.Second,
	})
	if err != nil {
		t.Fatalf("start %s (%s): %v", stage, model, err)
	}
	if registrar, ok := sess.(ports.AttachConsumerRegistrar); ok {
		release := registrar.RegisterAttachConsumer()
		defer release()
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("model output:\n%s", sess.MessageLog().AssistantText())
			if stderr, err := os.ReadFile(filepath.Join(stateDir, stage+".stderr")); err == nil {
				t.Logf("app-server stderr:\n%s", stderr)
			}
			if out, err := os.ReadFile(filepath.Join(stateDir, stage+".jsonl")); err == nil {
				if len(out) > 8000 {
					out = out[len(out)-8000:]
				}
				t.Logf("app-server output (tail):\n%s", out)
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var answered, rejected atomic.Bool
	finished := make(chan agent.PhaseOutcomeWaitResult, 1)
	go func() {
		finished <- agent.WaitForPhaseOutcome(sess, agent.PhaseOutcomeWaitOptions{
			Ctx: ctx,
			CommitOutcome: func(intent llm.CompletionIntent) ([]agent.ProtocolViolation, error) {
				if !answered.Load() {
					return nil, fmt.Errorf("completion reached validator before structured question was answered")
				}
				artifact, err := os.ReadFile(filepath.Join(workDir, artifactName))
				if os.IsNotExist(err) {
					rejected.Store(true)
					return []agent.ProtocolViolation{{Artifact: artifactName, Reason: "Required artifact is absent. Write the exact contract result with the user's chosen option, then call complete_phase again."}}, nil
				}
				if err != nil {
					return nil, err
				}
				if !rejected.Load() {
					return nil, fmt.Errorf("model bypassed the required rejected-completion exercise")
				}
				want := marker + "|" + fixtureToken + "|Beta\n"
				if string(artifact) != want || intent.Status != llm.CompletionIntentSuccess {
					return nil, fmt.Errorf("artifact = %q, want %q; intent = %+v", artifact, want, intent)
				}
				return nil, nil
			},
		})
	}()
	for {
		select {
		case msg, open := <-sess.AttachCh():
			if !open {
				// The coordinator closes the session after accepting completion.
				select {
				case result := <-finished:
					checkCodexContractResult(t, result, answered.Load(), rejected.Load())
					return protocol.SessionID()
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			if request := msg.ControlRequest; request != nil {
				if request.Request.ToolName != "AskUserQuestion" {
					if err := sess.RespondToControl(request.RequestID, true, "Allow isolated fixture operation."); err != nil {
						t.Fatal(err)
					}
					continue
				}
				checkCodexContractQuestion(t, request.Request.Input, marker, fixtureToken)
				if answered.Swap(true) {
					t.Fatal("model asked more than the declared single question")
				}
				if !sess.HasPendingRootAskUserQuestion() {
					t.Fatal("structured question was not recorded as pending")
				}
				if err := sess.RespondToAskUser(request.RequestID, resolveAskUser(t, request.Request.Input, map[string]string{"1": "Beta"})); err != nil {
					t.Fatal(err)
				}
			}
		case result := <-finished:
			checkCodexContractResult(t, result, answered.Load(), rejected.Load())
			return protocol.SessionID()
		case <-ctx.Done():
			t.Fatalf("live contract timed out (%s): %v", model, ctx.Err())
		}
	}
}

func checkCodexContractResult(t *testing.T, result agent.PhaseOutcomeWaitResult, answered, rejected bool) {
	t.Helper()
	if result.Err != nil || len(result.ProtocolViolations) != 0 || !answered || !rejected {
		t.Fatalf("completion = %+v; answered=%v, rejected=%v", result, answered, rejected)
	}
	if result.Status != "SUCCESS" {
		t.Fatalf("completion status = %q, want SUCCESS", result.Status)
	}
}

func checkCodexContractQuestion(t *testing.T, input json.RawMessage, marker, fixtureToken string) {
	t.Helper()
	bundle, err := askuser.Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Questions) != 1 {
		t.Fatalf("question bundle = %s", input)
	}
	question := bundle.Questions[0]
	if !strings.Contains(question.Question, marker) || !strings.Contains(question.Question, fixtureToken) || len(question.Options) != 3 {
		t.Fatalf("question lost developer instructions, fixture read, or option shape (want marker %s, token %s): %s", marker, fixtureToken, input)
	}
	for i, option := range question.Options {
		wantConfidence := []float64{0.9, 0.6, 0.2}[i]
		wantLabel := []string{"Alpha (Recommended)", "Beta", "Gamma"}[i]
		if option.Label != wantLabel || option.Confidence == nil || *option.Confidence != wantConfidence {
			t.Fatalf("option %d does not match confidence/recommendation contract: %s", i, input)
		}
	}
}

// defaultCodexContractModels picks the first two distinct models of the
// installed CLI's own catalog: which models an account may use varies, so a
// hard-coded pair goes stale.
func defaultCodexContractModels(t *testing.T) string {
	t.Helper()
	catalog, err := codex.NewProvider("").DiscoverModelCatalog(context.Background())
	if err != nil {
		t.Fatalf("discover the Codex model catalog (set AGENTIC_CODEX_MODELS to skip): %v", err)
	}
	var models []string
	for _, m := range catalog {
		base := llm.StripModelContextWindow(m.ID)
		if !slices.Contains(models, base) {
			models = append(models, base)
		}
		if len(models) == 2 {
			break
		}
	}
	return strings.Join(models, ",")
}

func resolveAskUser(t *testing.T, input json.RawMessage, answers map[string]string) askuser.Resolved {
	t.Helper()
	bundle, err := askuser.Parse(input)
	if err != nil {
		t.Fatalf("askuser.Parse: %v", err)
	}
	resolved, err := bundle.Resolve(answers)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return resolved
}
