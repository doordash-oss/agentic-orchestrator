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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

const providerCatalogCacheDirName = "model-catalogs"

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

func loadProviderCatalogCache(cacheRoot, provider, version string) ([]llm.ModelInfo, error) {
	path := providerCatalogCachePath(cacheRoot, provider, version)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cached providerCatalogCacheFile
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cached.Provider != provider || cached.Version != version {
		return nil, fmt.Errorf("cache metadata mismatch in %s", path)
	}
	if len(cached.Models) == 0 {
		return nil, fmt.Errorf("empty model catalog in %s", path)
	}
	return cached.Models, nil
}

func saveProviderCatalogCache(cacheRoot, provider, version string, models []llm.ModelInfo) error {
	if len(models) == 0 {
		return fmt.Errorf("cannot cache empty model catalog")
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
