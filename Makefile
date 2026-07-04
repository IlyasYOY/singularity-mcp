.DEFAULT_GOAL := check

GO ?= go
COVERPROFILE ?= coverage.out
COVERAGE_THRESHOLD ?= 80
COVERAGE_EXCLUDE ?= /api\.gen\.go:

.PHONY: check fix test coverage coverage-check vet generate install version

check: fix vet coverage-check

fix:
	$(GO) fix ./...

test:
	$(GO) test ./...

coverage:
	$(GO) test -coverpkg=./... -coverprofile=$(COVERPROFILE) ./...
	$(GO) tool cover -func=$(COVERPROFILE)

coverage-check:
	$(GO) test -coverpkg=./... -coverprofile=$(COVERPROFILE) ./...
	@awk -v threshold="$(COVERAGE_THRESHOLD)" -v exclude='$(COVERAGE_EXCLUDE)' '\
		BEGIN { threshold += 0 } \
		/^mode:/ { next } \
		exclude != "" && $$1 ~ exclude { next } \
		{ statements[$$1] = $$2; if ($$3 > 0) covered[$$1] = 1 } \
		END { \
			for (block in statements) { \
				total += statements[block]; \
				if (covered[block]) hit += statements[block]; \
			} \
			if (total == 0) { print "coverage: no statements after exclusions"; exit 1 } \
			pct = hit * 100 / total; \
			printf "coverage: %.1f%% (threshold %.1f%%)\n", pct, threshold; \
			if (pct + 0.000001 < threshold) exit 1; \
		}' $(COVERPROFILE)

vet:
	$(GO) vet ./...

generate:
	$(GO) generate ./...

install:
	$(GO) install ./cmd/singularity-mcp

version:
	$(GO) run ./cmd/singularity-mcp -version
