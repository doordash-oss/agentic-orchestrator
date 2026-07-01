# Active Manage Dry Run

Harness: user asks for status while the feature is running.

$ agentico feature detail feat-cli-1 --json
Result: feature is active in implementation.

$ agentico feature manage feat-cli-1 --json --watch
Result: watch emits `snapshot` events until attention is required.
