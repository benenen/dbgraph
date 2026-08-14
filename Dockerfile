# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e

FROM golang:1.26.5-alpine3.24@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS build

WORKDIR /src
RUN apk add --no-cache nodejs=24.18.1-r0 npm=11.12.1-r0
COPY web/package.json web/package-lock.json ./web/
RUN --mount=type=cache,target=/root/.npm \
    cd web && npm ci
COPY web ./web
RUN mkdir -p internal/transport/webapi/app \
    && cd web \
    && npm run build

ARG TARGETOS
ARG TARGETARCH
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=secret,id=go_proxy,required=false \
    go_proxy="$(cat /run/secrets/go_proxy 2>/dev/null || printf '%s' 'https://proxy.golang.org,direct')" \
    && GOPROXY="$go_proxy" go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -ldflags="-s -w" -o /out/dbgraph ./cmd/dbgraph

FROM alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b

RUN apk add --no-cache ca-certificates openssl tzdata \
    && addgroup -S -g 65532 dbgraph \
    && adduser -S -D -H -u 65532 -G dbgraph dbgraph \
    && install -d -o dbgraph -g dbgraph -m 0700 /var/lib/dbgraph

COPY --from=build --chown=65532:65532 /out/dbgraph /usr/local/bin/dbgraph
COPY --chown=65532:65532 --chmod=0755 docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

ENV DBGRAPH_DATABASE_PATH=/var/lib/dbgraph/dbgraph.sqlite \
    DBGRAPH_LISTEN_ADDRESS=0.0.0.0:8443 \
    DBGRAPH_TLS_CERT_FILE=/var/lib/dbgraph/tls/cert.pem \
    DBGRAPH_TLS_KEY_FILE=/var/lib/dbgraph/tls/key.pem

VOLUME ["/var/lib/dbgraph"]
EXPOSE 8443
USER 65532:65532

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["serve"]
