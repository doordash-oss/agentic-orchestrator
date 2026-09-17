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
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixtureReleasePrivateKeyPEM is the committed test-only Ed25519 fixture key
// (desktop/test/e2e/helpers/update-fixtures.ts); its public half is the
// fixture trust root. Signing fixture manifests with it never needs the
// production release private key.
const fixtureReleasePrivateKeyPEM = `-----BEGIN PRIVATE KEY-----
MC4CAQAwBQYDK2VwBCIEINZMXBFPD1S98rCr5jnAqC4oCAf7E+GQBz6NrbxOncAr
-----END PRIVATE KEY-----`

func fixtureReleasePrivateKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	block, _ := pem.Decode([]byte(fixtureReleasePrivateKeyPEM))
	if block == nil {
		t.Fatal("fixture private key PEM did not decode")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse fixture private key: %v", err)
	}
	edKey, ok := key.(ed25519.PrivateKey)
	if !ok {
		t.Fatalf("fixture private key is %T, want ed25519.PrivateKey", key)
	}
	return edKey
}

func signFixturePayload(t *testing.T, payload []byte) string {
	t.Helper()
	sig := ed25519.Sign(fixtureReleasePrivateKey(t), payload)
	return SignaturePrefix + base64.StdEncoding.EncodeToString(sig)
}

// releaseFixtureServer serves GitHub-shaped release metadata plus release
// assets on one loopback origin. Asset bodies, the manifest text, the
// signature text, and redirect behavior are programmable per test.
type releaseFixtureServer struct {
	server *httptest.Server
	mu     sync.Mutex

	// releases is the release list JSON served for the releases endpoint.
	releases string
	// assets maps the asset URL path (including /assets/) to the served body.
	assets map[string][]byte
	// assetHeaders are extra headers applied to asset responses.
	assetHeaders map[string]string
	// assetRedirect, when set, makes every asset request redirect there.
	assetRedirect string
	// linkHeader overrides the pagination Link header on page one.
	linkHeader string
	// sawAuth records every Authorization header the fixture observed.
	sawAuth []string
}

func newReleaseFixtureServer(t *testing.T) *releaseFixtureServer {
	t.Helper()
	f := &releaseFixtureServer{assets: map[string][]byte{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *releaseFixtureServer) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if auth := r.Header.Get("Authorization"); auth != "" {
		f.sawAuth = append(f.sawAuth, auth)
	}
	if r.URL.Path == "/slow" {
		// Hold the response past any reasonable fetch budget so deadline
		// tests observe the client-side bound. The wait ends when the client
		// aborts so fixture shutdown is never blocked.
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/repos/") && strings.HasSuffix(r.URL.Path, "/releases") {
		if f.linkHeader != "" {
			w.Header().Set("Link", f.linkHeader)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(f.releases))
		return
	}
	if name, ok := strings.CutPrefix(r.URL.Path, "/assets/"); ok {
		if f.assetRedirect != "" {
			w.Header().Set("Location", f.assetRedirect)
			w.WriteHeader(http.StatusFound)
			return
		}
		for k, v := range f.assetHeaders {
			w.Header().Set(k, v)
		}
		body, served := f.assets["/assets/"+name]
		if !served {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func (f *releaseFixtureServer) fixtureClient(t *testing.T) *FeedClient {
	t.Helper()
	client, err := NewFixtureFeedClient(f.server.URL, ProductionFeedSlug)
	if err != nil {
		t.Fatalf("fixture client: %v", err)
	}
	return client
}

// fixtureReleaseOptions builds one release whose assets are the exact
// required set (plus optionally the envelope) and whose bodies are
// programmable.
type fixtureReleaseOptions struct {
	tag          string
	goos, goarch string
	// tarballBytes is the platform tarball body (default deterministic).
	tarballBytes []byte
	// omit controls which required assets are left off the release.
	omit map[string]bool
	// aliasNames maps an expected exact name to a served alias variant.
	aliasNames map[string]string
	// envelope, when non-nil, advertises desktop-release.json with this body.
	envelope []byte
	// manifestMutate rewrites the manifest text before signing.
	manifestMutate func(text string) string
	// postSignMutate rewrites the served manifest bytes after signing, so
	// only signature verification can reject the tampered manifest.
	postSignMutate func(text string) string
	// wrongSigner signs the manifest with a fresh unrelated key.
	wrongSigner bool
	// extraAssets adds additional (non-required) assets.
	extraAssets []string
}

func (f *releaseFixtureServer) publishRelease(t *testing.T, opts fixtureReleaseOptions) ResolvedRelease {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()

	version := strings.TrimPrefix(opts.tag, "v")
	tarballName := ReleaseTarballName(version, opts.goos, opts.goarch)
	tarball := opts.tarballBytes
	if tarball == nil {
		tarball = []byte("fake tarball bytes for " + tarballName)
	}

	digestFor := func(body []byte) string {
		sum := sha256.Sum256(body)
		return hex.EncodeToString(sum[:])
	}
	f.assets["/assets/"+tarballName] = tarball

	manifestLines := []string{digestFor(tarball) + "  " + tarballName}
	var envelopeBody []byte
	if opts.envelope != nil {
		envelopeBody = opts.envelope
		f.assets["/assets/"+releaseEnvelopeName] = envelopeBody
		manifestLines = append(manifestLines, digestFor(envelopeBody)+"  "+releaseEnvelopeName)
	}
	for _, extra := range opts.extraAssets {
		body := []byte("extra asset " + extra)
		f.assets["/assets/"+extra] = body
		manifestLines = append(manifestLines, digestFor(body)+"  "+extra)
	}
	manifestText := strings.Join(manifestLines, "\n") + "\n"
	if opts.manifestMutate != nil {
		manifestText = opts.manifestMutate(manifestText)
	}
	manifest := []byte(manifestText)
	f.assets["/assets/"+checksumManifestName] = manifest
	signature := signFixturePayload(t, manifest)
	if opts.wrongSigner {
		_, priv, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		signature = SignaturePrefix + base64.StdEncoding.EncodeToString(ed25519.Sign(priv, manifest))
	}
	f.assets["/assets/"+checksumSignatureName] = []byte(signature + "\n")
	if opts.postSignMutate != nil {
		f.assets["/assets/"+checksumManifestName] = []byte(opts.postSignMutate(manifestText))
	}

	// Build the release metadata with exact asset names and same-origin
	// download destinations.
	type assetJSON struct {
		ID                 int64  `json:"id"`
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	}
	asset := func(i int, name string) assetJSON {
		served := name
		if alias, ok := opts.aliasNames[name]; ok {
			served = alias
			f.assets["/assets/"+alias] = f.assets["/assets/"+name]
			delete(f.assets, "/assets/"+name)
		}
		return assetJSON{ID: int64(100 + i), Name: served, BrowserDownloadURL: f.server.URL + "/assets/" + served}
	}
	assets := []assetJSON{}
	if !opts.omit["tarball"] {
		assets = append(assets, asset(0, tarballName))
	}
	if !opts.omit["manifest"] {
		assets = append(assets, asset(1, checksumManifestName))
	}
	if !opts.omit["signature"] {
		assets = append(assets, asset(2, checksumSignatureName))
	}
	if envelopeBody != nil && !opts.omit["envelope"] {
		assets = append(assets, asset(3, releaseEnvelopeName))
	}
	for i, extra := range opts.extraAssets {
		assets = append(assets, asset(4+i, extra))
	}
	release := map[string]any{
		"tag_name":   opts.tag,
		"draft":      false,
		"prerelease": false,
		"html_url":   "https://github.com/" + ProductionFeedSlug + "/releases/tag/" + opts.tag,
		"assets":     assets,
	}
	raw, err := json.Marshal([]any{release})
	if err != nil {
		t.Fatal(err)
	}
	f.releases = string(raw)

	resolved := ResolvedRelease{
		Version:   version,
		TagName:   opts.tag,
		Tarball:   ReleaseAsset{Name: tarballName, URL: f.server.URL + "/assets/" + tarballName},
		Manifest:  ReleaseAsset{Name: checksumManifestName, URL: f.server.URL + "/assets/" + checksumManifestName},
		Signature: ReleaseAsset{Name: checksumSignatureName, URL: f.server.URL + "/assets/" + checksumSignatureName},
	}
	if envelopeBody != nil {
		resolved.Envelope = &ReleaseAsset{Name: releaseEnvelopeName, URL: f.server.URL + "/assets/" + releaseEnvelopeName}
	}
	return resolved
}

// republishEnvelope replaces the served envelope body and re-signs the
// manifest so only the rule under test can reject it.
func (f *releaseFixtureServer) republishEnvelope(t *testing.T, body string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.assets["/assets/"+releaseEnvelopeName] = []byte(body)
	sum := sha256.Sum256([]byte(body))
	lines := []string{}
	for _, line := range strings.Split(string(f.assets["/assets/"+checksumManifestName]), "\n") {
		if line == "" {
			continue
		}
		if strings.Contains(line, releaseEnvelopeName) {
			line = hex.EncodeToString(sum[:]) + "  " + releaseEnvelopeName
		}
		lines = append(lines, line)
	}
	manifestText := strings.Join(lines, "\n") + "\n"
	f.assets["/assets/"+checksumManifestName] = []byte(manifestText)
	f.assets["/assets/"+checksumSignatureName] = []byte(signFixturePayload(t, []byte(manifestText)) + "\n")
}

func validEnvelopeJSON(t *testing.T, version, contract string) []byte {
	t.Helper()
	return []byte(fmt.Sprintf(`{
  "schema_version": 1,
  "tag": "v%s",
  "version": "%s",
  "commit": "0123456789abcdef0123456789abcdef01234567",
  "artifacts": [
    {"name": "Agentico-mac-universal.dmg", "sha256": "%s", "size": 1000},
    {"name": "Agentico-x64.AppImage", "sha256": "%s", "size": 2000}
  ]%s
}`, version, version, strings.Repeat("a", 64), strings.Repeat("b", 64), contract))
}

func TestReleaseResolveAndVerifyRoundTrip(t *testing.T) {
	t.Parallel()
	f := newReleaseFixtureServer(t)
	f.publishRelease(t, fixtureReleaseOptions{
		tag:         "v1.4.0",
		goos:        "darwin",
		goarch:      "arm64",
		envelope:    validEnvelopeJSON(t, "1.4.0", ",\n  \"server_contract\": {\"api_version\": 1, \"schema_version\": 1, \"min_client_schema\": 1}"),
		extraAssets: []string{"Agentico-mac-universal.dmg"},
	})
	client := f.fixtureClient(t)

	resolved, err := client.ResolveLatestRelease(context.Background(), "darwin", "arm64")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Tarball.Name != ReleaseTarballName("1.4.0", "darwin", "arm64") {
		t.Fatalf("tarball name = %q", resolved.Tarball.Name)
	}
	if resolved.Manifest.Name != checksumManifestName || resolved.Signature.Name != checksumSignatureName {
		t.Fatalf("manifest/signature names = %q/%q", resolved.Manifest.Name, resolved.Signature.Name)
	}
	if resolved.Envelope == nil || resolved.Envelope.Name != releaseEnvelopeName {
		t.Fatalf("envelope asset = %+v", resolved.Envelope)
	}

	verified, err := client.VerifyResolvedRelease(context.Background(), resolved)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	tarball := []byte("fake tarball bytes for " + ReleaseTarballName("1.4.0", "darwin", "arm64"))
	sum := sha256.Sum256(tarball)
	if verified.TarballDigest() != hex.EncodeToString(sum[:]) {
		t.Fatalf("tarball digest = %q, want the signed manifest entry", verified.TarballDigest())
	}
	if verified.Envelope() == nil {
		t.Fatal("verified envelope is missing")
	}
	if verified.ServerContract() == nil {
		t.Fatal("verified server contract is missing")
	}
	if got := verified.ServerContract(); got.APIVersion != 1 || got.SchemaVersion != 1 || got.MinClientSchema != 1 {
		t.Fatalf("server contract = %+v", got)
	}
	if len(verified.Envelope().Artifacts) != 2 {
		t.Fatalf("envelope artifacts = %d", len(verified.Envelope().Artifacts))
	}
	if len(verified.ManifestBytes()) == 0 || verified.ManifestSignature() == "" {
		t.Fatal("verified release must retain manifest provenance")
	}
	// A subsequent release check cannot silently change the retained target.
	f.mu.Lock()
	f.releases = `[{"tag_name":"v9.9.9","draft":false,"prerelease":false,"assets":[]}]`
	f.mu.Unlock()
	if verified.Resolved().Version != "1.4.0" || verified.TarballDigest() != hex.EncodeToString(sum[:]) {
		t.Fatal("verified release identity mutated after a later feed check")
	}
}

func TestReleaseVerifyWithoutEnvelope(t *testing.T) {
	t.Parallel()
	f := newReleaseFixtureServer(t)
	f.publishRelease(t, fixtureReleaseOptions{tag: "v2.0.0", goos: "linux", goarch: "amd64"})
	client := f.fixtureClient(t)
	resolved, err := client.ResolveLatestRelease(context.Background(), "linux", "amd64")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	verified, err := client.VerifyResolvedRelease(context.Background(), resolved)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verified.Envelope() != nil || verified.EnvelopeDigest() != "" || verified.ServerContract() != nil {
		t.Fatal("absent envelope must succeed with no envelope and no contract")
	}
	if verified.TarballDigest() == "" {
		t.Fatal("tarball digest binding missing")
	}
}

func TestReleaseResolveRejectsMissingAssets(t *testing.T) {
	t.Parallel()
	for _, omit := range []string{"tarball", "manifest", "signature"} {
		t.Run(omit, func(t *testing.T) {
			f := newReleaseFixtureServer(t)
			f.publishRelease(t, fixtureReleaseOptions{
				tag: "v1.0.0", goos: "darwin", goarch: "amd64",
				omit: map[string]bool{omit: true},
			})
			_, err := f.fixtureClient(t).ResolveLatestRelease(context.Background(), "darwin", "amd64")
			if err == nil || !strings.Contains(err.Error(), "missing the required") {
				t.Fatalf("expected missing-asset rejection, got %v", err)
			}
		})
	}
}

func TestReleaseResolveRejectsAssetNameAlias(t *testing.T) {
	t.Parallel()
	for alias, label := range map[string]string{
		" " + checksumManifestName + " ":      "padded",
		strings.ToUpper(checksumManifestName): "cased",
	} {
		t.Run(label, func(t *testing.T) {
			f := newReleaseFixtureServer(t)
			f.publishRelease(t, fixtureReleaseOptions{
				tag:        "v1.0.0",
				goos:       "darwin",
				goarch:     "amd64",
				aliasNames: map[string]string{checksumManifestName: alias},
			})
			_, err := f.fixtureClient(t).ResolveLatestRelease(context.Background(), "darwin", "amd64")
			if err == nil || !strings.Contains(err.Error(), "asset name alias") {
				t.Fatalf("expected alias rejection, got %v", err)
			}
		})
	}
}

func TestReleaseVerifyRejectsTamperedManifest(t *testing.T) {
	t.Parallel()
	f := newReleaseFixtureServer(t)
	resolved := f.publishRelease(t, fixtureReleaseOptions{
		tag: "v1.0.0", goos: "darwin", goarch: "amd64",
		postSignMutate: func(text string) string {
			// Flip the first digest character of the first entry: the
			// signature still covers the original bytes, so only signature
			// verification can reject the forged manifest.
			flip := "0"
			if text[0] == '0' {
				flip = "1"
			}
			return flip + text[1:]
		},
	})
	_, err := f.fixtureClient(t).VerifyResolvedRelease(context.Background(), resolved)
	if err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("expected signature rejection, got %v", err)
	}
}

func TestReleaseVerifyRejectsWrongSigningKey(t *testing.T) {
	t.Parallel()
	f := newReleaseFixtureServer(t)
	resolved := f.publishRelease(t, fixtureReleaseOptions{
		tag: "v1.0.0", goos: "darwin", goarch: "amd64", wrongSigner: true,
	})
	_, err := f.fixtureClient(t).VerifyResolvedRelease(context.Background(), resolved)
	if err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("expected signature rejection, got %v", err)
	}
}

func TestReleaseVerifyRejectsDuplicateManifestEntries(t *testing.T) {
	t.Parallel()
	f := newReleaseFixtureServer(t)
	resolved := f.publishRelease(t, fixtureReleaseOptions{
		tag: "v1.0.0", goos: "darwin", goarch: "amd64",
		manifestMutate: func(text string) string {
			first := text[:strings.Index(text, "\n")]
			return text + first + "\n"
		},
	})
	_, err := f.fixtureClient(t).VerifyResolvedRelease(context.Background(), resolved)
	if err == nil || !strings.Contains(err.Error(), "duplicate entry") {
		t.Fatalf("expected duplicate-entry rejection, got %v", err)
	}
}

func TestReleaseVerifyRejectsAdvertisedEnvelopeProblems(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(f *releaseFixtureServer)
		want   string
	}{
		{
			name: "missing manifest coverage",
			mutate: func(f *releaseFixtureServer) {
				// Rewrite the manifest without the envelope entry (re-signed
				// so only the coverage rule can reject it).
				f.mu.Lock()
				defer f.mu.Unlock()
				var kept []string
				for _, line := range strings.Split(string(f.assets["/assets/"+checksumManifestName]), "\n") {
					if line != "" && !strings.Contains(line, releaseEnvelopeName) {
						kept = append(kept, line)
					}
				}
				manifestText := strings.Join(kept, "\n") + "\n"
				f.assets["/assets/"+checksumManifestName] = []byte(manifestText)
				f.assets["/assets/"+checksumSignatureName] = []byte(signFixturePayload(t, []byte(manifestText)) + "\n")
			},
			want: "no signed manifest coverage",
		},
		{
			name: "digest mismatch",
			mutate: func(f *releaseFixtureServer) {
				f.mu.Lock()
				defer f.mu.Unlock()
				f.assets["/assets/"+releaseEnvelopeName] = []byte(`{"schema_version":1}`)
			},
			want: "digest does not match",
		},
		{
			name: "target mismatch",
			mutate: func(f *releaseFixtureServer) {
				f.republishEnvelope(t, string(validEnvelopeJSON(t, "9.9.9", "")))
			},
			want: "target mismatch",
		},
		{
			name: "malformed envelope schema",
			mutate: func(f *releaseFixtureServer) {
				body := `{"schema_version":2,"tag":"v1.0.0","version":"1.0.0","commit":"` + strings.Repeat("c", 40) + `","artifacts":[{"name":"a.dmg","sha256":"` + strings.Repeat("a", 64) + `","size":5}]}`
				f.republishEnvelope(t, body)
			},
			want: "schema_version must be 1",
		},
		{
			name: "malformed advertised contract",
			mutate: func(f *releaseFixtureServer) {
				f.republishEnvelope(t, string(validEnvelopeJSON(t, "1.0.0", ",\n  \"server_contract\": {\"api_version\": \"one\", \"schema_version\": 1, \"min_client_schema\": 1}")))
			},
			want: "malformed envelope JSON",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newReleaseFixtureServer(t)
			resolved := f.publishRelease(t, fixtureReleaseOptions{
				tag: "v1.0.0", goos: "darwin", goarch: "amd64",
				envelope: validEnvelopeJSON(t, "1.0.0", ""),
			})
			tc.mutate(f)
			_, err := f.fixtureClient(t).VerifyResolvedRelease(context.Background(), resolved)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected rejection containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestReleaseVerifyRejectsOversizedAssets(t *testing.T) {
	t.Parallel()
	f := newReleaseFixtureServer(t)
	resolved := f.publishRelease(t, fixtureReleaseOptions{tag: "v1.0.0", goos: "darwin", goarch: "amd64"})
	client := f.fixtureClient(t)

	// An oversized manifest beyond the one-MiB cap is rejected while
	// streaming; the cap holds regardless of any header the server sends.
	f.mu.Lock()
	f.assets["/assets/"+checksumManifestName] = []byte(strings.Repeat("a", feedMaxMetadataSize+1))
	f.mu.Unlock()
	_, err := client.VerifyResolvedRelease(context.Background(), resolved)
	if err == nil || !strings.Contains(err.Error(), "exceeds the") {
		t.Fatalf("expected manifest size rejection, got %v", err)
	}

	// An oversized signature beyond the 64-KiB cap is rejected too.
	f.mu.Lock()
	f.assets["/assets/"+checksumManifestName] = []byte("digest  name\n")
	f.assets["/assets/"+checksumSignatureName] = []byte(strings.Repeat("s", releaseSignatureMaxSize+1))
	f.mu.Unlock()
	_, err = client.VerifyResolvedRelease(context.Background(), resolved)
	if err == nil || !strings.Contains(err.Error(), "exceeds the") {
		t.Fatalf("expected signature size rejection, got %v", err)
	}
}

func TestReleaseFixtureOriginPolicy(t *testing.T) {
	// Not parallel: the credential check pins GITHUB_TOKEN with t.Setenv.
	// The fixture constructor accepts only a loopback origin: fixture trust
	// can never be combined with production hosts.
	for _, bad := range []string{"https://api.github.com", "http://10.0.0.5:8080", "http://user@127.0.0.1:1", "http://127.0.0.1:9/base", "::::"} {
		if _, err := NewFixtureFeedClient(bad, ProductionFeedSlug); err == nil {
			t.Fatalf("expected fixture constructor to reject %q", bad)
		}
	}
	if _, err := NewFixtureFeedClient("http://127.0.0.1:9", ProductionFeedSlug); err != nil {
		t.Fatalf("loopback origin rejected: %v", err)
	}

	// A cross-origin asset destination (another loopback port) is rejected
	// before any request leaves.
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("cross-origin"))
	}))
	t.Cleanup(other.Close)
	f := newReleaseFixtureServer(t)
	resolved := f.publishRelease(t, fixtureReleaseOptions{tag: "v1.0.0", goos: "darwin", goarch: "amd64"})
	resolved.Manifest.URL = other.URL + "/assets/" + checksumManifestName
	if _, err := f.fixtureClient(t).VerifyResolvedRelease(context.Background(), resolved); err == nil || !strings.Contains(err.Error(), "outside the fixture origin") {
		t.Fatalf("expected cross-origin rejection, got %v", err)
	}

	// A redirect out of the fixture origin is rejected too.
	f2 := newReleaseFixtureServer(t)
	resolved2 := f2.publishRelease(t, fixtureReleaseOptions{tag: "v1.0.0", goos: "darwin", goarch: "amd64"})
	f2.mu.Lock()
	f2.assetRedirect = other.URL + "/assets/" + checksumManifestName
	f2.mu.Unlock()
	if _, err := f2.fixtureClient(t).VerifyResolvedRelease(context.Background(), resolved2); err == nil || !strings.Contains(err.Error(), "outside the fixture origin") {
		t.Fatalf("expected cross-origin redirect rejection, got %v", err)
	}

	// A pagination link off the fixture origin is rejected.
	f3 := newReleaseFixtureServer(t)
	f3.publishRelease(t, fixtureReleaseOptions{tag: "v1.1.0", goos: "darwin", goarch: "amd64"})
	f3.mu.Lock()
	f3.linkHeader = fmt.Sprintf(`<%s/repos/%s/releases?per_page=%d&page=2>; rel="next"`, other.URL, ProductionFeedSlug, feedPerPage)
	f3.releases = f3.releases[:len(f3.releases)-1] + `,{"tag_name":"v1.2.0","draft":false,"prerelease":false,"assets":[]}]`
	f3.mu.Unlock()
	if _, err := f3.fixtureClient(t).ResolveLatestRelease(context.Background(), "darwin", "amd64"); err == nil || !strings.Contains(err.Error(), "outside the fixture origin") {
		t.Fatalf("expected cross-origin pagination rejection, got %v", err)
	}

	// Fixture requests never carry credentials, even when the surrounding
	// process holds GITHUB_TOKEN: the fixture server records every
	// Authorization header it sees.
	f4 := newReleaseFixtureServer(t)
	resolved4 := f4.publishRelease(t, fixtureReleaseOptions{tag: "v1.0.0", goos: "darwin", goarch: "amd64"})
	t.Setenv("GITHUB_TOKEN", "sekrit-token")
	client4 := f4.fixtureClient(t)
	if _, err := client4.ResolveLatestRelease(context.Background(), "darwin", "amd64"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := client4.VerifyResolvedRelease(context.Background(), resolved4); err != nil {
		t.Fatalf("verify: %v", err)
	}
	f4.mu.Lock()
	sawAuth := len(f4.sawAuth) > 0
	f4.mu.Unlock()
	if sawAuth {
		t.Fatal("fixture routing must never send credentials")
	}
}

func TestReleaseProductionDestinationPolicy(t *testing.T) {
	t.Parallel()
	client := NewProductionFeedClient("", func() string { return "sekrit-token" })

	// Asset destinations on production clients obey the closed host
	// allowlist; a disallowed host is rejected before any request.
	resolved := ResolvedRelease{
		Version:   "1.0.0",
		TagName:   "v1.0.0",
		Tarball:   ReleaseAsset{Name: "t.tar.gz", URL: "https://objects.githubusercontent.com/t"},
		Manifest:  ReleaseAsset{Name: checksumManifestName, URL: "https://evil.example.com/checksums.txt"},
		Signature: ReleaseAsset{Name: checksumSignatureName, URL: "https://github.com/checksums.txt.sig"},
	}
	if _, err := client.VerifyResolvedRelease(context.Background(), resolved); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected production host-policy rejection, got %v", err)
	}

	// Credentials follow only the API host, never an asset host: an injected
	// transport records the requests without live GitHub traffic.
	capture := &releaseCapturingTransport{body: "digest  name\n"}
	client.client.Transport = capture
	apiResolved := ResolvedRelease{
		Version:   "1.0.0",
		TagName:   "v1.0.0",
		Tarball:   ReleaseAsset{Name: "t.tar.gz", URL: "https://objects.githubusercontent.com/t"},
		Manifest:  ReleaseAsset{Name: checksumManifestName, URL: "https://api.github.com/repos/o/r/releases/assets/1"},
		Signature: ReleaseAsset{Name: checksumSignatureName, URL: "https://objects.githubusercontent.com/s"},
	}
	_, err := client.VerifyResolvedRelease(context.Background(), apiResolved)
	if err == nil || !strings.Contains(err.Error(), "signature") {
		// The synthetic body cannot verify; only the credential routing is
		// under test here.
		t.Fatalf("expected the synthetic body to fail at the signature check, got %v", err)
	}
	if capture.lastAPI == nil || capture.lastOther == nil {
		t.Fatal("expected one API-host and one asset-host request")
	}
	if auth := capture.lastAPI.Header.Get("Authorization"); auth != "Bearer sekrit-token" {
		t.Fatalf("api.github.com request carried %q, want the bearer token", auth)
	}
	if auth := capture.lastOther.Header.Get("Authorization"); auth != "" {
		t.Fatalf("asset host request carried credentials: %q", auth)
	}
}

// releaseCapturingTransport records the last request per host class and
// answers a fixed body without touching the network.
type releaseCapturingTransport struct {
	body      string
	lastAPI   *http.Request
	lastOther *http.Request
}

func (c *releaseCapturingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	if req.URL.Hostname() == "api.github.com" {
		c.lastAPI = cloned
	} else {
		c.lastOther = cloned
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(c.body)),
		Request:    req,
	}, nil
}

func TestReleaseVerifyDeadlineAcrossRedirects(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the real 8s fetch deadline")
	}
	t.Parallel()
	f := newReleaseFixtureServer(t)
	resolved := f.publishRelease(t, fixtureReleaseOptions{tag: "v1.0.0", goos: "darwin", goarch: "amd64"})
	// The manifest asset redirects once (same-origin) to /slow, which stalls:
	// the single eight-second fetch budget covers the hop plus the stall, so
	// the redirect chain can never reset the deadline indefinitely.
	f.mu.Lock()
	f.assetRedirect = f.server.URL + "/slow"
	f.mu.Unlock()
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := f.fixtureClient(t).VerifyResolvedRelease(ctx, resolved)
	if err == nil {
		t.Fatal("stalled asset should fail the deadline")
	}
	if elapsed := time.Since(start); elapsed >= 9*time.Second {
		t.Fatalf("redirect chain reset the fetch deadline: %v", elapsed)
	}
}
