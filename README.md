# singularity-mcp

Go MCP server for the Singularity v2 REST API.

## Install

Install the latest published version:

```bash
go install github.com/IlyasYOY/singularity-mcp/cmd/singularity-mcp@latest
```

Install the current local checkout:

```bash
make install
```

## Release

Releases are created manually from GitHub Actions:

1. Open Actions, then run the `Release` workflow from `main`.
2. Choose `patch`, `minor`, or `major`.
3. The workflow bumps the CLI version, commits `chore: release vX.Y.Z`,
   creates the annotated tag, and publishes the GitHub Release.

## Run

### Stdio

```bash
singularity-mcp -token "$SINGULARITY_TOKEN"
singularity-mcp -help
```

### Streamable HTTP And HTTPS

HTTP mode takes the Singularity token from every MCP request and never falls
back to a token configured on the process:

```http
Authorization: Bearer <singularity-token>
```

Run cleartext HTTP on loopback for a same-host reverse proxy:

```bash
singularity-mcp -transport http -http-address 127.0.0.1:8080
```

Expose `https://singularity.example.com/mcp` from Caddy, forwarding to the
loopback listener:

```caddyfile
singularity.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

Or serve HTTPS directly with certificate files:

```bash
singularity-mcp \
  -transport http \
  -http-address :8443 \
  -tls-cert /etc/letsencrypt/live/singularity.example.com/fullchain.pem \
  -tls-key /etc/letsencrypt/live/singularity.example.com/privkey.pem
```

Native HTTPS requires TLS 1.2 or newer. Without certificate flags, the server
refuses non-loopback listen addresses so a bearer token cannot accidentally be
sent over public cleartext HTTP. `GET /healthz` is unauthenticated and returns
only `{"status":"ok"}`; `/mcp` requires the bearer header for `POST`, `GET`, and
`DELETE`. Do not put tokens in the MCP URL, query string, logs, or proxy config.

The current ChatGPT developer-mode documentation supports streaming HTTP and
requires a reachable HTTPS MCP endpoint, but its documented authentication
modes are OAuth, no authentication, and mixed OAuth/no-auth. It does not offer a
raw API-key header mode. Therefore this pass-through mode works with MCP clients
that can set HTTP headers, while direct ChatGPT linking remains deferred until
OAuth is implemented. Do not work around this by exposing a no-auth server with
a process-level Singularity token.

- [Connect from ChatGPT](https://developers.openai.com/apps-sdk/deploy/connect-chatgpt)
- [ChatGPT developer mode](https://developers.openai.com/api/docs/guides/developer-mode)

Config precedence is CLI flag, then environment, then default:

- `-token` / `SINGULARITY_TOKEN` used by stdio and rejected in HTTP mode
- `-base-url` / `SINGULARITY_BASE_URL`, default `https://api.singularity-app.com`
- `-timeout` / `SINGULARITY_TIMEOUT`, default `30s` (each HTTP request)
- `-approval-timeout` / `SINGULARITY_MCP_APPROVAL_TIMEOUT`, default `2m`
- `-operation-timeout` / `SINGULARITY_MCP_OPERATION_TIMEOUT`, default `2m`
- `-max-pages` / `SINGULARITY_MCP_MAX_PAGES`, default `100`
- `-max-items` / `SINGULARITY_MCP_MAX_ITEMS`, default `10000`
- `-max-response-bytes` / `SINGULARITY_MCP_MAX_RESPONSE_BYTES`, default `1048576` (1 MiB)
- `-require-write-approval` / `SINGULARITY_MCP_REQUIRE_WRITE_APPROVAL`, default `true`
- `-transport` / `SINGULARITY_MCP_TRANSPORT`, default `stdio`; values `stdio`, `http`
- `-http-address` / `SINGULARITY_MCP_HTTP_ADDRESS`, default `127.0.0.1:8080`
- `-http-path` / `SINGULARITY_MCP_HTTP_PATH`, default `/mcp`
- `-tls-cert` / `SINGULARITY_MCP_TLS_CERT`, native HTTPS certificate file
- `-tls-key` / `SINGULARITY_MCP_TLS_KEY`, native HTTPS private key file
- `-version` prints the CLI version and exits
- `-help` / `-h` prints CLI usage and exits

When write approval is enabled, read-only operations run normally, while write
operations request MCP elicitation approval before the Singularity API call is
made. Read-only operations are `list`, `get`, `inbox`, `overdue`, `today`, and
`only-today`; all other operations, including `create`, `update`, `delete`, and
`delete_bulk`, require approval.
Approval requests time out after two minutes by default. A timeout fails closed,
so the write is blocked without calling the Singularity API. Late approval responses
are ignored, including responses from handlers that do not honor cancellation.
Three distinct timeout boundaries apply:

- the request timeout (`-timeout`) bounds each individual HTTP attempt;
- the operation timeout (`-operation-timeout`) bounds the complete API execution,
  including pagination and retry waits, and starts only after write approval;
- the approval timeout (`-approval-timeout`) bounds only the MCP elicitation wait.

As with all stdio MCP traffic, the client must continue draining server stdout for
timeout results and other protocol messages to be delivered.
Streamable HTTP clients must keep the listening `GET` connection active when
they advertise elicitation support, because write approval requests are sent on
that server-to-client stream. Clients without elicitation support continue to
fail closed for writes unless the operator explicitly disables write approval.
Set `-require-write-approval=false` or
`SINGULARITY_MCP_REQUIRE_WRITE_APPROVAL=false` only for trusted clients or
environments where write prompts are intentionally disabled.

## Tools

The server exposes 8 stable merged tools. Each tool publishes a strict root
`oneOf` schema with one variant per operation, so only fields relevant to the
selected operation are accepted. The MCP server validates these schemas before
the handler or Singularity API is reached:

- `singularity_projects`: `list`, `search`, `get`, `create`, `update`, `delete`
- `singularity_task_groups`: `list`, `get`, `create`, `update`, `delete`
- `singularity_tasks`: `list`, `inbox`, `overdue`, `today`, `only-today`, `search`, `get`, `create`, `update`, `delete`
- `singularity_habits`: `list`, `get`, `create`, `update`, `delete`
- `singularity_habit_progress`: `list`, `get`, `create`, `update`, `delete`
- `singularity_checklist_items`: `list`, `get`, `create`, `update`, `delete`
- `singularity_tags`: `list`, `search`, `get`, `create`, `update`, `delete`
- `singularity_time_stats`: `list`, `get`, `create`, `update`, `delete`, `delete_bulk`

Kanban operations are intentionally omitted.

Write calls are normalized and semantically validated before approval is
requested. The approval preview therefore contains the same normalized payload
that is sent to Singularity. Successful calls return machine-readable structured
JSON while retaining the JSON text content fallback for older MCP clients.

Discovery resources remain available at `singularity://capabilities` and
`singularity://openapi`.

Task date helpers are computed in the MCP client layer:

- `overdue`: active tasks with `start` before today
- `today`: active tasks with `start` today or earlier
- `only-today`: active tasks with `start` today only

Calendar days use the MCP process's local timezone. Full timestamps are
converted into that timezone before their calendar date is compared. A task is
active when it is not removed and neither `checked` nor `complete` is non-zero;
missing default fields are treated as zero. Direct task `startDateFrom` and
`startDateTo` filters must be RFC3339 timestamps because the live API rejects
date-only values even though its published OpenAPI schema declares plain strings.

Search helpers are computed in the MCP client layer for tasks, projects, and
tags. They fetch and filter one page at a time, stopping as soon as `limit+1`
matches establish truncation. Search uses case-insensitive substring matching;
`title` is searched by default, and `id` can be selected explicitly. Note-content
search is not exposed because Singularity
list/get responses contain opaque note IDs rather than note text. Project search
also supports client-side `parent` and `isNotebook` filters. `all=false`
examines exactly one API page; the default `all=true` remains bounded by the
configured page, item, response-size, and operation-time limits. It is not an
unlimited export mode.

Bounded list/search results include additive continuation metadata when more data
may exist. For example:

```json
{
  "pagination": {
    "scannedPages": 2,
    "scannedItems": 1375,
    "truncated": true,
    "morePagesPossible": true,
    "nextOffset": 1375,
    "reason": "max_items"
  }
}
```

`nextOffset` identifies the first unconsumed entity. Clients may continue from
that offset, but each continuation is independently bounded. Repeated non-empty
pages fail with `pagination_stalled`, and oversized responses fail with
`response_too_large` rather than being reported as malformed JSON.

GET requests retry only HTTP 429, 502, 503, and 504 responses, with at most three
total attempts. A valid integer-seconds or HTTP-date `Retry-After` value is
honored; otherwise bounded exponential backoff with deterministic jitter is used.
Cancellation interrupts retry waiting. POST, PATCH, and DELETE requests are never
retried, so an approved write is attempted once.

Examples:

```json
{"operation":"search","query":"mcp","fields":["title"],"limit":10}
```

```json
{"operation":"search","projectId":"P-...","tag":"TG-...","query":"review"}
```

By default, search traverses pages with `maxCount=1000` until it finds enough
matches, reaches the upstream end, or hits a configured bound; pass `all=false`
to inspect only the first API page.

## Generate And Test

`make check` applies Go fixers, runs vet, and enforces 80% coverage for
handwritten code. `make coverage` uses cross-package instrumentation so
integration tests count covered code in other packages. CI uses Go 1.26.5 and
runs the pinned `govulncheck` v1.6.0 scanner before the full check.

```bash
make check
make fix
make test
make coverage
make coverage-check
make vet
make generate
make install
make version
```

Opt-in read-only live contract tests exercise every list endpoint and the task
calendar helpers against the configured Singularity account:

```bash
SINGULARITY_LIVE=1 go test ./internal/singularity -run TestLive
```
