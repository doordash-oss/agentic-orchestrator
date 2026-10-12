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

package agent

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// errProviderUnavailable marks a hard, non-transient provider failure: the
// provider endpoint refused or could not be reached, its credentials were
// rejected, or its CLI could not boot. Retrying with the same policy only
// burns time, so helpers fail fast and loops surface the feature as Failed.
var errProviderUnavailable = errors.New("provider unavailable")

// errHelperInterrupted marks a helper whose session was stopped because the
// feature was interrupted (pause-stop) or its context was cancelled. It is
// never retryable and never a verdict.
var errHelperInterrupted = fmt.Errorf("helper interrupted: %w", observe.ErrSessionInterrupted)

// providerUnavailableError carries the provider's own diagnostic so the
// failure envelope names the real cause.
type providerUnavailableError struct {
	detail string
	cause  error
}

func (e *providerUnavailableError) Error() string {
	msg := "provider unavailable (non-retryable)"
	if e.detail != "" {
		msg += ": " + e.detail
	}
	if e.cause != nil && !strings.Contains(msg, e.cause.Error()) {
		msg += " (" + e.cause.Error() + ")"
	}
	return msg
}

func (e *providerUnavailableError) Unwrap() []error {
	return []error{errProviderUnavailable, e.cause}
}

func newProviderUnavailableError(detail string, cause error) error {
	return &providerUnavailableError{detail: strings.TrimSpace(detail), cause: cause}
}

// providerFailureClass partitions provider failure text for the retry policy.
type providerFailureClass int

const (
	// providerFailureUnknown keeps the caller's existing retry behavior.
	providerFailureUnknown providerFailureClass = iota
	// providerFailureTransient is safe to retry (stream cut, rate limit,
	// timeout, connection reset).
	providerFailureTransient
	// providerFailureHard will not heal on retry (connection refused, host
	// unreachable, credentials rejected, CLI not bootable).
	providerFailureHard
)

// hardProviderFailureMarkers are matched against provider error text only
// (never against a full transcript, whose handshake advertises login methods).
var hardProviderFailureMarkers = []string{
	"cannot connect to api",
	"unable to connect",
	"connection refused",
	"econnrefused",
	"enotfound",
	"no such host",
	"network is unreachable",
	"enetunreach",
	"ehostunreach",
	"invalid api key",
	"invalid x-api-key",
	"authentication failed",
	"authentication_error",
	"not authenticated",
	"not logged in",
	"login required",
	"unauthorized",
	"executable file not found",
}

var transientProviderFailureMarkers = []string{
	"stream_incomplete",
	"rate limit",
	"rate_limit",
	"too many requests",
	"overloaded",
	"timeout",
	"timed out",
	"deadline exceeded",
	"econnreset",
	"connection reset",
	"etimedout",
	"eai_again",
	"socket hang up",
}

// classifyProviderFailure classifies provider error text. Hard markers win:
// "Cannot connect to API" after a long internal provider retry is still hard.
func classifyProviderFailure(text string) providerFailureClass {
	lower := strings.ToLower(text)
	if strings.TrimSpace(lower) == "" {
		return providerFailureUnknown
	}
	for _, marker := range hardProviderFailureMarkers {
		if strings.Contains(lower, marker) {
			return providerFailureHard
		}
	}
	for _, marker := range transientProviderFailureMarkers {
		if strings.Contains(lower, marker) {
			return providerFailureTransient
		}
	}
	return providerFailureUnknown
}

// providerFailureText collects the provider-level error text of a finished
// helper session: the returned error, the terminal error result, and the
// session's recorded error detail.
func providerFailureText(sess ports.SessionView, result *boundedHelperResult, err error) string {
	var parts []string
	if err != nil {
		parts = append(parts, err.Error())
	}
	if result != nil && result.Result != nil && (result.Result.IsError || result.Result.Subtype == "error") {
		parts = append(parts, result.Result.Result)
	}
	if sess != nil {
		parts = append(parts, sess.ErrorDetail())
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// providerFailureDetail returns the most specific provider diagnostic.
func providerFailureDetail(sess ports.SessionView, result *boundedHelperResult) string {
	if result != nil && result.Result != nil && strings.TrimSpace(result.Result.Result) != "" {
		return result.Result.Result
	}
	if sess != nil {
		return sess.ErrorDetail()
	}
	return ""
}

// classifyStartSessionError decides whether a session that failed to start
// is retryable. Missing executables and hard provider errors fail fast; a
// handshake timeout (a provider slow to boot under load) is transient.
func classifyStartSessionError(err error) providerFailureClass {
	if err == nil || errors.Is(err, ports.ErrSessionShuttingDown) {
		return providerFailureUnknown
	}
	if errors.Is(err, exec.ErrNotFound) {
		return providerFailureHard
	}
	return classifyProviderFailure(err.Error())
}

// isProviderUnavailableError reports whether err is a hard provider failure.
func isProviderUnavailableError(err error) bool {
	return errors.Is(err, errProviderUnavailable)
}

// isHelperInterruptedError reports whether err records an interrupted helper.
func isHelperInterruptedError(err error) bool {
	return errors.Is(err, errHelperInterrupted)
}
