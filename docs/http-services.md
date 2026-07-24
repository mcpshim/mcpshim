# Configured HTTP services

MCPShim can expose an ordinary HTTP API through the same local interface as an
MCP server. A configured HTTP service appears in `mcpshim servers`, contributes
tools to `mcpshim tools`, supports `inspect` and `call`, participates in shell
wrapper generation, and records calls in the same history store.

Each service can expose either or both of these interfaces:

- Typed tools define a stable tool name, input schema, request path, query,
  headers, and optional body template.
- A raw tool is a constrained curl-like request. The caller selects the method,
  relative path, query, approved headers, content type, and body.

The full example is
[`configs/http-services.example.yaml`](../configs/http-services.example.yaml).

## Service model

```yaml
http_services:
  - name: deployment-api
    alias: deploy
    description: Manage application deployments
    base_url: https://deployments.example.com/api
    headers:
      Authorization: Bearer ${DEPLOY_API_TOKEN}
      Accept: application/json
    policy:
      timeout_seconds: 20
      max_request_bytes: 1048576
      max_response_bytes: 2097152
      redirects: same-origin
      allowed_request_content_types: [application/json]
      allowed_response_content_types: [application/json]
      success_statuses: [200, 201, 202, 204]
      response_headers: [ETag, Location, X-Request-ID]
    raw_tool:
      name: request
      methods: [GET, POST, PATCH]
      paths:
        - /v1/deployments/**
      request_headers: [If-Match, Idempotency-Key]
    tools: []
```

`base_url` must be an absolute `http` or `https` URL without user information,
a query, or a fragment. A base path is allowed. Every configured request path
is appended below it.

Service headers are daemon-owned. They are the right place for authentication,
tenant selection, and a fixed `Accept` header. Values support environment
references, and those references are not replaced with secrets when MCPShim
saves the configuration. Raw and typed callers cannot override configured
authentication or other protected transport headers. A non-protected service
header can be replaced only when the operator explicitly exposes the same name
in a typed request or the raw `request_headers` allowlist.

Server names and aliases share one namespace across MCP servers and HTTP
services. This keeps commands and history filters unambiguous.

## Typed tools

A typed tool turns explicit inputs into one fixed request shape:

```yaml
tools:
  - name: create_deployment
    description: Create a deployment
    request:
      method: POST
      path: /v1/teams/{team_id}/deployments
      query:
        dry_run:
          $arg: dry_run
          $default: false
      headers:
        Idempotency-Key:
          $arg: idempotency_key
      body:
        content_type: application/json
        template:
          release:
            name:
              $format: "{service}:{version}"
            target:
              environment:
                $arg: environment
              region:
                $arg: region
                $default: us-east-1
            metadata:
              labels:
                $arg: labels
                $default: {}
              ticket:
                $arg: ticket
                $omit_if_missing: true
    inputs:
      team_id:
        type: string
        required: true
      service:
        type: string
        required: true
      version:
        type: string
        required: true
      environment:
        type: string
        required: true
        enum: [staging, production]
      region:
        type: string
      labels:
        type: object
      ticket:
        type: string
      dry_run:
        type: boolean
      idempotency_key:
        type: string
        required: true
```

Path placeholders must occupy a full segment, such as `{team_id}`. Their input
must be required and typed as a string or integer. MCPShim escapes the value as
one path segment and rejects empty, slash, backslash, and traversal values.

Inputs support these types:

- `string`
- `integer`
- `number`
- `boolean`
- `object`
- `array`, with an optional nested `items` definition

An input can also set `description`, `required`, `default`, `enum`, `minimum`,
and `maximum`. Unknown inputs and values of the wrong type fail before any HTTP
request is made.

### JSON templates

Templates can contain literal JSON scalars, objects, and arrays at any level.
Nesting is supported up to 64 levels. A template marker is an object with one
of these forms:

```yaml
# Insert a value without converting its JSON type.
field:
  $arg: input_name

# Insert a template fallback when the input is absent.
field:
  $arg: input_name
  $default:
    nested: value

# Remove this object key or array item when the input is absent.
field:
  $arg: input_name
  $omit_if_missing: true

# Interpolate scalar inputs into a string.
field:
  $format: "{service}:{version}"
```

`$arg` preserves nested objects and arrays. `$default` is itself a template, so
it may also be nested. `$omit_if_missing` removes only absent values; an
explicit invalid or null typed value is rejected. `$format` accepts scalar
values and always produces a string.

Query and request-header values use the same template syntax. Array query
values become repeated parameters. Object query values are encoded as compact
JSON.

Typed bodies currently support `application/json`, structured `+json` media
types, and `text/*`. JSON templates are encoded as JSON. A text body template
must render to a string.

### Calling a typed tool

```bash
mcpshim inspect --server deploy --tool create_deployment

mcpshim --json call \
  --server deploy \
  --tool create_deployment \
  --team_id platform \
  --service billing \
  --version v2.4.0 \
  --environment production \
  --labels '{"tier":"critical","region":"global"}' \
  --ticket OPS-142 \
  --idempotency_key billing-v2.4.0
```

## Raw request tool

The raw tool accepts this call shape:

```json
{
  "method": "PATCH",
  "path": "/v1/deployments/dep-123",
  "query": {
    "include": ["status", "events"]
  },
  "headers": {
    "If-Match": "\"revision-3\""
  },
  "content_type": "application/json",
  "body": {
    "state": "paused"
  }
}
```

The corresponding CLI call is:

```bash
mcpshim --json call \
  --server deploy \
  --tool request \
  --method PATCH \
  --path /v1/deployments/dep-123 \
  --query '{"include":["status","events"]}' \
  --headers '{"If-Match":"\"revision-3\""}' \
  --content_type application/json \
  --body '{"state":"paused"}'
```

The raw tool is intentionally narrower than curl:

- The caller cannot choose a scheme, host, port, or absolute URL.
- `methods` is an allowlist. Supported configured methods are `GET`, `HEAD`,
  `POST`, `PUT`, `PATCH`, `DELETE`, and `OPTIONS`.
- `paths` is an allowlist. `*` matches exactly one segment and `**` matches zero
  or more segments. Wildcards must occupy a complete segment.
- Paths containing a query, fragment, backslash, encoded traversal, or
  cross-origin form are rejected.
- Only names in `request_headers` can be supplied. Authentication, host,
  connection, content length, and transfer headers cannot be supplied.
- `GET` and `HEAD` requests cannot include a body.
- A body defaults to `application/json`. Use a `text/*` content type for a
  string body.

An omitted `raw_tool` means callers can use only the explicitly modeled typed
tools.

## Policies and responses

Policy defaults are:

| Setting | Default |
| --- | --- |
| `timeout_seconds` | 20 |
| `max_request_bytes` | 1 MiB |
| `max_response_bytes` | 2 MiB |
| `redirects` | `same-origin` |
| successful status | any 2xx status |
| request and response content types | unrestricted |
| returned response headers | none |

`timeout_seconds` must be between 1 and the daemon call limit of 60 seconds.
`redirects` accepts `disabled`, `same-origin`, or `any`. Use `any` only when a
cross-origin redirect is part of the trusted API contract. Response headers are
deny-by-default and must be named in `response_headers`. Sensitive headers such
as `Set-Cookie` cannot be exposed.

Every successful call returns:

```json
{
  "status": 201,
  "content_type": "application/json",
  "headers": {
    "location": "/v1/deployments/dep-123",
    "x-request-id": "req-456"
  },
  "body": {
    "id": "dep-123",
    "state": "pending"
  },
  "truncated": false
}
```

JSON responses are decoded into JSON values. Other allowed responses are
returned as strings. A disallowed status or content type, or a response larger
than the configured bound, makes the tool call fail while preserving the
bounded response in the JSON `result` field. This lets automation inspect API
error bodies without treating the call as successful.

## Operating the configuration

Configured HTTP services are YAML-managed in this release. The `add`, `remove`,
and `set auth` commands continue to manage MCP servers only. After editing the
file:

```bash
mcpshim validate --config ~/.config/mcpshim/config.yaml
mcpshim reload
mcpshim servers
mcpshim tools --server deploy
```

Raw and typed HTTP calls are recorded in history under the canonical service
name, even when the alias was used:

```bash
mcpshim history --server deploy
```

History includes full call arguments, including raw bodies. Treat the database
as sensitive and set an appropriate retention limit.
