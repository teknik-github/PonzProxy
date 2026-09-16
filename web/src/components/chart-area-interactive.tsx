import * as React from "react"
import { Area, AreaChart, CartesianGrid, XAxis, YAxis } from "recharts"

import {
  Card,
  CardAction,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@/components/ui/chart"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  ToggleGroup,
  ToggleGroupItem,
} from "@/components/ui/toggle-group"
import { useIsMobile } from "@/hooks/use-mobile"
import { api } from "@/api/client"
import type { Resolution, SeriesPoint } from "@/api/types"

/** The windows an operator actually asks for, each with the bucket size that
 *  keeps the point count sane. */
const WINDOWS = {
  "1h": { label: "Last hour", hours: 1, resolution: "minute" as Resolution },
  "24h": { label: "Last 24 hours", hours: 24, resolution: "hour" as Resolution },
  "30d": { label: "Last 30 days", hours: 24 * 30, resolution: "day" as Resolution },
} satisfies Record<string, { label: string; hours: number; resolution: Resolution }>

type WindowKey = keyof typeof WINDOWS

const chartConfig = {
  served: { label: "Served/s", color: "var(--chart-2)" },
  failed: { label: "Failed/s", color: "var(--destructive)" },
} satisfies ChartConfig

interface Row {
  time: string
  /** Requests per second, not the bucket's raw count — see toRows. */
  served: number
  failed: number
}

/** ChartAreaInteractive plots request rate over the selected window.
 *
 *  Failures are stacked under the served rate rather than drawn on a separate
 *  chart: the question is always "how much of that traffic failed", and two
 *  charts would force an operator to line up timestamps by eye.
 *
 *  It plots a rate rather than each bucket's raw count, and leaves the newest
 *  bucket out entirely.
 *
 *  Both for the same reason. The newest bucket is always still filling: ten
 *  seconds into a minute it holds a sixth of a minute's traffic, so a chart of
 *  counts ended in a cliff that looked like the site had just died — every
 *  minute, on every window. Plotting a rate fixes the units; leaving the
 *  unfinished bucket out fixes the rest, because how much of it has actually
 *  reached the database depends on flush timing and any figure drawn there
 *  would be part guesswork. The live number above the chart answers "now". */
export function ChartAreaInteractive() {
  const isMobile = useIsMobile()
  const [range, setRange] = React.useState<WindowKey>("1h")
  const [rows, setRows] = React.useState<Row[]>([])
  const [error, setError] = React.useState<string | null>(null)

  React.useEffect(() => {
    if (isMobile) setRange("1h")
  }, [isMobile])

  const load = React.useCallback(async () => {
    const spec = WINDOWS[range]
    const to = new Date()
    const from = new Date(to.getTime() - spec.hours * 3_600_000)
    try {
      const series = await api.history({ from, to, resolution: spec.resolution })
      setRows(toRows(series.points ?? [], spec.resolution, from, to))
      setError(null)
    } catch {
      setError("Could not load history.")
    }
  }, [range])

  React.useEffect(() => {
    void load()
    // History is not a live feed. Refreshing once a minute keeps it current
    // without redrawing the chart under the operator's cursor.
    const timer = window.setInterval(() => void load(), 60_000)
    return () => window.clearInterval(timer)
  }, [load])

  return (
    <Card className="@container/card">
      <CardHeader>
        <CardTitle>Requests over time</CardTitle>
        <CardDescription>
          <span className="hidden @[540px]/card:block">
            Requests per second, with failures stacked underneath. The
            current bucket is still filling and is not drawn.
          </span>
          <span className="@[540px]/card:hidden">Requests/s and failures</span>
        </CardDescription>
        <CardAction>
          <ToggleGroup
            type="single"
            value={range}
            onValueChange={(value) => value && setRange(value as WindowKey)}
            variant="outline"
            className="hidden *:data-[slot=toggle-group-item]:!px-4 @[767px]/card:flex"
          >
            <ToggleGroupItem value="30d">Last 30 days</ToggleGroupItem>
            <ToggleGroupItem value="24h">Last 24 hours</ToggleGroupItem>
            <ToggleGroupItem value="1h">Last hour</ToggleGroupItem>
          </ToggleGroup>
          <Select value={range} onValueChange={(v) => setRange(v as WindowKey)}>
            <SelectTrigger
              className="flex w-40 **:data-[slot=select-value]:block **:data-[slot=select-value]:truncate @[767px]/card:hidden"
              size="sm"
              aria-label="Select a time range"
            >
              <SelectValue placeholder="Last hour" />
            </SelectTrigger>
            <SelectContent className="rounded-xl">
              {Object.entries(WINDOWS).map(([key, spec]) => (
                <SelectItem key={key} value={key} className="rounded-lg">
                  {spec.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </CardAction>
      </CardHeader>
      <div className="px-2 pt-4 sm:px-6 sm:pt-6">
        {error ? (
          <p className="text-destructive py-12 text-center text-sm">{error}</p>
        ) : rows.length === 0 ? (
          <div className="text-muted-foreground py-12 text-center text-sm">
            <p className="text-foreground font-medium">No traffic recorded yet</p>
            <p>Samples are written every few seconds once requests arrive.</p>
          </div>
        ) : (
          <ChartContainer
            config={chartConfig}
            className="aspect-auto h-[250px] w-full"
          >
            <AreaChart data={rows}>
              <defs>
                <linearGradient id="fillServed" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="5%" stopColor="var(--color-served)" stopOpacity={0.9} />
                  <stop offset="95%" stopColor="var(--color-served)" stopOpacity={0.1} />
                </linearGradient>
                <linearGradient id="fillFailed" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="5%" stopColor="var(--color-failed)" stopOpacity={0.9} />
                  <stop offset="95%" stopColor="var(--color-failed)" stopOpacity={0.1} />
                </linearGradient>
              </defs>
              <CartesianGrid vertical={false} />
              <XAxis
                dataKey="time"
                tickLine={false}
                axisLine={false}
                tickMargin={8}
                minTickGap={32}
              />
              <YAxis
                tickLine={false}
                axisLine={false}
                tickMargin={8}
                width={44}
                // A rate is often below one on a quiet host, where whole
                // numbers would collapse the whole axis onto zero.
                tickFormatter={(v: number) =>
                  v >= 10 || v === 0 ? String(Math.round(v)) : v.toFixed(1)
                }
              />
              <ChartTooltip
                cursor={false}
                content={<ChartTooltipContent indicator="dot" />}
              />
              {/* Animation is off deliberately. This chart reloads every
                  minute, and replaying a 1.5s grow-from-zero on each refresh
                  leaves an operator staring at an empty panel a sixtieth of
                  the time — and hides the data outright on any resize. */}
              <Area
                dataKey="failed"
                type="natural"
                fill="url(#fillFailed)"
                stroke="var(--color-failed)"
                stackId="a"
                isAnimationActive={false}
              />
              <Area
                dataKey="served"
                type="natural"
                fill="url(#fillServed)"
                stroke="var(--color-served)"
                stackId="a"
                isAnimationActive={false}
              />
            </AreaChart>
          </ChartContainer>
        )}
      </div>
    </Card>
  )
}

/** toRows turns the server's samples into one row per bucket across the whole
 *  window, including the buckets with no traffic. Values are rates, so the
 *  still-filling last bucket is comparable with the finished ones.
 *
 *  The server only stores a sample for an interval that actually saw requests,
 *  which keeps a month of history small. Plotted directly that leaves gaps,
 *  and an area chart draws gaps as disconnected dots — so a mostly idle proxy
 *  looks like broken data rather than a quiet one. Filling them with zero is
 *  both continuous and true: no sample means no traffic. */
function toRows(
  points: SeriesPoint[],
  resolution: Resolution,
  from: Date,
  to: Date,
): Row[] {
  const format: Intl.DateTimeFormatOptions =
    resolution === "day"
      ? { month: "short", day: "numeric" }
      : { hour: "2-digit", minute: "2-digit" }

  const step = BUCKET_MS[resolution]

  // Buckets are aligned to absolute time on the server, so the same rounding
  // has to be applied here or every sample would land between two slots.
  const served = new Map<number, { served: number; failed: number }>()
  // The newest bucket is still filling, and how much traffic it already holds
  // depends on where the sample flush happened to land inside it. There is no
  // figure to plot there that is not partly guesswork, so it is left out and
  // the chart ends at the last bucket that is actually complete. The live
  // figure above the chart is what answers "right now".
  let newest = -Infinity
  for (const p of points) {
    if (p.partial) continue
    const bucket = Math.floor(new Date(p.timestamp).getTime() / step) * step
    const failed = p.status5xx + p.statusError
    // The failure share is applied to the rate so the two areas stack to the
    // total rather than to the raw counts.
    const share = p.requests > 0 ? failed / p.requests : 0
    served.set(bucket, {
      served: p.requestsPerSec * (1 - share),
      failed: p.requestsPerSec * share,
    })
    if (bucket > newest) newest = bucket
  }

  const rows: Row[] = []
  const start = Math.floor(from.getTime() / step) * step
  // Stop one bucket short of now, so the gap-filling below does not put a
  // zero where the excluded partial bucket was — which would draw the very
  // cliff this avoids.
  const end = Math.floor(to.getTime() / step) * step - step
  for (let t = start; t <= end; t += step) {
    const found = served.get(t)
    rows.push({
      time: new Date(t).toLocaleString(undefined, format),
      served: found?.served ?? 0,
      failed: found?.failed ?? 0,
    })
  }
  return rows
}

const BUCKET_MS: Record<Resolution, number> = {
  minute: 60_000,
  hour: 3_600_000,
  day: 86_400_000,
}
