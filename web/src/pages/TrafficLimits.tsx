import type { Host, Snapshot } from "@/api/types"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
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

interface Props {
  hosts: Host[]
  snapshot: Snapshot | null
  canEdit: boolean
  onEditHost: (host: Host) => void
}

/** TrafficLimits is the overview the per-host setting needs to be usable.
 *
 *  Detect mode is worth nothing without somewhere to read what it observed:
 *  an operator switching limits on is deciding a number, and the only honest
 *  input to that decision is how often the number they are considering would
 *  have fired on their own traffic. That is what the two right-hand columns
 *  are for, and why "would have been refused" and "was refused" are separate
 *  figures rather than one total. */
export function TrafficLimits({ hosts, snapshot, canEdit, onEditHost }: Props) {
  const live = new Map((snapshot?.hosts ?? []).map((h) => [h.hostId, h]))

  const enabled = hosts.filter((h) => h.trafficLimits.mode !== "off")
  const off = hosts.filter((h) => h.trafficLimits.mode === "off")

  return (
    <div className="flex flex-col gap-4 px-4 lg:px-6">
      <Card>
        <CardHeader>
          <CardTitle>Traffic limits</CardTitle>
          <CardDescription>
            What one client address may ask of each host. This is not DDoS
            protection — a volumetric attack saturates the uplink before it
            reaches the proxy, and only an upstream scrubber can help there.
            What it covers is one client, or a script, asking for more than
            your backends can serve.
          </CardDescription>
        </CardHeader>

        <CardContent className="px-0">
          {hosts.length === 0 ? (
            <p className="text-muted-foreground px-6 py-8 text-center text-sm">
              No hosts yet. Limits are set on each host.
            </p>
          ) : (
            <div className="overflow-x-auto">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Host</TableHead>
                    <TableHead>Mode</TableHead>
                    <TableHead className="text-right">Rate</TableHead>
                    <TableHead className="text-right">In flight</TableHead>
                    <TableHead className="text-right">Body</TableHead>
                    <TableHead className="text-right">Exempt</TableHead>
                    <TableHead className="text-right">Over the limit</TableHead>
                    <TableHead className="text-right">Refused</TableHead>
                    {canEdit && <TableHead />}
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {[...enabled, ...off].map((host) => {
                    const l = host.trafficLimits
                    const stats = live.get(host.id)
                    const limited = stats?.limitedRequests ?? 0
                    const blocked = stats?.blockedRequests ?? 0
                    const dim = l.mode === "off"
                    return (
                      <TableRow key={host.id} className={dim ? "opacity-60" : undefined}>
                        <TableCell className="font-medium">
                          {host.name}
                          <div className="text-muted-foreground font-mono text-xs">
                            {host.domains[0] ?? "no domain"}
                            {host.domains.length > 1 && ` +${host.domains.length - 1}`}
                          </div>
                        </TableCell>
                        <TableCell>
                          <ModeBadge mode={l.mode} />
                        </TableCell>
                        <TableCell className="text-right font-mono text-xs tabular-nums">
                          {dim ? "—" : rate(l.requestsPerSecond, l.burst)}
                        </TableCell>
                        <TableCell className="text-right font-mono text-xs tabular-nums">
                          {dim || l.maxConcurrent === 0 ? "—" : l.maxConcurrent}
                        </TableCell>
                        <TableCell className="text-right font-mono text-xs tabular-nums">
                          {dim || l.maxBodyBytes === 0 ? "—" : bytes(l.maxBodyBytes)}
                        </TableCell>
                        <TableCell className="text-right font-mono text-xs tabular-nums">
                          {dim || (l.exempt ?? []).length === 0
                            ? "—"
                            : (l.exempt ?? []).length}
                        </TableCell>
                        <TableCell className="text-right font-mono text-xs tabular-nums">
                          {limited > 0 ? limited.toLocaleString() : "—"}
                        </TableCell>
                        <TableCell className="text-right font-mono text-xs tabular-nums">
                          {blocked > 0 ? (
                            <span className="text-destructive">
                              {blocked.toLocaleString()}
                            </span>
                          ) : (
                            "—"
                          )}
                        </TableCell>
                        {canEdit && (
                          <TableCell className="text-right">
                            <Button
                              variant="ghost"
                              size="sm"
                              onClick={() => onEditHost(host)}
                            >
                              Edit
                            </Button>
                          </TableCell>
                        )}
                      </TableRow>
                    )
                  })}
                </TableBody>
              </Table>
            </div>
          )}
        </CardContent>
      </Card>

      <p className="text-muted-foreground text-xs">
        Counts are since the proxy last started. In <strong>detect</strong> a
        host reports what would have been refused and refuses nothing, which is
        the number to read before switching a host to <strong>block</strong>.
        Limits count per client address, so behind Cloudflare or NAT every
        visitor shares one budget unless{" "}
        <code>PONZ_TRUSTED_PROXY_HEADER</code> is set.
      </p>
    </div>
  )
}

function ModeBadge({ mode }: { mode: Host["trafficLimits"]["mode"] }) {
  if (mode === "block") return <Badge variant="destructive">block</Badge>
  if (mode === "detect") {
    return (
      <Badge className="border-amber-500 bg-amber-500/15 text-amber-700 dark:text-amber-400">
        detect
      </Badge>
    )
  }
  return <Badge variant="secondary">off</Badge>
}

/** rate shows the burst only when it adds something, so the common case of
 *  "burst equals rate" does not read as two different numbers. */
function rate(rps: number, burst: number): string {
  if (rps === 0) return "—"
  if (burst > rps) return `${rps}/s · ${burst} burst`
  return `${rps}/s`
}

export function bytes(n: number): string {
  const units = ["B", "kB", "MB", "GB", "TB", "PB"]
  let value = n
  let unit = 0
  while (value >= 1000 && unit < units.length - 1) {
    value /= 1000
    unit++
  }
  // One decimal below 10 keeps 1.5 GB from rounding to 2 GB, which matters
  // when the figure is a bill.
  const digits = value < 10 && unit > 0 ? 1 : 0
  return `${value.toFixed(digits)} ${units[unit]}`
}
