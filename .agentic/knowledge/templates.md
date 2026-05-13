# Templates

## Embedding

Both `agents/` and `skills/` use `go:embed` to include markdown assets at compile time:

- `agents/embed.go`: `//go:embed *.md` → `var FS embed.FS` (package `agents`)
- `skills/embed.go`: `//go:embed */SKILL.md */**/*.md` → `var FS embed.FS` (package `skills`)

## Agent Personas

6 agent persona files in `agents/`:

| Persona | File | Description |
|---------|------|-------------|
| Codebase Analyzer | `codebase-analyzer.md` | Analyzes implementation details |
| Codebase Locator | `codebase-locator.md` | Locates files and components |
| Codebase Pattern Finder | `codebase-pattern-finder.md` | Finds similar patterns and examples |
| Thoughts Analyzer | `thoughts-analyzer.md` | Analyzes thoughts/notes documents |
| Thoughts Locator | `thoughts-locator.md` | Discovers thoughts/notes documents |
| Web Search Researcher | `web-search-researcher.md` | Performs web research |

### Persona Format

Each persona file has:
1. **YAML frontmatter**: `description` (what the agent does), `model` (default model)
2. **Markdown body**: System prompt defining the agent's behavior and capabilities

## Skills

Phase and utility prompts now live under `skills/` as embedded `SKILL.md` trees rather than flat command templates. The tree includes lifecycle skills such as:

- `build-knowledge-base/`
- `research-codebase/`
- `create-roadmap/`
- `plan-phase/`
- `implement/`
- `final-review/`
- `tweak-session/`
- `chat/`

Supporting validator and helper skills live alongside them (for example `validate-*`, `revise-*`, `guideline-reader`, and `knowledge-reader`).

## Skill Loading

`skilldef.ReconcileSkills` materializes the embedded `skills.FS` tree into `~/.agentic-workflow/skills/` at startup. Phase runners then load a specific skill via `agent.BuildSkillInstruction(skillsDir, skillName)`, which resolves to `<skillsDir>/<skillName>/SKILL.md`.

## Skill Format

Each skill is rooted at a `SKILL.md` file with YAML frontmatter plus markdown instructions. Skills can also ship adjacent reference material, user-guide docs, or helper assets under the same subtree.
