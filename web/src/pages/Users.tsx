import { useCallback, useEffect, useState } from "react"
import { IconPlus, IconKey, IconTrash } from "@tabler/icons-react"

import { api, ApiError } from "@/api/client"
import type { Role, User } from "@/api/types"
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
import { useAuth } from "@/hooks/useAuth"
import { date } from "@/format"

interface Props {
  canEdit: boolean
}

export function Users({ canEdit }: Props) {
  const { user: me } = useAuth()
  const [users, setUsers] = useState<User[]>([])
  const [adding, setAdding] = useState(false)
  const [resetting, setResetting] = useState<User | null>(null)
  const [error, setError] = useState<string | null>(null)

  const refresh = useCallback(() => {
    api
      .listUsers()
      .then(setUsers)
      .catch(() => setError("Could not load the accounts."))
  }, [])

  useEffect(refresh, [refresh])

  const admins = users.filter((u) => u.role === "admin").length

  async function changeRole(user: User, role: Role) {
    setError(null)
    try {
      await api.updateUserRole(user.id, role)
      refresh()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Could not change the role.")
    }
  }

  async function remove(user: User) {
    if (!confirm(`Delete ${user.username}? They will lose access immediately.`)) {
      return
    }
    setError(null)
    try {
      await api.deleteUser(user.id)
      refresh()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Could not delete the account.")
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

      <Card>
        <CardHeader>
          <CardTitle>Accounts</CardTitle>
          <CardDescription>
            Administrators can change routing and certificates. Viewers can
            watch traffic and nothing else.
          </CardDescription>
          {canEdit && (
            <CardAction>
              <Button size="sm" onClick={() => setAdding(true)}>
                <IconPlus />
                Add account
              </Button>
            </CardAction>
          )}
        </CardHeader>

        <div className="overflow-x-auto px-2 pb-2 sm:px-6 sm:pb-6">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Username</TableHead>
                <TableHead className="w-48">Role</TableHead>
                <TableHead>Created</TableHead>
                <TableHead>Last signed in</TableHead>
                {canEdit && <TableHead />}
              </TableRow>
            </TableHeader>
            <TableBody>
              {users.map((u) => {
                const isMe = u.id === me?.id
                // The API refuses these too; disabling them here just means
                // an operator is not invited to try.
                const lastAdmin = u.role === "admin" && admins <= 1
                return (
                  <TableRow key={u.id}>
                    <TableCell className="font-mono text-xs">
                      {u.username}
                      {isMe && (
                        <span className="text-muted-foreground ml-2 text-xs">you</span>
                      )}
                    </TableCell>
                    <TableCell>
                      {canEdit && !isMe && !lastAdmin ? (
                        <Select
                          value={u.role}
                          onValueChange={(v) => void changeRole(u, v as Role)}
                        >
                          <SelectTrigger className="w-36" size="sm">
                            <SelectValue />
                          </SelectTrigger>
                          <SelectContent>
                            <SelectItem value="admin">Administrator</SelectItem>
                            <SelectItem value="viewer">Viewer</SelectItem>
                          </SelectContent>
                        </Select>
                      ) : (
                        <Badge variant={u.role === "admin" ? "outline" : "secondary"}>
                          {u.role === "admin" ? "Administrator" : "Viewer"}
                        </Badge>
                      )}
                    </TableCell>
                    <TableCell className="text-sm">{date(u.createdAt)}</TableCell>
                    <TableCell className="text-sm">
                      {u.lastLoginAt ? date(u.lastLoginAt) : "never"}
                    </TableCell>
                    {canEdit && (
                      <TableCell className="text-right whitespace-nowrap">
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => setResetting(u)}
                        >
                          <IconKey />
                          Reset password
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          disabled={isMe || lastAdmin}
                          title={
                            isMe
                              ? "You cannot delete the account you are signed in as"
                              : lastAdmin
                                ? "This is the only administrator"
                                : undefined
                          }
                          onClick={() => void remove(u)}
                        >
                          <IconTrash />
                        </Button>
                      </TableCell>
                    )}
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </div>
      </Card>

      {adding && (
        <AccountSheet
          onClose={() => setAdding(false)}
          onSaved={() => {
            setAdding(false)
            refresh()
          }}
        />
      )}
      {resetting && (
        <ResetSheet
          user={resetting}
          onClose={() => setResetting(null)}
          onSaved={() => {
            setResetting(null)
            refresh()
          }}
        />
      )}
    </div>
  )
}

function AccountSheet({
  onClose,
  onSaved,
}: {
  onClose: () => void
  onSaved: () => void
}) {
  const [username, setUsername] = useState("")
  const [password, setPassword] = useState("")
  const [role, setRole] = useState<Role>("viewer")
  const [failure, setFailure] = useState<ApiError | null>(null)
  const [busy, setBusy] = useState(false)

  async function save() {
    setBusy(true)
    setFailure(null)
    try {
      await api.createUser({ username, password, role })
      onSaved()
    } catch (err) {
      setFailure(
        err instanceof ApiError ? err : new ApiError(0, "Could not create the account."),
      )
      setBusy(false)
    }
  }

  return (
    <Sheet open onOpenChange={(open) => !open && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-md">
        <SheetHeader>
          <SheetTitle>Add account</SheetTitle>
          <SheetDescription>
            The password is set once here. Nobody can read it back afterwards —
            an administrator can only replace it.
          </SheetDescription>
        </SheetHeader>

        <div className="flex flex-col gap-4 px-4">
          {failure && failure.fields.length === 0 && (
            <div className="border-destructive/50 bg-destructive/10 text-destructive rounded-md border px-3 py-2 text-sm">
              {failure.message}
            </div>
          )}

          <div className="flex flex-col gap-2">
            <Label htmlFor="new-username">Username</Label>
            <Input
              id="new-username"
              autoComplete="off"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
            />
            <span className="text-muted-foreground text-xs">
              Lower case letters, digits, dot, hyphen and underscore.
            </span>
            {failure?.fieldMessage("username") && (
              <span className="text-destructive text-xs">
                {failure.fieldMessage("username")}
              </span>
            )}
          </div>

          <div className="flex flex-col gap-2">
            <Label htmlFor="new-password">Password</Label>
            <Input
              id="new-password"
              type="password"
              autoComplete="new-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
            <span className="text-muted-foreground text-xs">
              At least 8 characters.
            </span>
            {failure?.fieldMessage("password") && (
              <span className="text-destructive text-xs">
                {failure.fieldMessage("password")}
              </span>
            )}
          </div>

          <div className="flex flex-col gap-2">
            <Label>Role</Label>
            <Select value={role} onValueChange={(v) => setRole(v as Role)}>
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="viewer">
                  Viewer — can watch traffic, change nothing
                </SelectItem>
                <SelectItem value="admin">
                  Administrator — can change routing and certificates
                </SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>

        <SheetFooter>
          <Button disabled={busy} onClick={() => void save()}>
            {busy ? "Creating…" : "Add account"}
          </Button>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}

function ResetSheet({
  user,
  onClose,
  onSaved,
}: {
  user: User
  onClose: () => void
  onSaved: () => void
}) {
  const [password, setPassword] = useState("")
  const [failure, setFailure] = useState<ApiError | null>(null)
  const [busy, setBusy] = useState(false)

  async function save() {
    setBusy(true)
    setFailure(null)
    try {
      await api.resetUserPassword(user.id, password)
      onSaved()
    } catch (err) {
      setFailure(
        err instanceof ApiError ? err : new ApiError(0, "Could not reset the password."),
      )
      setBusy(false)
    }
  }

  return (
    <Sheet open onOpenChange={(open) => !open && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-md">
        <SheetHeader>
          <SheetTitle>Reset {user.username}'s password</SheetTitle>
          <SheetDescription>
            Use this when someone is locked out. Changing your own password
            still asks for the current one.
          </SheetDescription>
        </SheetHeader>

        <div className="flex flex-col gap-4 px-4">
          {failure && failure.fields.length === 0 && (
            <div className="border-destructive/50 bg-destructive/10 text-destructive rounded-md border px-3 py-2 text-sm">
              {failure.message}
            </div>
          )}
          <div className="flex flex-col gap-2">
            <Label htmlFor="reset-password">New password</Label>
            <Input
              id="reset-password"
              type="password"
              autoComplete="new-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
            {failure?.fieldMessage("password") && (
              <span className="text-destructive text-xs">
                {failure.fieldMessage("password")}
              </span>
            )}
          </div>
        </div>

        <SheetFooter>
          <Button disabled={busy} onClick={() => void save()}>
            {busy ? "Resetting…" : "Reset password"}
          </Button>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
