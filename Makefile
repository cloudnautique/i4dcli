.DEFAULT_GOAL := build

GO ?= go
GOFMT ?= gofmt
BINARY ?= bin/i4d
STATICCHECK_VERSION := v0.8.2

.PHONY: build test vet generate lint ci

build:
	@mkdir -p "$(dir $(BINARY))"
	$(GO) build -o "$(BINARY)" ./cmd/i4d

test:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

generate:
	$(GO) generate ./...

lint:
	@set -e; unformatted=$$($(GOFMT) -l cmd internal); \
	if [ -n "$$unformatted" ]; then \
		printf 'Run gofmt on these files:\n%s\n' "$$unformatted"; \
		exit 1; \
	fi
	$(GO) run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) ./...

# Keep generation ahead of every check, including with make -j ci.
ci:
	$(MAKE) generate
	$(MAKE) build
	$(MAKE) vet
	$(MAKE) lint
	$(MAKE) test
