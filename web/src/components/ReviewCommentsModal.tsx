import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { ReviewComment } from "../api/types";

// ReviewCommentsModal pulls top-level PR comments for the feature
// (live-fetch via `gh` on the backend) and renders them grouped by
// repo. Comment bodies are rendered as plain text with newline
// preservation; full markdown rendering can wait for a follow-up.

export function ReviewCommentsModal({
  featureId,
  open,
  onClose,
}: {
  featureId: string | null;
  open: boolean;
  onClose: () => void;
}) {
  const q = useQuery({
    queryKey: ["review-comments", featureId],
    queryFn: ({ signal }) => api.reviewComments(featureId!, signal),
    enabled: !!featureId && open,
    retry: false,
  });

  if (!open || !featureId) return null;

  const comments = q.data?.comments ?? [];
  const groups = groupByRepo(comments);

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm"
      role="dialog"
      aria-modal="true"
    >
      <div className="bg-bg-secondary border border-border rounded-lg w-[min(840px,94vw)] max-h-[90vh] flex flex-col shadow-lg">
        <header className="flex items-center justify-between px-5 py-3 border-b border-border">
          <h2 className="text-sm font-semibold text-text-primary">
            Review comments · {featureId}
          </h2>
          <button
            type="button"
            onClick={onClose}
            className="text-text-tertiary hover:text-text-primary px-2"
            aria-label="Close review comments"
          >
            ✕
          </button>
        </header>
        <div className="flex-1 overflow-auto p-4 space-y-4">
          {q.isLoading && <p className="text-text-tertiary text-sm">loading…</p>}
          {q.error && (
            <p
              className="text-sm"
              style={{ color: "var(--status-error)" }}
            >
              {(q.error as Error).message}
            </p>
          )}
          {q.data && comments.length === 0 && (
            <p className="text-text-tertiary text-sm">
              No PR comments yet (or no PR has been opened).
            </p>
          )}
          {groups.map((g) => (
            <section key={g.repo} className="space-y-2">
              {g.repo && (
                <h3 className="text-xs font-mono text-text-tertiary uppercase tracking-wide">
                  {g.repo}
                </h3>
              )}
              {g.comments.map((c) => (
                <CommentCard key={`${g.repo}-${c.id}`} c={c} />
              ))}
            </section>
          ))}
        </div>
      </div>
    </div>
  );
}

function CommentCard({ c }: { c: ReviewComment }) {
  return (
    <article className="rounded-md border border-border bg-bg-tertiary p-3">
      <header className="flex items-center justify-between text-xs text-text-tertiary mb-1">
        <span>
          <strong className="text-text-primary">{c.user.login}</strong>
          {c.path && c.path !== "" && (
            <>
              {" "}
              · <span className="font-mono">{c.path}</span>
              {c.line ? `:${c.line}` : ""}
            </>
          )}
        </span>
        <time>{formatTime(c.created_at)}</time>
      </header>
      <p className="text-sm text-text-primary whitespace-pre-wrap break-words">
        {c.body}
      </p>
    </article>
  );
}

function groupByRepo(comments: ReviewComment[]): { repo: string; comments: ReviewComment[] }[] {
  const map = new Map<string, ReviewComment[]>();
  for (const c of comments) {
    const k = c.repo_name ?? "";
    const cur = map.get(k);
    if (cur) cur.push(c);
    else map.set(k, [c]);
  }
  return Array.from(map, ([repo, comments]) => ({ repo, comments }));
}

function formatTime(ts: string): string {
  if (!ts) return "";
  try {
    return new Date(ts).toLocaleString();
  } catch {
    return ts;
  }
}
