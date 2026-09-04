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

package testutil

// DesignDocumentMarkdown satisfies the required Design artifact sections.
const DesignDocumentMarkdown = `# Repository management

## Problem Statement
Users cannot clone repositories from the repository picker.

## Solution
Offer cloning into a configured workspace root.

## User Stories
1. As a developer, I want to clone a repository so I can start a feature.

## Implementation Decisions
The server owns cloning and destination validation.

## Testing Decisions
Exercise cloning through the server API using isolated local repositories.

## Acceptance Criteria
Cloning a valid URL makes the new repository available in the picker.

## Out of Scope
Changing credentials through the picker.

## Further Notes
None.
`

// PhaseMarkdown supplies valid Inquiry/Design documents and titled
// placeholders for other phase-routing tests.
func PhaseMarkdown(phase string) string {
	switch phase {
	case "inquire":
		return "# Research Questions\n\n1. Which components own repository initialization?\n"
	case "design":
		return DesignDocumentMarkdown
	default:
		return "# " + phase + "\n"
	}
}
