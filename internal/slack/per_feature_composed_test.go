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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

const perFeatureSecret = "xoxp-per-feature-credential-987654"

// Only the two mutations exercised here are adapted; their validation and
// routing still pass through the real HTTP handler.
type perFeatureMutationTarget struct {
	serverruntime.MutationTarget
	manager *feature.Manager
	orch    *orchestrator.Orchestrator
	store   *feature.Store
	token   string
}

func (m *perFeatureMutationTarget) CreateFeature(req serverruntime.CreateFeatureRequest) (serverruntime.CreateFeatureResponse, error) {
	f, err := m.manager.Create(req.Name, req.Description, req.Repos, req.Models,
		req.ExitCriteria, req.Inquireness, req.Images, feature.CreateOptions{
			Pipeline: feature.PipelineMoonshot, QueueSetup: true,
			SlackNotifications: serverruntime.SanitizeSlackNotifications(
				serverruntime.PatchSlackNotifications(nil, req.SlackNotifications), m.token),
		})
	if err != nil {
		return serverruntime.CreateFeatureResponse{}, err
	}
	return serverruntime.CreateFeatureResponse{FeatureID: f.ID, Result: "created"}, nil
}

func (m *perFeatureMutationTarget) UpdateFeatureConfig(id string, req serverruntime.FeatureConfigMutationRequest) (serverruntime.FeatureConfigUpdateResponse, error) {
	f, err := m.store.Load(id)
	if err != nil {
		return serverruntime.FeatureConfigUpdateResponse{}, err
	}
	err = m.orch.UpdateFeatureConfig(id, orchestrator.UpdateFeatureConfigInput{
		Models: f.Models, Effort: f.Effort, Inquireness: f.Inquireness,
		Checkpoints: f.Checkpoints, InputNotifications: f.InputNotifications,
		AutomaticReviewMode: f.AutomaticReviewMode,
		SlackNotifications: serverruntime.SanitizeSlackNotifications(
			serverruntime.PatchSlackNotifications(f.SlackNotifications, req.SlackNotifications), m.token),
	})
	if err != nil {
		return serverruntime.FeatureConfigUpdateResponse{}, err
	}
	return serverruntime.FeatureConfigUpdateResponse{FeatureID: id, Result: "updated"}, nil
}

type perFeatureStep struct {
	Name       string                        `json:"name"`
	First      feature.EffectiveSlack        `json:"first_effective"`
	Second     feature.EffectiveSlack        `json:"second_effective"`
	FirstState responderEvidenceTick         `json:"first_record_state"`
	OtherState responderEvidenceTick         `json:"second_record_state"`
	Requests   int                           `json:"request_count"`
	Answers    []responderEvidenceSubmission `json:"answer_submissions"`
}

type perFeatureTranscript struct {
	Requests     []responderEvidenceRequest    `json:"requests"`
	Steps        []perFeatureStep              `json:"steps"`
	Submissions  []responderEvidenceSubmission `json:"submissions"`
	FinalRecord  *featureRecord                `json:"final_slack_record"`
	FeatureSlack *feature.SlackNotifications   `json:"feature_slack_section"`
	OtherRecord  *featureRecord                `json:"second_slack_record"`
	StateStable  bool                          `json:"feature_state_unchanged_outside_slack"`
	DomainEvents []string                      `json:"domain_events"`
	Order        string                        `json:"request_order"`
}

type perFeatureJourney struct {
	t             *testing.T
	h             *notifierHarness
	handler       http.Handler
	n             *Notifier
	clock         *manualResponderClock
	answer        *responderEvidenceAnswerPort
	id            string
	other         string
	steps         []perFeatureStep
	events        []string
	requests      []responderEvidenceRequest
	requestOffset int
}

func newPerFeatureJourney(t *testing.T) *perFeatureJourney {
	t.Helper()
	global := testRecipients()[0]
	h := newNotifierHarness(t, defaultTestSettings(testToken, global))
	cfg := config.NewDefault()
	cfg.Slack = &config.SlackConfig{
		Enabled: true, Token: testToken,
		DefaultRecipients: []config.SlackRecipient{{
			TypedText: global.TypedText, Kind: string(global.Kind),
			ID: global.ID, DisplayName: global.DisplayName,
		}},
	}
	orch := orchestrator.New(orchestrator.Deps{
		Lifecycle: feature.NewManager(h.store, cfg), Store: h.store,
	}, orchestrator.Hooks{})
	target := &perFeatureMutationTarget{
		manager: feature.NewManager(h.store, cfg), orch: orch,
		store: h.store, token: testToken,
	}
	handler := serverruntime.NewHandler(serverruntime.HandlerOptions{
		Runtime: serverruntime.RuntimeIdentity{StateDir: h.stateDir},
		Config:  cfg, Features: h.store, FeatureStore: h.store,
		Mutations: target, DisableHostValidation: true,
	})
	// Generated feature IDs are lowercase hex; keep responder thread order stable.
	return &perFeatureJourney{t: t, h: h, handler: handler, other: "z-other"}
}

func (j *perFeatureJourney) request(method, path string, body any) map[string]any {
	j.t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		j.t.Fatal(err)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Agentico-Client", "local")
	w := httptest.NewRecorder()
	j.handler.ServeHTTP(w, r)
	if w.Code < 200 || w.Code >= 300 {
		j.t.Fatalf("%s %s: status %d, body %s", method, path, w.Code, w.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		j.t.Fatal(err)
	}
	return response
}

func (j *perFeatureJourney) configure(section map[string]any) {
	j.t.Helper()
	j.request(http.MethodPost, "/api/v1/features/"+j.id+"/config",
		map[string]any{"slack_notifications": section})
}

func (j *perFeatureJourney) create() {
	j.t.Helper()
	response := j.request(http.MethodPost, "/api/v1/features",
		map[string]any{"name": "Per feature evidence", "slack_notifications": map[string]any{}})
	j.id, _ = response["feature_id"].(string)
	if j.id == "" {
		j.t.Fatalf("creation response lacks feature ID: %+v", response)
	}
	// Stand in for setup/phase execution; Slack never changes these fields.
	if err := j.h.store.Modify(j.id, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.CurrentPhase = feature.PhaseImplement
		f.Created = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
		return nil
	}); err != nil {
		j.t.Fatal(err)
	}
	j.h.server.SetOwnUserID("U-BOT")
	sequence := map[string]int{}
	j.h.server.SetDefault(func(method string, request testsupport.Request) testsupport.Response {
		switch method {
		case "users.info":
			return responderEvidenceUserInfo("U-GRACE", "Grace Hopper")
		case "conversations.open":
			return testsupport.Response{Body: map[string]any{
				"ok": true, "channel": map[string]any{"id": "D-U-ADA"},
			}}
		case "chat.postMessage", "chat.update":
			channel := fieldString(request, "channel")
			base := "1758499200"
			if channel == "C-ENG" {
				base = "1758499201"
			}
			ts := base + ".999999"
			if method == "chat.postMessage" {
				sequence[channel]++
				ts = fmt.Sprintf("%s.%06d", base, sequence[channel])
			}
			return testsupport.Response{Body: map[string]any{
				"ok": true, "ts": ts,
				"channel": channel,
			}}
		default:
			return testsupport.Response{Body: map[string]any{
				"ok": false, "error": "unexpected " + method,
			}}
		}
	})
	j.clock = newManualResponderClock()
	j.answer = &responderEvidenceAnswerPort{}
	j.n = NewNotifier(NotifierOptions{
		Settings: j.h.settings, Store: j.h.store, StateDir: j.h.stateDir,
		Observer: j.h.observer, Pending: j.h.pending, Answer: j.answer,
		Clock: j.h.clock, ResponderClock: j.clock, QueueCapacity: 64,
		NewClient: func(token string) (slackClient, error) {
			return NewClient(token, WithBaseURL(j.h.server.URL()))
		},
	})
	j.n.SetServerName("Local agent")
	j.n.Start()
	j.t.Cleanup(func() { j.n.Stop(context.Background()) })
	j.n.SignalReady()
	waitFor(j.t, 5*time.Second, func() bool { return j.n.startupDone.Load() })
}

func (j *perFeatureJourney) feed(ev ports.Event) {
	j.t.Helper()
	j.events = append(j.events, fmt.Sprint(ev.Type)+":"+j.featureLabel(ev.FeatureID))
	j.n.DomainEventTap(ev)
}

func (j *perFeatureJourney) featureLabel(id string) string {
	if id == j.id {
		return "first"
	}
	return "second"
}

func (j *perFeatureJourney) record(id string) *featureRecord {
	j.t.Helper()
	record, err := loadFeatureRecord(j.h.stateDir, id)
	if err != nil {
		j.t.Fatal(err)
	}
	return record
}

func (j *perFeatureJourney) snapshot(name string) {
	j.t.Helper()
	first, err := j.h.store.Load(j.id)
	if err != nil {
		j.t.Fatal(err)
	}
	second, err := j.h.store.Load(j.other)
	if err != nil {
		j.t.Fatal(err)
	}
	global := feature.SlackGlobalSettings{
		Enabled: true, HasToken: true, Progress: true, NeedsInput: true, Problems: true,
		Recipients: []feature.SlackRecipient{{
			TypedText: "@ada", Kind: "user", ID: "U-ADA", DisplayName: "Ada Lovelace",
		}},
	}
	state := func(id string) responderEvidenceTick {
		record := j.record(id)
		keys := make([]string, 0, len(record.Destinations))
		for key := range record.Destinations {
			keys = append(keys, key)
		}
		// The record retains removed destinations; all ledgers are evidence.
		sort.Strings(keys)
		ledgers := make([]responderEvidenceLedger, 0, len(keys))
		for _, key := range keys {
			d := record.Destinations[key]
			ledgers = append(ledgers, responderEvidenceLedger{
				DestinationKey: key, MessageTimestamps: d.Ledger,
				IntegrationReactions: d.IntegrationReactions, PostingIndex: d.PostingIndex,
			})
		}
		return responderEvidenceTick{
			Name: name, Pending: responderEvidenceInputs(record.Pending),
			Resolved: responderEvidenceInputs(record.Resolved), Ledgers: ledgers,
		}
	}
	batch := responderEvidenceRequests(j.h.server.AllRequests()[j.requestOffset:])
	for i := range batch {
		batch[i].Index = len(j.requests) + i
	}
	j.requests = append(j.requests, batch...)
	j.requestOffset += len(batch)
	j.steps = append(j.steps, perFeatureStep{
		Name: name, First: feature.ResolveSlack(global, first.SlackNotifications),
		Second:     feature.ResolveSlack(global, second.SlackNotifications),
		FirstState: state(j.id), OtherState: state(j.other),
		Requests: len(j.requests), Answers: j.answer.all(),
	})
}

func perFeatureRequestCount(server *testsupport.Server, method, channel, prefix string) int {
	count := 0
	for _, request := range server.Requests(method) {
		if fieldString(request, "channel") == channel &&
			strings.HasPrefix(fieldString(request, "text"), prefix) {
			count++
		}
	}
	return count
}

func (j *perFeatureJourney) tick() {
	j.t.Helper()
	j.clock.tick(j.t)
	waitFor(j.t, 5*time.Second, func() bool { return len(j.clock.sleeps) == 1 })
}

func (j *perFeatureJourney) editAndSweep(section map[string]any) {
	j.t.Helper()
	j.configure(section)
	j.n.sweepDestinations()
}

func (j *perFeatureJourney) drain() {
	j.t.Helper()
	lastCount := -1
	stableSince := time.Now()
	waitFor(j.t, 5*time.Second, func() bool {
		count := len(j.h.server.AllRequests())
		if count != lastCount {
			lastCount = count
			stableSince = time.Now()
		}
		if j.n.queue.len() != 0 {
			return false
		}
		j.n.workerMu.Lock()
		defer j.n.workerMu.Unlock()
		for _, worker := range j.n.workers {
			worker.mu.Lock()
			busy := len(worker.inbox) != 0 || len(worker.dirty) != 0
			worker.mu.Unlock()
			if busy {
				return false
			}
		}
		return time.Since(stableSince) >= 100*time.Millisecond
	})
}

func runPerFeatureJourney(t *testing.T) []byte {
	t.Helper()
	j := newPerFeatureJourney(t)
	j.create()
	review := responderEvidenceReview(
		"review-1", "sha256:per-feature-review", []byte("# Plan\n\nApprove this gate.\n"))
	review.FeatureID = j.id
	j.h.pending.set(j.id, review)
	j.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: j.id})
	j.feed(ports.Event{Type: ports.ReviewRequired, FeatureID: j.id})
	waitFor(t, 5*time.Second, func() bool {
		first := j.record(j.id)
		return len(first.Pending) == 1 && first.Pending[0].MessageTS["user:U-ADA"] != ""
	})
	j.drain()
	j.h.seedFeature(j.other, func(f *feature.Feature) {
		f.Name = "Independent feature"
	})
	otherPermission := responderEvidencePermission("other-permission", "go test ./...")
	otherPermission.FeatureID = j.other
	j.h.pending.set(j.other, otherPermission)
	j.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: j.other})
	j.n.RuntimeMessageTap(controlRuntimeMessage(j.other, "other-session", "other-permission"))
	waitFor(t, 5*time.Second, func() bool {
		first, other := j.record(j.id), j.record(j.other)
		return len(first.Pending) == 1 && len(other.Pending) == 1 &&
			first.Pending[0].MessageTS["user:U-ADA"] != "" &&
			other.Pending[0].MessageTS["user:U-ADA"] != "" &&
			perFeatureRequestCount(j.h.server, "chat.update", "D-U-ADA", "Independent feature") >= 1
	})
	j.drain()
	j.snapshot("seeded")
	initialOther := j.record(j.other)
	firstSeed := j.record(j.id)
	for _, seeded := range []struct {
		record  *featureRecord
		subtype string
	}{
		{firstSeed, "file_share"},
		{initialOther, ""},
	} {
		destination := seeded.record.Destinations["user:U-ADA"]
		j.h.server.SeedThread("D-U-ADA", destination.RootTS, []testsupport.Message{
			{TS: destination.RootTS, User: "U-BOT"},
			{TS: seeded.record.Pending[0].MessageTS["user:U-ADA"],
				ThreadTS: destination.RootTS, User: "U-BOT", Subtype: seeded.subtype},
		})
	}
	featureBefore, err := j.h.store.Load(j.id)
	if err != nil {
		t.Fatal(err)
	}
	featureBefore.SlackNotifications = nil
	featureBefore.PersistSeq = 0
	before, err := json.Marshal(featureBefore)
	if err != nil {
		t.Fatal(err)
	}

	extra := map[string]any{
		"typed_text": "#eng " + perFeatureSecret,
		"kind":       "channel", "id": "C-ENG",
		"display_name": "Engineering " + perFeatureSecret,
	}
	j.editAndSweep(map[string]any{"recipients": []any{extra}})
	waitFor(t, 5*time.Second, func() bool {
		r := j.record(j.id)
		return r.Destinations["channel:C-ENG"].RootTS != "" &&
			len(r.Pending) == 1 && r.Pending[0].MessageTS["channel:C-ENG"] != ""
	})
	if perFeatureRequestCount(j.h.server, "chat.postMessage", "C-ENG", "") != 2 {
		t.Fatal("added channel must receive exactly one card and one pending review")
	}
	j.drain()
	j.snapshot("added_channel")
	addedYAML, err := os.ReadFile(filepath.Join(j.h.stateDir, j.id, "feature.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	addedProjections, err := json.Marshal([]any{
		j.request(http.MethodGet, "/api/v1/features/"+j.id+"/config", nil),
		j.request(http.MethodGet, "/api/v1/features/"+j.id, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, surface := range [][]byte{addedYAML, addedProjections} {
		if !bytes.Contains(surface, []byte("Engineering")) ||
			bytes.Contains(surface, []byte(testToken)) ||
			bytes.Contains(surface, []byte(perFeatureSecret)) {
			t.Fatal("added recipient persistence or projection lost its harmless name or leaked credentials")
		}
	}

	j.editAndSweep(map[string]any{"progress": "off"})
	j.drain()
	phasePosts := len(postsTo(j.h.server, "C-ENG"))
	j.h.server.SetRequestOrder([]testsupport.OrderedRequest{
		{Method: "chat.update", Channel: "D-U-ADA", TextPrefix: "Per feature evidence"},
		{Method: "chat.update", Channel: "C-ENG", TextPrefix: "Per feature evidence"},
	})
	j.feed(startedEvent(j.id, feature.PhaseResearch))
	j.drain()
	if remaining := j.h.server.OrderedRequestsRemaining(); remaining != 0 {
		t.Fatalf("progress-off refresh schedule left %d requests", remaining)
	}
	if got := len(postsTo(j.h.server, "C-ENG")); got != phasePosts {
		t.Fatalf("progress-off phase posted %d channel messages; want %d", got, phasePosts)
	}
	j.snapshot("progress_phase_suppressed")
	channelUpdates := perFeatureRequestCount(j.h.server, "chat.update", "C-ENG", "Per feature evidence")
	userUpdates := perFeatureRequestCount(j.h.server, "chat.update", "D-U-ADA", "Per feature evidence")
	j.h.server.SetRequestOrder([]testsupport.OrderedRequest{
		{Method: "chat.postMessage", Channel: "D-U-ADA"},
		{Method: "chat.postMessage", Channel: "C-ENG"},
		{Method: "chat.update", Channel: "D-U-ADA", TextPrefix: "Per feature evidence"},
		{Method: "chat.update", Channel: "C-ENG", TextPrefix: "Per feature evidence"},
	})
	failure := errcat.New(errcat.InternalError)
	j.feed(ports.Event{Type: ports.FeatureFailed, FeatureID: j.id, CanonicalError: &failure})
	waitFor(t, 5*time.Second, func() bool {
		return len(postsTo(j.h.server, "C-ENG")) == phasePosts+1 &&
			perFeatureRequestCount(j.h.server, "chat.update", "C-ENG", "Per feature evidence") > channelUpdates &&
			perFeatureRequestCount(j.h.server, "chat.update", "D-U-ADA", "Per feature evidence") > userUpdates
	})
	j.drain()
	if remaining := j.h.server.OrderedRequestsRemaining(); remaining != 0 {
		t.Fatalf("failure write schedule left %d requests", remaining)
	}
	j.snapshot("progress_off_failure_delivered")

	j.h.server.SetRequestOrder([]testsupport.OrderedRequest{
		{Method: "chat.update", Channel: "D-U-ADA", TextPrefix: "Per feature evidence"},
		{Method: "chat.update", Channel: "C-ENG", TextPrefix: "Per feature evidence"},
	})
	beforeMuteRequests := len(j.h.server.AllRequests())
	j.editAndSweep(map[string]any{"mode": "muted"})
	pausedCount := func() int {
		paused := 0
		for _, request := range j.h.server.Requests("chat.update") {
			if strings.Contains(fieldString(request, "text"), "Updates are paused") {
				paused++
			}
		}
		return paused
	}
	deadline := time.Now().Add(2 * time.Second)
	for pausedCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	paused := pausedCount()
	if paused != 2 {
		t.Fatalf("paused card edits = %d; want exactly two", paused)
	}
	if remaining := j.h.server.OrderedRequestsRemaining(); remaining != 0 {
		t.Fatalf("paused card schedule left %d requests", remaining)
	}
	j.drain()
	for _, request := range j.h.server.AllRequests()[beforeMuteRequests:] {
		if strings.TrimPrefix(request.Path, "/api/") == "chat.update" &&
			strings.HasPrefix(fieldString(request, "text"), "Per feature evidence") &&
			!strings.Contains(fieldString(request, "text"), "Updates are paused") {
			t.Fatalf("ordinary first-feature card update escaped after mute on %s",
				fieldString(request, "channel"))
		}
	}
	j.snapshot("muted")

	r := j.record(j.id)
	channel := r.Destinations["channel:C-ENG"]
	reviewTS := r.Pending[0].MessageTS["channel:C-ENG"]
	j.h.server.SeedThread("C-ENG", channel.RootTS, []testsupport.Message{
		{TS: channel.RootTS, User: "U-BOT"},
		{TS: reviewTS, ThreadTS: channel.RootTS, User: "U-BOT", Subtype: "file_share",
			Reactions: []testsupport.Reaction{{
				Name: "white_check_mark", Count: 1, Users: []string{"U-GRACE"},
			}}},
	})
	j.h.server.SetRequestOrder([]testsupport.OrderedRequest{
		{Method: "chat.postMessage", Channel: "D-U-ADA", TextPrefix: "#1 was approved"},
		{Method: "chat.postMessage", Channel: "C-ENG", TextPrefix: "#1 was approved"},
	})
	j.tick()
	waitFor(t, 5*time.Second, func() bool { return len(j.answer.all()) == 1 })
	if got := j.answer.all()[0]; got.ReviewID != "review-1" ||
		got.Outcome != ports.SlackAnswerAccepted {
		t.Fatalf("muted review approval = %+v", got)
	}
	waitFor(t, 5*time.Second, func() bool {
		return perFeatureRequestCount(j.h.server, "chat.postMessage", "C-ENG", "#1 was approved") == 1 &&
			perFeatureRequestCount(j.h.server, "chat.postMessage", "D-U-ADA", "#1 was approved") == 1
	})
	if remaining := j.h.server.OrderedRequestsRemaining(); remaining != 0 {
		t.Fatalf("approval write schedule left %d requests", remaining)
	}
	j.h.pending.set(j.id)
	j.drain()
	j.snapshot("muted_approval")
	if edits := perFeatureRequestCount(j.h.server, "chat.update", "D-U-ADA", "Independent feature"); edits != 1 {
		t.Fatalf("first feature transitions edited the other card %d times; want its initial edit only", edits)
	}

	otherDestination := initialOther.Destinations["user:U-ADA"]
	otherTS := initialOther.Pending[0].MessageTS["user:U-ADA"]
	j.h.server.SeedThread("D-U-ADA", otherDestination.RootTS, []testsupport.Message{
		{TS: otherDestination.RootTS, User: "U-BOT"},
		{TS: otherTS, ThreadTS: otherDestination.RootTS, User: "U-BOT",
			Reactions: []testsupport.Reaction{{
				Name: "white_check_mark", Count: 1, Users: []string{"U-GRACE"},
			}}},
	})
	j.tick()
	waitFor(t, 5*time.Second, func() bool { return len(j.answer.all()) == 2 })
	if got := j.answer.all()[1]; got.RequestID != "other-permission" ||
		got.Outcome != ports.SlackAnswerAccepted {
		t.Fatalf("other feature was not answerable during mute: %+v", got)
	}
	j.h.pending.set(j.other)
	j.drain()
	j.snapshot("other_feature_answered_while_first_muted")

	j.editAndSweep(map[string]any{"recipients": []any{}})
	channelRequests := len(postsTo(j.h.server, "C-ENG"))
	channelPolls := perFeatureRequestCount(j.h.server, "conversations.replies", "C-ENG", "")
	j.h.server.SeedThread("C-ENG", channel.RootTS, []testsupport.Message{
		{TS: channel.RootTS, User: "U-BOT"},
		{TS: reviewTS, ThreadTS: channel.RootTS, User: "U-BOT", Subtype: "file_share"},
		{TS: "1758499202.900001", ThreadTS: channel.RootTS,
			User: "U-GRACE", Text: "approve",
			Reactions: []testsupport.Reaction{{
				Name: "white_check_mark", Count: 1, Users: []string{"U-GRACE"},
			}}},
	})
	j.tick()
	if len(postsTo(j.h.server, "C-ENG")) != channelRequests ||
		perFeatureRequestCount(j.h.server, "conversations.replies", "C-ENG", "") != channelPolls ||
		len(j.answer.all()) != 2 {
		t.Fatal("removed channel was polled, written, or answered")
	}
	j.drain()
	j.snapshot("removed_channel")

	quietPermission := responderEvidencePermission("quiet-permission", "go test ./...")
	quietPermission.FeatureID = j.id
	j.h.pending.set(j.id, quietPermission)
	userRoot := j.record(j.id).Destinations["user:U-ADA"].RootTS
	firstThreadPosts := 0
	for _, post := range postsTo(j.h.server, "D-U-ADA") {
		if fieldString(post, "thread_ts") == userRoot {
			firstThreadPosts++
		}
	}
	j.tick()
	afterQuietPosts := 0
	for _, post := range postsTo(j.h.server, "D-U-ADA") {
		if fieldString(post, "thread_ts") == userRoot {
			afterQuietPosts++
		}
	}
	if afterQuietPosts != firstThreadPosts ||
		len(postsTo(j.h.server, "C-ENG")) != channelRequests {
		t.Fatal("muted or removed destination received a new pending item")
	}
	j.snapshot("quiet_item_pending")
	unmuteUpdates := perFeatureRequestCount(j.h.server, "chat.update", "D-U-ADA", "Per feature evidence")
	j.editAndSweep(map[string]any{"mode": "inherit"})
	waitFor(t, 5*time.Second, func() bool {
		r := j.record(j.id)
		for _, p := range r.Pending {
			if p.RequestID == "quiet-permission" && p.MessageTS["user:U-ADA"] != "" &&
				perFeatureRequestCount(j.h.server, "chat.update", "D-U-ADA", "Per feature evidence") > unmuteUpdates {
				return true
			}
		}
		return false
	})
	if len(postsTo(j.h.server, "C-ENG")) != channelRequests {
		t.Fatal("removed channel received unmute catch-up")
	}
	j.drain()
	j.snapshot("unmuted_catch_up")
	lastCard := ""
	for _, request := range j.h.server.Requests("chat.update") {
		if fieldString(request, "channel") == "D-U-ADA" &&
			strings.HasPrefix(fieldString(request, "text"), "Per feature evidence") {
			lastCard = fieldString(request, "text")
		}
	}
	if !strings.Contains(lastCard, "Waiting on you: #2") ||
		strings.Contains(lastCard, "Updates are paused") {
		t.Fatal("unmuted card did not catch up to the newly pending item")
	}

	final := j.record(j.id)
	afterFeature, err := j.h.store.Load(j.id)
	if err != nil {
		t.Fatal(err)
	}
	section := afterFeature.SlackNotifications
	afterFeature.SlackNotifications = nil
	afterFeature.PersistSeq = 0
	after, err := json.Marshal(afterFeature)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		var left, right map[string]any
		_ = json.Unmarshal(before, &left)
		_ = json.Unmarshal(after, &right)
		keys := []string{}
		for key, value := range left {
			if fmt.Sprint(value) != fmt.Sprint(right[key]) {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		t.Fatalf("Slack decisions changed feature state outside its Slack section: fields %v", keys)
	}
	other := j.record(j.other)
	if other.Destinations["user:U-ADA"].RootTS != otherDestination.RootTS ||
		perFeatureRequestCount(j.h.server, "chat.postMessage", "D-U-ADA", "Independent feature") != 1 {
		t.Fatal("first feature's transitions duplicated or replaced the other feature's card")
	}
	if len(other.Destinations) != 1 || len(final.Destinations) != 2 {
		t.Fatalf("destination counts = first %d other %d", len(final.Destinations), len(other.Destinations))
	}
	if count := perFeatureRequestCount(j.h.server, "chat.postMessage", "C-ENG", "Per feature evidence"); count != 1 {
		t.Fatalf("channel root count = %d; want one", count)
	}
	transcript := perFeatureTranscript{
		Requests: j.requests,
		Steps:    j.steps, Submissions: j.answer.all(), FinalRecord: final,
		FeatureSlack: section, OtherRecord: other, StateStable: true, DomainEvents: j.events,
		Order: "fake server receive order; indices are zero-based arrival positions",
	}
	assertPerFeatureRequestOrder(t, j.h.server, transcript)
	encoded := perFeatureJSON(t, transcript)
	encoded = bytes.ReplaceAll(encoded, []byte(j.id), []byte("F-first"))
	encoded = bytes.ReplaceAll(encoded, []byte(j.other), []byte("F-other"))
	featureYAML, err := os.ReadFile(filepath.Join(j.h.stateDir, j.id, "feature.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	slackYAML, err := os.ReadFile(recordPath(j.h.stateDir, j.id))
	if err != nil {
		t.Fatal(err)
	}
	projections, err := json.Marshal([]any{
		j.request(http.MethodGet, "/api/v1/features/"+j.id+"/config", nil),
		j.request(http.MethodGet, "/api/v1/features/"+j.id, nil),
		j.request(http.MethodGet, "/api/v1/config/runtime", nil),
		j.h.observer.all(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{testToken, perFeatureSecret} {
		for surface, data := range map[string][]byte{
			"evidence transcript": encoded, "feature record": featureYAML,
			"Slack record": slackYAML, "read models and events": projections,
		} {
			if bytes.Contains(data, []byte(secret)) {
				t.Fatalf("%s leaked a per-feature credential", surface)
			}
		}
	}
	if !bytes.Contains(encoded, []byte("Engineering")) ||
		!bytes.Contains(slackYAML, []byte("Engineering")) {
		t.Fatal("sanitized display name lost its harmless text")
	}
	return encoded
}

func assertPerFeatureRequestOrder(t *testing.T, server *testsupport.Server, transcript perFeatureTranscript) {
	t.Helper()
	received, err := json.Marshal(responderEvidenceRequests(server.AllRequests()))
	if err != nil {
		t.Fatal(err)
	}
	captured, err := json.Marshal(transcript.Requests)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(received, captured) {
		t.Fatal("transcript requests differ from fake server receive order")
	}
	ends := make(map[string]int, len(transcript.Steps))
	for _, step := range transcript.Steps {
		ends[step.Name] = step.Requests
	}
	if ends["unmuted_catch_up"] != len(transcript.Requests) {
		t.Fatal("final request count does not match fake server capture")
	}
	for i, request := range transcript.Requests {
		if request.Index != i {
			t.Fatalf("request %d has arrival index %d", i, request.Index)
		}
	}
	within := func(stage, method, channel, text string) int {
		t.Helper()
		start := 0
		for _, step := range transcript.Steps {
			if step.Name == stage {
				break
			}
			start = step.Requests
		}
		for i := start; i < ends[stage]; i++ {
			r := transcript.Requests[i]
			if r.Method == method && r.Channel == channel &&
				strings.Contains(r.FallbackText, text) {
				return i
			}
		}
		t.Fatalf("%s: missing %s to %s containing %q", stage, method, channel, text)
		return -1
	}
	before := func(label string, earlier, later int) {
		t.Helper()
		if earlier >= later {
			t.Fatalf("%s: request %d must precede request %d", label, earlier, later)
		}
	}
	before("default card before review",
		within("seeded", "chat.postMessage", "D-U-ADA", "Per feature evidence"),
		within("seeded", "chat.postMessage", "D-U-ADA", "#1 Review:"))
	before("added card before review",
		within("added_channel", "chat.postMessage", "C-ENG", "Per feature evidence"),
		within("added_channel", "chat.postMessage", "C-ENG", "#1 Review:"))
	for _, channel := range []string{"D-U-ADA", "C-ENG"} {
		before("failure before pause on "+channel,
			within("progress_off_failure_delivered", "chat.postMessage", channel, "Internal error"),
			within("muted", "chat.update", channel, "Updates are paused"))
		before("pause before approval poll on "+channel,
			within("muted", "chat.update", channel, "Updates are paused"),
			within("muted_approval", "conversations.replies", channel, ""))
		before("poll before approval confirmation on "+channel,
			within("muted_approval", "conversations.replies", channel, ""),
			within("muted_approval", "chat.postMessage", channel, "#1 was approved"))
	}
	before("quiet pending item before unmuted card refresh",
		within("unmuted_catch_up", "chat.postMessage", "D-U-ADA", "#2 Permission:"),
		within("unmuted_catch_up", "chat.update", "D-U-ADA", "Waiting on you: #2"))
}

func perFeatureJSON(t *testing.T, transcript perFeatureTranscript) []byte {
	t.Helper()
	raw, err := json.Marshal(transcript)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	restartEvidenceOmitClockFields(document)
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestSlackPerFeatureEvidence(t *testing.T) {
	var captures [2][]byte
	for i := range captures {
		t.Run(fmt.Sprintf("capture_%d", i+1), func(t *testing.T) {
			captures[i] = runPerFeatureJourney(t)
		})
	}
	if !bytes.Equal(captures[0], captures[1]) {
		var left, right perFeatureTranscript
		if err := json.Unmarshal(captures[0], &left); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(captures[1], &right); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < len(left.Requests) && i < len(right.Requests); i++ {
			a, _ := json.Marshal(left.Requests[i])
			b, _ := json.Marshal(right.Requests[i])
			if !bytes.Equal(a, b) {
				t.Logf("first differing requests at %d: %s / %s", i, a, b)
				break
			}
		}
		index := 0
		for index < len(captures[0]) && index < len(captures[1]) &&
			captures[0][index] == captures[1][index] {
			index++
		}
		t.Fatalf("per-feature transcript differs across identical runs at byte %d", index)
	}
	if dir := strings.TrimSpace(os.Getenv("AGENTICO_EVIDENCE_DIR")); dir != "" {
		path := filepath.Join(dir, "behaviors", "slack-per-feature.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, captures[0], 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSlackPerFeatureRedactionComposed(t *testing.T) {
	logs := captureLogs(t)
	encoded := runPerFeatureJourney(t)
	if bytes.Contains(encoded, []byte(testToken)) || bytes.Contains(encoded, []byte(perFeatureSecret)) ||
		strings.Contains(logs.String(), testToken) || strings.Contains(logs.String(), perFeatureSecret) {
		t.Fatal("per-feature runtime transcript or logs contain credentials")
	}
}
