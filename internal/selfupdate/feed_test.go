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

package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// feedFixture is a programmable GitHub-shaped releases API for feed tests.
type feedFixture struct {
	pages     [][]string // per page: release JSON payloads
	status    int
	headers   map[string]string
	redirect  string
	sawAuth   []string
	sawHosts  []string
	oversize  bool
	delay     time.Duration
	pageLimit int
}

func (f *feedFixture) handler(w http.ResponseWriter, r *http.Request) {
	f.sawHosts = append(f.sawHosts, r.Host)
	if auth := r.Header.Get("Authorization"); auth != "" {
		f.sawAuth = append(f.sawAuth, auth)
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.redirect != "" {
		w.Header().Set("Location", f.redirect)
		w.WriteHeader(http.StatusFound)
		return
	}
	if f.oversize {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[` + strings.Repeat(`{"tag_name":"v1.0.0","assets":[]},`, 300000) + `]`))
		return
	}
	if f.status != 0 && f.status != http.StatusOK {
		for k, v := range f.headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(f.status)
		return
	}
	page := 0
	if raw := r.URL.Query().Get("page"); raw != "" {
		page, _ = strconv.Atoi(raw)
	}
	if page == 0 {
		page = 1
	}
	if page > len(f.pages) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[]`))
		return
	}
	body := f.pages[page-1]
	next := ""
	if f.pageLimit == 0 || page < f.pageLimit {
		if page < len(f.pages) {
			next = fmt.Sprintf(`<http://%s/repos/%s/releases?per_page=%d&page=%d>; rel="next"`, r.Host, ProductionFeedSlug, feedPerPage, page+1)
		}
	}
	if next != "" {
		w.Header().Set("Link", next)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("[" + strings.Join(body, ",") + "]"))
}

func releaseJSON(tag string, draft, prerelease bool, assets ...string) string {
	r := map[string]any{"tag_name": tag, "draft": draft, "prerelease": prerelease, "html_url": "https://github.com/" + ProductionFeedSlug + "/releases/tag/" + tag}
	if len(assets) > 0 {
		entries := make([]string, 0, len(assets))
		for i, name := range assets {
			entries = append(entries, fmt.Sprintf(`{"id":%d,"name":%q}`, 100+i, name))
		}
		r["assets"] = json.RawMessage("[" + strings.Join(entries, ",") + "]")
	}
	raw, _ := json.Marshal(r)
	return string(raw)
}

func fixtureClient(t *testing.T, fixture *feedFixture) (*FeedClient, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(fixture.handler))
	t.Cleanup(server.Close)
	client, err := NewFixtureFeedClient(server.URL, ProductionFeedSlug)
	if err != nil {
		t.Fatalf("fixture client: %v", err)
	}
	return client, server
}

func feedTokenFunc(heard *[]string) func() string {
	return func() string {
		*heard = append(*heard, "called")
		return "sekrit-token"
	}
}

func TestFeedLatestStableSelectsGreatestAcrossUnsortedPages(t *testing.T) {
	t.Parallel()
	fixture := &feedFixture{pages: [][]string{
		{releaseJSON("v1.9.0", false, false), releaseJSON("v2.0.0-rc1", false, true), releaseJSON("v1.2.3", false, false)},
		{releaseJSON("v1.10.0", false, false), releaseJSON("v0.9.9-draft", true, false), releaseJSON("2.1.0", false, false)},
	}}
	client, _ := fixtureClient(t, fixture)
	sel, err := client.LatestStable(context.Background())
	if err != nil {
		t.Fatalf("latest stable: %v", err)
	}
	if sel.Version != "2.1.0" {
		t.Fatalf("version = %q, want 2.1.0 (numeric, not lexical: 2.1.0 > 1.10.0 > 1.9.0)", sel.Version)
	}
	if sel.TagName != "2.1.0" {
		t.Fatalf("tag = %q", sel.TagName)
	}
	if sel.ReleaseURL == "" {
		t.Fatal("release URL missing")
	}
}

func TestFeedLatestStableNumericOrderingAndOverflow(t *testing.T) {
	t.Parallel()
	// Huge but representable components order numerically; an unrepresentable
	// component is not selectable rather than overflowing.
	fixture := &feedFixture{pages: [][]string{{
		releaseJSON("v999999999999.0.0", false, false),
		releaseJSON("v9223372036854775808.0.0", false, false), // int64 overflow: excluded
	}}}
	client, _ := fixtureClient(t, fixture)
	sel, err := client.LatestStable(context.Background())
	if err != nil {
		t.Fatalf("latest stable: %v", err)
	}
	if sel.Version != "999999999999.0.0" {
		t.Fatalf("version = %q", sel.Version)
	}
}

func TestFeedLatestStableRejectsDuplicateNormalizedVersions(t *testing.T) {
	t.Parallel()
	fixture := &feedFixture{pages: [][]string{{
		releaseJSON("v1.2.3", false, false),
		releaseJSON("1.2.3", false, false), // normalizes equal: ambiguous
	}}}
	client, _ := fixtureClient(t, fixture)
	if _, err := client.LatestStable(context.Background()); err == nil {
		t.Fatal("duplicate normalized version should fail, not guess")
	}
}

func TestFeedLatestStableRejectsAmbiguousAssetIdentities(t *testing.T) {
	t.Parallel()
	fixture := &feedFixture{pages: [][]string{{
		releaseJSON("v2.0.0", false, false, "agentico-darwin-arm64.tar.gz", "agentico-darwin-arm64.tar.gz"),
	}}}
	client, _ := fixtureClient(t, fixture)
	if _, err := client.LatestStable(context.Background()); err == nil {
		t.Fatal("duplicate asset name on the selected release should fail")
	}
}

func TestFeedLatestStablePageExhaustionFails(t *testing.T) {
	t.Parallel()
	// Eleven pages of releases: the client caps at ten and must reject the
	// pagination rather than answering from a partial selection.
	pages := make([][]string, feedMaxPages+1)
	for i := range pages {
		pages[i] = []string{releaseJSON(fmt.Sprintf("v1.0.%d", i), false, false)}
	}
	fixture := &feedFixture{pages: pages}
	client, _ := fixtureClient(t, fixture)
	_, err := client.LatestStable(context.Background())
	if err == nil {
		t.Fatal("pagination beyond the page bound must fail instead of answering partially")
	}
	if !strings.Contains(err.Error(), "incomplete selection") {
		t.Fatalf("error = %v, want incomplete selection", err)
	}
}

func TestFeedLatestStableNoSelectableReleaseFails(t *testing.T) {
	t.Parallel()
	fixture := &feedFixture{pages: [][]string{{
		releaseJSON("v1.2.3-rc1", false, true),
		releaseJSON("v1.2.3-draft", true, false),
		releaseJSON("v1.2.3-5-gabc1234", false, false),
	}}}
	client, _ := fixtureClient(t, fixture)
	_, err := client.LatestStable(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no selectable stable release") {
		t.Fatalf("error = %v, want no selectable stable release", err)
	}
}

func TestFeedMalformedBodyFails(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"tag_name":5}]`)) // tag_name must be a string
	}))
	t.Cleanup(server.Close)
	client, err := NewFixtureFeedClient(server.URL, ProductionFeedSlug)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.LatestStable(context.Background()); err == nil {
		t.Fatal("malformed metadata must fail, not produce a partial answer")
	}
}

func TestFeedOversizeBodyRejectedRegardlessOfContentLength(t *testing.T) {
	t.Parallel()
	fixture := &feedFixture{oversize: true}
	client, _ := fixtureClient(t, fixture)
	_, err := client.LatestStable(context.Background())
	if err == nil || !strings.Contains(err.Error(), "one-MiB") {
		t.Fatalf("error = %v, want one-MiB limit", err)
	}
}

func TestFeedRateLimitCarriesRetryFloor(t *testing.T) {
	t.Parallel()
	reset := time.Now().Add(3 * time.Hour).Unix()
	fixture := &feedFixture{
		status: http.StatusTooManyRequests,
		headers: map[string]string{
			"X-RateLimit-Remaining": "0",
			"X-RateLimit-Reset":     strconv.FormatInt(reset, 10),
			"Retry-After":           "60",
		},
	}
	client, _ := fixtureClient(t, fixture)
	_, err := client.LatestStable(context.Background())
	if err == nil {
		t.Fatal("rate limit should fail")
	}
	notBefore, ok := IsFeedRetryError(err)
	if !ok {
		t.Fatalf("error should be retryable: %v", err)
	}
	// The primary reset (later) is the floor, not the shorter Retry-After.
	if remaining := time.Until(notBefore); remaining < 2*time.Hour {
		t.Fatalf("retry floor %v is sooner than the primary reset", notBefore)
	}
}

func TestFeedRetryAfterHTTPDate(t *testing.T) {
	t.Parallel()
	at := time.Now().Add(90 * time.Minute).UTC().Format(http.TimeFormat)
	fixture := &feedFixture{
		status:  http.StatusForbidden,
		headers: map[string]string{"Retry-After": at},
	}
	client, _ := fixtureClient(t, fixture)
	_, err := client.LatestStable(context.Background())
	notBefore, ok := IsFeedRetryError(err)
	if !ok {
		t.Fatalf("403 with Retry-After should be retryable: %v", err)
	}
	if remaining := time.Until(notBefore); remaining < time.Hour {
		t.Fatalf("HTTP-date retry floor too soon: %v", notBefore)
	}
}

func TestFeedRequestDeadlineBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the real 8s metadata deadline")
	}
	t.Parallel()
	fixture := &feedFixture{delay: 10 * time.Second}
	client, _ := fixtureClient(t, fixture)
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := client.LatestStable(ctx)
	if err == nil {
		t.Fatal("delayed feed should fail the deadline")
	}
	// The per-request client timeout (8s) must bound the wait even though the
	// caller allowed 20s.
	if elapsed := time.Since(start); elapsed >= 9*time.Second {
		t.Fatalf("deadline not bounded by the 8s metadata timeout: %v", elapsed)
	}
}

func TestFeedSendsTokenOnlyToAPIHost(t *testing.T) {
	t.Parallel()
	capture := &capturingTransport{body: `[]`}
	for _, tt := range []struct {
		baseURL string
		wantKey bool
	}{
		{baseURL: "https://api.github.com", wantKey: true},
		{baseURL: "https://github.com", wantKey: false},
		{baseURL: "https://objects.githubusercontent.com", wantKey: false},
	} {
		client := newFeedClient(tt.baseURL, ProductionFeedSlug, func() string { return "sekrit-token" })
		client.client.Transport = capture
		if _, err := client.LatestStable(context.Background()); err != nil && !strings.Contains(err.Error(), "no selectable stable release") {
			t.Fatalf("%s: latest stable: %v", tt.baseURL, err)
		}
		auth := capture.last.Header.Get("Authorization")
		if tt.wantKey && auth != "Bearer sekrit-token" {
			t.Fatalf("%s: authorization = %q, want the bearer token", tt.baseURL, auth)
		}
		if !tt.wantKey && auth != "" {
			t.Fatalf("%s: credential sent to non-API host", tt.baseURL)
		}
	}
}

// capturingTransport records requests and answers with a fixed body without
// touching the network.
type capturingTransport struct {
	last *http.Request
	body string
}

func (c *capturingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	c.last = cloned
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(c.body)),
		Request:    req,
	}, nil
}

func TestFeedDestinationPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{name: "production API host allowed", raw: "https://api.github.com/repos/o/r/releases?per_page=100"},
		{name: "download host allowed", raw: "https://objects.githubusercontent.com/x"},
		{name: "release assets host allowed", raw: "https://release-assets.githubusercontent.com/x"},
		{name: "scheme downgrade rejected", raw: "http://api.github.com/repos/x/releases", wantErr: "https"},
		{name: "unknown host rejected", raw: "https://evil.example.com/repos", wantErr: "not allowed"},
		{name: "userinfo rejected", raw: "https://user:pass@api.github.com/repos", wantErr: "userinfo"},
		{name: "non-default port rejected", raw: "https://api.github.com:8443/repos", wantErr: "default https port"},
		{name: "explicit default port allowed", raw: "https://api.github.com:443/repos"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFeedURL(tt.raw)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
	// A redirect Location resolves against the current destination and the
	// resolved destination is then validated with the same policy.
	resolved, err := resolveFeedURL("https://api.github.com/repos/o/r/releases", "/repos/o/r/releases?page=2")
	if err != nil || resolved != "https://api.github.com/repos/o/r/releases?page=2" {
		t.Fatalf("resolveFeedURL = %q, %v", resolved, err)
	}
	if err := validateFeedURL(resolved); err != nil {
		t.Fatalf("same-origin redirect should pass the policy: %v", err)
	}
	cross, err := resolveFeedURL("https://api.github.com/x", "https://evil.example.com/y")
	if err != nil {
		t.Fatalf("resolveFeedURL cross-host: %v", err)
	}
	if err := validateFeedURL(cross); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("error = %v, want not allowed", err)
	}
}

func TestFeedRedirectCap(t *testing.T) {
	t.Parallel()
	var hops int
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hops++
		w.Header().Set("Location", server.URL+"/hop")
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(server.Close)
	client, err := NewFixtureFeedClient(server.URL, ProductionFeedSlug)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.LatestStable(context.Background())
	if err == nil || !strings.Contains(err.Error(), "too many feed redirects") {
		t.Fatalf("error = %v, want redirect cap", err)
	}
}

func TestProductionFeedClientIgnoresEndpointOverrides(t *testing.T) {
	t.Setenv("GITHUB_API_URL", "https://evil.example.com")
	client := NewProductionFeedClient("", nil)
	if client.baseURL != ProductionFeedBaseURL {
		t.Fatalf("base URL = %q, want the fixed production feed", client.baseURL)
	}
	if client.slug != ProductionFeedSlug {
		t.Fatalf("slug = %q, want fallback slug", client.slug)
	}
	client = NewProductionFeedClient("owner/repo", nil)
	if client.slug != "owner/repo" {
		t.Fatalf("slug = %q, want module-derived slug", client.slug)
	}
}

func TestParseNextLink(t *testing.T) {
	t.Parallel()
	header := `<https://api.github.com/repos/o/r/releases?per_page=100&page=2>; rel="next", <https://api.github.com/repos/o/r/releases?per_page=100&page=5>; rel="last"`
	if got := parseNextLink(header); got != "https://api.github.com/repos/o/r/releases?per_page=100&page=2" {
		t.Fatalf("next = %q", got)
	}
	if got := parseNextLink(""); got != "" {
		t.Fatalf("empty header next = %q", got)
	}
	if got := parseNextLink(`<https://x>; rel="last"`); got != "" {
		t.Fatalf("last-only header next = %q", got)
	}
}
