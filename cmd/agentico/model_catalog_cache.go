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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

const providerCatalogCacheDirName = "model-catalogs"

// versionTokenPattern matches a clean, bounded version token: a semver core
// (MAJOR.MINOR.PATCH) with an optional leading "v" and optional pre-release /
// build metadata drawn from a restricted character set. It is anchored at both
// ends so a version carrying whitespace, terminal-control bytes, embedded
// newlines, or trailing credential-like content (for example a hostile
// `opencode --version` such as "1.17.9 sk-ant-...") fails to match.
var versionTokenPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+.][0-9A-Za-z][0-9A-Za-z.+_-]*)?$`)

// maxCacheableVersionLen bounds a version token's length so an absurdly long
// value cannot become a filename segment or persisted metadata even if it is
// otherwise version-shaped.
const maxCacheableVersionLen = 48

// errUncacheableVersion is returned by the cache read/write helpers when a
// provider version is not safe to use as a cache key, filename, or persisted
// metadata. The sentinel deliberately omits the offending version so the
// credential-like or terminal-control content a malformed version may carry can
// never reach a warning, a log line, or captured evidence through the error.
var errUncacheableVersion = errors.New("provider version is not cacheable")

// cacheableVersion reports whether version is safe to use as a model-catalog
// cache key, filename segment, persisted metadata value, and diagnostic token.
// Only a bounded, version-shaped token qualifies; any other value — empty,
// overlong, whitespace- or control-bearing, or carrying trailing credential-like
// content — is rejected so it can never reach a cache path, the cache JSON, or a
// warning string. The startup path first normalizes provider VersionInfo output
// through clirun.ParseVersionOutput; this function is the final backstop the cache
// read/write helpers apply independently, so a provider whose version cannot be
// reduced to a clean token degrades to running discovery without caching rather
// than leaking that output.
func cacheableVersion(version string) bool {
	if version == "" || len(version) > maxCacheableVersionLen {
		return false
	}
	return versionTokenPattern.MatchString(version)
}

type providerCatalogCacheFile struct {
	Provider     string          `json:"provider"`
	Version      string          `json:"version"`
	DiscoveredAt time.Time       `json:"discovered_at"`
	Models       []llm.ModelInfo `json:"models"`
}

func providerCatalogCachePath(cacheRoot, provider, version string) string {
	return filepath.Join(
		cacheRoot,
		providerCatalogCacheDirName,
		safeCacheSegment(provider),
		safeCacheSegment(version)+".json",
	)
}

func loadProviderCatalogCacheFile(cacheRoot, provider, version string) (providerCatalogCacheFile, error) {
	if !cacheableVersion(version) {
		// Refuse before building a cache path from the version: an unvalidated
		// version could otherwise embed credential-like or terminal-control
		// content into the path that callers echo in cache-read warnings.
		return providerCatalogCacheFile{}, errUncacheableVersion
	}
	path := providerCatalogCachePath(cacheRoot, provider, version)
	data, err := os.ReadFile(path)
	if err != nil {
		return providerCatalogCacheFile{}, err
	}

	var cached providerCatalogCacheFile
	if err := json.Unmarshal(data, &cached); err != nil {
		return providerCatalogCacheFile{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if cached.Provider != provider || cached.Version != version {
		return providerCatalogCacheFile{}, fmt.Errorf("cache metadata mismatch in %s", path)
	}
	if len(cached.Models) == 0 {
		return providerCatalogCacheFile{}, fmt.Errorf("empty model catalog in %s", path)
	}
	return cached, nil
}

func loadProviderCatalogCache(cacheRoot, provider, version string) ([]llm.ModelInfo, error) {
	cached, err := loadProviderCatalogCacheFile(cacheRoot, provider, version)
	if err != nil {
		return nil, err
	}
	return cached.Models, nil
}

func saveProviderCatalogCache(cacheRoot, provider, version string, models []llm.ModelInfo) error {
	if len(models) == 0 {
		return fmt.Errorf("cannot cache empty model catalog")
	}
	if !cacheableVersion(version) {
		// Never persist a malformed version into the cache path or metadata: it
		// could carry credential-like or terminal-control content, and the error
		// (which omits the version) is what cache-write warnings surface.
		return errUncacheableVersion
	}
	path := providerCatalogCachePath(cacheRoot, provider, version)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating model catalog cache dir: %w", err)
	}
	payload := providerCatalogCacheFile{
		Provider:     provider,
		Version:      version,
		DiscoveredAt: time.Now().UTC(),
		Models:       models,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal model catalog cache: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write model catalog cache: %w", err)
	}
	return nil
}

func safeCacheSegment(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}
