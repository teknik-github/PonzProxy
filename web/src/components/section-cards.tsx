import {
  IconAlertTriangle,
  IconArrowsExchange,
  IconClockBolt,
  IconPlugConnected,
} from "@tabler/icons-react"

import { Badge } from "@/components/ui/badge"
import {
  Card,
  CardAction,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { bytesPerSec, count, millis, percent, rate } from "@/format"
import type { Snapshot } from "@/api/types"

interface Props {
  snapshot: Snapshot | null
}

/** SectionCards is the masthead: the four numbers an operator checks first.
 *
 *  Only the error card changes colour, and only when something is actually
 *  wrong. If every card were tinted, none of them would read as an alarm. */
export function SectionCards({ snapshot }: Props) {
  const totals = snapshot?.totals
  const system = snapshot?.system

  const errorRate = totals?.errorRate ?? 0
  const failing = errorRate > 0.01
  const upstreamsDown =
    system !== undefined && system.upstreamsUp < system.upstreamsTotal

  return (
    <div className="grid grid-cols-1 gap-4 px-4 *:data-[slot=card]:bg-gradient-to-t *:data-[slot=card]:from-primary/5 *:data-[slot=card]:to-card *:data-[slot=card]:shadow-xs lg:px-6 @xl/main:grid-cols-2 @5xl/main:grid-cols-4 dark:*:data-[slot=card]:bg-card">
      <Card className="@container/card">
        <CardHeader>
          <CardDescription>Requests per second</CardDescription>
          <CardTitle className="text-2xl font-semibold tabular-nums @[250px]/card:text-3xl">
            {rate(totals?.requestsPerSec ?? 0)}
          </CardTitle>
          <CardAction>
            <Badge variant="outline">
              <IconArrowsExchange />
              live
            </Badge>
          </CardAction>
        </CardHeader>
        <CardFooter className="flex-col items-start gap-1.5 text-sm">
          <div className="line-clamp-1 flex gap-2 font-medium">
            {bytesPerSec(totals?.bytesOutPerSec ?? 0)} out
          </div>
          <div className="text-muted-foreground">
            {bytesPerSec(totals?.bytesInPerSec ?? 0)} in from clients
          </div>
        </CardFooter>
      </Card>

      <Card className="@container/card">
        <CardHeader>
          <CardDescription>Failing</CardDescription>
          <CardTitle
            className={`text-2xl font-semibold tabular-nums @[250px]/card:text-3xl ${
              failing ? "text-destructive" : ""
            }`}
          >
            {percent(errorRate)}%
          </CardTitle>
          <CardAction>
            <Badge variant={failing ? "destructive" : "outline"}>
              <IconAlertTriangle />
              {failing ? "attention" : "healthy"}
            </Badge>
          </CardAction>
        </CardHeader>
        <CardFooter className="flex-col items-start gap-1.5 text-sm">
          <div className="line-clamp-1 flex gap-2 font-medium">
            {failing
              ? "Requests are failing right now"
              : "No failures in this window"}
          </div>
          <div className="text-muted-foreground">
            Counts 5xx responses and requests that never reached an upstream
          </div>
        </CardFooter>
      </Card>

      <Card className="@container/card">
        <CardHeader>
          <CardDescription>Mean response</CardDescription>
          <CardTitle className="text-2xl font-semibold tabular-nums @[250px]/card:text-3xl">
            {millis(totals?.meanLatencyMs ?? 0)} ms
          </CardTitle>
          <CardAction>
            <Badge variant="outline">
              <IconClockBolt />
              end to end
            </Badge>
          </CardAction>
        </CardHeader>
        <CardFooter className="flex-col items-start gap-1.5 text-sm">
          <div className="line-clamp-1 flex gap-2 font-medium">
            Measured at the proxy
          </div>
          <div className="text-muted-foreground">
            Includes the time your upstream took to answer
          </div>
        </CardFooter>
      </Card>

      <Card className="@container/card">
        <CardHeader>
          <CardDescription>Open connections</CardDescription>
          <CardTitle className="text-2xl font-semibold tabular-nums @[250px]/card:text-3xl">
            {count(totals?.activeConns ?? 0)}
          </CardTitle>
          <CardAction>
            <Badge variant={upstreamsDown ? "destructive" : "outline"}>
              <IconPlugConnected />
              {system ? `${system.upstreamsUp}/${system.upstreamsTotal}` : "—"}
            </Badge>
          </CardAction>
        </CardHeader>
        <CardFooter className="flex-col items-start gap-1.5 text-sm">
          <div className="line-clamp-1 flex gap-2 font-medium">
            {upstreamsDown
              ? "An upstream is out of rotation"
              : "Every upstream is answering"}
          </div>
          <div className="text-muted-foreground">
            Requests in flight through the proxy
          </div>
        </CardFooter>
      </Card>
    </div>
  )
}
