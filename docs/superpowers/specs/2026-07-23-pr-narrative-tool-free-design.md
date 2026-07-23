# Tool-Free PR Narrative Generation

## Goal

PR narrative generation must either return model-authored content or fail visibly. It must never silently substitute a deterministic fallback, surface a permission prompt, or wait for human permission input.

## Design

The orchestrator continues to assemble the complete PR context before launching the utility session: feature name and description, roadmap, selected repositories' commit bodies, and diff statistics. The utility receives that context in its prompt and does not inspect the workspace itself.

The PR-description utility session is tool-free:

- Its system instructions state that all available context is already in the prompt.
- It must produce the requested title and body without requesting tools or additional information.
- No tools are pre-authorized because none are required.
- Any unexpected tool request is answered immediately with `deny`; it is never deferred to the desktop permission UI.
- The denial reason instructs the model to finish with the supplied context.

This policy is scoped to PR narrative generation. Other bounded helpers and feature agents retain their current permission behavior.

## Failure Semantics

The deterministic PR-description fallback is removed from PR narrative generation. Interactive generation and automatic publishing both return an error when:

- the provider or utility session fails;
- the utility requests a tool and cannot complete after the automatic denial;
- the session times out;
- the result lacks either a usable title or a usable body.

The existing server, main-process, IPC, and renderer error path presents an interactive failure in the Publish modal. No generated fields are populated on failure. Automatic publishing records and propagates the generation failure through its existing publish error path without creating a PR from fallback content.

## Prompt Contract

The PR-description prompt includes a concise instruction equivalent to:

> Use only the context supplied in this prompt. Do not request or invoke tools, inspect files, run commands, or ask for more information. Produce the best complete PR title and body possible from the available context.

The output contract remains a non-empty title and body in the existing parseable format.

## Testing

Tests will verify:

- successful model output still produces the parsed title and body;
- provider or helper errors propagate instead of returning fallback content;
- incomplete output fails instead of filling missing fields from fallback content;
- the PR-description session has a deny-all permission handler;
- unexpected Bash, read, write, web, and agent-tool requests are denied without deferral;
- the system instruction tells the model to complete using supplied context without tools;
- the desktop narrative timeout regression remains covered.

The required handoff verification is the Fast suite plus Go vet/build, desktop typecheck/build, focused agent and desktop tests, and targeted lint/format checks. Any unrelated failures from concurrent workspace changes are reported separately.
