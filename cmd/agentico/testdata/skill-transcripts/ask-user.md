# Ask User Dry Run

Harness: an agent asks the user to choose an API shape.

$ agentico feature manage feat-cli-1 --json --watch
Result: watch emits `attention_required` for an ask-user request.

$ agentico feature answer feat-cli-1 --json --input-json '{"kind":"ask-user","request_id":"ask-1","answers":{"api_shape":"Use the existing session endpoint."}}'
Result: answer is recorded and the session continues.
