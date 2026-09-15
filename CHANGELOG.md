# Changelog

Notable changes to ponzproxy, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[semantic versioning](https://semver.org/): the **major** number changes when
an upgrade needs action from you, the **minor** number when something is added,
and the **patch** number when something is only fixed.

Every released version has a matching container image and a git tag, so
`ghcr.io/teknik-github/ponzproxy:0.1.0` is exactly the code at `v0.1.0`.

## [Unreleased]

### Added

- **Access log** — a searchable record of requests, switched on per host. The
  writer batches in the background and drops entries rather than slowing the
  proxy down, and says how many it dropped so an incomplete history never looks
  whole. Query strings are omitted unless a host opts in.
- **Users** — add, remove and re-role operator accounts, and reset a forgotten
  password. Refuses any change that would leave the installation with no
  administrator.
- Published container images for `linux/amd64` and `linux/arm64`, built by CI.

### Fixed

- A clean shutdown no longer logs "context canceled" at error level. Background
  loops all take the process context, so every one of them reported a fault on
  the way down.

## [0.1.0] - 2026-09-15

First public release.

### Added

- **Load balancing** across four algorithms: round robin, weighted round robin,
  least connections, and IP hash. IP hash uses weighted rendezvous hashing, so
  losing one backend moves only the clients pinned to it rather than
  reshuffling everyone mid-deploy.
- **Active health checks** with separate thresholds for taking a backend out
  and putting it back, so one blip does not eject it and one lucky response
  does not restore a flapping one.
- **Passive health** — a backend that keeps refusing real connections is
  ejected without waiting for the next probe, and it works when active probing
  is switched off entirely. Only connection failures count: a backend answering
  500 is alive.
- **TLS** from Let's Encrypt over HTTP-01, TLS-ALPN-01 or DNS-01 (wildcards),
  from an uploaded certificate, or self-signed for internal hostnames. ACME
  certificates renew themselves.
- **Access lists** — allow and deny by client address, and HTTP basic auth in
  front of a host. Evaluated before an upstream is chosen, so a refused request
  never reaches your servers.
- **Redirects** — answer a domain with 301, 302, 307 or 308 instead of proxying
  it. Redirects and hosts share one domain namespace, enforced in the schema.
- **Live console** over WebSocket: request rates, response times, error rates,
  per-upstream share of traffic, and historical charts.
- **Login rate limiting** — five failures from an address, then a pause.

[Unreleased]: https://github.com/teknik-github/PonzProxy/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/teknik-github/PonzProxy/releases/tag/v0.1.0
