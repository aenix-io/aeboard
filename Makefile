BINARY := aenotes
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build frontend backend lint test test-web fmt tidy run

all: build

## frontend: build the SPA into web/dist (embedded into the Go binary)
frontend:
	cd web && npm ci && npm run build

## build: the SPA, then the single self-contained binary
build: frontend backend

## backend: the Go binary only (expects web/dist to exist)
backend:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/aenotes

lint:
	golangci-lint run

## test: Go tests (the git tests need the git binary for file:// remotes)
test:
	go test ./...

test-web:
	cd web && npm run typecheck && npm test

fmt:
	golangci-lint fmt

tidy:
	go mod tidy

## run: serve from source with boards as plain files under ./data
run:
	go run ./cmd/aenotes serve
