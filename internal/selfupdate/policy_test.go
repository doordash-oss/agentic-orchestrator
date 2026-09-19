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
	"errors"
	"testing"
	"time"
)

func TestResolveStartupSettingsPrecedence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		src  SettingsSources
		want Policy
	}{
		{name: "default notify", src: SettingsSources{}, want: PolicyNotify},
		{name: "flag wins over env and config", src: SettingsSources{Flag: "off", Env: "notify", ConfigPolicy: "notify"}, want: PolicyOff},
		{name: "env wins over config", src: SettingsSources{Env: "off", ConfigPolicy: "notify"}, want: PolicyOff},
		{name: "config used when others unset", src: SettingsSources{ConfigPolicy: "off"}, want: PolicyOff},
		{name: "blank env does not shadow config", src: SettingsSources{Env: "  ", ConfigPolicy: "off"}, want: PolicyOff},
		{name: "trimmed values", src: SettingsSources{Flag: " off "}, want: PolicyOff},
		{name: "auto flag", src: SettingsSources{Flag: "auto"}, want: PolicyAuto},
		{name: "auto env", src: SettingsSources{Env: "auto"}, want: PolicyAuto},
		{name: "auto config", src: SettingsSources{ConfigPolicy: "auto"}, want: PolicyAuto},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings, err := ResolveStartupSettings(tt.src)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if settings.Policy != tt.want {
				t.Fatalf("policy = %q, want %q", settings.Policy, tt.want)
			}
		})
	}
}

func TestWindowContainsAndNextOpen(t *testing.T) {
	t.Parallel()
	loc := time.FixedZone("test", 0)
	at := func(h, m int) time.Time { return time.Date(2026, 3, 10, h, m, 0, 0, loc) }
	tests := []struct {
		spec     string
		now      time.Time
		contains bool
		nextOpen time.Time
	}{
		{"01:00-05:00", at(2, 30), true, at(2, 30)},
		{"01:00-05:00", at(5, 0), false, at(1, 0).AddDate(0, 0, 1)},
		{"01:00-05:00", at(0, 59), false, at(1, 0)},
		{"22:00-02:00", at(23, 0), true, at(23, 0)},
		{"22:00-02:00", at(1, 59), true, at(1, 59)},
		{"22:00-02:00", at(12, 0), false, at(22, 0)},
	}
	for _, tt := range tests {
		w, err := ParseWindow(tt.spec)
		if err != nil {
			t.Fatalf("%s: %v", tt.spec, err)
		}
		if got := w.Contains(tt.now); got != tt.contains {
			t.Fatalf("%s contains %v = %v, want %v", tt.spec, tt.now, got, tt.contains)
		}
		if got := w.NextOpen(tt.now); !got.Equal(tt.nextOpen) {
			t.Fatalf("%s next open from %v = %v, want %v", tt.spec, tt.now, got, tt.nextOpen)
		}
	}
	for _, bad := range []string{"01:00", "1:00-05:00", "01:00-01:00", "24:00-01:00", "01:60-02:00", "01.00-02:00"} {
		if _, err := ParseWindow(bad); err == nil {
			t.Fatalf("%q: want parse error", bad)
		}
	}
}

func TestResolveStartupSettingsInvalidValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		src    SettingsSources
		field  string
		reason string
	}{
		{name: "garbage flag", src: SettingsSources{Flag: "banana"}, field: "--updates"},
		{name: "garbage env", src: SettingsSources{Env: "always"}, field: "AGENTICO_UPDATES"},
		{name: "garbage config policy", src: SettingsSources{ConfigPolicy: "yes"}, field: "server.updates.policy"},
		{name: "unknown channel", src: SettingsSources{ConfigChannel: "beta"}, field: "server.updates.channel"},
		{name: "zero interval", src: SettingsSources{ConfigCheckInterval: "0s"}, field: "server.updates.check_interval"},
		{name: "negative interval", src: SettingsSources{ConfigCheckInterval: "-1h"}, field: "server.updates.check_interval"},
		{name: "non-duration interval", src: SettingsSources{ConfigCheckInterval: "6 hours"}, field: "server.updates.check_interval"},
		{name: "unknown strategy", src: SettingsSources{ConfigStrategy: "aggressive"}, field: "server.updates.strategy"},
		{name: "window missing side", src: SettingsSources{ConfigWindow: "09:00"}, field: "server.updates.window"},
		{name: "window bad hour", src: SettingsSources{ConfigWindow: "24:00-08:00"}, field: "server.updates.window"},
		{name: "window bad minute", src: SettingsSources{ConfigWindow: "09:60-08:00"}, field: "server.updates.window"},
		{name: "window bad syntax", src: SettingsSources{ConfigWindow: "9:00-17:00"}, field: "server.updates.window"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ResolveStartupSettings(tt.src)
			var invalid *SettingsError
			if !errors.As(err, &invalid) {
				t.Fatalf("want SettingsError, got %v", err)
			}
			if invalid.Field != tt.field {
				t.Fatalf("field = %q, want %q", invalid.Field, tt.field)
			}
		})
	}
}

func TestResolveStartupSettingsDefaultsAndValidValues(t *testing.T) {
	t.Parallel()
	settings, err := ResolveStartupSettings(SettingsSources{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if settings.CheckInterval != 6*time.Hour {
		t.Fatalf("default interval = %v, want 6h", settings.CheckInterval)
	}
	if settings.Strategy != "quiesce" {
		t.Fatalf("default strategy = %q, want quiesce", settings.Strategy)
	}
	if settings.Channel != "stable" {
		t.Fatalf("default channel = %q, want stable", settings.Channel)
	}

	settings, err = ResolveStartupSettings(SettingsSources{
		ConfigCheckInterval: "30m",
		ConfigStrategy:      "idle",
		ConfigWindow:        "09:00-17:00",
		ConfigChannel:       "stable",
	})
	if err != nil {
		t.Fatalf("resolve valid: %v", err)
	}
	if settings.CheckInterval != 30*time.Minute {
		t.Fatalf("interval = %v, want 30m", settings.CheckInterval)
	}
	if settings.Strategy != "idle" || settings.Window != "09:00-17:00" {
		t.Fatalf("strategy/window = %q/%q", settings.Strategy, settings.Window)
	}
}

func TestJitterDelayClampedForShortIntervals(t *testing.T) {
	t.Parallel()
	if got := JitterDelay(6 * time.Hour); got != 20*time.Minute {
		t.Fatalf("6h jitter = %v, want 20m", got)
	}
	if got := JitterDelay(time.Minute); got != 15*time.Second {
		t.Fatalf("1m jitter = %v, want 15s", got)
	}
	if got := JitterDelay(time.Second); got != 250*time.Millisecond {
		t.Fatalf("1s jitter = %v, want 250ms", got)
	}
	// Every realized delay stays positive for short intervals: the band is
	// [interval - magnitude, interval + magnitude] and magnitude is at most
	// a quarter of the interval.
	for _, interval := range []time.Duration{time.Second, 5 * time.Second, time.Minute} {
		magnitude := JitterDelay(interval)
		if interval-magnitude <= 0 {
			t.Fatalf("interval %v with magnitude %v can produce non-positive delays", interval, magnitude)
		}
	}
}
