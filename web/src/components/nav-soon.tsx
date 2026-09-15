import { IconBell, IconUsersGroup, type Icon } from "@tabler/icons-react"

import {
  SidebarGroup,
  SidebarGroupLabel,
  SidebarMenu,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
} from "@/components/ui/sidebar"

interface Planned {
  title: string
  icon: Icon
  /** Shown on hover, so the roadmap explains itself rather than teasing. */
  detail: string
}

/** What ponzproxy is missing to stand next to nginx proxy manager. These are
 *  listed rather than hidden so an operator can tell at a glance whether the
 *  thing they came looking for exists yet. */
const PLANNED: Planned[] = [
  {
    title: "Alerts",
    icon: IconBell,
    detail: "Notify when an upstream drops out or a certificate is near expiry.",
  },
  {
    title: "Users",
    icon: IconUsersGroup,
    detail: "Add operator accounts and viewers from the console.",
  },
]

export function NavSoon() {
  return (
    <SidebarGroup className="group-data-[collapsible=icon]:hidden">
      <SidebarGroupLabel>Not built yet</SidebarGroupLabel>
      <SidebarMenu>
        {PLANNED.map((item) => (
          <SidebarMenuItem key={item.title}>
            {/* Deliberately a disabled button, not a link: these screens do
                not exist, and a nav item that navigates nowhere is worse
                than one that plainly says it is not ready. */}
            <SidebarMenuButton
              disabled
              tooltip={item.detail}
              className="text-muted-foreground cursor-default opacity-70 hover:bg-transparent"
              aria-disabled="true"
            >
              <item.icon />
              <span>{item.title}</span>
            </SidebarMenuButton>
            <SidebarMenuBadge className="text-muted-foreground peer-hover/menu-button:text-muted-foreground">
              Soon
            </SidebarMenuBadge>
          </SidebarMenuItem>
        ))}
      </SidebarMenu>
    </SidebarGroup>
  )
}
