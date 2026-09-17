import type {
  AccessList,
  Certificate,
  Host,
  HostSnapshot,
  Snapshot,
  TrafficSnapshot,
  UpstreamSnapshot,
} from "@/api/types"

/** How the balancer is currently treating a backend.
 *
 *  The four live states carry the same names the upstream table uses, on
 *  purpose: an operator should not have to learn a second vocabulary to read
 *  the diagram. `unknown` is the fifth case the table never has to show —
 *  a host the live feed does not report at all, which is what a disabled host
 *  looks like from here. */
export type UpstreamState = "up" | "down" | "ejected" | "paused" | "unknown"

/** Where an upstream's share came from, in the order they are preferred.
 *
 *  `window` is recent traffic and is what the diagram wants: it follows a
 *  configuration change within seconds. `requests` is the cumulative total
 *  since the proxy started, used when the window is empty — an idle pool has
 *  no split happening right now, and its history is more informative than
 *  nothing. `weight` is the last resort on a proxy that has served nothing at
 *  all; drawing a measured split there would be a lie, so the UI says which
 *  one it is showing. */
export type ShareBasis = "window" | "requests" | "weight"

/** One word per state, shared by the diagram, its tooltips and the detail
 *  panel so the same backend is never called two different things. */
export const STATE_LABEL: Record<UpstreamState, string> = {
  up: "up",
  down: "down",
  ejected: "ejected",
  paused: "paused",
  unknown: "no data",
}

export interface UpstreamView {
  key: string
  address: string
  /** The path prefix this backend serves, or empty for the host's own
   *  upstreams. A host can have several pools now, and without this the
   *  diagram shows all their backends in one list with no way to tell which
   *  path any of them answers. */
  location: string
  state: UpstreamState
  /** Fraction of this host's traffic, 0..1. */
  share: number
  weight: number
  activeConns: number
  meanLatencyMs: number
  totalRequests: number
  /** Requests in the rolling window, which is what `share` is normally
   *  measured from. */
  windowRequests: number
  /** Plain sentence naming why this backend is not serving, or undefined when
   *  it is. This is what the diagram must surface on hover and on click. */
  problem: string | undefined
}

/** `warn` and `bad` are the only two states that earn colour. Everything else
 *  is on or off and stays greyscale. */
export type PolicyLevel = "off" | "on" | "warn" | "bad"

/** One link in the chain a request passes through before it reaches a
 *  backend. The letters spell TRAIL in the order the proxy applies them,
 *  which is the only reason five single letters are readable at this size. */
export interface PolicyStep {
  key: "tls" | "redirect" | "access" | "inspect" | "log"
  letter: string
  label: string
  level: PolicyLevel
  detail: string
}

export interface HostView {
  hostId: number
  name: string
  domains: string[]
  /** False for a host the operator has switched off, which still belongs on
   *  the diagram: a domain that has stopped resolving to anything is exactly
   *  what somebody comes to this screen to find. */
  enabled: boolean
  algorithm: string
  requestsPerSec: number
  meanLatencyMs: number
  errorRate: number
  activeConns: number
  basis: ShareBasis
  policy: PolicyStep[]
  upstreams: UpstreamView[]
  /** Set when the host exists in the config but the live feed does not report
   *  it, so every figure on the row is configuration rather than measurement. */
  live: boolean
}

/** Used only until the first snapshot arrives, and when talking to a server
 *  too old to report the window. It must stay in step with metrics.shareWindow
 *  on the Go side; every live render uses the server's own number instead. */
const DEFAULT_SHARE_WINDOW = 30

export interface TopologyModel {
  hosts: HostView[]
  totals: TrafficSnapshot
  /** Requests that matched no host. The server collects them under host 0;
   *  they never reach an upstream, so they hang off the proxy, not a host. */
  unmatchedRps: number
  upstreamsUp: number
  upstreamsTotal: number
  /** How many seconds of traffic the measured split covers, as reported by the
   *  server, so the wording on screen always matches the measurement. */
  shareWindowSeconds: number
  /** Everything on the diagram that is not serving, already phrased, and
   *  carrying the identifiers the page needs to select it. */
  problems: {
    hostId: number
    host: string
    key: string
    upstream: string
    reason: string
  }[]
}

const EMPTY_TRAFFIC: TrafficSnapshot = {
  requestsPerSec: 0,
  bytesInPerSec: 0,
  bytesOutPerSec: 0,
  meanLatencyMs: 0,
  errorRate: 0,
  activeConns: 0,
}

/** buildTopology folds the configuration and the latest live snapshot into the
 *  one shape the diagram draws from.
 *
 *  The configuration is the source of the host list and of everything in front
 *  of a host; the snapshot is the source of every number. Merging here rather
 *  than in the renderer keeps the SVG free of `?.` chains and means the two
 *  halves can disagree — a configured host with no live row, a live row for a
 *  host the console has not loaded yet — without the drawing breaking. */
export function buildTopology(
  snapshot: Snapshot | null,
  hosts: Host[],
  certificates: Certificate[],
  accessLists: AccessList[],
): TopologyModel {
  const live = new Map<number, HostSnapshot>()
  for (const h of snapshot?.hosts ?? []) live.set(h.hostId, h)

  const views: HostView[] = []
  for (const host of hosts) {
    views.push(
      hostView(host, live.get(host.id), certificates, accessLists),
    )
    live.delete(host.id)
  }

  // A live row with no configuration behind it means the console's host list
  // is stale (a reload is in flight, or another admin just added one). Drawing
  // it without its policy chain beats dropping traffic off the diagram.
  const unmatched = live.get(0)
  live.delete(0)
  for (const orphan of live.values()) views.push(orphanView(orphan))

  // Only a real failure belongs in the banner. A backend an operator paused
  // and one the feed has no data for both carry a `problem` sentence worth
  // reading on hover, but neither is an incident and neither should turn the
  // top of the screen red.
  const problems: TopologyModel["problems"] = []
  for (const v of views) {
    for (const u of v.upstreams) {
      if (u.problem && (u.state === "down" || u.state === "ejected")) {
        problems.push({
          hostId: v.hostId,
          host: v.name,
          key: u.key,
          upstream: u.address,
          reason: u.problem,
        })
      }
    }
  }

  return {
    hosts: views,
    totals: snapshot?.totals ?? EMPTY_TRAFFIC,
    unmatchedRps: unmatched?.traffic.requestsPerSec ?? 0,
    upstreamsUp: snapshot?.system.upstreamsUp ?? 0,
    upstreamsTotal: snapshot?.system.upstreamsTotal ?? 0,
    shareWindowSeconds: snapshot?.shareWindowSeconds || DEFAULT_SHARE_WINDOW,
    problems,
  }
}

function hostView(
  host: Host,
  snap: HostSnapshot | undefined,
  certificates: Certificate[],
  accessLists: AccessList[],
): HostView {
  const upstreams = snap?.upstreams ?? null
  const views = upstreams
    ? upstreams.map(liveUpstream)
    : host.upstreams.map((u) => ({
        key: `${u.scheme}://${u.address}`,
        address: `${u.scheme}://${u.address}`,
        state: (u.enabled ? "unknown" : "paused") as UpstreamState,
        share: 0,
        weight: u.weight,
        location: "",
        activeConns: 0,
        meanLatencyMs: 0,
        totalRequests: 0,
        windowRequests: 0,
        problem: u.enabled
          ? "No live data for this backend. The host is not in the feed."
          : "Switched off in the host's configuration.",
      }))

  const basis = shareOut(views)
  const traffic = snap?.traffic ?? EMPTY_TRAFFIC

  return {
    hostId: host.id,
    name: host.name,
    domains: host.domains,
    enabled: host.enabled,
    algorithm: host.algorithm.replace(/_/g, " "),
    requestsPerSec: traffic.requestsPerSec,
    meanLatencyMs: traffic.meanLatencyMs,
    errorRate: traffic.errorRate,
    activeConns: traffic.activeConns,
    basis,
    policy: policyFor(host, certificates, accessLists),
    upstreams: views,
    live: snap !== undefined,
  }
}

function orphanView(snap: HostSnapshot): HostView {
  const views = (snap.upstreams ?? []).map(liveUpstream)
  return {
    hostId: snap.hostId,
    name: snap.name,
    domains: [],
    enabled: true,
    algorithm: "",
    requestsPerSec: snap.traffic.requestsPerSec,
    meanLatencyMs: snap.traffic.meanLatencyMs,
    errorRate: snap.traffic.errorRate,
    activeConns: snap.traffic.activeConns,
    basis: shareOut(views),
    policy: [],
    upstreams: views,
    live: true,
  }
}

function liveUpstream(u: UpstreamSnapshot): UpstreamView {
  return {
    // The same address can serve two different paths, so the path is part of
    // what identifies a backend on this screen.
    key: (u.location ?? "") + " " + u.address,
    address: u.address,
    location: u.location ?? "",
    state: stateOf(u),
    share: 0,
    weight: u.weight,
    activeConns: u.activeConns,
    meanLatencyMs: u.meanLatencyMs,
    totalRequests: u.totalRequests,
    windowRequests: u.windowRequests ?? 0,
    problem: problemOf(u),
  }
}

function stateOf(u: UpstreamSnapshot): UpstreamState {
  if (!u.enabled) return "paused"
  if (u.ejected) return "ejected"
  return u.healthy ? "up" : "down"
}

/** The three ways a backend leaves rotation have different fixes, so they get
 *  different sentences rather than one "unhealthy". */
function problemOf(u: UpstreamSnapshot): string | undefined {
  if (!u.enabled) return "Switched off in the host's configuration."
  if (u.ejected) {
    const back =
      u.ejectedForSeconds && u.ejectedForSeconds > 0
        ? ` Back in rotation in ${u.ejectedForSeconds}s.`
        : " Returning to rotation."
    return `Ejected by passive health after repeated failures on real traffic.${back}${
      u.lastError ? ` Last error: ${u.lastError}` : ""
    }`
  }
  if (!u.healthy) {
    return `The health check is failing.${
      u.lastError ? ` Last error: ${u.lastError}` : ""
    }`
  }
  return undefined
}

/** shareOut fills in each upstream's share in place and reports what it was
 *  measured from, preferring the most recent evidence it has.
 *
 *  The window comes first because it is the only one that answers the question
 *  an operator asks of this screen: change a host from weighted round robin to
 *  round robin and the new split is drawn within seconds, where a cumulative
 *  average would keep drawing the old weighting for as long as the history
 *  outweighs the present. */
function shareOut(views: UpstreamView[]): ShareBasis {
  if (views.length === 0) return "window"

  const recent = views.reduce((sum, u) => sum + u.windowRequests, 0)
  if (recent > 0) {
    for (const u of views) u.share = u.windowRequests / recent
    return "window"
  }

  const served = views.reduce((sum, u) => sum + u.totalRequests, 0)
  if (served > 0) {
    for (const u of views) u.share = u.totalRequests / served
    return "requests"
  }

  const weights = views.reduce((sum, u) => sum + Math.max(u.weight, 0), 0)
  for (const u of views) {
    u.share = weights > 0 ? Math.max(u.weight, 0) / weights : 1 / views.length
  }
  return "weight"
}

function policyFor(
  host: Host,
  certificates: Certificate[],
  accessLists: AccessList[],
): PolicyStep[] {
  return [
    tlsStep(host, certificates),
    redirectStep(host),
    accessStep(host, accessLists),
    inspectStep(host),
    logStep(host),
  ]
}

function tlsStep(host: Host, certificates: Certificate[]): PolicyStep {
  const step = { key: "tls", letter: "T", label: "TLS" } as const
  if (host.certificateId === null) {
    return {
      ...step,
      level: "off",
      detail: "No certificate. This host is served over plain HTTP only.",
    }
  }
  const cert = certificates.find((c) => c.id === host.certificateId)
  if (!cert) {
    return {
      ...step,
      level: "bad",
      detail: `Certificate #${host.certificateId} is attached but the console cannot find it.`,
    }
  }
  if (!cert.installed) {
    return {
      ...step,
      level: "bad",
      detail: `${cert.name} is attached but not installed.${
        cert.lastError ? ` ${cert.lastError}` : ""
      }`,
    }
  }
  if (cert.expiresInDays < 0) {
    return {
      ...step,
      level: "bad",
      detail: `${cert.name} expired ${Math.abs(cert.expiresInDays)} days ago.`,
    }
  }
  if (cert.expiresInDays < 14) {
    return {
      ...step,
      level: "warn",
      detail: `${cert.name} expires in ${cert.expiresInDays} days (${cert.issuer || "unknown issuer"}).`,
    }
  }
  return {
    ...step,
    level: "on",
    detail: `${cert.name} — ${cert.issuer || "unknown issuer"}, expires in ${cert.expiresInDays} days.`,
  }
}

function redirectStep(host: Host): PolicyStep {
  const step = { key: "redirect", letter: "R", label: "HTTPS redirect" } as const
  if (!host.forceHttps) {
    return { ...step, level: "off", detail: "Plain HTTP is served as it arrives." }
  }
  // A redirect to a port with nothing listening for this domain is a loop the
  // operator will only find out about from a user, so it is called out here.
  if (host.certificateId === null) {
    return {
      ...step,
      level: "bad",
      detail:
        "HTTP is redirected to HTTPS but no certificate is attached, so the redirect lands on a handshake that cannot complete.",
    }
  }
  const hsts =
    host.hstsMaxAge > 0
      ? ` HSTS is set for ${Math.round(host.hstsMaxAge / 86400)} days.`
      : " HSTS is not set."
  return {
    ...step,
    level: "on",
    detail: `Plain HTTP is redirected to HTTPS.${hsts}`,
  }
}

function accessStep(host: Host, accessLists: AccessList[]): PolicyStep {
  const step = { key: "access", letter: "A", label: "Access list" } as const
  if (host.accessListId === null) {
    return {
      ...step,
      level: "off",
      detail: "No access list. Every client address reaches this host.",
    }
  }
  const list = accessLists.find((l) => l.id === host.accessListId)
  if (!list) {
    return {
      ...step,
      level: "bad",
      detail: `Access list #${host.accessListId} is attached but the console cannot find it.`,
    }
  }
  const rules = list.rules?.length ?? 0
  const users = list.basicAuth?.length ?? 0
  if (rules === 0 && users === 0) {
    return {
      ...step,
      level: "warn",
      detail: `${list.name} is attached but has no rules and no users, so it blocks nothing.`,
    }
  }
  return {
    ...step,
    level: "on",
    detail: `${list.name} — ${rules} address rule${rules === 1 ? "" : "s"}, ${users} user${
      users === 1 ? "" : "s"
    }, satisfy ${list.satisfyAny ? "any" : "all"}.`,
  }
}

function inspectStep(host: Host): PolicyStep {
  const step = { key: "inspect", letter: "I", label: "Request inspection" } as const
  const rules = host.guardian.rules?.length ?? 0
  if (host.guardian.mode === "off") {
    return { ...step, level: "off", detail: "Requests are not inspected." }
  }
  if (host.guardian.mode === "detect") {
    return {
      ...step,
      level: "warn",
      detail: `Detect only: ${rules} rule${
        rules === 1 ? "" : "s"
      } record a match and the request is still forwarded.`,
    }
  }
  return {
    ...step,
    level: "on",
    detail: `Blocking: ${rules} rule${rules === 1 ? "" : "s"}, URIs over ${
      host.guardian.maxUriLength
    } bytes rejected.`,
  }
}

function logStep(host: Host): PolicyStep {
  const step = { key: "log", letter: "L", label: "Access log" } as const
  if (!host.accessLog.enabled) {
    return {
      ...step,
      level: "off",
      detail: "Requests to this host are not logged.",
    }
  }
  return {
    ...step,
    level: "on",
    detail: `Every request is logged${
      host.accessLog.includeQuery ? ", including the query string" : ", path only"
    }.`,
  }
}
