export type EventType =
  | "feature.created"
  | "feature.started"
  | "feature.advanced"
  | "feature.completed"
  | "feature.failed"
  | "feature.interrupted"
  | "phase.started"
  | "phase.completed"
  | "review.required"
  | "publish.started"
  | "publish.completed"
  | "recovery.scanned"
  | "recovery.executed"
  | "tweak.review_approved"
  | "feature.config_changed"
  | "need_user_input.required"
  | "server.dropped"
  | "pong"
  | "hello"
  // Legacy session frames kept for the activity renderer's exhaustive switch.
  | "session.history"
  | "session.message"
  | "session.done";

export interface FeaturePayload {
  feature_id: string;
}

export interface FeatureCreatedPayload {
  feature_id: string;
  slug?: string;
  name?: string;
  status?: string;
}

export interface FeaturePhasePayload {
  feature_id: string;
  phase: string;
}

export interface FeatureMessagePayload {
  feature_id: string;
  phase?: string;
  message?: string;
  errored?: boolean;
}

export interface PhaseResultPayload {
  feature_id: string;
  phase?: string;
  errored?: boolean;
}

export interface MessagePayload {
  message?: string;
}

export interface ServerDroppedPayload {
  dropped: number;
}

export interface HelloPayload {
  protocol_version: number;
  server_time: string;
}

// Envelope is the outer wire shape. Payload is event-specific.
export interface Envelope<P = unknown> {
  v: number;
  type: EventType;
  seq?: number;
  ts: string;
  payload?: P;
}

// Discriminated alias the dispatcher can switch on.
export type AnyEnvelope =
  | Envelope<HelloPayload>
  | Envelope<FeatureCreatedPayload>
  | Envelope<FeaturePayload>
  | Envelope<FeaturePhasePayload>
  | Envelope<FeatureMessagePayload>
  | Envelope<PhaseResultPayload>
  | Envelope<MessagePayload>
  | Envelope<ServerDroppedPayload>;
