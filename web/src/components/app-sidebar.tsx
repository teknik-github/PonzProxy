import {
  IconActivity,
  IconArrowsExchange2,
  IconBell,
  IconCertificate,
  IconFileText,
  IconRouteAltLeft,
  IconServer2,
  IconShieldLock,
  IconUserCog,
  IconUsersGroup,
} from "@tabler/icons-react"

import { NavHosts } from "@/components/nav-hosts"
import { NavMain, type NavItem } from "@/components/nav-main"
import { NavStatus } from "@/components/nav-status"
import { NavUser } from "@/components/nav-user"
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarSeparator,
} from "@/components/ui/sidebar"
import type { AccessList, Certificate, Host, Snapshot } from "@/api/types"
import type { LinkState } from "@/hooks/useLiveFeed"
import type { Section } from "@/sections"
import { cn } from "@/lib/utils"

interface Props extends React.ComponentProps<typeof Sidebar> {
  section: Section
  onNavigate: (section: Section) => void
  onAddHost: () => void
  onEditHost: (host: Host) => void
  link: LinkState
  hosts: Host[]
  certificates: Certificate[]
  accessLists: AccessList[]
  snapshot: Snapshot | null
  canEdit: boolean
}

export function AppSidebar({
  section,
  onNavigate,
  onAddHost,
  onEditHost,
  link,
  hosts,
  certificates,
  accessLists,
  snapshot,
  canEdit,
  ...props
}: Props) {
  // Certificates inside their renewal window are worth surfacing before they
  // expire, so the count doubles as a warning.
  const expiring = certificates.filter(
    (c) => c.installed && c.expiresInDays < 14,
  ).length

  const items: NavItem[] = [
    { id: "traffic", title: "Traffic", icon: IconActivity },
    {
      id: "hosts",
      title: "Hosts",
      icon: IconServer2,
      badge: hosts.length > 0 ? String(hosts.length) : undefined,
    },
    { id: "redirects", title: "Redirects", icon: IconArrowsExchange2 },
    {
      id: "access-lists",
      title: "Access lists",
      icon: IconShieldLock,
      badge: accessLists.length > 0 ? String(accessLists.length) : undefined,
    },
    { id: "access-log", title: "Access log", icon: IconFileText },
    {
      id: "certificates",
      title: "Certificates",
      icon: IconCertificate,
      badge:
        expiring > 0
          ? `${expiring} expiring`
          : certificates.length > 0
            ? String(certificates.length)
            : undefined,
      alarm: expiring > 0,
    },
    { id: "alerts", title: "Alerts", icon: IconBell },
    { id: "users", title: "Users", icon: IconUsersGroup },
    { id: "account", title: "Account", icon: IconUserCog },
  ]

  return (
    <Sidebar collapsible="offcanvas" {...props}>
      <SidebarHeader>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton
              asChild
              className="data-[slot=sidebar-menu-button]:!p-1.5"
            >
              <span>
                <IconRouteAltLeft className="!size-5" />
                <span className="text-base font-semibold">ponzproxy</span>
                {/* The feed indicator sits beside the mark because a stale
                    dashboard is worse than no dashboard. */}
                <span
                  className={cn(
                    "ml-auto size-2 rounded-full",
                    link === "live" ? "bg-emerald-500" : "bg-destructive",
                  )}
                  aria-label={
                    link === "live" ? "Live feed connected" : "Live feed lost"
                  }
                />
              </span>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>

      <SidebarContent>
        <NavMain
          items={items}
          active={section}
          onSelect={onNavigate}
          onAddHost={onAddHost}
          canEdit={canEdit}
        />
        <NavHosts
          hosts={hosts}
          live={snapshot?.hosts ?? []}
          onOpen={() => onNavigate("hosts")}
          onEdit={onEditHost}
          canEdit={canEdit}
        />
        <NavStatus
          system={snapshot?.system}
          requestsPerSec={snapshot?.totals.requestsPerSec ?? 0}
        />
      </SidebarContent>

      <SidebarFooter>
        <SidebarSeparator />
        <NavUser />
      </SidebarFooter>
    </Sidebar>
  )
}
