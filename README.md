# ponzproxy

A load balancer and reverse proxy with a live web console. One binary: the Go
data plane, the SQLite configuration store and the React dashboard ship
together, with no external database and no asset directory to keep in sync.

## What it does

- **Four balancing algorithms** — round robin, weighted round robin, least
  connections, and IP hash for sticky sessions without cookies.
- **Active health checks** that take a failing backend out of rotation before a
  client sees the failure, and put it back when it recovers.
- **TLS** from Let's Encrypt (HTTP-01, TLS-ALPN-01 or DNS-01 for wildcards),
  from a certificate you upload, or self-signed for internal hostnames.
  ACME certificates renew themselves.
- **A live console** over WebSocket: request rates, response times, error
  rates, and a diagram showing which backend is actually taking the load.

## Quick start

```sh
make            # builds the dashboard and the binary into bin/ponzproxy
./bin/ponzproxy
```

On first start it creates an `admin` account and prints a generated password
once. Open the console on <http://localhost:8080> and change it.

With Docker — no build, and no clone. One file is the whole installation; the
image is published for `linux/amd64` and `linux/arm64`:

```sh
curl -O https://raw.githubusercontent.com/teknik-github/PonzProxy/main/docker-compose.yml
docker compose up -d
docker compose logs | grep -A3 "first account"
```

That prints the generated admin password once. The console is then on
<http://localhost:8080>, bound to localhost only.

Settings go in a `.env` next to the compose file rather than in the file
itself, so an upgrade can replace the compose file without losing them:

```sh
PONZ_VERSION=0.1.0        # pin the image; defaults to latest
PONZ_HTTP_PORT=8081       # when 80, 443 or 8080 are already taken
PONZ_HTTPS_PORT=8444
PONZ_CONSOLE_PORT=9090
PONZ_ACME_EMAIL=you@example.com
```

Backends running on the host itself — not in a container — are reached as
`host.docker.internal:<port>`, and must be bound to more than `127.0.0.1` for
the container to connect. Backends in other containers are reached by their
service name.

Or without compose:

```sh
docker run -d --name ponzproxy \
  -p 80:80 -p 443:443 -p 127.0.0.1:8080:8080 \
  -v ponzproxy-data:/data \
  --cap-add NET_BIND_SERVICE \
  ghcr.io/teknik-github/ponzproxy:latest
```

Pin a version rather than `latest` for anything you care about — `PONZ_VERSION`
above, or `ghcr.io/teknik-github/ponzproxy:0.1.0` directly. Every release tag
has a matching image, and [CHANGELOG.md](CHANGELOG.md) says what changed in
each.

### If you lose the admin password

The console can change a password but not recover one. Run this on the machine
holding the data directory:

```sh
./bin/ponzproxy --reset-password admin
# with Docker:
docker compose exec ponzproxy ponzproxy --reset-password admin
```

It prints a freshly generated password on stdout — nothing else, so
`--reset-password admin | your-password-manager` works — and can be run while
the server is up: the new password takes effect on the next sign-in with no
restart. Sessions already signed in stay valid until they expire.

The password is generated rather than passed as an argument, because an
argument would land in your shell history and in every `ps` listing on the box.
The safeguard is that you need shell access to the data directory, which is
already enough to edit the database by hand.

## Ports

| Port   | Serves                                                  |
| ------ | ------------------------------------------------------- |
| `80`   | proxied HTTP traffic, and ACME HTTP-01 challenges        |
| `443`  | proxied HTTPS traffic, and ACME TLS-ALPN-01 challenges   |
| `8080` | the console, the REST API and the WebSocket feed         |

The console is a separate listener from the traffic it manages, so a reload or
a crash in the control plane cannot interrupt proxied requests. **Do not expose
port 8080 to the internet** — put it behind a VPN or an SSH tunnel.

## Configuration

Everything that changes at runtime — hosts, upstreams, certificates — lives in
the console. The environment only covers what must be known before boot.

| Variable                    | Default                  | Notes                                              |
| --------------------------- | ------------------------ | -------------------------------------------------- |
| `PONZ_DATA_DIR`             | `./data`                 | SQLite database, certificates, ACME account key     |
| `PONZ_HTTP_ADDR`            | `:80`                    | proxy HTTP listener                                 |
| `PONZ_HTTPS_ADDR`           | `:443`                   | proxy HTTPS listener                                |
| `PONZ_ADMIN_ADDR`           | `:8080`                  | console and API                                     |
| `PONZ_ACME_EMAIL`           | —                        | receives expiry warnings from the CA                |
| `PONZ_ACME_DIRECTORY`       | Let's Encrypt production | point at staging while testing                      |
| `PONZ_METRICS_FLUSH`        | `10s`                    | how often counters become a stored sample           |
| `PONZ_METRICS_RETENTION`    | `720h`                   | how long history is kept                            |
| `PONZ_SESSION_TTL`          | `24h`                    | console session lifetime                            |
| `PONZ_TRUSTED_PROXY_HEADER` | —                        | see below                                           |
| `PONZ_LOG_LEVEL`            | `info`                   | `debug` also logs every proxied request             |
| `PONZ_LOG_FORMAT`           | `text`                   | or `json`                                           |

### Running behind another proxy

`PONZ_TRUSTED_PROXY_HEADER` makes ponzproxy read the client address from a
header such as `X-Forwarded-For` instead of the connection. **Only set it when
ponzproxy itself sits behind a proxy you control.** Any client can send that
header, so trusting it when ponzproxy is directly exposed would let a caller
pick its own backend under IP hash and put whatever it likes in your logs.

### Let's Encrypt

Issuance is rate limited per domain, and a failed experiment can lock a domain
out for a week. Point `PONZ_ACME_DIRECTORY` at the staging endpoint while you
get routing working:

```
PONZ_ACME_DIRECTORY=https://acme-staging-v02.api.letsencrypt.org/directory
```

Wildcard certificates require the DNS-01 challenge, which needs API
credentials for your DNS provider. Cloudflare ships in the box; adding another
means implementing two methods — see `internal/certmgr/dnsprovider`.

## Development

```sh
make test     # every test, with the race detector
make check    # vet, gofmt, TypeScript, tests
make run      # backend on :8080 with a ./data directory
make dev      # Vite dev server, proxying /api to the running backend
```

The architecture, and the one rule about which package may import which, is in
[ARCHITECTURE.md](ARCHITECTURE.md).

## How it behaves under failure

Worth knowing before it happens in production:

- A request that cannot reach its backend is retried against another one, but
  **only when the request has no body**. A server handler cannot rewind a body,
  so replaying a POST would forward a truncated copy; those fail with 502
  instead.
- A host with no healthy backend returns 503. A host whose backends were all
  tried and refused the connection returns 502. The two are distinguished so
  the console can tell you which happened.
- Error pages never include the upstream address or the upstream's own
  response headers.
- Editing a host does not reset the health state of upstreams it still
  contains, so an unrelated change does not re-probe a working backend or
  briefly send traffic to a dead one.
