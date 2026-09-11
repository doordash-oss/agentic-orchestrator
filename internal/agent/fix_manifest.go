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

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// FixManifestFilename is the manifest's file name inside the fixer's
// iteration directory.
const FixManifestFilename = "fix-manifest.yaml"

// FixManifest is the optional artifact the Final Review fixer may write in its
// iteration directory to assign this fix round's changed files to the stack
// layer that owns them. The orchestrator's stack-aware commit path consumes
// it; files the manifest does not list belong to the top layer. YAML shape:
//
//	entries:
//	  - layer: 1
//	    repository: agentic-orchestrator
//	    paths:
//	      - internal/git/restack.go
type FixManifest struct {
	Entries []FixManifestEntry `yaml:"entries"`
}

// FixManifestEntry assigns one repository's changed files to one stack layer
// position.
type FixManifestEntry struct {
	Layer      int      `yaml:"layer"`
	Repository string   `yaml:"repository"`
	Paths      []string `yaml:"paths"`
}

// ReadFixManifest is the lenient loader for the optional fix manifest. A
// missing file is not a problem: it returns an empty manifest with an empty
// diagnostic. A malformed or unparsable file returns an empty manifest plus a
// human-readable diagnostic the caller can surface or ignore. Only a cleanly
// parsed file returns a populated manifest. Callers never handle an error
// beyond the diagnostic string.
func ReadFixManifest(path string) (FixManifest, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return FixManifest{}, ""
		}
		return FixManifest{}, fmt.Sprintf("reading fix manifest %s: %v", path, err)
	}
	var manifest FixManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return FixManifest{}, fmt.Sprintf("parsing fix manifest %s: %v", path, err)
	}
	return manifest, ""
}

// validateFixManifestArtifact binds the fix_manifest RoleSpec validator: a
// present manifest is parsed into the typed value on Outcome, and a
// semantically-malformed but YAML-parseable file is never a protocol
// violation — the manifest is optional advisory input, so the loader's
// diagnostic (if any) is the only signal and the contract stays clean.
func validateFixManifestArtifact(_ string, path string, out *Outcome) ([]ProtocolViolation, error) {
	manifest, diagnostic := ReadFixManifest(path)
	if diagnostic == "" {
		out.FixManifest = &manifest
	}
	return nil, nil
}
