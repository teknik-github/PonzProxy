# ponzproxy — Architecture

A load balancer / reverse proxy with a live web UI. Go data plane, React control
plane, one binary.

## Dependency rule

Packages are organised **per capability**, not per layer. There is exactly one
rule about imports, and it is enforced by review:

```
domain  <-  everything            domain imports nothing from this module
store, balancer, health, certmgr, metrics, proxy, api  ->  domain
cmd/ponzproxy  ->  all of the above          (composition root, wiring only)
```

Nothing outside `cmd/` constructs its own dependencies. Every package takes what
it needs through an interface declared in `domain` (for persistence) or in the
consuming package itself (for narrow collaborators).

## Layout

```
cmd/ponzproxy/          composition root: parse env, open store, wire, serve
internal/
  domain/               core types, invariants, repository interfaces. No deps.
  store/                SQLite implementation of the domain repositories
    migrations/         embedded .sql files, applied in order at boot
  balancer/             the four selection algorithms + the live backend pool
  health/               active health probing, flips pool members up/down
  proxy/                DATA PLANE: dynamic router, reverse proxy, access log
  certmgr/              TLS: certificate cache, ACME, self-signed, manual import
    dnsprovider/        DNS-01 solvers (Cloudflare, ...) behind one interface
  metrics/              in-memory counters, periodic rollup into the store
  api/                  CONTROL PLANE: REST handlers, auth, validation
    ws/                 WebSocket hub broadcasting live stats to the UI
  platform/
    config/             process settings from the environment
    logging/            slog setup shared by every package
  webui/                go:embed of the built React bundle
web/                    React + Vite source for the control plane UI
deploy/                 Dockerfile, systemd unit, compose file
```

## Two planes, two listeners

The **data plane** (`proxy`) owns `:80` and `:443` and serves end-user traffic.
The **control plane** (`api`) owns `:8080` and serves the REST API, the
WebSocket feed and the embedded UI. They share the store and the metrics
collector, and nothing else — a panic or reload in the control plane must never
interrupt proxied traffic.

Configuration changes are applied by publishing a new immutable routing snapshot
into the proxy through an `atomic.Pointer`, so in-flight requests keep using the
snapshot they started with and no request path ever takes a lock.
