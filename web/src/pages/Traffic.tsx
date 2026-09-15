import { ChartAreaInteractive } from "@/components/chart-area-interactive"
import { SectionCards } from "@/components/section-cards"
import { UpstreamTable } from "@/components/upstream-table"
import {
  Card,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import type { Host, Snapshot } from "@/api/types"
import { count, duration } from "@/format"

interface Props {
  snapshot: Snapshot | null
  hosts: Host[]
}

export function Traffic({ snapshot, hosts }: Props) {
  return (
    <>
      <SectionCards snapshot={snapshot} />
      <div className="px-4 lg:px-6">
        <ChartAreaInteractive />
      </div>
      <UpstreamTable hosts={hosts} live={snapshot?.hosts ?? []} />
      <ProcessCard snapshot={snapshot} />
    </>
  )
}

function ProcessCard({ snapshot }: { snapshot: Snapshot | null }) {
  const system = snapshot?.system
  if (!system) return null

  const facts = [
    { label: "uptime", value: duration(system.uptimeSeconds) },
    {
      label: "upstreams up",
      value: `${system.upstreamsUp} of ${system.upstreamsTotal}`,
      alarm: system.upstreamsUp < system.upstreamsTotal,
    },
    { label: "hosts enabled", value: String(system.hostsEnabled) },
    { label: "cpu", value: `${system.cpuPercent.toFixed(1)}%` },
    { label: "memory", value: `${(system.heapBytes / 1_048_576).toFixed(0)} MB` },
    { label: "goroutines", value: count(system.goroutines) },
  ]

  return (
    <div className="px-4 lg:px-6">
      <Card>
        <CardHeader>
          <CardTitle>Process</CardTitle>
          <CardDescription>The proxy itself, not the traffic</CardDescription>
        </CardHeader>
        <div className="grid grid-cols-2 gap-4 px-6 pb-6 sm:grid-cols-3 lg:grid-cols-6">
          {facts.map((f) => (
            <div key={f.label}>
              <div
                className={`font-mono text-lg tabular-nums ${
                  f.alarm ? "text-destructive" : ""
                }`}
              >
                {f.value}
              </div>
              <div className="text-muted-foreground text-xs">{f.label}</div>
            </div>
          ))}
        </div>
      </Card>
    </div>
  )
}
