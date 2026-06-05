# tmula — build/test pipeline (SSOT for local + CI commands)
SHELL := /bin/bash
BINARY := bin/tmula
PKG := ./...
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: all build go-build web-build embed test vet fmt lint run clean tidy

all: build

## build: build the single Go binary (embeds the committed UI placeholder).
build: go-build

go-build:
	@mkdir -p bin
	go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY) ./cmd/engine

## web-build: build the React UI (verifies the front-end compiles).
web-build:
	cd web && npm ci && npm run build

## embed: build the UI and copy assets into the Go embed dir, then build.
embed: web-build
	rm -rf internal/web/static/assets
	cp -R web/dist/. internal/web/static/
	$(MAKE) go-build

## test: run Go unit tests.
test:
	go test $(PKG)

vet:
	go vet $(PKG)

## fmt: format Go sources in place.
fmt:
	gofmt -w .

## lint: vet + gofmt verification (fails if any file needs formatting).
lint: vet
	@unformatted="$$(gofmt -l . | grep -v '/node_modules/' || true)"; \
	if [ -n "$$unformatted" ]; then echo "gofmt needed on:"; echo "$$unformatted"; exit 1; fi

## run: build and run a local engine on :8080.
run: build
	$(BINARY) --role local --addr :8080

tidy:
	go mod tidy

clean:
	rm -rf bin web/dist internal/web/static/assets
