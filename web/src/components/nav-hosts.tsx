import { useState } from "react"
import { IconDots, IconPencil, IconWorld } from "@tabler/icons-react"

import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  SidebarGroup,
  SidebarGroupLabel,
  SidebarMenu,
  SidebarMenuAction,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from "@/components/ui/sidebar"
import type { Host, HostSnapshot } from "@/api/types"
import { cn } from "@/lib/utils"

/** How many hosts are listed before the rest collapse behind "More". Beyond
 *  this the sidebar starts scrolling, which defeats the point of a glanceable
 *  list. */
const VISIBLE = 5

interface Props {
  hosts: Host[]
  live: HostSnapshot[]
  onOpen: (host: Host) => void
  onEdit: (host: Host) => void
  canEdit: boolean
}

/** NavHosts lists the configured hosts with their current health.
 *
 *  It occupies the slot the dashboard-01 block gives to a documents list, but
 *  carries something an operator actually needs at a glance: whether every
 *  upstream behind each host is answering. A red count here is the fastest
 *  path from "something is wrong" to "which host". */
export function NavHosts({ hosts, live, onOpen, onEdit, canEdit }: Props) {
  const { isMobile } = useSidebar()
  const [expanded, setExpanded] = useState(false)

  if (hosts.length === 0) return null

  const healthOf = (hostId: number) => {
    const snapshot = live.find((h) => h.hostId === hostId)
    const upstreams = snapshot?.upstreams ?? []
    if (upstreams.length === 0) return null
    const enabled = upstreams.filter((u) => u.enabled)
    const up = enabled.filter((u) => u.healthy).length
    return { up, total: enabled.length }
  }

  const shown = expanded ? hosts : hosts.slice(0, VISIBLE)

  return (
    <SidebarGroup className="group-data-[collapsible=icon]:hidden">
      <SidebarGroupLabel>Hosts</SidebarGroupLabel>
      <SidebarMenu>
        {shown.map((host) => {
          const health = healthOf(host.id)
          const degraded = health !== null && health.up < health.total
          return (
            <SidebarMenuItem key={host.id}>
              <SidebarMenuButton
                onClick={() => onOpen(host)}
                title={host.domains.join(", ")}
              >
                <IconWorld
                  className={cn(
                    degraded && "text-destructive",
                    !host.enabled && "text-muted-foreground",
                  )}
                />
                <span className="truncate">{host.domains[0] ?? host.name}</span>
                {health && (
                  <span
                    className={cn(
                      "ml-auto font-mono text-xs tabular-nums",
                      degraded ? "text-destructive" : "text-muted-foreground",
                    )}
                  >
                    {health.up}/{health.total}
                  </span>
                )}
              </SidebarMenuButton>

              {canEdit && (
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <SidebarMenuAction showOnHover>
                      <IconDots />
                      <span className="sr-only">More</span>
                    </SidebarMenuAction>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent
                    className="w-32 rounded-lg"
                    side={isMobile ? "bottom" : "right"}
                    align={isMobile ? "end" : "start"}
                  >
                    <DropdownMenuItem onClick={() => onEdit(host)}>
                      <IconPencil />
                      <span>Edit</span>
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
              )}
            </SidebarMenuItem>
          )
        })}

        {hosts.length > VISIBLE && (
          <SidebarMenuItem>
            <SidebarMenuButton
              className="text-sidebar-foreground/70"
              onClick={() => setExpanded((v) => !v)}
            >
              <IconDots className="text-sidebar-foreground/70" />
              <span>
                {expanded ? "Show fewer" : `${hosts.length - VISIBLE} more`}
              </span>
            </SidebarMenuButton>
          </SidebarMenuItem>
        )}
      </SidebarMenu>
    </SidebarGroup>
  )
}
