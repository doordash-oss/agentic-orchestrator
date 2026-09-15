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
	"fmt"
	"net/url"
	"strings"
)

// Validation failure kinds. The server layer maps these onto canonical
// error codes; details are already bounded and redacted.
const (
	ValidationRemoteInvalid       = "remote_invalid"
	ValidationRemoteTooLong       = "remote_too_long"
	ValidationRemoteCredentials   = "remote_credentials"
	ValidationRemoteTransport     = "remote_transport"
	ValidationRemoteOptionLike    = "remote_option_like"
	ValidationDestinationInvalid  = "destination_invalid"
	ValidationDestinationTooLong  = "destination_too_long"
	ValidationDestinationReserved = "destination_reserved_name"
	ValidationKeyInvalid          = "idempotency_key_invalid"
)

// ValidationError is a bounded, redacted input-validation failure.
type ValidationError struct {
	Kind   string
	Detail string
}

func (e *ValidationError) Error() string {
	if e == nil {
		return "clone validation error"
	}
	if e.Detail == "" {
		return "clone validation error: " + e.Kind
	}
	return fmt.Sprintf("clone validation error: %s (%s)", e.Kind, e.Detail)
}

// RedactRemote masks userinfo secrets and drops query/fragment content so a
// rejected remote can be named in diagnostics without echoing credentials.
func RedactRemote(raw string) string {
	redacted := raw
	if i := strings.IndexAny(redacted, "?#"); i >= 0 {
		redacted = redacted[:i] + "[redacted-query]"
	}
	if at := strings.LastIndex(redacted, "@"); at >= 0 {
		if scheme := strings.Index(redacted, "://"); scheme >= 0 && at > scheme {
			redacted = redacted[:scheme+3] + "[redacted-user]@" + redacted[at+1:]
		} else if colon := strings.Index(redacted, ":"); colon >= 0 && at > colon {
			redacted = "[redacted-user]@" + redacted[at+1:]
		}
	}
	if len(redacted) > 256 {
		redacted = redacted[:253] + "..."
	}
	return redacted
}

// hasControlOrSpace reports whether the input contains control runes,
// DEL, or raw spaces. Clone inputs are single tokens; any of these is
// malformed or hostile.
func hasControlOrSpace(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == ' ' {
			return true
		}
	}
	return false
}

// ValidateCloneRemote validates a user-submitted clone remote. Accepted
// forms: http(s):// URLs, ssh:// URLs (optionally with user, port), and
// SCP-style [[user@]host:]path... including single-segment ssh-config host
// aliases. Rejected: local paths, file/bundle and helper transports
// (file://, git://, ext::, anything with '::'), option-like inputs,
// controls, passwords and token-bearing queries/fragments. This validator
// is deliberately independent of the publishing-oriented remote parser.
func ValidateCloneRemote(raw string) error {
	if raw == "" {
		return &ValidationError{Kind: ValidationRemoteInvalid, Detail: "remote is required"}
	}
	if len(raw) > MaxRemoteURLLength {
		return &ValidationError{Kind: ValidationRemoteTooLong}
	}
	if strings.HasPrefix(raw, "-") {
		return &ValidationError{Kind: ValidationRemoteOptionLike}
	}
	if hasControlOrSpace(raw) {
		return &ValidationError{Kind: ValidationRemoteInvalid, Detail: "control or space characters"}
	}
	lower := strings.ToLower(raw)
	if strings.Contains(raw, "::") {
		return &ValidationError{Kind: ValidationRemoteTransport, Detail: "helper transport syntax"}
	}
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		return &ValidationError{Kind: ValidationRemoteCredentials, Detail: "query or fragment"}
	}
	switch {
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		return validateHTTPRemote(raw)
	case strings.HasPrefix(lower, "ssh://"):
		return validateSSHURLRemote(raw)
	case strings.HasPrefix(lower, "file://"), strings.HasPrefix(lower, "git://"),
		strings.HasPrefix(lower, "bundle://"), strings.HasPrefix(lower, "ftp://"),
		strings.HasPrefix(lower, "rsync://"):
		return &ValidationError{Kind: ValidationRemoteTransport}
	case strings.Contains(lower, "://"):
		return &ValidationError{Kind: ValidationRemoteTransport, Detail: "unsupported scheme"}
	default:
		return validateSCPRemote(raw)
	}
}

// validateHTTPRemote validates http:// and https:// clone URLs.
func validateHTTPRemote(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return &ValidationError{Kind: ValidationRemoteInvalid, Detail: "malformed URL"}
	}
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword || u.User.Username() != "" {
			return &ValidationError{Kind: ValidationRemoteCredentials, Detail: "embedded credentials"}
		}
	}
	if u.Hostname() == "" {
		return &ValidationError{Kind: ValidationRemoteInvalid, Detail: "missing host"}
	}
	if u.Path == "" || strings.Trim(u.Path, "/") == "" {
		return &ValidationError{Kind: ValidationRemoteInvalid, Detail: "missing repository path"}
	}
	return nil
}

// validateSSHURLRemote validates ssh://[user@]host[:port]/path URLs.
func validateSSHURLRemote(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return &ValidationError{Kind: ValidationRemoteInvalid, Detail: "malformed URL"}
	}
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			return &ValidationError{Kind: ValidationRemoteCredentials, Detail: "embedded password"}
		}
	}
	if u.Hostname() == "" {
		return &ValidationError{Kind: ValidationRemoteInvalid, Detail: "missing host"}
	}
	if u.Port() != "" {
		if !isNumericPort(u.Port()) {
			return &ValidationError{Kind: ValidationRemoteInvalid, Detail: "invalid port"}
		}
	}
	if strings.Trim(u.Path, "/") == "" {
		return &ValidationError{Kind: ValidationRemoteInvalid, Detail: "missing repository path"}
	}
	return nil
}

// validateSCPRemote validates SCP-style remotes: [[user@]host:]path, where
// the optional host may be an ssh-config alias (single label or FQDN) and
// the path carries one or more nested namespace segments. A password can
// only appear as user:pass@, which is rejected.
func validateSCPRemote(raw string) error {
	hostPart := raw
	pathPart := ""
	if at := strings.Index(raw, "@"); at >= 0 {
		user := raw[:at]
		if colon := strings.Index(user, ":"); colon >= 0 {
			return &ValidationError{Kind: ValidationRemoteCredentials, Detail: "embedded password"}
		}
		if user == "" {
			return &ValidationError{Kind: ValidationRemoteInvalid, Detail: "missing user before @"}
		}
	}
	rest := raw
	if at := strings.Index(raw, "@"); at >= 0 {
		rest = raw[at+1:]
	}
	if colon := strings.Index(rest, ":"); colon >= 0 {
		hostPart = rest[:colon]
		pathPart = rest[colon+1:]
	} else {
		// No colon at all: a bare local path, not a remote.
		return &ValidationError{Kind: ValidationRemoteInvalid, Detail: "local path"}
	}
	if hostPart == "" {
		return &ValidationError{Kind: ValidationRemoteInvalid, Detail: "missing host"}
	}
	if strings.ContainsAny(hostPart, "/\\") {
		return &ValidationError{Kind: ValidationRemoteInvalid, Detail: "malformed host"}
	}
	if pathPart == "" || strings.Trim(pathPart, "/") == "" {
		return &ValidationError{Kind: ValidationRemoteInvalid, Detail: "missing repository path"}
	}
	return nil
}

func isNumericPort(port string) bool {
	if port == "" || len(port) > 5 {
		return false
	}
	for _, r := range port {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// windowsReservedNames are destination-platform names that are invalid on
// Windows filesystems. Servers may host roots on cross-platform mounts, so
// these are rejected up front.
var windowsReservedNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// ValidateDestination validates the requested destination folder name: it
// must be exactly one non-hidden child name with no separators, traversal,
// controls, option-like shapes, or destination-platform-invalid names.
func ValidateDestination(name string) error {
	if name == "" {
		return &ValidationError{Kind: ValidationDestinationInvalid, Detail: "destination is required"}
	}
	if len(name) > MaxDestinationLength {
		return &ValidationError{Kind: ValidationDestinationTooLong}
	}
	if strings.HasPrefix(name, ".") {
		return &ValidationError{Kind: ValidationDestinationInvalid, Detail: "hidden names are not allowed"}
	}
	if strings.HasPrefix(name, "-") {
		return &ValidationError{Kind: ValidationDestinationInvalid, Detail: "option-like names are not allowed"}
	}
	if strings.ContainsAny(name, "/\\") {
		return &ValidationError{Kind: ValidationDestinationInvalid, Detail: "path separators are not allowed"}
	}
	if name == "." || name == ".." || strings.Contains(name, "..") {
		return &ValidationError{Kind: ValidationDestinationInvalid, Detail: "traversal names are not allowed"}
	}
	if hasControlOrSpace(name) {
		return &ValidationError{Kind: ValidationDestinationInvalid, Detail: "control or space characters"}
	}
	if name != strings.TrimRight(name, ". ") {
		return &ValidationError{Kind: ValidationDestinationInvalid, Detail: "trailing dot or space"}
	}
	if windowsReservedNames[strings.ToLower(name)] {
		return &ValidationError{Kind: ValidationDestinationReserved}
	}
	return nil
}

// ValidateIdempotencyKey bounds the client-supplied idempotency key. Keys
// are opaque tokens: printable ASCII only, no whitespace.
func ValidateIdempotencyKey(key string) error {
	if key == "" {
		return &ValidationError{Kind: ValidationKeyInvalid, Detail: "idempotency key is required"}
	}
	if len(key) > MaxIdempotencyLength {
		return &ValidationError{Kind: ValidationKeyInvalid, Detail: "idempotency key is too long"}
	}
	for _, r := range key {
		if r < 0x21 || r > 0x7e {
			return &ValidationError{Kind: ValidationKeyInvalid, Detail: "idempotency key must be printable ASCII"}
		}
	}
	return nil
}

// SuggestDestination derives a default folder name from a validated remote:
// the last path segment with a terminal ".git" stripped. It never fabricates
// uniqueness; the server still rejects existing destinations.
func SuggestDestination(raw string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	if i := strings.IndexAny(trimmed, "?#"); i >= 0 {
		trimmed = trimmed[:i]
	}
	if segs := strings.Split(trimmed, "/"); len(segs) > 0 {
		trimmed = segs[len(segs)-1]
	}
	if at := strings.Index(trimmed, ":"); at >= 0 {
		trimmed = trimmed[at+1:]
		if segs := strings.Split(trimmed, "/"); len(segs) > 0 {
			trimmed = segs[len(segs)-1]
		}
	}
	trimmed = strings.TrimSuffix(trimmed, ".git")
	if len(trimmed) > MaxDestinationLength {
		trimmed = trimmed[:MaxDestinationLength]
	}
	return trimmed
}
