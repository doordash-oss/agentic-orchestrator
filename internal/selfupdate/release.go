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
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Exact release asset basenames. The four platform tarballs follow the
// goreleaser archive template agentic-orchestrator_<version>_<os>_<arch>.tar.gz;
// the checksum manifest and its detached signature are fixed names.
const (
	checksumManifestName  = "checksums.txt"
	checksumSignatureName = "checksums.txt.sig"
	releaseEnvelopeName   = "desktop-release.json"

	// releaseSignatureMaxSize caps the detached signature asset fetch.
	releaseSignatureMaxSize = 64 << 10
)

// ReleaseTarballName returns the exact expected platform tarball basename
// for one release version and GOOS/GOARCH pair.
func ReleaseTarballName(version, goos, goarch string) string {
	return fmt.Sprintf("agentic-orchestrator_%s_%s_%s.tar.gz", version, goos, goarch)
}

// ReleaseAsset identifies one release asset by its exact basename and its
// download destination.
type ReleaseAsset struct {
	Name string
	URL  string
}

// ResolvedRelease is the selected release with its exact asset identities
// retained for verification. It is a snapshot: later feed checks can never
// silently change it.
type ResolvedRelease struct {
	Version    string
	TagName    string
	ReleaseURL string
	Tarball    ReleaseAsset
	Manifest   ReleaseAsset
	Signature  ReleaseAsset
	// Envelope is the optional advertised release envelope
	// (desktop-release.json). Absence is allowed; when it is advertised it
	// must verify before anything trusts its contents.
	Envelope *ReleaseAsset
}

// ServerContract is the typed server compatibility contract optionally
// advertised inside a verified release envelope.
type ServerContract struct {
	APIVersion      int `json:"api_version"`
	SchemaVersion   int `json:"schema_version"`
	MinClientSchema int `json:"min_client_schema"`
}

// EnvelopeArtifact is one artifact entry of the release envelope inventory.
type EnvelopeArtifact struct {
	Name   string
	SHA256 string
	Size   int64
}

// ReleaseEnvelope is the parsed release envelope: the desktop release
// manifest format with an optional typed server_contract extension.
type ReleaseEnvelope struct {
	SchemaVersion  int
	Tag            string
	Version        string
	Commit         string
	Artifacts      []EnvelopeArtifact
	ServerContract *ServerContract
}

// VerifiedRelease carries the authenticated identity of one resolved
// release: the manifest signature was verified over the exact manifest
// bytes, the selected tarball digest is bound through the signed manifest,
// and an advertised envelope — when present — was digest-verified through
// the same manifest and parsed strictly before its contract was exposed.
// The retained provenance is immutable and is reverified at the commit
// boundary.
type VerifiedRelease struct {
	resolved          ResolvedRelease
	tarballDigest     string
	envelopeDigest    string
	envelope          *ReleaseEnvelope
	serverContract    *ServerContract
	manifestBytes     []byte
	manifestSignature string
	// trustRoot is the exact key that verified the manifest; commit-time
	// revalidation re-verifies with this retained key and refuses any key
	// that is not one of the two embeddings.
	trustRoot ed25519.PublicKey
}

// Resolved returns the release and asset identities the verification bound.
func (v VerifiedRelease) Resolved() ResolvedRelease { return v.resolved }

// TarballDigest returns the signed manifest digest for the platform tarball.
func (v VerifiedRelease) TarballDigest() string { return v.tarballDigest }

// EnvelopeDigest returns the signed manifest digest for the advertised
// envelope, or "" when none was advertised.
func (v VerifiedRelease) EnvelopeDigest() string { return v.envelopeDigest }

// Envelope returns the parsed verified envelope, or nil when none was
// advertised.
func (v VerifiedRelease) Envelope() *ReleaseEnvelope { return v.envelope }

// ServerContract returns the verified server contract, or nil when the
// verified envelope carried none. Absence is allowed.
func (v VerifiedRelease) ServerContract() *ServerContract { return v.serverContract }

// ManifestBytes returns the exact signed checksum-manifest bytes retained
// for commit-time reverification.
func (v VerifiedRelease) ManifestBytes() []byte { return v.manifestBytes }

// ManifestSignature returns the retained detached-signature text for
// commit-time reverification.
func (v VerifiedRelease) ManifestSignature() string { return v.manifestSignature }

// ResolveLatestRelease resolves the greatest stable release and its exact
// asset identities for one platform: the platform tarball, the checksum
// manifest, its detached signature, and the optional advertised envelope.
// Selection reuses the bounded availability pagination; ambiguous release or
// asset identities, basename aliases, and missing required assets are
// rejected. This resolution is metadata-only: nothing is downloaded or
// staged here.
func (c *FeedClient) ResolveLatestRelease(ctx context.Context, goos, goarch string) (ResolvedRelease, error) {
	rel, parts, err := c.latestStableRelease(ctx)
	if err != nil {
		return ResolvedRelease{}, err
	}
	version := fmt.Sprintf("%d.%d.%d", parts[0], parts[1], parts[2])
	resolved, err := selectReleaseAssets(rel, version, goos, goarch)
	if err != nil {
		return ResolvedRelease{}, err
	}
	resolved.ReleaseURL = strings.TrimSpace(rel.HTMLURL)
	return resolved, nil
}

// ResolveReleaseVersion resolves the exact stable release for one pinned
// version and its exact asset identities: the install pipeline stages the
// version an accepted request pinned, never whatever is latest when staging
// begins. A pinned target that no longer exists on the feed fails.
func (c *FeedClient) ResolveReleaseVersion(ctx context.Context, version, goos, goarch string) (ResolvedRelease, error) {
	rel, _, err := c.releaseByVersion(ctx, version)
	if err != nil {
		return ResolvedRelease{}, err
	}
	resolved, err := selectReleaseAssets(rel, version, goos, goarch)
	if err != nil {
		return ResolvedRelease{}, err
	}
	resolved.ReleaseURL = strings.TrimSpace(rel.HTMLURL)
	return resolved, nil
}

// selectReleaseAssets matches the exact expected basenames on one selected
// release. Any asset whose normalized name equals an expected name without
// being byte-identical to it is a basename alias and is rejected; required
// assets must be present with a usable download destination.
func selectReleaseAssets(rel *feedRelease, version, goos, goarch string) (ResolvedRelease, error) {
	tag := strings.TrimSpace(deref(rel.TagName))
	resolved := ResolvedRelease{
		Version: version,
		TagName: tag,
	}
	expected := map[string]*ReleaseAsset{
		ReleaseTarballName(version, goos, goarch): &resolved.Tarball,
		checksumManifestName:                      &resolved.Manifest,
		checksumSignatureName:                     &resolved.Signature,
		releaseEnvelopeName:                       nil, // optional
	}
	normalizedExpected := make(map[string]string, len(expected))
	for name := range expected {
		normalizedExpected[strings.ToLower(strings.TrimSpace(name))] = name
	}
	for i := range rel.Assets {
		asset := &rel.Assets[i]
		name := strings.TrimSpace(asset.Name)
		want, ok := normalizedExpected[strings.ToLower(name)]
		if !ok {
			continue
		}
		// The wire name must be byte-identical to the exact expected
		// basename: padded, cased, or otherwise normalized lookalikes are
		// basename aliases and are rejected.
		if asset.Name != want {
			return ResolvedRelease{}, &FeedError{
				Reason: fmt.Sprintf("release %s carries asset name alias %q for expected %q", tag, asset.Name, want),
			}
		}
		target := expected[want]
		if target == nil {
			if resolved.Envelope != nil {
				return ResolvedRelease{}, &FeedError{Reason: fmt.Sprintf("ambiguous duplicate envelope asset on release %s", tag)}
			}
			resolved.Envelope = &ReleaseAsset{Name: name, URL: strings.TrimSpace(asset.BrowserDownloadURL)}
			continue
		}
		if target.Name != "" {
			return ResolvedRelease{}, &FeedError{Reason: fmt.Sprintf("ambiguous duplicate asset %q on release %s", name, tag)}
		}
		*target = ReleaseAsset{Name: name, URL: strings.TrimSpace(asset.BrowserDownloadURL)}
	}
	for _, required := range []struct {
		asset ReleaseAsset
		name  string
		label string
	}{
		{resolved.Tarball, ReleaseTarballName(version, goos, goarch), "platform tarball"},
		{resolved.Manifest, checksumManifestName, "checksum manifest"},
		{resolved.Signature, checksumSignatureName, "checksum signature"},
	} {
		if required.asset.Name == "" {
			return ResolvedRelease{}, &FeedError{
				Reason: fmt.Sprintf("release %s is missing the required %s asset %q", tag, required.label, required.name),
			}
		}
		if required.asset.URL == "" {
			return ResolvedRelease{}, &FeedError{
				Reason: fmt.Sprintf("release asset %q on %s carries no download destination", required.asset.Name, tag),
			}
		}
	}
	if resolved.Envelope != nil && resolved.Envelope.URL == "" {
		return ResolvedRelease{}, &FeedError{
			Reason: fmt.Sprintf("release asset %q on %s carries no download destination", releaseEnvelopeName, tag),
		}
	}
	return resolved, nil
}

// VerifyResolvedRelease authenticates one resolved release end to end before
// anything is staged or probed: the detached checksum-manifest signature is
// verified over the exact manifest bytes with the client's trust root, the
// platform tarball digest is bound through the signed manifest, and an
// advertised envelope is digest-verified through the same manifest and
// parsed strictly — its tag and version must match the selected target
// before its optional server contract is exposed. Missing envelope or absent
// server contract succeeds with no contract; a fetch failure, missing
// manifest coverage, a bad digest, a malformed envelope, a target mismatch,
// or a malformed advertised contract rejects staging before any probe.
func (c *FeedClient) VerifyResolvedRelease(ctx context.Context, resolved ResolvedRelease) (VerifiedRelease, error) {
	key, err := c.releaseTrustRoot()
	if err != nil {
		return VerifiedRelease{}, err
	}
	manifestBytes, err := c.fetchBounded(ctx, resolved.Manifest.URL, feedMaxMetadataSize)
	if err != nil {
		return VerifiedRelease{}, fmt.Errorf("fetching checksum manifest %q: %w", resolved.Manifest.Name, err)
	}
	signatureBytes, err := c.fetchBounded(ctx, resolved.Signature.URL, releaseSignatureMaxSize)
	if err != nil {
		return VerifiedRelease{}, fmt.Errorf("fetching checksum signature %q: %w", resolved.Signature.Name, err)
	}
	signatureText := string(signatureBytes)
	if err := VerifyReleaseSignature(manifestBytes, signatureText, key); err != nil {
		return VerifiedRelease{}, fmt.Errorf("checksum manifest %q signature: %w", resolved.Manifest.Name, err)
	}
	entries, err := parseChecksumManifest(manifestBytes)
	if err != nil {
		return VerifiedRelease{}, err
	}
	verified := VerifiedRelease{
		resolved:          resolved,
		manifestBytes:     manifestBytes,
		manifestSignature: signatureText,
		trustRoot:         key,
	}
	digest, ok := entries[resolved.Tarball.Name]
	if !ok {
		return VerifiedRelease{}, fmt.Errorf("signed checksum manifest carries no entry for %q", resolved.Tarball.Name)
	}
	verified.tarballDigest = digest

	if resolved.Envelope == nil {
		// Absence is allowed: no envelope, no contract, staging proceeds.
		return verified, nil
	}
	envelopeDigest, ok := entries[resolved.Envelope.Name]
	if !ok {
		return VerifiedRelease{}, fmt.Errorf("advertised envelope %q has no signed manifest coverage", resolved.Envelope.Name)
	}
	envelopeBytes, err := c.fetchBounded(ctx, resolved.Envelope.URL, feedMaxMetadataSize)
	if err != nil {
		return VerifiedRelease{}, fmt.Errorf("fetching release envelope %q: %w", resolved.Envelope.Name, err)
	}
	actual := sha256.Sum256(envelopeBytes)
	if hex.EncodeToString(actual[:]) != envelopeDigest {
		return VerifiedRelease{}, fmt.Errorf("release envelope %q digest does not match the signed manifest", resolved.Envelope.Name)
	}
	envelope, err := parseReleaseEnvelope(envelopeBytes)
	if err != nil {
		return VerifiedRelease{}, fmt.Errorf("release envelope %q: %w", resolved.Envelope.Name, err)
	}
	if envelope.Tag != resolved.TagName || envelope.Version != resolved.Version {
		return VerifiedRelease{}, fmt.Errorf(
			"release envelope target mismatch: envelope tag %q version %q vs release tag %q version %q",
			envelope.Tag, envelope.Version, resolved.TagName, resolved.Version)
	}
	verified.envelopeDigest = envelopeDigest
	verified.envelope = envelope
	verified.serverContract = envelope.ServerContract
	return verified, nil
}

// releaseTrustRoot selects the trust root for release verification: the
// production embedding for production clients, the committed fixture key for
// fixture clients. Fixture trust is unreachable from production traffic
// because a fixture client can only target its one loopback origin.
func (c *FeedClient) releaseTrustRoot() (ed25519.PublicKey, error) {
	if c.fixture {
		return FixtureReleasePublicKey()
	}
	return ProductionReleasePublicKey()
}

var (
	checksumDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	commitPattern         = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// parseChecksumManifest parses the signed checksum manifest exactly as
// written: one `<sha256>  <name>` line per artifact. Duplicate or malformed
// entries are rejected; nothing is consumed from an unverified manifest
// before its signature has been checked by the caller.
func parseChecksumManifest(data []byte) (map[string]string, error) {
	text := string(data)
	if !strings.HasSuffix(text, "\n") {
		return nil, errors.New("checksum manifest does not end with a newline")
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	entries := make(map[string]string, len(lines))
	for _, line := range lines {
		if line == "" {
			return nil, errors.New("checksum manifest carries an empty line")
		}
		digest, name, ok := strings.Cut(line, "  ")
		if !ok {
			return nil, fmt.Errorf("checksum manifest line %q is malformed", line)
		}
		if !checksumDigestPattern.MatchString(digest) {
			return nil, fmt.Errorf("checksum manifest entry %q carries an invalid digest", name)
		}
		if name == "" || strings.TrimSpace(name) != name {
			return nil, fmt.Errorf("checksum manifest entry name %q is malformed", name)
		}
		if _, dup := entries[name]; dup {
			return nil, fmt.Errorf("checksum manifest carries a duplicate entry for %q", name)
		}
		entries[name] = digest
	}
	return entries, nil
}

// rawReleaseEnvelope mirrors the release envelope JSON exactly: every field
// is a pointer or slice so absence is distinguishable from zero, and the
// decoder rejects unknown keys so the envelope stays a closed schema with
// the optional server_contract extension.
type rawReleaseEnvelope struct {
	SchemaVersion  *int                  `json:"schema_version"`
	Tag            *string               `json:"tag"`
	Version        *string               `json:"version"`
	Commit         *string               `json:"commit"`
	Artifacts      []rawEnvelopeArtifact `json:"artifacts"`
	ServerContract *rawServerContract    `json:"server_contract,omitempty"`
}

type rawEnvelopeArtifact struct {
	Name   *string `json:"name"`
	SHA256 *string `json:"sha256"`
	Size   *int64  `json:"size"`
}

type rawServerContract struct {
	APIVersion      *int `json:"api_version"`
	SchemaVersion   *int `json:"schema_version"`
	MinClientSchema *int `json:"min_client_schema"`
}

// parseReleaseEnvelope parses the release envelope strictly: exact schema 1,
// tag/version/commit/artifacts present and well-formed, and an optional
// typed server_contract with positive integer fields. The artifact inventory
// is validated for shape only — the desktop-only inventory never includes a
// headless tarball, and this parser must not require one.
func parseReleaseEnvelope(data []byte) (*ReleaseEnvelope, error) {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var raw rawReleaseEnvelope
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("malformed envelope JSON: %w", err)
	}
	if err := requireDecodedEOF(dec); err != nil {
		return nil, err
	}
	if raw.SchemaVersion == nil || *raw.SchemaVersion != 1 {
		return nil, errors.New("envelope schema_version must be 1")
	}
	if raw.Tag == nil || *raw.Tag == "" {
		return nil, errors.New("envelope tag is missing")
	}
	if raw.Version == nil || *raw.Version == "" {
		return nil, errors.New("envelope version is missing")
	}
	if *raw.Tag != "v"+*raw.Version {
		return nil, fmt.Errorf("envelope tag %q does not match version %q", *raw.Tag, *raw.Version)
	}
	if raw.Commit == nil || !commitPattern.MatchString(*raw.Commit) {
		return nil, errors.New("envelope commit must be 40 lowercase hex characters")
	}
	if len(raw.Artifacts) == 0 {
		return nil, errors.New("envelope carries no artifacts")
	}
	envelope := &ReleaseEnvelope{
		SchemaVersion: *raw.SchemaVersion,
		Tag:           *raw.Tag,
		Version:       *raw.Version,
		Commit:        *raw.Commit,
	}
	for i := range raw.Artifacts {
		art := &raw.Artifacts[i]
		if art.Name == nil || *art.Name == "" {
			return nil, errors.New("envelope artifact carries no name")
		}
		if art.SHA256 == nil || !checksumDigestPattern.MatchString(*art.SHA256) {
			return nil, fmt.Errorf("envelope artifact %q carries an invalid sha256", deref(art.Name))
		}
		if art.Size == nil || *art.Size <= 0 || *art.Size > int64(^uint32(0)) {
			return nil, fmt.Errorf("envelope artifact %q carries an invalid size", *art.Name)
		}
		envelope.Artifacts = append(envelope.Artifacts, EnvelopeArtifact{
			Name:   *art.Name,
			SHA256: *art.SHA256,
			Size:   *art.Size,
		})
	}
	if raw.ServerContract != nil {
		sc := raw.ServerContract
		if sc.APIVersion == nil || sc.SchemaVersion == nil || sc.MinClientSchema == nil {
			return nil, errors.New("advertised server_contract is missing required fields")
		}
		if *sc.APIVersion <= 0 || *sc.SchemaVersion <= 0 || *sc.MinClientSchema <= 0 {
			return nil, errors.New("advertised server_contract fields must be positive integers")
		}
		envelope.ServerContract = &ServerContract{
			APIVersion:      *sc.APIVersion,
			SchemaVersion:   *sc.SchemaVersion,
			MinClientSchema: *sc.MinClientSchema,
		}
	}
	return envelope, nil
}

// requireDecodedEOF rejects trailing JSON after the envelope object.
func requireDecodedEOF(dec *json.Decoder) error {
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("envelope carries trailing JSON data")
	}
	return nil
}
