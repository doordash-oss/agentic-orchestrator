// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package codex

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestConfiguredEndpointsUsesSelectedModelProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("OPENAI_BASE_URL", "https://ignored.example/v1")
	config := `model = "gpt-5"
model_provider = "gateway" # custom

[model_providers.other]
base_url = "https://other.example/v1"

[model_providers.gateway]
name = "Gateway"
base_url = "https://gateway.example.team/v1"
`
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	got := (&Provider{}).ConfiguredEndpoints()
	if want := []string{"https://gateway.example.team/v1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ConfiguredEndpoints() = %v; want %v", got, want)
	}
}

func TestConfiguredEndpointsFallsBackToOpenAIBaseURLOnly(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("OPENAI_BASE_URL", "")
	if got := (&Provider{}).ConfiguredEndpoints(); len(got) != 0 {
		t.Fatalf("ConfiguredEndpoints() = %v; want none for the default endpoint", got)
	}
	t.Setenv("OPENAI_BASE_URL", "https://proxy.example/v1")
	if got := (&Provider{}).ConfiguredEndpoints(); !reflect.DeepEqual(got, []string{"https://proxy.example/v1"}) {
		t.Fatalf("ConfiguredEndpoints() = %v; want OPENAI_BASE_URL", got)
	}
}
