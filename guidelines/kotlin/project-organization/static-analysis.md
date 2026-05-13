# Static Analysis

Static analysis tools catch bugs, enforce style, and maintain consistency across
a Kotlin codebase. The standard toolkit is detekt for code smells and complexity
checks, ktlint (or ktfmt) for formatting, and strict Kotlin compiler options.
All three should run in CI and block merges on failure.

## Detekt Setup

Detekt is the primary static analysis tool for Kotlin. Add it to the root
build file or a convention plugin:

```kotlin
plugins {
    id("io.gitlab.arturbosch.detekt") version "1.23.6"
}

detekt {
    config.setFrom("$rootDir/config/detekt/detekt.yml")
    buildUponDefaultConfig = true   // Layer custom config on top of defaults
    allRules = false                // Only enable rules explicitly configured
    parallel = true                 // Analyze modules in parallel
}

dependencies {
    detektPlugins("io.gitlab.arturbosch.detekt:detekt-formatting:1.23.6")
}
```

For multi-module projects, apply detekt in the convention plugin so every module
is analyzed:

```kotlin
// buildSrc/src/main/kotlin/kotlin-conventions.gradle.kts
plugins {
    kotlin("jvm")
    id("io.gitlab.arturbosch.detekt")
}
```

Run with `./gradlew detekt`. The task fails if any rule violations are found.

## Key Detekt Rule Sets

Configure rules in `config/detekt/detekt.yml`:

```yaml
complexity:
  LongMethod:
    threshold: 30
  CyclomaticComplexMethod:
    threshold: 10
  LongParameterList:
    functionThreshold: 5
    constructorThreshold: 8

coroutines:
  GlobalCoroutineUsage:
    active: true
  SuspendFunSwallowedCancellation:
    active: true

naming:
  FunctionNaming:
    functionPattern: '[a-z][a-zA-Z0-9]*'
    excludes: ['**/test/**']     # Allow backtick names in tests

performance:
  SpreadOperator:
    active: true

style:
  MagicNumber:
    ignoreNumbers: ['-1', '0', '1', '2']
    ignoreHashCodeFunction: true
  MaxLineLength:
    maxLineLength: 120
  WildcardImport:
    active: true
```

Important rule sets:
- **complexity** — Method length, cyclomatic complexity, parameter counts
- **coroutines** — GlobalScope usage, cancellation handling
- **empty-blocks** — Empty catch, if, function bodies
- **naming** — Naming conventions for classes, functions, variables
- **performance** — Spread operator on varargs, unnecessary allocations
- **style** — Magic numbers, wildcard imports, line length

## Custom Detekt Rules

Create project-specific rules for domain conventions:

```kotlin
class NoDirectDatabaseAccessRule(config: Config) : Rule(config) {
    override val issue = Issue(
        id = "NoDirectDatabaseAccess",
        severity = Severity.Defect,
        description = "Feature modules must not access the database directly",
        debt = Debt.TWENTY_MINS
    )

    override fun visitImportDirective(importDirective: KtImportDirective) {
        val importPath = importDirective.importedFqName?.asString() ?: return
        if (importPath.startsWith("org.jetbrains.exposed") ||
            importPath.startsWith("java.sql")) {
            report(CodeSmell(issue, Entity.from(importDirective),
                "Use repository interfaces from :core:domain instead"))
        }
    }
}
```

## Baseline File for Incremental Adoption

When adding detekt to an existing project, create a baseline to suppress existing
violations and only enforce rules on new code:

```kotlin
detekt {
    baseline = file("detekt-baseline.xml")
}
```

Generate the baseline: `./gradlew detektBaseline`. This creates an XML file listing
all current violations. New violations will still be reported. Periodically shrink
the baseline by fixing existing issues.

## ktlint for Formatting

ktlint enforces the official Kotlin coding conventions. Use it via a Gradle plugin:

```kotlin
plugins {
    id("org.jlleitschuh.gradle.ktlint") version "12.1.0"
}

ktlint {
    version.set("1.2.1")
    android.set(false)
    outputToConsole.set(true)
    reporters {
        reporter(org.jlleitschuh.gradle.ktlint.reporter.ReporterType.SARIF)
    }
}
```

Run `./gradlew ktlintCheck` to verify formatting and `./gradlew ktlintFormat` to
auto-fix issues. Add a Git pre-commit hook to format on commit:

```kotlin
tasks.register("installGitHook", Copy::class) {
    from("${rootDir}/scripts/pre-commit")
    into("${rootDir}/.git/hooks")
    fileMode = 0b111101101  // 755
}
```

## Kotlin Compiler Options for Strictness

Configure the compiler to catch more issues at build time:

```kotlin
kotlin {
    compilerOptions {
        allWarningsAsErrors = true              // Treat all warnings as errors
        freeCompilerArgs.addAll(
            "-Xjsr305=strict",                  // Strict Java null interop
            "-opt-in=kotlin.RequiresOptIn"       // Enable opt-in annotations
        )
    }
}
```

- **`allWarningsAsErrors`** — Prevents warnings from accumulating. Developers must
  fix or explicitly suppress each warning.
- **`-Xjsr305=strict`** — Makes `@Nonnull`/`@Nullable` Java annotations binding in
  Kotlin. Without this, annotated Java types remain platform types.
- **`-opt-in=kotlin.RequiresOptIn`** — Allows using `@OptIn` and `@RequiresOptIn`
  to mark unstable APIs.

## Progressive Mode and K2 Compiler

Enable progressive mode to opt into stricter checks from newer Kotlin versions
while keeping backward compatibility:

```kotlin
kotlin {
    compilerOptions {
        progressiveMode = true
    }
}
```

The K2 compiler (default since Kotlin 2.0) provides faster compilation and
improved type inference. It also enables new language features and performs
stricter analysis than the old frontend.

## CI Integration

Run all analysis tools in the CI pipeline:

```yaml
# Example GitHub Actions step
- name: Static analysis
  run: |
    ./gradlew detekt
    ./gradlew ktlintCheck
    ./gradlew compileKotlin  # With allWarningsAsErrors = true
```

Configure detekt to produce SARIF output for GitHub code scanning integration:

```kotlin
detekt {
    reports {
        sarif.required.set(true)
    }
}
```

## Suppress Annotations

When a rule must be bypassed, use `@Suppress` with a comment explaining why:

```kotlin
@Suppress("MagicNumber") // HTTP status codes are well-known constants
fun isSuccess(statusCode: Int): Boolean = statusCode in 200..299

@Suppress("TooManyFunctions") // Repository interface naturally has many operations
interface UserRepository {
    // ...
}
```

Use suppression sparingly. If a rule is frequently suppressed, reconfigure the
rule threshold instead of adding suppressions throughout the codebase.

## Common Pitfalls

- Do not disable detekt rules globally to make the build pass. Use a baseline file
  for existing code and fix violations incrementally.
- Do not skip static analysis in local builds — developers should see the same
  errors CI will catch. Use `./gradlew check` which includes detekt and ktlint.
- Do not use `@Suppress` without a comment explaining the reason. Uncommented
  suppressions become technical debt that nobody understands later.
- Do not rely solely on formatting tools. ktlint catches style issues but detekt
  catches logic and complexity problems that formatting cannot address.
