import { useCallback, useEffect, useState } from "react"
import { IconAlertTriangle, IconRefresh, IconSearch } from "@tabler/icons-react"

import { api, ApiError } from "@/api/client"
import type { AccessLogPage, Host } from "@/api/types"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
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
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { bytes, count, millis } from "@/format"

/** The sizes offered in the picker. 100 stays the default so the screen
 *  behaves as it did before the choice existed. */
const PAGE_SIZES = [10, 20, 50, 100] as const
const DEFAULT_PAGE_SIZE = 100

const WINDOWS = {
  "1h": { label: "Last hour", hours: 1 },
  "24h": { label: "Last 24 hours", hours: 24 },
  "7d": { label: "Last 7 days", hours: 24 * 7 },
} satisfies Record<string, { label: string; hours: number }>

type WindowKey = keyof typeof WINDOWS

interface Props {
  hosts: Host[]
}

export function AccessLog({ hosts }: Props) {
  const [page, setPage] = useState<AccessLogPage | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)

  const [hostId, setHostId] = useState("all")
  const [statusClass, setStatusClass] = useState("all")
  const [range, setRange] = useState<WindowKey>("24h")
  const [failedOnly, setFailedOnly] = useState(false)
  const [search, setSearch] = useState("")
  // Applied separately from `search` so typing does not fire a query per
  // keystroke against a table that can hold hundreds of thousands of rows.
  const [applied, setApplied] = useState("")
  const [offset, setOffset] = useState(0)
  const [pageSize, setPageSize] = useState<number>(DEFAULT_PAGE_SIZE)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const to = new Date()
      const from = new Date(to.getTime() - WINDOWS[range].hours * 3_600_000)
      const result = await api.accessLog({
        ...(hostId !== "all" ? { hostId: Number(hostId) } : {}),
        ...(statusClass !== "all" ? { statusClass: Number(statusClass) } : {}),
        ...(applied ? { search: applied } : {}),
        ...(failedOnly ? { failedOnly: true } : {}),
        from,
        to,
        limit: pageSize,
        offset,
      })
      setPage(result)
      setError(null)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Could not load the access log.")
    } finally {
      setLoading(false)
    }
  }, [hostId, statusClass, applied, failedOnly, range, offset, pageSize])

  useEffect(() => {
    void load()
  }, [load])

  // Any filter change returns to the first page; staying on page 4 of a
  // different result set shows nothing and looks broken.
  useEffect(() => {
    setOffset(0)
  }, [hostId, statusClass, applied, failedOnly, range, pageSize])

  const entries = page?.entries ?? []
  const total = page?.total ?? 0
  const dropped = page?.stats.dropped ?? 0
  const loggingHosts = hosts.filter((h) => h.accessLog.enabled)

  return (
    <div className="flex flex-col gap-4 px-4 lg:px-6">
      {error && (
        <div
          role="alert"
          className="border-destructive/50 bg-destructive/10 text-destructive rounded-md border px-3 py-2 text-sm"
        >
          {error}
        </div>
      )}

      {dropped > 0 && (
        <div className="flex items-start gap-3 rounded-md border border-amber-500/50 bg-amber-500/10 px-3 py-2 text-sm text-amber-700 dark:text-amber-400">
          <IconAlertTriangle className="mt-0.5 size-4 shrink-0" />
          <span>
            {count(dropped)} requests were not recorded because the log writer
            could not keep up. The proxy is never slowed down to write this
            log, so entries are dropped instead — what you see below is
            incomplete.
          </span>
        </div>
      )}

      <Card>
        <CardHeader>
          <CardTitle>Access log</CardTitle>
          <CardDescription>
            {loggingHosts.length === 0
              ? "No host is recording requests yet. Turn it on for a host under Hosts → Edit → Access log."
              : `Recording ${loggingHosts.map((h) => h.domains[0] ?? h.name).join(", ")}.`}
          </CardDescription>
          <CardAction>
            <Button variant="outline" size="sm" onClick={() => void load()} disabled={loading}>
              <IconRefresh />
              Refresh
            </Button>
          </CardAction>
        </CardHeader>

        <div className="flex flex-wrap items-end gap-3 px-6 pb-4">
          <div className="flex min-w-48 flex-1 flex-col gap-2">
            <Label htmlFor="log-search">Search</Label>
            <form
              className="relative"
              onSubmit={(e) => {
                e.preventDefault()
                setApplied(search.trim())
              }}
            >
              <IconSearch className="text-muted-foreground absolute top-2.5 left-2.5 size-4" />
              <Input
                id="log-search"
                className="pl-8"
                placeholder="Path or client address"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                onBlur={() => setApplied(search.trim())}
              />
            </form>
          </div>

          <div className="flex flex-col gap-2">
            <Label>Host</Label>
            <Select value={hostId} onValueChange={setHostId}>
              <SelectTrigger className="w-48">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">Every host</SelectItem>
                {hosts.map((h) => (
                  <SelectItem key={h.id} value={String(h.id)}>
                    {h.domains[0] ?? h.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div className="flex flex-col gap-2">
            <Label>Status</Label>
            <Select value={statusClass} onValueChange={setStatusClass}>
              <SelectTrigger className="w-36">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">Any</SelectItem>
                <SelectItem value="2">2xx success</SelectItem>
                <SelectItem value="3">3xx redirect</SelectItem>
                <SelectItem value="4">4xx refused</SelectItem>
                <SelectItem value="5">5xx failed</SelectItem>
              </SelectContent>
            </Select>
          </div>

          <div className="flex flex-col gap-2">
            <Label>Window</Label>
            <Select value={range} onValueChange={(v) => setRange(v as WindowKey)}>
              <SelectTrigger className="w-40">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {Object.entries(WINDOWS).map(([key, spec]) => (
                  <SelectItem key={key} value={key}>
                    {spec.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <Button
            variant={failedOnly ? "default" : "outline"}
            size="sm"
            onClick={() => setFailedOnly((v) => !v)}
          >
            Never reached an upstream
          </Button>
        </div>

        {entries.length === 0 ? (
          <div className="text-muted-foreground px-6 pb-8 text-center text-sm">
            <p className="text-foreground font-medium">
              {loading ? "Loading…" : "No requests match"}
            </p>
            {!loading && <p>Try a wider window, or clear the filters.</p>}
          </div>
        ) : (
          <>
            <div className="overflow-x-auto px-2 pb-2 sm:px-6">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="w-32">Time</TableHead>
                    <TableHead className="w-16">Method</TableHead>
                    <TableHead>Path</TableHead>
                    <TableHead className="w-16 text-right">Status</TableHead>
                    <TableHead className="w-20 text-right">Took</TableHead>
                    <TableHead className="w-20 text-right">Size</TableHead>
                    <TableHead className="w-32">Client</TableHead>
                    <TableHead className="w-40">Upstream</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {entries.map((e) => (
                    <TableRow key={e.id}>
                      <TableCell className="font-mono text-xs whitespace-nowrap">
                        {new Date(e.timestamp).toLocaleTimeString()}
                      </TableCell>
                      <TableCell className="font-mono text-xs">{e.method}</TableCell>
                      <TableCell className="max-w-md truncate font-mono text-xs">
                        {e.path}
                        {e.error && (
                          <div className="text-destructive text-xs">{e.error}</div>
                        )}
                      </TableCell>
                      <TableCell className="text-right">
                        <Badge variant={statusVariant(e.status)}>{e.status}</Badge>
                      </TableCell>
                      <TableCell className="text-right font-mono text-xs tabular-nums">
                        {millis(e.durationMs)} ms
                      </TableCell>
                      <TableCell className="text-right font-mono text-xs tabular-nums">
                        {bytes(e.bytesOut)}
                      </TableCell>
                      <TableCell className="font-mono text-xs">{e.clientIp}</TableCell>
                      <TableCell className="truncate font-mono text-xs">
                        {e.upstream || "—"}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>

            <div className="flex items-center gap-3 px-6 pb-6 text-sm">
              <span className="text-muted-foreground">
                {offset + 1}–{offset + entries.length} of {count(total)}
              </span>

              <div className="flex items-center gap-2">
                <Label htmlFor="log-page-size" className="text-muted-foreground">
                  Show
                </Label>
                <Select
                  value={String(pageSize)}
                  onValueChange={(v) => setPageSize(Number(v))}
                >
                  <SelectTrigger id="log-page-size" className="w-20" size="sm">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {PAGE_SIZES.map((n) => (
                      <SelectItem key={n} value={String(n)}>
                        {n}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>

              <div className="ml-auto flex gap-2">
                <Button
                  variant="outline"
                  size="sm"
                  disabled={offset === 0}
                  onClick={() => setOffset(Math.max(offset - pageSize, 0))}
                >
                  Newer
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={offset + entries.length >= total}
                  onClick={() => setOffset(offset + pageSize)}
                >
                  Older
                </Button>
              </div>
            </div>
          </>
        )}
      </Card>
    </div>
  )
}

function statusVariant(status: number): "outline" | "secondary" | "destructive" {
  if (status >= 500) return "destructive"
  if (status >= 400) return "secondary"
  return "outline"
}
