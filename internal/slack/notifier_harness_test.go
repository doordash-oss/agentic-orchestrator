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

package slack

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
	"gopkg.in/yaml.v3"
)

// fakeClock advances instantly: Sleep moves Now and records the wait, so
// pacing, rate-limit pauses, and backoff run without real sleeping.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	waits []time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(_ context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.waits = append(c.waits, d)
	c.mu.Unlock()
	return true
}

func (c *fakeClock) recordedWaits() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.waits...)
}

// fakeSettings is the mutable live-configuration source.
type fakeSettings struct {
	mu       sync.Mutex
	settings ports.SlackRuntimeSettings
}

func newFakeSettings(settings ports.SlackRuntimeSettings) *fakeSettings {
	return &fakeSettings{settings: settings}
}

func (s *fakeSettings) SlackSettings() ports.SlackRuntimeSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

func (s *fakeSettings) mutate(fn func(*ports.SlackRuntimeSettings)) {
	s.mu.Lock()
	fn(&s.settings)
	s.mu.Unlock()
}

// fakeObserver collects emitted observability events.
type fakeObserver struct {
	mu     sync.Mutex
	events []observe.Event
}

type fakePendingInputSource struct {
	mu    sync.Mutex
	items map[string][]ports.SlackPendingInput
	calls map[string]int
}

func newFakePendingInputSource() *fakePendingInputSource {
	return &fakePendingInputSource{
		items: map[string][]ports.SlackPendingInput{},
		calls: map[string]int{},
	}
}

func (s *fakePendingInputSource) PendingSlackInputs(featureID string) ([]ports.SlackPendingInput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[featureID]++
	return append([]ports.SlackPendingInput(nil), s.items[featureID]...), nil
}

func (s *fakePendingInputSource) set(featureID string, items ...ports.SlackPendingInput) {
	s.mu.Lock()
	s.items[featureID] = append([]ports.SlackPendingInput(nil), items...)
	s.mu.Unlock()
}

func (o *fakeObserver) Emit(evt observe.Event) error {
	o.mu.Lock()
	o.events = append(o.events, evt)
	o.mu.Unlock()
	return nil
}

func (o *fakeObserver) ofKind(kind string) []observe.Event {
	o.mu.Lock()
	defer o.mu.Unlock()
	var result []observe.Event
	for _, evt := range o.events {
		if evt.EventType == kind {
			result = append(result, evt)
		}
	}
	return result
}

func (o *fakeObserver) all() []observe.Event {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]observe.Event(nil), o.events...)
}

func allCategories() ports.SlackCategoryDefaults {
	return ports.SlackCategoryDefaults{Progress: true, NeedsInput: true, Problems: true}
}

func defaultTestSettings(token string, recipients ...ports.SlackRecipient) ports.SlackRuntimeSettings {
	return ports.SlackRuntimeSettings{
		Enabled:    true,
		Token:      token,
		Recipients: recipients,
		Categories: allCategories(),
	}
}

func testRecipients() []ports.SlackRecipient {
	return []ports.SlackRecipient{
		{TypedText: "@ada", Kind: ports.SlackRecipientUser, ID: "U-ADA", DisplayName: "Ada Lovelace"},
		{TypedText: "#eng", Kind: ports.SlackRecipientChannel, ID: "C-ENG", DisplayName: "#eng"},
	}
}

// defaultOKResponder answers unscripted lifecycle calls with fresh
// timestamps so tests need not pre-count posts.
func defaultOKResponder() (func(string, testsupport.Request) testsupport.Response, *atomic.Int64) {
	var counter atomic.Int64
	return func(method string, request testsupport.Request) testsupport.Response {
		switch method {
		case "conversations.open":
			return testsupport.Response{Body: map[string]any{
				"ok": true, "channel": map[string]any{"id": "D-" + fmt.Sprint(request.Fields["users"])},
			}}
		case "chat.postMessage", "chat.update":
			n := counter.Add(1)
			return testsupport.Response{Body: map[string]any{
				"ok":      true,
				"ts":      fmt.Sprintf("1758499200.%06d", n),
				"channel": fmt.Sprint(request.Fields["channel"]),
			}}
		default:
			return testsupport.Response{Body: map[string]any{"ok": false, "error": "unexpected " + method}}
		}
	}, &counter
}

// notifierHarness wires a notifier against the fake server with fakes for
// every dependency.
type notifierHarness struct {
	t        *testing.T
	server   *testsupport.Server
	store    *feature.Store
	stateDir string
	settings *fakeSettings
	clock    *fakeClock
	observer *fakeObserver
	pending  *fakePendingInputSource
	notifier *Notifier
	jitter   *atomic.Int64 // deterministic backoff jitter source
	jitterFn func() float64
}

func newNotifierHarness(t *testing.T, settings ports.SlackRuntimeSettings) *notifierHarness {
	t.Helper()
	server := testsupport.NewServer()
	t.Cleanup(server.Close)
	responder, _ := defaultOKResponder()
	server.SetDefault(responder)

	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)
	harness := &notifierHarness{
		t:        t,
		server:   server,
		store:    store,
		stateDir: stateDir,
		settings: newFakeSettings(settings),
		clock:    newFakeClock(),
		observer: &fakeObserver{},
		pending:  newFakePendingInputSource(),
	}
	return harness
}

func (h *notifierHarness) newNotifier(queueCapacity int) *Notifier {
	jitterCalls := &atomic.Int64{}
	jitterFn := func() float64 {
		n := jitterCalls.Add(1)
		switch n % 2 {
		case 1:
			return 0.1
		default:
			return 0.9
		}
	}
	h.jitter = jitterCalls
	h.jitterFn = jitterFn
	notifier := NewNotifier(NotifierOptions{
		Settings:      h.settings,
		Store:         h.store,
		StateDir:      h.stateDir,
		Observer:      h.observer,
		Pending:       h.pending,
		QueueCapacity: queueCapacity,
		Clock:         h.clock,
		Jitter:        jitterFn,
		NewClient: func(token string) (slackClient, error) {
			return NewClient(token, WithBaseURL(h.server.URL()))
		},
	})
	notifier.SetServerName("Local agent")
	return notifier
}

func (h *notifierHarness) start(queueCapacity int) *Notifier {
	h.notifier = h.newNotifier(queueCapacity)
	h.notifier.Start()
	h.t.Cleanup(func() { h.notifier.Stop(context.Background()) })
	return h.notifier
}

// seedFeature stores a feature record the notifier can load.
func (h *notifierHarness) seedFeature(id string, mutate func(*feature.Feature)) *feature.Feature {
	h.t.Helper()
	f := &feature.Feature{
		ID:            id,
		Name:          "Slack pane polish",
		Slug:          "slack-pane-polish",
		Description:   "test feature",
		Created:       h.clock.Now().Add(-time.Hour),
		Status:        feature.StatusImplementing,
		CurrentPhase:  feature.PhaseImplement,
		SchemaVersion: feature.SchemaVersionCurrent,
		Pipeline:      feature.PipelineMoonshot,
		Repos: []feature.FeatureRepo{
			{Name: "alpha"}, {Name: "beta"},
		},
	}
	if mutate != nil {
		mutate(f)
	}
	if err := h.store.Save(f); err != nil {
		h.t.Fatalf("seed feature %s: %v", id, err)
	}
	return f
}

func withRoadmap(f *feature.Feature) {
	f.CurrentRoadmapPhase = 1
	f.TotalRoadmapPhases = 2
}

// feed delivers a domain event through the real tap.
func (h *notifierHarness) feed(ev ports.Event) {
	h.notifier.DomainEventTap(ev)
}

// waitFor polls until cond holds or the deadline passes.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

// postsTo returns the chat.postMessage requests whose channel matches.
func postsTo(server *testsupport.Server, channel string) []testsupport.Request {
	var result []testsupport.Request
	for _, request := range server.Requests("chat.postMessage") {
		if fmt.Sprint(request.Fields["channel"]) == channel {
			result = append(result, request)
		}
	}
	return result
}

func fieldString(request testsupport.Request, key string) string {
	value, ok := request.Fields[key]
	if !ok {
		return ""
	}
	return fmt.Sprint(value)
}

func requestBlocks(request testsupport.Request) []map[string]any {
	raw, _ := request.Fields["blocks"].(string)
	if raw == "" {
		return nil
	}
	var blocks []map[string]any
	if err := yaml.Unmarshal([]byte(raw), &blocks); err != nil {
		return nil
	}
	return blocks
}

// recordDestinations decodes the durable record file for a feature.
func recordDestinations(t *testing.T, stateDir, featureID string) map[string]destinationRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(stateDir, featureID, recordFilename))
	if err != nil {
		t.Fatalf("read record for %s: %v", featureID, err)
	}
	var record featureRecord
	if err := yaml.Unmarshal(data, &record); err != nil {
		t.Fatalf("decode record for %s: %v", featureID, err)
	}
	return record.Destinations
}

// recordExists reports whether the durable record file for a feature is
// present.
func recordExists(stateDir, featureID string) bool {
	_, err := os.Stat(filepath.Join(stateDir, featureID, recordFilename))
	return err == nil
}

// recordLedger reads one destination's ledger, reporting false when the
// record file is absent or unreadable so callers can poll safely.
func recordLedger(stateDir, featureID, key string) ([]string, bool) {
	data, err := os.ReadFile(filepath.Join(stateDir, featureID, recordFilename))
	if err != nil {
		return nil, false
	}
	var record featureRecord
	if err := yaml.Unmarshal(data, &record); err != nil {
		return nil, false
	}
	entry, ok := record.Destinations[key]
	if !ok {
		return nil, false
	}
	return entry.Ledger, true
}

// captureLogs swaps the standard logger for the test and returns whatever
// was written.
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return buf
}

type syncBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
