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

package opencode

import (
	"context"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func TestChildSessionCostLookupSumsRecursiveDescendants(t *testing.T) {
	t.Parallel()
	runner := &fakeSQLiteRunner{
		output: "0.75\t100\t20\t300\t4\tchild-a,child-b\n",
	}
	lookup := openCodeChildSessionCostLookup{
		dbPath: "/tmp/opencode.db",
		runner: runner,
	}

	got, err := lookup.Lookup(context.Background(), "ses_parent")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	if got.TotalCostUSD != 0.75 {
		t.Errorf("TotalCostUSD = %v, want 0.75", got.TotalCostUSD)
	}
	if got.Usage != (llm.Usage{
		InputTokens:              100,
		OutputTokens:             20,
		CacheReadInputTokens:     300,
		CacheCreationInputTokens: 4,
	}) {
		t.Errorf("Usage = %+v, want recursive child token totals", got.Usage)
	}
	if strings.Join(got.SessionIDs, ",") != "child-a,child-b" {
		t.Errorf("SessionIDs = %v, want [child-a child-b]", got.SessionIDs)
	}
	if !strings.Contains(runner.query, "WITH RECURSIVE descendants") {
		t.Errorf("query = %q, want recursive descendant CTE", runner.query)
	}
	if !strings.Contains(runner.query, "parent_id = 'ses_parent'") {
		t.Errorf("query = %q, want parent_id filter for parent session", runner.query)
	}
}

type fakeSQLiteRunner struct {
	output string
	dbPath string
	query  string
}

func (r *fakeSQLiteRunner) RunSQLite(_ context.Context, dbPath, query string) ([]byte, error) {
	r.dbPath = dbPath
	r.query = query
	return []byte(r.output), nil
}
