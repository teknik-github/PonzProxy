# ponzproxy — build targets.
#
# The dashboard is compiled into the binary, so `make build` depends on the
# frontend bundle. Run `make` for the whole thing.

BINARY      := bin/ponzproxy
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X main.version=$(VERSION)
UI_DIST     := internal/webui/dist
UI_SOURCES  := $(shell find web/src web/index.html web/package.json -type f 2>/dev/null)

.PHONY: all
all: build

## build: compile the binary with the dashboard embedded
.PHONY: build
build: ui
	@mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/ponzproxy
	@echo "built $(BINARY) ($(VERSION))"

## ui: build the React dashboard into the embed directory
.PHONY: ui
ui: $(UI_DIST)/index.html

$(UI_DIST)/index.html: $(UI_SOURCES) web/package-lock.json
	cd web && npm ci --silent && npm run build

## run: build and start with a local data directory
.PHONY: run
run: build
	PONZ_DATA_DIR=./data \
	PONZ_HTTP_ADDR=:8081 \
	PONZ_HTTPS_ADDR=:8444 \
	PONZ_ADMIN_ADDR=:8080 \
	PONZ_LOG_LEVEL=debug \
	$(BINARY)

## dev: run the Vite dev server, which proxies the API to a running backend
.PHONY: dev
dev:
	@echo "start the backend with 'make run' in another terminal first"
	cd web && npm run dev

## test: run every test with the race detector
.PHONY: test
test:
	go test -race ./...

## cover: run tests and report per-package coverage
.PHONY: cover
cover:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -20

## check: everything CI runs
.PHONY: check
check: vet lint-ui test

.PHONY: vet
vet:
	go vet ./...
	@test -z "$$(gofmt -l cmd internal)" || (gofmt -l cmd internal; echo "run: gofmt -w cmd internal"; exit 1)

.PHONY: lint-ui
lint-ui:
	cd web && npx tsc -b --noEmit

## docker: build the container image
.PHONY: docker
docker:
	docker build -f deploy/Dockerfile -t ponzproxy:$(VERSION) .

## clean: remove build output
.PHONY: clean
clean:
	rm -rf bin coverage.out
	find $(UI_DIST) -mindepth 1 ! -name .gitkeep -delete

## help: list targets
.PHONY: help
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'
