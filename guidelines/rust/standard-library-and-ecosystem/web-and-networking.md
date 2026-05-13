# Web and Networking

## Axum — Web Framework

Axum builds on Tokio and Tower for composable, type-safe web services:

```rust
use axum::{Router, routing::get, extract::{State, Path, Json}, response::IntoResponse};

#[tokio::main]
async fn main() {
    let app = Router::new()
        .route("/health", get(health))
        .route("/users/{id}", get(get_user))
        .route("/users", post(create_user))
        .with_state(AppState::new());

    let listener = tokio::net::TcpListener::bind("0.0.0.0:3000").await.unwrap();
    axum::serve(listener, app).await.unwrap();
}

async fn health() -> &'static str {
    "ok"
}

async fn get_user(
    State(state): State<AppState>,
    Path(id): Path<u64>,
) -> Result<Json<User>, AppError> {
    let user = state.db.find_user(id).await?;
    Ok(Json(user))
}
```

### Extractors

Axum uses extractors to parse request data:

```rust
use axum::extract::{Query, State, Path, Json};

// Path parameters
async fn get_item(Path(id): Path<u64>) -> impl IntoResponse { ... }

// Query parameters: /search?q=rust&page=1
#[derive(Deserialize)]
struct SearchParams { q: String, page: Option<u32> }
async fn search(Query(params): Query<SearchParams>) -> impl IntoResponse { ... }

// JSON body
async fn create(Json(body): Json<CreateRequest>) -> impl IntoResponse { ... }

// Shared state
async fn handler(State(db): State<DbPool>) -> impl IntoResponse { ... }
```

### Error Handling in Axum

Create a custom error type that implements `IntoResponse`:

```rust
use axum::response::{IntoResponse, Response};
use axum::http::StatusCode;

enum AppError {
    NotFound(String),
    Validation(String),
    Internal(anyhow::Error),
}

impl IntoResponse for AppError {
    fn into_response(self) -> Response {
        let (status, message) = match self {
            AppError::NotFound(msg) => (StatusCode::NOT_FOUND, msg),
            AppError::Validation(msg) => (StatusCode::BAD_REQUEST, msg),
            AppError::Internal(err) => {
                tracing::error!("internal error: {err:#}");
                (StatusCode::INTERNAL_SERVER_ERROR, "internal error".into())
            }
        };
        (status, message).into_response()
    }
}

// Now handlers return Result<impl IntoResponse, AppError>
impl From<anyhow::Error> for AppError {
    fn from(err: anyhow::Error) -> Self {
        AppError::Internal(err)
    }
}
```

### Middleware with Tower

```rust
use tower_http::trace::TraceLayer;
use tower_http::cors::CorsLayer;
use tower_http::compression::CompressionLayer;
use tower_http::timeout::TimeoutLayer;

let app = Router::new()
    .route("/api/data", get(handler))
    .layer(TraceLayer::new_for_http())
    .layer(CompressionLayer::new())
    .layer(TimeoutLayer::new(Duration::from_secs(30)))
    .layer(CorsLayer::permissive());
```

## reqwest — HTTP Client

```rust
use reqwest::Client;

// Reuse the client (connection pooling)
let client = Client::builder()
    .timeout(Duration::from_secs(10))
    .build()?;

// GET
let response = client.get("https://api.example.com/data")
    .header("Authorization", format!("Bearer {token}"))
    .send()
    .await?;

let data: ApiResponse = response.json().await?;

// POST with JSON
let response = client.post("https://api.example.com/users")
    .json(&new_user)
    .send()
    .await?;
```

**Always reuse `Client`** — it maintains a connection pool internally.
Creating a new client per request defeats connection reuse.

## clap — CLI Arguments

```rust
use clap::Parser;

#[derive(Parser)]
#[command(name = "myapp", about = "My application")]
struct Cli {
    /// Input file path
    #[arg(short, long)]
    input: PathBuf,

    /// Output format
    #[arg(short, long, default_value = "json")]
    format: OutputFormat,

    /// Verbose output
    #[arg(short, long, action = clap::ArgAction::Count)]
    verbose: u8,
}

#[derive(Clone, clap::ValueEnum)]
enum OutputFormat {
    Json,
    Yaml,
    Toml,
}

fn main() {
    let cli = Cli::parse();
    // use cli.input, cli.format, cli.verbose
}
```

## Database Access with sqlx

Compile-time checked SQL queries:

```rust
use sqlx::PgPool;

async fn get_user(pool: &PgPool, id: i64) -> Result<User> {
    // Query is checked against the database at compile time
    let user = sqlx::query_as!(
        User,
        "SELECT id, name, email FROM users WHERE id = $1",
        id
    )
    .fetch_one(pool)
    .await?;

    Ok(user)
}

// Connection pool setup
let pool = PgPool::connect(&database_url).await?;
```

## Time Handling

```rust
use std::time::{Duration, Instant, SystemTime};

// Measuring elapsed time
let start = Instant::now();
do_work();
let elapsed = start.elapsed();
println!("took {elapsed:?}");

// Timestamps (for chrono crate)
use chrono::{Utc, DateTime};
let now: DateTime<Utc> = Utc::now();
let formatted = now.to_rfc3339();
```

Prefer `std::time::Instant` for measuring durations and
`chrono` or the `time` crate for calendar dates.
