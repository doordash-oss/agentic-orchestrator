# Create Feature Dry Run

Harness: user asks to build a cross-repo login cleanup.

$ agentico server ensure --json
Result: runtime attached and defaults returned in a schema-versioned envelope.

$ agentico feature select --json
Result: repos and existing features are listed; no duplicate feature is active.

$ agentico feature create --json --input-json '{"name":"Login cleanup","description":"Clean up login flow across web and api","repos":["web","api"],"pipeline":"medium","checkpoints":{"design_review":true,"manual_publish":true}}'
Result: `feat-cli-1` is created.

$ agentico feature action feat-cli-1 start --json
Result: feature starts.

$ agentico feature manage feat-cli-1 --json --watch
Result: watch emits `snapshot` events and later attention events.
