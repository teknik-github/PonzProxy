import { useEffect, useMemo, useState } from "react"
import { IconPlus } from "@tabler/icons-react"

import { api, ApiError } from "@/api/client"
import type {
  Certificate,
  CertificateInput,
  CertSource,
  ChallengeType,
  DnsProvider,
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
import { date, relativeDays } from "@/format"

interface Props {
  certificates: Certificate[]
  canEdit: boolean
  onChanged: () => void
}

export function Certificates({ certificates, canEdit, onChanged }: Props) {
  const [adding, setAdding] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [busyId, setBusyId] = useState<number | null>(null)

  async function issue(cert: Certificate) {
    setBusyId(cert.id)
    setError(null)
    try {
      await api.issueCertificate(cert.id)
      // Issuance runs on the server; the outcome lands on the certificate.
      window.setTimeout(onChanged, 4000)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Could not start issuance.")
    } finally {
      setBusyId(null)
    }
  }

  async function remove(cert: Certificate) {
    if (!confirm(`Delete ${cert.name}? Any host using it will stop serving HTTPS.`)) {
      return
    }
    try {
      await api.deleteCertificate(cert.id)
      onChanged()
    } catch (err) {
      setError(
        err instanceof ApiError ? err.message : "Could not delete the certificate.",
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
          <CardTitle>Certificates</CardTitle>
          <CardDescription>
            Get one free from Let's Encrypt, upload one you already have, or
            generate a self-signed pair for an internal hostname.
          </CardDescription>
          {canEdit && (
            <CardAction>
              <Button size="sm" onClick={() => setAdding(true)}>
                <IconPlus />
                Add certificate
              </Button>
            </CardAction>
          )}
        </CardHeader>

        {certificates.length === 0 ? (
          <div className="text-muted-foreground px-6 pb-8 text-center text-sm">
            <p className="text-foreground font-medium">No certificates yet</p>
            <p>Add one to serve a host over HTTPS.</p>
          </div>
        ) : (
          <div className="overflow-x-auto px-2 pb-2 sm:px-6 sm:pb-6">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Domains</TableHead>
                  <TableHead>Source</TableHead>
                  <TableHead>Validity</TableHead>
                  <TableHead className="text-right">In use</TableHead>
                  {canEdit && <TableHead />}
                </TableRow>
              </TableHeader>
              <TableBody>
                {certificates.map((cert) => (
                  <TableRow key={cert.id}>
                    <TableCell>
                      {cert.name}
                      {cert.lastError && (
                        <div className="text-destructive max-w-xs text-xs">
                          {cert.lastError}
                        </div>
                      )}
                    </TableCell>
                    <TableCell className="font-mono text-xs">
                      {cert.domains.join(", ")}
                    </TableCell>
                    <TableCell className="text-sm">
                      {sourceLabel(cert.source)}
                      {cert.source === "acme" && cert.challenge && (
                        <div className="text-muted-foreground text-xs">
                          {cert.challenge}
                        </div>
                      )}
                    </TableCell>
                    <TableCell>
                      {!cert.installed ? (
                        <Badge variant="secondary">not issued yet</Badge>
                      ) : (
                        <>
                          <Badge
                            variant={
                              cert.expiresInDays < 14 ? "destructive" : "outline"
                            }
                          >
                            {relativeDays(cert.expiresInDays)}
                          </Badge>
                          <div className="text-muted-foreground text-xs">
                            until {date(cert.notAfter)}
                          </div>
                        </>
                      )}
                    </TableCell>
                    <TableCell className="text-right font-mono text-xs tabular-nums">
                      {cert.inUseByHosts || "—"}
                    </TableCell>
                    {canEdit && (
                      <TableCell className="text-right whitespace-nowrap">
                        {cert.source === "acme" && (
                          <Button
                            variant="ghost"
                            size="sm"
                            disabled={busyId === cert.id}
                            onClick={() => void issue(cert)}
                          >
                            {cert.installed ? "Renew now" : "Request"}
                          </Button>
                        )}
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => void remove(cert)}
                        >
                          Delete
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

      {adding && (
        <CertificateSheet
          onClose={() => setAdding(false)}
          onSaved={() => {
            setAdding(false)
            onChanged()
          }}
        />
      )}
    </div>
  )
}

function sourceLabel(source: CertSource): string {
  switch (source) {
    case "acme":
      return "Let's Encrypt"
    case "manual":
      return "Uploaded"
    case "self_signed":
      return "Self-signed"
  }
}

function CertificateSheet({
  onClose,
  onSaved,
}: {
  onClose: () => void
  onSaved: () => void
}) {
  const [source, setSource] = useState<CertSource>("acme")
  const [name, setName] = useState("")
  const [domainText, setDomainText] = useState("")
  const [challenge, setChallenge] = useState<ChallengeType>("http-01")
  const [providers, setProviders] = useState<DnsProvider[]>([])
  const [providerName, setProviderName] = useState("")
  const [credentials, setCredentials] = useState<Record<string, string>>({})
  const [certificatePem, setCertificatePem] = useState("")
  const [privateKeyPem, setPrivateKeyPem] = useState("")
  const [failure, setFailure] = useState<ApiError | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    api
      .listDnsProviders()
      .then((list) => {
        setProviders(list)
        if (list[0]) setProviderName(list[0].name)
      })
      .catch(() => setProviders([]))
  }, [])

  const domains = useMemo(
    () =>
      domainText
        .split(/[\n,]/)
        .map((d) => d.trim())
        .filter(Boolean),
    [domainText],
  )
  const hasWildcard = domains.some((d) => d.startsWith("*."))
  const provider = providers.find((p) => p.name === providerName)

  // A wildcard can only be proven over DNS, so the form moves the operator
  // there rather than letting them submit something the CA will refuse.
  useEffect(() => {
    if (hasWildcard && challenge !== "dns-01") setChallenge("dns-01")
  }, [hasWildcard, challenge])

  const fieldError = (n: string) => failure?.fieldMessage(n)

  async function save() {
    setBusy(true)
    setFailure(null)

    const input: CertificateInput = { name, domains, source }
    if (source === "acme") {
      input.challenge = challenge
      if (challenge === "dns-01") {
        input.dnsProvider = providerName
        input.dnsCredentials = credentials
      }
    }
    if (source === "manual") {
      input.certificatePem = certificatePem
      input.privateKeyPem = privateKeyPem
    }

    try {
      const created = await api.createCertificate(input)
      // An ACME certificate is only a request until an order runs, so the
      // order starts as part of adding it.
      if (source === "acme") await api.issueCertificate(created.id)
      onSaved()
    } catch (err) {
      setFailure(
        err instanceof ApiError
          ? err
          : new ApiError(0, "Could not save the certificate."),
      )
      setBusy(false)
    }
  }

  return (
    <Sheet open onOpenChange={(open) => !open && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>Add certificate</SheetTitle>
          <SheetDescription>
            Let's Encrypt certificates renew themselves. Uploaded ones do not.
          </SheetDescription>
        </SheetHeader>

        <div className="flex flex-col gap-6 px-4">
          {failure && failure.fields.length === 0 && (
            <div className="border-destructive/50 bg-destructive/10 text-destructive rounded-md border px-3 py-2 text-sm">
              {failure.message}
            </div>
          )}

          <div className="flex flex-col gap-2">
            <Label>Where the certificate comes from</Label>
            <Select value={source} onValueChange={(v) => setSource(v as CertSource)}>
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="acme">
                  Let's Encrypt — issued and renewed automatically
                </SelectItem>
                <SelectItem value="manual">Upload one I already have</SelectItem>
                <SelectItem value="self_signed">
                  Self-signed — for internal hostnames and testing
                </SelectItem>
              </SelectContent>
            </Select>
          </div>

          <div className="flex flex-col gap-2">
            <Label>Name</Label>
            <Input
              placeholder="api.example.com"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
            {fieldError("name") && (
              <span className="text-destructive text-xs">{fieldError("name")}</span>
            )}
          </div>

          <div className="flex flex-col gap-2">
            <Label>Domains</Label>
            <textarea
              className="border-input bg-transparent placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-ring/50 min-h-20 w-full rounded-md border px-3 py-2 font-mono text-sm shadow-xs focus-visible:ring-[3px] focus-visible:outline-none"
              placeholder="api.example.com"
              value={domainText}
              onChange={(e) => setDomainText(e.target.value)}
            />
            <span className="text-muted-foreground text-xs">One per line.</span>
            {(fieldError("domains") ?? fieldError("domains[0]")) && (
              <span className="text-destructive text-xs">
                {fieldError("domains") ?? fieldError("domains[0]")}
              </span>
            )}
          </div>

          {source === "acme" && (
            <>
              <div className="flex flex-col gap-2">
                <Label>How to prove you control the domain</Label>
                <Select
                  value={challenge}
                  onValueChange={(v) => setChallenge(v as ChallengeType)}
                  disabled={hasWildcard}
                >
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="http-01">
                      HTTP — this server must be reachable on port 80
                    </SelectItem>
                    <SelectItem value="tls-alpn-01">
                      TLS — this server must be reachable on port 443
                    </SelectItem>
                    <SelectItem value="dns-01">
                      DNS — a record is published for you
                    </SelectItem>
                  </SelectContent>
                </Select>
                <span className="text-muted-foreground text-xs">
                  {challengeHint(challenge, hasWildcard)}
                </span>
              </div>

              {challenge === "dns-01" && (
                <>
                  <div className="flex flex-col gap-2">
                    <Label>DNS provider</Label>
                    <Select value={providerName} onValueChange={setProviderName}>
                      <SelectTrigger className="w-full">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {providers.map((p) => (
                          <SelectItem key={p.name} value={p.name}>
                            {p.label}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    {provider && (
                      <span className="text-muted-foreground text-xs">
                        {provider.description}
                      </span>
                    )}
                  </div>
                  {provider?.fields.map((f) => (
                    <div key={f.key} className="flex flex-col gap-2">
                      <Label>{f.label}</Label>
                      <Input
                        type={f.secret ? "password" : "text"}
                        autoComplete="off"
                        value={credentials[f.key] ?? ""}
                        onChange={(e) =>
                          setCredentials((prev) => ({
                            ...prev,
                            [f.key]: e.target.value,
                          }))
                        }
                      />
                      {f.help && (
                        <span className="text-muted-foreground text-xs">{f.help}</span>
                      )}
                    </div>
                  ))}
                </>
              )}
            </>
          )}

          {source === "manual" && (
            <>
              <div className="flex flex-col gap-2">
                <Label>Certificate</Label>
                <textarea
                  className="border-input bg-transparent placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-ring/50 min-h-32 w-full rounded-md border px-3 py-2 font-mono text-xs shadow-xs focus-visible:ring-[3px] focus-visible:outline-none"
                  placeholder="-----BEGIN CERTIFICATE-----"
                  value={certificatePem}
                  onChange={(e) => setCertificatePem(e.target.value)}
                />
                <span className="text-muted-foreground text-xs">
                  Paste the full chain, leaf first.
                </span>
                {fieldError("certificate") && (
                  <span className="text-destructive text-xs">
                    {fieldError("certificate")}
                  </span>
                )}
              </div>
              <div className="flex flex-col gap-2">
                <Label>Private key</Label>
                <textarea
                  className="border-input bg-transparent placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-ring/50 min-h-32 w-full rounded-md border px-3 py-2 font-mono text-xs shadow-xs focus-visible:ring-[3px] focus-visible:outline-none"
                  placeholder="-----BEGIN PRIVATE KEY-----"
                  value={privateKeyPem}
                  onChange={(e) => setPrivateKeyPem(e.target.value)}
                />
              </div>
            </>
          )}

          {source === "self_signed" && (
            <p className="text-muted-foreground text-sm">
              Browsers will warn visitors that this certificate is not trusted.
              Use it for internal hostnames, or while you get routing working
              before switching to Let's Encrypt.
            </p>
          )}
        </div>

        <SheetFooter>
          <Button disabled={busy} onClick={() => void save()}>
            {busy
              ? "Working…"
              : source === "acme"
                ? "Request certificate"
                : "Add certificate"}
          </Button>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}

function challengeHint(challenge: ChallengeType, hasWildcard: boolean): string {
  if (hasWildcard) return "A wildcard domain can only be proven over DNS."
  switch (challenge) {
    case "http-01":
      return "The certificate authority will fetch a file from port 80 of this server."
    case "tls-alpn-01":
      return "The certificate authority will connect to port 443. No port 80 needed."
    case "dns-01":
      return "Works even when this server is not reachable from the internet."
  }
}
