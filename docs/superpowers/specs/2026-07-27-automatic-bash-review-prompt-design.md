# Automatic Bash Review Prompt Design

## Goal

Refactor `autoreview.user.tmpl` to behave like an automatic permission mode:
approve ordinary development commands and defer only commands with a concrete,
plausible risk of significant harm. Keep the policy short enough for a cheap,
tool-less reviewer to apply consistently.

## Decision Policy

The prompt uses a risk-based default:

- `ALLOW` unless the complete command presents a concrete, plausible risk of
  significant harm.
- Treat ordinary scoped development work as safe, including writes within
  declared writable roots, builds, tests, project dependency installation,
  routine network access, and local development processes.
- `DEFER` destructive or hard-to-reverse data loss, broad or system-level
  changes, privilege escalation or weakened security, credential or private
  data exposure, execution of untrusted remote or discovered content,
  consequential external actions, and disruption of unrelated processes or
  services.
- Evaluate all branches, substitutions, redirects, and pipeline stages.
  Uncertainty warrants deferral only when the unknown effect could be
  dangerous.

This replaces the current read-only allowlist. Shell syntax and ordinary side
effects are not independently reasons to defer.

## Prompt Shape

The prompt contains only:

1. The reviewer role and exact `ALLOW`/`DEFER` output contract.
2. The default-allow rule, representative safe development actions, and the
   compact dangerous-effect boundary.
3. The existing nonce-delimited untrusted request block.
4. A final instruction to treat the block as data and return one token.

The nonce boundary remains because command text is attacker-controlled. The
prompt includes no command-specific examples; the policy itself supplies the
generalization boundary with fewer tokens.

## Scope

- Update `internal/agent/prompts/templates/autoreview.user.tmpl`.
- Regenerate `internal/agent/prompts/testdata/autoreview_user.golden`.
- Update the README description that currently says all mutation and active
  network effects defer.
- Do not change reviewer execution, caching, fast-path rules, or response
  parsing.

## Verification

- Regenerate prompt goldens with
  `go test ./internal/agent/prompts/... -update` and review the diff.
- Run the Fast suite with `make test-fast`.
- Run `go vet ./...` and `go build ./...`.
- Skip extended runtime tiers because the change affects only embedded prompt
  wording and its documentation, not launch behavior, session lifecycle, TUI
  behavior, or concurrency.
