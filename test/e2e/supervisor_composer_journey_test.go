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
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// stageUpload posts one file to the uploads route and returns its staged
// reference.
func (h *supervisorHarness) stageUpload(kind, name string, content []byte) string {
	h.t.Helper()
	q := url.Values{"kind": {kind}, "name": {name}}
	req, _ := http.NewRequest(http.MethodPost, h.baseURL+"/api/v1/uploads?"+q.Encode(), bytes.NewReader(content))
	req.Header.Set("Authorization", "Bearer "+supervisorTestToken)
	req.Header.Set("X-Agentico-Client", "local")
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("stage upload %s: %v", name, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("stage upload %s status = %d; body %s", name, resp.StatusCode, data)
	}
	var staged server.StageUploadResponse
	if err := json.Unmarshal(data, &staged); err != nil || staged.Reference == "" {
		h.t.Fatalf("decode staged upload %s: %v", data, err)
	}
	return staged.Reference
}

// deliveredTexts returns the text of every user turn that reached the
// harness, in order: fake Claude's stdin lines, fake Codex's turn/start
// input and fake OpenCode's session/prompt blocks.
func (h *supervisorHarness) deliveredTexts() []string {
	h.t.Helper()
	switch h.harness {
	case "codex":
		var out []string
		for _, turn := range h.codexTurns() {
			out = append(out, turn.TurnText())
		}
		return out
	case "opencode":
		var out []string
		for _, prompt := range h.openCodePrompts() {
			out = append(out, prompt.PromptText())
		}
		return out
	default:
		return h.userInputs()
	}
}

// conversationRecords reads one conversation's durable transcript file,
// whether or not it is the current conversation.
func (h *supervisorHarness) conversationRecords(conversationID string) []supervisor.Record {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.conversationDir(conversationID), "transcript.jsonl"))
	if err != nil {
		h.t.Fatalf("read transcript of %s: %v", conversationID, err)
	}
	var out []supervisor.Record
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var rec supervisor.Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			h.t.Fatalf("decode durable record %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func (h *supervisorHarness) reset() server.SupervisorResetResponse {
	h.t.Helper()
	var resp server.SupervisorResetResponse
	h.do(http.MethodPost, "/api/v1/supervisor/reset", map[string]any{}, http.StatusOK, &resp)
	return resp
}

// TestSupervisorAttachmentsReachEveryHarnessAndSurviveRelaunch attaches an
// image and a file both as local paths and as staged upload references. On
// every harness the delivered text names the copies inside the
// conversation's attachments directory, the copies hold the original bytes,
// and after End plus a relaunch the rebuilt history the fake reads back
// still names the same paths, which still exist.
func TestSupervisorAttachmentsReachEveryHarnessAndSurviveRelaunch(t *testing.T) {
	for _, harness := range []string{"claude", "codex", "opencode"} {
		t.Run(harness, func(t *testing.T) {
			var h *supervisorHarness
			switch harness {
			case "codex":
				h = newCodexSupervisorHarness(t, testutil.FakeCodexScript{})
			case "opencode":
				h = newOpenCodeSupervisorHarness(t, testutil.FakeOpenCodeScript{})
			default:
				h = newSupervisorHarness(t, testutil.FakeClaudeInteractiveScriptBody())
			}
			h.chooseSettings()

			srcDir := t.TempDir()
			localImage := filepath.Join(srcDir, "mock.png")
			localFile := filepath.Join(srcDir, "notes.txt")
			contents := map[string][]byte{
				"mock.png":  []byte("\x89PNG\r\n\x1a\nlocal image bytes"),
				"notes.txt": []byte("local notes first line\nsecond line\n"),
				"shot.webp": []byte("RIFF....WEBPuploaded image bytes"),
				"spec.md":   []byte("# Uploaded spec\nbody\n"),
			}
			for path, name := range map[string]string{localImage: "mock.png", localFile: "notes.txt"} {
				if err := os.WriteFile(path, contents[name], 0o644); err != nil {
					t.Fatal(err)
				}
			}
			imageRef := h.stageUpload("image", "shot.webp", contents["shot.webp"])
			fileRef := h.stageUpload("attachment", "spec.md", contents["spec.md"])

			const text = "Review the mock and the spec"
			var sent server.SupervisorMessageResponse
			h.do(http.MethodPost, "/api/v1/supervisor/messages", map[string]any{
				"text": text, "client_message_id": "attach-1",
				"images": []string{localImage}, "image_uploads": []string{imageRef},
				"attachments": []string{localFile}, "attachment_uploads": []string{fileRef},
			}, http.StatusOK, &sent)
			first := h.waitState("attachment turn", func(st server.SupervisorState) bool {
				return st.Lifecycle == server.SupervisorLifecycleIdle && st.HeadSeq >= 2
			})

			// The committed user record lists the four copies in harness
			// order with kind, original name and size.
			page := h.transcript("?limit=500")
			if len(page.Items) == 0 || page.Items[0].Kind != server.SupervisorRecordKindUser {
				t.Fatalf("transcript = %s", recordKinds(page.Items))
			}
			atts := page.Items[0].Attachments
			wantOrder := []struct {
				kind server.SupervisorAttachmentKind
				name string
			}{{server.Image, "mock.png"}, {server.Image, "shot.webp"}, {server.File, "notes.txt"}, {server.File, "spec.md"}}
			if len(atts) != len(wantOrder) {
				t.Fatalf("user record attachments = %+v", atts)
			}
			attachDir := filepath.Join(h.conversationDir(first.ConversationID), "attachments")
			var data supervisor.UserData
			data.Text = text
			for i, want := range wantOrder {
				a := atts[i]
				if a.Kind != want.kind || a.Name != want.name || a.Size != int64(len(contents[want.name])) {
					t.Fatalf("attachment %d = %+v, want %s %s", i, a, want.kind, want.name)
				}
				if filepath.Dir(a.Path) != attachDir || filepath.Ext(a.Path) != filepath.Ext(want.name) {
					t.Fatalf("attachment %d path %q is not a copy under %s with extension %s", i, a.Path, attachDir, filepath.Ext(want.name))
				}
				got, err := os.ReadFile(a.Path)
				if err != nil || !bytes.Equal(got, contents[want.name]) {
					t.Fatalf("attachment %d copy %s = %q (%v), want the original bytes", i, a.Path, got, err)
				}
				data.Attachments = append(data.Attachments, supervisor.Attachment{Path: a.Path, Kind: string(a.Kind), Name: a.Name, Size: a.Size})
			}
			// The originals are untouched and the staged sources are gone.
			for path, name := range map[string]string{localImage: "mock.png", localFile: "notes.txt"} {
				if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, contents[name]) {
					t.Fatalf("original %s changed: %q (%v)", path, got, err)
				}
			}
			for _, ref := range []string{imageRef, fileRef} {
				if _, err := os.Stat(filepath.Join(h.stateDir, "uploads", ref)); !os.IsNotExist(err) {
					t.Fatalf("staged source %s still present (err %v)", ref, err)
				}
			}

			// The harness received the visible text followed by the
			// attachment block naming the copies.
			rendered := supervisor.RenderUserMessage(data)
			for _, want := range []string{"Attached Images:\n- [Image #1]: " + atts[0].Path + "\n- [Image #2]: " + atts[1].Path,
				"Attached Files:\n- [notes.txt]: " + atts[2].Path + "\n- [spec.md]: " + atts[3].Path} {
				if !strings.Contains(rendered, want) {
					t.Fatalf("rendered message lacks %q:\n%s", want, rendered)
				}
			}
			delivered := h.deliveredTexts()
			if len(delivered) != 1 || delivered[0] != rendered {
				t.Fatalf("delivered texts = %q, want [%q]", delivered, rendered)
			}

			// End, then relaunch: the rebuilt history still names the same
			// paths and the copies still exist.
			h.do(http.MethodPost, "/api/v1/supervisor/end", map[string]any{}, http.StatusOK, nil)
			h.waitLifecycle(server.SupervisorLifecycleStopped)
			h.send(testutil.FakeSupervisorRecallFirst, "recall-first")
			relaunched := h.waitState("recall turn", func(st server.SupervisorState) bool {
				return st.Lifecycle == server.SupervisorLifecycleIdle && st.HeadSeq >= first.HeadSeq+2
			})
			if relaunched.Generation != first.Generation+1 || relaunched.ConversationID != first.ConversationID {
				t.Fatalf("relaunch state = %+v after %+v", relaunched, first)
			}
			if got := lastAssistantText(h.transcript("?limit=500").Items); got != "First user prompt: "+rendered {
				t.Fatalf("recall after relaunch = %q, want %q", got, "First user prompt: "+rendered)
			}
			for i, a := range atts {
				got, err := os.ReadFile(a.Path)
				if err != nil || !bytes.Equal(got, contents[wantOrder[i].name]) {
					t.Fatalf("attachment %d copy %s after relaunch = %q (%v)", i, a.Path, got, err)
				}
			}
		})
	}
}

// TestSupervisorResetDuringHeldTurnMarksInterruptAndResetsStream starts a
// new conversation while a turn is held: the old transcript gains the
// user-interrupted marker for the cut turn, a subscribed stream sees
// stream.reset under the new conversation and then its first record live,
// and the next send launches generation 1 with no resumed history.
func TestSupervisorResetDuringHeldTurnMarksInterruptAndResetsStream(t *testing.T) {
	h := newSupervisorHarness(t, testutil.FakeClaudeInteractiveScriptBody())
	h.chooseSettings()
	h.send("before the reset", "c1")
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	h.send("hold "+testutil.FakeSupervisorHold, "c2")
	held := h.waitLifecycle(server.SupervisorLifecycleRunning)
	oldID := held.ConversationID
	oldRecords := h.conversationRecords(oldID)
	heldTurn := oldRecords[len(oldRecords)-1].TurnID
	if oldRecords[len(oldRecords)-1].Kind != supervisor.KindUser || heldTurn == "" {
		t.Fatalf("held turn record = %+v", oldRecords[len(oldRecords)-1])
	}

	stream := h.openStream("")
	stream.until("running state", isState(server.SupervisorLifecycleRunning))

	resp := h.reset()
	st := resp.State
	if resp.Result != server.SupervisorResetDone || resp.PreviousConversationID != oldID {
		t.Fatalf("reset response = %+v", resp)
	}
	if st.ConversationID == oldID || st.Generation != 0 || st.Lifecycle != server.SupervisorLifecycleStopped ||
		st.LastTurnOutcome != server.SupervisorTurnOutcomeNone || st.HeadSeq != 0 || st.Settings != held.Settings || st.StreamEpoch == held.StreamEpoch {
		t.Fatalf("state after reset = %+v; before %+v", st, held)
	}

	events := stream.until("stream.reset", func(ev sseEvent) bool { return ev.kind == string(server.SupervisorEventStreamReset) })
	reset := events[len(events)-1].data
	if reset.ConversationID != st.ConversationID || !reset.SnapshotRequired || reset.StreamEpoch != st.StreamEpoch {
		t.Fatalf("stream.reset = %+v, want conversation %s epoch %s", reset, st.ConversationID, st.StreamEpoch)
	}

	// The old transcript ends with the user-interrupted marker for the cut
	// turn and is otherwise untouched by the new conversation.
	var marked bool
	for _, rec := range h.conversationRecords(oldID) {
		if rec.Kind != supervisor.KindMarker || rec.TurnID != heldTurn {
			continue
		}
		var marker supervisor.MarkerData
		if err := json.Unmarshal(rec.Data, &marker); err != nil {
			t.Fatal(err)
		}
		if marker.Marker == supervisor.MarkerInterrupted {
			marked = true
		}
	}
	if !marked {
		t.Fatalf("old transcript lacks the interrupted marker for turn %s: %+v", heldTurn, h.conversationRecords(oldID))
	}
	if page := h.transcript(""); page.ConversationID != st.ConversationID || len(page.Items) != 0 {
		t.Fatalf("new transcript = %s under %s", recordKinds(page.Items), page.ConversationID)
	}

	before := h.invocations()
	if sent := h.send("fresh start", "c3"); !sent.Launched {
		t.Fatalf("first send of the new conversation did not launch: %+v", sent)
	}
	stream.until("new conversation's first record", func(ev sseEvent) bool {
		return ev.kind == string(server.SupervisorEventRecord) && ev.data.ConversationID == st.ConversationID &&
			ev.data.Record != nil && ev.data.Record.Seq == 1 && ev.data.Record.Kind == server.SupervisorRecordKindUser
	})
	after := h.waitState("new conversation turn", func(s server.SupervisorState) bool {
		return s.Lifecycle == server.SupervisorLifecycleIdle && s.HeadSeq >= 2
	})
	if after.ConversationID != st.ConversationID || after.Generation != 1 || h.invocations() != before+1 {
		t.Fatalf("state after the first new send = %+v", after)
	}
	if argv, err := os.ReadFile(filepath.Join(filepath.Dir(h.script), testutil.FakeSupervisorArgvFile)); err != nil || strings.Contains(string(argv), "--resume") {
		t.Fatalf("new conversation launch argv = %q (%v), want no --resume", argv, err)
	}
	if got := lastAssistantText(h.transcript("").Items); !strings.HasPrefix(got, "Hello from turn") {
		t.Fatalf("new conversation reply = %q", got)
	}
}

// TestSupervisorResetKeepsRunningFeature starts a real feature through the
// helper on the built server, then starts a new conversation while the
// feature's phase worker is parked on its question: the feature's session
// and state are untouched, the previous conversation directory and the
// settings stay, and the next send launches generation 1 of the new
// conversation.
func TestSupervisorResetKeepsRunningFeature(t *testing.T) {
	h := newHelperJourney(t, testutil.FakeClaudeSupervisorHelperScriptBody())
	cfg, err := json.Marshal(map[string]any{
		"models": map[string]string{
			"inquiry": h.model, "research": h.model, "planning": h.model,
			"implementation": h.model, "review": h.model, "utilities": h.model, "kb_build": h.model,
		},
		"inquireness": "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.fakeDir, testutil.FakeHelperFeatureConfigFile), cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	h.chooseSupervisor()
	h.sendAndAwait("Create and start a feature. "+testutil.FakeHelperOperate, "operate-1", 8)
	before, _ := h.supervisorState()
	calls := h.durableHelperCalls(h.durableRecords(before.ConversationID))
	if len(calls) != 3 {
		t.Fatalf("operate helper calls = %+v\n%s", calls, h.diagnostics())
	}
	var created server.CreateFeatureResponse
	if err := json.Unmarshal([]byte(calls[0].output), &created); err != nil || created.FeatureID == "" {
		t.Fatalf("create tool_result is not the create body: %q (%v)", calls[0].output, err)
	}
	featureID := created.FeatureID
	ask := h.waitAsk("the phase worker's question", func(asks []server.ControlRequest) (server.ControlRequest, bool) {
		for _, a := range asks {
			if a.FeatureID == featureID && len(a.Questions) > 0 && a.Questions[0].Question == testutil.FakeHelperPhaseQuestion {
				return a, true
			}
		}
		return server.ControlRequest{}, false
	})

	featureSnapshot := func() (server.FeatureSummary, server.SessionSummary) {
		t.Helper()
		var feature *server.FeatureSummary
		for _, f := range h.features() {
			if f.ID == featureID {
				f := f
				feature = &f
			}
		}
		if feature == nil {
			t.Fatalf("feature %s is not listed", featureID)
		}
		var sessions server.SessionListResponse
		h.get("/api/v1/sessions", &sessions)
		for _, s := range sessions.Sessions {
			if s.ID == ask.SessionID {
				return *feature, s
			}
		}
		t.Fatalf("phase session %s is not listed: %+v", ask.SessionID, sessions.Sessions)
		return server.FeatureSummary{}, server.SessionSummary{}
	}
	featureBefore, sessionBefore := featureSnapshot()
	phaseLaunches := h.fakeFile(testutil.FakeHelperPhaseInvocationsFile)
	oldDir := h.conversationDir(before.ConversationID)
	oldTranscript, err := os.ReadFile(filepath.Join(oldDir, "transcript.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	status, raw := h.request(http.MethodPost, "/api/v1/supervisor/reset", `{}`)
	if status != http.StatusOK {
		t.Fatalf("reset status = %d body %s\n%s", status, raw, h.diagnostics())
	}
	var resp server.SupervisorResetResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	st := resp.State
	if resp.Result != server.SupervisorResetDone || resp.PreviousConversationID != before.ConversationID ||
		st.ConversationID == before.ConversationID || st.Generation != 0 || st.Lifecycle != server.SupervisorLifecycleStopped ||
		st.HeadSeq != 0 || st.Settings != before.Settings {
		t.Fatalf("reset response = %+v; before %+v", resp, before)
	}

	// The feature, its parked phase session and its question are untouched.
	featureAfter, sessionAfter := featureSnapshot()
	if featureAfter.Status != featureBefore.Status || featureAfter.CurrentPhase != featureBefore.CurrentPhase ||
		featureAfter.ActiveRun != featureBefore.ActiveRun || featureAfter.RunCount != featureBefore.RunCount {
		t.Fatalf("feature after reset = %+v, before %+v", featureAfter, featureBefore)
	}
	if sessionAfter.Status != sessionBefore.Status || sessionAfter.StartedAt != sessionBefore.StartedAt {
		t.Fatalf("phase session after reset = %+v, before %+v", sessionAfter, sessionBefore)
	}
	h.waitAsk("the phase question to stay pending", func(asks []server.ControlRequest) (server.ControlRequest, bool) {
		for _, a := range asks {
			if a.RequestID == ask.RequestID && a.SessionID == ask.SessionID {
				return a, true
			}
		}
		return server.ControlRequest{}, false
	})
	if got := h.fakeFile(testutil.FakeHelperPhaseInvocationsFile); got != phaseLaunches {
		t.Fatalf("phase launches changed across the reset: %q -> %q", phaseLaunches, got)
	}

	// The previous conversation directory is kept byte for byte.
	if got, err := os.ReadFile(filepath.Join(oldDir, "transcript.jsonl")); err != nil || !bytes.Equal(got, oldTranscript) {
		t.Fatalf("previous transcript changed across the reset (err %v)", err)
	}

	// The next send launches generation 1 of the new conversation.
	after := h.sendAndAwait("Hello again", "after-reset", 2)
	if after.ConversationID != st.ConversationID || after.Generation != 1 || after.Settings != before.Settings {
		t.Fatalf("state after the first new send = %+v", after)
	}
	page, _ := h.transcript()
	if got := recordKindList(page.Items); got != "user,assistant" || lastAssistantText(page.Items) != "Ready." {
		t.Fatalf("new conversation transcript = %s (%q)", got, lastAssistantText(page.Items))
	}
	if _, err := os.Stat(filepath.Join(oldDir, "transcript.jsonl")); err != nil {
		t.Fatalf("previous conversation lost after the new send: %v", err)
	}
	featureFinal, sessionFinal := featureSnapshot()
	if featureFinal.Status != featureBefore.Status || sessionFinal.Status != sessionBefore.Status {
		t.Fatalf("feature after the new send = %+v / %+v", featureFinal, sessionFinal)
	}
}
