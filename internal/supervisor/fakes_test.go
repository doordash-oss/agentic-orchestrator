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

package supervisor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// fakeSession is a concurrency-safe session double: it records delivered
// messages, the hidden context each carried, and interrupts, and exits on
// Stop.
type fakeSession struct {
	*mocks.MockSessionView
	observer ports.SessionObserver

	mu              sync.Mutex
	sent            []string
	hidden          []string
	interrupts      int
	stops           int
	settingsUpdates int
	settingsError   error
	status          ports.SessionStatus
	done            chan struct{}
	exitOnce        sync.Once
	// ignoreStop keeps the process alive on Stop until exit is called.
	ignoreStop bool
}

func newFakeSession(id string, observer ports.SessionObserver) *fakeSession {
	return &fakeSession{
		MockSessionView: mocks.NewMockSessionView(id, FeatureID),
		observer:        observer,
		status:          ports.SessionRunning,
		done:            make(chan struct{}),
	}
}

func (f *fakeSession) SendUserMessage(text string) error {
	return f.SendUserMessageWithHiddenContext(text, "")
}

func (f *fakeSession) SendUserMessageWithHiddenContext(visible, hiddenContext string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	select {
	case <-f.done:
		return errors.New("session stdin is closed")
	default:
	}
	f.sent = append(f.sent, visible)
	f.hidden = append(f.hidden, hiddenContext)
	return nil
}

func (f *fakeSession) Interrupt() error {
	f.mu.Lock()
	f.interrupts++
	f.mu.Unlock()
	return nil
}

func (f *fakeSession) ApplySettings(_ context.Context, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settingsUpdates++
	return f.settingsError
}

func (f *fakeSession) Stop() error {
	f.mu.Lock()
	f.stops++
	ignore := f.ignoreStop
	f.mu.Unlock()
	if !ignore {
		f.exit(ports.SessionDone)
	}
	return nil
}

func (f *fakeSession) exit(status ports.SessionStatus) {
	f.exitOnce.Do(func() {
		f.mu.Lock()
		f.status = status
		f.mu.Unlock()
		close(f.done)
	})
}

func (f *fakeSession) Done() <-chan struct{} { return f.done }

func (f *fakeSession) Status() ports.SessionStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *fakeSession) IsActive() bool {
	select {
	case <-f.done:
		return false
	default:
		return true
	}
}

func (f *fakeSession) Sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

// Hidden returns the hidden context each delivered message carried, in
// delivery order; plain sends carry "".
func (f *fakeSession) Hidden() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.hidden...)
}

func (f *fakeSession) Interrupts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.interrupts
}

func (f *fakeSession) Stops() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stops
}

// emit delivers one provider message through the session observer.
func (f *fakeSession) emit(msg llm.SDKMessage) {
	f.observer.ObserveSessionMessage(f.IDVal, msg)
}

func (f *fakeSession) answer(answer ports.ControlAnswer) {
	f.observer.ObserveControlAnswer(f.IDVal, answer)
}

// fakeLauncher records launches and returns fake sessions that complete
// their handshake synchronously unless configured otherwise.
type fakeLauncher struct {
	mu       sync.Mutex
	requests []LaunchRequest
	sessions []*fakeSession
	// gate, when set, blocks Launch until closed.
	gate chan struct{}
	// failNext fails that many upcoming launches.
	failNext int
	// silent sessions never answer the handshake.
	silent bool
	// exitBeforeHandshake sessions exit without answering the handshake.
	exitBeforeHandshake bool
	// plain sessions cannot carry hidden context.
	plain bool
	// init, when set, scripts the init message each launch reports.
	init func(LaunchRequest) *llm.SystemInitMessage
}

// plainSession hides the fake's hidden-context capability.
type plainSession struct{ ports.SessionView }

func (l *fakeLauncher) Launch(_ context.Context, req LaunchRequest) (ports.SessionView, error) {
	l.mu.Lock()
	l.requests = append(l.requests, req)
	gate := l.gate
	fail := l.failNext > 0
	if fail {
		l.failNext--
	}
	silent, exitEarly, plain, initFor := l.silent, l.exitBeforeHandshake, l.plain, l.init
	l.mu.Unlock()
	if gate != nil {
		<-gate
	}
	if fail {
		return nil, errors.New("scripted launch failure")
	}
	sess := newFakeSession(req.SessionID, req.Observer)
	if req.OnSpawned != nil {
		req.OnSpawned()
	}
	l.mu.Lock()
	l.sessions = append(l.sessions, sess)
	l.mu.Unlock()
	switch {
	case exitEarly:
		sess.exit(ports.SessionFailed)
	case !silent:
		sess.emit(llm.SDKMessage{Type: "control_response"})
		init := &llm.SystemInitMessage{Model: "haiku-effective"}
		if initFor != nil {
			init = initFor(req)
		}
		sess.emit(llm.SDKMessage{Type: "system", Subtype: "init", Init: init})
	}
	if plain {
		return plainSession{sess}, nil
	}
	return sess, nil
}

func (l *fakeLauncher) launchCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.requests)
}

func (l *fakeLauncher) session(i int) *fakeSession {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sessions[i]
}

func (l *fakeLauncher) request(i int) LaunchRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.requests[i]
}

// acceptAllCatalog accepts every settings choice except a model named
// "missing" or an effort named "bogus".
type acceptAllCatalog struct{}

func (acceptAllCatalog) ValidateSettings(s Settings) error {
	if s.Model == "missing" {
		return &SettingsInvalidError{Reason: "model is not available"}
	}
	if s.Effort == "bogus" {
		return &SettingsInvalidError{Reason: "effort is not supported"}
	}
	return nil
}

func newTestCoordinator(t *testing.T, stateDir string, launcher Launcher, mutate ...func(*Options)) *Coordinator {
	t.Helper()
	opts := Options{
		StateDir:         stateDir,
		Catalog:          acceptAllCatalog{},
		Launcher:         launcher,
		HandshakeTimeout: 2 * time.Second,
		InterruptGrace:   time.Second,
	}
	for _, fn := range mutate {
		fn(&opts)
	}
	c, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func chooseSettings(t *testing.T, c *Coordinator) {
	t.Helper()
	if _, err := c.UpdateSettings(Settings{Harness: "claude", Model: "haiku"}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitLifecycle(t *testing.T, c *Coordinator, want Lifecycle) State {
	t.Helper()
	var st State
	waitFor(t, "lifecycle "+string(want), func() bool {
		st = c.State()
		return st.Lifecycle == want
	})
	return st
}

func successResult() llm.SDKMessage {
	return llm.SDKMessage{Type: "result", Subtype: "success", Result: &llm.ResultMessage{Subtype: "success"}}
}

func assistantText(id, text string) llm.SDKMessage {
	return llm.SDKMessage{Type: "assistant", Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{
		ID: id, Role: "assistant", Content: []llm.ContentBlock{{Type: "text", Text: text}},
	}}}
}
