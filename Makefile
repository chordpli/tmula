# tmula — build/test pipeline (SSOT for local + CI commands)
SHELL := /bin/bash
BINARY := bin/tmula
PKG := ./...
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: all build go-build web-build embed web dev test vet fmt lint run clean tidy

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

## run: build and run a local engine on :8080 (placeholder UI; use `make web` for the real UI).
run: build
	$(BINARY) --role local --addr :8080

## web: build the real React UI into the binary and run the engine on :8080.
## This is the one command to open the browser console — http://localhost:8080.
web: embed
	@echo "tmula web console: open http://localhost:8080"
	$(BINARY) --role local --addr :8080

## dev: hot-reload UI dev server (proxies /api to a separately running engine).
## Run `make run` (or `make web`) in another terminal first.
dev:
	cd web && npm install && npm run dev

tidy:
	go mod tidy

clean:
	rm -rf bin web/dist internal/web/static/assets
