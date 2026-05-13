# Pydantic and Validation

## Pydantic v2 Basics

Use pydantic at system boundaries — API requests, config files, external data:

```python
from pydantic import BaseModel, Field

class User(BaseModel):
    name: str = Field(min_length=1, max_length=100)
    age: int = Field(ge=0, le=150)
    email: str

user = User(name="Alice", age=30, email="alice@example.com")
user.name   # "Alice"

# Validation error on invalid data
User(name="", age=-1, email="x")  # ValidationError with details
```

## Field Validators

```python
from pydantic import BaseModel, field_validator, model_validator

class DateRange(BaseModel):
    start: date
    end: date

    @field_validator("end")
    @classmethod
    def end_after_start(cls, v, info):
        if "start" in info.data and v < info.data["start"]:
            raise ValueError("end must be after start")
        return v

class Config(BaseModel):
    host: str
    port: int

    @field_validator("host")
    @classmethod
    def strip_host(cls, v: str) -> str:
        return v.strip()
```

## Model Validators

Validate across multiple fields:

```python
class Payment(BaseModel):
    amount: float
    currency: str
    tax: float

    @model_validator(mode="after")
    def check_tax(self) -> "Payment":
        if self.currency == "USD" and self.tax > self.amount * 0.15:
            raise ValueError("tax exceeds maximum rate")
        return self
```

## Serialization

```python
user = User(name="Alice", age=30, email="alice@example.com")

# To dict
user.model_dump()
# {"name": "Alice", "age": 30, "email": "alice@example.com"}

# To JSON string
user.model_dump_json()

# Exclude fields
user.model_dump(exclude={"email"})

# From dict/JSON
User.model_validate({"name": "Bob", "age": 25, "email": "bob@x.com"})
User.model_validate_json('{"name":"Bob","age":25,"email":"bob@x.com"}')
```

## Model Configuration

```python
from pydantic import BaseModel, ConfigDict

class StrictConfig(BaseModel):
    model_config = ConfigDict(
        strict=True,              # no type coercion
        frozen=True,              # immutable
        extra="forbid",           # reject unknown fields
        str_strip_whitespace=True,
    )

    name: str
    value: int
```

## Common Patterns

### Nested Models

```python
class Address(BaseModel):
    street: str
    city: str
    country: str = "US"

class User(BaseModel):
    name: str
    address: Address

# Nested dict is automatically parsed
User(name="Alice", address={"street": "123 Main", "city": "NYC"})
```

### Discriminated Unions

```python
from typing import Literal
from pydantic import BaseModel

class Cat(BaseModel):
    type: Literal["cat"]
    meow_volume: int

class Dog(BaseModel):
    type: Literal["dog"]
    bark_pitch: float

class Pet(BaseModel):
    animal: Cat | Dog = Field(discriminator="type")

Pet(animal={"type": "cat", "meow_volume": 5})   # creates Cat
Pet(animal={"type": "dog", "bark_pitch": 2.1})   # creates Dog
```

## When to Use Pydantic vs Dataclass

| Scenario | Use |
|----------|-----|
| Internal data structures | `dataclass` |
| API request/response models | pydantic |
| Configuration parsing | pydantic |
| Database ORM models | depends on ORM |
| Simple value objects | `dataclass(frozen=True)` |
| External data validation | pydantic |

Pydantic adds overhead — don't use it for internal data that doesn't need
validation.
