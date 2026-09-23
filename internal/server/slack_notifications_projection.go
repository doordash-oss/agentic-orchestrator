package server

import (
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func slackConfigured(cfg *config.Config) bool {
	return cfg != nil && cfg.Slack != nil && cfg.Slack.Enabled && cfg.Slack.Token != ""
}

func slackConfigToken(cfg *config.Config) string {
	if cfg == nil || cfg.Slack == nil {
		return ""
	}
	return cfg.Slack.Token
}

func slackNotificationDefaults(cfg *config.Config) SlackNotificationDefaults {
	result := SlackNotificationDefaults{Categories: allCategoriesOn(), RecipientNames: []string{}}
	if cfg == nil || cfg.Slack == nil {
		return result
	}
	categories := cfg.Slack.Categories.Effective()
	result.Categories = SlackCategories{
		Progress: categories.Progress, NeedsInput: categories.NeedsInput, Problems: categories.Problems,
	}
	for _, recipient := range cfg.Slack.DefaultRecipients {
		result.RecipientNames = append(result.RecipientNames, sanitizeSlackRecipientText(recipient.DisplayName, cfg.Slack.Token))
	}
	return result
}

func slackGlobalSettings(cfg *config.Config) feature.SlackGlobalSettings {
	defaults := slackNotificationDefaults(cfg)
	result := feature.SlackGlobalSettings{
		Enabled:  cfg != nil && cfg.Slack != nil && cfg.Slack.Enabled,
		HasToken: cfg != nil && cfg.Slack != nil && cfg.Slack.Token != "",
		Progress: defaults.Categories.Progress, NeedsInput: defaults.Categories.NeedsInput,
		Problems: defaults.Categories.Problems,
	}
	if cfg != nil && cfg.Slack != nil {
		for _, r := range cfg.Slack.DefaultRecipients {
			result.Recipients = append(result.Recipients, feature.SlackRecipient{
				TypedText: r.TypedText, Kind: r.Kind, ID: r.ID, DisplayName: r.DisplayName,
			})
		}
	}
	return result
}

func slackNotificationsDTO(section *feature.SlackNotifications, token string) SlackNotifications {
	result := SlackNotifications{Recipients: []SlackRecipient{}}
	if section == nil {
		return result
	}
	result.Mode = SlackNotificationsMode(section.Mode)
	result.Progress = SlackNotificationsProgress(section.Progress)
	result.NeedsInput = SlackNotificationsNeedsInput(section.NeedsInput)
	result.Problems = SlackNotificationsProblems(section.Problems)
	for _, r := range section.Recipients {
		result.Recipients = append(result.Recipients, SlackRecipient{
			TypedText: sanitizeSlackRecipientText(r.TypedText, token), Kind: SlackRecipientKind(r.Kind),
			ID: r.ID, DisplayName: sanitizeSlackRecipientText(r.DisplayName, token),
		})
	}
	return result
}

func slackEffectiveDTO(effective feature.EffectiveSlack, token string) EffectiveSlackNotifications {
	category := func(c feature.SlackEffectiveCategory) SlackNotificationValue {
		return SlackNotificationValue{Enabled: c.Enabled, Source: SlackNotificationValueSource(c.Source)}
	}
	result := EffectiveSlackNotifications{
		Configured: effective.Configured, Muted: effective.Muted,
		ModeSource: EffectiveSlackNotificationsModeSource(effective.ModeSource),
		Progress:   category(effective.Progress), NeedsInput: category(effective.NeedsInput),
		Problems: category(effective.Problems), Recipients: []SlackNotificationRecipient{},
	}
	for _, r := range effective.Recipients {
		result.Recipients = append(result.Recipients, SlackNotificationRecipient{
			TypedText: sanitizeSlackRecipientText(r.TypedText, token), Kind: SlackNotificationRecipientKind(r.Kind),
			ID: r.ID, DisplayName: sanitizeSlackRecipientText(r.DisplayName, token),
			Source: SlackNotificationRecipientSource(r.Source),
		})
	}
	return result
}
