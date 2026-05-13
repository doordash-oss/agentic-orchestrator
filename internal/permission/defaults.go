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

package permission

// defaultGlobalRules returns the curated built-in global permission rules
// materialized for fresh environments.
//
// The list is intentionally read-only: it includes inspection-oriented shell
// commands and excludes mutating, build, test, and write-capable operations.
func defaultGlobalRules() []Rule {
	return []Rule{
		{ToolPattern: "Bash(ls *)", Effect: "allow"},
		{ToolPattern: "Bash(pwd *)", Effect: "allow"},
		{ToolPattern: "Bash(cat *)", Effect: "allow"},
		{ToolPattern: "Bash(head *)", Effect: "allow"},
		{ToolPattern: "Bash(tail *)", Effect: "allow"},
		{ToolPattern: "Bash(wc *)", Effect: "allow"},
		{ToolPattern: "Bash(grep *)", Effect: "allow"},
		{ToolPattern: "Bash(rg *)", Effect: "allow"},
		{ToolPattern: "Bash(stat *)", Effect: "allow"},
		{ToolPattern: "Bash(file *)", Effect: "allow"},
		{ToolPattern: "Bash(find *)", Effect: "allow"},
		{ToolPattern: "Bash(du *)", Effect: "allow"},
		{ToolPattern: "Bash(git status *)", Effect: "allow"},
		{ToolPattern: "Bash(git diff *)", Effect: "allow"},
		{ToolPattern: "Bash(git log *)", Effect: "allow"},
		{ToolPattern: "Bash(git show *)", Effect: "allow"},
	}
}
