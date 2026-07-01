# Review Gates

Review gates are Agentico-owned pauses surfaced through CLI JSON state and watch events.

Watch for `attention_required` events with `kind` set to `review_gate`, or a feature detail snapshot whose review gate fields indicate a pause.

Typical decisions:

| Decision | Request shape |
| --- | --- |
| Proceed | `{"decision":"proceed","phase":"design"}` |
| Iterate | `{"decision":"iterate","phase":"design","comment":"What to change"}` |
| Rewind | `{"decision":"rewind","phase":"plan","comment":"Why"}` |

Use:

```bash
agentico feature review <feature-id> --json --input-json '{"decision":"proceed","phase":"design"}'
```

The harness should summarize the artifact and ask the user for judgment. Agentico owns validation of allowed phases and state transitions.
