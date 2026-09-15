import {
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
} from "@/components/ui/sidebar"
import type { SystemSnapshot } from "@/api/types"
import { duration, rate } from "@/format"
import { cn } from "@/lib/utils"

interface Props {
  system: SystemSnapshot | undefined
  requestsPerSec: number
}

/** NavStatus fills the slot dashboard-01 gives to a secondary nav group.
 *
 *  The template puts Settings, Get Help and Search there. ponzproxy has no
 *  such screens, and a link that goes nowhere is worse than an empty corner —
 *  so the space carries the three figures worth having in peripheral vision
 *  on every screen instead. */
export function NavStatus({ system, requestsPerSec }: Props) {
  const degraded =
    system !== undefined && system.upstreamsUp < system.upstreamsTotal

  const rows = [
    { label: "Throughput", value: `${rate(requestsPerSec)} req/s` },
    {
      label: "Upstreams",
      value: system ? `${system.upstreamsUp}/${system.upstreamsTotal}` : "—",
      alarm: degraded,
    },
    { label: "Uptime", value: system ? duration(system.uptimeSeconds) : "—" },
  ]

  return (
    <SidebarGroup className="mt-auto group-data-[collapsible=icon]:hidden">
      <SidebarGroupLabel>System</SidebarGroupLabel>
      <SidebarGroupContent className="flex flex-col gap-1 px-2 text-sm">
        {rows.map((row) => (
          <div key={row.label} className="flex items-center justify-between">
            <span className="text-muted-foreground">{row.label}</span>
            <span
              className={cn(
                "font-mono text-xs tabular-nums",
                row.alarm && "text-destructive",
              )}
            >
              {row.value}
            </span>
          </div>
        ))}
      </SidebarGroupContent>
    </SidebarGroup>
  )
}
