# Naming Conventions

## The Universal Rules

| Element | Convention | Example |
|---------|-----------|---------|
| Packages | lowercase, dot-separated, reverse domain | `com.example.orderservice` |
| Classes/Interfaces | UpperCamelCase, noun or noun phrase | `OrderProcessor`, `Readable` |
| Methods | lowerCamelCase, verb or verb phrase | `sendMessage()`, `isValid()` |
| Fields/local variables | lowerCamelCase, noun | `orderCount`, `currentUser` |
| Constants (`static final`) | UPPER_SNAKE_CASE | `MAX_RETRY_COUNT`, `DEFAULT_TIMEOUT` |
| Type parameters | Single uppercase letter or short name | `T`, `E`, `K`, `V`, `<ID>` |
| Enum constants | UPPER_SNAKE_CASE | `ORDER_PENDING`, `ACTIVE` |

## Package Names

- All lowercase, no underscores
- Reverse domain prefix: `com.company.project.module`
- Describe what the package **contains**, not what it does
- Never use `utils`, `helpers`, `common`, `misc`, or `shared`

```java
// Good — describes contents
com.example.billing.invoice
com.example.auth.token

// Bad — vague grab-bags
com.example.utils
com.example.common
```

## Class Names

- **UpperCamelCase**, always a noun or noun phrase
- Describe what the class **is**, not what it does
- Suffix with the pattern name when implementing a well-known pattern

```java
// Good
OrderProcessor          // what it is
InvoiceRepository       // well-known pattern suffix
HttpConnectionFactory   // pattern suffix
PaymentValidationResult // what it represents

// Bad
ProcessOrder       // verb — sounds like a method
OrderHelper        // vague
OrderMgr           // abbreviation
AbstractOrder      // prefix — use the abstract keyword instead
```

## Method Names

- **lowerCamelCase**, always a verb or verb phrase
- Boolean-returning methods: `is`, `has`, `can`, `should` prefix
- Factory methods: `of`, `from`, `valueOf`, `create`, `newInstance`
- Converters: `toXxx`, `asXxx`
- Accessors: plain name for records/modern style, `getXxx`/`setXxx` for JavaBeans

```java
// Good
sendNotification()
isEligible()
hasPermission()
toDto()
fromJson(String json)
Order.of(item, quantity)

// Bad
notification()    // ambiguous — get or send?
check()          // check what?
doProcess()      // redundant "do" prefix
```

## Field and Variable Names

- **lowerCamelCase**
- Single-character names only in lambda parameters and short loops
- Boolean fields: `isActive`, `hasErrors`, `enabled` (not `flag` or `status`)

```java
// Good
private final int maxRetries;
private boolean enabled;
var orderTotal = calculateTotal(items);

// Bad
private int n;           // meaningless
private boolean flag;    // flag for what?
private Object data;     // too vague
```

## Constants

Only truly **immutable values** get UPPER_SNAKE_CASE. A `static final` reference
to a mutable object is not a constant:

```java
// Constants — UPPER_SNAKE_CASE
static final int MAX_CONNECTIONS = 100;
static final Duration TIMEOUT = Duration.ofSeconds(30);
static final String API_VERSION = "v2";

// NOT constants — lowerCamelCase (mutable or not a fixed value)
static final List<String> defaultTags = new ArrayList<>();  // mutable!
static final Logger log = LoggerFactory.getLogger(MyClass.class);  // not a value
private static final AtomicInteger counter = new AtomicInteger();  // mutable
```

## Type Parameters

| Parameter | Convention |
|-----------|-----------|
| `T` | General type |
| `E` | Element type (collections) |
| `K`, `V` | Key, Value (maps) |
| `R` | Return type |
| `S`, `U` | Additional types |

For domain-specific parameters, descriptive names are acceptable:
`<ID>`, `<ENTITY>`, `<REQUEST>`.
