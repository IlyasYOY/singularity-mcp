# singularity-mcp

Go MCP server for the Singularity v2 REST API.

## Install

```bash
go install github.com/IlyasYOY/singularity-mcp/cmd/singularity-mcp@latest
```

## Run

```bash
singularity-mcp -token "$SINGULARITY_TOKEN"
```

Config precedence is CLI flag, then environment, then default:

- `-token` / `SINGULARITY_TOKEN` required for API calls
- `-base-url` / `SINGULARITY_BASE_URL`, default `https://api.singularity-app.com`
- `-timeout` / `SINGULARITY_TIMEOUT`, default `30s`

## Tools

The server exposes 8 merged tools with an `operation` enum:

- `singularity_projects`: `list`, `get`, `create`, `update`, `delete`
- `singularity_task_groups`: `list`, `get`, `create`, `update`, `delete`
- `singularity_tasks`: `list`, `inbox`, `get`, `create`, `update`, `delete`
- `singularity_habits`: `list`, `get`, `create`, `update`, `delete`
- `singularity_habit_progress`: `list`, `get`, `create`, `update`, `delete`
- `singularity_checklist_items`: `list`, `get`, `create`, `update`, `delete`
- `singularity_tags`: `list`, `get`, `create`, `update`, `delete`
- `singularity_time_stats`: `list`, `get`, `create`, `update`, `delete`, `delete_bulk`

Kanban operations are intentionally omitted.

## Generate And Test

```bash
make check
make test
make vet
make generate
make version
```
