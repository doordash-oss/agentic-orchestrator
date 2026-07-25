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
	"fmt"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/clirun"
)

// openCodeVerboseModel is the subset of the JSON object that `opencode models
// --verbose` prints after each "provider/model" header line. Cost amounts are
// USD per million tokens (models.dev convention); pointer fields distinguish an
// explicitly-zero value from an absent one.
type openCodeVerboseModel struct {
	ID         string              `json:"id"`
	ProviderID string              `json:"providerID"`
	Name       string              `json:"name"`
	Status     string              `json:"status"`
	Cost       *openCodeModelCost  `json:"cost"`
	Limit      *openCodeModelLimit `json:"limit"`
}

type openCodeModelCost struct {
	Input  *float64 `json:"input"`
	Output *float64 `json:"output"`
}

type openCodeModelLimit struct {
	Context int `json:"context"`
}

// discoveryAttempts lists the `opencode models` invocations discovery tries, in
// order: rich metadata refreshed from models.dev, rich metadata from the local
// cache (no network), then the plain line-only listing. The first attempt that
// yields at least one accepted entry wins; the parser handles both the verbose
// (header + JSON) and the line-only shapes, so a CLI that lacks --verbose or has
// no network access still produces a usable catalog.
func discoveryAttempts() [][]string {
	return [][]string{
		{"models", "--verbose", "--refresh"},
		{"models", "--verbose"},
		{"models"},
	}
}

// DiscoverModelCatalog refreshes OpenCode's model catalog from the local CLI.
func (p *Provider) DiscoverModelCatalog(ctx context.Context) ([]llm.ModelInfo, error) {
	return p.DiscoverModelCatalogWithProgress(ctx, nil)
}

// DiscoverModelCatalogWithProgress refreshes OpenCode's model catalog and, on
// success, reports each parsed model in catalog order before returning. Pricing
// parsed alongside the catalog is stored on the provider for ComputeCost. The
// returned error is sanitized so a failed discovery cannot leak credential-like
// or terminal-control content into a startup warning, log, or cache diagnostic.
func (p *Provider) DiscoverModelCatalogWithProgress(ctx context.Context, report llm.ModelDiscoveryReporter) ([]llm.ModelInfo, error) {
	runner := p.runner
	if runner == nil {
		runner = clirun.DefaultRunner()
	}

	binary := p.cliBinary()
	attempts := discoveryAttempts()
	var lastErr error
	for i, args := range attempts {
		attemptCtx, cancel := discoveryAttemptContext(ctx, len(attempts)-i)
		out, err := runner(attemptCtx, binary, args, nil)
		cancel()
		if err != nil {
			lastErr = fmt.Errorf(
				"running 'opencode %s': %s",
				strings.Join(args, " "),
				sanitizeDiagnostic(err.Error()),
			)
			continue
		}
		models, rates, perr := parseOpenCodeModels(out)
		if perr != nil {
			lastErr = perr
			continue
		}
		p.setRates(rates)
		if report != nil {
			for _, m := range models {
				report(m)
			}
		}
		return models, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("opencode model discovery produced no usable catalog")
	}
	return nil, lastErr
}

const defaultDiscoveryAttemptTimeout = 15 * time.Second

func discoveryAttemptContext(ctx context.Context, remainingAttempts int) (context.Context, context.CancelFunc) {
	if remainingAttempts <= 1 {
		return context.WithCancel(ctx)
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return context.WithCancel(ctx)
		}
		return context.WithTimeout(ctx, remaining/time.Duration(remainingAttempts))
	}
	return context.WithTimeout(ctx, defaultDiscoveryAttemptTimeout)
}

// parseOpenCodeModels parses `opencode models` output (verbose or line-only) into
// normalized catalog entries plus a pricing table keyed by lowercased model id
// (under both the suffixed and unsuffixed forms). It is tolerant of mixed and
// malformed output: blank lines are skipped, a header line with no following
// JSON object becomes a metadata-less entry, hidden/unavailable and duplicate
// entries are dropped, and ids that are not well-formed slash-form backend
// identifiers — bare tokens, flag-shaped lines, ids carrying unsafe characters,
// and credential-shaped strings — are rejected. It returns an error only when no
// usable model survives, so wholly malformed output fails over to the fallback.
func parseOpenCodeModels(out []byte) ([]llm.ModelInfo, map[string]modelRate, error) {
	lines := strings.Split(string(out), "\n")
	var models []llm.ModelInfo
	rates := make(map[string]modelRate)
	seen := make(map[string]bool)

	addEntry := func(backendID string, meta *openCodeVerboseModel) {
		id := sanitizeModelID(backendID)
		if id == "" {
			return
		}
		if meta != nil && !statusAvailable(meta.Status) {
			return
		}
		key := strings.ToLower(id)
		if seen[key] {
			return
		}
		seen[key] = true

		display := ""
		window := 0
		if meta != nil {
			display = sanitizeCatalogText(meta.Name)
			if meta.Limit != nil && meta.Limit.Context > 0 {
				window = meta.Limit.Context
			}
		}
		if display == "" {
			display = displayNameFromBackendID(id)
		}

		info := llm.ModelInfo{
			ID:                 id,
			DisplayName:        display,
			ContextWindow:      window,
			Category:           categoryForModel(id, display),
			EffortCapabilities: effortCapabilitiesForBackend(id),
		}
		// A valid window promotes the id to the suffixed form and keeps the bare
		// backend id reachable as an alias (backward compatibility); an unknown
		// window leaves the unsuffixed id untouched — no suffix is invented.
		if label := llm.ContextWindowLabel(window); label != "" {
			info.ID = llm.ModelWithContextWindow(id, window)
			info.DisplayName = display + " (" + label + ")"
			info.Aliases = llm.AppendUniqueAlias(info.Aliases, info.ID, id)
		}

		if meta != nil && meta.Cost != nil && meta.Cost.Input != nil && meta.Cost.Output != nil {
			rate := modelRate{inputPerMToken: *meta.Cost.Input, outputPerMToken: *meta.Cost.Output}
			rates[strings.ToLower(info.ID)] = rate
			rates[strings.ToLower(id)] = rate
		}

		models = append(models, info)
	}

	i := 0
	for i < len(lines) {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			i++
			continue
		}
		// An object with no preceding header: reconstruct the id from its
		// providerID/id fields. An unbalanced object means truncated/malformed
		// output, so stop — nothing past it can be parsed reliably.
		if strings.HasPrefix(trimmed, "{") {
			block, next, ok := collectJSONObject(lines, i)
			i = next
			if !ok {
				break
			}
			if meta, ok := decodeVerboseModel(block); ok {
				addEntry(backendIDFromMeta(meta), &meta)
			}
			continue
		}

		// Header line. Peek for an attached JSON object on the next non-blank line.
		header := trimmed
		j := i + 1
		for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
			j++
		}
		if j < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[j]), "{") {
			block, next, ok := collectJSONObject(lines, j)
			if !ok {
				// Header present but its object never closes: keep the header as a
				// metadata-less entry, then stop.
				addEntry(header, nil)
				break
			}
			if meta, ok := decodeVerboseModel(block); ok {
				addEntry(header, &meta)
			} else {
				addEntry(header, nil)
			}
			i = next
			continue
		}
		addEntry(header, nil)
		i++
	}

	if len(models) == 0 {
		return nil, nil, fmt.Errorf("opencode models output contained no usable models")
	}
	return models, rates, nil
}

// collectJSONObject joins lines starting at start until the JSON object opened on
// that line is balanced, ignoring braces inside string literals. It returns the
// object text, the index of the line after it, and ok=false when the object is
// never closed.
func collectJSONObject(lines []string, start int) (block string, next int, ok bool) {
	var b strings.Builder
	depth := 0
	inStr := false
	escaped := false
	started := false
	for k := start; k < len(lines); k++ {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(lines[k])
		line := lines[k]
		for i := 0; i < len(line); i++ {
			c := line[i]
			if inStr {
				switch {
				case escaped:
					escaped = false
				case c == '\\':
					escaped = true
				case c == '"':
					inStr = false
				}
				continue
			}
			switch c {
			case '"':
				inStr = true
			case '{':
				depth++
				started = true
			case '}':
				depth--
			}
		}
		if started && depth == 0 {
			return b.String(), k + 1, true
		}
	}
	return "", len(lines), false
}

func decodeVerboseModel(block string) (openCodeVerboseModel, bool) {
	var m openCodeVerboseModel
	if err := json.Unmarshal([]byte(block), &m); err != nil {
		return openCodeVerboseModel{}, false
	}
	return m, true
}

// backendIDFromMeta reconstructs the "providerID/id" backend id from a verbose
// model object; returns "" when either part is missing.
func backendIDFromMeta(m openCodeVerboseModel) string {
	prov := strings.TrimSpace(m.ProviderID)
	id := strings.TrimSpace(m.ID)
	if prov == "" || id == "" {
		return ""
	}
	return prov + "/" + id
}

// statusAvailable reports whether a verbose model status marks a usable model.
// Empty (line-only output, or a verbose object omitting the field) or "active"
// are usable; any other explicit status (a hidden, deprecated, preview, or
// otherwise unavailable marker) excludes the model from the catalog.
func statusAvailable(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "active":
		return true
	default:
		return false
	}
}

// sanitizeModelID validates and normalizes a backend id from discovery output.
// It strips terminal-control content, then accepts the id only when it is a
// well-formed OpenCode backend identifier: a slash-form "provider/model" whose
// every remaining rune is a safe model-id character, which does not look like a
// CLI flag, and which does not look like an API key or token literal. Anything
// else — a bare token with no provider prefix, a flag, a credential-shaped
// string, or a line carrying unsafe characters — returns "" so a malformed or
// hostile discovery line is dropped rather than carried into the catalog,
// reported through progress, written to the version-keyed cache, or handed to
// OpenCode. Real backend ids are clean slash-form values (both the verbose
// header lines and the line-only listing emit them), so this is a no-op for them.
func sanitizeModelID(raw string) string {
	id := strings.TrimSpace(stripTerminalControls(raw))
	if id == "" || strings.HasPrefix(id, "-") {
		return ""
	}
	for _, r := range id {
		if !isSafeModelRune(r) {
			return ""
		}
	}
	// OpenCode backend ids are native slash-form "provider/model" values; a line
	// without a non-empty provider and model part is malformed output, not a
	// model, so it must not become a catalog entry or be handed to OpenCode.
	provider, model, ok := strings.Cut(id, "/")
	if !ok || provider == "" || model == "" {
		return ""
	}
	// A credential-shaped id (a vendor-prefixed key/token literal anywhere in the
	// string, including as the model part of an otherwise slash-form id) is never
	// a real model. Rejecting it keeps malformed or hostile stdout from leaking
	// credential-like content into a reported or cached catalog entry.
	if tokenPrefixPattern.MatchString(id) {
		return ""
	}
	return id
}

// displayNameFromBackendID derives a fallback display name from a backend id when
// the CLI exposes none, using the model portion after the final slash.
func displayNameFromBackendID(id string) string {
	if idx := strings.LastIndex(id, "/"); idx >= 0 && idx < len(id)-1 {
		return id[idx+1:]
	}
	return id
}

// Deterministic category tokens. Matching is substring-based on the lowercased
// "id name" text and ordered: a shrink token (mini/small) demotes an otherwise
// capable family to balanced, and cheap tokens win outright. Unrecognized models
// return "" (unknown). The buckets only need to be stable and let selection run;
// Phase 6 owns provider-neutral default ranking.
var (
	cheapModelTokens     = []string{"nano", "haiku", "flash", "lite", "tiny", "embed"}
	balancedShrinkTokens = []string{"mini", "small"}
	capableModelTokens   = []string{"opus", "gpt-5", "-pro", "ultra", "-max", "405b", "reasoner", "deepseek-r"}
	balancedModelTokens  = []string{"sonnet", "gpt-4", "claude-3", "gemini", "llama", "mistral", "qwen", "gemma", "glm", "grok", "deepseek", "codex", "command"}
)

func categoryForModel(id, displayName string) string {
	s := strings.ToLower(id + " " + displayName)
	switch {
	case containsAnyToken(s, cheapModelTokens):
		return "cheap"
	case containsAnyToken(s, balancedShrinkTokens):
		return "balanced"
	case containsAnyToken(s, capableModelTokens):
		return "capable"
	case containsAnyToken(s, balancedModelTokens):
		return "balanced"
	default:
		return ""
	}
}

func containsAnyToken(s string, tokens []string) bool {
	for _, t := range tokens {
		if strings.Contains(s, t) {
			return true
		}
	}
	return false
}
