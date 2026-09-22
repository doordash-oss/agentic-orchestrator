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
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// RemoteForeignCommit is one adoptable remote-only commit: a non-merge
// commit reachable from the remote branch tip but from neither the local
// tip nor the last SHA Agentico itself pushed.
type RemoteForeignCommit struct {
	SHA     string
	Subject string
	Author  string
}

// RemoteDivergenceReport is the divergence inspection of one layer branch:
// whether the remote tip holds commits Agentico never pushed, and which of
// those commits a rebase pass can adopt. Merge commits among the
// remote-only commits are counted (RemoteOnlyCommits includes them) but
// never offered for adoption — their content is subsumed by the replay or
// deliberately not carried.
type RemoteDivergenceReport struct {
	Diverged bool
	// RemoteTip is the observed remote branch tip the inspection ran
	// against, echoed for the caller's record.
	RemoteTip string
	// ForeignCommits lists the adoptable non-merge remote-only commits,
	// oldest first.
	ForeignCommits []RemoteForeignCommit
	// RemoteOnlyCommits counts every commit reachable from the remote tip
	// but from neither the local tip nor the last-pushed SHA, including
	// the merge commits excluded from ForeignCommits.
	RemoteOnlyCommits int
}

// InspectRemoteDivergence reports whether a layer branch's remote tip
// diverges from the local chain: the remote tip exists, differs from the
// last SHA Agentico pushed, and is not an ancestor of the local tip. The
// remote-only commits are those reachable from the remote tip but from
// neither the local tip nor the last-pushed SHA; excluding the last-pushed
// SHA keeps a local rewrite that was never republished from being mistaken
// for reviewer work. An empty remote tip (an absent remote branch) and an
// empty local tip both read as not diverged. All object-graph reads run
// against the real graph with replace refs and grafts disabled, matching
// the push lease's own proof commands.
func InspectRemoteDivergence(repoPath, remoteTip, localTip, lastPushedSHA string) (*RemoteDivergenceReport, error) {
	report := &RemoteDivergenceReport{RemoteTip: remoteTip}
	if remoteTip == "" || localTip == "" {
		return report, nil
	}
	if remoteTip == lastPushedSHA {
		// The remote holds exactly what Agentico pushed.
		return report, nil
	}
	remoteIsAncestor, err := divergenceIsAncestor(repoPath, remoteTip, localTip)
	if err != nil {
		return nil, err
	}
	if remoteIsAncestor {
		// The local chain already contains the remote's work.
		return report, nil
	}
	report.Diverged = true

	args := []string{"rev-list", "--reverse", remoteTip, "^" + localTip}
	if lastPushedSHA != "" {
		args = append(args, "^"+lastPushedSHA)
	}
	out, err := divergenceProofOutput(repoPath, "enumerating remote-only commits", args...)
	if err != nil {
		return nil, err
	}
	remoteOnly := strings.Fields(string(out))
	report.RemoteOnlyCommits = len(remoteOnly)
	for _, sha := range remoteOnly {
		merge, err := divergenceIsMerge(repoPath, sha)
		if err != nil {
			return nil, err
		}
		if merge {
			continue
		}
		subject, author, err := divergenceCommitIdentity(repoPath, sha)
		if err != nil {
			return nil, err
		}
		report.ForeignCommits = append(report.ForeignCommits, RemoteForeignCommit{
			SHA:     sha,
			Subject: subject,
			Author:  author,
		})
	}
	return report, nil
}

// divergenceIsMerge reports whether the commit has two or more parents.
func divergenceIsMerge(repoPath, commitSHA string) (bool, error) {
	out, err := divergenceProofOutput(repoPath, "reading remote-only commit parents",
		"rev-list", "--parents", "-n", "1", commitSHA)
	if err != nil {
		return false, err
	}
	return len(strings.Fields(string(out))) >= 3, nil
}

// divergenceCommitIdentity reads one commit's subject and author identity.
func divergenceCommitIdentity(repoPath, commitSHA string) (subject, author string, err error) {
	out, err := divergenceProofOutput(repoPath, "reading remote-only commit identity",
		"show", "-s", "--format=%s%x1f%an <%ae>", commitSHA)
	if err != nil {
		return "", "", err
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "\x1f", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("reading remote-only commit identity: unexpected format for %s", commitSHA)
	}
	return parts[0], parts[1], nil
}

// divergenceIsAncestor and divergenceProofOutput mirror the rewritten-push
// proof commands: the real object graph, replace refs and grafts disabled,
// so a local overlay can never make reviewer work invisible.
func divergenceIsAncestor(repoPath, ancestor, descendant string) (bool, error) {
	cmd := rewritePushProofCommand(repoPath, "merge-base", "--is-ancestor", ancestor, descendant)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git merge-base --is-ancestor: %w", err)
}

func divergenceProofOutput(repoPath, operation string, args ...string) ([]byte, error) {
	cmd := rewritePushProofCommand(repoPath, args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
	}
	return out, nil
}
