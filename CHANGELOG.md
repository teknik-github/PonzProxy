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

- **Install with one file** — `docker-compose.yml` moved to the repository
  root and is now self-contained, so installing needs no clone: fetch that one
  file and `docker compose up -d`. It also declares `host.docker.internal`, so
  a backend running on the host rather than in a container is reachable on
  Linux. Every setting is read from a `.env` beside it, documented in the new
  `.env.example`, so an upgrade can replace the compose file without losing
  local configuration.
- **Rolling share window** — the request-path diagram draws each backend's
  line weight from the last 30 seconds of traffic instead of the cumulative
  total since the proxy started. Switching a host from weighted round robin to
  round robin took effect on the very next request but kept drawing the old
  weighting for many minutes, because a cumulative average is dragged by
  however much history sits behind it; the one screen that should answer "did
  my change take?" was the slowest thing in the system to admit it had. The
  counts are sampled once a second from totals the balancer already keeps, so
  the request path is untouched. Measured: 62.5/25/12.5 reaches 33/33/33 in
  exactly 30 seconds, while the cumulative figure had moved 6 points in 40.
- **`--reset-password <user>`** — recovers an account whose password is lost.
  The console could change a password but never recover one, so forgetting the
  last administrator's password meant editing SQLite by hand. It prints a
  generated password on stdout and can be run against a live server; the new
  password works on the next sign-in without a restart.
- **Request path** — a live diagram of internet → proxy → hosts → upstreams,
  drawn from the existing WebSocket feed. Line weight is each backend's share
  of the traffic, so a 5:2:1 weighting and an even split look different without
  reading a number; a link turns green and its dashes travel while it is
  carrying requests, and red and dashed when the backend is out of rotation, so
  "up but idle" and "up and busy" are not drawn the same. What sits in front
  of each host — TLS, redirect, access list, inspection, log — is a row of five
  squares in the order the proxy applies them.
- **Cache assets** — per-host in-memory cache for static paths, with an LRU
  budget and a per-object limit. The origin always wins: `no-store`,
  `no-cache`, `private` and a past `Expires` all refuse a response whatever the
  host setting says, and a response with no `Content-Length` is never stored
  because there is then no way to tell a finished body from a truncated one.
  Conditional requests are answered locally with 304. A host that does not use
  it pays 70ns and no allocations.
- **Block exploits** — per-host request inspection for path traversal, probes
  for sensitive files, control characters, self-identifying scanners, and SQL
  or shell injection. Three modes, and **detect** is the point of the design:
  on a proxy a false positive is a visible outage while a probe getting through
  usually is not, so an operator can watch what would be blocked before
  enforcing anything. Measured at 3µs per request; a request refused on length
  costs 22ns.
- **Alerts** — webhook channels told when an upstream drops out, is ejected by
  passive health, comes back, when a host has nothing left to serve it, or when
  a certificate is expiring or failed to renew. Repeats about the same subject
  are suppressed, because an alert stream nobody can read is worse than none.
  The webhook URL is stored but never returned by the API: for a chat webhook
  the URL is itself the credential.
- **Access log** — a searchable record of requests, switched on per host. The
  writer batches in the background and drops entries rather than slowing the
  proxy down, and says how many it dropped so an incomplete history never looks
  whole. Query strings are omitted unless a host opts in.
- **Users** — add, remove and re-role operator accounts, and reset a forgotten
  password. Refuses any change that would leave the installation with no
  administrator.
- Published container images for `linux/amd64` and `linux/arm64`, built by CI.

### Fixed

- A missing repository in the API's options used to surface as a nil
  dereference on whichever request happened to need it. They are checked at
  construction now, so a wiring mistake stops the process at boot instead.
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
