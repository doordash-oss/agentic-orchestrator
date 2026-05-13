# Error Handling — Index

> **This file is an index.** The actual rules and examples live in the topic files below.
> You MUST read at least the topic files relevant to your task.

## Topics

| File | When to Read |
|------|-------------|
| [wrapping-and-context.md](wrapping-and-context.md) | Using `fmt.Errorf` with `%w`, `errors.Is`/`errors.As`, choosing wrapping depth |
| [sentinel-and-custom-errors.md](sentinel-and-custom-errors.md) | Defining `ErrXxx` sentinels, custom error types, error hierarchies |
| [panic-and-recover.md](panic-and-recover.md) | When to panic vs return error, recover patterns, goroutine crash isolation |
| [error-flow-patterns.md](error-flow-patterns.md) | The errWriter pattern, logging vs returning, HTTP handler errors, main() errors |
