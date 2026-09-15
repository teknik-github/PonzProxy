import { useState, type FormEvent } from "react"

import { api, ApiError } from "@/api/client"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { useAuth } from "@/hooks/useAuth"
import { date } from "@/format"

export function Account() {
  const { user } = useAuth()
  const [current, setCurrent] = useState("")
  const [next, setNext] = useState("")
  const [confirm, setConfirm] = useState("")
  const [failure, setFailure] = useState<ApiError | null>(null)
  const [done, setDone] = useState(false)
  const [busy, setBusy] = useState(false)

  const mismatch = confirm !== "" && next !== confirm

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (mismatch) return
    setBusy(true)
    setFailure(null)
    setDone(false)
    try {
      await api.changePassword(current, next)
      setCurrent("")
      setNext("")
      setConfirm("")
      setDone(true)
    } catch (err) {
      setFailure(
        err instanceof ApiError
          ? err
          : new ApiError(0, "Could not change the password."),
      )
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex flex-col gap-4 px-4 lg:px-6">
      <Card>
        <CardHeader>
          <CardTitle>Account</CardTitle>
          <CardDescription>Who you are signed in as</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-6 sm:grid-cols-3">
          <div>
            <div className="font-mono text-lg">{user?.username}</div>
            <div className="text-muted-foreground text-xs">signed in as</div>
          </div>
          <div>
            <div className="text-lg">
              {user?.role === "admin" ? "Administrator" : "Viewer"}
            </div>
            <div className="text-muted-foreground text-xs">
              {user?.role === "admin"
                ? "can change routing and certificates"
                : "can watch traffic but not change anything"}
            </div>
          </div>
          <div>
            <div className="text-lg">{date(user?.createdAt)}</div>
            <div className="text-muted-foreground text-xs">account created</div>
          </div>
        </CardContent>
      </Card>

      <Card className="max-w-lg">
        <CardHeader>
          <CardTitle>Change password</CardTitle>
          <CardDescription>
            Your current password is required, so a stolen session alone cannot
            lock you out.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form className="flex flex-col gap-4" onSubmit={submit}>
            {failure && failure.fields.length === 0 && (
              <div className="border-destructive/50 bg-destructive/10 text-destructive rounded-md border px-3 py-2 text-sm">
                {failure.message}
              </div>
            )}
            {done && (
              <div className="rounded-md border border-emerald-500/50 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-700 dark:text-emerald-400">
                Password changed.
              </div>
            )}

            <div className="flex flex-col gap-2">
              <Label htmlFor="current">Current password</Label>
              <Input
                id="current"
                type="password"
                autoComplete="current-password"
                required
                value={current}
                onChange={(e) => setCurrent(e.target.value)}
              />
            </div>

            <div className="flex flex-col gap-2">
              <Label htmlFor="next">New password</Label>
              <Input
                id="next"
                type="password"
                autoComplete="new-password"
                required
                value={next}
                onChange={(e) => setNext(e.target.value)}
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
              <Label htmlFor="confirm">Confirm new password</Label>
              <Input
                id="confirm"
                type="password"
                autoComplete="new-password"
                required
                value={confirm}
                onChange={(e) => setConfirm(e.target.value)}
              />
              {mismatch && (
                <span className="text-destructive text-xs">
                  The two passwords do not match.
                </span>
              )}
            </div>

            <Button type="submit" className="self-start" disabled={busy || mismatch}>
              {busy ? "Changing…" : "Change password"}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
