import type { HostView, TopologyModel, UpstreamView } from "./model"

export interface Box {
  x: number
  y: number
  w: number
  h: number
}

export interface UpstreamNode {
  view: UpstreamView
  box: Box
  /** Path from the host's edge to this backend's edge. */
  link: string
  linkWidth: number
  /** Requests per second believed to be travelling this link, which sets how
   *  fast the flow dashes move. Zero means the link is drawn idle. */
  rps: number
}

export interface HostNode {
  view: HostView
  box: Box
  link: string
  linkWidth: number
  /** Stacked layout only: the vertical run under the host that every backend
   *  branches off. Null in the wide layout, where each backend gets its own
   *  curve straight from the host. */
  stem: string | null
  upstreams: UpstreamNode[]
}

export interface Layout {
  compact: boolean
  width: number
  height: number
  internet: Box
  proxy: Box
  trunk: string
  trunkWidth: number
  /** Stacked layout only: the vertical run below the proxy that every host
   *  branches off, carrying the whole of the proxy's traffic. */
  spine: string | null
  hosts: HostNode[]
}

/** Below this the four-column layout cannot hold a readable host name, so the
 *  diagram switches to a stacked tree. Picked from the sum of the wide
 *  layout's minimum column widths, not from a device breakpoint. */
export const WIDE_MIN = 700

/** Stroke width in px for a link carrying no traffic and for one carrying all
 *  of it. The span is deliberately wide: the whole point of drawing this
 *  instead of tabulating it is that 5:2:1 and an even three-way split are
 *  meant to be distinguishable without reading a number. */
const MIN_STROKE = 1.25
const MAX_STROKE = 9

const CORNER = 8

export function linkWidth(share: number): number {
  return MIN_STROKE + (MAX_STROKE - MIN_STROKE) * clamp(share, 0, 1)
}

/** layout places every node and link for one width.
 *
 *  Geometry depends only on how many hosts and backends there are, never on
 *  their numbers, so the paths stay byte-identical between snapshots and React
 *  leaves the DOM — and therefore any running flow animation — alone. Only the
 *  stroke widths change once a second. */
export function layout(model: TopologyModel, width: number): Layout {
  return width >= WIDE_MIN ? wide(model, width) : compact(model, width)
}

/* ------------------------------------------------------------------ wide -- */

const W = {
  pad: 8,
  internetW: 76,
  internetH: 46,
  trunkGap: 38,
  proxyW: 128,
  proxyH: 78,
  hostH: 84,
  upH: 42,
  upGap: 8,
  rowGap: 22,
}

function wide(model: TopologyModel, width: number): Layout {
  const upW = clamp(Math.round(width * 0.24), 168, 240)
  const hostW = clamp(Math.round(width * 0.25), 170, 260)
  const railEnd = W.pad + W.internetW + W.trunkGap + W.proxyW

  // Slack beyond the fixed columns goes into the two fan gaps, so a wide
  // window buys longer curves rather than stretched boxes full of air.
  const slack = Math.max(68, width - W.pad - railEnd - hostW - upW)
  const gapA = Math.max(34, Math.round(slack * 0.42))
  const hostX = railEnd + gapA
  const upX = Math.max(hostX + hostW + 34, width - W.pad - upW)

  const rows = model.hosts.map(rowHeightWide)
  const stack =
    rows.reduce((sum, h) => sum + h, 0) +
    W.rowGap * Math.max(0, rows.length - 1)
  const height = Math.max(
    W.pad * 2 + stack,
    W.pad * 2 + W.proxyH,
    // Keeps an empty diagram from collapsing to a sliver.
    140,
  )

  const railY = height / 2
  const internet: Box = {
    x: W.pad,
    y: railY - W.internetH / 2,
    w: W.internetW,
    h: W.internetH,
  }
  const proxy: Box = {
    x: W.pad + W.internetW + W.trunkGap,
    y: railY - W.proxyH / 2,
    w: W.proxyW,
    h: W.proxyH,
  }

  const shares = hostShares(model)
  const hosts: HostNode[] = []
  let y = W.pad + Math.max(0, (height - W.pad * 2 - stack) / 2)

  model.hosts.forEach((view, i) => {
    const rowH = rows[i] ?? W.hostH
    const box: Box = {
      x: hostX,
      y: y + (rowH - W.hostH) / 2,
      w: hostW,
      h: W.hostH,
    }
    const cy = box.y + box.h / 2
    const upsH = upsHeight(view.upstreams.length)
    const upTop = y + (rowH - upsH) / 2

    const upstreams = view.upstreams.map((u, j) => {
      const ubox: Box = {
        x: upX,
        y: upTop + j * (W.upH + W.upGap),
        w: upW,
        h: W.upH,
      }
      const ucy = ubox.y + ubox.h / 2
      return {
        view: u,
        box: ubox,
        link: curve(box.x + box.w, cy, ubox.x, ucy),
        linkWidth: u.state === "up" ? linkWidth(u.share) : MIN_STROKE,
        rps: u.state === "up" ? view.requestsPerSec * u.share : 0,
      }
    })

    hosts.push({
      view,
      box,
      link: curve(proxy.x + proxy.w, railY, box.x, cy),
      linkWidth: linkWidth(shares[i] ?? 0),
      stem: null,
      upstreams,
    })
    y += rowH + W.rowGap
  })

  return {
    compact: false,
    width,
    height,
    internet,
    proxy,
    trunk: `M ${internet.x + internet.w} ${railY} H ${proxy.x}`,
    trunkWidth: linkWidth(model.totals.requestsPerSec > 0 ? 1 : 0.35),
    spine: null,
    hosts,
  }
}

function rowHeightWide(view: HostView): number {
  return Math.max(W.hostH, upsHeight(view.upstreams.length))
}

function upsHeight(n: number): number {
  return n === 0 ? 0 : n * W.upH + (n - 1) * W.upGap
}

/* --------------------------------------------------------------- compact -- */

const C = {
  pad: 6,
  internetH: 34,
  trunkH: 18,
  proxyH: 62,
  railGap: 20,
  hostH: 86,
  upH: 44,
  upGap: 8,
  rowGap: 18,
  /** Left margin of the proxy spine, and of the per-host stem below it. The
   *  branch off each has to be long enough that its width is readable, which
   *  is what sets the two indents. */
  spineX: 16,
  hostX: 42,
  stemOffset: 12,
  upOffset: 34,
}

function compact(model: TopologyModel, width: number): Layout {
  const hostX = C.hostX
  const hostW = Math.max(120, width - C.pad - hostX)
  const stemX = hostX + C.stemOffset
  const upX = hostX + C.upOffset
  const upW = Math.max(100, width - C.pad - upX)

  const internet: Box = {
    x: C.pad,
    y: C.pad,
    w: Math.max(80, width - C.pad * 2),
    h: C.internetH,
  }
  const proxy: Box = {
    x: C.pad,
    y: internet.y + internet.h + C.trunkH,
    w: internet.w,
    h: C.proxyH,
  }
  const proxyBottom = proxy.y + proxy.h

  const shares = hostShares(model)
  const hosts: HostNode[] = []
  let y = proxyBottom + C.railGap
  let lastHostCy = proxyBottom

  model.hosts.forEach((view, i) => {
    const box: Box = { x: hostX, y, w: hostW, h: C.hostH }
    const cy = box.y + box.h / 2
    lastHostCy = cy
    const fanY = box.y + box.h
    let uy = fanY + 12
    let lastUpCy = fanY

    const upstreams = view.upstreams.map((u) => {
      const ubox: Box = { x: upX, y: uy, w: upW, h: C.upH }
      uy += C.upH + C.upGap
      const ucy = ubox.y + ubox.h / 2
      lastUpCy = ucy
      return {
        view: u,
        box: ubox,
        link: branch(stemX, ucy, ubox.x),
        linkWidth: u.state === "up" ? linkWidth(u.share) : MIN_STROKE,
        rps: u.state === "up" ? view.requestsPerSec * u.share : 0,
      }
    })

    hosts.push({
      view,
      box,
      link: branch(C.pad + C.spineX, cy, box.x),
      linkWidth: linkWidth(shares[i] ?? 0),
      // Drawn as one run rather than one per backend: overlapping strokes at
      // the same opacity stack, and a spine that darkens towards the top
      // would read as more traffic where there is only more overprinting.
      stem:
        upstreams.length === 0
          ? null
          : `M ${r(stemX)} ${r(fanY)} V ${r(lastUpCy)}`,
      upstreams,
    })

    y = (upstreams.length === 0 ? fanY : uy - C.upGap) + C.rowGap
  })

  return {
    compact: true,
    width,
    height: Math.max(y - C.rowGap + C.pad, proxyBottom + C.pad),
    internet,
    proxy,
    trunk: `M ${C.pad + C.spineX} ${internet.y + internet.h} V ${proxy.y}`,
    trunkWidth: linkWidth(model.totals.requestsPerSec > 0 ? 1 : 0.35),
    spine:
      hosts.length === 0
        ? null
        : `M ${r(C.pad + C.spineX)} ${r(proxyBottom)} V ${r(lastHostCy)}`,
    hosts,
  }
}

/* ----------------------------------------------------------------- paths -- */

function curve(x1: number, y1: number, x2: number, y2: number): string {
  const dx = Math.max(18, (x2 - x1) * 0.5)
  return `M ${r(x1)} ${r(y1)} C ${r(x1 + dx)} ${r(y1)}, ${r(x2 - dx)} ${r(y2)}, ${r(x2)} ${r(y2)}`
}

/** One tap off a vertical run: round the corner, then across. The stacked
 *  layout has no horizontal room for a curve that reads as a direction, and a
 *  bus with taps off it is how a rack diagram draws the same thing. */
function branch(x: number, y: number, x2: number): string {
  const c = Math.min(CORNER, Math.max(0, x2 - x))
  return `M ${r(x)} ${r(y - c)} Q ${r(x)} ${r(y)}, ${r(x + c)} ${r(y)} H ${r(x2)}`
}

/** Rounding to a tenth keeps the `d` strings stable across renders, so React
 *  skips the attribute write and the flow animation is never restarted. */
function r(n: number): number {
  return Math.round(n * 10) / 10
}

/* ----------------------------------------------------------------- share -- */

/** Each host's slice of the proxy's total throughput. With nothing flowing
 *  anywhere the links are drawn at an equal structural weight rather than all
 *  at the minimum, so the shape of the routing is still legible on a proxy
 *  that has just started. */
function hostShares(model: TopologyModel): number[] {
  const total = model.hosts.reduce((sum, h) => sum + h.requestsPerSec, 0)
  if (total <= 0) {
    const even = model.hosts.length > 0 ? 1 / model.hosts.length : 0
    return model.hosts.map(() => Math.min(even, 0.5))
  }
  return model.hosts.map((h) => h.requestsPerSec / total)
}

export function clamp(n: number, lo: number, hi: number): number {
  return n < lo ? lo : n > hi ? hi : n
}
