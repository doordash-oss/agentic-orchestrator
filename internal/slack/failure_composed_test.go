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
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

const composedSecondSecret = "xoxb-composed-second-secret-123456"

type composedFailureStore struct {
	serverruntime.MutationTarget

	mu         sync.Mutex
	cfg        *config.Config
	generation uint64
}

func (s *composedFailureStore) SlackSettings() ports.SlackRuntimeSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	slackConfig := s.cfg.Slack
	recipients := make([]ports.SlackRecipient, 0, len(slackConfig.DefaultRecipients))
	for _, recipient := range slackConfig.DefaultRecipients {
		recipients = append(recipients, ports.SlackRecipient{
			TypedText: recipient.TypedText, Kind: ports.SlackRecipientKind(recipient.Kind),
			ID: recipient.ID, DisplayName: recipient.DisplayName,
		})
	}
	return ports.SlackRuntimeSettings{
		Enabled: true, Token: slackConfig.Token, CredentialGeneration: s.generation,
		Recipients: recipients,
		Categories: ports.SlackCategoryDefaults{
			Progress: false, NeedsInput: true, Problems: true,
		},
	}
}

func (s *composedFailureStore) LoadSlackCredential() (string, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Slack.Token, s.generation
}

func (s *composedFailureStore) SlackCredentialCurrent(token string, generation uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Slack.Token == token && s.generation == generation
}

func (s *composedFailureStore) StoreSlackValidation(
	token string,
	generation uint64,
	validation *ports.SlackValidation,
	checkedAt time.Time,
) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.Slack.Token != token || s.generation != generation {
		return false, nil
	}
	s.cfg.Slack.Identity = &config.SlackIdentity{
		TeamID: validation.Identity.TeamID, TeamName: validation.Identity.TeamName,
		UserID: validation.Identity.UserID, DisplayName: validation.Identity.DisplayName,
		BotID: validation.Identity.BotID,
	}
	s.cfg.Slack.GrantedScopes = append([]string(nil), validation.GrantedScopes...)
	s.cfg.Slack.LastValidatedAt = checkedAt.UTC()
	return true, nil
}

type composedReporterRelay struct {
	mu     sync.Mutex
	target ports.SlackDeliveryReporter
}

func (r *composedReporterRelay) bind(target ports.SlackDeliveryReporter) {
	r.mu.Lock()
	r.target = target
	r.mu.Unlock()
}

func (r *composedReporterRelay) ReportSlackDeliveryFailure(
	at time.Time,
	generation uint64,
	canonical errcat.Error,
) {
	r.mu.Lock()
	target := r.target
	r.mu.Unlock()
	if target != nil {
		target.ReportSlackDeliveryFailure(at, generation, canonical)
	}
}

func (r *composedReporterRelay) ReportSlackDeliverySuccess(at time.Time, generation uint64) {
	r.mu.Lock()
	target := r.target
	r.mu.Unlock()
	if target != nil {
		target.ReportSlackDeliverySuccess(at, generation)
	}
}

type composedClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *composedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *composedClock) Sleep(ctx context.Context, duration time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	default:
	}
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
	return true
}

type composedSlackExchange struct {
	Method   string `json:"method"`
	Channel  string `json:"channel,omitempty"`
	ThreadTS string `json:"thread_ts,omitempty"`
	Response string `json:"response"`
}

type composedStageProjection struct {
	Stage        string         `json:"stage"`
	RuntimeSlack map[string]any `json:"runtime_slack"`
	Warnings     []any          `json:"warnings"`
	ConfigEvents int            `json:"config_updated_events"`
}

type composedFailureJourney struct {
	t           *testing.T
	fake        *testsupport.Server
	http        *httptest.Server
	store       *composedFailureStore
	features    *feature.Store
	notifier    *Notifier
	observer    *fakeObserver
	clock       *composedClock
	stageMu     sync.Mutex
	stage       string
	sequence    int
	exchanges   []composedSlackExchange
	sseEvents   chan string
	sseCancel   context.CancelFunc
	sseBody     io.ReadCloser
	projections []composedStageProjection
	injected    int
}

func newComposedFailureJourney(t *testing.T) *composedFailureJourney {
	t.Helper()
	stateDir := t.TempDir()
	features := feature.NewStore(stateDir)
	f := &feature.Feature{
		ID: "F-1", Name: "Slack failure surfacing", Slug: "slack-failure-surfacing",
		Description: "composed evidence feature", Created: time.Now().Add(-time.Hour),
		Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement,
		SchemaVersion: feature.SchemaVersionCurrent, Pipeline: feature.PipelineMoonshot,
		CurrentRoadmapPhase: 1, TotalRoadmapPhases: 2,
		Repos: []feature.FeatureRepo{{Name: "alpha"}},
	}
	if err := features.Save(f); err != nil {
		t.Fatal(err)
	}
	cfg := config.NewDefault()
	cfg.Slack = &config.SlackConfig{
		Enabled: true, Token: testToken,
		Identity: &config.SlackIdentity{
			TeamID: "T-old", TeamName: "Old team", UserID: "U-bot",
			DisplayName: "Agentico", BotID: "B-bot",
		},
		GrantedScopes: append([]string(nil), RequiredScopes()...),
		DefaultRecipients: []config.SlackRecipient{
			{TypedText: "@ada", Kind: "user", ID: "U-ADA", DisplayName: "Ada Lovelace"},
			{TypedText: "#eng", Kind: "channel", ID: "C-ENG", DisplayName: "#eng"},
		},
	}
	store := &composedFailureStore{cfg: cfg, generation: 1}
	fake := testsupport.New(t)
	t.Setenv(EnvSlackAPIBase, fake.URL())
	service := NewService()
	relay := &composedReporterRelay{}
	observer := &fakeObserver{}
	clock := &composedClock{now: time.Date(2026, 9, 22, 16, 0, 0, 0, time.UTC)}
	notifier := NewNotifier(NotifierOptions{
		Settings: store, Store: features, StateDir: stateDir,
		Observer: observer, Reporter: relay, Clock: clock, Jitter: func() float64 { return 0 },
	})
	notifier.SetServerName("Local agent")
	handler := serverruntime.NewHandler(serverruntime.HandlerOptions{
		Runtime: serverruntime.RuntimeIdentity{StateDir: stateDir},
		Config:  cfg, Features: features, FeatureStore: features,
		Slack: service, SlackWarnings: notifier, Mutations: store,
		BindSlackDeliveryReporter: relay.bind, DisableHostValidation: true,
	})
	journey := &composedFailureJourney{
		t: t, fake: fake, http: httptest.NewServer(handler), store: store,
		features: features, notifier: notifier, observer: observer, clock: clock,
		stage: "healthy", sseEvents: make(chan string, 128),
	}
	fake.SetDefault(journey.respond)
	journey.startSSE()
	notifier.Start()
	t.Cleanup(func() {
		notifier.Stop(context.Background())
		journey.sseCancel()
		_ = journey.sseBody.Close()
		journey.http.Close()
	})
	return journey
}

func (j *composedFailureJourney) respond(
	method string,
	request testsupport.Request,
) testsupport.Response {
	j.stageMu.Lock()
	defer j.stageMu.Unlock()
	j.sequence++
	channel := fmt.Sprint(request.Fields["channel"])
	if channel == "<nil>" {
		channel = ""
	}
	response := testsupport.Response{
		Body: map[string]any{
			"ok": true, "ts": fmt.Sprintf("1758566400.%06d", j.sequence),
			"channel": channel,
		},
	}
	switch method {
	case "conversations.open":
		response.Body = map[string]any{
			"ok": true, "channel": map[string]any{"id": "D-U-ADA"},
		}
	case "auth.test":
		response.Headers = http.Header{
			"X-OAuth-Scopes": []string{strings.Join(RequiredScopes(), ",")},
		}
		response.Body = map[string]any{
			"ok": true, "team": "Renamed team", "team_id": "T-new",
			"user": "agentico", "user_id": "U-bot", "bot_id": "B-bot",
		}
	case "chat.postMessage", "chat.update":
		switch j.stage {
		case "archived":
			if channel == "C-ENG" {
				response.Body = map[string]any{"ok": false, "error": "is_archived"}
			}
		case "rate_limited":
			if channel == "D-U-ADA" {
				response.Status = http.StatusTooManyRequests
				response.Headers = http.Header{"Retry-After": []string{"1"}}
				response.Body = nil
			}
		case "token_revoked":
			response.Body = map[string]any{"ok": false, "error": "token_revoked"}
		case "secret":
			if channel == "C-ENG" {
				response.Body = map[string]any{
					"ok":    false,
					"error": "is_archived: " + testToken + " " + composedSecondSecret,
				}
			} else {
				response.Body = map[string]any{
					"ok": false, "error": "missing_scope",
					"needed": "chat:write " + testToken + " " + composedSecondSecret,
				}
			}
		case "secret_destination":
			if channel == "C-ENG" {
				response.Body = map[string]any{
					"ok":    false,
					"error": "is_archived: " + testToken + " " + composedSecondSecret,
				}
			}
		}
	}
	responseText := scrub(testToken, scrub(composedSecondSecret, fmt.Sprint(response.Body)))
	j.exchanges = append(j.exchanges, composedSlackExchange{
		Method: method, Channel: channel,
		ThreadTS: fmt.Sprint(request.Fields["thread_ts"]), Response: responseText,
	})
	return response
}

func (j *composedFailureJourney) setStage(stage string) {
	j.stageMu.Lock()
	j.stage = stage
	j.stageMu.Unlock()
}

func (j *composedFailureJourney) feed(event ports.Event) {
	j.injected++
	j.notifier.DomainEventTap(event)
}

func (j *composedFailureJourney) exchangeCount(method, channel string) int {
	j.stageMu.Lock()
	defer j.stageMu.Unlock()
	count := 0
	for _, exchange := range j.exchanges {
		if exchange.Method == method && exchange.Channel == channel {
			count++
		}
	}
	return count
}

func (j *composedFailureJourney) startSSE() {
	j.t.Helper()
	ctx, cancel := context.WithCancel(j.t.Context())
	req, err := http.NewRequestWithContext(
		ctx, http.MethodGet, j.http.URL+"/api/v1/events", nil,
	)
	if err != nil {
		j.t.Fatal(err)
	}
	resp, err := j.http.Client().Do(req)
	if err != nil {
		j.t.Fatal(err)
	}
	j.sseCancel = cancel
	j.sseBody = resp.Body
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				j.sseEvents <- strings.TrimPrefix(line, "event: ")
			}
		}
		close(j.sseEvents)
	}()
	select {
	case event := <-j.sseEvents:
		if event != "connected" {
			j.t.Fatalf("first SSE event = %q; want connected", event)
		}
	case <-time.After(2 * time.Second):
		j.t.Fatal("timed out waiting for SSE connection")
	}
}

func (j *composedFailureJourney) configEventCount() int {
	count := 0
	for {
		select {
		case event := <-j.sseEvents:
			if event == "config.updated" {
				count++
			}
		default:
			return count
		}
	}
}

func (j *composedFailureJourney) get(path string) map[string]any {
	j.t.Helper()
	resp, err := j.http.Client().Get(j.http.URL + path)
	if err != nil {
		j.t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		j.t.Fatal(err)
	}
	return body
}

func (j *composedFailureJourney) snapshot(stage string) composedStageProjection {
	runtimeBody := j.get("/api/v1/config/runtime")
	runtimeSlack := runtimeBody["slack"].(map[string]any)
	detail := j.get("/api/v1/features/F-1")["feature"].(map[string]any)
	warnings, _ := detail["warnings"].([]any)
	projection := composedStageProjection{
		Stage: stage, RuntimeSlack: runtimeSlack, Warnings: warnings,
		ConfigEvents: j.configEventCount(),
	}
	j.projections = append(j.projections, projection)
	return projection
}

func (j *composedFailureJourney) failureEvents() []observe.Event {
	return j.observer.ofKind("slack.delivery_failed")
}

func projectionWarningsText(t *testing.T, projection composedStageProjection) string {
	t.Helper()
	encoded, err := json.Marshal(projection.Warnings)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestSlackFailureSurfacingEvidence(t *testing.T) {
	journey := newComposedFailureJourney(t)
	storedFeaturePath := filepath.Join(journey.notifier.stateDir, "F-1", "feature.yaml")
	featureBefore, err := os.ReadFile(storedFeaturePath)
	if err != nil {
		t.Fatal(err)
	}

	journey.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
	waitFor(t, 10*time.Second, func() bool {
		return len(postsTo(journey.fake, "D-U-ADA")) >= 1 &&
			len(postsTo(journey.fake, "C-ENG")) >= 1
	})
	seeded := journey.snapshot("seeded")
	if seeded.ConfigEvents != 0 || len(seeded.Warnings) != 0 {
		t.Fatalf(
			"seeded projection config events/warnings = %d/%d; want 0/0",
			seeded.ConfigEvents,
			len(seeded.Warnings),
		)
	}

	journey.setStage("archived")
	for i := 0; i < 5; i++ {
		before := len(journey.failureEvents())
		journey.feed(startedEvent("F-1", feature.PhaseResearch))
		waitFor(t, 10*time.Second, func() bool {
			return len(journey.failureEvents()) > before
		})
		projection := journey.snapshot(fmt.Sprintf("archived_%d", i+1))
		warningsText := projectionWarningsText(t, projection)
		countText := fmt.Sprintf("%d Slack messages", i+1)
		if i == 0 {
			countText = "1 Slack message"
		}
		if len(projection.Warnings) != 1 ||
			!strings.Contains(warningsText, string(errcat.SlackRecipientNotNotified)) ||
			!strings.Contains(warningsText, "#eng") ||
			!strings.Contains(warningsText, countText) {
			t.Fatalf(
				"archived step %d warnings = %s; want one growing channel warning",
				i+1,
				warningsText,
			)
		}
		if projection.ConfigEvents != 0 {
			t.Fatalf(
				"archived step %d config.updated events = %d; want 0",
				i+1,
				projection.ConfigEvents,
			)
		}
	}

	journey.setStage("rate_limited")
	beforeRate := len(journey.failureEvents())
	beforeRateRequests := journey.exchangeCount("chat.update", "D-U-ADA")
	journey.feed(completedEvent("F-1", feature.PhaseResearch))
	waitFor(t, 10*time.Second, func() bool {
		for _, event := range journey.failureEvents()[beforeRate:] {
			if event.Data["error_code"] == string(errcat.SlackDeliveryRetriesExhausted) {
				return true
			}
		}
		return false
	})
	rateProjection := journey.snapshot("rate_limited")
	rateWarnings := projectionWarningsText(t, rateProjection)
	if len(rateProjection.Warnings) != 1 ||
		!strings.Contains(rateWarnings, string(errcat.SlackDeliveryRetriesExhausted)) ||
		!strings.Contains(rateWarnings, "Ada Lovelace") ||
		!strings.Contains(rateWarnings, "rate_limited") {
		t.Fatalf(
			"rate-limit warnings = %s; want one isolated user exhaustion warning",
			rateWarnings,
		)
	}
	if rateProjection.ConfigEvents != 0 {
		t.Fatalf(
			"rate-limit config.updated events = %d; want 0",
			rateProjection.ConfigEvents,
		)
	}
	if attempts := journey.exchangeCount("chat.update", "D-U-ADA") - beforeRateRequests; attempts != 4 {
		t.Fatalf("rate-limit attempts = %d; want 4", attempts)
	}
	rateFailure := journey.failureEvents()[beforeRate:]
	if len(rateFailure) != 1 ||
		fmt.Sprint(rateFailure[0].Data["attempts"]) != "4" ||
		rateFailure[0].Data["slack_error"] != "rate_limited" {
		t.Fatalf("rate-limit delivery failure = %#v; want one four-attempt exhaustion", rateFailure)
	}

	journey.setStage("token_revoked")
	beforeCredential := len(journey.failureEvents())
	journey.feed(startedEvent("F-1", feature.PhaseDesign))
	waitFor(t, 10*time.Second, func() bool {
		return len(journey.failureEvents()) >= beforeCredential+2 &&
			journey.fake.CallCount("auth.test") == 1
	})
	credentialProjection := journey.snapshot("token_revoked")
	status := credentialProjection.RuntimeSlack["status"].(map[string]any)
	if status["state"] != string(ports.SlackCredentialError) {
		t.Fatalf("credential status = %#v; want credential_error", status)
	}
	lastError := status["last_error"].(map[string]any)
	if lastError["code"] != string(errcat.SlackTokenRejected) ||
		!strings.Contains(fmt.Sprint(lastError["summary"]), "token_revoked") {
		t.Fatalf("credential last error = %#v; want retained token_revoked delivery error", lastError)
	}
	identity := credentialProjection.RuntimeSlack["identity"].(map[string]any)
	if identity["team_name"] != "Renamed team" {
		t.Fatalf("refreshed identity = %#v; want renamed team", identity)
	}
	if credentialProjection.ConfigEvents != 1 {
		t.Fatalf(
			"credential config.updated events = %d; want exactly 1",
			credentialProjection.ConfigEvents,
		)
	}

	journey.setStage("recovered")
	beforeRecoveryEvents := journey.configEventCount()
	journey.feed(completedEvent("F-1", feature.PhaseDesign))
	waitFor(t, 10*time.Second, func() bool {
		runtimeSlack := journey.get("/api/v1/config/runtime")["slack"].(map[string]any)
		status := runtimeSlack["status"].(map[string]any)
		detail := journey.get("/api/v1/features/F-1")["feature"].(map[string]any)
		warnings, _ := detail["warnings"].([]any)
		return status["state"] == string(ports.SlackConnected) && len(warnings) == 0
	})
	recovered := journey.snapshot("recovered")
	if delta := recovered.ConfigEvents - beforeRecoveryEvents; delta != 1 {
		t.Fatalf("recovery config.updated events = %d; want exactly 1", delta)
	}

	journey.setStage("secret_destination")
	beforeSecret := len(journey.failureEvents())
	journey.feed(startedEvent("F-1", feature.PhasePlan))
	waitFor(t, 10*time.Second, func() bool {
		return len(journey.failureEvents()) > beforeSecret
	})
	secretProjection := journey.snapshot("secret")
	secretStatus := secretProjection.RuntimeSlack["status"].(map[string]any)
	if secretStatus["state"] != string(ports.SlackConnected) ||
		len(secretProjection.Warnings) == 0 {
		t.Fatalf("secret projections status/warnings = %#v/%#v", secretStatus, secretProjection.Warnings)
	}
	if secretProjection.ConfigEvents != 0 {
		t.Fatalf(
			"destination-only secret failure config.updated events = %d; want 0",
			secretProjection.ConfigEvents,
		)
	}

	finalRecord, err := loadFeatureRecord(journey.notifier.stateDir, "F-1")
	if err != nil {
		t.Fatal(err)
	}
	featureAfter, err := os.ReadFile(storedFeaturePath)
	if err != nil {
		t.Fatal(err)
	}
	transcript := struct {
		Exchanges        []composedSlackExchange   `json:"exchanges"`
		Projections      []composedStageProjection `json:"projections"`
		DeliveryFailures []observe.Event           `json:"delivery_failures"`
		FinalRecord      *featureRecord            `json:"final_record"`
		FeatureUnchanged bool                      `json:"feature_unchanged"`
		InjectedEvents   int                       `json:"injected_domain_events"`
		AuthTestCalls    int                       `json:"auth_test_calls"`
	}{
		Exchanges: journey.exchanges, Projections: journey.projections,
		DeliveryFailures: journey.failureEvents(), FinalRecord: finalRecord,
		FeatureUnchanged: bytes.Equal(featureBefore, featureAfter),
		InjectedEvents:   journey.injected,
		AuthTestCalls:    journey.fake.CallCount("auth.test"),
	}
	encoded, err := json.MarshalIndent(transcript, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{testToken, composedSecondSecret} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("composed evidence leaked %q", secret)
		}
	}
	if !transcript.FeatureUnchanged {
		t.Fatal("feature record changed during composed Slack failure journey")
	}
	if transcript.AuthTestCalls != 1 {
		t.Fatalf("auth.test calls = %d; want 1", transcript.AuthTestCalls)
	}
	channelFailure := finalRecord.Destinations[destinationKey("channel", "C-ENG")].Failure
	userFailure := finalRecord.Destinations[destinationKey("user", "U-ADA")].Failure
	if channelFailure == nil || channelFailure.Count != 1 || userFailure != nil {
		t.Fatalf(
			"final destination failures channel/user = %#v/%#v; want isolated channel count 1",
			channelFailure,
			userFailure,
		)
	}
	if !bytes.Contains(encoded, []byte("is_archived")) ||
		!bytes.Contains(encoded, []byte("slack_delivery_retries_exhausted")) ||
		!bytes.Contains(encoded, []byte("token_revoked")) ||
		!bytes.Contains(encoded, []byte("Renamed team")) {
		t.Fatalf("composed evidence misses contracted stages:\n%s", encoded)
	}
	if dir := strings.TrimSpace(os.Getenv("AGENTICO_EVIDENCE_DIR")); dir != "" {
		target := filepath.Join(dir, "behaviors")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(target, "slack-failure-surfacing.json"),
			append(encoded, '\n'),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSlackFailureSurfacingRedaction(t *testing.T) {
	logs := captureLogs(t)
	journey := newComposedFailureJourney(t)
	journey.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
	waitFor(t, 10*time.Second, func() bool {
		return len(postsTo(journey.fake, "D-U-ADA")) >= 1 &&
			len(postsTo(journey.fake, "C-ENG")) >= 1
	})
	journey.setStage("secret")
	journey.feed(startedEvent("F-1", feature.PhaseResearch))
	waitFor(t, 10*time.Second, func() bool {
		return len(journey.failureEvents()) >= 2
	})
	projection := journey.snapshot("secret")
	persisted, err := loadFeatureRecord(journey.notifier.stateDir, "F-1")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(struct {
		Runtime  map[string]any
		Warnings []any
		Record   *featureRecord
		Events   []observe.Event
		Logs     string
	}{
		Runtime: projection.RuntimeSlack, Warnings: projection.Warnings,
		Record: persisted, Events: journey.failureEvents(), Logs: logs.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{testToken, composedSecondSecret} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("real runtime/feature projection leaked %q: %s", secret, encoded)
		}
	}
	for _, expected := range []string{
		"is_archived", "chat:write", "#eng", "slack_scopes_revoked",
		"slack_recipient_not_notified",
	} {
		if !bytes.Contains(encoded, []byte(expected)) {
			t.Errorf("real projections = %s; want %q", encoded, expected)
		}
	}
}
