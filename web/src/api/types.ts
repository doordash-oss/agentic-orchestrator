// Mirrors internal/webui/view/feature.go.
// Hand-written for M2a; replaced by openapi-typescript-generated types
// once we add an OpenAPI spec.

export type FeatureStatus =
  | "Created"
  | "Researching"
  | "PlanReady"
  | "Planning"
  | "ImplementReady"
  | "Implementing"
  | "ReviewPassed"
  | "CodeReady"
  | "Published"
  | "Failed"
  | "Interrupted"
  | "Done"
  | "BuildingKB"
  | "PlanNeedsReview"
  | "Inquiring"
  | "InquireReady"
  | "DesignReady"
  | "Designing"
  | "BrainstormReady"
  | "Brainstorming"
  | "PromptNeedsReview"
  | "InquiryNeedsReview"
  | "ResearchNeedsReview"
  | "DesignNeedsReview"
  | "Reviewing"
  | "NeedUserInput"
  | "FinalReviewing"
  | "SettingUpWorktrees";

export type PhaseName =
  | "research"
  | "plan"
  | "implement"
  | "publish"
  | "review"
  | "knowledge_base"
  | "inquire"
  | "brainstorm"
  | "final_review";

export interface FeatureSummary {
  id: string;
  slug: string;
  name: string;
  status: FeatureStatus;
  current_phase?: string;
  pipeline?: string;
  risk_level?: string;
  inquireness?: string;
  tags?: string[];
  repo_names?: string[];
  summary?: string;
  created: string;
  is_running: boolean;
  needs_review: boolean;
  help_pending?: number;
  permissions_pending?: number;
  total_cost_usd?: number;
}

export interface ModelConfig {
  research?: string;
  planning?: string;
  implementation?: string;
  review?: string;
  utilities?: string;
  kb_build?: string;
}

export interface Checkpoints {
  inquiry_review?: boolean;
  research_review?: boolean;
  design_review?: boolean;
  roadmap_review?: boolean;
  phase_plan_review?: boolean;
  plan_review?: boolean;
  manual_publish?: boolean;
}

export interface Repo {
  name: string;
  branch?: string;
  base_branch?: string;
  publishable?: boolean;
  touched?: boolean;
  pr_url?: string;
  has_error?: boolean;
}

export interface Session {
  id: string;
  feature_id: string;
  phase?: string;
  repo_name?: string;
  kind?: string;
  label?: string;
  status: string;
  is_active: boolean;
  iteration?: number;
  started_at: string;
  provider?: string;
  model?: string;
  context_percentage?: number;
  has_pending_question?: boolean;
}

export interface Cycle {
  type: string;
  status?: string;
  count?: number;
  iteration?: number;
  has_error?: boolean;
}

export interface RepoCycle {
  repo: string;
  cycle: Cycle;
}

export interface HelpItem {
  question: string;
  pending: boolean;
  time: string;
}

export interface PermissionItem {
  tool: string;
  args?: string;
  pending: boolean;
  time: string;
}

export interface FeatureDetail extends FeatureSummary {
  description?: string;
  failure?: FeatureFailure;
  exit_criteria?: string;
  models: ModelConfig;
  checkpoints: Checkpoints;
  max_iterations?: number;
  max_plan_iterations?: number;
  active_iteration?: number;
  plan_iteration?: number;
  review_iteration?: number;
  started_at?: string;
  active_phase_start?: string;
  phase_timings_ms?: Record<string, number>;
  phase_costs?: Record<string, number>;
  repos?: Repo[];
  sessions?: Session[];
  active_cycle?: Cycle;
  repo_cycles?: RepoCycle[];
  help_queue?: HelpItem[];
  permissions_queue?: PermissionItem[];
  kb_status?: Record<string, string>;
  trace_id?: string;
  active_run?: number;
  run_count?: number;
  pending_need_user_input?: NeedUserInputView[];
}

export interface FeatureFailure {
  type?: string;
  message: string;
}

export interface NeedUserInputView {
  summary: string;
  questions?: NeedUserInputQuestion[];
  iteration?: number;
  /** Empty for a feature-level gate; otherwise the repo whose cycle is paused. */
  repo_name?: string;
}

export interface NeedUserInputQuestion {
  index: number;
  prompt: string;
  /** Short chip rendered above the picker (e.g. "Backend target"). */
  header?: string;
  /**
   * Structured choices. When present, the dashboard renders a radio
   * picker with a final "Other…" entry that reveals a free-text input.
   * Absent for free-text questions.
   */
  options?: NeedUserInputOption[];
}

export interface NeedUserInputOption {
  label: string;
  description?: string;
}

export interface NeedUserInputDecisionRequest {
  /**
   * - "save"   — persist answers to the gate YAML without resuming.
   * - "resume" — persist answers, then dispatch the resume.
   *              Every question must have a non-empty answer.
   * - "abort"  — discard the gate via the orchestrator; answers ignored.
   */
  decision: "save" | "resume" | "abort";
  /** Empty for feature-level gates; set for post-publish cycle gates. */
  repo_name?: string;
  answers?: NeedUserInputAnswerEntry[];
}

export interface NeedUserInputAnswerEntry {
  index: number;
  answer: string;
}

export interface FeaturesListResponse {
  features: FeatureSummary[];
}

export interface Health {
  status: string;
  uptime_seconds: number;
  assets_embedded: boolean;
  /** Healthz payload schema version. */
  version: number;
  /** Orchestrator build version (matches TUI "Orchestrator v…" footer). */
  app_version: string;
}

// M3 — Create feature wizard pre-flight + submission shapes.

export interface ModelOption {
  id: string;
  provider?: string;
}

export interface ModelCatalog {
  research?: ModelOption[];
  planning?: ModelOption[];
  implementation?: ModelOption[];
  review?: ModelOption[];
  utilities?: ModelOption[];
  kb_build?: ModelOption[];
}

export interface Defaults {
  models: ModelConfig;
  exit_criteria?: string;
  inquireness?: string;
  pipeline?: string;
  checkpoints: Checkpoints;
}

export interface Enums {
  pipelines: string[];
  risks: string[];
  inquireness: string[];
}

export interface ConfigPayload {
  repos: Repo[];
  workspace_roots?: string[];
  models: ModelCatalog;
  defaults: Defaults;
  enums: Enums;
}

// M7 — Recovery + Logs + Review comments.

export type RecoveryActionValue = "resume" | "kill" | "skip";

export interface RecoveryItem {
  key: string;
  feature_id: string;
  feature_name?: string;
  feature_slug?: string;
  repo_name?: string;
  phase?: string;
  iteration?: number;
  pid?: number;
  process_alive: boolean;
  started_at?: string;
  stale_seconds?: number;
}

export interface RecoveryScanResponse {
  snapshot_id: string;
  items: RecoveryItem[];
}

export interface ReviewComment {
  id: number;
  path: string;
  line: number;
  body: string;
  user: { login: string };
  created_at: string;
  diff_hunk: string;
  in_reply_to_id: number;
  type: string;
  repo_name?: string;
}

export interface ReviewCommentsResponse {
  comments: ReviewComment[];
  total: number;
}

// M4a — Session attach.
//
// SDKMessage mirrors internal/llm/message.go. Only the fields the
// attach drawer renders are typed; the rest stay as `unknown` so the
// frontend can ignore variants it doesn't know about without
// crashing.

export interface SDKContentBlock {
  type: string;
  text?: string;
  // tool_use blocks
  id?: string;
  name?: string;
  input?: unknown;
  // tool_result blocks
  tool_use_id?: string;
  content?: unknown;
  // thinking blocks
  thinking?: string;
}

// ConversationMsg is the {role, content} envelope inside assistant / user wire frames.
export interface ConversationMsg {
  role?: string;
  content?: SDKContentBlock[];
  model?: string;
}

export interface ControlRequestInner {
  subtype?: string;
  tool_name?: string;
  input?: unknown;
  hook_name?: string;
  callback_id?: string;
}

// SDKMessage is FLAT: variant-specific fields sit at the top level, discriminated by `type`.
// Mirrors internal/llm/message.go's variant structs (AssistantMessage, ControlRequestMessage,
// StatusMessage, ToolProgressMessage, …) — which is the wire shape both Claude's CLI emits
// and what SDKMessage.MarshalJSON re-emits on the WS session feed.
export interface SDKMessage {
  type: string;
  subtype?: string;

  // assistant / user variants — content is at msg.message.content
  message?: ConversationMsg | string;

  // control_request variant
  request_id?: string;
  request?: ControlRequestInner;

  // result variant
  is_error?: boolean;
  duration_ms?: number;
  total_cost_usd?: number;
  stop_reason?: string;

  // status variant — message is a plain string here (typed as union above)

  // tool_progress variant
  tool_name?: string;
  tool_use_id?: string;
  data?: unknown;

  // hook_* variants
  hook_name?: string;
  result?: string;

  // rate_limit variant — message is a string here
  retry_after_ms?: number;

  // init (system.init) variant
  session_id?: string;
  model?: string;
  tools?: string[];
}

export interface SessionMessageEnvelopePayload {
  message: SDKMessage;
}

export interface SessionHistoryEnvelopePayload {
  messages: SDKMessage[];
}

export interface SessionDoneEnvelopePayload {
  status?: string;
}

// M5 — Artifact review, rewind, AskUserQuestion response.

export interface ArtifactRef {
  key: string;
  phase?: string;
  bytes?: number;
  /** MIME type the read endpoint will return for this artifact, derived
   *  server-side from the file extension (text/markdown, application/json,
   *  application/x-yaml, text/plain). Used to pick a renderer
   *  (markdown preview vs raw <pre>) without a HEAD round trip. */
  mime?: string;
}

export interface ArtifactListResponse {
  items: ArtifactRef[];
  pending_review_phase?: string;
}

export type ReviewDecisionValue = "proceed" | "iterate";

export interface ReviewDecisionRequest {
  decision: ReviewDecisionValue;
  target_phase?: string;
  is_rewind?: boolean;
  phase_plan?: boolean;
  roadmap?: boolean;
  comment?: string;
}

export interface RewindRequest {
  target_phase: string;
}

export interface RewindResult {
  warnings?: string[];
  effective_phase: string;
}

export interface AskUserAnnotation {
  notes?: string;
  preview?: string;
}

export interface AskUserRequest {
  request_id: string;
  questions: unknown;
  answers: Record<string, string>;
  annotations?: Record<string, AskUserAnnotation>;
}

export interface CreateFeatureRequest {
  name: string;
  description: string;
  repos: string[];
  models: ModelConfig;
  exit_criteria?: string;
  inquireness?: string;
  risk_level?: string;
  pipeline?: string;
  use_current_branch?: boolean;
  checkpoints: Checkpoints;
  images?: string[];
  attachments?: string[];
}
