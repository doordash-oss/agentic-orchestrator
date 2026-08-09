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

// Package buildinfo exposes version metadata stamped into Agentic Orchestrator binaries.
package buildinfo

import (
	"fmt"
	"runtime/debug"
	"strings"
)

// version and revision are set at build time via -ldflags -X.
var version string
var revision string

// Version returns the application version from ldflags, module metadata, or a dev fallback.
func Version() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return strings.TrimPrefix(v, "v")
		}
	}
	return "dev"
}

// InjectedVersion returns the raw version supplied through build-time ldflags.
func InjectedVersion() string {
	return version
}

// Revision returns the build's VCS revision from ldflags or toolchain metadata.
func Revision() string {
	if revision != "" {
		return revision
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				return setting.Value
			}
		}
	}
	return ""
}

// VersionLine returns the stable single-line banner consumed by package verification.
func VersionLine() string {
	if rev := Revision(); rev != "" {
		return fmt.Sprintf("agentico v%s (revision %s)", Version(), rev)
	}
	return fmt.Sprintf("agentico v%s", Version())
}
