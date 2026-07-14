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
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const generatedFile = "internal/server/serverapi.gen.go"

var licenseHeader = []byte(`// Copyright 2026 DoorDash, Inc.
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

`)

func main() {
	check := flag.Bool("check", false, "verify generated OpenAPI code is current")
	flag.Parse()

	root, err := repoRoot()
	if err != nil {
		fail(err)
	}
	generated, err := generate(root)
	if err != nil {
		fail(err)
	}
	outPath := filepath.Join(root, generatedFile)
	if *check {
		current, err := os.ReadFile(outPath)
		if err != nil {
			fail(fmt.Errorf("read %s: %w", generatedFile, err))
		}
		if !bytes.Equal(current, generated) {
			fail(fmt.Errorf("%s is stale; run go generate ./internal/server", generatedFile))
		}
		return
	}
	if err := os.WriteFile(outPath, generated, 0o644); err != nil {
		fail(fmt.Errorf("write %s: %w", generatedFile, err))
	}
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find go.mod from %s", dir)
		}
		dir = parent
	}
}

func generate(root string) ([]byte, error) {
	cmd := exec.Command("go", "tool", "oapi-codegen", "-config", "api/oapi-codegen.yaml", "api/openapi.yaml")
	cmd.Dir = root
	cmd.Stderr = os.Stderr
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("run oapi-codegen: %w", err)
	}
	out := make([]byte, 0, len(licenseHeader)+len(raw))
	out = append(out, licenseHeader...)
	out = append(out, raw...)
	return out, nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
