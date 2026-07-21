---
description: Build or update a persistent repo knowledge base graph
license: Apache-2.0; HumanLayer inspiration acknowledged in ATTRIBUTION.md
provenance: upstream-inspired
---

# Build Knowledge Base Graph

You are building a knowledge base (KB) graph for a code repository.
The KB is a structured directory of focused documents — NOT a single monolithic file.

## Output Files

| Artifact | Path | Requirement | Purpose |
|----------|------|-------------|---------|
| `index.md` | `{phase_dir}/index.md` | required | top-level knowledge-base graph index markdown |

## CRITICAL: YOUR ONLY JOB IS TO DOCUMENT THE CODEBASE

- DO NOT edit or modify any code files — you are a documenter, not an implementer
- DO NOT suggest improvements or changes
- DO NOT critique the implementation
- ONLY describe what exists, where it exists, how it works, and how components interact
- You are creating a technical map/documentation of the existing system

## YOUR JOB

Produce a **knowledge base graph**: a directory structure with:
1. A top-level `index.md` (~100-200 lines) that serves as the entry map
2. Category subdirectories (you decide which categories fit the repo)
3. Per-category `index.md` files summarizing that category
4. Focused leaf files for specific topics

The goal is **progressive disclosure**: the index tells an agent what exists and
where to find it. Agents read the index first, then drill into specific files
only when they need deeper detail.

## Output Structure

Write all files under the KB Root Directory specified in the prompt.

```
<KB Root Directory>/
├── index.md                    # THE ENTRY POINT — concise map
├── <category-1>/
│   ├── index.md                # category overview
│   └── <topic>.md              # focused deep-dives
├── <category-2>/
│   ├── index.md
│   └── <topic>.md
└── ...
```

### Top-level index.md

This is the MOST IMPORTANT file. Keep it concise (~100-200 lines). It should contain:
- One-paragraph project overview
- Directory structure summary (top-level only)
- Build/test/run commands (the most commonly needed info)
- A **category map**: for each category dir, a one-line description and link
- Key architectural decisions or patterns (brief bullets, link to details)

Think of it as: "If an agent reads ONLY this file, what do they need to
navigate the rest of the KB effectively?"

### Category Directories

Each knowledge base has two kinds of categories: **standard** and **custom**.

**Standard categories** have dedicated researcher sub-agents that produce
high-quality, structured output. Spawn all of them in parallel.
Include a standard category whenever the researcher produces meaningful findings
(omit only if the repo genuinely has nothing for that category — e.g., no API
surface for a pure library with no CLI).

| Category | Researcher Agent | What It Covers |
|----------|-----------------|----------------|
| `architecture/` | **architecture-researcher** | Component maps, module boundaries, data flow, entry points, key abstractions |
| `conventions/` | **conventions-researcher** | Coding patterns, error handling, naming, testing patterns, formatting rules |
| `api-surface/` | **api-surface-researcher** | REST/gRPC/GraphQL endpoints, CLI commands, configs, exports, event contracts |
| `dependencies/` | **dependencies-researcher** | Third-party libraries, build tools, runtime services, system requirements |
| `verification/` | **verification-researcher** | Test suites, build checks, linting, CI commands, app execution, AI tool verification |

**Custom categories** are ones you create based on what the repo needs. A frontend
repo might have `components/`, `state-management/`. A microservice might have
`data-models/`, `middleware/`. Use your judgment — but prefer using standard
categories where they apply.

Each category directory MUST have an `index.md` that:
- Summarizes what's in the category
- Lists all leaf files with one-line descriptions

### MANDATORY: verification/ Category

Every knowledge base MUST include a `verification/` category. This is not optional.

This KB feeds a pipeline of AI agents that research, plan, implement, and review
code changes. The final objective is for the full pipeline to converge on a
correct result — a PR that works on the first try without the developer needing
to discover it is broken. That convergence is only possible if the implementation
and review agents can **verify their own work** by executing the repo's
verification methods. Without this category, downstream agents are flying blind.

The `verification/` category MUST document every available verification method
in the repository. Verification methods include but are not limited to:

- **Test suites** — unit, integration, e2e, smoke, contract, performance tests.
  Document the framework, exact command, prerequisites, scope, and approximate speed.
- **Build/compilation commands** — commands that catch type errors, import issues,
  and syntax errors. Compilation is verification.
- **Linting & static analysis** — linters, type checkers, formatters run in check
  mode, security scanners. Include config file paths.
- **CI pipeline commands** — extract the actual shell commands from CI configs.
  These are the authoritative verification sequence for the repo.
- **Task runner targets** — Makefile, Taskfile, package.json scripts, justfile
  targets related to testing, building, or linting.
- **Application execution** — how to start/run the application, exercise CLI
  subcommands, hit HTTP endpoints, or otherwise run the codebase in a sandbox.
  When no automated test infrastructure exists, this is how agents verify changes.
- **AI tool verification** — Claude Code skills or commands in `.claude/` that
  perform testing or verification workflows.

For each method, the documentation must include the **exact command** to run,
**prerequisites** (environment, services, build steps), and **what it verifies**.

There is ALWAYS at least one verification method — even if the repo has no tests,
it can be compiled, linted, or run. Find it and document it.

### Leaf Files

Focused documents on specific topics. Keep them under 200 lines each.
Include file paths and code references where helpful.

## For FULL BUILDS (no existing KB):

1. Spawn **all standard-category researcher agents in parallel**, plus
   **codebase-locator** for an initial directory/file map:
   - **codebase-locator** — map the directory structure and key files
   - **architecture-researcher** → feeds `architecture/` category
   - **conventions-researcher** → feeds `conventions/` category
   - **api-surface-researcher** → feeds `api-surface/` category
   - **dependencies-researcher** → feeds `dependencies/` category
   - **verification-researcher** → feeds `verification/` category (mandatory)
2. Collect all researcher outputs. Decide on any additional custom categories.
3. Create the full directory structure with all files. For each standard
   category, organize the corresponding researcher's output into an `index.md`
   and focused leaf files.
4. Write the top-level index.md LAST (so it accurately reflects what you created)

## For INCREMENTAL UPDATES (existing KB provided):

1. Read the existing index.md completely
2. Use git to understand what changed since the last build:
   - `git log --oneline <last-commit>..HEAD` for commit messages
   - `git diff --stat <last-commit>..HEAD` for changed file summary
   - Read specific changed files as needed
3. Browse the existing category directories
4. Update ONLY the files affected by the changes
5. Add new files/categories if the changes introduce new areas
6. For any standard category affected by the changes, re-run its researcher
   agent to get fresh data. Use the `git diff --stat` output to determine which
   categories are impacted (e.g., changes to test files → re-run
   verification-researcher; changes to API routes → re-run api-surface-researcher)
7. If any standard category doesn't exist yet, create it (run its researcher)
8. Update the top-level index.md if the structure changed
9. Preserve unchanged files verbatim — do NOT rewrite them

## Important Rules

- This is a DOCUMENTATION task — do NOT modify any code
- The KB should be useful to both humans and AI agents
- Keep the index concise — it's a map, not a manual
- Each file should be self-contained and focused on one topic
- Include file paths for key components (e.g., `internal/agent/phase.go:136`)
- Focus on "what" and "how", not "why"
- Do NOT create empty category directories — only create categories with content
