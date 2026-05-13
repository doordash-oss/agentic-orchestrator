# Procedural Macros

## Overview

Proc macros are functions that transform `TokenStream` → `TokenStream`. They
must live in a dedicated crate with `proc-macro = true`:

```toml
# my-macros/Cargo.toml
[lib]
proc-macro = true

[dependencies]
syn = { version = "2", features = ["full"] }
quote = "1"
proc-macro2 = "1"
```

## Three Types

### Derive Macros

Add to types with `#[derive(MyMacro)]`:

```rust
use proc_macro::TokenStream;
use quote::quote;
use syn::{parse_macro_input, DeriveInput};

#[proc_macro_derive(Builder)]
pub fn derive_builder(input: TokenStream) -> TokenStream {
    let input = parse_macro_input!(input as DeriveInput);
    let name = &input.ident;
    let builder_name = syn::Ident::new(
        &format!("{name}Builder"),
        name.span(),
    );

    let expanded = quote! {
        pub struct #builder_name {
            // builder fields...
        }

        impl #name {
            pub fn builder() -> #builder_name {
                #builder_name { /* defaults */ }
            }
        }
    };

    TokenStream::from(expanded)
}
```

### Attribute Macros

Applied as `#[my_attr]` to items:

```rust
#[proc_macro_attribute]
pub fn route(attr: TokenStream, item: TokenStream) -> TokenStream {
    let args = parse_macro_input!(attr as syn::LitStr);
    let input = parse_macro_input!(item as syn::ItemFn);
    let fn_name = &input.sig.ident;
    let path = args.value();

    let expanded = quote! {
        #input

        inventory::submit! {
            Route {
                path: #path,
                handler: #fn_name,
            }
        }
    };

    TokenStream::from(expanded)
}

// Usage:
// #[route("/api/users")]
// async fn list_users() -> impl IntoResponse { ... }
```

### Function-Like Macros

Called like functions: `my_macro!(...)`:

```rust
#[proc_macro]
pub fn sql(input: TokenStream) -> TokenStream {
    let query = parse_macro_input!(input as syn::LitStr);
    let query_str = query.value();

    // Validate SQL at compile time
    if !is_valid_sql(&query_str) {
        return syn::Error::new(query.span(), "invalid SQL")
            .to_compile_error()
            .into();
    }

    let expanded = quote! {
        sqlx::query(#query)
    };

    TokenStream::from(expanded)
}

// Usage: sql!("SELECT * FROM users WHERE id = $1")
```

## syn — Parsing

`syn` parses Rust source code into a typed AST:

```rust
use syn::{parse_macro_input, DeriveInput, Data, Fields};

#[proc_macro_derive(Describe)]
pub fn derive_describe(input: TokenStream) -> TokenStream {
    let input = parse_macro_input!(input as DeriveInput);

    let name = &input.ident;
    let field_count = match &input.data {
        Data::Struct(data) => match &data.fields {
            Fields::Named(fields) => fields.named.len(),
            Fields::Unnamed(fields) => fields.unnamed.len(),
            Fields::Unit => 0,
        },
        _ => panic!("Describe only supports structs"),
    };

    let expanded = quote! {
        impl #name {
            pub fn describe() -> String {
                format!("{} has {} fields", stringify!(#name), #field_count)
            }
        }
    };

    TokenStream::from(expanded)
}
```

## quote — Code Generation

`quote!` produces `TokenStream` with interpolation:

```rust
use quote::{quote, format_ident};

let field_name = format_ident!("my_field");
let field_type = quote! { String };

let tokens = quote! {
    pub struct Generated {
        pub #field_name: #field_type,
    }
};

// Repetition
let fields = vec![("name", "String"), ("age", "u32")];
let field_defs = fields.iter().map(|(name, ty)| {
    let name = format_ident!("{}", name);
    let ty: proc_macro2::TokenStream = ty.parse().unwrap();
    quote! { pub #name: #ty }
});

let tokens = quote! {
    pub struct User {
        #(#field_defs,)*
    }
};
```

## Error Handling in Proc Macros

Use `compile_error!` through `syn::Error`, not `panic!`:

```rust
#[proc_macro_derive(MyDerive)]
pub fn my_derive(input: TokenStream) -> TokenStream {
    let input = parse_macro_input!(input as DeriveInput);

    match impl_my_derive(&input) {
        Ok(tokens) => tokens.into(),
        Err(err) => err.to_compile_error().into(),
    }
}

fn impl_my_derive(input: &DeriveInput) -> syn::Result<proc_macro2::TokenStream> {
    match &input.data {
        Data::Struct(_) => { /* generate code */ }
        _ => {
            return Err(syn::Error::new_spanned(
                input,
                "MyDerive can only be applied to structs",
            ));
        }
    }
    Ok(quote! { /* ... */ })
}
```

**Why not `panic!`**: `syn::Error` produces proper compiler diagnostics with
source location. `panic!` produces unhelpful "proc macro panicked" messages.

## Helper Attributes

Proc macros can define helper attributes for additional configuration:

```rust
#[proc_macro_derive(Builder, attributes(builder))]
pub fn derive_builder(input: TokenStream) -> TokenStream {
    // Can now parse #[builder(...)] on fields
}

// Usage:
#[derive(Builder)]
struct Config {
    #[builder(default = "8080")]
    port: u16,
    #[builder(required)]
    host: String,
}
```
