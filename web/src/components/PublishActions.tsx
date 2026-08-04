import { useRef, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "../api/client";
import type { FeatureDetail } from "../api/types";
import {
  CreationProgress,
  ProgressOverlay,
  StageChipRow,
  forcePushStages,
  publishStages,
  rebaseStages,
  refactorStages,
  tweakStages,
} from "./CreationProgress";

// M6 post-CodeReady actions for a feature: publish (auto + manual),
// tweak (open + commit + finish), refactor (prompt-driven), rebase
// per repo (start + force-push after conflict).
//
// These are kept as four self-contained modals rather than a single
// catch-all to keep each interaction focused. The PublishWizard is the
// substantive one — it walks the user through commit-uncommitted →
// diff preview (via /api/features/:id/diff) → dispatch publish. The
// tweak / refactor / rebase modals are small forms that dispatch and
// rely on the WS event stream to report progress.

function formatErr(err: unknown): string {
  if (err instanceof ApiError) return `${err.status}: ${err.message}`;
  if (err instanceof Error) return err.message;
  return String(err);
}

// ---------------------------------------------------------------- Publish

type PublishStep = "review" | "running" | "done" | "manual";

export function PublishWizard({
  feature,
  open,
  onClose,
}: {
  feature: FeatureDetail;
  open: boolean;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const [step, setStep] = useState<PublishStep>("review");
  const [prURL, setPRURL] = useState("");
  const [diff, setDiff] = useState<string | null>(null);
  const publishStartedAtRef = useRef<number>(0);

  const loadDiff = useMutation({
    mutationFn: () => api.diff(feature.id),
    onSuccess: (text) => setDiff(text),
  });
  const commit = useMutation({
    mutationFn: () => api.publishCommitUncommitted(feature.id),
  });
  const publish = useMutation({
    mutationFn: () => api.publish(feature.id),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["features"] });
      await qc.invalidateQueries({ queryKey: ["feature", feature.id] });
      setStep("done");
    },
  });
  const markManual = useMutation({
    mutationFn: () => api.publishMark(feature.id, prURL.trim()),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["features"] });
      await qc.invalidateQueries({ queryKey: ["feature", feature.id] });
      setStep("done");
    },
  });

  if (!open) return null;

  return (
    <Shell title={`Publish · ${feature.name || feature.id}`} onClose={onClose}>
      {step === "review" && (
        <ReviewStep
          feature={feature}
          diff={diff}
          onLoadDiff={() => loadDiff.mutate()}
          loadingDiff={loadDiff.isPending}
          loadDiffErr={loadDiff.error}
          commitPending={commit.isPending}
          onCommit={() => commit.mutate()}
          commitDone={commit.isSuccess}
          commitErr={commit.error}
          publishPending={publish.isPending}
          onPublish={() => {
            publishStartedAtRef.current = performance.now();
            setStep("running");
            publish.mutate();
          }}
          onSwitchToManual={() => setStep("manual")}
        />
      )}
      {step === "running" && (
        <RunningStep
          error={publish.error}
          startedAt={publishStartedAtRef.current}
        />
      )}
      {step === "manual" && (
        <ManualStep
          prURL={prURL}
          setPRURL={setPRURL}
          pending={markManual.isPending}
          err={markManual.error}
          onSubmit={() => markManual.mutate()}
          onBack={() => setStep("review")}
        />
      )}
      {step === "done" && (
        <DoneStep onClose={onClose} />
      )}
    </Shell>
  );
}

function ReviewStep({
  feature,
  diff,
  onLoadDiff,
  loadingDiff,
  loadDiffErr,
  commitPending,
  onCommit,
  commitDone,
  commitErr,
  publishPending,
  onPublish,
  onSwitchToManual,
}: {
  feature: FeatureDetail;
  diff: string | null;
  onLoadDiff: () => void;
  loadingDiff: boolean;
  loadDiffErr: unknown;
  commitPending: boolean;
  onCommit: () => void;
  commitDone: boolean;
  commitErr: unknown;
  publishPending: boolean;
  onPublish: () => void;
  onSwitchToManual: () => void;
}) {
  const repos = feature.repos ?? [];
  return (
    <div className="space-y-4">
      <section>
        <SectionLabel>Repos to publish</SectionLabel>
        <ul className="space-y-1 text-sm">
          {repos.map((r) => (
            <li
              key={r.name}
              className="flex items-center justify-between gap-3 p-2 rounded-md border border-border bg-bg-tertiary"
            >
              <div>
                <span className="font-mono text-xs text-text-primary">{r.name}</span>
                {r.branch && (
                  <span className="text-xs text-text-tertiary">
                    {" "}
                    {r.branch} ← {r.base_branch ?? "?"}
                  </span>
                )}
              </div>
              <div className="flex items-center gap-2 text-xs">
                {r.pr_url && (
                  <a
                    href={r.pr_url}
                    target="_blank"
                    rel="noopener noreferrer"
                    style={{ color: "var(--accent)" }}
                  >
                    PR open ↗
                  </a>
                )}
                {r.touched && !r.pr_url && (
                  <span style={{ color: "var(--accent)" }}>touched, will publish</span>
                )}
                {!r.touched && (
                  <span className="text-text-tertiary">untouched, will skip</span>
                )}
                {r.has_error && (
                  <span style={{ color: "var(--status-error)" }}>error</span>
                )}
              </div>
            </li>
          ))}
        </ul>
      </section>

      <section>
        <div className="flex items-center justify-between mb-2">
          <SectionLabel>Step 1 · Commit uncommitted changes</SectionLabel>
          <button
            type="button"
            onClick={onCommit}
            disabled={commitPending || commitDone}
            className="px-2 py-1 text-xs rounded-sm border border-border disabled:opacity-50"
          >
            {commitPending ? "committing…" : commitDone ? "✓ committed" : "commit all"}
          </button>
        </div>
        {commitErr ? (
          <p className="text-xs" style={{ color: "var(--status-error)" }}>
            {formatErr(commitErr)}
          </p>
        ) : null}
      </section>

      <section>
        <div className="flex items-center justify-between mb-2">
          <SectionLabel>Step 2 · Review diff</SectionLabel>
          <button
            type="button"
            onClick={onLoadDiff}
            disabled={loadingDiff}
            className="px-2 py-1 text-xs rounded-sm border border-border disabled:opacity-50"
          >
            {loadingDiff ? "loading…" : diff !== null ? "reload diff" : "load diff"}
          </button>
        </div>
        {loadDiffErr ? (
          <p className="text-xs" style={{ color: "var(--status-error)" }}>
            {formatErr(loadDiffErr)}
          </p>
        ) : null}
        {diff !== null && (
          <pre className="text-[0.7rem] font-mono whitespace-pre-wrap break-words leading-snug max-h-80 overflow-auto p-2 rounded-sm bg-bg-tertiary border border-border">
            {diff || "(no diff)"}
          </pre>
        )}
      </section>

      <section className="flex justify-between items-center pt-2">
        <button
          type="button"
          onClick={onSwitchToManual}
          className="text-xs underline text-text-tertiary"
        >
          mark already-published manually instead
        </button>
        <button
          type="button"
          onClick={onPublish}
          disabled={publishPending}
          className="px-3 py-1.5 text-sm rounded-sm text-text-inverse disabled:opacity-50"
          style={{ background: "var(--accent)" }}
        >
          {publishPending ? "publishing…" : "Step 3 · publish"}
        </button>
      </section>
    </div>
  );
}

function RunningStep({
  error,
  startedAt,
}: {
  error: unknown;
  startedAt: number;
}) {
  return (
    <div className="space-y-3 text-sm">
      <CreationProgress
        heading="publishing…"
        stages={publishStages}
        currentStageIndex={0}
        startedAt={startedAt}
        failed={!!error}
        errorLabel={error ? formatErr(error) : undefined}
      />
      {!error && (
        <p className="text-text-tertiary text-xs text-center">
          Watch the activity panel for per-repo progress. This step is async;
          you can close the modal and check back.
        </p>
      )}
    </div>
  );
}

function ManualStep({
  prURL,
  setPRURL,
  pending,
  err,
  onSubmit,
  onBack,
}: {
  prURL: string;
  setPRURL: (s: string) => void;
  pending: boolean;
  err: unknown;
  onSubmit: () => void;
  onBack: () => void;
}) {
  return (
    <div className="space-y-3">
      <p className="text-sm text-text-secondary">
        If you already opened the PR outside the orchestrator, paste its URL
        below to mark the feature published.
      </p>
      <input
        type="url"
        value={prURL}
        onChange={(e) => setPRURL(e.target.value)}
        placeholder="https://github.com/org/repo/pull/123"
        className="w-full px-2 py-1.5 text-sm rounded-sm bg-bg-tertiary border border-border text-text-primary focus:outline-none focus:border-accent"
      />
      {err ? (
        <p className="text-xs" style={{ color: "var(--status-error)" }}>
          {formatErr(err)}
        </p>
      ) : null}
      <div className="flex justify-between">
        <button
          type="button"
          onClick={onBack}
          disabled={pending}
          className="px-3 py-1.5 text-sm border border-border rounded-sm text-text-secondary"
        >
          ← back
        </button>
        <button
          type="button"
          onClick={onSubmit}
          disabled={pending || prURL.trim() === ""}
          className="px-3 py-1.5 text-sm rounded-sm text-text-inverse disabled:opacity-50"
          style={{ background: "var(--accent)" }}
        >
          {pending ? "marking…" : "mark published"}
        </button>
      </div>
    </div>
  );
}

function DoneStep({ onClose }: { onClose: () => void }) {
  return (
    <div className="space-y-3">
      <p className="text-sm">Publish dispatched.</p>
      <p className="text-xs text-text-tertiary">
        The feature transitions to Published once every touched repo lands a PR.
        Live updates flow through the activity panel.
      </p>
      <div className="flex justify-end">
        <button
          type="button"
          onClick={onClose}
          className="px-3 py-1.5 text-sm rounded-sm text-text-inverse"
          style={{ background: "var(--accent)" }}
        >
          close
        </button>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------- Tweak

export function TweakDialog({
  featureId,
  open,
  onClose,
}: {
  featureId: string;
  open: boolean;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const start = useMutation({ mutationFn: () => api.tweakStart(featureId) });
  const commit = useMutation({ mutationFn: () => api.tweakCommit(featureId) });
  const finish = useMutation({
    mutationFn: (had: boolean) => api.tweakFinish(featureId, had),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["feature", featureId] });
      onClose();
    },
  });

  if (!open) return null;

  const hadChanges = commit.data?.had_changes ?? false;

  // Derive which tweak stage is the "active" focal point. Order:
  //   - finish in flight → stage 2 active
  //   - commit done      → stage 2 active (waiting for finish)
  //   - commit in flight → stage 1 active
  //   - start done       → stage 1 active (waiting for commit)
  //   - start in flight  → stage 0 active
  //   - finish success   → all done (chip row shows three ticks just
  //                        before onClose fires from finish.onSuccess)
  let tweakIdx = 0;
  if (finish.isSuccess) tweakIdx = tweakStages.length;
  else if (finish.isPending) tweakIdx = 2;
  else if (commit.isSuccess) tweakIdx = 2;
  else if (commit.isPending) tweakIdx = 1;
  else if (start.isSuccess) tweakIdx = 1;
  const tweakFailed = !!(start.error || commit.error || finish.error);

  return (
    <Shell title={`Tweak · ${featureId}`} onClose={onClose}>
      <p className="text-sm text-text-secondary">
        Open an interactive PTY session that mounts every repo's worktree. Use
        it to make small fixes; on commit the diff is gathered across all
        repos and pushed.
      </p>
      <StageChipRow
        stages={tweakStages}
        currentStageIndex={tweakIdx}
        failed={tweakFailed}
      />
      <ol className="space-y-2 text-sm">
        <li className="flex items-center justify-between gap-2">
          <span>
            <strong>1.</strong> Start session
            {start.data?.session_id && (
              <span className="ml-2 text-text-tertiary font-mono text-xs">
                {start.data.session_id}
              </span>
            )}
          </span>
          <button
            type="button"
            onClick={() => start.mutate()}
            disabled={start.isPending || start.isSuccess}
            className="px-3 py-1 text-xs rounded-sm border border-border disabled:opacity-50"
          >
            {start.isPending ? "starting…" : start.isSuccess ? "✓ started" : "start"}
          </button>
        </li>
        <li className="flex items-center justify-between gap-2">
          <span>
            <strong>2.</strong> Commit any uncommitted changes
            {commit.isSuccess && (
              <span className="ml-2 text-text-tertiary text-xs">
                {hadChanges ? "had changes" : "nothing to commit"}
              </span>
            )}
          </span>
          <button
            type="button"
            onClick={() => commit.mutate()}
            disabled={!start.isSuccess || commit.isPending || commit.isSuccess}
            className="px-3 py-1 text-xs rounded-sm border border-border disabled:opacity-50"
          >
            {commit.isPending ? "committing…" : commit.isSuccess ? "✓ committed" : "commit"}
          </button>
        </li>
        <li className="flex items-center justify-between gap-2">
          <span>
            <strong>3.</strong> Finish (push and finalise)
          </span>
          <button
            type="button"
            onClick={() => finish.mutate(hadChanges)}
            disabled={!commit.isSuccess || finish.isPending}
            className="px-3 py-1 text-xs rounded-sm text-text-inverse disabled:opacity-50"
            style={{ background: "var(--accent)" }}
          >
            {finish.isPending ? "finishing…" : "finish"}
          </button>
        </li>
      </ol>
      {(start.error || commit.error || finish.error) && (
        <p className="text-xs" style={{ color: "var(--status-error)" }}>
          {formatErr(start.error || commit.error || finish.error)}
        </p>
      )}
    </Shell>
  );
}

// ---------------------------------------------------------------- Refactor

export function RefactorDialog({
  featureId,
  repos,
  open,
  onClose,
}: {
  featureId: string;
  repos: string[];
  open: boolean;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const [prompt, setPrompt] = useState("");
  const [repoName, setRepoName] = useState<string>(repos[0] ?? "");
  const refactorStartedAtRef = useRef<number>(0);
  const start = useMutation({
    mutationFn: () => api.refactorStart(featureId, prompt, repoName || undefined),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["feature", featureId] });
      onClose();
    },
  });
  if (!open) return null;
  return (
    <Shell title={`Refactor · ${featureId}`} onClose={onClose}>
      <ProgressOverlay
        active={start.isPending}
        heading="dispatching refactor…"
        stages={refactorStages}
        currentStageIndex={0}
        startedAt={refactorStartedAtRef.current}
      />
      <p className="text-sm text-text-secondary">
        Kick off a prompt-driven refactor cycle across the feature's repos.
        Feature must be Published (post-publish polish).
      </p>
      <label className="block space-y-1">
        <span className="text-xs uppercase tracking-wide text-text-secondary">
          Repo hint (optional)
        </span>
        <select
          value={repoName}
          onChange={(e) => setRepoName(e.target.value)}
          className="w-full px-2 py-1 text-sm rounded-sm bg-bg-tertiary border border-border text-text-primary focus:outline-none focus:border-accent"
        >
          <option value="">(all)</option>
          {repos.map((r) => (
            <option key={r} value={r}>
              {r}
            </option>
          ))}
        </select>
      </label>
      <label className="block space-y-1">
        <span className="text-xs uppercase tracking-wide text-text-secondary">
          Prompt
        </span>
        <textarea
          value={prompt}
          onChange={(e) => setPrompt(e.target.value)}
          rows={4}
          placeholder="describe the refactor you want…"
          className="w-full px-2 py-1.5 text-sm rounded-sm bg-bg-tertiary border border-border text-text-primary focus:outline-none focus:border-accent font-mono"
        />
      </label>
      {start.error && (
        <p className="text-xs" style={{ color: "var(--status-error)" }}>
          {formatErr(start.error)}
        </p>
      )}
      <div className="flex justify-end gap-2">
        <button
          type="button"
          onClick={onClose}
          className="px-3 py-1.5 text-sm border border-border rounded-sm text-text-secondary"
        >
          cancel
        </button>
        <button
          type="button"
          onClick={() => {
            refactorStartedAtRef.current = performance.now();
            start.mutate();
          }}
          disabled={start.isPending || prompt.trim() === ""}
          className="px-3 py-1.5 text-sm rounded-sm text-text-inverse disabled:opacity-50"
          style={{ background: "var(--accent)" }}
        >
          {start.isPending ? "dispatching…" : "start refactor"}
        </button>
      </div>
    </Shell>
  );
}

// ---------------------------------------------------------------- Rebase

export function RebaseDialog({
  featureId,
  repos,
  open,
  onClose,
}: {
  featureId: string;
  repos: string[];
  open: boolean;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const [repoName, setRepoName] = useState<string>(repos[0] ?? "");
  const rebaseStartedAtRef = useRef<number>(0);
  const start = useMutation({
    mutationFn: () => api.rebaseStart(featureId, repoName),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["feature", featureId] });
    },
  });
  const forcePush = useMutation({
    mutationFn: () => api.rebaseForcePush(featureId, repoName),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["feature", featureId] });
      onClose();
    },
  });
  if (!open) return null;
  const overlayActive = start.isPending || forcePush.isPending;
  const overlayStages = forcePush.isPending ? forcePushStages : rebaseStages;
  const overlayHeading = forcePush.isPending ? "force-pushing…" : "rebasing…";
  return (
    <Shell title={`Rebase · ${featureId}`} onClose={onClose}>
      <ProgressOverlay
        active={overlayActive}
        heading={overlayHeading}
        stages={overlayStages}
        currentStageIndex={0}
        startedAt={rebaseStartedAtRef.current}
      />
      <p className="text-sm text-text-secondary">
        Rebase one repo's feature branch onto its base. On conflict, resolve
        in the worktree manually then click "force-push".
      </p>
      <label className="block space-y-1">
        <span className="text-xs uppercase tracking-wide text-text-secondary">
          Repo
        </span>
        <select
          value={repoName}
          onChange={(e) => setRepoName(e.target.value)}
          className="w-full px-2 py-1 text-sm rounded-sm bg-bg-tertiary border border-border text-text-primary focus:outline-none focus:border-accent"
        >
          {repos.map((r) => (
            <option key={r} value={r}>
              {r}
            </option>
          ))}
        </select>
      </label>
      {(start.error || forcePush.error) && (
        <p className="text-xs" style={{ color: "var(--status-error)" }}>
          {formatErr(start.error || forcePush.error)}
        </p>
      )}
      <div className="flex justify-between">
        <button
          type="button"
          onClick={() => {
            rebaseStartedAtRef.current = performance.now();
            forcePush.mutate();
          }}
          disabled={!repoName || forcePush.isPending}
          className="px-3 py-1.5 text-sm rounded-sm border border-border disabled:opacity-50"
          title="Run after manually resolving conflicts"
        >
          {forcePush.isPending ? "force-pushing…" : "force-push"}
        </button>
        <button
          type="button"
          onClick={() => {
            rebaseStartedAtRef.current = performance.now();
            start.mutate();
          }}
          disabled={!repoName || start.isPending}
          className="px-3 py-1.5 text-sm rounded-sm text-text-inverse disabled:opacity-50"
          style={{ background: "var(--accent)" }}
        >
          {start.isPending ? "rebasing…" : "rebase"}
        </button>
      </div>
    </Shell>
  );
}

// ---------------------------------------------------------------- shared shell

function Shell({
  title,
  onClose,
  children,
}: {
  title: string;
  onClose: () => void;
  children: React.ReactNode;
}) {
  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm"
      role="dialog"
      aria-modal="true"
    >
      <div className="relative bg-bg-secondary border border-border rounded-lg w-[min(720px,92vw)] max-h-[88vh] flex flex-col shadow-lg">
        <header className="flex items-center justify-between px-5 py-3 border-b border-border">
          <h2 className="text-sm font-semibold text-text-primary">{title}</h2>
          <button
            type="button"
            onClick={onClose}
            className="text-text-tertiary hover:text-text-primary px-2"
            aria-label="Close"
          >
            ✕
          </button>
        </header>
        <div className="flex-1 overflow-auto p-5 space-y-4">{children}</div>
      </div>
    </div>
  );
}

function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <h3 className="text-xs font-semibold uppercase tracking-wide text-text-secondary">
      {children}
    </h3>
  );
}
