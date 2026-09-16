import { useEffect, useState } from "react"
import { IconPlus, IconTrash } from "@tabler/icons-react"

import { api, ApiError } from "@/api/client"
import type {
  AccessList,
  AlgorithmOption,
  GuardianMode,
  Mode,
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
      cache: {
        enabled: false,
        paths: [
          ".js", ".mjs", ".css", ".woff", ".woff2", ".ico", ".png",
          ".jpg", ".jpeg", ".gif", ".svg", ".webp", ".avif",
          "/assets/", "/static/",
        ],
        ttlSeconds: 300,
        maxTtlSeconds: 86400,
        maxObjectBytes: 1048576,
        maxBytes: 67108864,
      },
      // Off, like the guardian and the cache. The numbers are what the
      // operator gets the moment they switch it on, sitting well above
      // ordinary browsing: a page load is a burst of a dozen requests.
      trafficLimits: {
        mode: "off",
        requestsPerSecond: 50,
        burst: 100,
        maxConcurrent: 40,
        maxBodyBytes: 33554432,
        exempt: [],
      },
      // Off, with wording ready to use: turning maintenance on during an
      // incident should be one decision, not a decision and a writing
      // exercise.
      maintenance: {
        enabled: false,
        statusCode: 503,
        title: "Down for maintenance",
        message:
          "We are making some changes and will be back shortly. Thank you for your patience.",
        retryAfterSeconds: 300,
        allowFrom: [],
      },
      errorPages: {
        enabled: false,
        title: "This site is temporarily unavailable",
        message: "Something went wrong on our side. Please try again in a few moments.",
      },
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
    cache: {
      enabled: host.cache.enabled,
      paths: host.cache.paths ?? [],
      ttlSeconds: nanosToSeconds(host.cache.ttl),
      maxTtlSeconds: nanosToSeconds(host.cache.maxTtl),
      maxObjectBytes: host.cache.maxObjectBytes,
      maxBytes: host.cache.maxBytes,
    },
    trafficLimits: {
      mode: host.trafficLimits.mode,
      requestsPerSecond: host.trafficLimits.requestsPerSecond,
      burst: host.trafficLimits.burst,
      maxConcurrent: host.trafficLimits.maxConcurrent,
      maxBodyBytes: host.trafficLimits.maxBodyBytes,
      exempt: host.trafficLimits.exempt ?? [],
    },
    maintenance: {
      ...host.maintenance,
      allowFrom: host.maintenance.allowFrom ?? [],
    },
    errorPages: { ...host.errorPages },
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
              <h3 className="text-sm font-medium">Cache assets</h3>
              <p className="text-muted-foreground text-xs">
                Serves matching responses from memory instead of asking the
                backend. Your origin always wins: a <code>Cache-Control</code>
                {" "}saying not to cache overrides everything here, and a
                response with no <code>Content-Length</code> is never stored.
              </p>
            </div>
            <CheckField
              label="Cache responses for the paths below"
              checked={input.cache.enabled}
              onChange={(v) => patch({ cache: { ...input.cache, enabled: v } })}
            />
            {input.cache.enabled && (
              <>
                <FormField
                  label="Paths"
                  hint="An entry starting with a dot is a file extension; anything else is a path prefix. Comma separated."
                  error={fieldError("cache.paths")}
                >
                  <Input
                    className="font-mono"
                    value={input.cache.paths.join(", ")}
                    onChange={(e) =>
                      patch({
                        cache: {
                          ...input.cache,
                          paths: e.target.value
                            .split(",")
                            .map((p) => p.trim())
                            .filter(Boolean),
                        },
                      })
                    }
                  />
                </FormField>

                <div className="grid gap-4 sm:grid-cols-2">
                  <FormField
                    label="Keep for (seconds)"
                    hint="Used only when the origin does not say."
                    error={fieldError("cache.ttl")}
                  >
                    <Input
                      type="number"
                      min={1}
                      value={input.cache.ttlSeconds}
                      onChange={(e) =>
                        patch({ cache: { ...input.cache, ttlSeconds: Number(e.target.value) } })
                      }
                    />
                  </FormField>
                  <FormField
                    label="Never keep longer than (seconds)"
                    hint="Caps an origin asking for a very long life."
                    error={fieldError("cache.maxTtl")}
                  >
                    <Input
                      type="number"
                      min={1}
                      value={input.cache.maxTtlSeconds}
                      onChange={(e) =>
                        patch({ cache: { ...input.cache, maxTtlSeconds: Number(e.target.value) } })
                      }
                    />
                  </FormField>
                  <FormField
                    label="Largest object (bytes)"
                    hint="Anything bigger streams straight through."
                    error={fieldError("cache.maxObjectBytes")}
                  >
                    <Input
                      type="number"
                      min={1}
                      value={input.cache.maxObjectBytes}
                      onChange={(e) =>
                        patch({
                          cache: { ...input.cache, maxObjectBytes: Number(e.target.value) },
                        })
                      }
                    />
                  </FormField>
                  <FormField
                    label="Memory budget for this host (bytes)"
                    hint="Least recently used objects are dropped to stay inside it."
                    error={fieldError("cache.maxBytes")}
                  >
                    <Input
                      type="number"
                      min={1}
                      value={input.cache.maxBytes}
                      onChange={(e) =>
                        patch({ cache: { ...input.cache, maxBytes: Number(e.target.value) } })
                      }
                    />
                  </FormField>
                </div>
              </>
            )}
          </div>

          <Separator />

          <div className="flex flex-col gap-3">
            <div>
              <h3 className="text-sm font-medium">Maintenance mode</h3>
              <p className="text-muted-foreground text-xs">
                Answers this host with a page instead of proxying it. Better
                than stopping the backend: that tells visitors the site is
                broken rather than being worked on, and looks like a real
                outage in your own monitoring.
              </p>
            </div>

            <CheckField
              label="Show a maintenance page instead of serving this host"
              checked={input.maintenance.enabled}
              onChange={(v) =>
                patch({ maintenance: { ...input.maintenance, enabled: v } })
              }
            />

            {input.maintenance.enabled && (
              <>
                <FormField
                  label="Heading"
                  error={fieldError("maintenance.title")}
                >
                  <Input
                    value={input.maintenance.title}
                    onChange={(e) =>
                      patch({ maintenance: { ...input.maintenance, title: e.target.value } })
                    }
                  />
                </FormField>
                <FormField
                  label="Message"
                  hint="Plain text. Line breaks are kept; markup is not, because this page is served from your own domain."
                  error={fieldError("maintenance.message")}
                >
                  <textarea
                    className="border-input bg-transparent dark:bg-input/30 min-h-20 w-full rounded-md border px-3 py-2 text-sm shadow-xs outline-none"
                    value={input.maintenance.message}
                    onChange={(e) =>
                      patch({ maintenance: { ...input.maintenance, message: e.target.value } })
                    }
                  />
                </FormField>

                <div className="grid gap-4 sm:grid-cols-2">
                  <FormField
                    label="Status"
                    hint="503 is the one status that means 'come back later', and search engines treat it that way."
                    error={fieldError("maintenance.statusCode")}
                  >
                    <Select
                      value={String(input.maintenance.statusCode)}
                      onValueChange={(v) =>
                        patch({
                          maintenance: { ...input.maintenance, statusCode: Number(v) },
                        })
                      }
                    >
                      <SelectTrigger className="w-full">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="503">
                          503 — temporarily unavailable
                        </SelectItem>
                        <SelectItem value="307">307 — temporary redirect</SelectItem>
                        <SelectItem value="200">
                          200 — OK (crawlers will index this page)
                        </SelectItem>
                      </SelectContent>
                    </Select>
                  </FormField>
                  <FormField
                    label="Retry after (seconds)"
                    hint="Sent as the Retry-After header. 0 omits it."
                    error={fieldError("maintenance.retryAfterSeconds")}
                  >
                    <Input
                      type="number"
                      min={0}
                      value={input.maintenance.retryAfterSeconds}
                      onChange={(e) =>
                        patch({
                          maintenance: {
                            ...input.maintenance,
                            retryAfterSeconds: Number(e.target.value),
                          },
                        })
                      }
                    />
                  </FormField>
                </div>

                <FormField
                  label="Let these addresses through"
                  hint="Your own address, so you can check the work before turning the page off. Addresses or CIDR ranges, comma separated."
                  error={fieldError("maintenance.allowFrom")}
                >
                  <Input
                    placeholder="203.0.113.9, 10.0.0.0/8"
                    value={(input.maintenance.allowFrom ?? []).join(", ")}
                    onChange={(e) =>
                      patch({
                        maintenance: {
                          ...input.maintenance,
                          allowFrom: e.target.value
                            .split(",")
                            .map((c) => c.trim())
                            .filter(Boolean),
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
              <h3 className="text-sm font-medium">Error page</h3>
              <p className="text-muted-foreground text-xs">
                What a visitor sees when no backend can be reached. Kept
                separate from maintenance on purpose: saying "planned work"
                during an unplanned outage is a lie a customer remembers.
              </p>
            </div>

            <CheckField
              label="Use my own wording for 502 and 503"
              checked={input.errorPages.enabled}
              onChange={(v) => patch({ errorPages: { ...input.errorPages, enabled: v } })}
            />

            {input.errorPages.enabled && (
              <>
                <FormField label="Heading" error={fieldError("errorPages.title")}>
                  <Input
                    value={input.errorPages.title}
                    onChange={(e) =>
                      patch({ errorPages: { ...input.errorPages, title: e.target.value } })
                    }
                  />
                </FormField>
                <FormField
                  label="Message"
                  hint="Plain text. Avoid naming the backend: a visitor cannot act on it, and it tells a prober how you are put together."
                  error={fieldError("errorPages.message")}
                >
                  <textarea
                    className="border-input bg-transparent dark:bg-input/30 min-h-20 w-full rounded-md border px-3 py-2 text-sm shadow-xs outline-none"
                    value={input.errorPages.message}
                    onChange={(e) =>
                      patch({ errorPages: { ...input.errorPages, message: e.target.value } })
                    }
                  />
                </FormField>
              </>
            )}
          </div>

          <Separator />

          <div className="flex flex-col gap-3">
            <div>
              <h3 className="text-sm font-medium">Traffic limits</h3>
              <p className="text-muted-foreground text-xs">
                Bounds what one client address may ask of this host. This is
                not DDoS protection — a volumetric attack saturates the link
                before it reaches the proxy — but it does stop one client, or a
                script, from asking for more than your backends can serve.
                Start in <strong>detect</strong>.
              </p>
            </div>

            <FormField label="Mode" error={fieldError("trafficLimits.mode")}>
              <Select
                value={input.trafficLimits.mode}
                onValueChange={(v) =>
                  patch({ trafficLimits: { ...input.trafficLimits, mode: v as Mode } })
                }
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="off">Off — no limits</SelectItem>
                  <SelectItem value="detect">
                    Detect — record what would be refused, serve it anyway
                  </SelectItem>
                  <SelectItem value="block">
                    Block — answer 429, or 413 for an oversized body
                  </SelectItem>
                </SelectContent>
              </Select>
            </FormField>

            {input.trafficLimits.mode !== "off" && (
              <>
                {/* The single most common way to break a site with this
                    feature, so it is a warning and not a footnote. */}
                <p className="text-muted-foreground border-l-2 border-amber-500 pl-3 text-xs">
                  Limits count per client address. Behind Cloudflare, a load
                  balancer or NAT, every visitor arrives from the same address
                  unless <code>PONZ_TRUSTED_PROXY_HEADER</code> is set — and
                  then one limit is shared by everyone.
                </p>

                <div className="grid gap-4 sm:grid-cols-2">
                  <FormField
                    label="Requests per second"
                    hint="Sustained rate per client. 0 leaves the rate unlimited."
                    error={fieldError("trafficLimits.requestsPerSecond")}
                  >
                    <Input
                      type="number"
                      min={0}
                      value={input.trafficLimits.requestsPerSecond}
                      onChange={(e) =>
                        patch({
                          trafficLimits: {
                            ...input.trafficLimits,
                            requestsPerSecond: Number(e.target.value),
                          },
                        })
                      }
                    />
                  </FormField>
                  <FormField
                    label="Burst"
                    hint="How far above the rate a client may go momentarily. A page load is a burst of a dozen requests."
                    error={fieldError("trafficLimits.burst")}
                  >
                    <Input
                      type="number"
                      min={0}
                      value={input.trafficLimits.burst}
                      onChange={(e) =>
                        patch({
                          trafficLimits: { ...input.trafficLimits, burst: Number(e.target.value) },
                        })
                      }
                    />
                  </FormField>
                  <FormField
                    label="Requests in flight"
                    hint="Concurrent requests from one address. 0 is unlimited."
                    error={fieldError("trafficLimits.maxConcurrent")}
                  >
                    <Input
                      type="number"
                      min={0}
                      value={input.trafficLimits.maxConcurrent}
                      onChange={(e) =>
                        patch({
                          trafficLimits: {
                            ...input.trafficLimits,
                            maxConcurrent: Number(e.target.value),
                          },
                        })
                      }
                    />
                  </FormField>
                  <FormField
                    label="Largest request body (bytes)"
                    hint="0 allows any size. Refused with 413 before the body is read."
                    error={fieldError("trafficLimits.maxBodyBytes")}
                  >
                    <Input
                      type="number"
                      min={0}
                      value={input.trafficLimits.maxBodyBytes}
                      onChange={(e) =>
                        patch({
                          trafficLimits: {
                            ...input.trafficLimits,
                            maxBodyBytes: Number(e.target.value),
                          },
                        })
                      }
                    />
                  </FormField>
                </div>

                <FormField
                  label="Never limit these addresses"
                  hint="Your monitoring, an office range, a health checker. Addresses or CIDR ranges, comma separated."
                  error={fieldError("trafficLimits.exempt")}
                >
                  <Input
                    placeholder="10.0.0.0/8, 203.0.113.9"
                    value={(input.trafficLimits.exempt ?? []).join(", ")}
                    onChange={(e) =>
                      patch({
                        trafficLimits: {
                          ...input.trafficLimits,
                          exempt: e.target.value
                            .split(",")
                            .map((c) => c.trim())
                            .filter(Boolean),
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
