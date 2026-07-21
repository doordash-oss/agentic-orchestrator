---
name: guideline-reader
description: Navigate the language-specific coding guidelines to follow best practices, idioms, and conventions when writing or reviewing code.
topics: guidelines, coding standards, best practices, idioms, conventions, error handling, concurrency, naming, testing, style
license: Apache-2.0
provenance: agentic-orchestrator-original
---

# Guideline Reader

You have access to **language-specific coding guidelines** for the target repository. The guidelines discovery table is already inlined in your prompt under "## Available Language Guidelines" — scan it now to see which languages are available.

The guidelines are organized as a hierarchy of markdown documents. Each language has a top-level `index.md` that links to category-specific files containing the actual rules. **Do not stop at the top-level index** — it is an index, not the guidelines themselves.

## When to Read Guidelines

Consult the guidelines **before** you:

| Situation | Read from these categories |
|-----------|---------------------------|
| Write new code | All categories matching the language(s) in use |
| Handle errors | `error-handling/` — wrapping, sentinel errors, error grading |
| Use goroutines, channels, or mutexes | `concurrency/` — patterns, context propagation, synchronization |
| Name variables, functions, or packages | `naming-and-style/` — naming conventions, formatting rules |
| Design interfaces or inject dependencies | `abstractions/` — composition, interface design, DI patterns |
| Write or modify tests | `testing/` — table-driven tests, mocks, benchmarks, fuzz tests |
| Structure packages or modules | `project-organization/` — package layout, module boundaries |
| Work with slices, maps, or structs | `data-types-and-memory/` — allocation patterns, value vs pointer semantics |
| Use stdlib packages (io, net/http, etc.) | `standard-library/` — idiomatic usage of standard library |

## How to Navigate

1. **Scan the inlined guidelines table** in your prompt — it shows each language and the path to its `index.md`
2. **Read the `index.md`** for the language you are working with — it contains a Category Index table with "When to Read" guidance
3. **Read the category `index.md`** for each category relevant to your task (e.g., `error-handling/index.md`)
4. **Read deeper topic files** if the category index references them and they are relevant to your work
5. Only read what you need — typically 2-4 category files per task

## Important Notes

- Guidelines are best practices, not absolute rules. If a guideline conflicts with the existing codebase's established patterns, prefer consistency with the codebase.
- The categories listed above are examples from Go guidelines. Other languages may have different categories — always check the language's `index.md` first.
- Do NOT re-read the guidelines discovery table — its content is already inlined in your prompt.
