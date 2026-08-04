import { useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "../api/client";
import { useUI } from "../store/ui";
import { Spinner } from "./Spinner";
import {
  ProgressOverlay,
  createFeatureStages,
  stageIndexFor,
  type CreateFeaturePhase,
} from "./CreationProgress";
import type {
  Checkpoints,
  ConfigPayload,
  CreateFeatureRequest,
  ModelConfig,
  ModelOption,
} from "../api/types";

// 4-step create-feature wizard backed by POST /api/features and
// POST /api/features/:id/start. Mirrors the TUI's wizard.go flow so
// both clients converge on the same domain choices.

type Step = "what" | "where" | "pipeline" | "review";
const STEPS: Step[] = ["what", "where", "pipeline", "review"];
const STEP_LABELS: Record<Step, string> = {
  what: "What",
  where: "Where",
  pipeline: "Pipeline",
  review: "Review",
};

interface Draft {
  name: string;
  description: string;
  exitCriteria: string;
  repos: string[];
  useCurrentBranch: boolean;
  pipeline: string;
  risk: string;
  inquireness: string;
  models: ModelConfig;
  checkpoints: Checkpoints;
  autoStart: boolean;
}

function emptyDraft(): Draft {
  return {
    name: "",
    description: "",
    exitCriteria: "",
    repos: [],
    useCurrentBranch: false,
    pipeline: "medium",
    risk: "medium",
    inquireness: "medium",
    models: {},
    checkpoints: {},
    autoStart: true,
  };
}

export function Wizard() {
  const open = useUI((s) => s.wizardOpen);
  const close = useUI((s) => s.closeWizard);
  if (!open) return null;
  return <WizardModal close={close} />;
}

function WizardModal({ close }: { close: () => void }) {
  const qc = useQueryClient();
  const [step, setStep] = useState<Step>("what");
  const [draft, setDraft] = useState<Draft>(emptyDraft);

  const cfgQuery = useQuery({
    queryKey: ["config"],
    queryFn: ({ signal }) => api.config(signal),
    staleTime: 5 * 60_000,
  });

  // Default-fill enums/models from config once available.
  useMemo(() => {
    if (!cfgQuery.data) return;
    const d = cfgQuery.data.defaults;
    setDraft((cur) => ({
      ...cur,
      pipeline: cur.pipeline || d.pipeline || "medium",
      inquireness: cur.inquireness || d.inquireness || "medium",
      exitCriteria: cur.exitCriteria || d.exit_criteria || "",
      models: { ...d.models, ...cur.models },
      checkpoints: { ...d.checkpoints, ...cur.checkpoints },
    }));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cfgQuery.data]);

  // Phase-driven progress: 'idle' → 'save' → ('start') → 'ready' or
  // 'error'. Drives both the submit-button spinner and the rich
  // CreationProgress overlay. We manage the async flow inline rather
  // than via useMutation so the wizard can advance through multiple
  // API calls (createFeature → startFeature) with visible stage
  // transitions in between.
  const [phase, setPhase] = useState<CreateFeaturePhase>("idle");
  const [submitError, setSubmitError] = useState<unknown>(null);
  const startedAtRef = useRef<number>(0);

  const submitting = phase === "save" || phase === "start";

  const submit = async () => {
    const body: CreateFeatureRequest = {
      name: draft.name.trim(),
      description: draft.description.trim(),
      repos: draft.repos,
      models: draft.models,
      exit_criteria: draft.exitCriteria.trim() || undefined,
      inquireness: draft.inquireness || undefined,
      risk_level: draft.risk || undefined,
      pipeline: draft.pipeline || undefined,
      use_current_branch: draft.useCurrentBranch,
      checkpoints: draft.checkpoints,
    };
    setSubmitError(null);
    startedAtRef.current = performance.now();
    setPhase("save");
    try {
      const created = await api.createFeature(body);
      if (draft.autoStart) {
        setPhase("start");
        try {
          await api.startFeature(created.id);
        } catch (err) {
          // Surface but don't roll back the create.
          console.error("startFeature failed", err);
        }
      }
      setPhase("ready");
      await qc.invalidateQueries({ queryKey: ["features"] });
      // Brief pause so the user sees the "ready" check before the modal closes.
      window.setTimeout(close, 450);
    } catch (err) {
      setSubmitError(err);
      setPhase("error");
    }
  };

  const canAdvance = stepIsValid(step, draft);
  const stepIndex = STEPS.indexOf(step);
  const stages = useMemo(() => createFeatureStages(draft.autoStart), [draft.autoStart]);
  const currentStageIndex = stageIndexFor(phase, draft.autoStart);

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm"
      role="dialog"
      aria-modal="true"
    >
      <div className="relative bg-bg-secondary border border-border rounded-lg w-[min(720px,92vw)] max-h-[88vh] flex flex-col shadow-lg">
        <Header stepIndex={stepIndex} close={close} />

        <ProgressOverlay
          active={submitting || phase === "ready"}
          heading="creating feature…"
          stages={stages}
          currentStageIndex={currentStageIndex}
          startedAt={startedAtRef.current}
          failed={phase === "error"}
          errorLabel={
            submitError instanceof ApiError
              ? `${submitError.status}: ${submitError.message}`
              : submitError instanceof Error
                ? submitError.message
                : undefined
          }
        />

        <div className="flex-1 overflow-auto p-5">
          {step === "what" && <StepWhat draft={draft} setDraft={setDraft} />}
          {step === "where" && (
            <StepWhere draft={draft} setDraft={setDraft} config={cfgQuery.data} />
          )}
          {step === "pipeline" && (
            <StepPipeline draft={draft} setDraft={setDraft} config={cfgQuery.data} />
          )}
          {step === "review" && (
            <StepReview draft={draft} setDraft={setDraft} config={cfgQuery.data} />
          )}
        </div>

        <Footer
          step={step}
          stepIndex={stepIndex}
          canAdvance={canAdvance}
          submitting={submitting}
          error={submitError}
          onBack={() => setStep(STEPS[Math.max(0, stepIndex - 1)])}
          onNext={() => setStep(STEPS[Math.min(STEPS.length - 1, stepIndex + 1)])}
          onSubmit={submit}
        />
      </div>
    </div>
  );
}

function Header({
  stepIndex,
  close,
}: {
  stepIndex: number;
  close: () => void;
}) {
  return (
    <header className="flex items-center justify-between px-5 py-3 border-b border-border">
      <div className="flex items-center gap-3">
        <h2 className="text-sm font-semibold text-text-primary">New feature</h2>
        <div className="flex items-center gap-1 text-xs text-text-tertiary">
          {STEPS.map((s, i) => (
            <span key={s} className="flex items-center gap-1">
              <span
                className={`inline-block w-1.5 h-1.5 rounded-full ${
                  i === stepIndex ? "" : "opacity-40"
                }`}
                style={{ background: "var(--accent)" }}
              />
              <span className={i === stepIndex ? "text-text-primary" : ""}>
                {STEP_LABELS[s]}
              </span>
              {i < STEPS.length - 1 && <span className="mx-1">→</span>}
            </span>
          ))}
        </div>
      </div>
      <button
        type="button"
        onClick={close}
        className="text-text-tertiary hover:text-text-primary px-2"
        aria-label="Close wizard"
      >
        ✕
      </button>
    </header>
  );
}

function Footer({
  step,
  stepIndex,
  canAdvance,
  submitting,
  error,
  onBack,
  onNext,
  onSubmit,
}: {
  step: Step;
  stepIndex: number;
  canAdvance: boolean;
  submitting: boolean;
  error: unknown;
  onBack: () => void;
  onNext: () => void;
  onSubmit: () => void;
}) {
  const isLast = step === "review";
  return (
    <footer className="flex items-center justify-between px-5 py-3 border-t border-border gap-3">
      <div className="text-xs">
        {error ? (
          <span style={{ color: "var(--status-error)" }}>
            {error instanceof ApiError
              ? `${error.status}: ${error.message}`
              : error instanceof Error
                ? error.message
                : String(error)}
          </span>
        ) : null}
      </div>
      <div className="flex gap-2">
        <button
          type="button"
          onClick={onBack}
          disabled={stepIndex === 0 || submitting}
          className="px-3 py-1.5 text-sm border border-border rounded-sm text-text-secondary hover:bg-bg-tertiary disabled:opacity-50 disabled:cursor-not-allowed"
        >
          ← back
        </button>
        {!isLast && (
          <button
            type="button"
            onClick={onNext}
            disabled={!canAdvance}
            className="px-3 py-1.5 text-sm rounded-sm text-text-inverse disabled:opacity-50 disabled:cursor-not-allowed"
            style={{ background: canAdvance ? "var(--accent)" : "var(--text-tertiary)" }}
          >
            next →
          </button>
        )}
        {isLast && (
          <button
            type="button"
            onClick={onSubmit}
            disabled={!canAdvance || submitting}
            className="px-3 py-1.5 text-sm rounded-sm text-text-inverse disabled:opacity-60 disabled:cursor-not-allowed inline-flex items-center gap-2"
            style={{ background: "var(--accent)" }}
            aria-busy={submitting}
          >
            {submitting ? (
              <>
                <Spinner size="xs" ariaLabel="creating feature" />
                <span>creating…</span>
              </>
            ) : (
              "create"
            )}
          </button>
        )}
      </div>
    </footer>
  );
}

function stepIsValid(step: Step, d: Draft): boolean {
  switch (step) {
    case "what":
      return d.name.trim().length > 0 && d.description.trim().length > 0;
    case "where":
      return d.repos.length > 0;
    case "pipeline":
      return !!d.pipeline;
    case "review":
      return d.name.trim().length > 0 && d.repos.length > 0;
  }
}

function StepWhat({
  draft,
  setDraft,
}: {
  draft: Draft;
  setDraft: React.Dispatch<React.SetStateAction<Draft>>;
}) {
  return (
    <div className="space-y-4">
      <Field label="Name" hint="becomes the feature's slug">
        <input
          type="text"
          value={draft.name}
          onChange={(e) => setDraft((d) => ({ ...d, name: e.target.value }))}
          autoFocus
          placeholder="Name that represents the short product outcome, also used as the slug."
          className="w-full px-2 py-1.5 text-sm rounded-sm bg-bg-tertiary border border-border text-text-primary focus:outline-none focus:border-accent"
        />
      </Field>
      <Field
        label="Description"
        hint="what the feature does, who it's for, any constraints"
      >
        <textarea
          value={draft.description}
          onChange={(e) =>
            setDraft((d) => ({ ...d, description: e.target.value }))
          }
          rows={6}
          data-persist-key="wizard.description"
          placeholder="Describe the user problem, who this is for, and what a good outcome looks like. Add any useful context, constraints, examples, or edge cases — the more relevant detail you provide here, the better the agents can understand what you actually need."
          className="w-full px-2 py-1.5 text-sm rounded-sm bg-bg-tertiary border border-border text-text-primary focus:outline-none focus:border-accent font-mono"
        />
      </Field>
    </div>
  );
}

function StepWhere({
  draft,
  setDraft,
  config,
}: {
  draft: Draft;
  setDraft: React.Dispatch<React.SetStateAction<Draft>>;
  config?: ConfigPayload;
}) {
  const repos = config?.repos ?? [];
  return (
    <div className="space-y-4">
      <Field label="Repos" hint="select one or more">
        {repos.length === 0 && (
          <p className="text-xs text-text-tertiary italic">
            {config ? "no repos configured" : "loading…"}
          </p>
        )}
        <ul className="grid grid-cols-2 gap-1">
          {repos.map((r) => {
            const checked = draft.repos.includes(r.name);
            return (
              <li key={r.name}>
                <label className="flex items-center gap-2 px-2 py-1 rounded-sm hover:bg-bg-tertiary cursor-pointer text-sm">
                  <input
                    type="checkbox"
                    checked={checked}
                    onChange={(e) =>
                      setDraft((d) => ({
                        ...d,
                        repos: e.target.checked
                          ? [...d.repos, r.name]
                          : d.repos.filter((n) => n !== r.name),
                      }))
                    }
                  />
                  <span className="font-mono text-xs">{r.name}</span>
                </label>
              </li>
            );
          })}
        </ul>
      </Field>
      <Field label="Branch base" hint="">
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={draft.useCurrentBranch}
            onChange={(e) =>
              setDraft((d) => ({ ...d, useCurrentBranch: e.target.checked }))
            }
          />
          <span>Branch from the repo's current HEAD instead of its default branch</span>
        </label>
      </Field>
    </div>
  );
}

function StepPipeline({
  draft,
  setDraft,
  config,
}: {
  draft: Draft;
  setDraft: React.Dispatch<React.SetStateAction<Draft>>;
  config?: ConfigPayload;
}) {
  const e = config?.enums ?? { pipelines: ["medium", "large", "moonshot"], risks: ["low", "medium", "high"], inquireness: ["none", "medium", "high"] };
  return (
    <div className="space-y-4">
      <RadioGroup
        label="Pipeline profile"
        hint="moonshot is the lightest; large does the full inquire→research→plan dance"
        value={draft.pipeline}
        options={e.pipelines}
        onChange={(v) => setDraft((d) => ({ ...d, pipeline: v }))}
      />
      <RadioGroup
        label="Risk"
        hint="influences review gates and timing budgets"
        value={draft.risk}
        options={e.risks}
        onChange={(v) => setDraft((d) => ({ ...d, risk: v }))}
      />
      <RadioGroup
        label="Inquireness"
        hint="how much upfront question-asking the agent should do"
        value={draft.inquireness}
        options={e.inquireness}
        onChange={(v) => setDraft((d) => ({ ...d, inquireness: v }))}
      />
      <Field label="Checkpoints" hint="phases that pause for human review">
        <div className="grid grid-cols-2 gap-1 text-sm">
          {(
            [
              ["inquiry_review", "Inquiry"],
              ["research_review", "Research"],
              ["design_review", "Design"],
              ["plan_review", "Plan"],
              ["manual_publish", "Manual publish"],
            ] as const
          ).map(([key, label]) => (
            <label
              key={key}
              className="flex items-center gap-2 px-2 py-1 rounded-sm hover:bg-bg-tertiary cursor-pointer"
            >
              <input
                type="checkbox"
                checked={!!draft.checkpoints[key as keyof Checkpoints]}
                onChange={(ev) =>
                  setDraft((d) => ({
                    ...d,
                    checkpoints: {
                      ...d.checkpoints,
                      [key]: ev.target.checked,
                    },
                  }))
                }
              />
              {label}
            </label>
          ))}
        </div>
      </Field>
      <Field label="Exit criteria" hint="leave empty for default">
        <textarea
          value={draft.exitCriteria}
          onChange={(e) =>
            setDraft((d) => ({ ...d, exitCriteria: e.target.value }))
          }
          rows={3}
          placeholder="Given… When… Then…"
          className="w-full px-2 py-1.5 text-sm rounded-sm bg-bg-tertiary border border-border text-text-primary focus:outline-none focus:border-accent font-mono"
        />
      </Field>
    </div>
  );
}

function StepReview({
  draft,
  setDraft,
  config,
}: {
  draft: Draft;
  setDraft: React.Dispatch<React.SetStateAction<Draft>>;
  config?: ConfigPayload;
}) {
  return (
    <div className="space-y-4">
      <ReviewRow label="Name" value={draft.name} />
      <ReviewRow label="Repos" value={draft.repos.join(", ") || "—"} />
      <ReviewRow
        label="Pipeline / risk / inquireness"
        value={`${draft.pipeline} · ${draft.risk} · ${draft.inquireness}`}
      />
      <ReviewRow
        label="Checkpoints"
        value={
          Object.entries(draft.checkpoints)
            .filter(([, v]) => v)
            .map(([k]) => k)
            .join(", ") || "none"
        }
      />
      <Field label="Models per phase" hint="">
        <ModelPickers draft={draft} setDraft={setDraft} config={config} />
      </Field>
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={draft.autoStart}
          onChange={(e) =>
            setDraft((d) => ({ ...d, autoStart: e.target.checked }))
          }
        />
        <span>Start the feature immediately after create</span>
      </label>
    </div>
  );
}

function ModelPickers({
  draft,
  setDraft,
  config,
}: {
  draft: Draft;
  setDraft: React.Dispatch<React.SetStateAction<Draft>>;
  config?: ConfigPayload;
}) {
  const phases: Array<{ key: keyof ModelConfig; label: string; opts?: ModelOption[] }> = [
    { key: "research", label: "Research", opts: config?.models.research },
    { key: "planning", label: "Planning", opts: config?.models.planning },
    { key: "implementation", label: "Implementation", opts: config?.models.implementation },
    { key: "review", label: "Review", opts: config?.models.review },
    { key: "utilities", label: "Utilities", opts: config?.models.utilities },
    { key: "kb_build", label: "KB build", opts: config?.models.kb_build },
  ];
  return (
    <div className="grid grid-cols-2 gap-2 text-sm">
      {phases.map((p) => (
        <label key={p.key} className="flex flex-col gap-1">
          <span className="text-[0.65rem] uppercase tracking-wide text-text-tertiary">
            {p.label}
          </span>
          <select
            value={(draft.models[p.key] as string) ?? ""}
            onChange={(e) =>
              setDraft((d) => ({
                ...d,
                models: { ...d.models, [p.key]: e.target.value || undefined },
              }))
            }
            className="px-2 py-1 rounded-sm bg-bg-tertiary border border-border text-text-primary focus:outline-none focus:border-accent"
          >
            <option value="">(default)</option>
            {(p.opts ?? []).map((o) => (
              <option key={o.id} value={o.id}>
                {o.provider ? `${o.provider}: ${o.id}` : o.id}
              </option>
            ))}
          </select>
        </label>
      ))}
    </div>
  );
}

function ReviewRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="grid grid-cols-[140px_1fr] gap-3 text-sm">
      <dt className="text-text-tertiary">{label}</dt>
      <dd className="text-text-primary">{value}</dd>
    </div>
  );
}

function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-1">
      <div className="flex items-baseline gap-2">
        <label className="text-xs font-semibold uppercase tracking-wide text-text-secondary">
          {label}
        </label>
        {hint && <span className="text-[0.65rem] text-text-tertiary">{hint}</span>}
      </div>
      {children}
    </div>
  );
}

function RadioGroup({
  label,
  hint,
  value,
  options,
  onChange,
}: {
  label: string;
  hint?: string;
  value: string;
  options: string[];
  onChange: (v: string) => void;
}) {
  return (
    <Field label={label} hint={hint}>
      <div className="flex gap-2">
        {options.map((opt) => {
          const selected = opt === value;
          return (
            <button
              key={opt}
              type="button"
              onClick={() => onChange(opt)}
              className="px-3 py-1 text-sm rounded-sm border"
              style={
                selected
                  ? {
                      background: "var(--accent)",
                      borderColor: "var(--accent)",
                      color: "var(--text-inverse)",
                    }
                  : {
                      borderColor: "var(--border-color)",
                      color: "var(--text-secondary)",
                    }
              }
            >
              {opt}
            </button>
          );
        })}
      </div>
    </Field>
  );
}
