# The dashboard and the binary build in separate stages, so a change to one
# does not invalidate the other's layer cache.

FROM node:22-alpine AS ui
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.27-alpine AS build
WORKDIR /src
# Dependencies are fetched before the source is copied, so editing code does
# not re-download the module cache.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /src/internal/webui/dist ./internal/webui/dist
ARG VERSION=dev
# CGO is off because the SQLite driver is pure Go, so this produces a static
# binary with no runtime library dependencies.
RUN CGO_ENABLED=0 go build -trimpath \
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
