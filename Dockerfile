# syntax=docker/dockerfile:1

ARG GO_VERSION=1.25.12
ARG BUILDPLATFORM=linux/amd64

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build

ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath \
    -ldflags "-s -w -X github.com/mcpshim/mcpshim/internal/version.Version=${VERSION}" \
    -o /out/mcpshim ./cmd/mcpshim && \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath \
    -ldflags "-s -w -X github.com/mcpshim/mcpshim/internal/version.Version=${VERSION}" \
    -o /out/mcpshimd ./cmd/mcpshimd

FROM alpine:3.22

RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S -g 10001 mcpshim && \
    adduser -S -D -H -u 10001 -G mcpshim mcpshim && \
    mkdir -p \
        /home/mcpshim/.config/mcpshim \
        /home/mcpshim/.local/share/mcpshim \
        /home/mcpshim/.run && \
    chown -R mcpshim:mcpshim /home/mcpshim

COPY --from=build /out/mcpshim /usr/local/bin/mcpshim
COPY --from=build /out/mcpshimd /usr/local/bin/mcpshimd
COPY --chown=mcpshim:mcpshim configs/mcpshim.docker.yaml \
    /home/mcpshim/.config/mcpshim/config.yaml
COPY configs/mcpshim.example.yaml \
    /usr/local/share/mcpshim/mcpshim.example.yaml
COPY configs/http-services.example.yaml \
    /usr/local/share/mcpshim/http-services.example.yaml
COPY docs/http-services.md \
    /usr/local/share/mcpshim/docs/http-services.md

ENV HOME=/home/mcpshim \
    XDG_CONFIG_HOME=/home/mcpshim/.config \
    XDG_DATA_HOME=/home/mcpshim/.local/share \
    XDG_RUNTIME_DIR=/home/mcpshim/.run \
    MCPSHIM_CONFIG=/home/mcpshim/.config/mcpshim/config.yaml

USER mcpshim
WORKDIR /home/mcpshim

VOLUME ["/home/mcpshim/.config/mcpshim", "/home/mcpshim/.local/share/mcpshim"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD mcpshim --json status >/dev/null || exit 1

ENTRYPOINT ["mcpshimd"]
