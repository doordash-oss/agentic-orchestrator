# Parked Management Dry Run

Harness: user returns to a stopped feature and asks to continue.

$ agentico feature detail feat-cli-parked --json
Result: feature is parked; harness uses a turn-by-turn snapshot instead of watch.

$ agentico feature action feat-cli-parked resume --json
Result: Agentico resumes the feature.
