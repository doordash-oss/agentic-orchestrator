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

package server

import "runtime/debug"

const (
	// CompatibilitySchemaVersion is the monotonic series number of the REST
	// schema contract within the current API major. Bump it whenever the
	// wire schema changes in a way existing clients cannot tolerate.
	CompatibilitySchemaVersion = 1

	// CompatibilityMinClientSchema is the minimum client schema series a
	// connecting client must implement for this server to consider it
	// compatible.
	CompatibilityMinClientSchema = 1

	// CompatibilityRuntimePolicy names the loopback runtime security/ownership
	// policy contract: loopback-only listener, bearer-token auth, single-owner
	// discovery record.
	CompatibilityRuntimePolicy = "loopback-bearer-v1"

	// CompatibilityNetworkRuntimePolicy names the network runtime policy: a
	// non-loopback listener under the same bearer-token auth, with any Host
	// header accepted and the Origin rule unchanged from the loopback policy.
	CompatibilityNetworkRuntimePolicy = "network-bearer-v1"
)

// NewCompatibilityDeclaration builds the explicit compatibility contract
// served on /api/v1/health. buildVersion is the server build's version
// string (the instance-lock owner version); an empty value falls back to
// "dev" so the declaration always carries a non-empty build identity.
// runtimePolicy is the bind-mode policy from the resolved listen address;
// an empty value falls back to the loopback policy.
func NewCompatibilityDeclaration(buildVersion, runtimePolicy string) CompatibilityDeclaration {
	if buildVersion == "" {
		buildVersion = "dev"
	}
	if runtimePolicy == "" {
		runtimePolicy = CompatibilityRuntimePolicy
	}
	return CompatibilityDeclaration{
		APIVersion:      APIVersion,
		SchemaVersion:   CompatibilitySchemaVersion,
		MinClientSchema: CompatibilityMinClientSchema,
		RuntimePolicy:   runtimePolicy,
		ServerBuild: BuildIdentity{
			Version:  buildVersion,
			Revision: buildRevision(),
		},
	}
}

// buildRevision reports the VCS revision stamped into the binary by the Go
// toolchain, or "" when unavailable (e.g. tests, go run).
func buildRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return setting.Value
		}
	}
	return ""
}
