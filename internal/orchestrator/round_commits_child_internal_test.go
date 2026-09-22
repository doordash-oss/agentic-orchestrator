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

import (
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// Today's ReviewFeedbackComment carries no layer identity, so the
// commented-layer resolution must default every payload — including payloads
// whose comments will span layers once identity lands — to the parent's top
// layer. commentLayerPositions is the seam Task 3's comment layer fields
// feed; this pins the fallback until then.
func TestReviewFeedbackCommentedLayer_DefaultsToTopWithoutCommentLayerIdentity(t *testing.T) {
	stack := []feature.StackLayer{
		{Position: 1, Branch: "feature/stack/1"},
		{Position: 2, Branch: "feature/stack/2"},
		{Position: 3, Branch: "feature/stack/3"},
	}
	if got := reviewFeedbackCommentedLayer(stack, nil); got != 3 {
		t.Fatalf("commentedLayer(no comments) = %d, want the top layer 3", got)
	}
	comments := []feature.ReviewFeedbackComment{
		{Repo: "repo-a", ID: 1, Type: feature.ReviewFeedbackCommentTypeReview, Path: "p3.txt"},
		{Repo: "repo-a", ID: 2, Type: feature.ReviewFeedbackCommentTypeIssue, Path: "p1.txt"},
	}
	if got := reviewFeedbackCommentedLayer(stack, comments); got != 3 {
		t.Fatalf("commentedLayer(comments without layer identity) = %d, want the top layer 3", got)
	}
}
