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

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
)

const discoverySchemaVersion = 1

func DiscoveryPath(runtimeDir string) string {
	return filepath.Join(runtimeDir, discoveryFilename)
}

func ReadDiscovery(runtimeDir string) (DiscoveryRecord, error) {
	data, err := os.ReadFile(DiscoveryPath(runtimeDir))
	if err != nil {
		return DiscoveryRecord{}, fmt.Errorf("read discovery: %w", err)
	}
	var rec DiscoveryRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return DiscoveryRecord{}, fmt.Errorf("parse discovery: %w", err)
	}
	return rec, nil
}

func PublishDiscovery(runtimeDir string, rec DiscoveryRecord) error {
	if runtimeDir == "" {
		return errors.New("runtime dir is empty")
	}
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return fmt.Errorf("create runtime dir: %w", err)
	}
	if rec.SchemaVersion == 0 {
		rec.SchemaVersion = discoverySchemaVersion
	}
	if rec.APIVersion == "" {
		rec.APIVersion = APIVersion
	}
	if rec.PublishedAt.IsZero() {
		rec.PublishedAt = time.Now().UTC()
	}
	if rec.MCP == (MCPMetadata{}) {
		rec.MCP = mcpMetadataForBaseURL(rec.BaseURL)
	}
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rec); err != nil {
		return fmt.Errorf("marshal discovery: %w", err)
	}

	tmp, err := os.OpenFile(
		filepath.Join(runtimeDir, fmt.Sprintf(".agentico-server-%d.json.tmp", os.Getpid())),
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return fmt.Errorf("create discovery temp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(body.Bytes()); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write discovery temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close discovery temp: %w", err)
	}
	if err := os.Rename(tmpName, DiscoveryPath(runtimeDir)); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("commit discovery: %w", err)
	}
	if err := os.Chmod(DiscoveryPath(runtimeDir), 0o600); err != nil {
		return fmt.Errorf("chmod discovery: %w", err)
	}
	return nil
}

func mcpMetadataForBaseURL(baseURL string) MCPMetadata {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return MCPMetadata{
		Transport:      "streamable_http",
		Path:           MCPEndpointPath,
		Endpoint:       baseURL + MCPEndpointPath,
		RESTAPIVersion: APIVersion,
	}
}

func NewLaunchPolicy(enabledProviders []string, dangerouslySkipPerms bool) LaunchPolicy {
	return LaunchPolicy{
		Resolved:                   true,
		Providers:                  normalizePolicyProviders(enabledProviders),
		DangerouslySkipPermissions: dangerouslySkipPerms,
	}
}

func PrepareDiscovery(ctx context.Context, runtimeDir string, identity RuntimeIdentity, policy LaunchPolicy, client *http.Client) (DiscoveryDecision, error) {
	path := DiscoveryPath(runtimeDir)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return DiscoveryDecision{}, nil
	} else if err != nil {
		return DiscoveryDecision{Replace: true, Reason: "discovery stat failed"}, nil
	}
	if err := validateDiscoveryFileSecurity(path); err != nil {
		return DiscoveryDecision{Replace: true, Reason: "unsafe discovery permissions"}, nil
	}
	rec, err := ReadDiscovery(runtimeDir)
	if err != nil {
		return DiscoveryDecision{Replace: true, Reason: "unreadable discovery"}, nil
	}
	if !isLoopbackBaseURL(rec.BaseURL) {
		return DiscoveryDecision{Replace: true, Reason: "non-loopback discovery base_url", Record: rec}, nil
	}
	if rec.APIVersion != APIVersion || rec.Runtime != identity {
		return DiscoveryDecision{Replace: true, Reason: "mismatched discovery runtime", Record: rec}, nil
	}
	if !launchPolicyEquivalent(policy, rec.LaunchPolicy) {
		return DiscoveryDecision{Replace: true, Reason: "mismatched discovery policy", Record: rec}, nil
	}
	health, ok, reason := discoveryHealth(ctx, client, rec.BaseURL)
	if !ok {
		if reason == "" {
			reason = "stale discovery"
		}
		return DiscoveryDecision{Replace: true, Reason: reason, Record: rec}, nil
	}
	if health.Runtime != identity {
		return DiscoveryDecision{Replace: true, Reason: "mismatched discovery runtime", Record: rec}, nil
	}
	if !launchPolicyEquivalent(policy, health.LaunchPolicy) {
		return DiscoveryDecision{Replace: true, Reason: "mismatched discovery policy", Record: rec}, nil
	}
	if (!rec.StartedAt.IsZero() || !health.StartedAt.IsZero()) && !rec.StartedAt.Equal(health.StartedAt) {
		return DiscoveryDecision{Replace: true, Reason: "mismatched discovery health", Record: rec}, nil
	}
	if rec.Owner != health.Owner {
		return DiscoveryDecision{Replace: true, Reason: "mismatched discovery owner", Record: rec}, nil
	}
	return DiscoveryDecision{AlreadyRunning: true, Reason: "matching healthy server", Record: rec}, nil
}

func validateDiscoveryFileSecurity(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("discovery file is readable or writable by non-owner")
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("discovery owner metadata unavailable")
	}
	if int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("discovery file is owned by uid %d", stat.Uid)
	}
	return nil
}

func isLoopbackBaseURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isLoopbackOrigin(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func discoveryHealthOK(ctx context.Context, client *http.Client, baseURL string) bool {
	_, ok, _ := discoveryHealth(ctx, client, baseURL)
	return ok
}

func discoveryHealth(ctx context.Context, client *http.Client, baseURL string) (HealthResponse, bool, string) {
	if client == nil {
		client = &http.Client{Timeout: time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/v1/health", nil)
	if err != nil {
		return HealthResponse{}, false, "invalid discovery health URL"
	}
	resp, err := client.Do(req)
	if err != nil {
		return HealthResponse{}, false, "dead discovery port"
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return HealthResponse{}, false, "unhealthy discovery response"
	}
	var health HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return HealthResponse{}, false, "unreadable discovery health"
	}
	if health.APIVersion != APIVersion || health.Status != "ok" {
		return HealthResponse{}, false, "mismatched discovery health"
	}
	return health, true, ""
}

func launchPolicyEquivalent(a, b LaunchPolicy) bool {
	if !a.Resolved || !b.Resolved {
		return false
	}
	if a.DangerouslySkipPermissions != b.DangerouslySkipPermissions {
		return false
	}
	ap := normalizePolicyProviders(a.Providers)
	bp := normalizePolicyProviders(b.Providers)
	if len(ap) != len(bp) {
		return false
	}
	for i := range ap {
		if ap[i] != bp[i] {
			return false
		}
	}
	return true
}

func normalizePolicyProviders(providers []string) []string {
	if providers == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, provider := range providers {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider == "" || seen[provider] {
			continue
		}
		seen[provider] = true
		out = append(out, provider)
	}
	sort.Strings(out)
	return out
}
