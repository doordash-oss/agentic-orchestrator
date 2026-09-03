/*
Copyright 2026 DoorDash, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

/**
 * On-demand aftercare evidence: the per-repository diff and the pull-request
 * review feedback the "What shipped" group reads. Both are fetched once per
 * aftercare mount, never blocking the runway, and cached against the feature's
 * polling refresh — the effect keys on the repository *names*, not the
 * snapshot, so a poll cycle that hands back a new snapshot object cannot
 * restart the fetches. Every failure degrades to omission: a rejected fetch
 * simply leaves its datum out.
 *
 * The review-feedback fetch now returns the server-owned pending draft view:
 * the same repo grouping as before, plus a `revision` and per-comment
 * `stableRef`/`selected`/`createdAt`. The receipt grouping only reads
 * `repos[].repo` and `repos[].comments`, so it is shape-compatible both ways;
 * the draft fields pass through unused here.
 */
import { useEffect, useRef, useState } from 'react';
import type { FetchReviewFeedbackResult, RepositoryDiffResult } from '../../../shared/ipc';

export interface AftercareEvidence {
  /** Successful repository diffs only; a failed or empty repo is absent. */
  diffs: RepositoryDiffResult[];
  /** Null until the fetch resolves, and after a failed fetch. */
  reviewFeedback: FetchReviewFeedbackResult | null;
}

export const EMPTY_AFTERCARE_EVIDENCE: AftercareEvidence = { diffs: [], reviewFeedback: null };

export function useAftercareEvidence(
  featureId: string,
  repos: readonly string[],
  hasPullRequest: boolean,
  enabled: boolean,
): AftercareEvidence {
  const [evidence, setEvidence] = useState<AftercareEvidence>(EMPTY_AFTERCARE_EVIDENCE);
  // The effect depends on the repository *names*, not the array identity a poll
  // cycle hands back; the ref carries the array itself into the fetch. NUL is the
  // delimiter because it cannot occur inside a repository name, so no set of names
  // can forge the key of a different set.
  const repoKey = repos.join('\u0000');
  const reposRef = useRef(repos);
  reposRef.current = repos;

  useEffect(() => {
    if (!enabled || repoKey === '') {
      setEvidence(EMPTY_AFTERCARE_EVIDENCE);
      return;
    }
    let current = true;
    setEvidence(EMPTY_AFTERCARE_EVIDENCE);
    void Promise.all(
      reposRef.current.map((repo) =>
        window.agentico.getRepositoryDiff({ featureId, repo }).catch(() => null),
      ),
    ).then((results) => {
      if (!current) return;
      const diffs = results.filter((result): result is RepositoryDiffResult => result !== null);
      setEvidence((previous) => ({ ...previous, diffs }));
    });
    if (hasPullRequest) {
      void window.agentico
        .fetchReviewFeedback({ featureId })
        .then((result) => {
          if (current) setEvidence((previous) => ({ ...previous, reviewFeedback: result }));
        })
        .catch(() => undefined);
    }
    return () => {
      current = false;
    };
  }, [enabled, featureId, hasPullRequest, repoKey]);

  return evidence;
}
