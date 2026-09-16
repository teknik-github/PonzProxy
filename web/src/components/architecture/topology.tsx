import { useMemo } from "react"

import { bytesPerSec, count, millis, percent, rate } from "@/format"
import { cn } from "@/lib/utils"
import type { Box, HostNode, Layout, UpstreamNode } from "./layout"
import { layout } from "./layout"
import { STATE_LABEL, type PolicyStep, type TopologyModel } from "./model"

/** What the operator has clicked. Kept as identifiers rather than object
 *  references so a selection survives the next snapshot, which replaces every
 *  view object once a second. */
export type Selection =
  | { kind: "host"; hostId: number }
  | { kind: "upstream"; hostId: number; key: string }

interface Props {
  model: TopologyModel
  /** Measured container width in CSS pixels. Zero before the first measure,
   *  which renders nothing rather than guessing and reflowing. */
  width: number
  selection: Selection | null
  onSelect: (selection: Selection | null) => void
}

/** Flow dashes are declared here rather than in the stylesheet so this
 *  component carries its own animation and the shared tokens file stays the
 *  place for tokens.
 *
 *  Reduced motion is honoured in CSS instead of JavaScript so it follows the
 *  operating system setting live. With the animation off the dashes remain,
 *  which still reads as a pipe carrying something. */
const FLOW_CSS = `
@keyframes ppx-flow { from { stroke-dashoffset: 0 } to { stroke-dashoffset: -28 } }
.ppx-flow {
  stroke-dasharray: 4 10;
  animation-name: ppx-flow;
  animation-timing-function: linear;
  animation-iteration-count: infinite;
}
@media (prefers-reduced-motion: reduce) { .ppx-flow { animation: none } }
`

/** Topology draws the request path: internet, the proxy, each host with the
 *  chain of things in front of it, and the backends it balances across.
 *
 *  It is hand-written SVG because every mark has to mean something specific —
 *  stroke width is share of traffic, a dashed red link is a backend out of
 *  rotation — and a charting library would have to be argued out of its own
 *  opinions about all of it. */
export function Topology({ model, width, selection, onSelect }: Props) {
  const geometry = useMemo(() => layout(model, width), [model, width])
  if (width === 0) return null

  return (
    <svg
      width={geometry.width}
      height={geometry.height}
      viewBox={`0 0 ${geometry.width} ${geometry.height}`}
      role="group"
      aria-label="Request path from the internet through the proxy to each backend"
      className="max-w-full overflow-visible"
    >
      <defs>
        <style>{FLOW_CSS}</style>
      </defs>

      <Link
        d={geometry.trunk}
        width={geometry.trunkWidth}
        rps={model.totals.requestsPerSec}
      />
      {geometry.spine && (
        <Link
          d={geometry.spine}
          width={geometry.trunkWidth}
          rps={model.totals.requestsPerSec}
        />
      )}
      <InternetNode box={geometry.internet} model={model} layout={geometry} />
      <ProxyNode box={geometry.proxy} model={model} layout={geometry} />

      {geometry.hosts.map((host) => (
        <g key={host.view.hostId}>
          <Link
            d={host.link}
            width={host.linkWidth}
            rps={host.view.requestsPerSec}
            muted={!host.view.enabled}
          />
          {host.stem && (
            <Link
              d={host.stem}
              width={host.linkWidth}
              rps={host.view.requestsPerSec}
              muted={!host.view.enabled}
            />
          )}
          {host.upstreams.map((up) => (
            <Link
              key={up.view.key}
              d={up.link}
              width={up.linkWidth}
              rps={up.rps}
              bad={up.view.state === "down" || up.view.state === "ejected"}
              muted={up.view.state === "paused" || up.view.state === "unknown"}
            />
          ))}
          <HostBox
            node={host}
            selected={
              selection?.kind === "host" &&
              selection.hostId === host.view.hostId
            }
            onSelect={onSelect}
          />
          {host.upstreams.map((up) => (
            <UpstreamBox
              key={up.view.key}
              node={up}
              hostId={host.view.hostId}
              basisIsWeight={host.view.basis === "weight"}
              selected={
                selection?.kind === "upstream" &&
                selection.hostId === host.view.hostId &&
                selection.key === up.view.key
              }
              onSelect={onSelect}
            />
          ))}
        </g>
      ))}
    </svg>
  )
}

/* ----------------------------------------------------------------- links -- */

/** Link draws one connection twice: a wide backing stroke whose width is the
 *  share of traffic, and a thin dashed stroke on top that moves at a speed set
 *  by the request rate. Width answers "how much of the load", colour and motion
 *  answer "is anything happening right now" — and they are genuinely different
 *  questions on an idle proxy with a lopsided weighting. */
function Link({
  d,
  width,
  rps,
  bad = false,
  muted = false,
}: {
  d: string
  width: number
  rps: number
  bad?: boolean
  muted?: boolean
}) {
  const duration = bad || muted ? null : flowDuration(rps)
  // A non-null duration is exactly the "carrying traffic right now" state, so
  // it decides the colour as well as the motion: green for flowing, red for
  // down or ejected, grey for a link that is up but idle. Without the green a
  // live link and an idle one differ only by a moving dash, which is invisible
  // in a screenshot and to anyone running prefers-reduced-motion.
  const flowing = duration !== null
  return (
    <g>
      <path
        d={d}
        fill="none"
        strokeLinecap="round"
        strokeWidth={width}
        strokeDasharray={bad || muted ? "5 5" : undefined}
        className={
          bad
            ? "stroke-destructive"
            : flowing
              ? "stroke-emerald-500"
              : "stroke-muted-foreground"
        }
        opacity={bad ? 0.65 : muted ? 0.3 : flowing ? 0.4 : 0.16}
      />
      {duration !== null && (
        <path
          d={d}
          fill="none"
          strokeLinecap="round"
          strokeWidth={Math.max(1, width * 0.5)}
          className="ppx-flow stroke-emerald-500"
          opacity={0.9}
          style={{ animationDuration: `${duration}s` }}
        />
      )}
    </g>
  )
}

/** Seconds per dash cycle, in six fixed steps.
 *
 *  The speed is quantised rather than continuous for the same reason the
 *  history chart turns its animation off: this component re-renders every
 *  second, and changing a CSS animation-duration jumps the animation. Six
 *  buckets means the jump happens when the traffic genuinely changes order of
 *  magnitude, not on every jitter of a decimal place. */
function flowDuration(rps: number): number | null {
  if (!Number.isFinite(rps) || rps <= 0) return null
  if (rps < 1) return 3.2
  if (rps < 5) return 2.4
  if (rps < 20) return 1.8
  if (rps < 100) return 1.3
  if (rps < 500) return 0.9
  return 0.6
}

/* ----------------------------------------------------------------- nodes -- */

function InternetNode({
  box,
  model,
  layout: geometry,
}: {
  box: Box
  model: TopologyModel
  layout: Layout
}) {
  const traffic = `${bytesPerSec(model.totals.bytesInPerSec)} in · ${bytesPerSec(
    model.totals.bytesOutPerSec,
  )} out`

  if (geometry.compact) {
    return (
      <g>
        <Frame box={box} />
        <text x={box.x + 12} y={box.y + 21} className="fill-foreground text-[12px] font-medium">
          Internet
        </text>
        <text
          x={box.x + box.w - 12}
          y={box.y + 21}
          textAnchor="end"
          className="fill-muted-foreground font-mono text-[10px] tabular-nums"
        >
          {fit(traffic, box.w - 100, 10, true)}
        </text>
      </g>
    )
  }

  return (
    <g>
      <Frame box={box} />
      <text
        x={box.x + box.w / 2}
        y={box.y + 20}
        textAnchor="middle"
        className="fill-foreground text-[12px] font-medium"
      >
        Internet
      </text>
      <text
        x={box.x + box.w / 2}
        y={box.y + 34}
        textAnchor="middle"
        className="fill-muted-foreground font-mono text-[9px] tabular-nums"
      >
        {bytesPerSec(model.totals.bytesInPerSec)}
      </text>
      <title>{traffic}</title>
    </g>
  )
}

function ProxyNode({
  box,
  model,
  layout: geometry,
}: {
  box: Box
  model: TopologyModel
  layout: Layout
}) {
  const degraded = model.upstreamsUp < model.upstreamsTotal
  const health = `${model.upstreamsUp} of ${model.upstreamsTotal} upstreams up`
  const load = `${count(model.totals.activeConns)} open · ${millis(
    model.totals.meanLatencyMs,
  )} ms · ${percent(model.totals.errorRate)}% failed`

  const unmatched =
    model.unmatchedRps > 0 ? (
      <g>
        <title>
          These requests arrived for a domain no host claims. They never reach a
          backend.
        </title>
        <text
          x={geometry.compact ? box.x + 42 : box.x + box.w / 2}
          y={box.y + box.h + 13}
          textAnchor={geometry.compact ? "start" : "middle"}
          className="fill-destructive font-mono text-[9px] tabular-nums"
        >
          {rate(model.unmatchedRps)} req/s no matching host
        </text>
      </g>
    ) : null

  if (geometry.compact) {
    return (
      <g>
        <Frame box={box} emphasis />
        <text x={box.x + 12} y={box.y + 22} className="fill-foreground text-[13px] font-semibold">
          ponzproxy
        </text>
        <text
          x={box.x + box.w - 12}
          y={box.y + 22}
          textAnchor="end"
          className="fill-foreground font-mono text-[12px] tabular-nums"
        >
          {rate(model.totals.requestsPerSec)} req/s
        </text>
        <text
          x={box.x + 12}
          y={box.y + 39}
          className="fill-muted-foreground font-mono text-[10px] tabular-nums"
        >
          {fit(load, box.w - 24, 10, true)}
        </text>
        <text
          x={box.x + 12}
          y={box.y + 54}
          className={cn(
            "font-mono text-[10px] tabular-nums",
            degraded ? "fill-destructive" : "fill-muted-foreground",
          )}
        >
          {health}
        </text>
        {unmatched}
      </g>
    )
  }

  return (
    <g>
      <Frame box={box} emphasis />
      <text
        x={box.x + box.w / 2}
        y={box.y + 19}
        textAnchor="middle"
        className="fill-foreground text-[12px] font-semibold"
      >
        ponzproxy
      </text>
      <text
        x={box.x + box.w / 2}
        y={box.y + 36}
        textAnchor="middle"
        className="fill-foreground font-mono text-[13px] tabular-nums"
      >
        {rate(model.totals.requestsPerSec)} req/s
      </text>
      <text
        x={box.x + box.w / 2}
        y={box.y + 51}
        textAnchor="middle"
        className="fill-muted-foreground font-mono text-[9px] tabular-nums"
      >
        {count(model.totals.activeConns)} open · {millis(model.totals.meanLatencyMs)} ms
      </text>
      <text
        x={box.x + box.w / 2}
        y={box.y + 66}
        textAnchor="middle"
        className={cn(
          "font-mono text-[9px] tabular-nums",
          degraded ? "fill-destructive" : "fill-muted-foreground",
        )}
      >
        {model.upstreamsUp}/{model.upstreamsTotal} up · {percent(model.totals.errorRate)}% failed
      </text>
      <title>{`${health}. ${load}.`}</title>
      {unmatched}
    </g>
  )
}

function HostBox({
  node,
  selected,
  onSelect,
}: {
  node: HostNode
  selected: boolean
  onSelect: (selection: Selection | null) => void
}) {
  const { box, view } = node
  const up = view.upstreams.filter((u) => u.state === "up").length
  const total = view.upstreams.length
  const degraded = up < total
  const inset = 12
  const rightEdge = box.x + box.w - inset
  // The chip strip is anchored to the bottom of the box and the line above it
  // is measured off the strip, so the two can never collide when a box height
  // changes.
  const chipY = box.y + box.h - inset - CHIP
  const column = (box.w - inset * 2) * 0.5

  const rps = `${rate(view.requestsPerSec)} req/s`
  // A failure rate is only worth the space when there is one. On a healthy
  // host the line reads "34 ms" and nothing competes with it for attention.
  const failing = view.errorRate > 0
  const latency = `${millis(view.meanLatencyMs)} ms`
  const spelled = `${latency} · ${percent(view.errorRate)}% failed`
  const stats = !failing
    ? latency
    : textWidth(spelled, 10, true) <= (box.w - 24) * 0.5
      ? spelled
      : `${latency} · ${percent(view.errorRate)}%`
  const domains =
    view.domains.length === 0
      ? "no domain"
      : view.domains.length === 1
        ? (view.domains[0] ?? "")
        : `${view.domains[0] ?? ""} +${view.domains.length - 1}`

  const chips = view.policy.length * (CHIP + CHIP_GAP) - CHIP_GAP
  const healthLabel = total === 0 ? "no backends" : `${up}/${total} up`

  return (
    <g
      role="button"
      tabIndex={0}
      aria-label={`${view.name}, ${rps}, ${healthLabel}`}
      className="cursor-pointer"
      onClick={() => onSelect(selected ? null : { kind: "host", hostId: view.hostId })}
      onKeyDown={(event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault()
          onSelect(selected ? null : { kind: "host", hostId: view.hostId })
        }
      }}
    >
      <title>
        {`${view.name} — ${view.domains.join(", ") || "no domain"}\n${rps}, ${stats}\n${
          view.algorithm || "no algorithm"
        }, ${healthLabel}${view.enabled ? "" : "\nDisabled: this host serves nothing."}`}
      </title>
      <Frame box={box} selected={selected} dimmed={!view.enabled} />

      <text
        x={box.x + inset}
        y={box.y + 20}
        className={cn(
          "text-[13px] font-semibold",
          view.enabled ? "fill-foreground" : "fill-muted-foreground",
        )}
      >
        {fit(view.name, box.w - inset * 2 - textWidth(rps, 11, true) - 10, 13, false)}
      </text>
      <text
        x={rightEdge}
        y={box.y + 20}
        textAnchor="end"
        className="fill-foreground font-mono text-[11px] tabular-nums"
      >
        {rps}
      </text>

      <text
        x={box.x + inset}
        y={box.y + 35}
        className="fill-muted-foreground font-mono text-[10px]"
      >
        {fit(domains, box.w - inset * 2, 10, true)}
      </text>

      <text
        x={box.x + inset}
        y={chipY - 7}
        className="fill-muted-foreground text-[10px]"
      >
        {fit(view.enabled ? view.algorithm : "disabled", column, 10, false)}
      </text>
      <text
        x={rightEdge}
        y={chipY - 7}
        textAnchor="end"
        className={cn(
          "font-mono text-[10px] tabular-nums",
          failing ? "fill-destructive" : "fill-muted-foreground",
        )}
      >
        {fit(stats, column, 10, true)}
      </text>

      {view.policy.map((step, i) => (
        <Chip
          key={step.key}
          x={box.x + inset + i * (CHIP + CHIP_GAP)}
          y={chipY}
          step={step}
        />
      ))}
      <text
        x={rightEdge}
        y={chipY + CHIP - 3}
        textAnchor="end"
        className={cn(
          "font-mono text-[10px] tabular-nums",
          degraded ? "fill-destructive" : "fill-muted-foreground",
        )}
      >
        {fit(healthLabel, box.w - inset * 2 - chips - 8, 10, true)}
      </text>
    </g>
  )
}

function UpstreamBox({
  node,
  hostId,
  basisIsWeight,
  selected,
  onSelect,
}: {
  node: UpstreamNode
  hostId: number
  basisIsWeight: boolean
  selected: boolean
  onSelect: (selection: Selection | null) => void
}) {
  const { box, view } = node
  const bad = view.state === "down" || view.state === "ejected"
  const dim = view.state === "paused" || view.state === "unknown"
  const inset = 10

  // A share measured from requests stays meaningful for a backend that has
  // just gone down — it is what that backend was carrying. A share inferred
  // from configured weight does not, so it is withheld rather than implied.
  const share =
    basisIsWeight && view.state !== "up"
      ? "—"
      : `${Math.round(view.share * 100)}%`

  const detail = bad
    ? (view.problem ?? "Not serving")
    : `${STATE_LABEL[view.state]} · ${count(view.activeConns)} open · ${millis(
        view.meanLatencyMs,
      )} ms · weight ${view.weight}`

  // `http://` is the default for a backend behind a terminating proxy and
  // eats a third of the line at the narrowest width. `https://` is the case
  // worth seeing, so that one is kept. The full address is in the tooltip and
  // in the detail panel either way.
  const shown = view.address.startsWith("http://")
    ? view.address.slice("http://".length)
    : view.address

  const label = `${view.address}, ${STATE_LABEL[view.state]}, ${share} of this host's traffic`

  return (
    <g
      role="button"
      tabIndex={0}
      aria-label={label}
      className="cursor-pointer"
      onClick={() =>
        onSelect(selected ? null : { kind: "upstream", hostId, key: view.key })
      }
      onKeyDown={(event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault()
          onSelect(selected ? null : { kind: "upstream", hostId, key: view.key })
        }
      }}
    >
      <title>{`${view.address}\n${STATE_LABEL[view.state]} · ${share} of this host's traffic${
        view.problem ? `\n${view.problem}` : ""
      }`}</title>
      <Frame box={box} selected={selected} bad={bad} dimmed={dim} />

      <circle
        cx={box.x + inset + 4}
        cy={box.y + 16}
        r={4}
        className={cn(
          bad ? "fill-destructive" : dim ? "fill-muted-foreground" : "fill-emerald-500",
        )}
      />
      <text
        x={box.x + inset + 15}
        y={box.y + 20}
        className={cn(
          "font-mono text-[11px]",
          bad ? "fill-destructive line-through" : dim ? "fill-muted-foreground" : "fill-foreground",
        )}
      >
        {fit(shown, box.w - inset * 2 - 15 - 36, 11, true)}
      </text>
      <text
        x={box.x + box.w - inset}
        y={box.y + 20}
        textAnchor="end"
        className={cn(
          "font-mono text-[11px] font-semibold tabular-nums",
          bad ? "fill-destructive" : "fill-foreground",
        )}
      >
        {share}
      </text>
      <text
        x={box.x + inset}
        y={box.y + 34}
        className={cn(
          "text-[10px]",
          bad ? "fill-destructive" : "fill-muted-foreground",
        )}
      >
        {fit(detail, box.w - inset * 2, 10, false)}
      </text>
    </g>
  )
}

/* ---------------------------------------------------------------- pieces -- */

/** Frame is every node's rectangle. Fill is `muted` rather than `card`
 *  because the diagram sits on a card and a card-on-card has no edge in
 *  either theme. */
function Frame({
  box,
  emphasis = false,
  selected = false,
  bad = false,
  dimmed = false,
}: {
  box: Box
  emphasis?: boolean
  selected?: boolean
  bad?: boolean
  dimmed?: boolean
}) {
  return (
    <rect
      x={box.x}
      y={box.y}
      width={box.w}
      height={box.h}
      rx={8}
      strokeWidth={selected ? 2 : bad || emphasis ? 1.5 : 1}
      strokeDasharray={dimmed ? "4 3" : undefined}
      className={cn(
        bad ? "fill-destructive/10" : "fill-muted",
        selected
          ? "stroke-foreground"
          : bad
            ? "stroke-destructive"
            : emphasis
              ? "stroke-foreground/35"
              : "stroke-border",
      )}
      opacity={dimmed && !selected ? 0.75 : 1}
    />
  )
}

const CHIP = 15
const CHIP_GAP = 4

/** Chip is one link of the TRAIL chain. Off is an empty outline, on is solid,
 *  and only the two states that need an operator's attention take colour. */
function Chip({ x, y, step }: { x: number; y: number; step: PolicyStep }) {
  const box = {
    off: "fill-none stroke-border",
    on: "fill-foreground stroke-none",
    warn: "fill-amber-500/20 stroke-amber-500",
    bad: "fill-destructive/15 stroke-destructive",
  }[step.level]

  const letter = {
    off: "fill-muted-foreground",
    on: "fill-background",
    warn: "fill-amber-600 dark:fill-amber-400",
    bad: "fill-destructive",
  }[step.level]

  return (
    <g opacity={step.level === "off" ? 0.7 : 1}>
      <title>{`${step.label}: ${step.detail}`}</title>
      <rect x={x} y={y} width={CHIP} height={CHIP} rx={3.5} strokeWidth={1} className={box} />
      <text
        x={x + CHIP / 2}
        y={y + CHIP - 4}
        textAnchor="middle"
        className={cn("text-[9px] font-semibold", letter)}
      >
        {step.letter}
      </text>
    </g>
  )
}

/* ------------------------------------------------------------------ text -- */

// SVG has no ellipsis, so labels are trimmed against an estimated advance
// width. The factors are measured averages for the console's two families;
// they only ever need to be close enough to keep text inside its box.
const ADVANCE = { sans: 0.54, mono: 0.6 }

function textWidth(text: string, fontPx: number, mono: boolean): number {
  return text.length * fontPx * (mono ? ADVANCE.mono : ADVANCE.sans)
}

function fit(text: string, maxPx: number, fontPx: number, mono: boolean): string {
  const per = fontPx * (mono ? ADVANCE.mono : ADVANCE.sans)
  const max = Math.floor(maxPx / per)
  if (max >= text.length) return text
  if (max <= 1) return "…"
  return `${text.slice(0, max - 1)}…`
}
