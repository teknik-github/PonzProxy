import { useState } from "react"
import { IconPlus, IconTrash } from "@tabler/icons-react"

import { api, ApiError } from "@/api/client"
import type {
  AccessAction,
  AccessList,
  AccessListInput,
  AccessRuleInput,
  BasicAuthInput,
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

interface Props {
  accessLists: AccessList[]
  canEdit: boolean
  onChanged: () => void
}

export function AccessLists({ accessLists, canEdit, onChanged }: Props) {
  const [editing, setEditing] = useState<AccessList | "new" | null>(null)
  const [error, setError] = useState<string | null>(null)

  async function remove(list: AccessList) {
    if (!confirm(`Delete ${list.name}? Hosts using it would be open to everyone.`)) {
      return
    }
    try {
      await api.deleteAccessList(list.id)
      onChanged()
    } catch (err) {
      setError(
        err instanceof ApiError ? err.message : "Could not delete the access list.",
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
          <CardTitle>Access lists</CardTitle>
          <CardDescription>
            A named rule set a host can sit behind. Requests are checked before
            an upstream is chosen, so anything refused never reaches your
            servers.
          </CardDescription>
          {canEdit && (
            <CardAction>
              <Button size="sm" onClick={() => setEditing("new")}>
                <IconPlus />
                Add access list
              </Button>
            </CardAction>
          )}
        </CardHeader>

        {accessLists.length === 0 ? (
          <div className="text-muted-foreground px-6 pb-8 text-center text-sm">
            <p className="text-foreground font-medium">No access lists yet</p>
            <p>Add one to limit a host to known addresses, or put it behind a password.</p>
          </div>
        ) : (
          <div className="overflow-x-auto px-2 pb-2 sm:px-6 sm:pb-6">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Addresses</TableHead>
                  <TableHead>Password</TableHead>
                  <TableHead>Requires</TableHead>
                  <TableHead>Used by</TableHead>
                  {canEdit && <TableHead />}
                </TableRow>
              </TableHeader>
              <TableBody>
                {accessLists.map((list) => {
                  const rules = list.rules ?? []
                  const users = list.basicAuth ?? []
                  const allows = rules.filter((r) => r.action === "allow").length
                  return (
                    <TableRow key={list.id}>
                      <TableCell className="font-medium">{list.name}</TableCell>
                      <TableCell className="text-sm">
                        {rules.length === 0 ? (
                          <span className="text-muted-foreground">any</span>
                        ) : (
                          `${allows} allowed, ${rules.length - allows} denied`
                        )}
                      </TableCell>
                      <TableCell className="text-sm">
                        {users.length === 0 ? (
                          <span className="text-muted-foreground">none</span>
                        ) : (
                          `${users.length} user${users.length === 1 ? "" : "s"}`
                        )}
                      </TableCell>
                      <TableCell>
                        <Badge variant="outline">
                          {list.satisfyAny ? "either" : "all of them"}
                        </Badge>
                      </TableCell>
                      <TableCell className="text-sm tabular-nums">
                        {list.inUseByHosts === 0 ? (
                          <span className="text-muted-foreground">no hosts</span>
                        ) : (
                          `${list.inUseByHosts} host${list.inUseByHosts === 1 ? "" : "s"}`
                        )}
                      </TableCell>
                      {canEdit && (
                        <TableCell className="text-right whitespace-nowrap">
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => setEditing(list)}
                          >
                            Edit
                          </Button>
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => void remove(list)}
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
        <AccessListSheet
          list={editing === "new" ? null : editing}
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

function blankRule(): AccessRuleInput {
  return { action: "allow", cidr: "" }
}

function blankUser(): BasicAuthInput {
  return { username: "", password: "" }
}

function toInput(list: AccessList | null): AccessListInput {
  if (!list) {
    return { name: "", satisfyAny: false, rules: [blankRule()], basicAuth: [] }
  }
  return {
    name: list.name,
    satisfyAny: list.satisfyAny,
    rules: (list.rules ?? []).map((r) => ({ action: r.action, cidr: r.cidr })),
    // The server never sends a password back, so an untouched field stays
    // empty and means "keep the one already stored".
    basicAuth: (list.basicAuth ?? []).map((u) => ({ username: u.username, password: "" })),
  }
}

function AccessListSheet({
  list,
  onClose,
  onSaved,
}: {
  list: AccessList | null
  onClose: () => void
  onSaved: () => void
}) {
  const [input, setInput] = useState<AccessListInput>(() => toInput(list))
  const [failure, setFailure] = useState<ApiError | null>(null)
  const [busy, setBusy] = useState(false)

  const patch = (fields: Partial<AccessListInput>) =>
    setInput((prev) => ({ ...prev, ...fields }))
  const patchRule = (index: number, fields: Partial<AccessRuleInput>) =>
    patch({
      rules: input.rules.map((r, i) => (i === index ? { ...r, ...fields } : r)),
    })
  const patchUser = (index: number, fields: Partial<BasicAuthInput>) =>
    patch({
      basicAuth: input.basicAuth.map((u, i) => (i === index ? { ...u, ...fields } : u)),
    })

  const fieldError = (name: string) => failure?.fieldMessage(name)
  const hasRules = input.rules.some((r) => r.cidr.trim() !== "")
  const hasUsers = input.basicAuth.length > 0

  async function save() {
    setBusy(true)
    setFailure(null)
    try {
      if (list) await api.updateAccessList(list.id, input)
      else await api.createAccessList(input)
      onSaved()
    } catch (err) {
      setFailure(
        err instanceof ApiError
          ? err
          : new ApiError(0, "Could not save the access list."),
      )
      setBusy(false)
    }
  }

  return (
    <Sheet open onOpenChange={(open) => !open && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>{list ? `Edit ${list.name}` : "Add access list"}</SheetTitle>
          <SheetDescription>
            Rules are checked against the client address, and a denied block
            always wins over an allowed one. With no allow rule the list only
            subtracts; add one and everything else is refused.
          </SheetDescription>
        </SheetHeader>

        <div className="flex flex-col gap-6 px-4">
          {failure && failure.fields.length === 0 && (
            <div className="border-destructive/50 bg-destructive/10 text-destructive rounded-md border px-3 py-2 text-sm">
              {failure.message}
            </div>
          )}

          <div className="grid gap-4 sm:grid-cols-2">
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
              label="A request must satisfy"
              hint={
                input.satisfyAny
                  ? "An allowed address gets in without a password, and the password works from anywhere."
                  : "Both the address rules and the password, when either is configured."
              }
              error={fieldError("satisfyAny")}
            >
              <Select
                value={input.satisfyAny ? "any" : "all"}
                onValueChange={(v) => patch({ satisfyAny: v === "any" })}
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">Every check</SelectItem>
                  <SelectItem value="any">Either check</SelectItem>
                </SelectContent>
              </Select>
            </FormField>
          </div>

          {input.satisfyAny && !(hasRules && hasUsers) && (
            <p className="text-muted-foreground text-xs">
              Either-check needs both an address rule and a user: with only one
              of them, the empty half would let every request through.
            </p>
          )}

          <Separator />

          <div className="flex flex-col gap-3">
            <div className="flex items-center">
              <h3 className="text-sm font-medium">Address rules</h3>
              <Button
                variant="outline"
                size="sm"
                className="ml-auto"
                onClick={() => patch({ rules: [...input.rules, blankRule()] })}
              >
                <IconPlus />
                Add rule
              </Button>
            </div>
            {fieldError("rules") && (
              <p className="text-destructive text-xs">{fieldError("rules")}</p>
            )}

            {input.rules.map((rule, i) => (
              <div
                key={i}
                className="bg-muted/40 grid gap-3 rounded-lg border p-3 sm:grid-cols-[120px_1fr_auto] sm:items-start"
              >
                <FormField label="Action">
                  <Select
                    value={rule.action}
                    onValueChange={(v) => patchRule(i, { action: v as AccessAction })}
                  >
                    <SelectTrigger className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="allow">Allow</SelectItem>
                      <SelectItem value="deny">Deny</SelectItem>
                    </SelectContent>
                  </Select>
                </FormField>
                <FormField
                  label="Address or block"
                  hint="10.0.0.0/8, 203.0.113.7 or 2001:db8::/32"
                  error={fieldError(`rules[${i}].cidr`) ?? fieldError(`rules[${i}].action`)}
                >
                  <Input
                    className="font-mono"
                    placeholder="10.0.0.0/8"
                    value={rule.cidr}
                    onChange={(e) => patchRule(i, { cidr: e.target.value })}
                  />
                </FormField>
                <Button
                  variant="ghost"
                  size="sm"
                  className="text-muted-foreground sm:mt-7"
                  onClick={() => patch({ rules: input.rules.filter((_, j) => j !== i) })}
                >
                  <IconTrash />
                  Remove
                </Button>
              </div>
            ))}
          </div>

          <Separator />

          <div className="flex flex-col gap-3">
            <div className="flex items-center">
              <h3 className="text-sm font-medium">Password protection</h3>
              <Button
                variant="outline"
                size="sm"
                className="ml-auto"
                onClick={() => patch({ basicAuth: [...input.basicAuth, blankUser()] })}
              >
                <IconPlus />
                Add user
              </Button>
            </div>
            <p className="text-muted-foreground text-xs">
              Visitors are asked for these by the browser, over HTTP basic auth.
              Use it on a host that redirects to HTTPS, or the password travels
              in the clear.
            </p>

            {input.basicAuth.map((user, i) => (
              <div
                key={i}
                className="bg-muted/40 grid gap-3 rounded-lg border p-3 sm:grid-cols-[1fr_1fr_auto] sm:items-start"
              >
                <FormField
                  label="Username"
                  error={fieldError(`basicAuth[${i}].username`)}
                >
                  <Input
                    value={user.username}
                    onChange={(e) => patchUser(i, { username: e.target.value })}
                  />
                </FormField>
                <FormField
                  label="Password"
                  hint={list ? "Leave blank to keep the current one." : undefined}
                  error={fieldError(`basicAuth[${i}].password`)}
                >
                  <Input
                    type="password"
                    autoComplete="new-password"
                    placeholder={list ? "unchanged" : ""}
                    value={user.password ?? ""}
                    onChange={(e) => patchUser(i, { password: e.target.value })}
                  />
                </FormField>
                <Button
                  variant="ghost"
                  size="sm"
                  className="text-muted-foreground sm:mt-7"
                  onClick={() =>
                    patch({ basicAuth: input.basicAuth.filter((_, j) => j !== i) })
                  }
                >
                  <IconTrash />
                  Remove
                </Button>
              </div>
            ))}
          </div>
        </div>

        <SheetFooter>
          <Button disabled={busy} onClick={() => void save()}>
            {busy ? "Saving…" : list ? "Save changes" : "Add access list"}
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
