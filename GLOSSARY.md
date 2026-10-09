# Glossary

Terms are added lazily, by the phase that names or sharpens them.

**Admission.** The internal per-feature work reservation the orchestrator holds
against the runtime work-admission boundary while the feature owns work. A
closed boundary refuses new work; the reservation is released when the feature
owns no work. Not a caller concern: orchestration operations reserve and settle
it. Lives in `internal/orchestrator`.

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

**Read model.** The server's projections from feature state to wire DTOs, such as
the feature summary and detail shapes. Lives in `internal/server`.

**Relationship lock.** The internal read-write lock that serializes child
creation (write) against relationship-guarded mutations (read), so no guard can
pass while a child is being created. Not a public concept: orchestration
operations take it. Lives in `internal/orchestrator`.
