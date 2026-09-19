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
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// SignaturePrefix is the exact detached-signature wire format shared by the
// desktop updater and the release signer: "agentico-ed25519:" followed by
// standard base64 of the 64-byte detached Ed25519 signature over the original
// payload bytes.
const SignaturePrefix = "agentico-ed25519:"

// productionReleasePublicKeySPKI is the production Ed25519 trust root as SPKI
// DER, base64-encoded. It must decode to the exact same key bytes as the
// RELEASE_PUBLIC_KEY embedding in desktop/src/main/updates.ts; the release
// signer refuses to write a signature when the two embeddings disagree, and
// test/release-trust-vectors holds shared fixture vectors both ecosystems
// verify byte-for-byte.
const productionReleasePublicKeySPKI = "MCowBQYDK2VwAyEAhuVYcgW0zOV+M0/dJ/b+KjDBUijMv3ieZCwJB7RIhdU="

// fixtureReleasePublicKeySPKI is the public half of the committed test-only
// Ed25519 fixture key (desktop/test/e2e/helpers/update-fixtures.ts). It is
// trusted only by deliberately built fixture routing bound to one explicit
// loopback origin, never for production hosts.
const fixtureReleasePublicKeySPKI = "MCowBQYDK2VwAyEAmhM+TNlJSPzGSFwd/DakW3G6MzxCpouletrsW4WAezE="

// Ed25519 SPKI DER is a fixed 44-byte encoding: a 12-byte algorithm header
// followed by the 32-byte raw key.
const ed25519SPKILength = 44

// parseReleasePublicKey decodes an SPKI base64 embedding into the raw Ed25519
// public key bytes. Missing, malformed, or non-Ed25519 material fails
// deterministically.
func parseReleasePublicKey(spkiBase64 string) (ed25519.PublicKey, error) {
	der, err := base64.StdEncoding.DecodeString(strings.TrimSpace(spkiBase64))
	if err != nil {
		return nil, fmt.Errorf("release public key is not valid base64: %w", err)
	}
	if len(der) != ed25519SPKILength {
		return nil, fmt.Errorf("release public key is %d bytes; expected a %d-byte Ed25519 SPKI key", len(der), ed25519SPKILength)
	}
	key := ed25519.PublicKey(der[ed25519SPKILength-ed25519.PublicKeySize:])
	if !isEd25519SPKIPrefix(der[:ed25519SPKILength-ed25519.PublicKeySize]) {
		return nil, errors.New("release public key is not an Ed25519 SPKI key")
	}
	return key, nil
}

// isEd25519SPKIPrefix reports whether header is the fixed SubjectPublicKeyInfo
// prefix for Ed25519 keys (RFC 8410).
func isEd25519SPKIPrefix(header []byte) bool {
	want := []byte{0x30, 0x2a, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x70, 0x03, 0x21, 0x00}
	return string(header) == string(want)
}

// ProductionReleasePublicKey returns the production Ed25519 release trust
// root. It is the same key the desktop updater embeds; unequal embeddings
// fail the release signer before any signature is written.
func ProductionReleasePublicKey() (ed25519.PublicKey, error) {
	return parseReleasePublicKey(productionReleasePublicKeySPKI)
}

// FixtureReleasePublicKey returns the committed test fixture trust root. It
// is only consulted by fixture routing bound to a loopback origin.
func FixtureReleasePublicKey() (ed25519.PublicKey, error) {
	return parseReleasePublicKey(fixtureReleasePublicKeySPKI)
}

// ParseReleaseSignature parses the detached-signature wire format: optional
// surrounding whitespace, the exact "agentico-ed25519:" prefix, and canonical
// standard base64 of exactly 64 signature bytes. Anything else is malformed.
func ParseReleaseSignature(text string) ([]byte, error) {
	trimmed := strings.TrimSpace(text)
	rest, ok := strings.CutPrefix(trimmed, SignaturePrefix)
	if !ok {
		return nil, fmt.Errorf("signature is missing the %q prefix", SignaturePrefix)
	}
	if rest == "" {
		return nil, errors.New("signature carries no signature bytes")
	}
	sig, err := base64.StdEncoding.DecodeString(rest)
	if err != nil {
		return nil, fmt.Errorf("signature bytes are not valid base64: %w", err)
	}
	// Canonical encoding only: a non-canonical or padded variant that decodes
	// to the same bytes is still a different wire format than the one the
	// desktop verifier accepts.
	if base64.StdEncoding.EncodeToString(sig) != rest {
		return nil, errors.New("signature base64 is not canonical")
	}
	if len(sig) != ed25519.SignatureSize {
		return nil, fmt.Errorf("signature is %d bytes; expected %d", len(sig), ed25519.SignatureSize)
	}
	return sig, nil
}

// VerifyReleaseSignature verifies a detached release signature over the exact
// payload bytes. The payload is never trimmed, reserialized, or otherwise
// transformed before verification; the signature text follows the shared
// "agentico-ed25519:<base64>" wire format.
func VerifyReleaseSignature(payload []byte, signatureText string, key ed25519.PublicKey) error {
	if len(key) != ed25519.PublicKeySize {
		return errors.New("release public key is not an Ed25519 key")
	}
	sig, err := ParseReleaseSignature(signatureText)
	if err != nil {
		return err
	}
	if !ed25519.Verify(key, payload, sig) {
		return errors.New("release signature verification failed")
	}
	return nil
}
