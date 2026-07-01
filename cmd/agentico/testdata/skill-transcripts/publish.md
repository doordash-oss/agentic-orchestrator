# Publish Dry Run

Harness: user approves publishing a code-ready feature.

$ agentico feature detail feat-cli-1 --json
Result: feature is code-ready and publishable.

$ agentico feature action feat-cli-1 publish --json --input-json '{"repos":["web","api"],"title":"Clean up login flow","body":"Implements the approved Agentico feature."}'
Result: Agentico publishes the PRs and returns structured metadata.
