# API Design

Building HTTP APIs that are predictable, consistent, and hard to misuse.
A well-designed API makes the right thing easy and the wrong thing obvious.
Clients should be able to guess the endpoint for a new resource without
reading the docs.

## REST Conventions

REST is a set of constraints, not a spec. The useful parts: resources are
nouns, HTTP methods are verbs, status codes are return values, and URLs are
identifiers.

### Resources as Nouns

URLs identify resources. Verbs come from HTTP methods, not URL paths.

```text
GET    /identities          # list identities
POST   /identities          # create an identity
GET    /identities/mal      # get one identity
PUT    /identities/mal      # replace an identity
PATCH  /identities/mal      # update fields on an identity
DELETE /identities/mal      # delete an identity
```

Wrong:

```text
POST /createIdentity
GET  /getIdentity?name=mal
POST /deleteIdentity
```

The resource name is always plural. `/identity/mal` looks like a singleton;
`/identities/mal` communicates that `mal` is one of many.

### HTTP Methods

| Method | Semantics | Idempotent | Safe |
|--------|-----------|------------|------|
| GET | Read | Yes | Yes |
| POST | Create / trigger action | No | No |
| PUT | Replace entire resource | Yes | No |
| PATCH | Partial update | No* | No |
| DELETE | Remove | Yes | No |

*PATCH can be made idempotent with merge-patch semantics, but the spec does
not require it.

PUT replaces the entire resource. If the client omits a field, that field is
cleared. PATCH updates only the fields provided. Choose based on client
ergonomics -- most APIs benefit from PATCH for updates because clients
rarely have the complete resource in hand.

### Status Codes

Status codes are your function's return type. Use them precisely.

| Code | Meaning | When |
|------|---------|------|
| 200 | OK | GET, PATCH, DELETE that returns a body |
| 201 | Created | POST that creates a resource |
| 204 | No Content | DELETE or PUT with no response body |
| 400 | Bad Request | Malformed JSON, missing required field |
| 401 | Unauthorized | No credentials or expired credentials |
| 403 | Forbidden | Valid credentials, insufficient permissions |
| 404 | Not Found | Resource does not exist |
| 409 | Conflict | Duplicate key, version mismatch |
| 422 | Unprocessable Entity | Valid JSON but fails business rules |
| 429 | Too Many Requests | Rate limit exceeded |
| 500 | Internal Server Error | Bug in the server |
| 502 | Bad Gateway | Upstream service failure |
| 503 | Service Unavailable | Planned maintenance or overload |

Never return 200 with an error in the body. The status code is the first
thing clients check. An error wrapped in a 200 response bypasses every
retry, circuit breaker, and monitoring system that depends on HTTP
semantics.

## URL Design

### Hierarchical Resources

Nest URLs when there is a true parent-child ownership relationship:

```text
/teams/firefly/members/mal       # mal is a member of team firefly
/projects/ethos/issues/42        # issue 42 belongs to project ethos
```

Do not nest when the relationship is a reference, not ownership:

```text
# Wrong: an identity is not owned by a talent
/talents/engineering/identities/mal

# Right: query by talent using a filter
/identities?talent=engineering
```

Nesting deeper than two levels is a smell. If you reach
`/a/1/b/2/c/3`, the resource `c` should probably be top-level with
query parameters for `a` and `b`.

### Filtering

Use query parameters for filtering, sorting, and field selection:

```text
GET /identities?kind=agent&sort=name&fields=name,email
```

Rules:

- Filter parameters match field names exactly.
- Multiple values for the same field use commas: `?kind=agent,human`.
- Sort direction: `sort=name` (ascending), `sort=-name` (descending).
- Unknown parameters are ignored with a warning header, not a 400. This
  allows clients to add parameters before the server supports them.

### Versioning in URLs

Version the API in the URL path when breaking changes are unavoidable:

```text
/v1/identities
/v2/identities
```

The version applies to the entire API surface, not individual endpoints.
Do not version individual resources (`/identities/v2/mal`).

## Request and Response Design

### Consistent Envelope

Every response follows the same structure. Clients parse one format, not
a different shape per endpoint.

For single resources:

```json
{
  "data": {
    "handle": "mal",
    "kind": "human",
    "email": "mal@serenity.ship"
  }
}
```

For collections:

```json
{
  "data": [
    {"handle": "mal", "kind": "human"},
    {"handle": "wash", "kind": "human"}
  ],
  "pagination": {
    "next_cursor": "eyJoYW5kbGUiOiJ3YXNoIn0=",
    "has_more": true
  }
}
```

For errors:

```json
{
  "error": {
    "code": "identity_not_found",
    "message": "Identity 'zoe' does not exist.",
    "details": {
      "handle": "zoe",
      "searched": [
        ".punt-labs/ethos/identities/",
        "~/.punt-labs/ethos/identities/"
      ]
    }
  }
}
```

### Field Naming

- snake_case for all field names. Never camelCase.
- Timestamps in RFC 3339: `"created_at": "2025-03-15T14:30:00Z"`.
- Booleans are positive: `is_active`, not `is_not_disabled`.
- IDs are strings, even if they look numeric. Numeric IDs overflow
  JavaScript's `Number.MAX_SAFE_INTEGER` at scale. Strings from day one.
- Null means absent. An empty string means the field exists and is empty.
  Do not conflate them.

### HATEOAS

Include links when they reduce client logic:

```json
{
  "data": {"handle": "mal", "kind": "human"},
  "links": {
    "self": "/v1/identities/mal",
    "talents": "/v1/identities/mal/talents",
    "sessions": "/v1/sessions?identity=mal"
  }
}
```

This is useful when the URL construction logic is non-trivial (nested
resources, pagination cursors). Do not add links that repeat what the client
already knows from the URL pattern.

## Error Handling

### Structured Errors

Every error response has the same shape. Clients write one error parser.

```json
{
  "error": {
    "code": "validation_failed",
    "message": "Request body has 2 validation errors.",
    "details": [
      {"field": "email", "reason": "not a valid email address"},
      {"field": "handle", "reason": "must be 1-32 lowercase alphanumeric characters"}
    ]
  }
}
```

### Error Codes

Error codes are stable machine-readable identifiers. They do not change
between versions. The message is human-readable and can change freely.

Use a flat namespace of descriptive codes:

```text
identity_not_found
validation_failed
rate_limit_exceeded
authentication_required
permission_denied
conflict_duplicate_handle
internal_error
```

Never use numeric error codes that require a lookup table. Never use the
HTTP status code as the error code -- they serve different purposes.

### Validation Errors

Return all validation errors at once. A client that submits a form with 3
invalid fields should see all 3 errors, not discover them one at a time
across 3 requests.

For nested objects, use dot notation for the field path:

```json
{"field": "address.postal_code", "reason": "required for US addresses"}
```

## Authentication

### When to Use Each

| Mechanism | Use case | Rotation | Complexity |
|-----------|----------|----------|------------|
| API key | Server-to-server, CLI tools | Manual | Low |
| OAuth 2.0 | Third-party apps, user delegation | Token refresh | High |
| JWT | Stateless auth, microservices | Short-lived + refresh | Medium |
| Session token | Browser apps, stateful servers | On each request (CSRF) | Medium |

### API Keys

- Sent in the `Authorization: Bearer <key>` header. Never in the URL --
  URLs end up in logs, browser history, and referer headers.
- Prefix keys with a tool identifier: `ethos_live_ak_...`, `ethos_test_ak_...`.
  This makes leaked keys identifiable and greppable.
- Support key rotation: allow two active keys simultaneously so clients can
  switch without downtime.
- Hash keys before storage. A leaked database should not expose API keys.

### JWT

- Short-lived access tokens (5-15 minutes). Long-lived refresh tokens
  (days-weeks) stored securely.
- Validate signature, issuer, audience, and expiration on every request.
  Do not skip audience validation -- it is how you prevent tokens meant for
  service A from being replayed against service B.
- Never store sensitive data in the JWT payload. Tokens are base64-encoded,
  not encrypted. Anyone with the token can read the claims.

### OAuth 2.0

- Use Authorization Code flow with PKCE for public clients (CLI tools,
  SPAs). Never Implicit flow -- it is deprecated for good reason.
- Store tokens in secure, httpOnly cookies for browsers. Never in
  localStorage.
- Implement token refresh transparently. The client should not handle 401
  responses manually.

## Rate Limiting

### Algorithms

**Token bucket**: allows bursts up to the bucket size, then refills at a
steady rate. Good for APIs where occasional bursts are acceptable.

**Sliding window**: counts requests in a rolling time window. More
predictable than token bucket, no burst allowance. Good for strict
rate enforcement.

Use token bucket unless the API has specific reasons to prevent bursts
(e.g., an expensive compute endpoint).

### Response Headers

Always include rate limit state in response headers:

```text
X-RateLimit-Limit: 100
X-RateLimit-Remaining: 42
X-RateLimit-Reset: 1710524400
```

When the limit is exceeded, return 429 with a `Retry-After` header:

```text
HTTP/1.1 429 Too Many Requests
Retry-After: 30

{
  "error": {
    "code": "rate_limit_exceeded",
    "message": "Rate limit exceeded. Try again in 30 seconds.",
    "details": {
      "limit": 100,
      "window_seconds": 60,
      "retry_after_seconds": 30
    }
  }
}
```

### Scoping

Rate limits should apply per API key or per user, not per IP address.
IP-based limits are unreliable: they break for corporate NATs (legitimate
users denied service) and create a denial-of-service vector (attackers
trigger limits to block all users on shared infrastructure). Use
per-API-key or per-user limits instead. When unauthenticated endpoints
need protection, use IP-based limits as a fallback with generous
thresholds.

## Pagination

### Cursor-Based Over Offset

Offset pagination (`?page=5&per_page=20`) breaks when items are inserted
or deleted between pages. Row 100 on page 5 becomes row 101 when a new item
is inserted, causing duplicates or skips.

Cursor pagination uses an opaque token pointing to a specific position:

```text
GET /identities?limit=20
GET /identities?limit=20&cursor=eyJoYW5kbGUiOiJ3YXNoIn0=
```

Response:

```json
{
  "data": [...],
  "pagination": {
    "next_cursor": "eyJoYW5kbGUiOiJ6b2UifQ==",
    "has_more": true
  }
}
```

Cursors are opaque to the client. Base64-encode them to discourage client
manipulation. The server decodes them to a `WHERE handle > 'wash'` clause.

### Total Count

Include `total_count` only when the client needs it and the query is cheap.
Counting all rows requires a full table scan on large datasets. Provide it
on request with `?include=total_count`, not by default.

### Page Size

- Default to a reasonable size (20-50 items).
- Allow clients to request up to a maximum (`?limit=100`).
- Document the maximum. Silently capping at 100 when the client asks for
  1000 is confusing; return 400 with a message.

## Versioning

### Strategies and Tradeoffs

**URL path versioning** (`/v1/identities`):

- Simple to implement and understand.
- Easy to route at the load balancer.
- Downside: the entire API version changes at once, even for endpoints
  that did not change.

**Header versioning** (`Accept: application/vnd.ethos.v2+json`):

- Cleaner URLs.
- Allows per-endpoint versioning.
- Downside: harder to test (cannot paste URL in browser), harder to cache.

**Content negotiation** (`Accept: application/json; version=2`):

- Most flexible.
- Downside: most complex, easy to get wrong.

Use URL path versioning unless there is a specific reason not to. It is the
most widely understood pattern and the hardest to misuse.

### Deprecation Policy

When retiring an API version:

1. Announce deprecation with a timeline (minimum 6 months for external APIs).
2. Add `Deprecation: true` and `Sunset: <date>` response headers.
3. Log usage of deprecated versions to identify affected clients.
4. After the sunset date, return 410 Gone with a migration guide URL.

## Documentation

### OpenAPI / Swagger

Maintain an OpenAPI 3.x spec as the source of truth. Generate docs, client
SDKs, and server stubs from it. Never hand-write what can be generated.

The spec lives in the repo (`openapi.yaml`), is validated in CI, and is
published alongside the API.

### Every Endpoint Needs

- Description of what it does (not how it is implemented).
- Request schema with field descriptions, types, and constraints.
- Response schema for every status code the endpoint can return.
- At least one request/response example.
- Authentication requirements.
- Rate limit tier (if different from the default).

### Error Catalog

Maintain a catalog of all error codes with:

- The error code string.
- HTTP status code it maps to.
- Human-readable description.
- Common causes and fixes.

This catalog is part of the API docs, not hidden in source code comments.

## Idempotency

### Safe Methods

GET, HEAD, and OPTIONS are safe -- they do not modify state. Clients and
proxies assume they can retry safe methods freely. If your GET handler has
side effects, it is broken.

### Idempotent Methods

PUT and DELETE are idempotent by the HTTP spec. Calling them twice produces
the same result as calling them once. If `DELETE /identities/mal` returns
204 the first time, it returns 404 (or 204) the second time -- either is
acceptable, but the resource is still deleted.

### Idempotency Keys for POST

POST is neither safe nor idempotent. To make it safe to retry (network
failures, timeouts), use idempotency keys:

```text
POST /identities
Idempotency-Key: a1b2c3d4-e5f6-7890-abcd-ef1234567890

{"handle": "mal", "kind": "human"}
```

Server behavior:

1. First request with this key: process normally, store the response keyed
   by the idempotency key.
2. Subsequent requests with the same key: return the stored response without
   re-processing.
3. Keys expire after 24 hours.

The client generates the key (UUIDv4). The server never generates it --
that defeats the purpose.

## CORS

### Origin Whitelist

Never use `Access-Control-Allow-Origin: *` for APIs that use credentials.
Maintain an explicit allowlist of origins:

```text
Access-Control-Allow-Origin: https://app.punt-labs.com
Access-Control-Allow-Credentials: true
```

### Preflight Caching

Preflight requests (OPTIONS) add latency to every cross-origin request.
Cache them aggressively:

```text
Access-Control-Max-Age: 86400
```

This tells the browser to cache the preflight response for 24 hours.
Without this header, the browser sends an OPTIONS request before every
non-simple request.

### Credentials Mode

When `Access-Control-Allow-Credentials: true` is set:

- `Access-Control-Allow-Origin` must be a specific origin, not `*`.
- `Access-Control-Allow-Headers` must list specific headers, not `*`.
- `Access-Control-Allow-Methods` must list specific methods, not `*`.

This is a common source of CORS errors. The `*` wildcard is incompatible
with credentials mode.

## Performance

### Caching Headers

```text
Cache-Control: public, max-age=3600           # cacheable for 1 hour
Cache-Control: private, no-cache              # revalidate every time
Cache-Control: no-store                       # never cache (sensitive data)
```

Use `Cache-Control` instead of `Expires`. Set appropriate cache lifetimes
based on how often the data changes. Static reference data can be cached
for hours; user-specific data should use `no-cache` with ETag validation.

### ETags

ETags enable conditional requests. The server returns an ETag with the
response:

```text
ETag: "a1b2c3d4"
```

The client sends it back on subsequent requests:

```text
If-None-Match: "a1b2c3d4"
```

If the resource has not changed, the server returns 304 Not Modified with
no body. This saves bandwidth and server processing for large resources.

Use strong ETags (exact byte-level match) for most cases. Weak ETags
(`W/"a1b2c3"`) when semantic equivalence is sufficient.

### Compression

Enable gzip or brotli compression for responses over 1 KB. Most HTTP
frameworks handle this transparently via middleware. Check the
`Accept-Encoding` request header and set `Content-Encoding` in the
response.

Do not compress already-compressed content (images, pre-compressed
archives). Do not compress responses under 1 KB -- the compression overhead
exceeds the savings.

### Batch Endpoints

When clients need multiple resources, provide a batch endpoint to avoid
N+1 request patterns:

```text
POST /identities/batch
{
  "handles": ["mal", "wash", "zoe"]
}
```

Response:

```json
{
  "data": [
    {"handle": "mal", "kind": "human"},
    {"handle": "wash", "kind": "human"},
    {"handle": "zoe", "kind": "human"}
  ],
  "errors": []
}
```

Batch endpoints should:

- Accept up to a documented maximum (e.g., 100 items).
- Return partial results with per-item errors, not fail entirely if one
  item is invalid.
- Be idempotent (batch reads) or use idempotency keys (batch writes).

### Connection Handling

- Support HTTP/2 for multiplexed requests over a single connection.
- Set reasonable timeouts: read timeout (10-30s), write timeout (30-60s),
  idle timeout (60-120s).
- Use keep-alive connections. Establishing a new TCP+TLS connection for
  every request adds 50-150ms of latency.
- Document expected response times. Clients need to know whether an endpoint
  returns in 50ms or 30 seconds to set appropriate timeouts.
