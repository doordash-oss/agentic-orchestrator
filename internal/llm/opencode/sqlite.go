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
	"database/sql"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" driver
)

const sqliteBusyTimeoutMS = 500

// sqliteDriverRunner reads the database in-process, so hosts without a
// sqlite3 binary still get OpenCode cost lookups.
type sqliteDriverRunner struct{}

func (sqliteDriverRunner) RunSQLite(ctx context.Context, dbPath, query string) ([]byte, error) {
	db, err := sql.Open("sqlite", sqliteReadOnlyDSN(dbPath))
	if err != nil {
		return nil, fmt.Errorf("open OpenCode session database: %w", err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query OpenCode session database: %w", err)
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("read OpenCode query columns: %w", err)
	}
	var out strings.Builder
	values := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range values {
		ptrs[i] = &values[i]
	}
	fields := make([]string, len(cols))
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("scan OpenCode query row: %w", err)
		}
		for i, v := range values {
			fields[i] = sqliteText(v)
		}
		out.WriteString(strings.Join(fields, "\t"))
		out.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read OpenCode query rows: %w", err)
	}
	return []byte(out.String()), nil
}

func sqliteReadOnlyDSN(dbPath string) string {
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", sqliteBusyTimeoutMS))
	return (&url.URL{Scheme: "file", Path: dbPath, RawQuery: q.Encode()}).String()
}

func sqliteText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(t)
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64)
	default:
		return fmt.Sprint(t)
	}
}
