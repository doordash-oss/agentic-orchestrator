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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Fixed production feed configuration. The server's release feed is immune to
// legacy CLI endpoint overrides: GITHUB_API_URL never applies here.
const (
	ProductionFeedBaseURL = "https://api.github.com"
	ProductionFeedSlug    = "doordash-oss/agentic-orchestrator"

	feedMaxPages        = 10
	feedPerPage         = 100
	feedMaxMetadataSize = 1 << 20 // one MiB, enforced while streaming
	feedRequestTimeout  = 8 * time.Second
	feedMaxRedirects    = 5
)

// allowedFeedHosts is the closed destination allowlist for the production
// metadata feed, applied to the initial request and to every redirect.
var allowedFeedHosts = map[string]bool{
	"api.github.com":                       true,
	"github.com":                           true,
	"objects.githubusercontent.com":        true,
	"github-releases.githubusercontent.com": true,
	"release-assets.githubusercontent.com": true,
}

// FeedError is one failed metadata check. Retry marks errors raised by a
// server-imposed deadline (403/429 with applicable reset hints); NotBefore is
// the earliest instant a retry may be attempted and is a floor local backoff
// and jitter can never shorten.
type FeedError struct {
	Reason     string
	StatusCode int
	Retry      bool
	NotBefore  time.Time
}

func (e *FeedError) Error() string {
	if e.Retry && !e.NotBefore.IsZero() {
		return fmt.Sprintf("%s (retry after %s)", e.Reason, e.NotBefore.UTC().Format(time.RFC3339))
	}
	return e.Reason
}

// feedRelease is the strict subset of the GitHub release object the
// availability feed reads. Unknown fields are ignored; type mismatches fail
// the decode so malformed data can never produce a partial answer.
type feedRelease struct {
	TagName    *string     `json:"tag_name"`
	Draft      bool        `json:"draft"`
	Prerelease bool        `json:"prerelease"`
	HTMLURL    string      `json:"html_url"`
	Assets     []feedAsset `json:"assets"`
}

type feedAsset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// ReleaseSelection is the outcome of one successful metadata check.
type ReleaseSelection struct {
	// Version is the normalized clean MAJOR.MINOR.PATCH of the greatest
	// stable release.
	Version string
	// TagName is the raw tag string of the selected release.
	TagName string
	// ReleaseURL is the release's public HTML page, when published.
	ReleaseURL string
}

// FeedClient performs metadata-only release checks against a strict
// production-shaped feed: HTTPS only, a closed destination allowlist,
// per-destination authorization (GITHUB_TOKEN only ever sent to
// api.github.com), capped redirects, bounded page count, a streamed one-MiB
// response limit, and an eight-second per-request deadline. It never fetches
// checksums, signatures, envelopes, or packages, stages files, runs probes,
// writes receipts, or touches the executable.
type FeedClient struct {
	baseURL string
	slug    string
	token   func() string
	now     func() time.Time
	client  *http.Client
	// fixture marks a deliberately built test routing: the destination
	// policy is relaxed to the given (loopback) URL and no credential is
	// ever attached. Production clients never set it.
	fixture bool
}

// NewProductionFeedClient returns the production feed client: fixed base URL,
// module-derived slug with the known fallback, and GITHUB_TOKEN (sent only to
// api.github.com).
func NewProductionFeedClient(moduleSlug string, token func() string) *FeedClient {
	slug := strings.TrimSpace(moduleSlug)
	if slug == "" {
		slug = ProductionFeedSlug
	}
	if token == nil {
		token = func() string { return "" }
	}
	return newFeedClient(ProductionFeedBaseURL, slug, token)
}

// NewFixtureFeedClient returns a feed client bound to one explicit base URL
// with no credential. It exists so deliberately built test binaries (and
// in-process tests) can route metadata checks to a local fixture; the
// production client always uses the fixed production configuration, and no
// environment value of an ordinarily built binary can reach this routing.
func NewFixtureFeedClient(baseURL, slug string) *FeedClient {
	c := newFeedClient(baseURL, slug, nil)
	c.fixture = true
	return c
}

// newFeedClient constructs a client against an explicit base URL. Production
// wiring never calls this with anything but the fixed production
// configuration; in-process tests use it for deterministic fixture servers,
// and deliberately-built test binaries route through it with a local fixture
// that receives no credentials.
func newFeedClient(baseURL, slug string, token func() string) *FeedClient {
	if token == nil {
		token = func() string { return "" }
	}
	return &FeedClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		slug:    slug,
		token:   token,
		now:     time.Now,
		// Redirects are followed manually so every hop is validated against
		// the destination policy before the request is re-issued.
		client: &http.Client{
			Timeout: feedRequestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// LatestStable selects the numerically greatest clean three-component stable
// release across at most feedMaxPages pages of feedPerPage releases. Drafts
// and prereleases are excluded; ambiguous duplicate normalized versions or
// ambiguous asset identities on the selected release, malformed data, a feed
// with no selectable stable release, or pagination that indicates incomplete
// selection at the page bound all fail rather than producing a partial
// answer.
func (c *FeedClient) LatestStable(ctx context.Context) (ReleaseSelection, error) {
	nextURL := fmt.Sprintf("%s/repos/%s/releases?per_page=%d", c.baseURL, c.slug, feedPerPage)
	seen := make(map[string]string)
	var bestTag string
	var bestParts [3]int
	var bestURL string
	var bestAssets []feedAsset
	haveBest := false

	for page := 1; page <= feedMaxPages; page++ {
		body, next, err := c.get(ctx, nextURL)
		if err != nil {
			return ReleaseSelection{}, err
		}
		var releases []feedRelease
		if err := json.Unmarshal(body, &releases); err != nil {
			return ReleaseSelection{}, &FeedError{Reason: fmt.Sprintf("malformed release metadata: %v", err)}
		}
		for i := range releases {
			rel := &releases[i]
			if rel.Draft || rel.Prerelease {
				continue
			}
			if rel.TagName == nil {
				return ReleaseSelection{}, &FeedError{Reason: "malformed release metadata: release without tag_name"}
			}
			tag := strings.TrimSpace(*rel.TagName)
			parts, ok := ParseReleaseVersion(tag)
			if !ok {
				// Not a clean three-component stable version: not
				// selectable, but not malformed — drafts, prereleases, and
				// unusual tags are simply excluded.
				continue
			}
			normalized := fmt.Sprintf("%d.%d.%d", parts[0], parts[1], parts[2])
			if _, dup := seen[normalized]; dup {
				return ReleaseSelection{}, &FeedError{
					Reason: fmt.Sprintf("ambiguous duplicate stable release for version %s", normalized),
				}
			}
			seen[normalized] = tag
			if !haveBest || compareParts(parts, bestParts) > 0 {
				haveBest = true
				bestTag, bestParts, bestURL, bestAssets = tag, parts, rel.HTMLURL, rel.Assets
			}
		}
		if next == "" {
			break
		}
		if page == feedMaxPages {
			return ReleaseSelection{}, &FeedError{
				Reason: fmt.Sprintf("incomplete selection: more than %d pages of releases", feedMaxPages),
			}
		}
		nextURL = next
	}

	if !haveBest {
		return ReleaseSelection{}, &FeedError{Reason: "no selectable stable release"}
	}
	if err := checkAssetIdentities(bestTag, bestAssets); err != nil {
		return ReleaseSelection{}, err
	}
	version := fmt.Sprintf("%d.%d.%d", bestParts[0], bestParts[1], bestParts[2])
	selection := ReleaseSelection{Version: version, TagName: bestTag}
	if u := strings.TrimSpace(bestURL); u != "" {
		selection.ReleaseURL = u
	}
	return selection, nil
}

// checkAssetIdentities rejects a selected release whose assets carry
// duplicate identities (equal IDs or equal normalized names): the asset set
// would be ambiguous for any later platform-matched selection.
func checkAssetIdentities(tag string, assets []feedAsset) error {
	ids := make(map[int64]bool, len(assets))
	names := make(map[string]bool, len(assets))
	for _, asset := range assets {
		if ids[asset.ID] {
			return &FeedError{Reason: fmt.Sprintf("ambiguous duplicate asset identity on release %s", tag)}
		}
		ids[asset.ID] = true
		name := strings.ToLower(strings.TrimSpace(asset.Name))
		if name != "" {
			if names[name] {
				return &FeedError{Reason: fmt.Sprintf("ambiguous duplicate asset name on release %s", tag)}
			}
			names[name] = true
		}
	}
	return nil
}

func compareParts(a, b [3]int) int {
	for i := range 3 {
		switch {
		case a[i] < b[i]:
			return -1
		case a[i] > b[i]:
			return 1
		}
	}
	return 0
}

// get performs one validated feed request and returns the bounded body plus
// the rel="next" pagination destination (already validated). Redirects are
// followed manually, each hop re-validated and re-authorized per destination.
func (c *FeedClient) get(ctx context.Context, rawURL string) (body []byte, next string, err error) {
	dest := rawURL
	for hop := 0; ; hop++ {
		if hop > feedMaxRedirects {
			return nil, "", &FeedError{Reason: "too many feed redirects"}
		}
		if !c.fixture {
			if err := validateFeedURL(dest); err != nil {
				return nil, "", err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, dest, nil)
		if err != nil {
			return nil, "", &FeedError{Reason: fmt.Sprintf("building feed request: %v", err)}
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		// Authorization is constructed anew per destination: a token follows
		// only the API host, never a redirect target. Fixture routing never
		// attaches a credential.
		if !c.fixture && req.URL.Hostname() == "api.github.com" && c.token() != "" {
			req.Header.Set("Authorization", "Bearer "+c.token())
		}
		resp, err := c.client.Do(req)
		if err != nil {
			return nil, "", &FeedError{Reason: fmt.Sprintf("requesting release metadata: %v", err)}
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			location := resp.Header.Get("Location")
			_ = resp.Body.Close()
			if location == "" {
				return nil, "", &FeedError{Reason: fmt.Sprintf("feed redirect without location (status %d)", resp.StatusCode)}
			}
			resolved, err := resolveFeedURL(dest, location)
			if err != nil {
				return nil, "", err
			}
			dest = resolved
			continue
		}
		defer resp.Body.Close()
		return c.readResponse(resp)
	}
}

// readResponse consumes one non-redirect feed response: the one-MiB streamed
// limit, rate-limit decoding for 403/429, and the rel="next" pagination link.
func (c *FeedClient) readResponse(resp *http.Response) (body []byte, next string, err error) {
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return nil, "", c.retryError(resp)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", &FeedError{
			Reason:     fmt.Sprintf("feed returned status %s", resp.Status),
			StatusCode: resp.StatusCode,
		}
	}
	// Stream with the limit enforced regardless of Content-Length: an
	// oversized body is truncated by the limit reader and rejected by the
	// size check below.
	limited := io.LimitReader(resp.Body, feedMaxMetadataSize+1)
	body, err = io.ReadAll(limited)
	if err != nil {
		return nil, "", &FeedError{Reason: fmt.Sprintf("reading release metadata: %v", err)}
	}
	if len(body) > feedMaxMetadataSize {
		return nil, "", &FeedError{
			Reason:     "release metadata exceeds the one-MiB limit",
			StatusCode: resp.StatusCode,
		}
	}
	return body, parseNextLink(resp.Header.Get("Link")), nil
}

// retryError converts a 403/429 response into a retryable FeedError carrying
// the applicable server-imposed deadline: the earliest of the primary
// X-RateLimit-Reset and Retry-After hints (seconds or HTTP-date), floored at
// now.
func (c *FeedClient) retryError(resp *http.Response) error {
	now := c.now()
	floor := now
	reason := fmt.Sprintf("feed rate limited (status %s)", resp.Status)
	if reset := resp.Header.Get("X-RateLimit-Remaining"); reset == "0" {
		if raw := resp.Header.Get("X-RateLimit-Reset"); raw != "" {
			if secs, err := strconv.ParseInt(raw, 10, 64); err == nil {
				if at := time.Unix(secs, 0); at.After(floor) {
					floor = at
				}
				reason = fmt.Sprintf("feed primary rate limit resets at %s", floor.UTC().Format(time.RFC3339))
			}
		}
	}
	if retryAfter := strings.TrimSpace(resp.Header.Get("Retry-After")); retryAfter != "" {
		if at, ok := parseRetryAfter(retryAfter, now); ok && at.After(floor) {
			floor = at
			reason = fmt.Sprintf("feed requested retry after %s", floor.UTC().Format(time.RFC3339))
		}
	}
	return &FeedError{
		Reason:     reason,
		StatusCode: resp.StatusCode,
		Retry:      true,
		NotBefore:  floor,
	}
}

// parseRetryAfter parses a Retry-After value as delay-seconds or HTTP-date.
func parseRetryAfter(v string, now time.Time) (time.Time, bool) {
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return now.Add(time.Duration(secs) * time.Second), true
	}
	if at, err := http.ParseTime(v); err == nil {
		return at, true
	}
	return time.Time{}, false
}

// validateFeedURL enforces the destination policy on every request and
// redirect: HTTPS only, an allowlisted host, no userinfo, and no non-default
// port.
func validateFeedURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return &FeedError{Reason: fmt.Sprintf("invalid feed destination: %v", err)}
	}
	if u.Scheme != "https" {
		return &FeedError{Reason: fmt.Sprintf("feed destination %q must use https", u.Host)}
	}
	if !allowedFeedHosts[u.Hostname()] {
		return &FeedError{Reason: fmt.Sprintf("feed destination %q is not allowed", u.Hostname())}
	}
	if u.User != nil {
		return &FeedError{Reason: "feed destination must not carry userinfo"}
	}
	if p := u.Port(); p != "" && p != "443" {
		return &FeedError{Reason: fmt.Sprintf("feed destination %q must use the default https port", u.Host)}
	}
	return nil
}

// resolveFeedURL resolves a redirect Location against the current
// destination and validates the result with the same policy.
func resolveFeedURL(current, location string) (string, error) {
	base, err := url.Parse(current)
	if err != nil {
		return "", &FeedError{Reason: fmt.Sprintf("invalid feed destination: %v", err)}
	}
	ref, err := url.Parse(location)
	if err != nil {
		return "", &FeedError{Reason: fmt.Sprintf("invalid feed redirect: %v", err)}
	}
	resolved := base.ResolveReference(ref)
	return resolved.String(), nil
}

// parseNextLink extracts the rel="next" pagination URL from a Link header.
func parseNextLink(header string) string {
	if strings.TrimSpace(header) == "" {
		return ""
	}
	for _, part := range strings.Split(header, ",") {
		segments := strings.Split(part, ";")
		if len(segments) < 2 {
			continue
		}
		target := strings.TrimSpace(segments[0])
		if !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
			continue
		}
		isNext := false
		for _, param := range segments[1:] {
			key, value, ok := strings.Cut(strings.TrimSpace(param), "=")
			if !ok || strings.ToLower(key) != "rel" {
				continue
			}
			if strings.Trim(strings.TrimSpace(value), `"`) == "next" {
				isNext = true
			}
		}
		if isNext {
			return strings.Trim(target, "<>")
		}
	}
	return ""
}

// IsFeedRetryError reports whether err is a server-imposed retry deadline
// and, when it is, the earliest instant a retry may be attempted.
func IsFeedRetryError(err error) (notBefore time.Time, ok bool) {
	var feedErr *FeedError
	if errors.As(err, &feedErr) && feedErr.Retry {
		return feedErr.NotBefore, true
	}
	return time.Time{}, false
}
