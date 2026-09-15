import { useCallback, useEffect, useState } from "react"
import { IconPlus, IconSend, IconTrash } from "@tabler/icons-react"

import { api, ApiError } from "@/api/client"
import type {
  AlertChannel,
  AlertChannelInput,
  AlertEvent,
  AlertEventOption,
  AlertStats,
} from "@/api/types"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Separator } from "@/components/ui/separator"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { count, date } from "@/format"

interface Props {
  canEdit: boolean
}

export function Alerts({ canEdit }: Props) {
  const [channels, setChannels] = useState<AlertChannel[]>([])
  const [events, setEvents] = useState<AlertEventOption[]>([])
  const [stats, setStats] = useState<AlertStats | null>(null)
  const [editing, setEditing] = useState<AlertChannel | "new" | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [testing, setTesting] = useState<number | null>(null)
  const [testResult, setTestResult] = useState<string | null>(null)

  const refresh = useCallback(() => {
    Promise.all([api.listAlertChannels(), api.alertStats()])
      .then(([c, s]) => {
        setChannels(c)
        setStats(s)
      })
      .catch(() => setError("Could not load the alert channels."))
  }, [])

  useEffect(() => {
    refresh()
    api.listAlertEvents().then(setEvents).catch(() => setEvents([]))
  }, [refresh])

  async function test(channel: AlertChannel) {
    setTesting(channel.id)
    setTestResult(null)
    try {
      const result = await api.testAlertChannel(channel.id)
      setTestResult(
        result.delivered
          ? `Delivered to ${channel.name}. Check the destination.`
          : `${channel.name} did not accept it: ${result.error ?? "unknown error"}`,
      )
      refresh()
    } catch (err) {
      setTestResult(err instanceof ApiError ? err.message : "The test could not be sent.")
    } finally {
      setTesting(null)
    }
  }

  async function remove(channel: AlertChannel) {
    if (!confirm(`Delete ${channel.name}? It will stop receiving alerts.`)) return
    try {
      await api.deleteAlertChannel(channel.id)
      refresh()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Could not delete the channel.")
    }
  }

  return (
    <div className="flex flex-col gap-4 px-4 lg:px-6">
      {error && (
        <div
          role="alert"
          className="border-destructive/50 bg-destructive/10 text-destructive flex items-center gap-3 rounded-md border px-3 py-2 text-sm"
        >
          {error}
          <Button variant="ghost" size="sm" className="ml-auto" onClick={() => setError(null)}>
            Dismiss
          </Button>
        </div>
      )}

      {testResult && (
        <div className="bg-muted flex items-center gap-3 rounded-md border px-3 py-2 text-sm">
          {testResult}
          <Button variant="ghost" size="sm" className="ml-auto" onClick={() => setTestResult(null)}>
            Dismiss
          </Button>
        </div>
      )}

      {stats && stats.dropped > 0 && (
        <div className="rounded-md border border-amber-500/50 bg-amber-500/10 px-3 py-2 text-sm text-amber-700 dark:text-amber-400">
          {count(stats.dropped)} alerts were dropped because delivery could not
          keep up. Nothing in the proxy waits for a webhook, so alerts are
          discarded rather than slowing it down.
        </div>
      )}

      <Card>
        <CardHeader>
          <CardTitle>Alert channels</CardTitle>
          <CardDescription>
            Each channel is a webhook and the events it wants. Repeats about the
            same thing are suppressed, so a flapping backend does not turn into
            a hundred messages.
          </CardDescription>
          {canEdit && (
            <CardAction>
              <Button size="sm" onClick={() => setEditing("new")}>
                <IconPlus />
                Add channel
              </Button>
            </CardAction>
          )}
        </CardHeader>

        {channels.length === 0 ? (
          <div className="text-muted-foreground px-6 pb-8 text-center text-sm">
            <p className="text-foreground font-medium">No channels yet</p>
            <p>
              Add a webhook — most chat systems and on-call tools accept one —
              to be told when an upstream drops out or a certificate is about
              to expire.
            </p>
          </div>
        ) : (
          <div className="overflow-x-auto px-2 pb-2 sm:px-6 sm:pb-6">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Destination</TableHead>
                  <TableHead>Events</TableHead>
                  <TableHead>Repeat after</TableHead>
                  <TableHead>Last delivery</TableHead>
                  {canEdit && <TableHead />}
                </TableRow>
              </TableHeader>
              <TableBody>
                {channels.map((c) => (
                  <TableRow key={c.id}>
                    <TableCell>
                      {c.name}
                      {!c.enabled && (
                        <Badge variant="secondary" className="ml-2">
                          paused
                        </Badge>
                      )}
                    </TableCell>
                    <TableCell className="font-mono text-xs">{c.url}</TableCell>
                    <TableCell className="text-xs">
                      {(c.events ?? []).length} of {events.length}
                    </TableCell>
                    <TableCell className="font-mono text-xs tabular-nums">
                      {Math.round(c.minIntervalSeconds / 60)} min
                    </TableCell>
                    <TableCell className="text-xs">
                      {c.lastAttempt ? (
                        c.lastError ? (
                          <span className="text-destructive">{c.lastError}</span>
                        ) : (
                          <span className="text-muted-foreground">
                            delivered {date(c.lastAttempt)}
                          </span>
                        )
                      ) : (
                        <span className="text-muted-foreground">never used</span>
                      )}
                    </TableCell>
                    {canEdit && (
                      <TableCell className="text-right whitespace-nowrap">
                        <Button
                          variant="ghost"
                          size="sm"
                          disabled={testing === c.id}
                          onClick={() => void test(c)}
                        >
                          <IconSend />
                          {testing === c.id ? "Sending…" : "Test"}
                        </Button>
                        <Button variant="ghost" size="sm" onClick={() => setEditing(c)}>
                          Edit
                        </Button>
                        <Button variant="ghost" size="sm" onClick={() => void remove(c)}>
                          <IconTrash />
                        </Button>
                      </TableCell>
                    )}
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </Card>

      {editing && (
        <ChannelSheet
          channel={editing === "new" ? null : editing}
          events={events}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            refresh()
          }}
        />
      )}
    </div>
  )
}

function ChannelSheet({
  channel,
  events,
  onClose,
  onSaved,
}: {
  channel: AlertChannel | null
  events: AlertEventOption[]
  onClose: () => void
  onSaved: () => void
}) {
  const [name, setName] = useState(channel?.name ?? "")
  const [url, setUrl] = useState("")
  const [enabled, setEnabled] = useState(channel?.enabled ?? true)
  const [selected, setSelected] = useState<AlertEvent[]>(channel?.events ?? [])
  const [minutes, setMinutes] = useState(
    channel ? Math.round(channel.minIntervalSeconds / 60) : 5,
  )
  const [failure, setFailure] = useState<ApiError | null>(null)
  const [busy, setBusy] = useState(false)

  const toggle = (event: AlertEvent, on: boolean) =>
    setSelected((prev) => (on ? [...prev, event] : prev.filter((e) => e !== event)))

  async function save() {
    setBusy(true)
    setFailure(null)
    const input: AlertChannelInput = {
      name,
      type: "webhook",
      enabled,
      url,
      events: selected,
      minIntervalSeconds: minutes * 60,
    }
    try {
      if (channel) await api.updateAlertChannel(channel.id, input)
      else await api.createAlertChannel(input)
      onSaved()
    } catch (err) {
      setFailure(
        err instanceof ApiError ? err : new ApiError(0, "Could not save the channel."),
      )
      setBusy(false)
    }
  }

  const fieldError = (n: string) => failure?.fieldMessage(n)

  return (
    <Sheet open onOpenChange={(open) => !open && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-lg">
        <SheetHeader>
          <SheetTitle>{channel ? `Edit ${channel.name}` : "Add alert channel"}</SheetTitle>
          <SheetDescription>
            ponzproxy posts a JSON document. Most chat systems accept one
            directly; anything else needs a few lines of script.
          </SheetDescription>
        </SheetHeader>

        <div className="flex flex-col gap-5 px-4">
          {failure && failure.fields.length === 0 && (
            <div className="border-destructive/50 bg-destructive/10 text-destructive rounded-md border px-3 py-2 text-sm">
              {failure.message}
            </div>
          )}

          <div className="flex flex-col gap-2">
            <Label htmlFor="channel-name">Name</Label>
            <Input
              id="channel-name"
              placeholder="Ops chat"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
            {fieldError("name") && (
              <span className="text-destructive text-xs">{fieldError("name")}</span>
            )}
          </div>

          <div className="flex flex-col gap-2">
            <Label htmlFor="channel-url">Webhook URL</Label>
            <Input
              id="channel-url"
              type="password"
              autoComplete="off"
              className="font-mono"
              placeholder={channel ? "Leave blank to keep the current URL" : "https://…"}
              value={url}
              onChange={(e) => setUrl(e.target.value)}
            />
            <span className="text-muted-foreground text-xs">
              {channel
                ? "The stored URL is never shown. Leave this blank to keep it."
                : "Treated as a secret: it is never shown again once saved."}
            </span>
            {fieldError("url") && (
              <span className="text-destructive text-xs">{fieldError("url")}</span>
            )}
          </div>

          <Separator />

          <div className="flex flex-col gap-3">
            <div>
              <h3 className="text-sm font-medium">Tell me about</h3>
              {fieldError("events") && (
                <p className="text-destructive text-xs">{fieldError("events")}</p>
              )}
            </div>
            {events.map((e) => (
              <label key={e.value} className="flex items-start gap-3 text-sm">
                <Checkbox
                  className="mt-0.5"
                  checked={selected.includes(e.value)}
                  onCheckedChange={(v) => toggle(e.value, v === true)}
                />
                <span>
                  <span className="font-medium">{e.label}</span>
                  {e.severity === "critical" && (
                    <Badge variant="destructive" className="ml-2">
                      critical
                    </Badge>
                  )}
                  <span className="text-muted-foreground block text-xs">
                    {e.description}
                  </span>
                </span>
              </label>
            ))}
          </div>

          <Separator />

          <div className="flex flex-col gap-2">
            <Label htmlFor="channel-interval">Repeat after (minutes)</Label>
            <Input
              id="channel-interval"
              type="number"
              min={1}
              max={1440}
              value={minutes}
              onChange={(e) => setMinutes(Number(e.target.value))}
            />
            <span className="text-muted-foreground text-xs">
              The same event about the same backend is suppressed for this long.
              Different backends still alert independently, so one noisy server
              cannot hide another.
            </span>
            {fieldError("minInterval") && (
              <span className="text-destructive text-xs">{fieldError("minInterval")}</span>
            )}
          </div>

          <label className="flex items-center gap-2 text-sm">
            <Checkbox
              checked={enabled}
              onCheckedChange={(v) => setEnabled(v === true)}
            />
            Send alerts to this channel
          </label>
        </div>

        <SheetFooter>
          <Button disabled={busy} onClick={() => void save()}>
            {busy ? "Saving…" : channel ? "Save changes" : "Add channel"}
          </Button>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
