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

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var expectedTUISmokeTests = []string{
	"TestConcurrentPlainAgenticoColdStart",
	"TestConcurrentPlainAgenticoStaleDiscoveryRepair",
	"TestDefaultCommandLaunchesAPIBackedTUI",
	"TestLauncherFailureClassification",
	"TestPlainAgenticoCLIRouting",
	"TestPlainAgenticoReusePolicyMismatch",
	"TestPreCutoverRuntimeCompatibility",
	"TestTUIReconnectSnapshotRecovery",
}

func TestDefaultCommandLaunchesAPIBackedTUI(t *testing.T) {
	runRepoGoTest(t, "./cmd/agentico", "^TestRunArgsLaunchesClientServerByDefault$")
}

func TestConcurrentPlainAgenticoColdStart(t *testing.T) {
	runRepoGoTest(t, "./cmd/agentico", "^TestDefaultLaunchConcurrentColdStartStartsOneOwnedServer$")
}

func TestConcurrentPlainAgenticoStaleDiscoveryRepair(t *testing.T) {
	runRepoGoTest(t, "./cmd/agentico", "^TestDefaultLaunchConcurrentStaleRepairStartsOneOwnedServer$")
}

func TestLauncherFailureClassification(t *testing.T) {
	runRepoGoTest(t, "./cmd/agentico", "^Test(DefaultLaunchReportsServerReadinessTimeout|ServerBootstrapRejectsHeldInstanceLock)$")
}

func TestPlainAgenticoReusePolicyMismatch(t *testing.T) {
	runRepoGoTest(t, "./internal/server", "^TestPrepareDiscoveryRequiresPolicyEquivalentHealthyServer$")
}

func TestTUIReconnectSnapshotRecovery(t *testing.T) {
	runRepoGoTest(t, "./internal/tui", "^TestAPIAppModelReconnectSnapshotRecoveryPreservesSelection$")
}

func TestPlainAgenticoCLIRouting(t *testing.T) {
	runRepoGoTest(t, "./cmd/agentico", "^Test(RunArgsLaunchesClientServerByDefault|RunArgsPassesRetainedLaunchFlags|RunArgsDispatchesServerToSeam|ParseLaunchArgsServerSurface|ParseLaunchArgsRejectsRemovedSurface)$")
}

func TestPreCutoverRuntimeCompatibility(t *testing.T) {
	runRepoGoTest(t, "./cmd/agentico", "^TestPickRuntimeParent$")
}

func TestSmokeScriptDoesNotUseRemovedFeatureCommands(t *testing.T) {
	body, err := os.ReadFile("smoke.sh")
	if err != nil {
		t.Fatalf("ReadFile(smoke.sh): %v", err)
	}
	script := string(body)
	for _, banned := range []string{
		" feature list",
		" feature create",
		"agentico run",
		"--refresh-models",
	} {
		if strings.Contains(script, banned) {
			t.Fatalf("smoke.sh still contains removed command-era surface %q", banned)
		}
	}

	targets := e2eSmokeRunTargets(t, script)
	for _, want := range []string{
		"TestDefaultCommandLaunchesAPIBackedTUI",
		"TestTUIReconnectSnapshotRecovery",
	} {
		if !targets[want] {
			t.Fatalf("smoke.sh must run %s", want)
		}
	}

	existing := e2eTestNames(t)
	for target := range targets {
		if !existing[target] {
			t.Fatalf("smoke.sh runs %s, but no such e2e test exists", target)
		}
	}

	for _, want := range expectedTUISmokeTests {
		if !existing[want] {
			t.Fatalf("TUI smoke inventory missing %s", want)
		}
	}
	var got []string
	for name := range existing {
		if name == "TestSmokeScriptDoesNotUseRemovedFeatureCommands" {
			continue
		}
		got = append(got, name)
	}
	sort.Strings(got)
	want := append([]string(nil), expectedTUISmokeTests...)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("TUI smoke inventory drift\n got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func e2eSmokeRunTargets(t *testing.T, script string) map[string]bool {
	t.Helper()
	re := regexp.MustCompile(`go test \./test/e2e -run '\^([A-Za-z0-9_]+)\$'`)
	matches := re.FindAllStringSubmatch(script, -1)
	if len(matches) == 0 {
		t.Fatal("smoke.sh must run at least one exact e2e Go test")
	}
	targets := make(map[string]bool, len(matches))
	for _, match := range matches {
		targets[match[1]] = true
	}
	return targets
}

func runRepoGoTest(t *testing.T, pkg, run string) {
	t.Helper()
	cmd := exec.Command("go", "test", pkg, "-run", run, "-count=1")
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go test %s -run %s failed: %v\n%s", pkg, run, err, out)
	}
	if strings.Contains(string(out), "[no tests to run]") {
		t.Fatalf("go test %s -run %s matched no tests:\n%s", pkg, run, out)
	}
}

func e2eTestNames(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("Glob e2e Go files: %v", err)
	}
	re := regexp.MustCompile(`func (Test[A-Za-z0-9_]+)\(`)
	names := make(map[string]bool)
	for _, path := range files {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", path, err)
		}
		for _, match := range re.FindAllStringSubmatch(string(body), -1) {
			names[match[1]] = true
		}
	}
	return names
}
