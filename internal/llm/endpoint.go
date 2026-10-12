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

package llm

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// EndpointReporter is implemented by providers that can name the custom API
// endpoints their CLI is explicitly configured to call (a gateway base URL,
// an ANTHROPIC_BASE_URL, a custom model provider). Readiness probes those
// endpoints for reachability and surfaces a non-blocking warning when one
// does not answer. Default vendor endpoints are not reported: the check
// targets the private-gateway case (e.g. a VPN-only host) where a ready CLI
// still cannot serve a single prompt.
type EndpointReporter interface {
	ConfiguredEndpoints() []string
}

// EndpointURL normalizes raw into an absolute http(s) URL, reporting false
// for anything else (empty, relative, templated, or non-HTTP values).
func EndpointURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "{}$") {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", false
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), true
}

// EndpointHost returns the host[:port] of an endpoint URL for display.
func EndpointHost(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		return u.Host
	}
	return endpoint
}

// ProbeEndpoint reports whether endpoint answers an unauthenticated HEAD
// request before ctx ends. Any HTTP response, including 401/404/5xx, proves
// the network path works and counts as reachable; only transport failures
// (DNS, connection refused, TLS, timeout) are returned as errors. The client
// should not follow redirects.
func ProbeEndpoint(ctx context.Context, client *http.Client, endpoint string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
