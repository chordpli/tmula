# syntax=docker/dockerfile:1
#
# Multi-stage build for the tmula demo image. One image carries three binaries —
# the control plane (with the real React UI embedded) and the two example SUTs —
# so docker-compose can run the whole demo without any Go/Node toolchain on the
# host. A plain `make build` only embeds a placeholder UI; this image runs the
# equivalent of `make embed`, so the console works out of the box.

# 1) Build the React control-plane UI into web/dist.
FROM node:20-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# 2) Build the Go binaries with the freshly built UI embedded.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Replace the committed placeholder UI with the real build before `go build`
# bakes server/internal/web/static into the binary via go:embed.
RUN rm -rf server/internal/web/static/assets
COPY --from=web /web/dist/ server/internal/web/static/
ARG VERSION=docker
WORKDIR /src/server
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/tmula          ./cmd/tmula \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w"                            -o /out/sample-api     ./examples/sample-api \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w"                            -o /out/ticketing-api  ./examples/ticketing-api

# 3) Slim runtime carrying all three static binaries.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates wget \
 && adduser -D -u 10001 tmula
COPY --from=build /out/ /usr/local/bin/
USER tmula
EXPOSE 8080
# No ENTRYPOINT, so a compose `command:` fully replaces this — the SUT services
# run `sample-api` / `ticketing-api` from the same image.
CMD ["tmula", "--role", "local", "--addr", ":8080"]
