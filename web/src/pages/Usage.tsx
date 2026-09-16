import { useCallback, useEffect, useState } from "react"

import { IconFileSpreadsheet, IconFileTypePdf } from "@tabler/icons-react"

import { api } from "@/api/client"
import type { UsageReport, UsageRow } from "@/api/types"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Table,
  TableBody,
  TableCell,
  TableFooter,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { bytes } from "@/pages/TrafficLimits"

/** The windows an operator actually asks about. "This month" is missing on
 *  purpose: a calendar month is not the same as thirty days, and offering it
 *  without knowing the billing day would be a guess dressed as a figure. */
const PERIODS = [
  { id: "24h", label: "Last 24 hours", days: 1 },
  { id: "7d", label: "Last 7 days", days: 7 },
  { id: "30d", label: "Last 30 days", days: 30 },
] as const

type PeriodID = (typeof PERIODS)[number]["id"]

/** Usage reports how much traffic each host carried.
 *
 *  It reads the same samples as the historical charts, which is why it can
 *  only reach back as far as they are kept — and why it says so when the
 *  window asked for is wider than the retention rather than quietly returning
 *  a small number someone might compare against a hosting bill. */
export function Usage() {
  const [period, setPeriod] = useState<PeriodID>("7d")
  const [report, setReport] = useState<UsageReport | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [downloading, setDownloading] = useState<"xlsx" | "pdf" | null>(null)

  const windowOf = (id: PeriodID) => {
    const days = PERIODS.find((p) => p.id === id)?.days ?? 7
    const to = new Date()
    return { from: new Date(to.getTime() - days * 24 * 60 * 60 * 1000), to }
  }

  const save = (format: "xlsx" | "pdf") => {
    setDownloading(format)
    api
      .downloadUsage({ ...windowOf(period), format })
      .catch(() => setError(`Could not build the ${format.toUpperCase()} report.`))
      .finally(() => setDownloading(null))
  }

  const load = useCallback(() => {
    const days = PERIODS.find((p) => p.id === period)?.days ?? 7
    const to = new Date()
    const from = new Date(to.getTime() - days * 24 * 60 * 60 * 1000)

    setLoading(true)
    api
      .usage({ from, to })
      .then((r) => {
        setReport(r)
        setError(null)
      })
      .catch(() => setError("Could not load the usage report."))
      .finally(() => setLoading(false))
  }, [period])

  useEffect(load, [load])

  const rows: UsageRow[] = report?.rows ?? []
  const totals = rows.reduce(
    (acc, r) => ({
      requests: acc.requests + r.requests,
      bytesIn: acc.bytesIn + r.bytesIn,
      bytesOut: acc.bytesOut + r.bytesOut,
      total: acc.total + r.totalBytes,
    }),
    { requests: 0, bytesIn: 0, bytesOut: 0, total: 0 },
  )

  return (
    <div className="flex flex-col gap-4 px-4 lg:px-6">
      <Card>
        <CardHeader>
          <CardTitle>Traffic used</CardTitle>
          <CardDescription>
            How much each host carried, heaviest first. Out is what your
            visitors downloaded, which is the half a hosting bill usually
            charges for.
          </CardDescription>
          <CardAction className="flex flex-wrap items-center gap-2">
            <Select value={period} onValueChange={(v) => setPeriod(v as PeriodID)}>
              <SelectTrigger className="w-40" aria-label="Period">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {PERIODS.map((p) => (
                  <SelectItem key={p.id} value={p.id}>
                    {p.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {/* The exports cover the period selected above, not everything
                ever recorded, so they sit next to the picker rather than at
                the foot of the page. */}
            <Button
              variant="outline"
              size="sm"
              disabled={rows.length === 0 || downloading !== null}
              onClick={() => save("xlsx")}
            >
              <IconFileSpreadsheet />
              {downloading === "xlsx" ? "Building…" : "Excel"}
            </Button>
            <Button
              variant="outline"
              size="sm"
              disabled={rows.length === 0 || downloading !== null}
              onClick={() => save("pdf")}
            >
              <IconFileTypePdf />
              {downloading === "pdf" ? "Building…" : "PDF"}
            </Button>
          </CardAction>
        </CardHeader>

        <CardContent className="px-0">
          {error ? (
            <p className="text-destructive px-6 py-8 text-center text-sm">{error}</p>
          ) : loading && report === null ? (
            <p className="text-muted-foreground px-6 py-8 text-center text-sm">
              Loading…
            </p>
          ) : rows.length === 0 ? (
            <p className="text-muted-foreground px-6 py-8 text-center text-sm">
              Nothing was served in this window.
            </p>
          ) : (
            <div className="overflow-x-auto">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Host</TableHead>
                    <TableHead className="text-right">Requests</TableHead>
                    <TableHead className="text-right">In</TableHead>
                    <TableHead className="text-right">Out</TableHead>
                    <TableHead className="text-right">Total</TableHead>
                    <TableHead className="text-right">Share</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {rows.map((row) => (
                    <TableRow key={row.hostId}>
                      <TableCell className="font-medium">{row.name}</TableCell>
                      <TableCell className="text-right font-mono text-xs tabular-nums">
                        {row.requests.toLocaleString()}
                      </TableCell>
                      <TableCell className="text-right font-mono text-xs tabular-nums">
                        {bytes(row.bytesIn)}
                      </TableCell>
                      <TableCell className="text-right font-mono text-xs tabular-nums">
                        {bytes(row.bytesOut)}
                      </TableCell>
                      <TableCell className="text-right font-mono text-xs tabular-nums">
                        {bytes(row.totalBytes)}
                      </TableCell>
                      <TableCell className="text-right">
                        <Share fraction={totals.total > 0 ? row.totalBytes / totals.total : 0} />
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
                <TableFooter>
                  <TableRow>
                    <TableCell className="font-medium">All hosts</TableCell>
                    <TableCell className="text-right font-mono text-xs tabular-nums">
                      {totals.requests.toLocaleString()}
                    </TableCell>
                    <TableCell className="text-right font-mono text-xs tabular-nums">
                      {bytes(totals.bytesIn)}
                    </TableCell>
                    <TableCell className="text-right font-mono text-xs tabular-nums">
                      {bytes(totals.bytesOut)}
                    </TableCell>
                    <TableCell className="text-right font-mono text-xs tabular-nums">
                      {bytes(totals.total)}
                    </TableCell>
                    <TableCell />
                  </TableRow>
                </TableFooter>
              </Table>
            </div>
          )}
        </CardContent>
      </Card>

      {report?.truncated && (
        <p className="text-muted-foreground border-l-2 border-amber-500 pl-3 text-xs">
          History is kept for {report.retentionDays} days, so this window covers
          less time than it asks for. Raise{" "}
          <code>PONZ_METRICS_RETENTION</code> to report further back.
        </p>
      )}

      <p className="text-muted-foreground text-xs">
        Counted at the proxy: request and response bytes as they crossed it,
        headers included. A response served from the cache still counts — your
        visitor downloaded it either way — while a request refused by an access
        list, the guardian or a traffic limit never reached a backend and shows
        only the bytes it cost to refuse.
      </p>
    </div>
  )
}

/** Share is a bar rather than a number because the question it answers is
 *  "which host is the expensive one", and that is a comparison. */
function Share({ fraction }: { fraction: number }) {
  const pct = Math.round(fraction * 100)
  return (
    <span className="flex items-center justify-end gap-2">
      <span className="bg-muted hidden h-1.5 w-20 overflow-hidden rounded-full sm:block">
        <span
          className="bg-foreground block h-full rounded-full"
          style={{ width: `${Math.max(fraction * 100, fraction > 0 ? 2 : 0)}%` }}
        />
      </span>
      <span className="font-mono text-xs tabular-nums">{pct}%</span>
    </span>
  )
}
