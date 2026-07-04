.DEFAULT_GOAL := check

GO ?= go
COVERPROFILE ?= coverage.out

.PHONY: check fix test coverage vet generate install version

check: fix vet test

fix:
	$(GO) fix ./...

test:
	$(GO) test ./...

coverage:
	$(GO) test -coverpkg=./... -coverprofile=$(COVERPROFILE) ./...
	$(GO) tool cover -func=$(COVERPROFILE)

vet:
	$(GO) vet ./...

generate:
	$(GO) generate ./...

install:
	$(GO) install ./cmd/singularity-mcp

version:
	$(GO) run ./cmd/singularity-mcp -version
