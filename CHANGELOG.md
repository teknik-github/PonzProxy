# Changelog

Notable changes to ponzproxy, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[semantic versioning](https://semver.org/): the **major** number changes when
an upgrade needs action from you, the **minor** number when something is added,
and the **patch** number when something is only fixed.

Every released version has a matching container image and a git tag, so
`ghcr.io/teknik-github/ponzproxy:0.2.1` is exactly the code at `v0.2.1`.

## [Unreleased]

### Added

- **Backups** — a snapshot of everything the proxy cannot rebuild: the
  database, every certificate, the ACME account key and the session secret.
  Written every 24 hours into `<data>/backups` keeping the newest seven, on
  demand from the console, and from the command line with `--backup` for a
  cron job that copies the result off the machine. `--restore` puts one back.

  The database is copied with SQLite's `VACUUM INTO` rather than read from
  disk: in WAL mode committed data is split between two files, so a plain copy
  of a running database can be torn. A restore never deletes — the existing
  data directory is renamed and left behind, because a restore happens in
  exactly the circumstances where a second mistake is most likely.

  The console says plainly what a local snapshot is worth. It survives a
  deleted host and a corrupted database; it does not survive the disk it sits
  on. A scheduled backup someone mistakes for off-site protection is worse
  than none, because it buys false confidence.
- **Maintenance mode** — a per-host switch that answers with a page instead of
  proxying. Better than stopping the backend, which tells visitors the site is
  broken rather than being worked on and is indistinguishable from a real
  outage in your own monitoring. 503 by default, with `Retry-After`, and it is
  not counted as a failure — planned maintenance that drives the error rate up
  sets off exactly the alerting the window was booked to avoid. Addresses can
  be allowed through, so whoever is doing the work can check that it worked.
- **Custom error pages** — per-host wording for 502 and 503, kept separate from
  maintenance because saying "planned work" during an unplanned outage is a lie
  a customer remembers. Both pages are self-contained: no external stylesheet,
  no image, no script, because they are served exactly when nothing else works.
  Operator text is escaped, not rendered as markup, since the page is served
  from the operator's own domain.
- **Traffic limits** — a per-host bound on what one client address may ask for:
  a sustained request rate with burst headroom, a cap on requests in flight,
  and a maximum request body. Off, detect and block, the same three modes as
  request inspection and for the same reason — a false positive on a proxy is
  a visible outage, so an operator can watch what *would* be refused before
  refusing anything. Addresses or ranges can be exempted, because switching
  limits on otherwise takes out your own monitoring first: it is the most
  regular traffic a host receives.

  This is **not** DDoS protection and is not described as such anywhere in the
  console. A volumetric attack saturates the uplink before a packet reaches
  this process; only upstream scrubbing helps there. What this covers is one
  client, or a script, asking for more than the backends can serve.

  It runs before the access list so a client inventing credentials cannot buy
  a bcrypt comparison per request, and costs 361ns and one allocation for a
  host that uses it, nothing at all for a host that does not.
- **Traffic used** — how much each host carried over the last day, week or
  month, with in, out, total and share. Read from the samples that already
  back the historical charts, so it stops where retention does — and says so
  rather than returning a small number someone might compare against a bill.
- **Excel and PDF reports** of that page, covering the selected period. The
  PDF opens with a bar chart of traffic by host. Both are written against the
  standard library — an .xlsx is a zip of XML, and a PDF of a table in a
  standard font is a few kilobytes of text — so the project still has four
  direct dependencies. The spreadsheet stores figures as numbers rather than
  as text that merely looks like one, so its columns can be summed.
- **A sidebar with groups.** Twelve rows in one list had outgrown being
  scannable; they are now under Monitor, Routing, Protection and Settings,
  each foldable and remembered. A group is never folded while the screen you
  are on is inside it, and a problem inside a folded group still shows.
- **`install.sh`** — one command sets up a machine from nothing, installing
  Docker Engine first when it is missing. Re-running it upgrades in place and
  keeps the existing `.env` and data volume. `--dry-run` prints every command
  it would run and changes nothing, which is the least a script asking to be
  piped into a shell can offer.

### Fixed

- `--restore` created the data directory it was about to replace, because
  loading the configuration writes a session secret. It then reported moving
  aside a directory it had manufactured seconds earlier. Configuration is now
  read without side effects for that command.
- A health-check test could fail under load, because it counted probes on the
  server side and a request already on the wire can be counted after the probe
  loop has exited. It now measures what it meant to: that no *new* probe starts
  after a host is removed.

### Changed

- The compose file no longer pins `container_name`, so two installations on one
  machine are two compose projects rather than a name collision. Every
  documented command addresses the service, which has not changed; `docker logs
  ponzproxy` becomes `docker compose logs`.

## [0.2.1] - 2026-09-16

### Fixed

- `:latest` now follows the newest release rather than the newest commit. A
  push to `main` and the version tag on that same commit are two separate CI
  runs with different version stamps, so both moving `latest` left it with a
  different digest from the release it was meant to be, reporting a commit sha
  where the release number should have been. Pushes to `main` still publish
  `:main` and `:sha-xxxxxxx`.

## [0.2.0] - 2026-09-16

### Upgrading

`docker-compose.yml` moved from `deploy/` to the repository root, so
`docker compose -f deploy/docker-compose.yml …` no longer resolves. Drop the
`-f` and run it from the repository root, or — now that the file is
self-contained — fetch it on its own and stop cloning the repository to
install:

```sh
curl -O https://raw.githubusercontent.com/teknik-github/PonzProxy/main/docker-compose.yml
curl -O https://raw.githubusercontent.com/teknik-github/PonzProxy/main/.env.example
cp .env.example .env
docker compose up -d
```

Nothing else needs action. The database migrates itself, and settings that
were environment variables still are.

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

[Unreleased]: https://github.com/teknik-github/PonzProxy/compare/v0.2.1...HEAD
[0.2.1]: https://github.com/teknik-github/PonzProxy/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/teknik-github/PonzProxy/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/teknik-github/PonzProxy/releases/tag/v0.1.0
