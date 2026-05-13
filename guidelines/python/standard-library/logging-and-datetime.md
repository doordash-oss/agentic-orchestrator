# Logging and Datetime

## Logging

### Basic Setup

```python
import logging

# Always use __name__ as the logger name
logger = logging.getLogger(__name__)

def process(data):
    logger.info("processing %d items", len(data))
    try:
        result = transform(data)
    except ValueError:
        logger.exception("transform failed")   # includes traceback
        raise
    logger.debug("transform complete: %d results", len(result))
    return result
```

### Configuration

Configure logging once at application startup, never in libraries:

```python
# Application entry point
import logging

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(name)s %(levelname)s %(message)s",
    datefmt="%Y-%m-%d %H:%M:%S",
)

# Or with dictConfig for more control
import logging.config

logging.config.dictConfig({
    "version": 1,
    "disable_existing_loggers": False,
    "formatters": {
        "standard": {
            "format": "%(asctime)s [%(levelname)s] %(name)s: %(message)s"
        },
    },
    "handlers": {
        "console": {
            "class": "logging.StreamHandler",
            "formatter": "standard",
            "level": "INFO",
        },
    },
    "root": {
        "handlers": ["console"],
        "level": "INFO",
    },
})
```

### Libraries Should NOT Configure Logging

```python
# In a library — just use getLogger, never configure
logger = logging.getLogger(__name__)

# Add NullHandler to prevent "No handler found" warnings
logger.addHandler(logging.NullHandler())
```

### Use Lazy Formatting

```python
# Correct — formatting only happens if the level is enabled
logger.info("user %s logged in from %s", user_id, ip_addr)

# Anti-pattern — f-string is always evaluated
logger.info(f"user {user_id} logged in from {ip_addr}")
```

### Structured Logging

For production systems, use structured logging (JSON):

```python
import json
import logging

class JSONFormatter(logging.Formatter):
    def format(self, record: logging.LogRecord) -> str:
        return json.dumps({
            "time": self.formatTime(record),
            "level": record.levelname,
            "logger": record.name,
            "message": record.getMessage(),
            "module": record.module,
            "line": record.lineno,
        })
```

Or use a library like `structlog` or `python-json-logger`.

## Datetime

### Always Use Timezone-Aware Datetimes

```python
from datetime import datetime, timezone
from zoneinfo import ZoneInfo       # Python 3.9+

# UTC
now_utc = datetime.now(timezone.utc)

# Specific timezone
now_eastern = datetime.now(ZoneInfo("America/New_York"))

# Anti-pattern — naive datetime (no timezone info)
now_naive = datetime.now()           # ambiguous! which timezone?
```

### Conversion

```python
# UTC to local
utc_time = datetime.now(timezone.utc)
local_time = utc_time.astimezone(ZoneInfo("Europe/London"))

# Parse ISO format
dt = datetime.fromisoformat("2024-01-15T10:30:00+00:00")

# Format
dt.isoformat()                       # "2024-01-15T10:30:00+00:00"
dt.strftime("%Y-%m-%d %H:%M")       # "2024-01-15 10:30"
```

### `timedelta`

```python
from datetime import timedelta

one_day = timedelta(days=1)
one_hour = timedelta(hours=1)

tomorrow = datetime.now(timezone.utc) + one_day
two_hours_ago = datetime.now(timezone.utc) - timedelta(hours=2)

# Total seconds
duration = timedelta(hours=2, minutes=30)
duration.total_seconds()             # 9000.0
```

### Common Pitfalls

```python
# Don't compare naive and aware datetimes
naive = datetime(2024, 1, 15)
aware = datetime(2024, 1, 15, tzinfo=timezone.utc)
naive == aware                       # TypeError!

# Don't use utcnow() — deprecated in 3.12
datetime.utcnow()                   # returns naive datetime!
datetime.now(timezone.utc)           # correct — returns aware

# Don't hardcode UTC offset
datetime(2024, 7, 1, tzinfo=timezone(timedelta(hours=-4)))  # DST changes!
datetime(2024, 7, 1, tzinfo=ZoneInfo("America/New_York"))   # handles DST
```

## Secrets and Random

```python
import secrets
import random

# For security (tokens, passwords, keys) — use secrets
token = secrets.token_urlsafe(32)
api_key = secrets.token_hex(16)

# For non-security (shuffling, sampling) — use random
random.choice(items)
random.shuffle(deck)

# Anti-pattern: using random for security
token = ''.join(random.choices(string.ascii_letters, k=32))  # predictable!
```
