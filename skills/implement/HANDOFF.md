# Implement Smart Zone Handoff

You are above the Smart Zone threshold. Wind this iteration down so the next iteration can resume with fresh context.

1. Do not start new implementation work or substantial new tool calls.
2. Write `progress.md` using the exact section schema in `skills/implement/SKILL.md`.
3. Set `## Iteration State` to `RETRY`.
4. Do not include `## Questions for User`; that section is only for `NEED_USER_INPUT`.
5. Write `verification-report.yaml` at the runtime path with every result you have actually run. Leave checks you have not run as `not_run`.
6. Touch the `phase_complete` marker as the very last action, then end the turn.
