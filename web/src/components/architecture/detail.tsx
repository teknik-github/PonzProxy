import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { count, millis, percent, rate } from "@/format"
import { cn } from "@/lib/utils"
import { STATE_LABEL, type HostView, type PolicyStep, type TopologyModel, type UpstreamView } from "./model"
import type { Selection } from "./topology"

interface Props {
  model: TopologyModel
  selection: Selection | null
  onClear: () => void
}

/** Detail is the panel under the diagram for whatever the operator clicked.
 *
 *  The diagram carries as much as fits in a 15px square; this is where the
 *  sentences live — which certificate, which access list, why a backend was
 *  ejected and when it comes back. Nothing here is available by hovering
 *  alone, which is why it is a panel and not only a tooltip. */
export function Detail({ model, selection, onClear }: Props) {
  if (!selection) return null

  const host = model.hosts.find((h) => h.hostId === selection.hostId)
  // The selection outlives any single snapshot, so the thing it names can be
  // deleted out from under it. Showing nothing is correct; the diagram above
  // has already stopped drawing it.
  if (!host) return null

  const windowSeconds = model.shareWindowSeconds

  if (selection.kind === "upstream") {
    const upstream = host.upstreams.find((u) => u.key === selection.key)
    if (!upstream) return null
    return (
      <Panel
        title={upstream.address}
        mono
        badge={
          <Badge variant={upstream.state === "up" ? "outline" : "destructive"}>
            {STATE_LABEL[upstream.state]}
          </Badge>
        }
        onClear={onClear}
      >
        {upstream.problem && (
          <p className="text-destructive text-sm">{upstream.problem}</p>
        )}
        <Facts
          facts={[
            {
              label: "share of host traffic",
              value: `${Math.round(upstream.share * 100)}%`,
            },
            {
              label: `requests in the last ${windowSeconds}s`,
              value: count(upstream.windowRequests),
            },
            { label: "requests served", value: count(upstream.totalRequests) },
            { label: "open connections", value: count(upstream.activeConns) },
            {
              label: "mean response",
              value: `${millis(upstream.meanLatencyMs)} ms`,
            },
            { label: "configured weight", value: String(upstream.weight) },
            {
              label: "serves",
              value: upstream.location === "" ? "everything else" : upstream.location,
            },
            { label: "host", value: host.name },
          ]}
        />
        <p className="text-muted-foreground text-xs">
          {host.basis === "window"
            ? `Share is measured from the requests this backend served in the last ${windowSeconds} seconds, so it follows a change to the host's algorithm within about that long.`
            : host.basis === "requests"
              ? `Nothing has arrived in the last ${windowSeconds} seconds, so share is the split over everything served since the proxy started.`
              : "Nothing has been served yet, so share is the configured weight rather than a measurement."}
        </p>
      </Panel>
    )
  }

  return (
    <Panel
      title={host.name}
      badge={
        host.enabled ? null : <Badge variant="secondary">disabled</Badge>
      }
      onClear={onClear}
    >
      <p className="text-muted-foreground font-mono text-xs">
        {host.domains.length > 0 ? host.domains.join("  ") : "no domains"}
      </p>
      <Facts facts={hostFacts(host)} />
      <div className="flex flex-col gap-2">
        {host.policy.map((step) => (
          <Step key={step.key} step={step} />
        ))}
        {host.policy.length === 0 && (
          <p className="text-muted-foreground text-xs">
            This host is in the live feed but not in the console's host list, so
            what sits in front of it is unknown here.
          </p>
        )}
      </div>
    </Panel>
  )
}

function hostFacts(host: HostView): Fact[] {
  const up = host.upstreams.filter((u: UpstreamView) => u.state === "up").length
  return [
    { label: "throughput", value: `${rate(host.requestsPerSec)} req/s` },
    { label: "mean response", value: `${millis(host.meanLatencyMs)} ms` },
    {
      label: "failed",
      value: `${percent(host.errorRate)}%`,
      alarm: host.errorRate > 0,
    },
    { label: "open connections", value: count(host.activeConns) },
    { label: "balancing", value: host.algorithm || "—" },
    {
      label: "backends up",
      value: `${up} of ${host.upstreams.length}`,
      alarm: up < host.upstreams.length,
    },
  ]
}

interface Fact {
  label: string
  value: string
  alarm?: boolean
}

function Facts({ facts }: { facts: Fact[] }) {
  return (
    <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-6">
      {facts.map((fact) => (
        // min-w-0 so a long value such as "weighted round robin" wraps inside
        // its own column instead of running into the next one.
        <div key={fact.label} className="min-w-0">
          <div
            className={cn(
              "font-mono break-words tabular-nums",
              // The grid is sized for figures. A word-shaped value such as an
              // algorithm name gets a smaller size rather than a column of
              // its own, so the numbers stay the same size as each other.
              fact.value.length > 12 ? "text-sm" : "text-base",
              fact.alarm && "text-destructive",
            )}
          >
            {fact.value}
          </div>
          <div className="text-muted-foreground text-xs">{fact.label}</div>
        </div>
      ))}
    </div>
  )
}

function Step({ step }: { step: PolicyStep }) {
  const square = {
    off: "border-border text-muted-foreground border bg-transparent",
    on: "bg-foreground text-background",
    warn: "border-amber-500 bg-amber-500/20 text-amber-600 dark:text-amber-400 border",
    bad: "border-destructive bg-destructive/15 text-destructive border",
  }[step.level]

  return (
    <div className="flex items-start gap-2 text-sm">
      <span
        className={cn(
          "mt-0.5 inline-flex size-[15px] shrink-0 items-center justify-center rounded-[3.5px] text-[9px] font-semibold",
          square,
        )}
      >
        {step.letter}
      </span>
      <span className="w-36 shrink-0">{step.label}</span>
      <span
        className={cn(
          "text-muted-foreground",
          step.level === "bad" && "text-destructive",
          step.level === "warn" && "text-amber-600 dark:text-amber-400",
        )}
      >
        {step.detail}
      </span>
    </div>
  )
}

function Panel({
  title,
  mono = false,
  badge,
  onClear,
  children,
}: {
  title: string
  mono?: boolean
  badge?: React.ReactNode
  onClear: () => void
  children: React.ReactNode
}) {
  return (
    <div className="flex flex-col gap-4 border-t px-4 py-4 sm:px-6">
      <div className="flex items-center gap-3">
        <h3 className={cn("font-medium break-all", mono && "font-mono text-sm")}>
          {title}
        </h3>
        {badge}
        <Button variant="ghost" size="sm" className="ml-auto" onClick={onClear}>
          Close
        </Button>
      </div>
      {children}
    </div>
  )
}
