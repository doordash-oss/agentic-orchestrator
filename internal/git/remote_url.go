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

package git

import (
	"fmt"
	"net/url"
	"strings"
)

// ParseRemoteURL extracts host, owner, and repository name from a git
// remote URL in https, ssh://, or scp-like (git@host:owner/repo) form.
func ParseRemoteURL(remote string) (host, owner, repo string, err error) {
	remote = strings.TrimSuffix(strings.TrimSpace(remote), ".git")
	if strings.HasPrefix(remote, "http://") || strings.HasPrefix(remote, "https://") || strings.HasPrefix(remote, "ssh://") {
		u, parseErr := url.Parse(remote)
		if parseErr != nil {
			return "", "", "", fmt.Errorf("parsing remote URL %q: %w", remote, parseErr)
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if u.Hostname() == "" || len(parts) < 2 || parts[0] == "" || parts[1] == "" {
			return "", "", "", fmt.Errorf("remote URL %q has no host/owner/repo", remote)
		}
		return u.Hostname(), parts[len(parts)-2], parts[len(parts)-1], nil
	}
	if at := strings.Index(remote, "@"); at >= 0 {
		if colon := strings.Index(remote[at:], ":"); colon > 0 {
			host := remote[at+1 : at+colon]
			parts := strings.Split(strings.Trim(remote[at+colon+1:], "/"), "/")
			if host != "" && len(parts) >= 2 {
				return host, parts[len(parts)-2], parts[len(parts)-1], nil
			}
		}
	}
	return "", "", "", fmt.Errorf("unsupported remote URL: %q", remote)
}

// originRepo resolves the origin remote of the repo at repoPath into
// host, owner, and repository name.
func originRepo(repoPath string) (host, owner, repo string, err error) {
	cmd := readGitCmd(repoPath, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return "", "", "", fmt.Errorf("reading origin remote: %w", err)
	}
	return ParseRemoteURL(strings.TrimSpace(string(out)))
}
