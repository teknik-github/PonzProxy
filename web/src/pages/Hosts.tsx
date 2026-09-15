import { useEffect, useState } from "react"
import { IconPlus, IconTrash } from "@tabler/icons-react"

import { api, ApiError } from "@/api/client"
import type {
  AccessList,
  AlgorithmOption,
  GuardianMode,
  GuardianRuleOption,
  Certificate,
  Host,
  HostInput,
  UpstreamInput,
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
import { nanosToSeconds } from "@/format"

interface Props {
  hosts: Host[]
  certificates: Certificate[]
  accessLists: AccessList[]
  canEdit: boolean
  onChanged: () => void
  /** Set when the sidebar asked to open the editor, so its quick action and
   *  per-host menu land on this screen with the sheet already open. */
  openFromSidebar?: Host | "new" | null
  onSidebarOpenHandled?: () => void
}

export function Hosts({
  hosts,
  certificates,
  accessLists,
  canEdit,
  onChanged,
  openFromSidebar,
  onSidebarOpenHandled,
}: Props) {
  const [editing, setEditing] = useState<Host | "new" | null>(null)

  useEffect(() => {
    if (!openFromSidebar) return
    setEditing(openFromSidebar)
    onSidebarOpenHandled?.()
  }, [openFromSidebar, onSidebarOpenHandled])
  const [algorithms, setAlgorithms] = useState<AlgorithmOption[]>([])
  const [guardianRules, setGuardianRules] = useState<GuardianRuleOption[]>([])
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    api.listAlgorithms().then(setAlgorithms).catch(() => setAlgorithms([]))
    api.listGuardianRules().then(setGuardianRules).catch(() => setGuardianRules([]))
  }, [])

  async function remove(host: Host) {
    if (!confirm(`Stop routing ${host.domains.join(", ")}? This cannot be undone.`)) {
      return
    }
    try {
      await api.deleteHost(host.id)
      onChanged()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Could not delete the host.")
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
          <CardTitle>Hosts</CardTitle>
          <CardDescription>
            A host maps domains to the servers behind them and decides how
            requests are spread across those servers.
          </CardDescription>
          {canEdit && (
            <CardAction>
              <Button size="sm" onClick={() => setEditing("new")}>
                <IconPlus />
                Add host
              </Button>
            </CardAction>
          )}
        </CardHeader>

        {hosts.length === 0 ? (
          <div className="text-muted-foreground px-6 pb-8 text-center text-sm">
            <p className="text-foreground font-medium">No hosts yet</p>
            <p>Add one to start routing a domain to your servers.</p>
          </div>
        ) : (
          <div className="overflow-x-auto px-2 pb-2 sm:px-6 sm:pb-6">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Domains</TableHead>
                  <TableHead>Balancing</TableHead>
                  <TableHead className="text-right">Upstreams</TableHead>
                  <TableHead>TLS</TableHead>
                  <TableHead>State</TableHead>
                  {canEdit && <TableHead />}
                </TableRow>
              </TableHeader>
              <TableBody>
                {hosts.map((host) => {
                  const cert = certificates.find((c) => c.id === host.certificateId)
                  return (
                    <TableRow key={host.id}>
                      <TableCell>
                        <span className="font-mono text-xs">
                          {host.domains.join(", ")}
                        </span>
                        <div className="text-muted-foreground text-xs">
                          {host.name}
                        </div>
                      </TableCell>
                      <TableCell className="text-sm">
                        {host.algorithm.replace(/_/g, " ")}
                      </TableCell>
                      <TableCell className="text-right font-mono text-xs tabular-nums">
                        {host.upstreams.filter((u) => u.enabled).length}
                      </TableCell>
                      <TableCell className="text-xs">
                        {cert ? (
                          <span className="font-mono">
                            {cert.name}
                            {host.forceHttps && (
                              <span className="text-muted-foreground"> forced</span>
                            )}
                          </span>
                        ) : (
                          <span className="text-muted-foreground">none</span>
                        )}
                      </TableCell>
                      <TableCell>
                        <Badge variant={host.enabled ? "outline" : "secondary"}>
                          {host.enabled ? "routing" : "paused"}
                        </Badge>
                      </TableCell>
                      {canEdit && (
                        <TableCell className="text-right whitespace-nowrap">
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => setEditing(host)}
                          >
                            Edit
                          </Button>
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => void remove(host)}
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
        <HostSheet
          host={editing === "new" ? null : editing}
          algorithms={algorithms}
          certificates={certificates}
          accessLists={accessLists}
          guardianRules={guardianRules}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            onChanged()
          }}
        />
      )}
    </div>
  )
}

function blankUpstream(): UpstreamInput {
  return {
    scheme: "http",
    address: "",
    weight: 1,
    maxConns: 0,
    enabled: true,
    skipTlsVerify: false,
  }
}

function toInput(host: Host | null): HostInput {
  if (!host) {
    return {
      name: "",
      enabled: true,
      domains: [],
      algorithm: "round_robin",
      upstreams: [blankUpstream()],
      certificateId: null,
      accessListId: null,
      forceHttps: false,
      hstsMaxAge: 0,
      websocketSupport: true,
      preserveHost: false,
      healthCheck: {
        enabled: true,
        path: "/",
        intervalSeconds: 10,
        timeoutSeconds: 5,
        healthyThreshold: 2,
        unhealthyThreshold: 3,
        expectStatus: 0,
      },
      passiveHealth: { enabled: true, maxFails: 3, ejectForSeconds: 30 },
      accessLog: { enabled: false, includeQuery: false },
      guardian: { mode: "off", rules: [], maxUriLength: 2048 },
    }
  }
  return {
    name: host.name,
    enabled: host.enabled,
    domains: host.domains,
    algorithm: host.algorithm,
    upstreams: host.upstreams.map((u) => ({
      scheme: u.scheme,
      address: u.address,
      weight: u.weight,
      maxConns: u.maxConns,
      enabled: u.enabled,
      skipTlsVerify: u.skipTlsVerify,
    })),
    certificateId: host.certificateId,
    accessListId: host.accessListId,
    forceHttps: host.forceHttps,
    hstsMaxAge: host.hstsMaxAge,
    websocketSupport: host.websocketSupport,
    preserveHost: host.preserveHost,
    healthCheck: {
      enabled: host.healthCheck.enabled,
      path: host.healthCheck.path,
      intervalSeconds: nanosToSeconds(host.healthCheck.interval),
      timeoutSeconds: nanosToSeconds(host.healthCheck.timeout),
      healthyThreshold: host.healthCheck.healthyThreshold,
      unhealthyThreshold: host.healthCheck.unhealthyThreshold,
      expectStatus: host.healthCheck.expectStatus,
    },
    passiveHealth: {
      enabled: host.passiveHealth.enabled,
      maxFails: host.passiveHealth.maxFails,
      ejectForSeconds: nanosToSeconds(host.passiveHealth.ejectFor),
    },
    accessLog: {
      enabled: host.accessLog.enabled,
      includeQuery: host.accessLog.includeQuery,
    },
    guardian: {
      mode: host.guardian.mode,
      rules: host.guardian.rules ?? [],
      maxUriLength: host.guardian.maxUriLength,
    },
  }
}

function HostSheet({
  host,
  algorithms,
  certificates,
  accessLists,
  guardianRules,
  onClose,
  onSaved,
}: {
  host: Host | null
  algorithms: AlgorithmOption[]
  certificates: Certificate[]
  accessLists: AccessList[]
  guardianRules: GuardianRuleOption[]
  onClose: () => void
  onSaved: () => void
}) {
  const [input, setInput] = useState<HostInput>(() => toInput(host))
  const [domainText, setDomainText] = useState(() => (host?.domains ?? []).join("\n"))
  const [failure, setFailure] = useState<ApiError | null>(null)
  const [busy, setBusy] = useState(false)

  const patch = (fields: Partial<HostInput>) =>
    setInput((prev) => ({ ...prev, ...fields }))
  const patchUpstream = (index: number, fields: Partial<UpstreamInput>) =>
    patch({
      upstreams: input.upstreams.map((u, i) => (i === index ? { ...u, ...fields } : u)),
    })

  const chosen = algorithms.find((a) => a.value === input.algorithm)
  const fieldError = (name: string) => failure?.fieldMessage(name)

  async function save() {
    setBusy(true)
    setFailure(null)
    const payload: HostInput = {
      ...input,
      domains: domainText
        .split(/[\n,]/)
        .map((d) => d.trim())
        .filter(Boolean),
    }
    try {
      if (host) await api.updateHost(host.id, payload)
      else await api.createHost(payload)
      onSaved()
    } catch (err) {
      setFailure(
        err instanceof ApiError ? err : new ApiError(0, "Could not save the host."),
      )
      setBusy(false)
    }
  }

  return (
    <Sheet open onOpenChange={(open) => !open && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>{host ? `Edit ${host.name}` : "Add host"}</SheetTitle>
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

          <div className="grid gap-4 sm:grid-cols-2">
            <FormField label="Name" hint="For your own reference in this console.">
              <Input
                value={input.name}
                onChange={(e) => patch({ name: e.target.value })}
              />
            </FormField>
            <FormField
              label="Balancing"
              hint={chosen?.description}
              error={fieldError("algorithm")}
            >
              <Select
                value={input.algorithm}
                onValueChange={(v) =>
                  patch({ algorithm: v as HostInput["algorithm"] })
                }
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {algorithms.map((a) => (
                    <SelectItem key={a.value} value={a.value}>
                      {a.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </FormField>
          </div>

          <FormField
            label="Domains"
            hint="One per line. Use *.example.com to match any single subdomain."
            error={fieldError("domains") ?? fieldError("domains[0]")}
          >
            <textarea
              className="border-input bg-transparent placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-ring/50 min-h-20 w-full rounded-md border px-3 py-2 font-mono text-sm shadow-xs focus-visible:ring-[3px] focus-visible:outline-none"
              placeholder="api.example.com"
              value={domainText}
              onChange={(e) => setDomainText(e.target.value)}
            />
          </FormField>

          <Separator />

          <div className="flex flex-col gap-3">
            <div className="flex items-center">
              <h3 className="text-sm font-medium">Upstreams</h3>
              <Button
                variant="outline"
                size="sm"
                className="ml-auto"
                onClick={() =>
                  patch({ upstreams: [...input.upstreams, blankUpstream()] })
                }
              >
                <IconPlus />
                Add upstream
              </Button>
            </div>
            {fieldError("upstreams") && (
              <p className="text-destructive text-xs">{fieldError("upstreams")}</p>
            )}

            {input.upstreams.map((u, i) => (
              <div key={i} className="bg-muted/40 flex flex-col gap-3 rounded-lg border p-3">
                <div className="grid gap-3 sm:grid-cols-[100px_1fr_90px]">
                  <FormField label="Scheme">
                    <Select
                      value={u.scheme}
                      onValueChange={(v) =>
                        patchUpstream(i, { scheme: v as "http" | "https" })
                      }
                    >
                      <SelectTrigger className="w-full">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="http">http</SelectItem>
                        <SelectItem value="https">https</SelectItem>
                      </SelectContent>
                    </Select>
                  </FormField>
                  <FormField
                    label="Address"
                    hint="host:port"
                    error={fieldError(`upstreams[${i}].address`)}
                  >
                    <Input
                      className="font-mono"
                      placeholder="10.0.0.5:8080"
                      value={u.address}
                      onChange={(e) => patchUpstream(i, { address: e.target.value })}
                    />
                  </FormField>
                  <FormField
                    label="Weight"
                    error={fieldError(`upstreams[${i}].weight`)}
                  >
                    <Input
                      type="number"
                      min={1}
                      max={1000}
                      value={u.weight}
                      onChange={(e) =>
                        patchUpstream(i, { weight: Number(e.target.value) })
                      }
                    />
                  </FormField>
                </div>

                <div className="flex flex-wrap items-center gap-4">
                  <CheckField
                    label="Send traffic here"
                    checked={u.enabled}
                    onChange={(v) => patchUpstream(i, { enabled: v })}
                  />
                  {u.scheme === "https" && (
                    <CheckField
                      label="Accept a self-signed certificate"
                      checked={u.skipTlsVerify}
                      onChange={(v) => patchUpstream(i, { skipTlsVerify: v })}
                    />
                  )}
                  {input.upstreams.length > 1 && (
                    <Button
                      variant="ghost"
                      size="sm"
                      className="text-muted-foreground ml-auto"
                      onClick={() =>
                        patch({ upstreams: input.upstreams.filter((_, j) => j !== i) })
                      }
                    >
                      <IconTrash />
                      Remove
                    </Button>
                  )}
                </div>
              </div>
            ))}
          </div>

          <Separator />

          <FormField
            label="Access list"
            hint="Restricts who may reach this host, before any upstream is chosen."
            error={fieldError("accessListId")}
          >
            <Select
              value={input.accessListId === null ? "none" : String(input.accessListId)}
              onValueChange={(v) =>
                patch({ accessListId: v === "none" ? null : Number(v) })
              }
            >
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="none">Open to everyone</SelectItem>
                {accessLists.map((a) => (
                  <SelectItem key={a.id} value={String(a.id)}>
                    {a.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </FormField>

          <Separator />

          <div className="flex flex-col gap-3">
            <h3 className="text-sm font-medium">TLS</h3>
            <div className="grid gap-4 sm:grid-cols-2">
              <FormField label="Certificate" error={fieldError("certificateId")}>
                <Select
                  value={input.certificateId === null ? "none" : String(input.certificateId)}
                  onValueChange={(v) =>
                    patch(
                      v === "none"
                        ? { certificateId: null, forceHttps: false, hstsMaxAge: 0 }
                        : { certificateId: Number(v) },
                    )
                  }
                >
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="none">Serve over HTTP only</SelectItem>
                    {certificates.map((c) => (
                      <SelectItem key={c.id} value={String(c.id)}>
                        {c.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </FormField>
              <FormField
                label="Strict transport security"
                hint="Seconds. 0 turns it off. Only set this once HTTPS works."
                error={fieldError("hstsMaxAge")}
              >
                <Input
                  type="number"
                  min={0}
                  disabled={input.certificateId === null}
                  value={input.hstsMaxAge}
                  onChange={(e) => patch({ hstsMaxAge: Number(e.target.value) })}
                />
              </FormField>
            </div>
            <div className="flex flex-wrap gap-4">
              <CheckField
                label="Redirect HTTP to HTTPS"
                checked={input.forceHttps}
                disabled={input.certificateId === null}
                onChange={(v) => patch({ forceHttps: v })}
              />
              <CheckField
                label="Allow WebSocket upgrades"
                checked={input.websocketSupport}
                onChange={(v) => patch({ websocketSupport: v })}
              />
              <CheckField
                label="Send the original Host header upstream"
                checked={input.preserveHost}
                onChange={(v) => patch({ preserveHost: v })}
              />
            </div>
            {fieldError("forceHttps") && (
              <p className="text-destructive text-xs">{fieldError("forceHttps")}</p>
            )}
          </div>

          <Separator />

          <div className="flex flex-col gap-3">
            <h3 className="text-sm font-medium">Health checks</h3>
            <CheckField
              label="Probe each upstream and take failing ones out of rotation"
              checked={input.healthCheck.enabled}
              onChange={(v) =>
                patch({ healthCheck: { ...input.healthCheck, enabled: v } })
              }
            />
            {input.healthCheck.enabled && (
              <div className="grid gap-4 sm:grid-cols-3">
                <FormField label="Path">
                  <Input
                    className="font-mono"
                    value={input.healthCheck.path}
                    onChange={(e) =>
                      patch({
                        healthCheck: { ...input.healthCheck, path: e.target.value },
                      })
                    }
                  />
                </FormField>
                <FormField label="Every (seconds)">
                  <Input
                    type="number"
                    min={1}
                    value={input.healthCheck.intervalSeconds}
                    onChange={(e) =>
                      patch({
                        healthCheck: {
                          ...input.healthCheck,
                          intervalSeconds: Number(e.target.value),
                        },
                      })
                    }
                  />
                </FormField>
                <FormField label="Give up after (seconds)">
                  <Input
                    type="number"
                    min={1}
                    value={input.healthCheck.timeoutSeconds}
                    onChange={(e) =>
                      patch({
                        healthCheck: {
                          ...input.healthCheck,
                          timeoutSeconds: Number(e.target.value),
                        },
                      })
                    }
                  />
                </FormField>
                <FormField label="Failures before removing">
                  <Input
                    type="number"
                    min={1}
                    value={input.healthCheck.unhealthyThreshold}
                    onChange={(e) =>
                      patch({
                        healthCheck: {
                          ...input.healthCheck,
                          unhealthyThreshold: Number(e.target.value),
                        },
                      })
                    }
                  />
                </FormField>
                <FormField label="Successes before restoring">
                  <Input
                    type="number"
                    min={1}
                    value={input.healthCheck.healthyThreshold}
                    onChange={(e) =>
                      patch({
                        healthCheck: {
                          ...input.healthCheck,
                          healthyThreshold: Number(e.target.value),
                        },
                      })
                    }
                  />
                </FormField>
                <FormField
                  label="Expected status"
                  hint="0 accepts any 2xx or 3xx."
                  error={fieldError("healthCheck.expectStatus")}
                >
                  <Input
                    type="number"
                    min={0}
                    max={599}
                    value={input.healthCheck.expectStatus}
                    onChange={(e) =>
                      patch({
                        healthCheck: {
                          ...input.healthCheck,
                          expectStatus: Number(e.target.value),
                        },
                      })
                    }
                  />
                </FormField>
              </div>
            )}
          </div>

          <Separator />

          <div className="flex flex-col gap-3">
            <div>
              <h3 className="text-sm font-medium">Failing upstreams</h3>
              <p className="text-muted-foreground text-xs">
                Uses what real requests already reveal, so a dead upstream
                stops receiving traffic without waiting for the next probe —
                and even when probing above is switched off.
              </p>
            </div>
            <CheckField
              label="Take an upstream out after repeated connection failures"
              checked={input.passiveHealth.enabled}
              onChange={(v) =>
                patch({ passiveHealth: { ...input.passiveHealth, enabled: v } })
              }
            />
            {input.passiveHealth.enabled && (
              <div className="grid gap-4 sm:grid-cols-2">
                <FormField
                  label="Failures before removing"
                  hint="Consecutive failures to connect. Responses, including 5xx, never count."
                  error={fieldError("passiveHealth.maxFails")}
                >
                  <Input
                    type="number"
                    min={1}
                    max={100}
                    value={input.passiveHealth.maxFails}
                    onChange={(e) =>
                      patch({
                        passiveHealth: {
                          ...input.passiveHealth,
                          maxFails: Number(e.target.value),
                        },
                      })
                    }
                  />
                </FormField>
                <FormField
                  label="Keep it out for (seconds)"
                  hint="Traffic returns after this, and a success restores it sooner."
                  error={fieldError("passiveHealth.ejectFor")}
                >
                  <Input
                    type="number"
                    min={1}
                    max={3600}
                    value={input.passiveHealth.ejectForSeconds}
                    onChange={(e) =>
                      patch({
                        passiveHealth: {
                          ...input.passiveHealth,
                          ejectForSeconds: Number(e.target.value),
                        },
                      })
                    }
                  />
                </FormField>
              </div>
            )}
          </div>

          <Separator />

          <div className="flex flex-col gap-3">
            <div>
              <h3 className="text-sm font-medium">Block exploits</h3>
              <p className="text-muted-foreground text-xs">
                Inspects each request for obvious attack patterns before it
                reaches your servers. Start in <strong>detect</strong> and read
                the access log for a while: refusing a real customer is a
                visible outage, a probe getting through usually is not.
              </p>
            </div>

            <FormField label="Mode" error={fieldError("guardian.mode")}>
              <Select
                value={input.guardian.mode}
                onValueChange={(v) =>
                  patch({ guardian: { ...input.guardian, mode: v as GuardianMode } })
                }
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="off">Off — inspect nothing</SelectItem>
                  <SelectItem value="detect">
                    Detect — record matches, forward the request anyway
                  </SelectItem>
                  <SelectItem value="block">Block — refuse matched requests</SelectItem>
                </SelectContent>
              </Select>
            </FormField>

            {input.guardian.mode !== "off" && (
              <>
                {fieldError("guardian.rules") && (
                  <p className="text-destructive text-xs">{fieldError("guardian.rules")}</p>
                )}
                <div className="flex flex-col gap-3">
                  {guardianRules.map((rule) => (
                    <label key={rule.value} className="flex items-start gap-3 text-sm">
                      <Checkbox
                        className="mt-0.5"
                        checked={(input.guardian.rules ?? []).includes(rule.value)}
                        onCheckedChange={(v) =>
                          patch({
                            guardian: {
                              ...input.guardian,
                              rules:
                                v === true
                                  ? [...(input.guardian.rules ?? []), rule.value]
                                  : (input.guardian.rules ?? []).filter(
                                      (x) => x !== rule.value,
                                    ),
                            },
                          })
                        }
                      />
                      <span>
                        <span className="font-medium">{rule.label}</span>
                        {!rule.safeByDefault && (
                          <Badge variant="secondary" className="ml-2">
                            watch first
                          </Badge>
                        )}
                        <span className="text-muted-foreground block text-xs">
                          {rule.description}
                        </span>
                      </span>
                    </label>
                  ))}
                </div>

                <FormField
                  label="Longest request target (characters)"
                  hint="Anything longer is refused before it is scanned. 0 disables the check."
                  error={fieldError("guardian.maxUriLength")}
                >
                  <Input
                    type="number"
                    min={0}
                    max={65536}
                    value={input.guardian.maxUriLength}
                    onChange={(e) =>
                      patch({
                        guardian: {
                          ...input.guardian,
                          maxUriLength: Number(e.target.value),
                        },
                      })
                    }
                  />
                </FormField>
              </>
            )}
          </div>

          <Separator />

          <div className="flex flex-col gap-3">
            <div>
              <h3 className="text-sm font-medium">Access log</h3>
              <p className="text-muted-foreground text-xs">
                Stores each request so you can search them later. Off by
                default — a busy host fills the log quickly.
              </p>
            </div>
            <CheckField
              label="Record requests to this host"
              checked={input.accessLog.enabled}
              onChange={(v) =>
                patch({ accessLog: { ...input.accessLog, enabled: v } })
              }
            />
            {input.accessLog.enabled && (
              <>
                <CheckField
                  label="Include the query string"
                  checked={input.accessLog.includeQuery}
                  onChange={(v) =>
                    patch({ accessLog: { ...input.accessLog, includeQuery: v } })
                  }
                />
                <p className="text-muted-foreground text-xs">
                  Query strings often carry session tokens and reset links, and
                  anyone who can open this console can search them. Leave this
                  off unless you need it to debug.
                </p>
              </>
            )}
          </div>

          <Separator />

          <CheckField
            label="Route traffic to this host"
            checked={input.enabled}
            onChange={(v) => patch({ enabled: v })}
          />
        </div>

        <SheetFooter>
          <Button disabled={busy} onClick={() => void save()}>
            {busy ? "Saving…" : host ? "Save changes" : "Add host"}
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
  disabled,
  onChange,
}: {
  label: string
  checked: boolean
  disabled?: boolean
  onChange: (value: boolean) => void
}) {
  return (
    <Label className="flex items-center gap-2 text-sm font-normal">
      <Checkbox
        checked={checked}
        disabled={disabled ?? false}
        onCheckedChange={(v) => onChange(v === true)}
      />
      {label}
    </Label>
  )
}
