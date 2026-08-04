import { useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "../api/client";
import { useUI } from "../store/ui";
import type { FeatureDetail as Detail } from "../api/types";
import { ConfirmDeleteModal } from "./ConfirmDeleteModal";
import { ArtifactReviewModal } from "./ArtifactReviewModal";
import { AttachDrawer } from "./AttachDrawer";
import { useKeyAction } from "./KeymapProvider";
import { LogsViewer } from "./LogsViewer";
import {
  PublishWizard,
  RebaseDialog,
  RefactorDialog,
  TweakDialog,
} from "./PublishActions";
import { ReviewCommentsModal } from "./ReviewCommentsModal";
import { RewindMenu } from "./RewindMenu";

// Centre panel. Shows the selected feature's identity, lifecycle,
// repos, phase timings, sessions, and pending queues. Switches to an
// empty state when no feature is selected.
export function FeatureDetail() {
  const id = useUI((s) => s.selectedFeatureId);

  const { data, isLoading, error } = useQuery({
    queryKey: ["feature", id],
    queryFn: ({ signal }) => api.featureDetail(id!, signal),
    enabled: !!id,
    refetchInterval: 5_000,
  });

  if (!id) return <EmptyState />;
  if (isLoading) {
    return (
      <Panel>
        <p className="text-text-tertiary text-sm">loading…</p>
      </Panel>
    );
  }
  if (error) {
    return (
      <Panel>
        <p className="text-sm" style={{ color: "var(--status-error)" }}>
          failed to load feature: {(error as Error).message}
        </p>
      </Panel>
    );
  }
  if (!data) return <EmptyState />;

  return <FeatureDetailLoaded data={data} featureId={id!} />;
}

type DetailTab = "overview" | "sessions" | "costs";

function FeatureDetailLoaded({
  data,
  featureId,
}: {
  data: Detail;
  featureId: string;
}) {
  const [logsOpen, setLogsOpen] = useState(false);
  const [commentsOpen, setCommentsOpen] = useState(false);
  const [reviewOpen, setReviewOpen] = useState(false);
  const [rewindOpen, setRewindOpen] = useState(false);
  const [publishOpen, setPublishOpen] = useState(false);
  const [tweakOpen, setTweakOpen] = useState(false);
  const [refactorOpen, setRefactorOpen] = useState(false);
  const [rebaseOpen, setRebaseOpen] = useState(false);
  const [attachSession, setAttachSession] = useState<string | null>(null);
  const [tab, setTab] = useState<DetailTab>("overview");
  const [confirmDeleteOpen, setConfirmDeleteOpen] = useState(false);

  const qc = useQueryClient();
  const setSelected = useUI((s) => s.selectFeature);
  const deleteStartedAtRef = useRef<number>(0);

  const stopMut = useMutation({
    mutationFn: () => api.stopFeature(featureId),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["features"] });
      await qc.invalidateQueries({ queryKey: ["feature", featureId] });
    },
  });
  const deleteMut = useMutation({
    mutationFn: () => api.deleteFeature(featureId),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["features"] });
      // Clear the selection so the detail panel falls back to the empty
      // state — the feature no longer exists.
      setSelected(null);
      setConfirmDeleteOpen(false);
    },
  });
  const retryMut = useMutation({
    mutationFn: () => api.retryFeature(featureId),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["features"] });
      await qc.invalidateQueries({ queryKey: ["feature", featureId] });
    },
  });

  const pendingReview = data.needs_review;
  const repoNames = (data.repos ?? []).map((r) => r.name);
  const sessionsCount = data.sessions?.length ?? 0;
  const queuesPending =
    (data.help_queue ?? []).filter((h) => h.pending).length +
    (data.permissions_queue ?? []).filter((p) => p.pending).length;
  const sessionsBadge = sessionsCount + queuesPending;
  const costsCount =
    Object.keys(data.phase_timings_ms ?? {}).length +
    Object.keys(data.phase_costs ?? {}).length;

  const triggerStop = () => {
    if (!data.is_running || stopMut.isPending) return;
    stopMut.mutate();
  };
  const triggerDelete = () => {
    if (deleteMut.isPending) return;
    setConfirmDeleteOpen(true);
  };

  // Keyboard shortcuts available while a feature is selected.
  useKeyAction("logs", () => setLogsOpen(true));
  useKeyAction("reviewComments", () => setCommentsOpen(true));
  useKeyAction("artifactReview", () => setReviewOpen(true));
  useKeyAction("rewind", () => setRewindOpen(true));
  useKeyAction("publish", () => setPublishOpen(true));
  useKeyAction("stopFeature", triggerStop);
  useKeyAction("deleteFeature", triggerDelete);
  // Escape closes the topmost modal, prioritising the most-recent.
  useKeyAction("closeTop", () => {
    if (confirmDeleteOpen) return setConfirmDeleteOpen(false);
    if (rebaseOpen) return setRebaseOpen(false);
    if (refactorOpen) return setRefactorOpen(false);
    if (tweakOpen) return setTweakOpen(false);
    if (publishOpen) return setPublishOpen(false);
    if (rewindOpen) return setRewindOpen(false);
    if (reviewOpen) return setReviewOpen(false);
    if (commentsOpen) return setCommentsOpen(false);
    if (logsOpen) return setLogsOpen(false);
    if (attachSession !== null) return setAttachSession(null);
  });
  return (
    <>
      <Panel>
        <Header f={data} />
        <Actions
          onLogs={() => setLogsOpen(true)}
          onComments={() => setCommentsOpen(true)}
          onReview={() => setReviewOpen(true)}
          onRewind={() => setRewindOpen(true)}
          onPublish={() => setPublishOpen(true)}
          onTweak={() => setTweakOpen(true)}
          onRefactor={() => setRefactorOpen(true)}
          onRebase={() => setRebaseOpen(true)}
          onRetry={() => retryMut.mutate()}
          onStop={triggerStop}
          onDelete={triggerDelete}
          isRunning={data.is_running}
          stopPending={stopMut.isPending}
          retryPending={retryMut.isPending}
          needsReview={pendingReview}
          status={data.status}
        />
        <TabStrip
          tab={tab}
          onTab={setTab}
          sessionsBadge={sessionsBadge}
          costsBadge={costsCount}
        />
        {tab === "overview" && (
          <>
            <FailureBanner f={data} retryPending={retryMut.isPending} onRetry={() => retryMut.mutate()} />
            <NeedUserInputPanel f={data} featureId={featureId} />
            <Lifecycle f={data} />
            <Repos f={data} />
            <Description f={data} />
          </>
        )}
        {tab === "sessions" && (
          <>
            <Queues f={data} />
            <Sessions f={data} onAttach={setAttachSession} />
          </>
        )}
        {tab === "costs" && (
          <>
            <PhaseTimings f={data} />
            <PhaseCosts f={data} />
          </>
        )}
      </Panel>
      <LogsViewer
        featureId={featureId}
        open={logsOpen}
        onClose={() => setLogsOpen(false)}
      />
      <ReviewCommentsModal
        featureId={featureId}
        open={commentsOpen}
        onClose={() => setCommentsOpen(false)}
      />
      <ArtifactReviewModal
        featureId={featureId}
        open={reviewOpen}
        onClose={() => setReviewOpen(false)}
        pendingReviewPhase={data.current_phase ?? ""}
      />
      <RewindMenu
        featureId={featureId}
        open={rewindOpen}
        onClose={() => setRewindOpen(false)}
      />
      <AttachDrawer
        sessionId={attachSession}
        featureId={featureId}
        open={attachSession !== null}
        onClose={() => setAttachSession(null)}
      />
      <PublishWizard
        feature={data}
        open={publishOpen}
        onClose={() => setPublishOpen(false)}
      />
      <TweakDialog
        featureId={featureId}
        open={tweakOpen}
        onClose={() => setTweakOpen(false)}
      />
      <RefactorDialog
        featureId={featureId}
        repos={repoNames}
        open={refactorOpen}
        onClose={() => setRefactorOpen(false)}
      />
      <RebaseDialog
        featureId={featureId}
        repos={repoNames}
        open={rebaseOpen}
        onClose={() => setRebaseOpen(false)}
      />
      <ConfirmDeleteModal
        featureName={data.name || featureId}
        open={confirmDeleteOpen}
        isRunning={data.is_running}
        pending={deleteMut.isPending}
        error={
          deleteMut.error instanceof ApiError
            ? deleteMut.error.message
            : deleteMut.error instanceof Error
              ? deleteMut.error.message
              : null
        }
        onCancel={() => setConfirmDeleteOpen(false)}
        onConfirm={() => {
          deleteStartedAtRef.current = performance.now();
          deleteMut.mutate();
        }}
        startedAt={deleteStartedAtRef.current}
      />
    </>
  );
}

function Actions({
  onLogs,
  onComments,
  onReview,
  onRewind,
  onPublish,
  onTweak,
  onRefactor,
  onRebase,
  onRetry,
  onStop,
  onDelete,
  isRunning,
  stopPending,
  retryPending,
  needsReview,
  status,
}: {
  onLogs: () => void;
  onComments: () => void;
  onReview: () => void;
  onRewind: () => void;
  onPublish: () => void;
  onTweak: () => void;
  onRefactor: () => void;
  onRebase: () => void;
  onRetry: () => void;
  onStop: () => void;
  onDelete: () => void;
  isRunning: boolean;
  stopPending: boolean;
  retryPending: boolean;
  needsReview: boolean;
  status: string;
}) {
  // Publish is offered as soon as the feature can land code (CodeReady
  // or beyond); tweak/refactor/rebase are post-publish polish.
  const canPublish = [
    "CodeReady",
    "ReviewPassed",
    "ImplementReady",
    "Published",
  ].includes(status);
  const isPublished = status === "Published";

  return (
    <div className="flex flex-wrap gap-2">
      <button
        type="button"
        onClick={onLogs}
        className="px-2 py-1 text-xs rounded-sm border border-border text-text-secondary hover:bg-bg-tertiary"
        title="View phase logs (l)"
      >
        logs
      </button>
      <button
        type="button"
        onClick={onComments}
        className="px-2 py-1 text-xs rounded-sm border border-border text-text-secondary hover:bg-bg-tertiary"
        title="View PR review comments (g)"
      >
        review comments
      </button>
      <button
        type="button"
        onClick={onReview}
        className="px-2 py-1 text-xs rounded-sm border text-text-inverse"
        style={
          needsReview
            ? {
                background: "var(--banner-warning-icon)",
                borderColor: "var(--banner-warning-border)",
              }
            : {
                background: "var(--bg-tertiary)",
                borderColor: "var(--border-color)",
                color: "var(--text-secondary)",
              }
        }
        title="Review the current artifact"
      >
        {needsReview ? "review now" : "artifacts"}
      </button>
      <button
        type="button"
        onClick={onRewind}
        className="px-2 py-1 text-xs rounded-sm border border-border text-text-secondary hover:bg-bg-tertiary"
        title="Rewind to an earlier phase"
      >
        rewind…
      </button>
      {canPublish && (
        <button
          type="button"
          onClick={onPublish}
          className="px-2 py-1 text-xs rounded-sm text-text-inverse"
          style={{ background: "var(--accent)" }}
          title="Publish (open the multi-step wizard)"
        >
          publish…
        </button>
      )}
      {isPublished && (
        <>
          <button
            type="button"
            onClick={onTweak}
            className="px-2 py-1 text-xs rounded-sm border border-border text-text-secondary hover:bg-bg-tertiary"
            title="Open an interactive tweak session"
          >
            tweak…
          </button>
          <button
            type="button"
            onClick={onRefactor}
            className="px-2 py-1 text-xs rounded-sm border border-border text-text-secondary hover:bg-bg-tertiary"
            title="Dispatch a prompt-driven refactor"
          >
            refactor…
          </button>
          <button
            type="button"
            onClick={onRebase}
            className="px-2 py-1 text-xs rounded-sm border border-border text-text-secondary hover:bg-bg-tertiary"
            title="Rebase a repo's feature branch"
          >
            rebase…
          </button>
        </>
      )}
      <span className="flex-1" />
      {status === "Failed" && (
        <button
          type="button"
          onClick={onRetry}
          disabled={retryPending}
          className="px-2 py-1 text-xs rounded-sm text-text-inverse disabled:opacity-50"
          style={{ background: "var(--accent)" }}
          title="Retry the failed feature setup"
        >
          {retryPending ? "retrying…" : "retry"}
        </button>
      )}
      {isRunning && (
        <button
          type="button"
          onClick={onStop}
          disabled={stopPending}
          className="px-2 py-1 text-xs rounded-sm border text-text-secondary hover:bg-bg-tertiary disabled:opacity-50"
          style={{
            borderColor: "color-mix(in srgb, var(--status-warning) 40%, transparent)",
            color: "var(--status-warning)",
          }}
          title="Stop the feature's running sessions (s)"
        >
          {stopPending ? "stopping…" : "■ stop"}
        </button>
      )}
      <button
        type="button"
        onClick={onDelete}
        className="px-2 py-1 text-xs rounded-sm border hover:bg-bg-tertiary"
        style={{
          borderColor: "color-mix(in srgb, var(--status-error) 40%, transparent)",
          color: "var(--status-error)",
        }}
        title="Delete this feature (d)"
      >
        ✕ delete
      </button>
    </div>
  );
}

function TabStrip({
  tab,
  onTab,
  sessionsBadge,
  costsBadge,
}: {
  tab: DetailTab;
  onTab: (t: DetailTab) => void;
  sessionsBadge: number;
  costsBadge: number;
}) {
  const tabs: { key: DetailTab; label: string; badge?: number }[] = [
    { key: "overview", label: "Overview" },
    { key: "sessions", label: "Sessions", badge: sessionsBadge },
    { key: "costs", label: "Costs & Timings", badge: costsBadge },
  ];
  return (
    <nav className="tab-strip" role="tablist">
      {tabs.map((t) => (
        <button
          key={t.key}
          type="button"
          role="tab"
          aria-selected={tab === t.key}
          onClick={() => onTab(t.key)}
          className={`tab-strip__tab ${
            tab === t.key ? "tab-strip__tab--active" : ""
          }`}
        >
          {t.label}
          {typeof t.badge === "number" && t.badge > 0 && (
            <span className="ml-1.5 text-[0.65rem] tabular-nums text-text-tertiary">
              {t.badge}
            </span>
          )}
        </button>
      ))}
    </nav>
  );
}

function Panel({ children }: { children: React.ReactNode }) {
  return (
    <section
      aria-label="feature detail"
      tabIndex={-1}
      className="h-full overflow-auto bg-bg-primary border-r border-border"
    >
      <div className="p-6 space-y-6 max-w-4xl">{children}</div>
    </section>
  );
}

function EmptyState() {
  return (
    <Panel>
      <div className="text-text-tertiary text-sm">
        Select a feature on the left to see its detail.
      </div>
    </Panel>
  );
}

function Header({ f }: { f: Detail }) {
  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2 flex-wrap">
        <h1 className="text-xl font-semibold text-text-primary">{f.name}</h1>
        <StatusBadge status={f.status} />
        {f.is_running && (
          <span className="chip chip--teal">running</span>
        )}
        {f.needs_review && (
          <span className="chip chip--amber">review pending</span>
        )}
      </div>
      <div className="flex flex-wrap gap-1.5">
        <AttrChip label="id" value={f.id} />
        {f.current_phase && <AttrChip label="phase" value={f.current_phase} />}
        {f.pipeline && <AttrChip label="pipeline" value={f.pipeline} />}
        {f.risk_level && <AttrChip label="risk" value={f.risk_level} />}
        {f.inquireness && <AttrChip label="inquireness" value={f.inquireness} />}
        {f.trace_id && (
          <AttrChip label="trace" value={`${f.trace_id.slice(0, 8)}…`} title={f.trace_id} />
        )}
      </div>
      {f.tags && f.tags.length > 0 && (
        <div className="flex flex-wrap gap-1.5">
          {f.tags.map((t) => (
            <span
              key={t}
              className="text-[0.7rem] px-1.5 py-0.5 rounded-sm bg-bg-tertiary text-text-secondary border border-border"
            >
              {t}
            </span>
          ))}
        </div>
      )}
    </div>
  );
}

// AttrChip renders a key/value pair in an outlined pill, mirroring the
// observability-panel "type agent" / "name KnowledgeBaseAgent" pattern.
function AttrChip({
  label,
  value,
  title,
}: {
  label: string;
  value: string;
  title?: string;
}) {
  return (
    <span className="attr-chip" title={title}>
      <b>{label}</b>
      {value}
    </span>
  );
}

// statusBadgeFamily classifies a feature status into one of six colour
// families so the StatusBadge chip + leading icon stay in sync.
// `interrupted` is split out from `error` because a user-cancelled run
// isn't a failure — purple keeps it distinct without screaming red.
function statusBadgeFamily(
  status: string,
): "ok" | "error" | "interrupted" | "warn" | "muted" | "running" {
  if (status === "Done" || status === "Published") return "ok";
  if (status === "Failed") return "error";
  if (status === "Interrupted") return "interrupted";
  if (status === "NeedUserInput" || status.endsWith("NeedsReview"))
    return "warn";
  if (status === "Created") return "muted";
  return "running";
}

// chipClassForBadge returns the full literal chip class so Tailwind's
// scanner detects every variant — template-literal construction
// (`chip--${color}`) would let the scanner prune rules whose suffix
// isn't seen elsewhere as a literal.
function chipClassForBadge(
  family: ReturnType<typeof statusBadgeFamily>,
): string {
  switch (family) {
    case "ok":
      return "chip chip--green";
    case "error":
      return "chip chip--rose";
    case "interrupted":
      return "chip chip--purple";
    case "warn":
      return "chip chip--amber";
    case "muted":
      return "chip chip--slate";
    case "running":
      return "chip chip--teal";
  }
}

function StatusBadge({ status }: { status: string }) {
  const family = statusBadgeFamily(status);
  const iconStyle =
    family === "interrupted"
      ? {
          borderColor: "var(--chip-purple-text)",
          color: "var(--chip-purple-text)",
        }
      : undefined;
  const iconMod =
    family === "error"
      ? "check-icon--error"
      : family === "warn"
        ? "check-icon--warning"
        : family === "muted"
          ? "check-icon--muted"
          : "";
  return (
    <span className={chipClassForBadge(family)}>
      <span className={`check-icon ${iconMod}`} aria-hidden style={iconStyle}>
        {family === "error" ? (
          <svg viewBox="0 0 10 10" fill="none">
            <path d="M2 2l6 6M8 2l-6 6" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
          </svg>
        ) : family === "interrupted" ? (
          <svg viewBox="0 0 10 10" fill="none">
            <path d="M4 2.5v5M6 2.5v5" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
          </svg>
        ) : family === "warn" ? (
          <svg viewBox="0 0 10 10" fill="none">
            <path d="M5 2.5v3M5 7.25v.25" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
          </svg>
        ) : family === "muted" ? (
          <svg viewBox="0 0 10 10" fill="none">
            <circle cx="5" cy="5" r="1.25" fill="currentColor" />
          </svg>
        ) : (
          <svg viewBox="0 0 10 10" fill="none">
            <path d="M2 5.25l2 2 4-4.5" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
        )}
      </span>
      {status}
    </span>
  );
}

function FailureBanner({
  f,
  retryPending,
  onRetry,
}: {
  f: Detail;
  retryPending: boolean;
  onRetry: () => void;
}) {
  if (!f.failure) return null;
  return (
    <div
      className="mt-4 rounded-md border p-3 text-sm space-y-2"
      style={{
        borderColor: "color-mix(in srgb, var(--status-error) 35%, transparent)",
        background: "color-mix(in srgb, var(--status-error) 8%, var(--bg-secondary))",
      }}
    >
      <div className="flex items-start justify-between gap-3">
        <div>
          <p className="font-medium" style={{ color: "var(--status-error)" }}>
            Feature failed{f.failure.type ? `: ${f.failure.type}` : ""}
          </p>
          <p className="mt-1 text-text-secondary">{f.failure.message}</p>
        </div>
        {f.status === "Failed" && (
          <button
            type="button"
            onClick={onRetry}
            disabled={retryPending}
            className="shrink-0 px-2 py-1 text-xs rounded-sm text-text-inverse disabled:opacity-50"
            style={{ background: "var(--accent)" }}
          >
            {retryPending ? "retrying…" : "retry"}
          </button>
        )}
      </div>
    </div>
  );
}

function Lifecycle({ f }: { f: Detail }) {
  const fmt = (s?: string) => (s ? new Date(s).toLocaleString() : "—");
  return (
    <div>
      <SectionLabel>Lifecycle</SectionLabel>
      <dl className="grid grid-cols-2 gap-y-1 gap-x-6 text-sm">
        <Row label="Created" value={fmt(f.created)} />
        <Row label="Started" value={fmt(f.started_at)} />
        <Row label="Phase started" value={fmt(f.active_phase_start)} />
        <Row label="Plan iteration" value={String(f.plan_iteration ?? 0)} />
        <Row label="Active iteration" value={String(f.active_iteration ?? 0)} />
        <Row label="Review iteration" value={String(f.review_iteration ?? 0)} />
      </dl>
    </div>
  );
}

function Repos({ f }: { f: Detail }) {
  if (!f.repos || f.repos.length === 0) return null;
  return (
    <div>
      <SectionLabel>Repos</SectionLabel>
      <ul className="space-y-1.5 text-sm">
        {f.repos.map((r) => (
          <li
            key={r.name}
            className="flex items-center justify-between gap-3 p-2 rounded-md border border-border bg-bg-tertiary"
          >
            <div className="flex items-center gap-3">
              <span className="font-mono text-xs text-text-primary">{r.name}</span>
              {r.branch && (
                <span className="text-xs text-text-tertiary">
                  {r.branch} ← {r.base_branch ?? "?"}
                </span>
              )}
            </div>
            <div className="flex items-center gap-2 text-xs">
              {r.touched && <span className="text-accent">touched</span>}
              {r.has_error && (
                <span style={{ color: "var(--status-error)" }}>error</span>
              )}
              {r.pr_url && (
                <a
                  href={r.pr_url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="text-accent hover:underline"
                >
                  PR ↗
                </a>
              )}
            </div>
          </li>
        ))}
      </ul>
    </div>
  );
}

function Sessions({
  f,
  onAttach,
}: {
  f: Detail;
  onAttach?: (id: string) => void;
}) {
  if (!f.sessions || f.sessions.length === 0) return null;
  return (
    <div>
      <SectionLabel>Sessions</SectionLabel>
      <ul className="space-y-1.5 text-sm">
        {f.sessions.map((s) => (
          <li
            key={s.id}
            className="flex items-center justify-between gap-3 p-2 rounded-md border border-border bg-bg-tertiary"
          >
            <div className="min-w-0">
              <div className="flex items-center gap-2">
                <span className="font-mono text-xs text-text-primary">{s.id}</span>
                <span className="text-xs text-text-tertiary">
                  {s.phase ?? "—"}
                  {s.repo_name ? ` · ${s.repo_name}` : ""}
                </span>
              </div>
              <div className="text-[0.7rem] text-text-tertiary">
                {s.provider} · {s.model}
                {typeof s.context_percentage === "number" &&
                  s.context_percentage > 0 &&
                  ` · ctx ${s.context_percentage}%`}
              </div>
            </div>
            <div className="flex items-center gap-2 text-xs">
              {s.is_active && (
                <span className="px-1.5 py-0.5 rounded-sm bg-accent text-text-inverse">
                  active
                </span>
              )}
              {s.has_pending_question && (
                <span className="px-1.5 py-0.5 rounded-sm bg-amber-500 text-amber-900">
                  needs input
                </span>
              )}
              <span className="text-text-tertiary">{s.status}</span>
              {onAttach && (
                <button
                  type="button"
                  onClick={() => onAttach(s.id)}
                  disabled={!s.is_active}
                  className="px-2 py-0.5 rounded-sm border border-border text-text-secondary hover:bg-bg-secondary disabled:opacity-50 disabled:cursor-not-allowed disabled:hover:bg-transparent"
                  title={
                    s.is_active
                      ? "Attach to this session"
                      : "This session has ended; nothing to attach to"
                  }
                >
                  attach
                </button>
              )}
            </div>
          </li>
        ))}
      </ul>
    </div>
  );
}

function NeedUserInputPanel({
  f,
  featureId,
}: {
  f: Detail;
  featureId: string;
}) {
  const gates = f.pending_need_user_input ?? [];
  if (gates.length === 0) return null;
  return (
    <div className="space-y-3">
      {gates.map((gate, i) => (
        <NeedUserInputForm
          key={`${gate.repo_name ?? "feature"}-${i}`}
          featureId={featureId}
          gate={gate}
        />
      ))}
    </div>
  );
}

// Sentinel value stored in `selected` when the user picks the "Other…"
// radio. Kept separate from the actual free-text content so the user
// can choose Other and start typing without the empty input being
// mistaken for "no selection". Matches the AskUserAnswerForm pattern in
// AttachDrawer.tsx so the two gating UIs feel identical.
const NUI_OTHER_SENTINEL = "__other__";

function NeedUserInputForm({
  featureId,
  gate,
}: {
  featureId: string;
  gate: NonNullable<Detail["pending_need_user_input"]>[number];
}) {
  const qc = useQueryClient();
  // Local answer state keyed by question index. We intentionally do NOT
  // seed from the gate (the YAML answers stay blank until the user
  // touches them) because surfacing prior partial answers would require
  // the projection to ship them, which it doesn't today.
  const [answers, setAnswers] = useState<Record<number, string>>({});
  // selected: which radio is currently picked per question (option
  // label or NUI_OTHER_SENTINEL). Used only when q.options is non-empty.
  const [selected, setSelected] = useState<Record<number, string>>({});
  // otherText: the typed free-text content per question while "Other…"
  // is the active selection.
  const [otherText, setOtherText] = useState<Record<number, string>>({});
  const [pendingAction, setPendingAction] = useState<
    "save" | "resume" | "abort" | null
  >(null);

  const questions = gate.questions ?? [];
  const allAnswered =
    questions.length > 0 &&
    questions.every((q) => (answers[q.index] ?? "").trim() !== "");

  const submit = useMutation({
    mutationFn: (
      decision: "save" | "resume" | "abort",
    ) => {
      setPendingAction(decision);
      // Sparse-send: emit only the answers the user actually filled in
      // this session. The server's applyAnswersToRecord overwrites by
      // index, so sending `""` for every untouched question would erase
      // anything a prior save-draft persisted to disk. Local `answers`
      // state is intentionally never seeded from the existing YAML
      // (see comment above), so refresh-then-save would otherwise wipe
      // earlier work.
      return api.needUserInputDecision(featureId, {
        decision,
        repo_name: gate.repo_name,
        answers:
          decision === "abort"
            ? undefined
            : questions
                .filter((q) => {
                  const a = answers[q.index];
                  return a !== undefined && a !== "";
                })
                .map((q) => ({
                  index: q.index,
                  answer: answers[q.index],
                })),
      });
    },
    onSettled: () => {
      setPendingAction(null);
      qc.invalidateQueries({ queryKey: ["feature", featureId] });
    },
  });

  const pending = submit.isPending;
  const error = submit.error as ApiError | Error | null;

  return (
    <div className="banner banner--warning">
      <span className="banner-icon" aria-hidden>
        ?
      </span>
      <div className="flex-1 min-w-0">
        <div className="banner-title">
          Waiting for input
          {gate.repo_name ? ` · ${gate.repo_name}` : ""}
          {gate.iteration ? ` · iteration ${gate.iteration}` : ""}
        </div>
        {gate.summary && (
          <div className="banner-body whitespace-pre-wrap">{gate.summary}</div>
        )}
        <form
          className="mt-3 space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (!pending && allAnswered) submit.mutate("resume");
          }}
        >
          {questions.map((q) => {
            const inputId = `nui-${featureId}-${gate.repo_name ?? "f"}-${q.index}`;
            const groupName = `nui-group-${inputId}`;
            const hasOptions = (q.options?.length ?? 0) > 0;
            const isOtherSelected = selected[q.index] === NUI_OTHER_SENTINEL;
            const pickOption = (label: string) => {
              setSelected((c) => ({ ...c, [q.index]: label }));
              setAnswers((c) => ({ ...c, [q.index]: label }));
            };
            const pickOther = () => {
              setSelected((c) => ({ ...c, [q.index]: NUI_OTHER_SENTINEL }));
              setAnswers((c) => ({
                ...c,
                [q.index]: otherText[q.index] ?? "",
              }));
            };
            const updateOther = (txt: string) => {
              setOtherText((c) => ({ ...c, [q.index]: txt }));
              setAnswers((c) => ({ ...c, [q.index]: txt }));
            };
            return (
              <div key={q.index} className="space-y-1">
                <label
                  htmlFor={hasOptions ? undefined : inputId}
                  className="text-[0.7rem] font-semibold text-text-primary block whitespace-pre-wrap"
                >
                  {q.index}. {q.prompt}
                </label>
                {q.header && (
                  <p className="text-[0.65rem] font-medium text-accent opacity-80">
                    {q.header}
                  </p>
                )}
                {hasOptions ? (
                  <fieldset className="space-y-1" disabled={pending}>
                    {q.options!.map((o) => (
                      <label
                        key={o.label}
                        className="flex items-start gap-2 text-xs text-text-primary cursor-pointer"
                      >
                        <input
                          type="radio"
                          name={groupName}
                          value={o.label}
                          checked={selected[q.index] === o.label}
                          onChange={() => pickOption(o.label)}
                          className="mt-0.5 accent-[var(--accent)]"
                        />
                        <span className="flex-1">
                          <span>{o.label}</span>
                          {o.description && (
                            <span className="block text-[0.65rem] opacity-70">
                              {o.description}
                            </span>
                          )}
                        </span>
                      </label>
                    ))}
                    <label className="flex items-start gap-2 text-xs text-text-primary cursor-pointer">
                      <input
                        type="radio"
                        name={groupName}
                        value={NUI_OTHER_SENTINEL}
                        checked={isOtherSelected}
                        onChange={pickOther}
                        className="mt-0.5 accent-[var(--accent)]"
                      />
                      <span className="flex-1">Other…</span>
                    </label>
                    {isOtherSelected && (
                      <textarea
                        autoFocus
                        value={otherText[q.index] ?? ""}
                        onChange={(e) => updateOther(e.target.value)}
                        rows={2}
                        placeholder="type your answer…"
                        className="w-full px-2 py-1 text-xs rounded-sm bg-bg-tertiary border border-border text-text-primary focus:outline-none focus:border-accent font-mono"
                      />
                    )}
                  </fieldset>
                ) : (
                  <textarea
                    id={inputId}
                    value={answers[q.index] ?? ""}
                    onChange={(e) =>
                      setAnswers((cur) => ({
                        ...cur,
                        [q.index]: e.target.value,
                      }))
                    }
                    rows={3}
                    placeholder="answer…"
                    disabled={pending}
                    className="w-full px-2 py-1 text-xs rounded-sm bg-bg-tertiary border border-border text-text-primary focus:outline-none focus:border-accent font-mono disabled:opacity-50"
                  />
                )}
              </div>
            );
          })}
          {error && (
            <p className="text-xs" style={{ color: "var(--status-error)" }}>
              {error.message}
            </p>
          )}
          <div className="flex flex-wrap items-center justify-end gap-2">
            <button
              type="button"
              disabled={pending}
              onClick={() => submit.mutate("save")}
              className="px-3 py-1 text-xs rounded-sm border border-border text-text-primary hover:bg-bg-tertiary disabled:opacity-50"
              title="Persist these answers without resuming the agent."
            >
              {pendingAction === "save" ? "saving…" : "save draft"}
            </button>
            <button
              type="button"
              disabled={pending}
              onClick={() => {
                // Cycle-scoped gates (post-publish) only fail the cycle —
                // the orchestrator preserves the feature at Published.
                // Feature-level gates fail the whole feature. Branch the
                // confirm copy on gate.repo_name so the wording matches.
                const prompt = gate.repo_name
                  ? `Abort this post-publish cycle on ${gate.repo_name}? The cycle will be marked failed; the feature stays published.`
                  : "Abort this gate? The feature will be marked failed and answers will be discarded.";
                if (window.confirm(prompt)) {
                  submit.mutate("abort");
                }
              }}
              className="px-3 py-1 text-xs rounded-sm border border-border text-text-primary hover:bg-bg-tertiary disabled:opacity-50"
            >
              {pendingAction === "abort" ? "aborting…" : "abort"}
            </button>
            <button
              type="submit"
              disabled={pending || !allAnswered}
              className="px-3 py-1 text-xs rounded-sm text-text-inverse disabled:opacity-50"
              style={{ background: "var(--accent)" }}
              title={
                allAnswered
                  ? "Persist answers and dispatch a fresh iteration."
                  : "Every question must have a non-empty answer before resume."
              }
            >
              {pendingAction === "resume" ? "resuming…" : "save & resume"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

function Queues({ f }: { f: Detail }) {
  const hasHelp = (f.help_queue ?? []).some((h) => h.pending);
  const hasPerms = (f.permissions_queue ?? []).some((p) => p.pending);
  if (!hasHelp && !hasPerms) return null;

  return (
    <div className="space-y-3">
      {hasHelp && (
        <div className="banner banner--warning">
          <span className="banner-icon" aria-hidden>
            ?
          </span>
          <div>
            <div className="banner-title">Agent is waiting for help</div>
            <div className="banner-body">
              {f.help_queue!.filter((h) => h.pending).map((h) => h.question).join(" · ")}
            </div>
          </div>
        </div>
      )}
      {hasPerms && (
        <div className="banner banner--warning">
          <span className="banner-icon" aria-hidden>
            !
          </span>
          <div>
            <div className="banner-title">Pending permission requests</div>
            <div className="banner-body">
              {f.permissions_queue!.filter((p) => p.pending).map((p) => p.tool).join(", ")}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

function PhaseTimings({ f }: { f: Detail }) {
  if (!f.phase_timings_ms || Object.keys(f.phase_timings_ms).length === 0)
    return null;
  return (
    <div>
      <SectionLabel>Phase timings</SectionLabel>
      <ul className="text-sm grid grid-cols-2 gap-x-6 gap-y-1">
        {Object.entries(f.phase_timings_ms).map(([k, ms]) => (
          <li key={k} className="flex justify-between gap-2">
            <span className="text-text-tertiary">{k}</span>
            <span className="font-mono text-text-primary">{formatDuration(ms)}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

function PhaseCosts({ f }: { f: Detail }) {
  const costs = f.phase_costs ?? {};
  const keys = Object.keys(costs);
  if (keys.length === 0 && !f.total_cost_usd) return null;

  // Sort by descending cost so the biggest contributors are on top.
  keys.sort((a, b) => (costs[b] ?? 0) - (costs[a] ?? 0));

  return (
    <div>
      <SectionLabel>Costs</SectionLabel>
      <ul className="text-sm grid grid-cols-2 gap-x-6 gap-y-1">
        {keys.map((k) => (
          <li key={k} className="flex justify-between gap-2">
            <span className="text-text-tertiary">{k}</span>
            <span className="font-mono text-text-primary tabular-nums">
              {formatUSD(costs[k] ?? 0)}
            </span>
          </li>
        ))}
        <li className="col-span-2 mt-1 pt-1 border-t border-border flex justify-between gap-2">
          <span className="text-text-tertiary font-semibold uppercase tracking-wide text-[0.65rem]">
            total
          </span>
          <span
            className="font-mono tabular-nums"
            style={{ color: "var(--accent)" }}
          >
            {formatUSD(f.total_cost_usd ?? 0)}
          </span>
        </li>
      </ul>
    </div>
  );
}

function formatUSD(n: number): string {
  return `$${n.toFixed(2)}`;
}

function Description({ f }: { f: Detail }) {
  if (!f.description && !f.exit_criteria) return null;
  return (
    <div className="space-y-3">
      {f.description && (
        <div>
          <SectionLabel>Description</SectionLabel>
          <p className="text-sm text-text-secondary whitespace-pre-wrap leading-relaxed">
            {f.description}
          </p>
        </div>
      )}
      {f.exit_criteria && (
        <div>
          <SectionLabel>Exit criteria</SectionLabel>
          <p className="text-sm text-text-secondary whitespace-pre-wrap leading-relaxed">
            {f.exit_criteria}
          </p>
        </div>
      )}
    </div>
  );
}

function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <h2 className="text-xs font-semibold uppercase tracking-wide text-text-tertiary mb-2">
      {children}
    </h2>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <>
      <dt className="text-text-tertiary">{label}</dt>
      <dd className="text-text-primary">{value}</dd>
    </>
  );
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(1)}s`;
  const m = Math.floor(s / 60);
  const rs = Math.round(s % 60);
  return `${m}m ${rs}s`;
}
