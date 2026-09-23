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

package feature

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

type SlackMode string

const (
	SlackInherit SlackMode = ""
	SlackMuted   SlackMode = "muted"
)

type SlackOverride string

const (
	SlackDefault SlackOverride = ""
	SlackOn      SlackOverride = "on"
	SlackOff     SlackOverride = "off"
)

type SlackSource string

const (
	SlackSourceGlobal  SlackSource = "global"
	SlackSourceFeature SlackSource = "feature"
)

type SlackRecipient struct {
	TypedText   string `yaml:"typed_text" json:"typed_text"`
	Kind        string `yaml:"kind" json:"kind"`
	ID          string `yaml:"id" json:"id"`
	DisplayName string `yaml:"display_name" json:"display_name"`
}

// SlackNotifications is optional on a feature. Its zero values inherit all
// workspace defaults; recipients are additive, not replacements.
type SlackNotifications struct {
	Mode       SlackMode        `yaml:"mode,omitempty" json:"mode,omitempty"`
	Recipients []SlackRecipient `yaml:"recipients,omitempty" json:"recipients,omitempty"`
	Progress   SlackOverride    `yaml:"progress,omitempty" json:"progress,omitempty"`
	NeedsInput SlackOverride    `yaml:"needs_input,omitempty" json:"needs_input,omitempty"`
	Problems   SlackOverride    `yaml:"problems,omitempty" json:"problems,omitempty"`
}

func CloneSlackNotifications(section *SlackNotifications) *SlackNotifications {
	if section == nil {
		return nil
	}
	clone := *section
	clone.Recipients = append([]SlackRecipient(nil), section.Recipients...)
	return &clone
}

func ParseSlackMode(raw string) (SlackMode, error) {
	switch raw {
	case "", "inherit":
		return SlackInherit, nil
	case "muted":
		return SlackMuted, nil
	default:
		return "", fmt.Errorf("invalid Slack mode %q", raw)
	}
}

func ParseSlackOverride(raw string) (SlackOverride, error) {
	switch raw {
	case "", "inherit":
		return SlackDefault, nil
	case "on":
		return SlackOn, nil
	case "off":
		return SlackOff, nil
	default:
		return "", fmt.Errorf("invalid Slack category override %q", raw)
	}
}

func (s *SlackNotifications) UnmarshalYAML(value *yaml.Node) error {
	type plain SlackNotifications
	var decoded plain
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	mode, err := ParseSlackMode(string(decoded.Mode))
	if err != nil {
		return err
	}
	decoded.Mode = mode
	for _, field := range []*SlackOverride{&decoded.Progress, &decoded.NeedsInput, &decoded.Problems} {
		override, err := ParseSlackOverride(string(*field))
		if err != nil {
			return err
		}
		*field = override
	}
	*s = SlackNotifications(decoded)
	return nil
}

type SlackGlobalSettings struct {
	Enabled    bool
	HasToken   bool
	Recipients []SlackRecipient
	Progress   bool
	NeedsInput bool
	Problems   bool
}

type SlackEffectiveCategory struct {
	Enabled bool        `json:"enabled"`
	Source  SlackSource `json:"source"`
}

type SlackEffectiveRecipient struct {
	SlackRecipient
	Source SlackSource `json:"source"`
}

type EffectiveSlack struct {
	Configured  bool                      `json:"configured"`
	Muted       bool                      `json:"muted"`
	ModeSource  SlackSource               `json:"mode_source"`
	Recipients  []SlackEffectiveRecipient `json:"recipients"`
	Progress    SlackEffectiveCategory    `json:"progress"`
	NeedsInput  SlackEffectiveCategory    `json:"needs_input"`
	Problems    SlackEffectiveCategory    `json:"problems"`
}

// ResolveSlack performs no I/O. The owner feature must be selected by its
// caller; the child's own section is never used for notification decisions.
func ResolveSlack(global SlackGlobalSettings, section *SlackNotifications) EffectiveSlack {
	result := EffectiveSlack{
		Configured: global.Enabled && global.HasToken,
		ModeSource: SlackSourceGlobal,
		Progress:   resolveSlackCategory(global.Progress, SlackDefault),
		NeedsInput: resolveSlackCategory(global.NeedsInput, SlackDefault),
		Problems:   resolveSlackCategory(global.Problems, SlackDefault),
	}
	if section != nil {
		result.Muted = section.Mode == SlackMuted
		if result.Muted {
			result.ModeSource = SlackSourceFeature
		}
		result.Progress = resolveSlackCategory(global.Progress, section.Progress)
		result.NeedsInput = resolveSlackCategory(global.NeedsInput, section.NeedsInput)
		result.Problems = resolveSlackCategory(global.Problems, section.Problems)
	}
	if !result.Configured {
		return result
	}
	capacity := len(global.Recipients)
	if section != nil {
		capacity += len(section.Recipients)
	}
	result.Recipients = make([]SlackEffectiveRecipient, 0, capacity)
	seen := make(map[string]struct{}, capacity)
	add := func(recipient SlackRecipient, source SlackSource) {
		key := strings.Join([]string{recipient.Kind, recipient.ID}, "\x00")
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		result.Recipients = append(result.Recipients, SlackEffectiveRecipient{SlackRecipient: recipient, Source: source})
	}
	for _, recipient := range global.Recipients {
		add(recipient, SlackSourceGlobal)
	}
	if section != nil {
		for _, recipient := range section.Recipients {
			add(recipient, SlackSourceFeature)
		}
	}
	return result
}

func resolveSlackCategory(enabled bool, override SlackOverride) SlackEffectiveCategory {
	switch override {
	case SlackOn:
		return SlackEffectiveCategory{Enabled: true, Source: SlackSourceFeature}
	case SlackOff:
		return SlackEffectiveCategory{Enabled: false, Source: SlackSourceFeature}
	default:
		return SlackEffectiveCategory{Enabled: enabled, Source: SlackSourceGlobal}
	}
}
