import { useCallback, useEffect, useState } from "react"

import { api } from "@/api/client"
import type { AccessList, Certificate, Host } from "@/api/types"
import { AppSidebar } from "@/components/app-sidebar"
import { SiteHeader } from "@/components/site-header"
import { Button } from "@/components/ui/button"
import { SidebarInset, SidebarProvider } from "@/components/ui/sidebar"
import { useAuth } from "@/hooks/useAuth"
import { useLiveFeed } from "@/hooks/useLiveFeed"
import { Account } from "@/pages/Account"
import { Certificates } from "@/pages/Certificates"
import { AccessLists } from "@/pages/AccessLists"
import { AccessLog } from "@/pages/AccessLog"
import { Alerts } from "@/pages/Alerts"
import { Hosts } from "@/pages/Hosts"
import { Redirects } from "@/pages/Redirects"
import { SignIn } from "@/pages/SignIn"
import { Traffic } from "@/pages/Traffic"
import { Users } from "@/pages/Users"
import type { Section } from "@/sections"

export function App() {
  const { user, checking } = useAuth()
  const [section, setSection] = useState<Section>("traffic")
  // Raised by the sidebar's quick action and by its per-host edit menu, then
  // consumed by the Hosts screen so the sheet opens on the right record.
  const [hostToOpen, setHostToOpen] = useState<Host | "new" | null>(null)
  const [hosts, setHosts] = useState<Host[]>([])
  const [certificates, setCertificates] = useState<Certificate[]>([])
  const [accessLists, setAccessLists] = useState<AccessList[]>([])
  const [loadError, setLoadError] = useState<string | null>(null)

  const { snapshot, link } = useLiveFeed(user !== null)

  const refresh = useCallback(() => {
    if (!user) return
    Promise.all([api.listHosts(), api.listCertificates(), api.listAccessLists()])
      .then(([h, c, a]) => {
        setHosts(h)
        setCertificates(c)
        setAccessLists(a)
        setLoadError(null)
      })
      .catch(() => setLoadError("Could not load the configuration."))
  }, [user])

  useEffect(refresh, [refresh])

  if (checking) {
    // A blank frame rather than a spinner: the check is one request, and a
    // spinner that flashes for 40ms is noise.
    return <div className="bg-background min-h-svh" />
  }

  if (!user) return <SignIn />

  const canEdit = user.role === "admin"

  return (
    <SidebarProvider
      style={
        {
          "--sidebar-width": "calc(var(--spacing) * 72)",
          "--header-height": "calc(var(--spacing) * 12)",
        } as React.CSSProperties
      }
    >
      <AppSidebar
        variant="inset"
        section={section}
        onNavigate={setSection}
        onAddHost={() => {
          setSection("hosts")
          setHostToOpen("new")
        }}
        onEditHost={(host) => {
          setSection("hosts")
          setHostToOpen(host)
        }}
        link={link}
        hosts={hosts}
        certificates={certificates}
        accessLists={accessLists}
        snapshot={snapshot}
        canEdit={canEdit}
      />
      <SidebarInset>
        <SiteHeader section={section} link={link} />
        <div className="flex flex-1 flex-col">
          <div className="@container/main flex flex-1 flex-col gap-2">
            <div className="flex flex-col gap-4 py-4 md:gap-6 md:py-6">
              {loadError && (
                <div className="px-4 lg:px-6">
                  <div
                    role="alert"
                    className="border-destructive/50 bg-destructive/10 text-destructive flex items-center gap-3 rounded-md border px-3 py-2 text-sm"
                  >
                    {loadError}
                    <Button
                      variant="ghost"
                      size="sm"
                      className="ml-auto"
                      onClick={refresh}
                    >
                      Retry
                    </Button>
                  </div>
                </div>
              )}

              {section === "traffic" && (
                <Traffic snapshot={snapshot} hosts={hosts} />
              )}
              {section === "hosts" && (
                <Hosts
                  hosts={hosts}
                  certificates={certificates}
                  accessLists={accessLists}
                  canEdit={canEdit}
                  onChanged={refresh}
                  openFromSidebar={hostToOpen}
                  onSidebarOpenHandled={() => setHostToOpen(null)}
                />
              )}
              {section === "redirects" && (
                <Redirects
                  certificates={certificates}
                  canEdit={canEdit}
                  onChanged={refresh}
                />
              )}
              {section === "access-lists" && (
                <AccessLists
                  accessLists={accessLists}
                  canEdit={canEdit}
                  onChanged={refresh}
                />
              )}
              {section === "access-log" && <AccessLog hosts={hosts} />}
              {section === "certificates" && (
                <Certificates
                  certificates={certificates}
                  canEdit={canEdit}
                  onChanged={refresh}
                />
              )}
              {section === "alerts" && <Alerts canEdit={canEdit} />}
              {section === "users" && <Users canEdit={canEdit} />}
              {section === "account" && <Account />}
            </div>
          </div>
        </div>
      </SidebarInset>
    </SidebarProvider>
  )
}
