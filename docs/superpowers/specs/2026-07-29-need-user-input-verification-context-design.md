# Contextual Verification Input Gates

## Problem

Harness verification pauses currently reach the desktop as a generic summary and
a free-text question that asks the user to type `WAIVE` or
`RETRY_AFTER_AUTH`. The testing contract and verification report know which
checks were blocked, what each check does, and why it could not run, but gate
synthesis discards that information. The modal therefore asks for a
consequential decision without enough context to make it.

## Goals

- Explain which required verification checks could not run and why.
- Tell the user what must change before a retry can succeed.
- Replace verification magic-string entry with explicit retry and waiver
  choices.
- Make waiver consequences clear before the user resumes.
- Present the same decision consistently in the feature modal and Attention
  inbox.
- Preserve the existing questionnaire experience for generic and legacy gates.

## Non-goals

- Redesigning live `AskUserQuestion` requests or permission prompts.
- Changing verification classification or waiver semantics.
- Allowing the desktop to read server-side artifact paths directly.
- Adding new verification outcomes beyond retry-after-auth and waiver.

## Approach

Enrich the existing gate record at the point where the harness has both the
testing contract and completed verification report. Carry the structured data
through the server API, desktop validation, main-process mapping, and renderer.
The renderer selects the structured verification experience only when the
optional decision payload is present.

Encoding the details into prompt prose was rejected because it would make the
UI parse presentation text. Reading artifacts from Electron was rejected
because attached servers may not share the desktop filesystem and the renderer
must not receive arbitrary server paths.

## Gate Data Model

`NeedUserInputRecord` gains an optional structured verification presentation
payload alongside the existing trusted `verification_decision`:

- Blocked checks:
  - stable item ID;
  - user-facing name;
  - repository, when scoped to one;
  - command or requirement;
  - exact blocked reason from the verification result;
  - declared missing capability names;
  - a concise retry instruction derived from the blocker type.
- Allowed actions:
  - `RETRY_AFTER_AUTH`;
  - `WAIVE`.

The trusted contract path and revision remain internal to the gate artifact and
continue to protect waiver application against stale requirements. Filesystem
paths, probe commands, and raw stdout/stderr paths are not exposed through the
API.

Gate synthesis receives the testing contract and verification report that
produced the blocked item IDs. It joins those sources by item ID and persists a
snapshot of the relevant user-facing context. Persisting the snapshot keeps the
decision stable even if other run artifacts later change.

The API adds optional `verification` data to `NeedUserInputGate`. Generated Go
and TypeScript types, runtime parsers, and the strict IPC schema carry the same
bounded fields. Generic gates and pre-change artifacts omit the field and
remain valid.

## Desktop Experience

For a structured verification gate, the modal title becomes "Verification
needs your input." Its summary states how many required checks could not run.
Each blocked check is rendered as a compact context card containing:

- check name and repository;
- command or requirement;
- the precise reason it was blocked;
- what the user should make available before retrying.

Technical commands use monospace styling and wrap safely. Internal item IDs are
not the primary label but may appear as secondary diagnostic context when no
name exists.

Below the cards, two explicit, mutually exclusive choices replace the textarea:

1. **I've granted access — retry verification.** Explain that the harness will
   rerun the blocked checks from the same iteration.
2. **Waive blocked checks and continue.** Explain that the checks will be
   recorded as user-authorized waivers and will not be run.

The user selects one choice before the footer action is enabled. The footer
label reflects the choice: "Retry verification" or "Waive and resume." The
waiver choice and action use warning styling. This two-step selection preserves
the deliberateness previously provided by typing `WAIVE`.

The Attention inbox uses the same blocker presentation and explicit choices so
the outcome does not depend on where the user resolves the gate. Draft
persistence continues to store the selected action in the existing question
answer slot, which avoids a mutation-protocol change.

Generic or legacy gates without structured verification data continue to show
their summary, questions, textareas, and existing resume behavior.

## Data Flow

1. The deterministic executor records blocked verification results.
2. Implementation gate synthesis joins blocked IDs to contract items and report
   results, then writes the enriched gate artifact.
3. The read model sanitizes and serializes the optional verification context.
4. The desktop main process validates and maps it into the strict Attention
   item IPC contract.
5. The modal or inbox renders the structured decision.
6. Selecting an option saves `RETRY_AFTER_AUTH` or `WAIVE` through the existing
   draft endpoint.
7. Resume uses the existing decision endpoint. The orchestrator applies the
   same trusted, revision-checked semantics as today.

## Error Handling and Compatibility

- Missing or unreadable gate artifacts retain the current minimal DTO behavior.
- Missing contract/report matches omit the affected optional display fields;
  the item ID remains as a final fallback label.
- Unknown allowed actions are ignored by the desktop rather than rendered.
- A verification gate without both supported actions falls back to the generic
  questionnaire, avoiding a misleading partial decision UI.
- Server-side answer validation remains authoritative. Stale contract revisions
  continue to reject waiver application with the existing error surfaced in
  the modal or inbox.
- The API and IPC bound collection sizes and text lengths using their existing
  attention limits.

## Testing

### Agent and server

- Gate synthesis captures check name, repository, command, capability, blocked
  reason, and remediation from a representative missing-capability result.
- Environment-limited blockers receive an accurate retry instruction without
  inventing a login requirement.
- Existing waiver, retry, stale-revision, and legacy record tests remain valid.
- Read API contract tests cover enriched feature- and cycle-scoped gates and
  confirm internal artifact paths are absent.

### Desktop

- Runtime API parsing and main-process Attention mapping preserve the optional
  structured context.
- Modal tests assert blocker context, explicit mutually exclusive choices,
  contextual footer labels, warning treatment, draft persistence, and exact
  cycle routing.
- Attention inbox tests assert the same decision behavior.
- Legacy gate tests continue to assert textarea fallback.
- The packaged attention-resolution journey seeds a structured verification
  gate and resolves retry-after-auth without typing a magic string.

## Verification Tiers

Run before handoff:

- **Fast suite:** `make test-fast`
- **E2E smoke shell:** `bash test/e2e/smoke.sh` because the change touches an
  embedded verification gate and resume behavior.
- **Isolated integration:** `go test ./test/integration/... -count=1` because
  the change touches same-iteration lifecycle resume.
- **E2E Go:** `go test ./test/e2e/... -count=1 -race` because the change affects
  session lifecycle after gate resolution.
- Static analysis: `go vet ./...`
- Build: `go build ./...`
- Relevant desktop unit and packaged journey commands from `desktop/package.json`.

The **Race regression** tier is not required unless implementation introduces
concurrency-sensitive behavior.
