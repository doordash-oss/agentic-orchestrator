# Management Actions

All management goes through Agentico CLI JSON.

| User intent | Command family |
| --- | --- |
| Inspect current state | `agentico feature detail <feature-id> --json` |
| Watch an active feature | `agentico feature manage <feature-id> --json --watch` |
| Start or resume work | `agentico feature action <feature-id> start --json` or `resume` |
| Stop or interrupt work | `agentico feature action <feature-id> stop --json` or `interrupt` |
| Retry failed work | `agentico feature action <feature-id> retry --json` |
| Rewind to an earlier phase | `agentico feature action <feature-id> rewind --json` |
| Rebase, tweak, refactor, review comments | `agentico feature action <feature-id> <action> --json` |
| Answer planning/user prompts | `agentico feature answer <feature-id> --json` |
| Answer permission prompts | `agentico feature answer <feature-id> --json` |
| Approve or reject a review gate | `agentico feature review <feature-id> --json` |
| Publish as PR | `agentico feature action <feature-id> publish --json` |
| Merge local-only work | `agentico feature action <feature-id> merge --json` |
| Mark done, cleanup, delete | `agentico feature action <feature-id> <action> --json` |

The harness maps user decisions into request JSON. Agentico decides whether an action is currently valid and returns structured success or error envelopes.
