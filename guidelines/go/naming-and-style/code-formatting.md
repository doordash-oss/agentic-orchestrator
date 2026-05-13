# Code Formatting

## gofmt and goimports

Run `gofmt` or `goimports` on all code. This is not optional. `goimports` is
preferred because it also manages import lines.

**Go proverb**: "Gofmt's style is no one's favorite, yet gofmt is everyone's
favorite." — automated formatting eliminates bikeshedding.

Key formatting rules (handled by gofmt):
- Tabs for indentation, never spaces
- No line length limit
- Opening braces on the same line as the control structure (required by Go's
  semicolon insertion rules)
- Minimal parentheses: `if`, `for`, `switch` don't take parens around conditions

## Import Organization

Imports are grouped with blank lines between groups:

```go
import (
    "fmt"                          // Group 1: standard library
    "os"

    "github.com/foo/bar"           // Group 2: third-party
    "golang.org/x/sync/errgroup"

    "mycompany.com/repo/internal"  // Group 3: internal/local
)
```

- `goimports` manages this automatically.
- Avoid renaming imports unless there's a collision. When renaming, rename the
  most local/project-specific import.
- `import _` (blank import for side effects) only in `main` packages and tests.
- `import .` (dot import) only in test files with circular dependency issues.

## Doc Comments

Every exported name must have a doc comment. Start with the name of the thing
being described, end with a period:

```go
// Request represents a request to run a command.
type Request struct { ... }

// Encode writes the JSON encoding of req to w.
func Encode(w io.Writer, req *Request) { ... }

// Package math provides basic constants and mathematical functions.
package math
```

For packages with extensive documentation, use a separate `doc.go` file
containing only the package comment and `package` clause.

### Deprecation

```go
// Deprecated: Use NewFoo instead.
func OldFoo() { ... }
```

## Comment Style

- Comments are full sentences with proper capitalization and punctuation.
- Use `//` for line comments (preferred). Use `/* */` only for package-level
  block comments.
- Don't add comments that restate the code. Comments explain **why**, not **what**.
- No trailing comments on closing braces.

## Line Length

There is no rigid limit. Rules:
- Avoid uncomfortably long lines.
- Don't wrap lines artificially when longer lines are more readable.
- If lines are too long, fix the names or semantics, not the line length.
- Break lines because of **semantics**, not character count.

## Control Flow Formatting

### Guard Clauses — Flatten with Early Returns

```go
// Anti-pattern: nested
if x > 0 {
    if y > 0 {
        return x + y
    }
    return x
}
return 0

// Correct: guard clauses
if x <= 0 {
    return 0
}
if y <= 0 {
    return x
}
return x + y
```

### Switch over Long If-Else Chains

```go
// Anti-pattern:
if c == 'a' {
    ...
} else if c == 'b' {
    ...
} else if c == 'c' {
    ...
}

// Correct:
switch c {
case 'a':
    ...
case 'b':
    ...
case 'c':
    ...
}
```

### Labeled Break for Nested Loops

```go
Loop:
    for _, item := range items {
        switch item.Type {
        case TypeDone:
            break Loop // breaks outer for, not just switch
        }
    }
```

## Grouped Declarations

Group related declarations together:

```go
var (
    ErrNotFound   = errors.New("not found")
    ErrPermission = errors.New("permission denied")
)

const (
    StatusPending  = "pending"
    StatusActive   = "active"
    StatusInactive = "inactive"
)
```
