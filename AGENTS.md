# Repository Guidelines

## Project Structure & Module Organization

This is a Go MCP server for the Singularity v2 REST API. The CLI entrypoint is
in `cmd/singularity-mcp/`. Internal packages live under `internal/`: `config`
parses flags and environment variables, `singularity` contains the OpenAPI
catalog, API client, errors, and generated client code, and `tools` builds the
MCP server and tool schemas. Embedded OpenAPI assets are in `openapi/`, with
`openapi/singularity-v2.json` as the checked-in API snapshot. Tests sit beside
their packages as `*_test.go`.

## Build, Test, and Development Commands

- `make check`: default CI command; runs tests, then vet.
- `make test`: runs unit, integration-style in-process MCP tests, and the stdio
  smoke tests. Live tests are skipped unless explicitly enabled.
- `make vet`: runs Go static checks.
- `make generate`: regenerates OpenAPI-derived code from the checked-in schema
  and `openapi/oapi-codegen.yaml`.
- `make version`: verifies the command starts and prints its version.
- `go run ./cmd/singularity-mcp -token "$SINGULARITY_TOKEN"`: runs the server
  locally over stdio.

## Coding Style & Naming Conventions

Use standard Go formatting and idioms. Run `gofmt` on edited Go files before
review. Keep package names short and lowercase, exported identifiers descriptive
only when they are part of cross-package API, and errors wrapped with context.
Do not hand-edit generated OpenAPI output except to fix the generator inputs.

## Testing Guidelines

Use Go's standard `testing` package. Name tests `Test<Behavior>` and keep them
near the code they exercise. Prefer `httptest` and in-process MCP clients for
deterministic coverage. Live API tests must remain opt-in behind
`SINGULARITY_LIVE=1` and require `SINGULARITY_TOKEN`.

## Commit & Pull Request Guidelines

History uses Conventional Commits, for example
`feat: initialize singularity mcp server`. Keep commits focused and describe
the behavior change, not only the files touched. Pull requests should include a
short summary, verification commands run, and any configuration or token-related
notes. Mention API snapshot or generated-code changes explicitly when present.

## Security & Configuration Tips

Never commit real Singularity tokens. Configuration precedence is CLI flag,
then environment, then defaults. Use `SINGULARITY_TOKEN`,
`SINGULARITY_BASE_URL`, and `SINGULARITY_TIMEOUT` for local runs and tests.
