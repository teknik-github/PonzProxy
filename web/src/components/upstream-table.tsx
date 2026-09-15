import { Badge } from "@/components/ui/badge"
import {
  Card,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import type { Host, HostSnapshot, UpstreamSnapshot } from "@/api/types"
import { count, millis, rate } from "@/format"

interface Props {
  hosts: Host[]
  live: HostSnapshot[]
}

/** UpstreamTable answers the question a load balancer console exists to
 *  answer: which backend is actually taking the load, and is any of them out.
 *
 *  The share column carries a bar because the proportions are the point — a
 *  5:2:1 weighting and an even split read differently at a glance, where three
 *  percentages do not. */
export function UpstreamTable({ hosts, live }: Props) {
  const configured = new Map(hosts.map((h) => [h.id, h]))

  // Hosts the operator configured come first; the synthetic bucket that
  // collects traffic for unknown domains is not one of them.
  const rows = live.filter((h) => (h.upstreams?.length ?? 0) > 0)
  const unmatched = live.find((h) => h.hostId === 0)

  return (
    <div className="px-4 lg:px-6">
      <Card>
        <CardHeader>
          <CardTitle>Where traffic is going</CardTitle>
          <CardDescription>
            Share is measured from the requests each upstream has actually
            served, so the balancing algorithm is visible rather than assumed.
          </CardDescription>
        </CardHeader>

        {rows.length === 0 ? (
          <div className="text-muted-foreground px-6 pb-8 text-center text-sm">
            <p className="text-foreground font-medium">
              Nothing is being proxied yet
            </p>
            <p>Add a host to start routing a domain to your servers.</p>
          </div>
        ) : (
          <div className="overflow-x-auto px-2 pb-2 sm:px-6 sm:pb-6">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Upstream</TableHead>
                  <TableHead className="w-[180px]">Share of traffic</TableHead>
                  <TableHead className="text-right">Open</TableHead>
                  <TableHead className="text-right">Response</TableHead>
                  <TableHead className="text-right">Weight</TableHead>
                  <TableHead className="text-right">State</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((host) => {
                  const config = configured.get(host.hostId)
                  const upstreams = host.upstreams ?? []
                  const total =
                    upstreams.reduce((sum, u) => sum + u.totalRequests, 0) || 1
                  return (
                    <HostGroup
                      key={host.hostId}
                      host={host}
                      algorithm={config?.algorithm ?? ""}
                      upstreams={upstreams}
                      total={total}
                    />
                  )
                })}
                {unmatched && (
                  <TableRow className="bg-muted/30">
                    <TableCell className="text-muted-foreground font-mono text-xs">
                      requests for domains with no host
                    </TableCell>
                    <TableCell colSpan={4} />
                    <TableCell className="text-right">
                      <span className="text-muted-foreground font-mono text-xs tabular-nums">
                        {rate(unmatched.traffic.requestsPerSec)} req/s
                      </span>
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          </div>
        )}
      </Card>
    </div>
  )
}

function HostGroup({
  host,
  algorithm,
  upstreams,
  total,
}: {
  host: HostSnapshot
  algorithm: string
  upstreams: UpstreamSnapshot[]
  total: number
}) {
  return (
    <>
      <TableRow className="bg-muted/50 hover:bg-muted/50">
        <TableCell colSpan={4} className="font-medium">
          {host.name}
          {algorithm && (
            <span className="text-muted-foreground ml-2 text-xs font-normal">
              {algorithm.replace(/_/g, " ")}
            </span>
          )}
        </TableCell>
        <TableCell colSpan={2} className="text-right">
          <span className="text-muted-foreground font-mono text-xs tabular-nums">
            {rate(host.traffic.requestsPerSec)} req/s ·{" "}
            {count(host.traffic.activeConns)} open
          </span>
        </TableCell>
      </TableRow>

      {upstreams.map((u) => {
        const share = u.totalRequests / total
        const serving = u.healthy && u.enabled && !u.ejected && !u.ejected
        return (
          <TableRow key={u.address}>
            <TableCell className="pl-8 font-mono text-xs">
              <span
                className={
                  serving ? "" : "text-destructive line-through decoration-1"
                }
              >
                {u.address}
              </span>
              {!serving && u.lastError && (
                <div className="text-destructive mt-0.5 text-xs">
                  {u.lastError}
                </div>
              )}
              {u.ejected && (
                <div className="text-muted-foreground mt-0.5 text-xs">
                  Back in rotation in {u.ejectedForSeconds ?? 0}s
                </div>
              )}
            </TableCell>
            <TableCell>
              <div className="flex items-center gap-2">
                <div className="bg-muted h-1.5 w-full overflow-hidden rounded-full">
                  <div
                    className={`h-full rounded-full ${
                      serving ? "bg-chart-2" : "bg-destructive"
                    }`}
                    style={{ width: `${Math.round(share * 100)}%` }}
                  />
                </div>
                <span className="text-muted-foreground w-9 shrink-0 text-right font-mono text-xs tabular-nums">
                  {Math.round(share * 100)}%
                </span>
              </div>
            </TableCell>
            <TableCell className="text-right font-mono text-xs tabular-nums">
              {count(u.activeConns)}
            </TableCell>
            <TableCell className="text-right font-mono text-xs tabular-nums">
              {millis(u.meanLatencyMs)} ms
            </TableCell>
            <TableCell className="text-right font-mono text-xs tabular-nums">
              {u.weight}
            </TableCell>
            <TableCell className="text-right">
              <Badge variant={badgeVariant(u)}>{stateLabel(u)}</Badge>
            </TableCell>
          </TableRow>
        )
      })}
    </>
  )
}

// The three ways a backend leaves rotation read differently on purpose:
// "paused" is a person's decision, "down" is the active probe's verdict, and
// "ejected" is real traffic failing. An operator needs to know which.
function stateLabel(u: UpstreamSnapshot): string {
  if (!u.enabled) return "paused"
  if (u.ejected) return "ejected"
  return u.healthy ? "up" : "down"
}

function badgeVariant(u: UpstreamSnapshot): "outline" | "destructive" | "secondary" {
  if (!u.enabled) return "secondary"
  return u.healthy && !u.ejected ? "outline" : "destructive"
}
