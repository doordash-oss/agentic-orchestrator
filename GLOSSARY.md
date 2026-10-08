# Glossary

Terms are added lazily, by the phase that names or sharpens them.

**Chokepoint.** The orchestrator as the single path through which feature state
changes: every mutation goes through it so guards, hooks, and events apply
uniformly. Lives in `internal/orchestrator`.

**Feature action.** A REST mutation on one feature, addressed by action name on
the feature's action route. The server decodes it and dispatches it to the
mutation target. Lives in `internal/server`.

**Mutation module.** The sub-package that implements the mutation target by
translating server request DTOs into orchestrator operations. It is the only
production implementation. Lives in `internal/server/mutations`.

**Mutation target.** The server's `MutationTarget` interface, which the HTTP
handler calls for every REST mutation. Server tests fake it; production wires
the mutation module. Lives in `internal/server`.

**Read model.** The server's projections from feature state to wire DTOs, such as
the feature summary and detail shapes. Lives in `internal/server`.
