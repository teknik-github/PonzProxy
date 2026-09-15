import { useCallback, useEffect, useState } from "react"
import { IconPlus } from "@tabler/icons-react"

import { ApiError, session } from "@/api/client"
import type { Certificate } from "@/api/types"
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
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

/* ----------------------------------------------------------------- api -- */
/* These mirror the Go API the same way @/api/types does. They live here for
 * now so the redirect screen is one file; moving them into @/api/types and
 * @/api/client alongside the host equivalents is a straight lift. */

/** RedirectStatus is the set of codes the server accepts. */
export type RedirectStatus = 301 | 302 | 307 | 308

/** Redirect is one redirection host as the API returns it. */
export interface Redirect {
  id: number
  name: string
  enabled: boolean
  domains: string[]
  target: string
  statusCode: RedirectStatus
  preservePath: boolean
  certificateId: number | null
  createdAt: string
  updatedAt: string
}

/** RedirectInput is the write shape; the server owns everything else. */
export interface RedirectInput {
  name: string
  enabled: boolean
  domains: string[]
  target: string
  statusCode: RedirectStatus
  preservePath: boolean
  certificateId: number | null
}

/** RedirectStatusOption is one entry of the picker the server describes. */
export interface RedirectStatusOption {
  value: RedirectStatus
  label: string
  description: string
}

async function requestJson<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  const token = session.get()
  if (token) headers.set("Authorization", `Bearer ${token}`)
  if (init.body !== undefined) headers.set("Content-Type", "application/json")

  let response: Response
  try {
    response = await fetch(path, { ...init, headers })
  } catch {
    throw new ApiError(0, "Cannot reach ponzproxy. Check that the service is running.")
  }

  if (response.status === 204) return undefined as T

  const text = await response.text()
  let body: unknown = null
  try {
    body = text ? JSON.parse(text) : null
  } catch {
    body = null
  }

  if (!response.ok) {
    const shaped = body as { error?: string; fields?: ApiError["fields"] } | null
    throw new ApiError(
      response.status,
      shaped?.error ?? `Request failed with status ${response.status}.`,
      shaped?.fields ?? [],
    )
  }
  return body as T
}

const redirectApi = {
  list: () => requestJson<Redirect[]>("/api/redirects"),
  statuses: () => requestJson<RedirectStatusOption[]>("/api/redirect-statuses"),
  create: (input: RedirectInput) =>
    requestJson<Redirect>("/api/redirects", {
      method: "POST",
      body: JSON.stringify(input),
    }),
  update: (id: number, input: RedirectInput) =>
    requestJson<Redirect>(`/api/redirects/${id}`, {
      method: "PUT",
      body: JSON.stringify(input),
    }),
  remove: (id: number) =>
    requestJson<void>(`/api/redirects/${id}`, { method: "DELETE" }),
}

/* --------------------------------------------------------------- screen -- */

interface Props {
  certificates: Certificate[]
  canEdit: boolean
  /** Called after a change, so the shell can refresh anything that counts
   *  routed domains. The list on this screen reloads itself either way. */
  onChanged?: () => void
}

export function Redirects({ certificates, canEdit, onChanged }: Props) {
  const [redirects, setRedirects] = useState<Redirect[]>([])
  const [statuses, setStatuses] = useState<RedirectStatusOption[]>([])
  const [editing, setEditing] = useState<Redirect | "new" | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)

  const reload = useCallback(async () => {
    try {
      setRedirects(await redirectApi.list())
      setError(null)
    } catch (err) {
      setError(
        err instanceof ApiError ? err.message : "Could not load the redirects.",
      )
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void reload()
  }, [reload])

  useEffect(() => {
    redirectApi.statuses().then(setStatuses).catch(() => setStatuses([]))
  }, [])

  async function remove(redirect: Redirect) {
    if (!confirm(`Stop redirecting ${redirect.domains.join(", ")}?`)) return
    try {
      await redirectApi.remove(redirect.id)
      await reload()
      onChanged?.()
    } catch (err) {
      setError(
        err instanceof ApiError ? err.message : "Could not delete the redirect.",
      )
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
          <Button
            variant="ghost"
            size="sm"
            className="ml-auto"
            onClick={() => setError(null)}
          >
            Dismiss
          </Button>
        </div>
      )}

      <Card>
        <CardHeader>
          <CardTitle>Redirects</CardTitle>
          <CardDescription>
            A redirect answers a domain with a Location header instead of
            proxying it. A domain belongs either to a host or to a redirect,
            never to both.
          </CardDescription>
          {canEdit && (
            <CardAction>
              <Button size="sm" onClick={() => setEditing("new")}>
                <IconPlus />
                Add redirect
              </Button>
            </CardAction>
          )}
        </CardHeader>

        {loading ? (
          <div className="text-muted-foreground px-6 pb-8 text-center text-sm">
            Loading…
          </div>
        ) : redirects.length === 0 ? (
          <div className="text-muted-foreground px-6 pb-8 text-center text-sm">
            <p className="text-foreground font-medium">No redirects yet</p>
            <p>Add one to send a retired domain somewhere else.</p>
          </div>
        ) : (
          <div className="overflow-x-auto px-2 pb-2 sm:px-6 sm:pb-6">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Domains</TableHead>
                  <TableHead>Target</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Path</TableHead>
                  <TableHead>TLS</TableHead>
                  <TableHead>State</TableHead>
                  {canEdit && <TableHead />}
                </TableRow>
              </TableHeader>
              <TableBody>
                {redirects.map((redirect) => {
                  const cert = certificates.find(
                    (c) => c.id === redirect.certificateId,
                  )
                  return (
                    <TableRow key={redirect.id}>
                      <TableCell>
                        <span className="font-mono text-xs">
                          {redirect.domains.join(", ")}
                        </span>
                        <div className="text-muted-foreground text-xs">
                          {redirect.name}
                        </div>
                      </TableCell>
                      <TableCell className="font-mono text-xs">
                        {redirect.target}
                      </TableCell>
                      <TableCell className="font-mono text-xs tabular-nums">
                        {redirect.statusCode}
                      </TableCell>
                      <TableCell className="text-xs">
                        {redirect.preservePath ? "kept" : "dropped"}
                      </TableCell>
                      <TableCell className="text-xs">
                        {cert ? (
                          <span className="font-mono">{cert.name}</span>
                        ) : (
                          <span className="text-muted-foreground">none</span>
                        )}
                      </TableCell>
                      <TableCell>
                        <Badge variant={redirect.enabled ? "outline" : "secondary"}>
                          {redirect.enabled ? "answering" : "paused"}
                        </Badge>
                      </TableCell>
                      {canEdit && (
                        <TableCell className="text-right whitespace-nowrap">
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => setEditing(redirect)}
                          >
                            Edit
                          </Button>
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => void remove(redirect)}
                          >
                            Delete
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
      </Card>

      {editing && (
        <RedirectSheet
          redirect={editing === "new" ? null : editing}
          statuses={statuses}
          certificates={certificates}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            void reload()
            onChanged?.()
          }}
        />
      )}
    </div>
  )
}

const DEFAULT_STATUS: RedirectStatus = 301

function toInput(redirect: Redirect | null): RedirectInput {
  if (!redirect) {
    return {
      name: "",
      enabled: true,
      domains: [],
      target: "",
      statusCode: DEFAULT_STATUS,
      preservePath: true,
      certificateId: null,
    }
  }
  return {
    name: redirect.name,
    enabled: redirect.enabled,
    domains: redirect.domains,
    target: redirect.target,
    statusCode: redirect.statusCode,
    preservePath: redirect.preservePath,
    certificateId: redirect.certificateId,
  }
}

function RedirectSheet({
  redirect,
  statuses,
  certificates,
  onClose,
  onSaved,
}: {
  redirect: Redirect | null
  statuses: RedirectStatusOption[]
  certificates: Certificate[]
  onClose: () => void
  onSaved: () => void
}) {
  const [input, setInput] = useState<RedirectInput>(() => toInput(redirect))
  const [domainText, setDomainText] = useState(() =>
    (redirect?.domains ?? []).join("\n"),
  )
  const [failure, setFailure] = useState<ApiError | null>(null)
  const [busy, setBusy] = useState(false)

  const patch = (fields: Partial<RedirectInput>) =>
    setInput((prev) => ({ ...prev, ...fields }))
  const fieldError = (name: string) => failure?.fieldMessage(name)
  const chosen = statuses.find((s) => s.value === input.statusCode)

  async function save() {
    setBusy(true)
    setFailure(null)
    const payload: RedirectInput = {
      ...input,
      domains: domainText
        .split(/[\n,]/)
        .map((d) => d.trim())
        .filter(Boolean),
    }
    try {
      if (redirect) await redirectApi.update(redirect.id, payload)
      else await redirectApi.create(payload)
      onSaved()
    } catch (err) {
      setFailure(
        err instanceof ApiError
          ? err
          : new ApiError(0, "Could not save the redirect."),
      )
      setBusy(false)
    }
  }

  return (
    <Sheet open onOpenChange={(open) => !open && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>
            {redirect ? `Edit ${redirect.name}` : "Add redirect"}
          </SheetTitle>
          <SheetDescription>
            Changes take effect as soon as you save. In-flight requests keep the
            configuration they started with.
          </SheetDescription>
        </SheetHeader>

        <div className="flex flex-col gap-6 px-4">
          {failure && failure.fields.length === 0 && (
            <div className="border-destructive/50 bg-destructive/10 text-destructive rounded-md border px-3 py-2 text-sm">
              {failure.message}
            </div>
          )}

          <FormField
            label="Name"
            hint="For your own reference in this console."
            error={fieldError("name")}
          >
            <Input
              value={input.name}
              onChange={(e) => patch({ name: e.target.value })}
            />
          </FormField>

          <FormField
            label="Domains"
            hint="One per line. Use *.example.com to match any single subdomain. A domain already used by a host will be refused."
            error={fieldError("domains") ?? fieldError("domains[0]")}
          >
            <textarea
              className="border-input bg-transparent placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-ring/50 min-h-20 w-full rounded-md border px-3 py-2 font-mono text-sm shadow-xs focus-visible:ring-[3px] focus-visible:outline-none"
              placeholder="old.example.com"
              value={domainText}
              onChange={(e) => setDomainText(e.target.value)}
            />
          </FormField>

          <Separator />

          <FormField
            label="Send visitors to"
            hint="A URL or a domain. Without a scheme, https is assumed."
            error={fieldError("target")}
          >
            <Input
              className="font-mono"
              placeholder="https://new.example.com"
              value={input.target}
              onChange={(e) => patch({ target: e.target.value })}
            />
          </FormField>

          <FormField
            label="Status code"
            hint={
              chosen?.description ??
              "307 and 308 keep the method and body; 301 and 302 may turn a POST into a GET."
            }
            error={fieldError("statusCode")}
          >
            <Select
              value={String(input.statusCode)}
              onValueChange={(v) =>
                patch({ statusCode: Number(v) as RedirectStatus })
              }
            >
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {(statuses.length > 0
                  ? statuses
                  : ([301, 302, 307, 308] as RedirectStatus[]).map((value) => ({
                      value,
                      label: String(value),
                      description: "",
                    }))
                ).map((s) => (
                  <SelectItem key={s.value} value={String(s.value)}>
                    {s.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </FormField>

          <CheckField
            label="Keep the path and query string"
            checked={input.preservePath}
            onChange={(v) => patch({ preservePath: v })}
          />
          <p className="text-muted-foreground -mt-4 text-xs">
            {input.preservePath
              ? "/a/b?c=d on the old domain lands on /a/b?c=d at the target."
              : "Every request lands on the target itself."}
          </p>

          <Separator />

          <FormField
            label="Certificate"
            hint="Needed to answer these domains over HTTPS. Without one they are only answered on port 80."
            error={fieldError("certificateId")}
          >
            <Select
              value={
                input.certificateId === null ? "none" : String(input.certificateId)
              }
              onValueChange={(v) =>
                patch({ certificateId: v === "none" ? null : Number(v) })
              }
            >
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="none">Answer over HTTP only</SelectItem>
                {certificates.map((c) => (
                  <SelectItem key={c.id} value={String(c.id)}>
                    {c.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </FormField>

          <CheckField
            label="Answer these domains"
            checked={input.enabled}
            onChange={(v) => patch({ enabled: v })}
          />
        </div>

        <SheetFooter>
          <Button disabled={busy} onClick={() => void save()}>
            {busy ? "Saving…" : redirect ? "Save changes" : "Add redirect"}
          </Button>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}

function FormField({
  label,
  hint,
  error,
  children,
}: {
  label: string
  hint?: string | undefined
  error?: string | undefined
  children: React.ReactNode
}) {
  return (
    <div className="flex flex-col gap-2">
      <Label>{label}</Label>
      {children}
      {error ? (
        <span className="text-destructive text-xs">{error}</span>
      ) : hint ? (
        <span className="text-muted-foreground text-xs">{hint}</span>
      ) : null}
    </div>
  )
}

function CheckField({
  label,
  checked,
  onChange,
}: {
  label: string
  checked: boolean
  onChange: (value: boolean) => void
}) {
  return (
    <Label className="flex items-center gap-2 text-sm font-normal">
      <Checkbox
        checked={checked}
        onCheckedChange={(v) => onChange(v === true)}
      />
      {label}
    </Label>
  )
}
