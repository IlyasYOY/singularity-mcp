.DEFAULT_GOAL := check

GO ?= go

.PHONY: check test vet generate version

check: test vet

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

generate:
	$(GO) generate ./...

version:
	$(GO) run ./cmd/singularity-mcp -version
