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

package tui

import "fmt"

// DescriptionChatContext holds the snapshot of PR description state used to
// prime the description-refinement chat session.
type DescriptionChatContext struct {
	FeatureID    string
	RepoName     string
	CurrentTitle string
	CurrentBody  string
	DiffSummary  string
}

// OpenDescriptionChatMsg is emitted by PublishModel when the user presses the
// refine key in the PR description step. It carries the snapshot context needed
// to start the description chat.
type OpenDescriptionChatMsg struct {
	ctx DescriptionChatContext
}

// PublishDescriptionUpdatedMsg is emitted by the description chat when the AI
// calls the UpdatePRDescription tool. It carries the new title and body.
type PublishDescriptionUpdatedMsg struct {
	title string
	body  string
}

// DescriptionChatExitMsg signals the description chat view should close without
// applying changes.
type DescriptionChatExitMsg struct{}

// buildDescriptionChatSystemPrompt produces a system prompt that primes the AI
// to refine a GitHub PR title and body based on the snapshotted context.
func buildDescriptionChatSystemPrompt(ctx DescriptionChatContext) string {
	return fmt.Sprintf(`You are an AI assistant helping to refine a GitHub Pull Request title and body.

Context:
- Feature ID: %s
- Repo Name: %s
- Current Title: %s
- Current Body:
%s
- Diff Summary:
%s

Your task is to help the user improve the PR title and body. You may ask clarifying questions or suggest improvements.

When you have a complete, ready-to-apply revised description, call the UpdatePRDescription tool with the new "title" and "body" arguments. Calling this tool applies the change and ends the refinement session, so only call it when the proposal is complete.

The user may ask for multiple rounds of revision before you call the tool. Converse naturally and helpfully until they are satisfied.`,
		ctx.FeatureID,
		ctx.RepoName,
		ctx.CurrentTitle,
		ctx.CurrentBody,
		ctx.DiffSummary,
	)
}
