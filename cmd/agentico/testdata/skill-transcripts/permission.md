# Permission Dry Run

Harness: an agent requests command permission.

$ agentico feature manage feat-cli-1 --json --watch
Result: watch emits `attention_required` for a permission request.

$ agentico feature answer feat-cli-1 --json --input-json '{"kind":"permission","request_id":"perm-1","session_id":"session-1","decision":"allow"}'
Result: permission decision is recorded.
