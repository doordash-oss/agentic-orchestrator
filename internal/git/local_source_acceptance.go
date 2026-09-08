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

package git

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

var ErrLocalSourceStale = errors.New("local source selection is stale")

type LocalSourceExpectation struct {
	Identity       RepoIdentity
	Mode           LocalSourceMode
	Kind           string
	Branch         string
	ObservedCommit string
}

type LocalSourceStaleError struct {
	Reason    string
	Refreshed LocalSource
}

func (e *LocalSourceStaleError) Error() string {
	if e == nil || e.Reason == "" {
		return ErrLocalSourceStale.Error()
	}
	return fmt.Sprintf("%s: %s", ErrLocalSourceStale, e.Reason)
}

func (e *LocalSourceStaleError) Unwrap() error { return ErrLocalSourceStale }

// AcceptLocalSource revalidates a displayed source against the same checkout.
// Branch tips may advance only when Git proves the displayed commit is an
// ancestor of the current tip; detached sources must remain byte-for-byte
// unchanged.
func AcceptLocalSource(ctx context.Context, repoPath string, expected LocalSourceExpectation) (LocalSource, error) {
	identity, ok := ResolveRepoIdentity(repoPath)
	if !ok || !identity.Equal(expected.Identity) {
		return LocalSource{}, &LocalSourceStaleError{Reason: "the repository checkout was replaced or moved"}
	}
	if !validFullCommit(expected.ObservedCommit) {
		return LocalSource{}, &LocalSourceStaleError{Reason: "the displayed commit is not a valid full SHA"}
	}
	current, err := InspectLocalSource(ctx, repoPath, expected.Mode)
	if err != nil {
		return LocalSource{}, &LocalSourceStaleError{Reason: err.Error()}
	}
	stale := func(reason string) (LocalSource, error) {
		return LocalSource{}, &LocalSourceStaleError{Reason: reason, Refreshed: current}
	}
	if current.Mode != expected.Mode || current.Kind != expected.Kind || current.Branch != expected.Branch {
		return stale("the selected source branch or checkout state changed")
	}
	if strings.EqualFold(current.Commit, expected.ObservedCommit) {
		return current, nil
	}
	if current.Kind == LocalSourceDetached {
		return stale("the detached checkout moved to a different commit")
	}
	if !IsAncestor(repoPath, expected.ObservedCommit, current.Commit) {
		return stale("the selected branch was rewound or rewritten")
	}
	return current, nil
}

func validFullCommit(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
