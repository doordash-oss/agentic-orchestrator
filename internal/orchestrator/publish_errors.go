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

package orchestrator

// PublishRemoteDivergedError reports remote work that cannot safely be
// replaced by the workspace's rewritten pull-request branch.
type PublishRemoteDivergedError struct {
	RepoName          string
	Branch            string
	RemoteOnlyCommits int
}

func (e *PublishRemoteDivergedError) Error() string {
	return "pull-request branch contains remote work that is not in this workspace"
}

// PublishRemoteChangedError reports a remote branch that moved after its
// safety state was inspected for a rewritten push.
type PublishRemoteChangedError struct {
	RepoName string
	Branch   string
}

func (e *PublishRemoteChangedError) Error() string {
	return "pull-request branch changed while Agentico was publishing"
}
