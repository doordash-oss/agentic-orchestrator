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

package clone

import (
	"strings"
	"testing"
)

func TestValidateCloneRemoteAcceptsSupportedForms(t *testing.T) {
	t.Parallel()
	valid := []string{
		"https://github.com/acme/widget.git",
		"http://gitea.internal/acme/widget.git",
		"https://example.com/acme/deep/nested/widget.git",
		"ssh://git@github.com:22/acme/widget.git",
		"ssh://git@github.com/acme/widget.git",
		"ssh://git@ssh.github.com:443/acme/widget.git",
		"ssh://host.example.com/srv/git/widget.git",
		"git@github.com:acme/widget.git",
		"github.com:acme/widget.git",
		"gh:acme/widget.git",
		"git@bitbucket.org:acme/team/widget.git",
		"gitolite:widget",
	}
	for _, remote := range valid {
		if err := ValidateCloneRemote(remote); err != nil {
			t.Errorf("ValidateCloneRemote(%q) = %v, want nil", remote, err)
		}
	}
}

func TestValidateCloneRemoteRejectsUnsupportedForms(t *testing.T) {
	t.Parallel()
	cases := []struct {
		remote string
		kind   string
	}{
		{"/tmp/local/path", ValidationRemoteInvalid},
		{"./relative/path", ValidationRemoteInvalid},
		{"~/src/widget", ValidationRemoteInvalid},
		{"file:///srv/git/widget.git", ValidationRemoteTransport},
		{"git://example.com/acme/widget.git", ValidationRemoteTransport},
		{"ext::ssh-wrapper-%G", ValidationRemoteTransport},
		{"--upload-pack=evil", ValidationRemoteOptionLike},
		{"https://example.com/acme/widget.git?token=abc123", ValidationRemoteCredentials},
		{"https://example.com/acme/widget.git#frag", ValidationRemoteCredentials},
		{"https://user:secretpw@example.com/acme/widget.git", ValidationRemoteCredentials},
		{"ssh://user:secretpw@example.com/acme/widget.git", ValidationRemoteCredentials},
		{"user:secretpw@example.com:acme/widget.git", ValidationRemoteCredentials},
		{"https://user@example.com/acme/widget.git", ValidationRemoteCredentials},
		{"https://example.com", ValidationRemoteInvalid},
		{"ssh://example.com", ValidationRemoteInvalid},
		{"ssh://example.com:notaport/acme/widget.git", ValidationRemoteInvalid},
		{"https://exam ple.com/acme/widget.git", ValidationRemoteInvalid},
		{"https://example.com/acme/wid\x01get.git", ValidationRemoteInvalid},
		{"", ValidationRemoteInvalid},
	}
	for _, tc := range cases {
		err := ValidateCloneRemote(tc.remote)
		if err == nil {
			t.Errorf("ValidateCloneRemote(%q) = nil, want %s", tc.remote, tc.kind)
			continue
		}
		verr, ok := err.(*ValidationError)
		if !ok {
			t.Fatalf("ValidateCloneRemote(%q) error type %T", tc.remote, err)
		}
		if verr.Kind != tc.kind {
			t.Errorf("ValidateCloneRemote(%q) kind = %s, want %s", tc.remote, verr.Kind, tc.kind)
		}
	}
}

func TestValidateCloneRemoteRejectsOverlongRemote(t *testing.T) {
	t.Parallel()
	long := "https://example.com/" + strings.Repeat("a", MaxRemoteURLLength) + ".git"
	err := ValidateCloneRemote(long)
	if err == nil {
		t.Fatal("overlong remote accepted")
	}
	if verr := err.(*ValidationError); verr.Kind != ValidationRemoteTooLong {
		t.Fatalf("kind = %s, want %s", verr.Kind, ValidationRemoteTooLong)
	}
}

func TestRedactRemoteNeverEchoesSecrets(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"https://user:secretpw@example.com/acme/widget.git?token=abc": "contains neither secretpw nor token value",
		"ssh://user:secretpw@example.com/acme/widget.git#frag":        "contains neither secretpw nor frag",
		"user:secretpw@example.com:acme/widget.git":                   "contains neither secretpw nor path",
	}
	for raw, want := range cases {
		redacted := RedactRemote(raw)
		if strings.Contains(redacted, "secretpw") {
			t.Errorf("RedactRemote(%q) = %q leaked password", raw, redacted)
		}
		if strings.Contains(redacted, "token=abc") || strings.Contains(redacted, "#frag") {
			t.Errorf("RedactRemote(%q) = %q leaked query/fragment", raw, redacted)
		}
		_ = want
	}
}

func TestValidateDestination(t *testing.T) {
	t.Parallel()
	valid := []string{"widget", "my-widget-2", "Widget.Name", "ünïcode-repo", "a"}
	for _, name := range valid {
		if err := ValidateDestination(name); err != nil {
			t.Errorf("ValidateDestination(%q) = %v, want nil", name, err)
		}
	}
	invalid := map[string]string{
		"":                ValidationDestinationInvalid,
		".":               ValidationDestinationInvalid,
		"..":              ValidationDestinationInvalid,
		".hidden":         ValidationDestinationInvalid,
		"..traversal":     ValidationDestinationInvalid,
		"with..inside":    ValidationDestinationInvalid,
		"sub/dir":         ValidationDestinationInvalid,
		"sub\\dir":        ValidationDestinationInvalid,
		"-option":         ValidationDestinationInvalid,
		"with space":      ValidationDestinationInvalid,
		"with\x01control": ValidationDestinationInvalid,
		"trailingdot.":    ValidationDestinationInvalid,
		"trailingspace ":  ValidationDestinationInvalid,
		"CON":             ValidationDestinationReserved,
		"com1":            ValidationDestinationReserved,
		"LPT9":            ValidationDestinationReserved,
		strings.Repeat("x", MaxDestinationLength+1): ValidationDestinationTooLong,
	}
	for name, kind := range invalid {
		err := ValidateDestination(name)
		if err == nil {
			t.Errorf("ValidateDestination(%q) = nil, want %s", name, kind)
			continue
		}
		if verr := err.(*ValidationError); verr.Kind != kind {
			t.Errorf("ValidateDestination(%q) kind = %s, want %s", name, verr.Kind, kind)
		}
	}
}

func TestValidateIdempotencyKey(t *testing.T) {
	t.Parallel()
	if err := ValidateIdempotencyKey("0192f0c1-8f2a-7c3e-b9d1-3e4f5a6b7c8d"); err != nil {
		t.Errorf("valid uuid key rejected: %v", err)
	}
	if err := ValidateIdempotencyKey(""); err == nil {
		t.Error("empty key accepted")
	}
	if err := ValidateIdempotencyKey(strings.Repeat("k", MaxIdempotencyLength+1)); err == nil {
		t.Error("overlong key accepted")
	}
	if err := ValidateIdempotencyKey("with space"); err == nil {
		t.Error("key with space accepted")
	}
	if err := ValidateIdempotencyKey("with\x01control"); err == nil {
		t.Error("key with control accepted")
	}
}

func TestSuggestDestination(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"https://github.com/acme/widget.git":        "widget",
		"https://github.com/acme/widget":            "widget",
		"https://github.com/acme/widget/":           "widget",
		"git@github.com:acme/widget.git":            "widget",
		"ssh://git@github.com:2222/acme/widget.git": "widget",
		"gh:acme/deep/nested/widget.git":            "widget",
	}
	for remote, want := range cases {
		if got := SuggestDestination(remote); got != want {
			t.Errorf("SuggestDestination(%q) = %q, want %q", remote, got, want)
		}
	}
}
