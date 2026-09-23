package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

type slackConfigMutationRecorder struct {
	MutationTarget
	called bool
}

func (r *slackConfigMutationRecorder) UpdateFeatureConfig(string, FeatureConfigMutationRequest) (FeatureConfigUpdateResponse, error) {
	r.called = true
	return FeatureConfigUpdateResponse{FeatureID: fixtureFeatureID, Result: resultUpdated}, nil
}

func TestSlackPerFeaturePatchPreservesOmittedAndClearsExplicit(t *testing.T) {
	existing := &feature.SlackNotifications{
		Mode: feature.SlackMuted, Progress: feature.SlackOff, Problems: feature.SlackOn,
		Recipients: []feature.SlackRecipient{{TypedText: "@ops", Kind: "user", ID: "U1", DisplayName: "Ops"}},
	}
	var patch SlackNotificationsPatch
	if err := json.Unmarshal([]byte(`{"mode":"","recipients":[],"needs_input":"off"}`), &patch); err != nil {
		t.Fatal(err)
	}
	got := PatchSlackNotifications(existing, &patch)
	if got.Mode != feature.SlackInherit || got.Progress != feature.SlackOff || got.Problems != feature.SlackOn ||
		got.NeedsInput != feature.SlackOff || len(got.Recipients) != 0 {
		t.Fatalf("patched section = %+v", got)
	}
	if existing.Mode != feature.SlackMuted || len(existing.Recipients) != 1 {
		t.Fatalf("mutated original: %+v", existing)
	}
	if got := PatchSlackNotifications(existing, nil); got != existing {
		t.Fatal("omitted object should retain the existing section")
	}
}

func TestSlackPerFeatureMutationRejectsInvalidObject(t *testing.T) {
	handler := NewHandler(HandlerOptions{
		Config: config.NewDefault(), Mutations: &createFeatureRecorder{}, DisableHostValidation: true,
	})
	for _, tc := range []struct {
		body, path string
	}{
		{`{"name":"x","slack_notifications":{"mode":"snoozed"}}`, "slack_notifications.mode"},
		{`{"name":"x","slack_notifications":{"progress":"later"}}`, "slack_notifications.progress"},
		{`{"name":"x","slack_notifications":{"recipients":[{"typed_text":"@ops","kind":"user","id":"U1"}]}}`, "slack_notifications.recipients[0].display_name"},
		{`{"name":"x","slack_notifications":{"recipients":[{"typed_text":"@ops","kind":"user","id":"U1","display_name":"Ops"},{"typed_text":"ops","kind":"user","id":"U1","display_name":"Ops"}]}}`, "slack_notifications.recipients[1]"},
		{`{"name":"x","slack_notifications":{"unknown":true}}`, "slack_notifications.unknown"},
		{`{"name":"x","slack_notifications":{"recipients":[{"typed_text":"@ops","kind":"user","id":"U1","display_name":"Ops","unknown":true}]}}`, "slack_notifications.recipients[0].unknown"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			w := postTrustedJSON(handler, apiPathFeatures, json.RawMessage(tc.body))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			var response ErrorResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Error.Code != string(errcat.BadRequest) || !strings.Contains(response.Error.Diagnostics, tc.path) {
				t.Fatalf("error = %+v, want bad_request at %s", response.Error, tc.path)
			}
		})
	}
}

func TestSlackPerFeatureConfigMutationRejectsInvalidObjectBeforeWrite(t *testing.T) {
	target := &slackConfigMutationRecorder{}
	handler := NewHandler(HandlerOptions{
		Config: config.NewDefault(), Mutations: target, DisableHostValidation: true,
	})
	for _, tc := range []struct{ body, path string }{
		{`{"slack_notifications":{"needs_input":"unknown"}}`, "slack_notifications.needs_input"},
		{`{"slack_notifications":{"recipients":[{"typed_text":"#eng","kind":"channel","id":"C1","display_name":"Engineering","extra":true}]}}`, "slack_notifications.recipients[0].extra"},
	} {
		w := postTrustedJSON(handler, "/api/v1/features/"+fixtureFeatureID+"/config", json.RawMessage(tc.body))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tc.path) || target.called {
			t.Fatalf("status=%d body=%s called=%t; want rejection at %s", w.Code, w.Body.String(), target.called, tc.path)
		}
	}
}

func TestSlackPerFeatureProjectionAndDefaults(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Slack = &config.SlackConfig{
		Enabled: true, Token: "xoxb-private-token",
		DefaultRecipients: []config.SlackRecipient{{TypedText: "@ops", Kind: "user", ID: "U1", DisplayName: "Ops"}},
	}
	section := &feature.SlackNotifications{
		Mode: feature.SlackMuted, Progress: feature.SlackOff,
		Recipients: []feature.SlackRecipient{{TypedText: "#eng", Kind: "channel", ID: "C1", DisplayName: "Engineering"}},
	}
	effective := slackEffectiveDTO(feature.ResolveSlack(slackGlobalSettings(cfg), section), cfg.Slack.Token)
	if !effective.Configured || !effective.Muted ||
		effective.ModeSource != EffectiveSlackNotificationsModeSourceFeature ||
		effective.Progress.Enabled || effective.Progress.Source != SlackNotificationValueSource("feature") ||
		!effective.NeedsInput.Enabled || effective.NeedsInput.Source != SlackNotificationValueSource("global") ||
		len(effective.Recipients) != 2 || effective.Recipients[0].Source != SlackNotificationRecipientSourceGlobal ||
		effective.Recipients[1].Source != SlackNotificationRecipientSourceFeature {
		t.Fatalf("effective Slack = %+v", effective)
	}
	raw := slackNotificationsDTO(section, cfg.Slack.Token)
	if raw.Mode != SlackNotificationsModeMuted || raw.Progress != SlackNotificationsProgressOff ||
		len(raw.Recipients) != 1 {
		t.Fatalf("raw Slack = %+v", raw)
	}
	defaults := slackNotificationDefaults(cfg)
	if len(defaults.RecipientNames) != 1 || defaults.RecipientNames[0] != "Ops" ||
		!defaults.Categories.Progress {
		t.Fatalf("Slack defaults = %+v", defaults)
	}
	if data, err := json.Marshal(struct {
		Raw       SlackNotifications          `json:"raw"`
		Effective EffectiveSlackNotifications `json:"effective"`
		Defaults  SlackNotificationDefaults   `json:"defaults"`
	}{raw, effective, defaults}); err != nil || strings.Contains(string(data), cfg.Slack.Token) {
		t.Fatalf("token escaped into projection: %s, err=%v", data, err)
	}
	cfg.Slack.Enabled = false
	if slackConfigured(cfg) {
		t.Fatal("disabled Slack should not report configured")
	}
	cfg.Slack.Enabled, cfg.Slack.Token = true, ""
	if slackConfigured(cfg) {
		t.Fatal("missing token should not report configured")
	}
}

func TestSlackPerFeatureReadModels(t *testing.T) {
	store, f := seedReadFeature(t)
	cfg := config.NewDefault()
	cfg.Slack = &config.SlackConfig{Enabled: true, Token: "xoxb-private-token",
		DefaultRecipients: []config.SlackRecipient{{TypedText: "@ops", Kind: "user", ID: "U1", DisplayName: "Ops"}}}
	handler := NewHandler(HandlerOptions{Features: store, Config: cfg, DisableHostValidation: true})
	check := func(configured bool, source string, count, storedCount int) {
		t.Helper()
		detail := getJSONMap(t, handler, "/api/v1/features/"+f.ID)
		notifications := detail["feature"].(map[string]any)["slack_notifications"].(map[string]any)
		if notifications["configured"] != configured || notifications["mode_source"] != source ||
			len(notifications["recipients"].([]any)) != count {
			t.Fatalf("feature detail notifications = %+v", notifications)
		}
		read := getJSONMap(t, handler, "/api/v1/features/"+f.ID+"/config")
		current := read["current"].(map[string]any)
		stored := current["slack_notifications"].(map[string]any)
		if current["slack_configured"] != configured || len(stored["recipients"].([]any)) != storedCount {
			t.Fatalf("feature config = %+v", current)
		}
		if strings.Contains(string(mustMarshalJSON(t, detail)), cfg.Slack.Token) ||
			strings.Contains(string(mustMarshalJSON(t, read)), cfg.Slack.Token) {
			t.Fatal("Slack token leaked into a read model")
		}
	}
	check(true, "global", 1, 0)
	if err := store.Modify(f.ID, func(loaded *feature.Feature) error {
		loaded.SlackNotifications = &feature.SlackNotifications{
			Mode: feature.SlackMuted, Problems: feature.SlackOff,
			Recipients: []feature.SlackRecipient{{TypedText: "#eng", Kind: "channel", ID: "C1", DisplayName: "Engineering"}},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	check(true, "feature", 2, 1)
	cfg.Slack.Enabled = false
	check(false, "feature", 0, 1)
}

func TestSlackPerFeatureChildDetailUsesNotificationOwner(t *testing.T) {
	store, parent := seedReadFeature(t)
	if err := store.Modify(parent.ID, func(f *feature.Feature) error {
		f.SlackNotifications = &feature.SlackNotifications{Mode: feature.SlackMuted, Progress: feature.SlackOff}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	child := seedReadChildFeature(t, store, parent.ID, feature.StatusCreated, feature.SetupStatusQueued, "")
	if err := store.Modify(child.ID, func(f *feature.Feature) error {
		f.SlackNotifications = &feature.SlackNotifications{Progress: feature.SlackOn}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cfg := config.NewDefault()
	cfg.Slack = &config.SlackConfig{Enabled: true, Token: "xoxb-test-token"}
	handler := NewHandler(HandlerOptions{Features: store, Config: cfg, DisableHostValidation: true})
	detail := getJSONMap(t, handler, "/api/v1/features/"+child.ID)
	effective := detail["feature"].(map[string]any)["slack_notifications"].(map[string]any)
	progress := effective["progress"].(map[string]any)
	if effective["muted"] != true || effective["mode_source"] != "feature" ||
		progress["enabled"] != false || progress["source"] != "feature" {
		t.Fatalf("child detail resolved its own section rather than the parent: %+v", effective)
	}
}

func TestSlackPerFeatureRedactionRecipientText(t *testing.T) {
	const token = "xoxb-per-feature-token-sentinel-123456789"
	const second = "https://alice:secret-value@example.com/path"
	section := &feature.SlackNotifications{Recipients: []feature.SlackRecipient{{
		TypedText: "#eng " + token + " " + second,
		Kind:      "channel", ID: "C1",
		DisplayName: "Engineering " + token + " " + second,
	}}}
	safe := SanitizeSlackNotifications(section, token)
	encoded, err := json.Marshal(safe)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), token) || strings.Contains(string(encoded), second) ||
		!strings.Contains(string(encoded), "Engineering") {
		t.Fatalf("recipient text not sanitized: %s", encoded)
	}
	if !strings.Contains(section.Recipients[0].DisplayName, token) {
		t.Fatal("sanitization mutated the submitted section")
	}
	cfg := config.NewDefault()
	cfg.Slack = &config.SlackConfig{Enabled: true, Token: token,
		DefaultRecipients: []config.SlackRecipient{{TypedText: "@ops", Kind: "user", ID: "U1", DisplayName: "Ops " + token + " " + second}}}
	for _, projected := range []any{
		slackNotificationsDTO(section, token),
		slackEffectiveDTO(feature.ResolveSlack(slackGlobalSettings(cfg), section), token),
		slackNotificationDefaults(cfg),
	} {
		encoded, err := json.Marshal(projected)
		if err != nil || strings.Contains(string(encoded), token) || strings.Contains(string(encoded), second) {
			t.Fatalf("projection leaked a secret: %s, err=%v", encoded, err)
		}
	}
}
