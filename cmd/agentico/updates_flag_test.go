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

package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/selfupdate"
)

func TestParseLaunchArgsUpdatesFlagForms(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    []string
		want    string
		wantErr string
	}{
		{name: "separate value form", args: []string{"server", "--updates", "off"}, want: "off"},
		{name: "equals value form", args: []string{"server", "--updates=off"}, want: "off"},
		{name: "equals notify", args: []string{"server", "--updates=notify"}, want: "notify"},
		{name: "auto parses (resolution rejects)", args: []string{"server", "--updates", "auto"}, want: "auto"},
		{name: "invalid value fails at parse", args: []string{"server", "--updates", "banana"}, wantErr: `invalid --updates value "banana"`},
		{name: "equals invalid value fails at parse", args: []string{"server", "--updates=always"}, wantErr: "invalid --updates value"},
		{name: "missing value", args: []string{"server", "--updates"}, wantErr: "--updates requires a value"},
		{name: "rejected outside server mode (equals)", args: []string{"--updates=off"}, wantErr: "--updates is available only with the headless server"},
		{name: "rejected outside server mode (separate)", args: []string{"--updates", "off"}, wantErr: "--updates is available only with the headless server"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := parseLaunchArgs(tt.args)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("want error %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if opts.updatesPolicy != tt.want {
				t.Fatalf("updatesPolicy = %q, want %q", opts.updatesPolicy, tt.want)
			}
		})
	}
}

func TestClassifyRuntimeEligibilityGathersRealSignals(t *testing.T) {
	// Pin the version signals so the classification is about lease state,
	// not the test binary's own build info.
	originalBuildInfo, originalInjected := updatesBuildInfoVersion, updatesInjectedVersion
	updatesBuildInfoVersion = func() string { return "" }
	updatesInjectedVersion = func() string { return "1.2.3" }
	t.Cleanup(func() {
		updatesBuildInfoVersion, updatesInjectedVersion = originalBuildInfo, originalInjected
	})

	dir := t.TempDir()
	exec := captureFixtureExecutable(t, dir)

	boot := &runtimeBootstrap{selfUpdateExec: exec}
	eligibility := classifyRuntimeEligibility(boot)
	if eligibility.Supported {
		t.Fatalf("no lease held should not be eligible: %+v", eligibility)
	}
	if eligibility.Reason != "lease_unavailable" {
		t.Fatalf("reason = %q, want lease_unavailable (not contention)", eligibility.Reason)
	}

	// Holding the lease upgrades the same installation to eligible.
	boot.updateLease = &selfupdate.Lease{}
	eligibility = classifyRuntimeEligibility(boot)
	if !eligibility.Supported {
		t.Fatalf("held lease should be eligible: %+v", eligibility)
	}

	// Contention is reported as ownership_contention, not a generic lease
	// failure: the running binary is a secondary runtime. The contention
	// error comes from a second acquisition of the same binary lease through
	// the exported acquisition path.
	boot.updateLease = nil
	record := selfupdate.OwnershipRecord{
		RuntimeDir:       dir,
		StateDir:         dir + "/features",
		Config:           dir + "/config.yaml",
		PID:              os.Getpid(),
		Version:          "1.2.3",
		StartedAt:        time.Now(),
		ExecutablePath:   exec.Path,
		ExecutableDigest: exec.Digest,
		ExecutableIno:    exec.ID.Ino,
	}
	firstLease, err := selfupdate.AcquireLease(exec, record)
	if err != nil {
		t.Fatalf("first lease acquisition: %v", err)
	}
	t.Cleanup(func() { _ = firstLease.Close() })
	_, contention := selfupdate.AcquireLease(exec, record)
	boot.updateLeaseErr = contention
	eligibility = classifyRuntimeEligibility(boot)
	if eligibility.Supported || eligibility.Reason != "ownership_contention" {
		t.Fatalf("contention classification = %+v", eligibility)
	}
}
