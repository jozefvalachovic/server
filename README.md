# server

Reusable building blocks for Go HTTP and TCP servers. One dependency (`logger/v4`), Go 1.26+.

```
go get github.com/jozefvalachovic/server@latest
```

## Quick start

```go
package main

import (
    "context"
    "net/http"
    "time"

    "github.com/jozefvalachovic/server/cache"
    "github.com/jozefvalachovic/server/response"
    "github.com/jozefvalachovic/server/routes"
    "github.com/jozefvalachovic/server/server"
    "github.com/jozefvalachovic/server/watch"
)

func main() {
    watch.Init() // hot-reload when DEV=1

    mux := http.NewServeMux()
    store, _ := routes.RegisterRoutes(mux, &cache.CacheConfig{
        MaxSize: 1000, DefaultTTL: 5 * time.Minute, CleanupInterval: time.Minute,
    }, func(mux *http.ServeMux) {
        mux.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) {
            response.APIResponseWriter(w, "pong", http.StatusOK)
        })
    })
    _ = store

    hc := server.NewHealthChecker("1.0.0", 5*time.Second)
    mux.HandleFunc("GET /healthz", hc.LivenessHandler())
    mux.HandleFunc("GET /readyz", hc.ReadinessHandler())

    srv, _ := server.NewHTTPServer(mux, "my-app", "1.0.0", server.HTTPServerConfig{})
    _ = srv.Start()
}
```

```bash
HTTP_HOST=127.0.0.1 HTTP_PORT=8080 go run .
```

Run the bundled example with hot-reload:

```bash
./example.sh
# Open http://127.0.0.1:8080/docs
```

## Packages

| Package                     | Import              | Purpose                                                       |
| --------------------------- | ------------------- | ------------------------------------------------------------- |
| [`server`](#server-1)       | `server/server`     | HTTP, TCP, and metrics servers with graceful shutdown         |
| [`middleware`](#middleware) | `server/middleware` | Auth, CORS, rate-limit, cache, compress, security, timeout, … |
| [`routes`](#routes)         | `server/routes`     | Route registration, method routing, swagger/MCP helpers       |
| [`response`](#response)     | `server/response`   | Typed JSON response writers and request body decoding         |
| [`request`](#request)       | `server/request`    | IP address extraction, email validation                       |
| [`cache`](#cache)           | `server/cache`      | In-memory key-value store with TTL, eviction, and stats       |
| [`client`](#client)         | `server/client`     | Resilient HTTP client with retry and circuit breaker          |
| [`mcp`](#mcp)               | `server/mcp`        | Model Context Protocol (MCP) tool server                      |
| [`swagger`](#swagger)       | `server/swagger`    | Auto-generated Swagger UI from Go types                       |
| [`admin`](#admin)           | `server/admin`      | Password-protected metrics and cache explorer UI              |
| [`watch`](#watch)           | `server/watch`      | Hot-reload file watcher for development                       |
| [`config`](#config)         | `server/config`     | Default timeouts, limits, and shared constants                |

The `server` package re-exports client and middleware config types so that most applications only need a single import:

```go
server.NewClient(server.ClientConfig{...})    // wraps client.New
server.HTTPRateLimitConfig{...}               // re-exported from middleware
```

---

## server

HTTP and TCP servers with TLS, graceful shutdown, and optional metrics sidecar.

### HTTP server

```go
srv, err := server.NewHTTPServer(mux, "app", "1.0.0", server.HTTPServerConfig{
    ReadTimeout:  30 * time.Second,
    WriteTimeout: 60 * time.Second,
    MaxConns:     10_000,
    TLSConfig:    tlsCfg, // optional — also reads HTTP_TLS_CERT_PATH / HTTP_TLS_KEY_PATH
    CORS:         &server.CORSConfig{AllowedOrigins: []string{"https://example.com"}},
    RateLimitConfig: &server.HTTPRateLimitConfig{RequestsPerSecond: 100, Burst: 200},
    Compress:     &server.CompressConfig{Enabled: true},
    Admin:        &server.AdminConfig{AppName: "app", AppVersion: "1.0.0", Store: store},
    Middlewares:  []server.HTTPMiddleware{customLogger},
})
srv.Start()                          // blocks in background goroutine
srv.GracefulShutdown(ctx)            // drains in-flight requests
```

| Environment variable | Required | Description                               |
| -------------------- | -------- | ----------------------------------------- |
| `HTTP_HOST`          | yes      | Bind address (IP literal, e.g. `0.0.0.0`) |
| `HTTP_PORT`          | yes      | Listen port (1–65535)                     |
| `HTTP_TLS_CERT_PATH` | no       | TLS certificate file path                 |
| `HTTP_TLS_KEY_PATH`  | no       | TLS private key file path                 |

### TCP server

```go
srv, err := server.NewTCPServer(handler, "tcp-app", "1.0.0", server.TCPServerConfig{
    ReadTimeout:  15 * time.Second,
    WriteTimeout: 15 * time.Second,
    TLSConfig:    tlsCfg,
    RateLimitConfig: &server.TCPRateLimitConfig{ConnectionsPerSecond: 50},
})
```

| Environment variable | Required | Description                                  |
| -------------------- | -------- | -------------------------------------------- |
| `TCP_HOST`           | yes      | Bind address                                 |
| `TCP_PORT`           | yes      | Listen port                                  |
| `TCP_MAX_CONNS`      | no       | Max concurrent connections (default: 10 000) |

### Health checks

```go
hc := server.NewHealthChecker("1.0.0", 5*time.Second)
hc.Register("postgres", func(ctx context.Context) error { return db.PingContext(ctx) })
hc.Register("redis",    func(ctx context.Context) error { return rdb.Ping(ctx).Err() })
hc.SetRedactCheckNames(true) // hide dependency names in external responses

mux.HandleFunc("GET /healthz", hc.LivenessHandler())  // always 200 OK
mux.HandleFunc("GET /readyz",  hc.ReadinessHandler())  // 200 / 503 based on checks
```

Health status is `ok`, `degraded` (some checks failing), or `down` (all failing).

### Metrics server

A separate HTTP server for `/debug/pprof` or Prometheus handlers:

```go
ms, _ := server.StartMetricsServer(&server.MetricsServerConfig{Handler: promHandler})
defer ms.Shutdown(ctx)
```

| Environment variable | Required | Description  |
| -------------------- | -------- | ------------ |
| `METRICS_HOST`       | yes      | Bind address |
| `METRICS_PORT`       | yes      | Listen port  |

---

## middleware

All middleware follow the `func(http.Handler) http.Handler` pattern and can be applied per-route or server-wide via `HTTPServerConfig.Middlewares`.

### Auth

```go
authMw := middleware.Auth(middleware.AuthConfig{
    Scheme: middleware.AuthSchemeBearer,
    Verify: func(ctx context.Context, token string) (string, error) {
        claims, err := validateJWT(token)
        return claims.Subject, err
    },
    SkipPaths: []string{"/healthz", "/readyz"},
})

// Retrieve identity downstream:
identity := middleware.AuthIdentityFromContext(r)
```

Supported schemes: `AuthSchemeBearer` (Authorization header) and `AuthSchemeAPIKey` (custom header, default `X-API-Key`).

### HTTP cache

```go
cached := middleware.HTTPCache(middleware.HTTPCacheConfig{
    Store:     store,
    TTL:       5 * time.Minute,
    KeyPrefix: func(r *http.Request) string { return "user:" + getUserID(r) },
})
mux.Handle("GET /products", cached(productsHandler))
```

Automatically invalidates on POST/PUT/PATCH/DELETE to the same prefix. The `CacheBackend` interface can be satisfied by `*cache.CacheStore` or a custom implementation.

### CORS

```go
middleware.CORS(middleware.CORSConfig{
    AllowedOrigins:   []string{"https://app.example.com"},
    AllowCredentials: true,
    MaxAge:           24 * time.Hour,
})
```

### Rate limiting

```go
// HTTP
middleware.HTTPRateLimit(middleware.HTTPRateLimitConfig{
    RequestsPerSecond: 100,
    Burst:             200,
    KeyFunc:           func(r *http.Request) string { return r.Header.Get("X-API-Key") },
})

// TCP
middleware.TCPRateLimit(middleware.TCPRateLimitConfig{
    ConnectionsPerSecond: 50,
    Burst:                100,
})
```

Per-key token bucket with automatic cleanup of idle entries.

### Other middleware

| Middleware    | Usage                                                        | Notes                                                                                  |
| ------------- | ------------------------------------------------------------ | -------------------------------------------------------------------------------------- |
| `Recovery`    | `middleware.Recovery(handler)`                               | Catches panics, logs stack traces, returns 500                                         |
| `Security`    | `middleware.Security(handler)`                               | Sets X-Content-Type-Options, X-Frame-Options, CSP, HSTS (production)                   |
| `Compress`    | `middleware.Compress(CompressConfig{Enabled: true})`         | gzip for JSON, XML, text, JS                                                           |
| `Timeout`     | `middleware.Timeout(TimeoutConfig{Timeout: 10*time.Second})` | Per-request context deadline                                                           |
| `RequestID`   | `middleware.RequestID()`                                     | Adds `X-Request-ID` header (crypto random)                                             |
| `RequestSize` | `middleware.RequestSize(handler)`                            | Limits body to 10 MiB (override with `MAX_REQUEST_SIZE_MB` env or `RequestSizeConfig`) |
| `IPFilter`    | `middleware.IPFilter(IPFilterConfig{Allowlist: [...]})`      | CIDR-based allow/blocklist with proxy trust                                            |

---

## routes

Helper functions for registering routes on `http.ServeMux`.

```go
// Register routes with automatic cache store creation
store, err := routes.RegisterRoutes(mux, &cache.CacheConfig{...}, registrarA, registrarB)

// Method routing with 405 Method Not Allowed
mux.HandleFunc("/products", routes.RouteHandler(routes.Routes{
    "GET":  listProducts,
    "POST": createProduct,
}))

// Cached route handler
mux.HandleFunc("/products", routes.CachedRouteHandler(routes.Routes{
    "GET":  listProducts,
    "POST": createProduct,
}, middleware.HTTPCacheConfig{Store: store, TTL: 5 * time.Minute, ...}))

// Per-route middleware
routes.RegisterRoute(mux, routes.Route{
    Method: "GET", Path: "/admin/users",
    Handler: listUsers,
    Middlewares: []func(http.Handler) http.Handler{adminAuth},
})

// Mount sub-systems
routes.RegisterSwagger(mux, "/docs", swagger.Config{...})
routes.RegisterMCP(mux, "/mcp", mcp.Config{...})
```

---

## response

Typed JSON response writers and request body decoding.

```go
// Success
response.APIResponseWriter(w, product, http.StatusOK)

// Paginated
response.APIResponseWriterWithPagination(w, products, http.StatusOK, limit, offset, totalCount)

// Error
response.APIErrorWriter(w, response.APIError[any]{
    Code: http.StatusBadRequest, Message: "validation failed",
})

// Convenience
response.APIUnauthorized(w, "invalid token")
response.APIForbidden(w, "insufficient permissions")

// Decode + validate request body (rejects unknown fields)
product, apiErr := response.ValidateAndDecode[Product](r)
if apiErr != nil {
    response.APIErrorWriter(w, *apiErr)
    return
}
```

All responses follow the envelope:

```json
{
  "code": 200,
  "data": { ... },
  "message": "optional",
  "pagination": { "limit": 20, "offset": 0, "totalCount": 142, ... }
}
```

---

## request

```go
ip := request.GetIPAddress(r)          // checks X-Forwarded-For, X-Real-IP, RemoteAddr
err := request.ValidateEmail(email)     // RFC 5322 validation
clean := request.SanitizeEmail(email)   // lowercase + trim
```

---

## cache

In-memory key-value store with TTL, LRU eviction, memory limits, and lazy expiry on read.

```go
store, err := cache.NewCacheStore(cache.CacheConfig{
    MaxSize:         10_000,
    DefaultTTL:      5 * time.Minute,
    CleanupInterval: time.Minute,
    MaxMemoryMB:     256,           // optional memory cap
})
defer store.Stop()

store.Set("user:42", userData, nil)                            // default TTL
store.Set("session:abc", session, new(30 * time.Minute))       // custom TTL
val, err := store.Get("user:42")                               // returns ErrNotFound if missing/expired
store.Delete("user:42")
store.DeleteByPrefix("session:")
store.Flush()

stats := store.GetStats()       // hits, misses, evictions, size, bytes
exported := store.Export()      // snapshot of all entries
```

Cache keys for HTTP response caching:

```go
key := cache.BuildResponseKey("u42_products", "/api/products", "page=2&category=shoes")
// → "u42_products:GET:/api/products:category=shoes&page=2"  (query params sorted)
```

---

## client

Resilient HTTP client with exponential backoff retry, circuit breaker, and base URL support.

```go
c := server.NewClient(server.ClientConfig{
    Timeout: 10 * time.Second,
    BaseURL: "https://api.example.com/v1",
    Retry: &server.ClientRetryConfig{
        MaxRetries:     3,
        InitialBackoff: 100 * time.Millisecond,
    },
    CircuitBreaker: &server.ClientCircuitBreakerConfig{
        Threshold:    5,              // open after 5 consecutive failures
        OpenDuration: 30 * time.Second,
    },
})

resp, err := c.Get(ctx, "/users")    // resolved to https://api.example.com/v1/users
resp, err := c.Post(ctx, "/users", "application/json", body)
resp, err := c.Do(customReq)
```

Default retry targets: `429`, `502`, `503`, `504`, and all network errors. Customize with `RetryConfig.ShouldRetry`.

---

## mcp

[Model Context Protocol](https://modelcontextprotocol.io/) server implementing the MCP 2024-11-05 spec over Streamable HTTP (JSON-RPC 2.0).

```go
routes.RegisterMCP(mux, "/mcp", mcp.Config{
    Name:    "my-service",
    Version: "1.0.0",
    AllowedOrigins: []string{"https://app.example.com"},
    Tools: []mcp.Tool{
        {
            Name:        "get_product",
            Description: "Fetch a product by ID.",
            Input:       (*GetProductInput)(nil),
            Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
                var in GetProductInput
                if err := json.Unmarshal(raw, &in); err != nil {
                    return nil, err
                }
                return findProduct(in.ID)
            },
        },
    },
})
```

Input schemas are reflected automatically from the `Input` struct's fields and JSON tags.

---

## swagger

Embedded Swagger-like API documentation UI generated from Go types.

```go
routes.RegisterSwagger(mux, "/docs", swagger.Config{
    Title:       "Product API",
    Description: "CRUD operations for products",
    Version:     "1.0.0",
    Endpoints: []swagger.Endpoint{
        {
            Method:   swagger.POST,
            Path:     "/products",
            Summary:  "Create a product",
            Tags:     []string{"products"},
            Request:  (*CreateProductRequest)(nil),
            Response: (*Product)(nil),
        },
    },
})
```

---

## admin

Password-protected admin UI with per-route metrics and cache explorer. Requires `ADMIN_NAME` and `ADMIN_SECRET` environment variables.

```go
collector := admin.NewCollector()

srv, _ := server.NewHTTPServer(mux, "app", "1.0.0", server.HTTPServerConfig{
    Admin: &server.AdminConfig{
        AppName:    "app",
        AppVersion: "1.0.0",
        Store:      store,
    },
    Middlewares: []server.HTTPMiddleware{collector.Middleware},
})
```

Registers at `/metrics/` (request count, latency, error rate per route) and `/cache/` (browse, delete, flush entries). Session-based HMAC-SHA256 auth with 8-hour TTL.

---

## watch

Development-only hot-reload watcher. Call as the first statement in `main()`:

```go
func main() {
    watch.Init(watch.Config{
        ExtraDirs: []string{"templates"},
        Interval:  500 * time.Millisecond,
    })
    // ... rest of application
}
```

Activated by `DEV=1`. Monitors `.go` files (configurable), rebuilds, and restarts on changes. No-op in production.

---

## config

Shared default constants used by the server and middleware packages:

| Constant                | Value  | Used by                         |
| ----------------------- | ------ | ------------------------------- |
| `ShutdownTimeout`       | 15 s   | Graceful shutdown deadline      |
| `HTTPIdleTimeout`       | 30 s   | HTTP keep-alive idle            |
| `HTTPReadHeaderTimeout` | 5 s    | Header read deadline            |
| `TCPReadTimeout`        | 15 s   | TCP per-op read deadline        |
| `TCPWriteTimeout`       | 15 s   | TCP per-op write deadline       |
| `DefaultTCPMaxConns`    | 10 000 | Max concurrent TCP connections  |
| `DefaultReadTimeout`    | 30 s   | Controller/handler read budget  |
| `DefaultWriteTimeout`   | 60 s   | Controller/handler write budget |
| `DefaultStreamTimeout`  | 290 s  | SSE / long-poll budget          |
| `MaxPageLimit`          | 1 000  | Max items per page              |
| `MaxOffset`             | 10 000 | Max pagination offset           |

---

## Environment variables

| Variable              | Package    | Required    | Description                            |
| --------------------- | ---------- | ----------- | -------------------------------------- |
| `HTTP_HOST`           | server     | yes         | HTTP bind address (IP literal)         |
| `HTTP_PORT`           | server     | yes         | HTTP listen port                       |
| `HTTP_TLS_CERT_PATH`  | server     | no          | TLS certificate path                   |
| `HTTP_TLS_KEY_PATH`   | server     | no          | TLS key path                           |
| `TCP_HOST`            | server     | for TCP     | TCP bind address                       |
| `TCP_PORT`            | server     | for TCP     | TCP listen port                        |
| `TCP_MAX_CONNS`       | config     | no          | Max TCP connections (default: 10 000)  |
| `METRICS_HOST`        | server     | for metrics | Metrics server bind address            |
| `METRICS_PORT`        | server     | for metrics | Metrics server port                    |
| `ADMIN_NAME`          | admin      | for admin   | Admin UI username                      |
| `ADMIN_SECRET`        | admin      | for admin   | Admin UI password / HMAC key           |
| `MAX_REQUEST_SIZE_MB` | middleware | no          | Body size limit override (default: 10) |
| `ENV`                 | middleware | no          | Set to `production` for HSTS header    |
| `DEV`                 | watch      | no          | Set to `1` to enable hot-reload        |

## License

See [LICENSE](LICENSE) for details.
