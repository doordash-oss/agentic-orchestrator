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
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repoRoot anchors the shared cross-language fixtures: the committed trust
// vectors under test/release-trust-vectors and the desktop trust-root
// embedding under desktop/src/main/updates.ts. It walks upward from the
// package directory to the directory carrying go.mod, so it works in both
// plain checkouts and nested worktrees.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no repo root carrying go.mod found above the package directory")
		}
		dir = parent
	}
}

// desktopReleasePublicKeyDER extracts the production trust root embedded in
// the desktop updater and returns its SPKI DER bytes.
func desktopReleasePublicKeyDER(t *testing.T) []byte {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(repoRoot(t), "desktop", "src", "main", "updates.ts"))
	if err != nil {
		t.Fatalf("read desktop updates.ts: %v", err)
	}
	re := regexp.MustCompile("const RELEASE_PUBLIC_KEY = `-----BEGIN PUBLIC KEY-----\n([^\n]+)\n-----END PUBLIC KEY-----`")
	m := re.FindSubmatch(src)
	if m == nil {
		t.Fatal("RELEASE_PUBLIC_KEY embedding not found in desktop/src/main/updates.ts")
	}
	der, err := base64.StdEncoding.DecodeString(string(m[1]))
	if err != nil {
		t.Fatalf("desktop trust root is not valid base64: %v", err)
	}
	return der
}

func TestProductionReleasePublicKeyMatchesDesktopEmbedding(t *testing.T) {
	t.Parallel()
	key, err := ProductionReleasePublicKey()
	if err != nil {
		t.Fatalf("production embedding failed to decode: %v", err)
	}
	if len(key) != ed25519.PublicKeySize {
		t.Fatalf("production key is %d bytes; want %d", len(key), ed25519.PublicKeySize)
	}
	// The Go SPKI DER and the desktop SPKI DER must be identical bytes, which
	// makes the decoded Ed25519 key bytes identical by construction.
	goDER, err := base64.StdEncoding.DecodeString(productionReleasePublicKeySPKI)
	if err != nil {
		t.Fatalf("go embedding is not valid base64: %v", err)
	}
	if !bytes.Equal(goDER, desktopReleasePublicKeyDER(t)) {
		t.Fatal("Go and desktop production Ed25519 embeddings decode to different key bytes")
	}
}

func TestProductionAndFixtureTrustRootsAreDistinct(t *testing.T) {
	t.Parallel()
	prod, err := ProductionReleasePublicKey()
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := FixtureReleasePublicKey()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(prod, fixture) {
		t.Fatal("production and fixture trust roots must never be the same key")
	}
}

func TestParseReleasePublicKeyRejectsBadEmbeddings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
	}{
		{"missing", ""},
		{"whitespace only", "   "},
		{"not base64", "!!!not-base64!!!"},
		{"wrong length", base64.StdEncoding.EncodeToString([]byte("too short"))},
		{"wrong algorithm prefix", func() string {
			der, _ := base64.StdEncoding.DecodeString(productionReleasePublicKeySPKI)
			der[6] ^= 0x01
			return base64.StdEncoding.EncodeToString(der)
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseReleasePublicKey(tc.in); err == nil {
				t.Fatalf("expected deterministic failure for %q", tc.in)
			}
		})
	}
}

// trustVectors holds the shared committed fixture vector bytes that both the
// Go and desktop verifiers check byte-for-byte.
func trustVectors(t *testing.T) (payload, signature []byte) {
	t.Helper()
	dir := filepath.Join(repoRoot(t), "test", "release-trust-vectors")
	payload, err := os.ReadFile(filepath.Join(dir, "payload.bin"))
	if err != nil {
		t.Fatalf("read shared fixture payload: %v", err)
	}
	signature, err = os.ReadFile(filepath.Join(dir, "payload.sig"))
	if err != nil {
		t.Fatalf("read shared fixture signature: %v", err)
	}
	return payload, signature
}

func TestSharedFixtureVectorVerifies(t *testing.T) {
	t.Parallel()
	payload, signature := trustVectors(t)
	key, err := FixtureReleasePublicKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyReleaseSignature(payload, string(signature), key); err != nil {
		t.Fatalf("shared fixture vector must verify with the fixture trust root: %v", err)
	}
}

func TestSharedFixtureVectorRejectsMutatedBytes(t *testing.T) {
	t.Parallel()
	payload, signature := trustVectors(t)
	key, err := FixtureReleasePublicKey()
	if err != nil {
		t.Fatal(err)
	}
	// Changing payload bytes must fail verification.
	mutatedPayload := append([]byte{}, payload...)
	mutatedPayload[0] ^= 0x01
	if err := VerifyReleaseSignature(mutatedPayload, string(signature), key); err == nil {
		t.Fatal("mutated payload bytes must fail signature verification")
	}
	// Changing signature bytes must fail verification.
	mutatedSig := append([]byte{}, signature...)
	mutatedSig[len(mutatedSig)-3] ^= 0x01
	if err := VerifyReleaseSignature(payload, string(mutatedSig), key); err == nil {
		t.Fatal("mutated signature bytes must fail verification")
	}
	// The production trust root must never verify fixture-signed bytes.
	prod, err := ProductionReleasePublicKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyReleaseSignature(payload, string(signature), prod); err == nil {
		t.Fatal("fixture signature must not verify under the production trust root")
	}
}

func TestParseReleaseSignatureFormat(t *testing.T) {
	t.Parallel()
	_, signature := trustVectors(t)
	sig, err := ParseReleaseSignature(string(signature))
	if err != nil {
		t.Fatalf("committed vector signature must parse: %v", err)
	}
	if len(sig) != ed25519.SignatureSize {
		t.Fatalf("signature is %d bytes; want %d", len(sig), ed25519.SignatureSize)
	}
	cases := []struct {
		name string
		in   string
	}{
		{"wrong prefix", "ssh-ed25519:" + strings.TrimPrefix(string(signature), SignaturePrefix)},
		{"no prefix", strings.TrimSpace(strings.TrimPrefix(string(signature), SignaturePrefix))},
		{"empty", ""},
		{"prefix only", SignaturePrefix},
		{"not base64", SignaturePrefix + "!!!!"},
		{"short", SignaturePrefix + base64.StdEncoding.EncodeToString([]byte("short"))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseReleaseSignature(tc.in); err == nil {
				t.Fatalf("expected malformed signature %q to fail", tc.in)
			}
		})
	}
	// Surrounding whitespace (a trailing newline in a stored .sig file) is
	// tolerated, exactly like the desktop parser trims the text.
	padded := "\n" + strings.TrimSpace(string(signature)) + "\n"
	if _, err := ParseReleaseSignature(padded); err != nil {
		t.Fatalf("whitespace-padded signature must parse: %v", err)
	}
}

func TestVerifyReleaseSignatureNeverTransformsPayload(t *testing.T) {
	t.Parallel()
	// A signature over bytes with trailing whitespace must fail when the
	// verifier is handed trimmed bytes: the signed payload is verified
	// exactly as provided, never normalized.
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("agentico payload \n")
	sig := ed25519.Sign(priv, payload)
	text := SignaturePrefix + base64.StdEncoding.EncodeToString(sig)
	if err := VerifyReleaseSignature(payload, text, pub); err != nil {
		t.Fatalf("exact payload bytes must verify: %v", err)
	}
	if err := VerifyReleaseSignature(bytes.TrimRight(payload, "\n "), text, pub); err == nil {
		t.Fatal("trimmed payload bytes must not verify against the original signature")
	}
}
