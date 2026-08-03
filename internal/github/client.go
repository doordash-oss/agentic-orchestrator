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

// Package github provides typed GitHub REST and GraphQL access via the
// go-gh library. Credentials resolve through go-gh's auth chain
// (GH_TOKEN/GITHUB_TOKEN, hosts.yml, then the gh binary's keyring); no
// gh CLI stdout is ever parsed.
package github

import (
	"fmt"
	"net/http"
	"net/url"
	"sync"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/auth"
)

// Client bundles the REST and GraphQL clients for one GitHub host.
type Client struct {
	rest *api.RESTClient
	gql  *api.GraphQLClient
}

// NoCredentialsError reports that no token could be resolved for a host.
type NoCredentialsError struct{ Host string }

func (e *NoCredentialsError) Error() string {
	return fmt.Sprintf("no GitHub credentials for %s: set GH_TOKEN or run 'gh auth login'", e.Host)
}

type testOverride struct {
	target *url.URL
	token  string
}

var (
	mu       sync.Mutex
	cache    = map[string]*Client{}
	override *testOverride
)

// OverrideForTest routes every subsequent client at the given base URL
// with the given token, bypassing credential resolution and the cache.
// The returned func undoes the override.
func OverrideForTest(baseURL, token string) (restore func()) {
	target, err := url.Parse(baseURL)
	if err != nil {
		panic(fmt.Sprintf("OverrideForTest: bad base URL %q: %v", baseURL, err))
	}
	mu.Lock()
	prev := override
	override = &testOverride{target: target, token: token}
	cache = map[string]*Client{}
	mu.Unlock()
	return func() {
		mu.Lock()
		override = prev
		cache = map[string]*Client{}
		mu.Unlock()
	}
}

// rewriteTransport redirects every request to a fixed scheme+host,
// preserving path and query. Used only by test overrides.
type rewriteTransport struct{ target *url.URL }

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = t.target.Scheme
	req.URL.Host = t.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

// ForHost returns a client for the given GitHub host ("" means
// github.com). Clients are cached per host.
func ForHost(host string) (*Client, error) {
	if host == "" {
		host = "github.com"
	}
	mu.Lock()
	ov := override
	mu.Unlock()
	if ov != nil {
		return newClient(host, ov.token, rewriteTransport{target: ov.target})
	}
	mu.Lock()
	cached, ok := cache[host]
	mu.Unlock()
	if ok {
		return cached, nil
	}
	token, _ := auth.TokenForHost(host)
	if token == "" {
		return nil, &NoCredentialsError{Host: host}
	}
	client, err := newClient(host, token, nil)
	if err != nil {
		return nil, err
	}
	mu.Lock()
	cache[host] = client
	mu.Unlock()
	return client, nil
}

func newClient(host, token string, transport http.RoundTripper) (*Client, error) {
	opts := api.ClientOptions{Host: host, AuthToken: token, Transport: transport}
	rest, err := api.NewRESTClient(opts)
	if err != nil {
		return nil, fmt.Errorf("building GitHub REST client for %s: %w", host, err)
	}
	gql, err := api.NewGraphQLClient(opts)
	if err != nil {
		return nil, fmt.Errorf("building GitHub GraphQL client for %s: %w", host, err)
	}
	return &Client{rest: rest, gql: gql}, nil
}
