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

import "net/url"

// redactedRequestURL strips the access_token query parameter from a raw
// request URL. SSE routes accept access_token as a query-string bearer
// token fallback (see sseTokenFallbackAllowed) because EventSource clients
// cannot set an Authorization header; any future request/access logging
// MUST pass request URLs through this function first so the token is never
// written to a log, trace span, or crash report.
func redactedRequestURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if !u.Query().Has("access_token") {
		return raw
	}
	q := u.Query()
	q.Del("access_token")
	u.RawQuery = q.Encode()
	return u.String()
}
