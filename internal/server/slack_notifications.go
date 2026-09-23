package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/clone"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// SlackNotificationsPatch keeps presence at both the section and field level.
type SlackNotificationsPatch struct {
	Mode       *string           `json:"mode,omitempty"`
	Recipients *[]SlackRecipient `json:"recipients,omitempty"`
	Progress   *string           `json:"progress,omitempty"`
	NeedsInput *string           `json:"needs_input,omitempty"`
	Problems   *string           `json:"problems,omitempty"`
}

type slackNotificationsDecodeError struct{ detail string }

func (e *slackNotificationsDecodeError) Error() string { return e.detail }

func (p *SlackNotificationsPatch) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("slack_notifications: %w", err)
	}
	for name, value := range fields {
		switch name {
		case "mode":
			if err := json.Unmarshal(value, &p.Mode); err != nil {
				return fmt.Errorf("slack_notifications.mode: %w", err)
			}
		case "progress":
			if err := json.Unmarshal(value, &p.Progress); err != nil {
				return fmt.Errorf("slack_notifications.progress: %w", err)
			}
		case "needs_input":
			if err := json.Unmarshal(value, &p.NeedsInput); err != nil {
				return fmt.Errorf("slack_notifications.needs_input: %w", err)
			}
		case "problems":
			if err := json.Unmarshal(value, &p.Problems); err != nil {
				return fmt.Errorf("slack_notifications.problems: %w", err)
			}
		case "recipients":
			var raw []json.RawMessage
			if err := json.Unmarshal(value, &raw); err != nil {
				return fmt.Errorf("slack_notifications.recipients: %w", err)
			}
			recipients := make([]SlackRecipient, 0, len(raw))
			for i, item := range raw {
				var recipient SlackRecipient
				decoder := json.NewDecoder(bytes.NewReader(item))
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&recipient); err != nil {
					return &slackNotificationsDecodeError{fmt.Sprintf("slack_notifications.recipients[%d].%s", i, jsonFieldError(err))}
				}
				recipients = append(recipients, recipient)
			}
			p.Recipients = &recipients
		default:
			return &slackNotificationsDecodeError{fmt.Sprintf("slack_notifications.%s is unknown", name)}
		}
	}
	return nil
}

func jsonFieldError(err error) string {
	const prefix = `json: unknown field "`
	if text := err.Error(); strings.HasPrefix(text, prefix) && strings.HasSuffix(text, `"`) {
		return strings.TrimSuffix(strings.TrimPrefix(text, prefix), `"`) + " is unknown"
	}
	return err.Error()
}

func validateSlackNotifications(w http.ResponseWriter, patch *SlackNotificationsPatch) bool {
	if patch == nil {
		return true
	}
	if patch.Mode != nil {
		if _, err := feature.ParseSlackMode(*patch.Mode); err != nil {
			writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
				errcat.WithDiagnostics("slack_notifications.mode must be inherit or muted"))
			return false
		}
	}
	for _, field := range []struct {
		name  string
		value *string
	}{
		{"progress", patch.Progress}, {"needs_input", patch.NeedsInput}, {"problems", patch.Problems},
	} {
		if field.value != nil {
			if _, err := feature.ParseSlackOverride(*field.value); err != nil {
				writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
					errcat.WithDiagnostics("slack_notifications."+field.name+" must be inherit, on, or off"))
				return false
			}
		}
	}
	return patch.Recipients == nil || validateSlackRecipientsAtPath(w, *patch.Recipients, "slack_notifications.recipients")
}

// PatchSlackNotifications never changes the input section; nil patch means no edit.
func PatchSlackNotifications(existing *feature.SlackNotifications, patch *SlackNotificationsPatch) *feature.SlackNotifications {
	if patch == nil {
		return existing
	}
	next := feature.CloneSlackNotifications(existing)
	if next == nil {
		next = &feature.SlackNotifications{}
	}
	if patch.Mode != nil {
		next.Mode, _ = feature.ParseSlackMode(*patch.Mode)
	}
	for _, field := range []struct {
		input *string
		dest  *feature.SlackOverride
	}{
		{patch.Progress, &next.Progress}, {patch.NeedsInput, &next.NeedsInput}, {patch.Problems, &next.Problems},
	} {
		if field.input != nil {
			*field.dest, _ = feature.ParseSlackOverride(*field.input)
		}
	}
	if patch.Recipients != nil {
		next.Recipients = make([]feature.SlackRecipient, 0, len(*patch.Recipients))
		for _, r := range *patch.Recipients {
			next.Recipients = append(next.Recipients, feature.SlackRecipient{
				TypedText: r.TypedText, Kind: string(r.Kind), ID: r.ID, DisplayName: r.DisplayName,
			})
		}
	}
	return next
}

func sanitizeSlackRecipientText(text, token string) string {
	if token != "" {
		text = strings.ReplaceAll(text, token, "[redacted]")
	}
	return clone.RedactDiagnostics(text)
}

// SanitizeSlackNotifications removes secrets before the section is saved on a feature.
func SanitizeSlackNotifications(section *feature.SlackNotifications, token string) *feature.SlackNotifications {
	next := feature.CloneSlackNotifications(section)
	if next == nil {
		return nil
	}
	for i := range next.Recipients {
		next.Recipients[i].TypedText = sanitizeSlackRecipientText(next.Recipients[i].TypedText, token)
		next.Recipients[i].DisplayName = sanitizeSlackRecipientText(next.Recipients[i].DisplayName, token)
	}
	return next
}
