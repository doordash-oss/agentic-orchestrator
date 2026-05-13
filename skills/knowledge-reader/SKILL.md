---
name: knowledge-reader
description: Navigate the pre-built Knowledge Base graph to discover repo conventions, architecture, testing patterns, and verification strategies before coding.
topics: knowledge base, KB, conventions, testing, architecture, patterns, verification, dependencies, code style, error handling
---

# Knowledge Base Reader

You have access to a pre-built **Knowledge Base (KB)** for the target repository. The KB index path and KB directory are listed in the **Pre-flight** section of your prompt — read the KB index first to see what categories are available.

The KB is a structured graph of markdown documents organized into categories. Each category has an index file and detailed leaf files. Start by reading the KB index, then use the KB directory path to read specific leaf files relevant to your task.

## When to Read KB Leaf Files

Consult the KB **before** you:

| Situation | Read from these KB categories |
|-----------|------------------------------|
| Write or modify code | `conventions/` — read the index, then leaf files for error handling, naming, code style |
| Write or modify tests | `verification/` + `conventions/` — test patterns, test infrastructure, testing conventions |
| Make architectural decisions | `architecture/` — components, data flow, key abstractions |
| Use or extend APIs | `api-surface/` — services, client libraries, config schemas |
| Add dependencies | `dependencies/` — approved libraries, version constraints |
| Set up verification steps | `verification/` — test suites, CI pipeline, verification order |

## How to Navigate

1. **Read the KB index** from the path listed in Pre-flight — it shows the category map, build commands, key architecture decisions, and important file paths
2. **Identify which categories** are relevant to your current task using the table above
3. **Read the category's `index.md`** first (e.g., `<KB Directory>/conventions/index.md`) to discover the actual leaf filenames — these vary per repo
4. **Read the relevant leaf files** based on what the category index lists
5. Only read what you need — typically 2-4 leaf files per task

## Category Overview

| Category | What it covers |
|----------|---------------|
| `architecture/` | Component maps, data flow patterns, dependency injection modules, key abstractions, concurrency and persistence models |
| `conventions/` | Error handling patterns (including error grading), naming conventions, testing patterns, code style, logging and context propagation |
| `api-surface/` | gRPC services, CLI tools, client library API, configuration schemas, environment variables, Kafka event contracts |
| `dependencies/` | Go library inventory, build tools (Task, Bazel, etc.), runtime services (Cassandra, CRDB, Kafka), version constraints |
| `verification/` | Unit test patterns, integration test infrastructure, CI pipeline, linting rules, recommended verification order |

## Important Notes

- The KB may be slightly behind the current codebase. If KB content conflicts with what you see in the actual source code, trust the source code.
