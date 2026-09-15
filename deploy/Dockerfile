# The dashboard and the binary build in separate stages, so a change to one
# does not invalidate the other's layer cache.

FROM --platform=$BUILDPLATFORM node:22-alpine AS ui
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# BUILDPLATFORM keeps this stage on the machine's own architecture and lets Go
# cross-compile instead. Emulating the build for another architecture would be
# an order of magnitude slower for no benefit: the output is a static binary
# with no cgo.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /src/internal/webui/dist ./internal/webui/dist

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o /ponzproxy ./cmd/ponzproxy

FROM alpine:3.21
# ca-certificates is needed to reach the ACME directory over HTTPS, and tzdata
# so the dashboard's timestamps match the operator's timezone.
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -H -u 10001 ponzproxy \
 && mkdir -p /data && chown ponzproxy:ponzproxy /data

COPY --from=build /ponzproxy /usr/local/bin/ponzproxy

USER ponzproxy
ENV PONZ_DATA_DIR=/data
VOLUME ["/data"]
EXPOSE 80 443 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
    CMD wget -qO- http://127.0.0.1:8080/api/health >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/ponzproxy"]

LABEL org.opencontainers.image.source="https://github.com/teknik-github/PonzProxy" \
      org.opencontainers.image.description="Load balancer and reverse proxy with a live web console." \
      org.opencontainers.image.licenses="MIT"
