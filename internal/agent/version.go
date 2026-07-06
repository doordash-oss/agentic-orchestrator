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

package agent

import (
	"fmt"
	"regexp"
	"strconv"
	"sync"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// VersionResult holds the outcome of checking a single provider's CLI version.
type VersionResult struct {
	Provider string // provider name (e.g. "claude", "codex")
	Version  string // raw version string from VersionInfo()
	Err      error  // non-nil if VersionInfo() failed
	Warning  string // non-empty if version is below minimum or unparseable
}

// CheckProviderVersions calls VersionInfo() on each provider and validates
// the returned version against the provider's own MinVersion(). Returns one
// result per provider, in the same order as the input.
//
// Providers are queried concurrently because VersionInfo() forks an external
// CLI (e.g. `claude --version`) — running them serially was a measurable
// chunk of startup latency.
func CheckProviderVersions(providers []llm.LLMProvider) []VersionResult {
	if len(providers) == 0 {
		return nil
	}
	results := make([]VersionResult, len(providers))
	var wg sync.WaitGroup
	wg.Add(len(providers))
	for i, p := range providers {
		go func(i int, p llm.LLMProvider) {
			defer wg.Done()
			results[i] = checkOneProviderVersion(p)
		}(i, p)
	}
	wg.Wait()
	return results
}

func checkOneProviderVersion(p llm.LLMProvider) VersionResult {
	r := VersionResult{Provider: p.Name()}
	version, err := p.VersionInfo()
	if err != nil {
		r.Err = err
		return r
	}
	r.Version = version
	major, minor, patch, parseErr := parseCLIVersion(version)
	if parseErr != nil {
		r.Warning = fmt.Sprintf("could not parse version %q from %s CLI", version, p.Name())
		return r
	}
	minVer := p.MinVersion()
	if !meetsMinVersion(major, minor, patch, minVer) {
		r.Warning = fmt.Sprintf(
			"%s CLI version %d.%d.%d is below minimum %d.%d.%d; upgrade with: %s",
			p.Name(), major, minor, patch,
			minVer[0], minVer[1], minVer[2],
			p.InstallHint(),
		)
	}
	return r
}

// BelowMinVersion reports whether a provider's installed CLI version parsed
// cleanly and is strictly below its declared MinVersion(). It is the hard
// version gate used at startup for providers that implement llm.VersionEnforcer.
//
// It returns below=false when VersionInfo() errors or its output cannot be
// parsed: those are warn-only conditions surfaced by CheckProviderVersions, not
// hard version failures, so a provider is never filtered out merely because its
// version could not be determined. The returned version is the raw VersionInfo()
// output (empty on error) and minVer echoes the provider's MinVersion(), so
// callers can build a precise diagnostic without re-querying the provider.
func BelowMinVersion(p llm.LLMProvider) (below bool, version string, minVer [3]int) {
	minVer = p.MinVersion()
	version, err := p.VersionInfo()
	if err != nil {
		return false, "", minVer
	}
	major, minor, patch, parseErr := parseCLIVersion(version)
	if parseErr != nil {
		return false, version, minVer
	}
	return !meetsMinVersion(major, minor, patch, minVer), version, minVer
}

// versionRe matches version strings like "1.2.3" or "claude 1.2.3".
var versionRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

func parseCLIVersion(output string) (major, minor, patch int, err error) {
	m := versionRe.FindStringSubmatch(output)
	if m == nil {
		return 0, 0, 0, fmt.Errorf("no version pattern found in %q", output)
	}
	major, _ = strconv.Atoi(m[1])
	minor, _ = strconv.Atoi(m[2])
	patch, _ = strconv.Atoi(m[3])
	return major, minor, patch, nil
}

func meetsMinVersion(major, minor, patch int, minVer [3]int) bool {
	if major != minVer[0] {
		return major > minVer[0]
	}
	if minor != minVer[1] {
		return minor > minVer[1]
	}
	return patch >= minVer[2]
}
