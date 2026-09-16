import { useCallback, useEffect, useState } from "react"
import { IconDownload, IconTrash } from "@tabler/icons-react"

import { api } from "@/api/client"
import type { BackupSnapshot, BackupStatus } from "@/api/types"
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
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { bytes } from "@/pages/TrafficLimits"

interface Props {
  canEdit: boolean
}

/** Backups is the screen that exists so an operator can see whether backups
 *  are actually happening, rather than assuming they are — which is how most
 *  people find out their backups stopped a year ago.
 *
 *  Its second job is to be honest about what a local snapshot is worth. It
 *  survives a deleted host and a corrupted database; it does not survive the
 *  disk it sits on. Only the download leaves the machine. */
export function Backups({ canEdit }: Props) {
  const [status, setStatus] = useState<BackupStatus | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState<string | null>(null)

  const load = useCallback(() => {
    api
      .backupStatus()
      .then((s) => {
        setStatus(s)
        setError(null)
      })
      .catch(() => setError("Could not read the backup status."))
  }, [])

  useEffect(load, [load])

  const run = (key: string, action: () => Promise<unknown>, after?: () => void) => {
    setBusy(key)
    action()
      .then(() => {
        setError(null)
        after?.()
      })
      .catch(() => setError("That did not work. Check the server log."))
      .finally(() => setBusy(null))
  }

  const snapshots: BackupSnapshot[] = status?.snapshots ?? []
  const scheduled = (status?.everySeconds ?? 0) > 0

  return (
    <div className="flex flex-col gap-4 px-4 lg:px-6">
      <Card>
        <CardHeader>
          <CardTitle>Backups</CardTitle>
          <CardDescription>
            One archive holds everything this proxy cannot rebuild: the
            database, every certificate, the Let's Encrypt account key and the
            session secret. Configuration can be retyped — certificates cannot,
            because Let's Encrypt allows five per domain per week.
          </CardDescription>
          <CardAction className="flex flex-wrap items-center gap-2">
            {canEdit && (
              <>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={busy !== null}
                  onClick={() => run("new", () => api.createBackup(), load)}
                >
                  {busy === "new" ? "Writing…" : "Take a snapshot"}
                </Button>
                <Button
                  size="sm"
                  disabled={busy !== null}
                  onClick={() => run("download", () => api.downloadBackup())}
                >
                  <IconDownload />
                  {busy === "download" ? "Building…" : "Download now"}
                </Button>
              </>
            )}
          </CardAction>
        </CardHeader>

        <CardContent className="flex flex-col gap-4">
          {error && <p className="text-destructive text-sm">{error}</p>}

          <div className="grid gap-3 text-sm sm:grid-cols-3">
            <Fact label="Schedule">
              {scheduled ? `every ${hours(status!.everySeconds)}` : "off"}
            </Fact>
            <Fact label="Kept">{status ? `${status.keep} newest` : "—"}</Fact>
            <Fact label="On disk">{snapshots.length}</Fact>
          </div>

          {/* The single most important sentence on this page. A scheduled
              snapshot that someone mistakes for off-site protection is worse
              than no snapshot, because it buys false confidence. */}
          <p className="text-muted-foreground border-l-2 border-amber-500 pl-3 text-xs">
            Snapshots are written next to the data they copy. They protect you
            from a deleted host, a bad edit and a corrupted database — not from
            losing this machine. Use <strong>Download now</strong>, or copy the
            files off the server, for that.
          </p>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Snapshots on disk</CardTitle>
          <CardDescription className="font-mono text-xs">
            {status?.directory ?? ""}
          </CardDescription>
        </CardHeader>
        <CardContent className="px-0">
          {snapshots.length === 0 ? (
            <p className="text-muted-foreground px-6 py-8 text-center text-sm">
              {scheduled
                ? "None yet. The first is written shortly after the proxy starts."
                : "Scheduled snapshots are off. Set PONZ_BACKUP_EVERY to switch them on."}
            </p>
          ) : (
            <div className="overflow-x-auto">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>File</TableHead>
                    <TableHead>Taken</TableHead>
                    <TableHead className="text-right">Size</TableHead>
                    {canEdit && <TableHead />}
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {snapshots.map((s) => (
                    <TableRow key={s.name}>
                      <TableCell className="font-mono text-xs">{s.name}</TableCell>
                      <TableCell className="text-sm">
                        {new Date(s.createdAt).toLocaleString()}
                      </TableCell>
                      <TableCell className="text-right font-mono text-xs tabular-nums">
                        {bytes(s.bytes)}
                      </TableCell>
                      {canEdit && (
                        <TableCell className="text-right">
                          <Button
                            variant="ghost"
                            size="sm"
                            disabled={busy !== null}
                            onClick={() => run(s.name, () => api.downloadBackupFile(s.name))}
                          >
                            <IconDownload />
                          </Button>
                          <Button
                            variant="ghost"
                            size="sm"
                            disabled={busy !== null}
                            onClick={() => run(s.name, () => api.deleteBackup(s.name), load)}
                          >
                            <IconTrash className="text-destructive" />
                          </Button>
                        </TableCell>
                      )}
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Restoring</CardTitle>
          <CardDescription>
            Restoring is a command, not a button. A running proxy holds the
            database open, so replacing it from inside the console would leave
            the process serving from a file that no longer exists.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <pre className="bg-muted overflow-x-auto rounded-md p-3 font-mono text-xs">
{`# stop the proxy first
docker compose down                       # or: systemctl stop ponzproxy

ponzproxy --restore ponzproxy-20260916-120000.tar.gz

docker compose up -d                      # or: systemctl start ponzproxy`}
          </pre>
          <p className="text-muted-foreground text-xs">
            Nothing is deleted: the existing data directory is renamed with a
            timestamp and left for you to remove once you are satisfied. The
            same binary also writes one — <code>ponzproxy --backup out.tar.gz</code>{" "}
            — which is what to put in a cron job that copies the result somewhere
            else.
          </p>
          <p className="text-muted-foreground text-xs">
            Treat an archive exactly as you would the server itself. It contains
            every private key this proxy holds, so whoever has one can
            impersonate every site it serves.
          </p>
        </CardContent>
      </Card>
    </div>
  )
}

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="text-muted-foreground text-xs">{label}</div>
      <div className="font-mono text-sm tabular-nums">{children}</div>
    </div>
  )
}

function hours(seconds: number): string {
  if (seconds % 86400 === 0) {
    const d = seconds / 86400
    return d === 1 ? "24 hours" : `${d} days`
  }
  if (seconds % 3600 === 0) return `${seconds / 3600} hours`
  return `${Math.round(seconds / 60)} minutes`
}
