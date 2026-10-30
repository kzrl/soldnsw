# SoldProperty.au — build, generate, and run targets.
#
# Quick start:
#   make build   # runs sqlc generate, then builds all binaries into bin/
#   make run     # builds, then starts the web server
#   make test    # runs the Go test suite
#
# Override defaults, for example:
#   make run PORT=9090 DB=properties.db

SHELL := /bin/bash

GO         ?= go
SQLC       ?= sqlc
TAGS       ?= sqlite_fts5
BIN_DIR    ?= bin
DB         ?= properties.db
PORT       ?= 8080
TEMPLATES  ?= templates
STATIC     ?= static
DATA_DIR   ?= data/nsw_weekly
WORKERS    ?= 4
BATCH      ?= 5000

SERVER     := $(BIN_DIR)/server
INGEST     := $(BIN_DIR)/ingest
DOWNLOADER := $(BIN_DIR)/downloader
IMPORT_NSW := $(BIN_DIR)/import_nsw

.DEFAULT_GOAL := build
.PHONY: all generate build run run-server run-ingest run-downloader run-import \
        test vet fmt tidy check clean help

all: build

## generate: run sqlc generate (required before build)
generate:
	@command -v $(SQLC) >/dev/null 2>&1 || { \
		echo "error: sqlc not found in PATH."; \
		echo "Install it with: go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest"; \
		exit 1; \
	}
	@echo "==> Generating sqlc code from db/query.sql"
	cd db && $(SQLC) generate

## build: generate sqlc code and build all binaries
build: generate
	@mkdir -p $(BIN_DIR)
	@echo "==> Building $(SERVER)"
	$(GO) build -tags "$(TAGS)" -trimpath -o $(SERVER) ./cmd/server
	@echo "==> Building $(INGEST)"
	$(GO) build -tags "$(TAGS)" -trimpath -o $(INGEST) ./cmd/ingest
	@echo "==> Building $(DOWNLOADER)"
	$(GO) build -tags "$(TAGS)" -trimpath -o $(DOWNLOADER) ./cmd/downloader
	@echo "==> Building $(IMPORT_NSW)"
	$(GO) build -tags "$(TAGS)" -trimpath -o $(IMPORT_NSW) ./cmd/import_nsw

## run: build and start the web server (the app)
run: build
	@echo "==> Starting server on http://localhost:$(PORT)"
	./$(SERVER) -db $(DB) -port $(PORT) -templates $(TEMPLATES) -static $(STATIC)

run-server: run

## run-ingest: seed the sample G-NAF and sales data
run-ingest: build
	./$(INGEST) -db $(DB)

## run-downloader: download NSW Valuer General bulk PSI archives
run-downloader: build
	./$(DOWNLOADER) -out $(DATA_DIR) -workers $(WORKERS)

## run-import: import downloaded NSW PSI archives into the database
run-import: build
	./$(IMPORT_NSW) -data $(DATA_DIR) -db $(DB) -batch $(BATCH)

## test: run the Go test suite
test:
	$(GO) test -tags "$(TAGS)" ./...

## vet: run go vet
vet:
	$(GO) vet -tags "$(TAGS)" ./...

## fmt: format Go source files
fmt:
	gofmt -w cmd internal

## tidy: tidy go.mod/go.sum
tidy:
	$(GO) mod tidy

## check: generate, vet and test
check: generate vet test

## clean: remove built binaries
clean:
	rm -rf $(BIN_DIR)

## help: show this help
help:
	@echo "SoldNSW make targets:"
	@echo ""
	@grep -E '^## [a-z-]+:' $(MAKEFILE_LIST) | sed -E 's/^## /  /'
	@echo ""
	@echo "Variables: GO=$(GO) SQLC=$(SQLC) TAGS=$(TAGS) DB=$(DB) PORT=$(PORT) BIN_DIR=$(BIN_DIR)"
