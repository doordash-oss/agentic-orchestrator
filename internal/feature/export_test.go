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

package feature

// This file exposes a handful of unexported helpers to the external
// feature_test package. It is compiled only during testing.

// GenerateIDForTest returns a fresh random feature ID. Exported here solely
// for tests that need to fabricate Feature values directly in the store.
func GenerateIDForTest() string { return generateID() }
