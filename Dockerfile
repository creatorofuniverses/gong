# syntax=docker/dockerfile:1

FROM golang:1.26.8-alpine3.23 AS build

ARG VERSION=dev

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-buildvcs=false \
    go build -trimpath \
    -ldflags="-s -w -X github.com/creatorofuniverses/gong/internal/cli.Version=${VERSION}" \
    -o /out/gong ./cmd/gong

FROM alpine:3.23.5

RUN apk add --no-cache ca-certificates wget \
    && addgroup -S -g 10001 gong \
    && adduser -S -D -H -u 10001 -G gong gong \
    && install -d -o 10001 -g 10001 -m 0750 /etc/gong

COPY --from=build --chown=0:0 /out/gong /usr/local/bin/gong

USER 10001:10001
EXPOSE 8080
STOPSIGNAL SIGTERM

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD ["wget", "-q", "-O", "/dev/null", "http://127.0.0.1:8080/health"]

ENTRYPOINT ["/usr/local/bin/gong"]
CMD ["serve", "--config", "/etc/gong/gong.yaml"]
