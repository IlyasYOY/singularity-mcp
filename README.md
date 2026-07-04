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

```bash
singularity-mcp -token "$SINGULARITY_TOKEN"
```

Config precedence is CLI flag, then environment, then default:

- `-token` / `SINGULARITY_TOKEN` required for API calls
- `-base-url` / `SINGULARITY_BASE_URL`, default `https://api.singularity-app.com`
- `-timeout` / `SINGULARITY_TIMEOUT`, default `30s`
- `-require-write-approval` / `SINGULARITY_MCP_REQUIRE_WRITE_APPROVAL`, default `true`

When write approval is enabled, read-only operations run normally, while write
operations request MCP elicitation approval before the Singularity API call is
made. Read-only operations are `list`, `get`, `inbox`, `overdue`, `today`, and
`only-today`; all other operations, including `create`, `update`, `delete`, and
`delete_bulk`, require approval.
Set `-require-write-approval=false` or
`SINGULARITY_MCP_REQUIRE_WRITE_APPROVAL=false` only for trusted clients or
environments where write prompts are intentionally disabled.

## Tools

The server exposes 8 merged tools with an `operation` enum:

- `singularity_projects`: `list`, `get`, `create`, `update`, `delete`
- `singularity_task_groups`: `list`, `get`, `create`, `update`, `delete`
- `singularity_tasks`: `list`, `inbox`, `overdue`, `today`, `only-today`, `get`, `create`, `update`, `delete`
- `singularity_habits`: `list`, `get`, `create`, `update`, `delete`
- `singularity_habit_progress`: `list`, `get`, `create`, `update`, `delete`
- `singularity_checklist_items`: `list`, `get`, `create`, `update`, `delete`
- `singularity_tags`: `list`, `get`, `create`, `update`, `delete`
- `singularity_time_stats`: `list`, `get`, `create`, `update`, `delete`, `delete_bulk`

Kanban operations are intentionally omitted.

Task date helpers are computed in the MCP client layer:

- `overdue`: active tasks with `start` before today
- `today`: active tasks with `start` today or earlier
- `only-today`: active tasks with `start` today only

## Generate And Test

`make check` applies Go fixers, runs vet, and enforces 80% coverage for
handwritten code. `make coverage` uses cross-package instrumentation so
integration tests count covered code in other packages.

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
