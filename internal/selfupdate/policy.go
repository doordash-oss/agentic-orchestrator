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

package selfupdate

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Policy is the effective startup update policy for a headless server.
type Policy string

const (
	// PolicyOff disables all update checks: no check timer, no feed traffic,
	// and update mutations are refused.
	PolicyOff Policy = "off"
	// PolicyNotify checks release metadata on a schedule and on explicit
	// request. It never installs anything.
	PolicyNotify Policy = "notify"
	// PolicyAuto is recognized by the parser so misconfiguration produces an
	// explicit unsupported-policy error instead of a silent fallback, but no
	// runtime in this phase may serve under it.
	PolicyAuto Policy = "auto"
)

// PolicyDefault is the policy used when no source expresses a preference.
const PolicyDefault = PolicyNotify

// ParsePolicyValue parses one raw policy value (flag, env, or config) after
// trimming. ok is false for any value that is not exactly off, notify, or
// auto; the caller reports it as invalid configuration rather than guessing.
func ParsePolicyValue(v string) (Policy, bool) {
	switch Policy(strings.TrimSpace(v)) {
	case PolicyOff:
		return PolicyOff, true
	case PolicyNotify:
		return PolicyNotify, true
	case PolicyAuto:
		return PolicyAuto, true
	default:
		return "", false
	}
}

const (
	// DefaultCheckInterval is the default periodic check interval: six hours.
	DefaultCheckInterval = 6 * time.Hour
	// DefaultChannel is the only supported release channel.
	DefaultChannel = "stable"
	// DefaultStrategy is the default (reserved) update strategy.
	DefaultStrategy = "quiesce"
	// MaxCheckJitter bounds periodic schedule jitter: twenty minutes.
	MaxCheckJitter = 20 * time.Minute
)

// StartupSettings is the resolved, validated update configuration a server
// runs under. Strategy and Window are reserved: they are parsed, validated,
// and reported for transparency, but they never schedule work and never
// affect manual installation in this phase.
type StartupSettings struct {
	Policy        Policy
	Channel       string
	CheckInterval time.Duration
	Strategy      string
	Window        string
}

// SettingsSources carries one raw value per precedence level. Every field is
// the untouched string from its source; empty means the source expressed no
// preference. Resolution order is explicit server flag over AGENTICO_UPDATES
// over server configuration over the notify default.
type SettingsSources struct {
	// Flag is the raw --updates value (both --updates=v and --updates v forms
	// normalize to this).
	Flag string
	// Env is the raw AGENTICO_UPDATES value.
	Env string
	// ConfigPolicy through ConfigWindow mirror the server.updates config map.
	ConfigPolicy        string
	ConfigChannel       string
	ConfigCheckInterval string
	ConfigStrategy      string
	ConfigWindow        string
}

// SettingsError describes one invalid update setting. Field names the setting
// (flag, env, or a config key), Value is the offending raw value, and Reason
// explains the expected form.
type SettingsError struct {
	Field  string
	Value  string
	Reason string
}

func (e *SettingsError) Error() string {
	return fmt.Sprintf("invalid update setting %s=%q: %s", e.Field, e.Value, e.Reason)
}

// UnsupportedPolicyError reports an effective policy value — auto — that no
// runtime in this phase supports. It is raised instead of silently falling
// back so operators see the misconfiguration at startup.
type UnsupportedPolicyError struct {
	Value  string
	Source string
}

func (e *UnsupportedPolicyError) Error() string {
	return fmt.Sprintf("update policy %q from %s is not supported; use off or notify", e.Value, e.Source)
}

// ResolveStartupSettings resolves and validates the effective update startup
// settings from the precedence chain. An effective auto policy returns
// *UnsupportedPolicyError; malformed values return *SettingsError.
func ResolveStartupSettings(src SettingsSources) (StartupSettings, error) {
	settings := StartupSettings{
		Policy:        PolicyDefault,
		Channel:       DefaultChannel,
		CheckInterval: DefaultCheckInterval,
		Strategy:      DefaultStrategy,
	}

	rawPolicy, source := "", "default"
	switch {
	case strings.TrimSpace(src.Flag) != "":
		rawPolicy, source = src.Flag, "--updates"
	case strings.TrimSpace(src.Env) != "":
		rawPolicy, source = src.Env, "AGENTICO_UPDATES"
	case strings.TrimSpace(src.ConfigPolicy) != "":
		rawPolicy, source = src.ConfigPolicy, "server.updates.policy"
	}
	if rawPolicy != "" {
		policy, ok := ParsePolicyValue(rawPolicy)
		if !ok {
			return settings, &SettingsError{
				Field:  source,
				Value:  rawPolicy,
				Reason: "expected off, notify, or auto",
			}
		}
		if policy == PolicyAuto {
			return settings, &UnsupportedPolicyError{Value: string(policy), Source: source}
		}
		settings.Policy = policy
	}

	if v := strings.TrimSpace(src.ConfigChannel); v != "" {
		if v != DefaultChannel {
			return settings, &SettingsError{
				Field:  "server.updates.channel",
				Value:  src.ConfigChannel,
				Reason: fmt.Sprintf("expected %q; only the stable channel is supported", DefaultChannel),
			}
		}
		settings.Channel = v
	}

	if v := strings.TrimSpace(src.ConfigCheckInterval); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return settings, &SettingsError{
				Field:  "server.updates.check_interval",
				Value:  src.ConfigCheckInterval,
				Reason: "expected a positive Go duration like 6h or 30m",
			}
		}
		settings.CheckInterval = d
	}

	if v := strings.TrimSpace(src.ConfigStrategy); v != "" {
		if v != "idle" && v != "quiesce" {
			return settings, &SettingsError{
				Field:  "server.updates.strategy",
				Value:  src.ConfigStrategy,
				Reason: "expected idle or quiesce",
			}
		}
		settings.Strategy = v
	}

	if v := strings.TrimSpace(src.ConfigWindow); v != "" {
		if err := ValidateWindow(v); err != nil {
			return settings, &SettingsError{
				Field:  "server.updates.window",
				Value:  src.ConfigWindow,
				Reason: err.Error(),
			}
		}
		settings.Window = v
	}

	return settings, nil
}

// ValidateWindow checks the reserved local-time maintenance-window syntax:
// HH:MM-HH:MM with 00-23 hours and 00-59 minutes on both sides. The window is
// never consulted for scheduling in this phase; validation exists so a typo
// fails at startup instead of silently changing behavior later.
func ValidateWindow(v string) error {
	const layout = "15:04"
	sides := strings.SplitN(v, "-", 2)
	if len(sides) != 2 {
		return fmt.Errorf("expected HH:MM-HH:MM in local time")
	}
	for _, side := range sides {
		if len(side) != len(layout) {
			return fmt.Errorf("expected HH:MM-HH:MM in local time")
		}
		hour, err := strconv.Atoi(side[0:2])
		if err != nil || hour < 0 || hour > 23 {
			return fmt.Errorf("expected HH:MM-HH:MM in local time")
		}
		minute, err := strconv.Atoi(side[3:5])
		if err != nil || minute < 0 || minute > 59 {
			return fmt.Errorf("expected HH:MM-HH:MM in local time")
		}
	}
	return nil
}

// JitterDelay returns the periodic scheduling jitter for the configured
// interval: at most MaxCheckJitter, clamped to a quarter of the interval so
// even very short test intervals keep every realized delay positive. The
// returned magnitude is the half-width of the jitter band; the caller draws
// an offset in [-magnitude, +magnitude).
func JitterDelay(interval time.Duration) time.Duration {
	magnitude := MaxCheckJitter
	if interval > 0 && interval/4 < magnitude {
		magnitude = interval / 4
	}
	return magnitude
}
