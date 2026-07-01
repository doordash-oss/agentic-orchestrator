# Creation Options

Create features with `agentico feature create --json` and a JSON body shaped like Agentico's `CreateFeatureRequest`.

Common fields:

| Field | Meaning |
| --- | --- |
| `name` | Short feature name. Required unless Agentico adds a default in the future. |
| `description` | User goal and constraints. |
| `repos` | Repository names selected from CLI JSON discovery. |
| `models` | Optional per-phase model overrides. |
| `exit_criteria` | Explicit completion criteria from the user. |
| `inquireness` | How often planning questions should surface. |
| `images` | Image paths to include as visual context. |
| `attachments` | File paths to include as context. |
| `use_current_branch` | Apply current branch behavior across repos. |
| `use_current_branch_per_repo` | Per-repo current branch overrides. |
| `checkpoints` | Review gate toggles. |
| `risk_level` | Risk classification for validation depth. |
| `pipeline` | Pipeline profile, such as small, medium, large, or moonshot. |

Harnesses should collect only decisions the user needs to make. Leave omitted fields to Agentico defaults whenever CLI JSON discovery already gives a safe default.
