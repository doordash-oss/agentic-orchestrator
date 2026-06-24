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

package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// runnerResponse is the canned result a fake runner returns for one argv.
type runnerResponse struct {
	out []byte
	err error
}

// fakeModelsRunner dispatches on the space-joined argv so tests can drive the
// `opencode models` discovery attempt chain (--verbose --refresh, --verbose,
// plain) without a real CLI. Unmapped argv fails loudly.
func fakeModelsRunner(t *testing.T, responses map[string]runnerResponse) func(context.Context, string, []string, []string) ([]byte, error) {
	t.Helper()
	return func(_ context.Context, name string, args []string, _ []string) ([]byte, error) {
		if name != "opencode" {
			t.Fatalf("discovery ran %q, want opencode", name)
		}
		key := strings.Join(args, " ")
		if r, ok := responses[key]; ok {
			return r.out, r.err
		}
		return nil, fmt.Errorf("unexpected argv %q", key)
	}
}

const (
	verboseRefreshKey = "models --verbose --refresh"
	verboseKey        = "models --verbose"
	plainKey          = "models"
)

// verboseTwoModels is a realistic `opencode models --verbose` fixture: each
// "provider/model" header line is followed by a pretty-printed JSON object.
const verboseTwoModels = `anthropic/claude-sonnet-4-5
{
  "id": "claude-sonnet-4-5",
  "providerID": "anthropic",
  "name": "Claude Sonnet 4.5",
  "status": "active",
  "cost": { "input": 3, "output": 15, "cache": { "read": 0.3, "write": 3.75 } },
  "limit": { "context": 200000, "output": 64000 }
}
openai/gpt-5-nano
{
  "id": "gpt-5-nano",
  "providerID": "openai",
  "name": "GPT-5 Nano",
  "status": "active",
  "cost": { "input": 0.05, "output": 0.4 },
  "limit": { "context": 400000, "output": 16000 }
}
`

func findModel(models []llm.ModelInfo, id string) (llm.ModelInfo, bool) {
	for _, m := range models {
		if m.ID == id {
			return m, true
		}
	}
	return llm.ModelInfo{}, false
}

// TestDiscover_VerboseSuccess covers a successful verbose discovery: slash-form
// ids, display names, context-window suffixes, unsuffixed aliases, deterministic
// categories, and parsed pricing that drives ComputeCost.
func TestDiscover_VerboseSuccess(t *testing.T) {
	p := &Provider{runner: fakeModelsRunner(t, map[string]runnerResponse{
		verboseRefreshKey: {out: []byte(verboseTwoModels)},
	})}

	models, err := p.DiscoverModelCatalog(context.Background())
	if err != nil {
		t.Fatalf("DiscoverModelCatalog error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("discovered %d models, want 2: %+v", len(models), models)
	}

	sonnet, ok := findModel(models, "anthropic/claude-sonnet-4-5[200K]")
	if !ok {
		t.Fatalf("missing suffixed sonnet id; got %+v", models)
	}
	if sonnet.ContextWindow != 200000 {
		t.Errorf("sonnet ContextWindow = %d, want 200000", sonnet.ContextWindow)
	}
	if sonnet.DisplayName != "Claude Sonnet 4.5 (200K)" {
		t.Errorf("sonnet DisplayName = %q, want %q", sonnet.DisplayName, "Claude Sonnet 4.5 (200K)")
	}
	if sonnet.Category != "balanced" {
		t.Errorf("sonnet Category = %q, want balanced", sonnet.Category)
	}
	if !slices.Contains(sonnet.Aliases, "anthropic/claude-sonnet-4-5") {
		t.Errorf("sonnet aliases = %v, want unsuffixed backend id preserved", sonnet.Aliases)
	}

	nano, ok := findModel(models, "openai/gpt-5-nano[400K]")
	if !ok {
		t.Fatalf("missing suffixed nano id; got %+v", models)
	}
	if nano.Category != "cheap" {
		t.Errorf("nano Category = %q, want cheap", nano.Category)
	}

	// Pricing parsed from cost.{input,output} (USD per 1M tokens) drives
	// ComputeCost via both the suffixed id and the unsuffixed alias.
	p.SetModelCatalog(models)
	if got := p.ComputeCost("anthropic/claude-sonnet-4-5[200K]", 1_000_000, 1_000_000); got != 18 {
		t.Errorf("ComputeCost(suffixed) = %v, want 18 (3 in + 15 out)", got)
	}
	if got := p.ComputeCost("anthropic/claude-sonnet-4-5", 1_000_000, 1_000_000); got != 18 {
		t.Errorf("ComputeCost(alias) = %v, want 18", got)
	}
}

// TestDiscover_LineOnlyFallback proves that when the verbose attempts fail,
// discovery falls back to the plain line-only listing and still produces useful
// entries (ids, heuristic categories) with deterministic zero windows/pricing.
func TestDiscover_LineOnlyFallback(t *testing.T) {
	p := &Provider{runner: fakeModelsRunner(t, map[string]runnerResponse{
		verboseRefreshKey: {err: errors.New("unknown flag --refresh")},
		verboseKey:        {err: errors.New("unknown flag --verbose")},
		plainKey:          {out: []byte("anthropic/claude-sonnet-4-5\nopenai/gpt-5-nano\n")},
	})}

	models, err := p.DiscoverModelCatalog(context.Background())
	if err != nil {
		t.Fatalf("DiscoverModelCatalog error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("discovered %d models, want 2: %+v", len(models), models)
	}
	// No metadata: ids stay unsuffixed and windows are zero.
	if _, ok := findModel(models, "anthropic/claude-sonnet-4-5"); !ok {
		t.Errorf("line-only id should stay unsuffixed; got %+v", models)
	}
	for _, m := range models {
		if m.ContextWindow != 0 {
			t.Errorf("line-only model %q has window %d, want 0", m.ID, m.ContextWindow)
		}
		if strings.Contains(m.ID, "[") {
			t.Errorf("line-only model %q has an invented context suffix", m.ID)
		}
	}
	// No pricing was available, so cost is zero.
	p.SetModelCatalog(models)
	if got := p.ComputeCost("anthropic/claude-sonnet-4-5", 1_000_000, 1_000_000); got != 0 {
		t.Errorf("ComputeCost line-only = %v, want 0", got)
	}
}

func TestDiscover_RefreshTimeoutFallsBackBeforeOverallDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	var attempts []string
	p := &Provider{runner: func(ctx context.Context, name string, args []string, _ []string) ([]byte, error) {
		if name != "opencode" {
			t.Fatalf("discovery ran %q, want opencode", name)
		}
		key := strings.Join(args, " ")
		attempts = append(attempts, key)
		switch key {
		case verboseRefreshKey:
			<-ctx.Done()
			return nil, ctx.Err()
		case verboseKey:
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return []byte(verboseTwoModels), nil
		default:
			return nil, fmt.Errorf("unexpected argv %q", key)
		}
	}}

	models, err := p.DiscoverModelCatalog(ctx)
	if err != nil {
		t.Fatalf("DiscoverModelCatalog error: %v; attempts = %v", err, attempts)
	}
	if _, ok := findModel(models, "anthropic/claude-sonnet-4-5[200K]"); !ok {
		t.Fatalf("models = %+v, want cached verbose fallback after refresh timeout", models)
	}
	if !slices.Equal(attempts, []string{verboseRefreshKey, verboseKey}) {
		t.Fatalf("attempts = %v, want refresh then cached verbose", attempts)
	}
}

// TestDiscover_DuplicateBackendIDs proves a backend id repeated in the output is
// kept only once.
func TestDiscover_DuplicateBackendIDs(t *testing.T) {
	out := "anthropic/claude-sonnet-4-5\nanthropic/claude-sonnet-4-5\nopenai/gpt-5\n"
	models, _, err := parseOpenCodeModels([]byte(out))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2 (duplicate collapsed): %+v", len(models), models)
	}
}

func TestDiscover_AcceptsPortkeyFireworksBackendID(t *testing.T) {
	t.Parallel()
	const backend = "portkey/@fireworks/accounts/fireworks/models/glm-5p2"
	out := backend + "\n" +
		`{ "id": "@fireworks/accounts/fireworks/models/glm-5p2", "providerID": "portkey", "name": "glm-5p2", "status": "active", "limit": { "context": 0 } }` + "\n"

	models, _, err := parseOpenCodeModels([]byte(out))
	if err != nil {
		t.Fatalf("parseOpenCodeModels() error: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1: %+v", len(models), models)
	}
	if models[0].ID != backend {
		t.Fatalf("model ID = %q, want %q", models[0].ID, backend)
	}
	if models[0].DisplayName != "glm-5p2" {
		t.Fatalf("DisplayName = %q, want glm-5p2", models[0].DisplayName)
	}
}

// TestDiscover_MissingDisplayName proves a model with no name still gets a
// non-empty display name derived from the backend id.
func TestDiscover_MissingDisplayName(t *testing.T) {
	out := `anthropic/claude-sonnet-4-5
{
  "id": "claude-sonnet-4-5",
  "providerID": "anthropic",
  "status": "active",
  "limit": { "context": 200000 }
}
`
	models, _, err := parseOpenCodeModels([]byte(out))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	if !strings.Contains(models[0].DisplayName, "claude-sonnet-4-5") {
		t.Errorf("DisplayName = %q, want a non-empty fallback derived from the id", models[0].DisplayName)
	}
}

// TestDiscover_HiddenOrUnavailableExcluded proves models whose status is neither
// empty nor "active" are dropped.
func TestDiscover_HiddenOrUnavailableExcluded(t *testing.T) {
	out := `anthropic/claude-sonnet-4-5
{ "id": "claude-sonnet-4-5", "providerID": "anthropic", "name": "Sonnet", "status": "active", "limit": { "context": 200000 } }
anthropic/claude-secret
{ "id": "claude-secret", "providerID": "anthropic", "name": "Secret", "status": "hidden", "limit": { "context": 200000 } }
openai/gpt-old
{ "id": "gpt-old", "providerID": "openai", "name": "Old", "status": "deprecated" }
`
	models, _, err := parseOpenCodeModels([]byte(out))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1 (hidden/deprecated excluded): %+v", len(models), models)
	}
	if _, ok := findModel(models, "anthropic/claude-secret[200K]"); ok {
		t.Error("hidden model leaked into catalog")
	}
}

// TestDiscover_MalformedOutput proves junk lines are skipped while a valid entry
// in the same output is still recovered.
func TestDiscover_MalformedOutput(t *testing.T) {
	out := `garbage line with spaces
=== not a model ===
{ this is not json }
anthropic/claude-sonnet-4-5
{ "id": "claude-sonnet-4-5", "providerID": "anthropic", "name": "Sonnet", "status": "active", "limit": { "context": 200000 } }
`
	models, _, err := parseOpenCodeModels([]byte(out))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1 valid entry recovered: %+v", len(models), models)
	}
	if models[0].ID != "anthropic/claude-sonnet-4-5[200K]" {
		t.Errorf("recovered id = %q, want anthropic/claude-sonnet-4-5[200K]", models[0].ID)
	}
}

// assertNoModelMentions fails if any catalog entry's id, display name, or alias
// contains the forbidden substring. A malformed, bare-token, or credential-like
// discovery line must never survive into an entry that discovery would report
// through progress or persist to the version-keyed cache.
func assertNoModelMentions(t *testing.T, models []llm.ModelInfo, forbidden string) {
	t.Helper()
	for _, m := range models {
		if strings.Contains(m.ID, forbidden) {
			t.Errorf("model id %q contains forbidden content %q", m.ID, forbidden)
		}
		if strings.Contains(m.DisplayName, forbidden) {
			t.Errorf("model display name %q contains forbidden content %q", m.DisplayName, forbidden)
		}
		for _, a := range m.Aliases {
			if strings.Contains(a, forbidden) {
				t.Errorf("model alias %q contains forbidden content %q", a, forbidden)
			}
		}
	}
}

// TestDiscover_RejectsBareTokenModelID proves a discovery line that is not a
// slash-form provider/model id — a bare token such as "bareword" — is dropped
// rather than carried into the catalog, so OpenCode never captures a bare model
// name and malformed stdout cannot manufacture a catalog entry. A valid
// slash-form id in the same output still survives, and output that is nothing
// but bare tokens yields an error so discovery fails over to the fallback.
func TestDiscover_RejectsBareTokenModelID(t *testing.T) {
	out := "bareword\nanthropic/claude-sonnet-4-5\nopusish\n"
	models, _, err := parseOpenCodeModels([]byte(out))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1 (bare tokens dropped): %+v", len(models), models)
	}
	if models[0].ID != "anthropic/claude-sonnet-4-5" {
		t.Errorf("surviving id = %q, want anthropic/claude-sonnet-4-5", models[0].ID)
	}
	assertNoModelMentions(t, models, "bareword")
	assertNoModelMentions(t, models, "opusish")

	if _, _, err := parseOpenCodeModels([]byte("bareword\nanother\n")); err == nil {
		t.Error("parseOpenCodeModels(bare tokens) = nil error, want error so discovery fails over to fallback")
	}
}

// TestDiscover_RejectsCredentialLikeModelID proves a credential-shaped discovery
// line never becomes a catalog entry, whether it appears as a bare token or as
// the model part of an otherwise slash-form id. This keeps malformed or hostile
// stdout from leaking credential-like content into a reported or cached catalog
// entry. A valid slash-form id in the same output still survives.
func TestDiscover_RejectsCredentialLikeModelID(t *testing.T) {
	const cred = "sk-ant-deadbeefdeadbeefdeadbeef0123"
	out := cred + "\n" +
		"anthropic/" + cred + "\n" +
		"anthropic/claude-sonnet-4-5\n"
	models, _, err := parseOpenCodeModels([]byte(out))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1 (credential lines dropped): %+v", len(models), models)
	}
	if models[0].ID != "anthropic/claude-sonnet-4-5" {
		t.Errorf("surviving id = %q, want anthropic/claude-sonnet-4-5", models[0].ID)
	}
	assertNoModelMentions(t, models, cred)
	assertNoModelMentions(t, models, "sk-ant-")

	if _, _, err := parseOpenCodeModels([]byte(cred + "\n")); err == nil {
		t.Errorf("parseOpenCodeModels(%q) = nil error, want error so discovery fails over to fallback", cred)
	}
}

// TestDiscover_EmptyOutput proves empty/whitespace-only output yields an error
// (so discovery degrades to the next attempt or the provider fallback).
func TestDiscover_EmptyOutput(t *testing.T) {
	for _, out := range []string{"", "   \n  \n"} {
		if _, _, err := parseOpenCodeModels([]byte(out)); err == nil {
			t.Errorf("parseOpenCodeModels(%q) = nil error, want error for empty output", out)
		}
	}
}

// TestDiscover_ProgressReportingOrder proves each discovered model is reported
// once, in catalog order, and only for the winning attempt.
func TestDiscover_ProgressReportingOrder(t *testing.T) {
	p := &Provider{runner: fakeModelsRunner(t, map[string]runnerResponse{
		verboseRefreshKey: {out: []byte(verboseTwoModels)},
	})}

	var reported []string
	models, err := p.DiscoverModelCatalogWithProgress(context.Background(), func(m llm.ModelInfo) {
		reported = append(reported, m.ID)
	})
	if err != nil {
		t.Fatalf("discovery error: %v", err)
	}
	want := make([]string, len(models))
	for i, m := range models {
		want[i] = m.ID
	}
	if !slices.Equal(reported, want) {
		t.Fatalf("reported %v, want %v (one report per model in catalog order)", reported, want)
	}
}

// TestDiscover_ReReportsOnlyWinningAttempt proves that when verbose attempts fail
// before the line-only attempt succeeds, models are reported exactly once (no
// double-reporting across the attempt chain).
func TestDiscover_ReReportsOnlyWinningAttempt(t *testing.T) {
	p := &Provider{runner: fakeModelsRunner(t, map[string]runnerResponse{
		verboseRefreshKey: {err: errors.New("boom")},
		verboseKey:        {out: []byte("{ truncated json that never closes")},
		plainKey:          {out: []byte("anthropic/claude-sonnet-4-5\nopenai/gpt-5\n")},
	})}

	var reported []string
	_, err := p.DiscoverModelCatalogWithProgress(context.Background(), func(m llm.ModelInfo) {
		reported = append(reported, m.ID)
	})
	if err != nil {
		t.Fatalf("discovery error: %v", err)
	}
	if len(reported) != 2 {
		t.Fatalf("reported %d times, want 2 (no double-report across attempts): %v", len(reported), reported)
	}
}

// TestDiscover_SanitizesErrorDiagnostics proves a discovery failure surfaces a
// sanitized error: credential-like content and terminal-control bytes from the
// CLI error are scrubbed before the error can reach a startup warning, log, or
// cache diagnostic.
func TestDiscover_SanitizesErrorDiagnostics(t *testing.T) {
	withEnv(t, []string{"PATH=/usr/bin"})
	esc := string(rune(0x1b))
	bel := string(rune(0x07))
	leak := esc + "[31mauth failed token=sk-ant-DISCOVERYLEAK1234567890" + bel
	p := &Provider{runner: fakeModelsRunner(t, map[string]runnerResponse{
		verboseRefreshKey: {err: errors.New(leak)},
		verboseKey:        {err: errors.New(leak)},
		plainKey:          {err: errors.New(leak)},
	})}

	_, err := p.DiscoverModelCatalog(context.Background())
	if err == nil {
		t.Fatal("DiscoverModelCatalog = nil error, want failure")
	}
	msg := err.Error()
	if strings.Contains(msg, "sk-ant-DISCOVERYLEAK1234567890") {
		t.Errorf("discovery error leaked a credential: %q", msg)
	}
	if strings.ContainsRune(msg, rune(0x1b)) || strings.ContainsRune(msg, rune(0x07)) {
		t.Errorf("discovery error leaked terminal-control bytes: %q", msg)
	}
}

// TestDiscover_SanitizesMaliciousCatalogText proves terminal-control content in a
// model's display name is stripped before it can enter a catalog entry, progress
// update, cache artifact, or transcript. The ESC byte is built at runtime and
// json.Marshal encodes it as a valid JSON  escape, exactly as a real CLI
// would have to encode it; the parser must then strip the decoded control bytes.
func TestDiscover_SanitizesMaliciousCatalogText(t *testing.T) {
	esc := string(rune(0x1b))
	nameJSON, err := json.Marshal("Claude " + esc + "[31mEVIL" + esc + "[0m Sonnet")
	if err != nil {
		t.Fatalf("marshal name: %v", err)
	}
	out := "anthropic/claude-sonnet-4-5\n" +
		`{ "id": "claude-sonnet-4-5", "providerID": "anthropic", "name": ` + string(nameJSON) +
		`, "status": "active", "limit": { "context": 200000 } }` + "\n"

	models, _, perr := parseOpenCodeModels([]byte(out))
	if perr != nil {
		t.Fatalf("parse error: %v", perr)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	name := models[0].DisplayName
	if strings.ContainsRune(name, rune(0x1b)) {
		t.Errorf("display name retained an escape byte: %q", name)
	}
	if !strings.Contains(name, "EVIL") {
		t.Errorf("display name lost its visible text: %q", name)
	}
}
