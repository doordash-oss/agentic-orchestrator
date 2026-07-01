# Review Gate Dry Run

Harness: user must decide whether the design gate can proceed.

$ agentico feature manage feat-cli-1 --json --watch
Result: watch emits `attention_required` with kind `review_gate`.

$ agentico feature review feat-cli-1 --json --input-json '{"decision":"proceed","phase":"design","comment":"Design matches the requested scope."}'
Result: Agentico accepts the review decision and resumes the workflow.
