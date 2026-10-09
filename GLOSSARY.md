# Glossary

Terms are added lazily, by the phase that names or sharpens them.

**Admission.** The internal per-feature work reservation the orchestrator holds
against the runtime work-admission boundary while the feature owns work. A
closed boundary refuses new work; the reservation is released when the feature
owns no work. Not a caller concern: orchestration operations reserve and settle
it. Lives in `internal/orchestrator`.

**Answer key.** The key of one answer in an ask-user or need-user-input answer
map: the answered question's question index as a decimal string, such as `"1"`.
Values are an option label verbatim or free text. No other key form is
accepted. See ADR 0009.

**Artifact validator.** The function bound to one artifact in a role spec. It
reads the artifact from its resolved path and reports protocol violations and
parsed outcome fields. Lives in `internal/agent`.

**Ask-user turn.** The moment a provider's agent asks the operator a structured
question: one `AskUserQuestion` envelope of questions, each with text, a header,
a multi-select flag and options carrying a label, a description and an optional
confidence. The `askuser` module parses, encodes, signs and bounds it. Lives in
`internal/llm/askuser`.

**Auto-pick signature.** The confidence-free identity of an ask-user turn,
derived from each question's text, header and multi-select flag and each
option's label and description. A control request and the assistant tool-use
block that carried option confidence share it, which is how the session's
auto-pick and the read model recover missing confidence. Lives in
`internal/llm/askuser`.

**Chokepoint.** The orchestrator as the single path through which feature state
changes: every mutation goes through it so guards, hooks, and events apply
uniformly. Lives in `internal/orchestrator`.

**Feature action.** A REST mutation on one feature, addressed by action name on
the feature's action route. The server decodes it and dispatches it to the
mutation target. Lives in `internal/server`.

**Mutation module.** The sub-package that implements the mutation target by
translating server request DTOs into orchestration operations. It holds no
feature store or feature manager handle and sequences no locks, admission, or
dispatch. It owns the server's result vocabulary (`started`, `created`,
`updated`, and the rest) and returns the zero response on error. It is the only
production implementation. Lives in `internal/server/mutations`.

**Mutation target.** The server's `MutationTarget` interface, which the HTTP
handler calls for every REST mutation. On success the target fills the feature
id and result of every response that has those fields; the handler only stamps
the API version and discards the response on error. Server tests fake it, and a
fake must fill those fields too; production wires the mutation module. Lives in
`internal/server`.

**Orchestration operation.** A single exported orchestrator method that owns
the relationship lock, admission, and dispatch order for one feature mutation,
such as stop, restart, retry, setup dispatch, child launch, delete, config
update, or rewind. It is the only way callers sequence those concerns. Lives in
`internal/orchestrator`.

**Question index.** The one-based position of a question in its ask-user turn
or need-user-input gate. The server emits it with every question and clients
echo it back as the answer key. Gate files store it, and the gate reader rejects
an index that is not positive or not unique.

**Read model.** The server's projections from feature state to wire DTOs, such as
the feature summary and detail shapes. Lives in `internal/server`.

**Relationship lock.** The internal read-write lock that serializes child
creation (write) against relationship-guarded mutations (read), so no guard can
pass while a child is being created. Not a public concept: orchestration
operations take it. Lives in `internal/orchestrator`.

**Role.** The identity an agent session runs under, named by a phase and a
role. The harness validates the session's completion artifacts against it.
Lives in `internal/agent`.

**Role contract.** The derived set of required artifacts, each with its
resolved path and validator, that lookup by phase and role returns and validate
checks. Lives in `internal/agent`.

**Role spec.** The single declaration of one phase-and-role pairing: its skill,
output roots, artifacts with bound validators, and prompt posture flags. Lives
in `internal/agent`.

**Schema series.** The monotonic REST contract series within the API major,
declared on `/api/v1/health` together with the minimum client series the
server accepts. It is bumped whenever the wire contract changes in a way
existing clients cannot tolerate, and a client and server from different series
refuse each other. Distinct from the discovery record's format version. Lives
in `internal/server` and, on the desktop side, `desktop/src/main/gateway`.
