# Multi-Module Structure

Multi-module Gradle projects improve build times through incremental compilation,
enforce architectural boundaries at the build system level, and make dependency
relationships explicit. Module boundaries should reflect domain concepts, not
technical layers.

## Standard Source Set Layout

Every module follows the same directory structure:

```
module/
├── src/
│   ├── main/
│   │   ├── kotlin/         # Production Kotlin sources
│   │   └── resources/      # Runtime resources (config files, templates)
│   └── test/
│       ├── kotlin/         # Test sources
│       └── resources/      # Test-only resources (fixtures, test configs)
└── build.gradle.kts
```

Additional source sets (e.g., `integrationTest`) can be added for specialized
testing. Keep test utilities in a separate `testFixtures` source set when they
need to be shared across modules.

```kotlin
// build.gradle.kts
java {
    registerFeature("testFixtures") {
        usingSourceSet(sourceSets.create("testFixtures"))
    }
}
```

## Module Organization Strategies

### By Feature/Domain (Recommended)

Organize modules around business capabilities:

```
settings.gradle.kts
app/                        # Application entry point, wiring
core/
  domain/                   # Shared domain types, interfaces
  data/                     # Data access abstractions
  network/                  # HTTP client, serialization
feature/
  user/                     # User registration, profile, auth
  order/                    # Order creation, tracking
  payment/                  # Payment processing
shared/
  model/                    # DTOs shared across features
  test-utils/               # Common test helpers
```

Each feature module is self-contained and owns its domain logic, data access,
and API surface.

### By Layer (Less Recommended)

```
domain/                     # All business logic
data/                       # All data access
presentation/               # All UI / API controllers
```

Layer-based organization leads to large, unfocused modules. Changes to a single
feature touch multiple modules, and dependency graphs become tangled.

### Hybrid Approach

For larger projects, combine both strategies:

```
:app
:core:domain
:core:network
:core:database
:feature:user:domain
:feature:user:data
:feature:user:api
:feature:order:domain
:feature:order:data
:feature:order:api
```

This provides fine-grained control but adds complexity. Use it only when the
simpler feature-based approach becomes insufficient.

## Dependency Direction Rules

Strict dependency direction prevents architectural decay:

```
feature:user ──> core:domain    (features depend on core)
feature:user ──> core:network   (features depend on core)
feature:user ──╳ feature:order  (features never depend on other features)
core:domain  ──╳ core:network   (core modules are independent of each other)
core:domain  ──╳ feature:user   (core never depends on features)
shared:model ──╳ business logic (shared modules carry no business logic)
```

Rules:
- **Features depend on core**, never on other features.
- **Core modules are independent** of each other and of features.
- **Shared modules contain no business logic** — only data structures and utilities.
- **The app module** wires everything together and depends on all features.

When two features need to communicate, introduce a shared interface in a core
module and use dependency injection to bind the implementation.

### Anti-Pattern: Circular Dependencies

```kotlin
// BAD — circular dependency between modules
// :feature:user depends on :feature:order
// :feature:order depends on :feature:user
```

Gradle will reject circular module dependencies at configuration time. If you
encounter this, extract the shared contract into a core module.

## settings.gradle.kts for Multi-Module Projects

```kotlin
rootProject.name = "my-app"

include(
    ":app",
    ":core:domain",
    ":core:data",
    ":core:network",
    ":feature:user",
    ":feature:order",
    ":feature:payment",
    ":shared:model",
    ":shared:test-utils",
)
```

For projects with many modules, use `enableFeaturePreview("TYPESAFE_PROJECT_ACCESSORS")`
in settings to get type-safe project dependency references:

```kotlin
dependencies {
    implementation(projects.core.domain)    // instead of project(":core:domain")
    implementation(projects.core.network)
}
```

## api vs implementation Dependencies

Choose the correct dependency configuration:

```kotlin
dependencies {
    // implementation — hidden from consumers (default choice)
    implementation(libs.kotlinx.coroutines.core)

    // api — exposed to consumers (use sparingly)
    api(libs.kotlinx.serialization.core)
}
```

- **`implementation`** — The dependency is an internal detail. Consumers of this
  module cannot access it. This is the default and correct choice for most cases.
- **`api`** — The dependency is part of this module's public API. Consumers can
  see and use it. Use only when types from the dependency appear in your public
  function signatures or class hierarchies.

Overusing `api` creates unnecessary recompilation cascades. When a `api` dependency
changes, all consumers must recompile. Prefer `implementation` and expose only
what is necessary.

## Module-Specific Build Logic with Convention Plugins

Create convention plugins for different module types:

```kotlin
// buildSrc/src/main/kotlin/feature-conventions.gradle.kts
plugins {
    id("kotlin-conventions")  // base Kotlin setup
}

dependencies {
    "implementation"(project(":core:domain"))
}
```

```kotlin
// buildSrc/src/main/kotlin/library-conventions.gradle.kts
plugins {
    id("kotlin-conventions")
    `java-library`
}
```

Apply the appropriate convention in each module:

```kotlin
// feature/user/build.gradle.kts
plugins {
    id("feature-conventions")
}

dependencies {
    implementation(project(":core:network"))
    testImplementation(project(":shared:test-utils"))
}
```

## Package Naming

Package names should mirror the module structure:

```
:core:domain      -> org.example.core.domain
:feature:user     -> org.example.feature.user
:shared:model     -> org.example.shared.model
```

Consistent naming makes it easy to identify which module a class belongs to
and prevents package name collisions across modules.

## Common Pitfalls

- Do not put all code in a single module and plan to split later. Start with
  at least `app`, `core`, and one feature module.
- Do not expose internal types through `api` dependencies just to avoid creating
  proper public interfaces.
- Do not create modules that are too granular — each module adds build overhead.
  A module should represent a meaningful boundary, not a single class.
- Avoid `project(":some:module")` string references when typesafe project accessors
  are available.
