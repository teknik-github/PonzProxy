import {
  IconActivity,
  IconAdjustments,
  IconArrowsExchange2,
  IconBell,
  IconCertificate,
  IconChartBar,
  IconEye,
  IconFileText,
  IconGauge,
  IconRoute,
  IconRouteAltLeft,
  IconServer2,
  IconSitemap,
  IconShieldHalf,
  IconShieldLock,
  IconUserCog,
  IconUsersGroup,
} from "@tabler/icons-react"

import { NavHosts } from "@/components/nav-hosts"
import { NavMain, type NavGroup } from "@/components/nav-main"
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

  // How many hosts have limits switched on at all. A count rather than a
  // warning: limits being on is the intended state, not a problem.
  const limited = hosts.filter((h) => h.trafficLimits.mode !== "off").length

  // Four groups rather than one list of twelve rows. The split follows the
  // questions an operator arrives with — what is happening, what do I serve,
  // who can reach it, who runs this — because that is what they can answer
  // before they have found the screen.
  const groups: NavGroup[] = [
    {
      id: "monitor",
      title: "Monitor",
      icon: IconEye,
      items: [
        { id: "traffic", title: "Traffic", icon: IconActivity },
        { id: "architecture", title: "Request path", icon: IconSitemap },
        { id: "usage", title: "Traffic used", icon: IconChartBar },
        { id: "access-log", title: "Access log", icon: IconFileText },
        { id: "alerts", title: "Alerts", icon: IconBell },
      ],
    },
    {
      id: "routing",
      title: "Routing",
      icon: IconRoute,
      items: [
        {
          id: "hosts",
          title: "Hosts",
          icon: IconServer2,
          badge: hosts.length > 0 ? String(hosts.length) : undefined,
        },
        { id: "redirects", title: "Redirects", icon: IconArrowsExchange2 },
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
      ],
    },
    {
      id: "protection",
      title: "Protection",
      icon: IconShieldHalf,
      items: [
        {
          id: "access-lists",
          title: "Access lists",
          icon: IconShieldLock,
          badge: accessLists.length > 0 ? String(accessLists.length) : undefined,
        },
        {
          id: "traffic-limits",
          title: "Traffic limits",
          icon: IconGauge,
          badge: limited > 0 ? String(limited) : undefined,
        },
      ],
    },
    {
      id: "settings",
      title: "Settings",
      icon: IconAdjustments,
      items: [
        { id: "users", title: "Users", icon: IconUsersGroup },
        { id: "account", title: "Account", icon: IconUserCog },
      ],
    },
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
          groups={groups}
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
