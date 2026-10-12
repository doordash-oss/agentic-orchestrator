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
	"context"
	"maps"
	"net/http"
	"sync"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// endpointProbeTimeout bounds one whole endpoint-check pass; every endpoint
// is probed in parallel inside it.
const endpointProbeTimeout = 3 * time.Second

// endpointCheckTTL is how long a completed pass is reused before a plain
// readiness read schedules a fresh background pass.
const endpointCheckTTL = time.Minute

// endpointResult is one provider's endpoint-check outcome. host names the
// first unreachable endpoint; detail is the bounded transport error.
type endpointResult struct {
	reachable bool
	host      string
	detail    string
}

// endpointChecks caches provider endpoint reachability. Checks never gate
// readiness or feature start: plain reads schedule them in the background
// and use whatever result is cached; only explicit refreshes wait, bounded
// by endpointProbeTimeout.
type endpointChecks struct {
	mu        sync.Mutex
	results   map[string]endpointResult
	checkedAt time.Time
	gen       uint64
	inflight  bool
	// timeout and client are test seams; zero values use the defaults.
	timeout time.Duration
	client  *http.Client
}

// endpointProbeClient never follows redirects (the first response already
// proves reachability) and keeps no idle connections.
var endpointProbeClient = &http.Client{
	Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DisableKeepAlives: true},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// checkProviderEndpoints schedules an endpoint-check pass when forced, or
// when no pass is running and the cached one is older than the TTL. A
// forced pass is awaited (bounded by the probe timeout and ctx); otherwise
// the call returns immediately. A pass that changes the cached results
// publishes a runtime invalidation so clients re-read readiness.
func (h *apiHandler) checkProviderEndpoints(ctx context.Context, force bool) {
	if h.registry == nil {
		return
	}
	e := &h.endpoints
	e.mu.Lock()
	if !force && (e.inflight || time.Since(e.checkedAt) < endpointCheckTTL) {
		e.mu.Unlock()
		return
	}
	targets := map[string][]string{}
	for _, p := range h.registry.All() {
		if reporter, ok := p.(llm.EndpointReporter); ok {
			if endpoints := reporter.ConfiguredEndpoints(); len(endpoints) > 0 {
				targets[p.Name()] = endpoints
			}
		}
	}
	if len(targets) == 0 {
		e.gen++
		e.results, e.checkedAt, e.inflight = nil, time.Now(), false
		e.mu.Unlock()
		return
	}
	// The pass is launched work: it owns an admission reservation for its
	// whole lifetime, and a closed boundary skips it.
	release, err := h.probeAdmissionBegin()
	if err != nil {
		e.mu.Unlock()
		return
	}
	e.gen++
	gen := e.gen
	e.inflight = true
	timeout, client := e.timeout, e.client
	e.mu.Unlock()
	if timeout <= 0 {
		timeout = endpointProbeTimeout
	}
	if client == nil {
		client = endpointProbeClient
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer release()
		probeCtx, cancel := context.WithTimeout(context.Background(), timeout)
		results := probeEndpoints(probeCtx, client, targets)
		cancel()
		e.mu.Lock()
		if gen != e.gen {
			e.mu.Unlock()
			return
		}
		changed := !maps.Equal(results, e.results)
		e.results, e.checkedAt, e.inflight = results, time.Now(), false
		e.mu.Unlock()
		if changed && h.broker != nil {
			h.broker.publish(snapshotRequiredEventDTO(sseEventLifecycleUpdated, Resource{Type: resourceTypeRuntime}))
		}
	}()
	if force {
		select {
		case <-done:
		case <-ctx.Done():
		}
	}
}

// probeEndpoints probes every endpoint in parallel and folds the outcomes
// per provider: a provider is reachable only when all its endpoints are.
func probeEndpoints(ctx context.Context, client *http.Client, targets map[string][]string) map[string]endpointResult {
	type outcome struct {
		provider, endpoint string
		err                error
	}
	outcomes := make(chan outcome)
	count := 0
	for provider, endpoints := range targets {
		for _, endpoint := range endpoints {
			count++
			go func() {
				outcomes <- outcome{provider, endpoint, llm.ProbeEndpoint(ctx, client, endpoint)}
			}()
		}
	}
	results := make(map[string]endpointResult, len(targets))
	for range count {
		o := <-outcomes
		current, seen := results[o.provider]
		if o.err == nil {
			if !seen {
				results[o.provider] = endpointResult{reachable: true}
			}
			continue
		}
		if !seen || current.reachable {
			results[o.provider] = endpointResult{
				host:   llm.EndpointHost(o.endpoint),
				detail: SafeDisplayText(o.err.Error(), maxReadinessTextLen),
			}
		}
	}
	return results
}

// applyEndpointResults decorates installed providers with the cached
// endpoint outcome. An unreachable endpoint adds a warning-class issue and
// never changes ready.
func (h *apiHandler) applyEndpointResults(providers []ProviderReadiness) []ProviderReadiness {
	h.endpoints.mu.Lock()
	results := h.endpoints.results
	h.endpoints.mu.Unlock()
	for i := range providers {
		result, ok := results[providers[i].Name]
		if !ok || !providers[i].Installed {
			continue
		}
		reachable := result.reachable
		providers[i].EndpointReachable = &reachable
		if !reachable {
			providers[i].Warning = readinessIssue(errcat.ProviderEndpointUnreachable,
				errcat.WithParams(errcat.ProviderEndpointParams{Provider: providers[i].Name, Host: result.host}),
				errcat.WithDiagnostics(result.detail))
		}
	}
	return providers
}
