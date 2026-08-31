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

package main

import (
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/github"
	"github.com/rogpeppe/go-internal/testscript"
)

func TestMain(m *testing.M) {
	// Default-deny: tests must never reach the real GitHub API; fixtures stack on top of this default.
	github.OverrideForTest("http://127.0.0.1:1", "test-dead")
	testscript.Main(m, map[string]func(){
		"agentico": run,
	})
}

func TestCLIScripts(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI black-box tests in short mode")
	}
	testscript.Run(t, testscript.Params{
		Dir: "testdata/scripts",
		Setup: func(env *testscript.Env) error {
			// Set HOME to $WORK so config files don't pollute real home
			env.Setenv("HOME", env.WorkDir)
			return nil
		},
	})
}
