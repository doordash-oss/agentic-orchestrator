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

import "strings"

const (
	testingContractBaselineName = "repo-default"
)

var defaultBaselineVerificationSteps = []VerificationStep{
	{Description: "Build succeeds", Command: "run the project build command"},
	{Description: "Static analysis / linting passes", Command: "run the project linter"},
	{Description: "Relevant tests pass", Command: "run the full test suite"},
}

func DefaultBaselineVerificationSteps() []VerificationStep {
	return append([]VerificationStep(nil), defaultBaselineVerificationSteps...)
}

func writeGenericProjectVerificationChecklist(b *strings.Builder) {
	for _, step := range DefaultBaselineVerificationSteps() {
		b.WriteString("- [ ] ")
		b.WriteString(step.Description)
		b.WriteString(": `")
		b.WriteString(step.Command)
		b.WriteString("`\n")
	}
	b.WriteString("\n")
}
