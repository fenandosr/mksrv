SHELL := /usr/bin/env bash
BINARY := bin/mksrv
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || printf unknown)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: build test lint snapshot schema-lint tf-validate validate-example hygiene clean

build:
	@mkdir -p bin
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/mksrv

test:
	go test ./...

lint:
	@test -z "$$(gofmt -l .)" || (echo "gofmt changes required" >&2; gofmt -l . >&2; exit 1)
	go vet ./...
	@if command -v staticcheck >/dev/null 2>&1; then staticcheck ./...; else echo "staticcheck not installed; CI installs it" >&2; fi

snapshot:
	@if command -v goreleaser >/dev/null 2>&1; then \
	  goreleaser release --snapshot --clean; \
	else \
	  echo "goreleaser not installed; using the checked-in offline snapshot fallback" >&2; \
	  VERSION=$(VERSION) COMMIT=$(COMMIT) DATE=$(DATE) ./scripts/snapshot.sh; \
	fi

schema-lint:
	go test ./internal/schema -run TestEmbeddedSchemasAreValidJSON
	go run ./cmd/mksrv validate examples/workspace --json >/dev/null

validate-example: build
	$(BINARY) validate examples/workspace

hygiene:
	./scripts/public-hygiene.sh
	@if command -v gitleaks >/dev/null 2>&1; then gitleaks detect --no-banner --redact; else echo "gitleaks not installed; CI runs it" >&2; fi

tf-validate:
	@if ! command -v terraform >/dev/null 2>&1; then \
	  echo "terraform not installed; skipping locally (CI runs it)" >&2; \
	  exit 0; \
	fi; \
	terraform fmt -check -recursive infra; \
	for dir in infra/root infra/modules/*; do \
	  echo "==> terraform validate $$dir"; \
	  terraform -chdir="$$dir" init -backend=false -input=false >/dev/null; \
	  terraform -chdir="$$dir" validate; \
	done

clean:
	rm -rf bin dist coverage.out
