# Watch Events

`agentico feature manage <feature-id> --json --watch` emits normalized events. The harness should treat these as authoritative and avoid per-harness state classifiers.

Known event types:

| Type | Meaning |
| --- | --- |
| `snapshot` | Full or partial feature state at a point in time. |
| `attention_required` | User input, permission, or review gate is waiting. |
| `terminal` | Feature reached a terminal state such as published, done, or failed. |

Known attention kinds:

| Kind | Follow-up |
| --- | --- |
| `need_user_input` | Use `feature answer` with kind `need-user-input` or `need-user-input-draft`. |
| `ask_user` | Use `feature answer` with kind `ask-user`. |
| `permission` | Use `feature answer` with kind `permission`. |
| `review_gate` | Use `feature review`. |

When the stream ends or the feature is parked, take a fresh `feature detail` snapshot on the next user turn.
