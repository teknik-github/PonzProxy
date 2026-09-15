import { useState, type FormEvent } from "react"

import { ApiError } from "@/api/client"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { useAuth } from "@/hooks/useAuth"
import { cn } from "@/lib/utils"

/** LoginForm is the login-03 block wired to ponzproxy's own accounts.
 *
 *  The block ships with Apple and Google buttons and a sign-up link. None of
 *  those exist here: ponzproxy is a self-hosted appliance whose accounts live
 *  in its own database, so offering them would be offering something that
 *  cannot work. */
export function LoginForm({
  className,
  ...props
}: React.ComponentProps<"div">) {
  const { signIn } = useAuth()
  const [username, setUsername] = useState("")
  const [password, setPassword] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent) {
    event.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await signIn(username, password)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Sign-in failed. Try again.")
      setBusy(false)
    }
  }

  return (
    <div className={cn("flex flex-col gap-6", className)} {...props}>
      <Card>
        <CardHeader className="text-center">
          <CardTitle className="text-xl">Sign in</CardTitle>
          <CardDescription>
            Sign in to route traffic, manage certificates and watch what the
            proxy is doing.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={submit}>
            <FieldGroup>
              {error && (
                <div
                  role="alert"
                  className="border-destructive/50 bg-destructive/10 text-destructive rounded-md border px-3 py-2 text-sm"
                >
                  {error}
                </div>
              )}
              <Field>
                <FieldLabel htmlFor="username">Username</FieldLabel>
                <Input
                  id="username"
                  type="text"
                  autoComplete="username"
                  autoFocus
                  required
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="password">Password</FieldLabel>
                <Input
                  id="password"
                  type="password"
                  autoComplete="current-password"
                  required
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                />
              </Field>
              <Field>
                <Button type="submit" disabled={busy}>
                  {busy ? "Signing in…" : "Sign in"}
                </Button>
                <FieldDescription className="text-center">
                  The first account and its password are printed to the
                  server's console the first time ponzproxy starts.
                </FieldDescription>
              </Field>
            </FieldGroup>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
