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
	"fmt"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/github"
)

// PRState reports whether the PR at prURL is still open. An empty string means
// the state could not be determined, and the accompanying error says why.
// Callers must treat the indeterminate answer as "unknown", never as "closed".
func PRState(_ string, prURL string) (string, error) {
	owner, repo, number, err := ParsePRURL(prURL)
	if err != nil {
		return "", err
	}
	client, err := github.ForHost(prURLHost(prURL))
	if err != nil {
		return "", err
	}
	info, err := client.GetPR(owner, repo, number)
	if err != nil {
		return "", err
	}
	if info.Merged {
		return PRStateMerged, nil
	}
	if strings.EqualFold(info.State, "closed") {
		return PRStateClosed, nil
	}
	if strings.EqualFold(info.State, "open") {
		return PRStateOpen, nil
	}
	return "", fmt.Errorf("unrecognised pull-request state %q", info.State)
}

// Pull-request states PRState can report.
const (
	PRStateOpen   = "open"
	PRStateClosed = "closed"
	PRStateMerged = "merged"
)
