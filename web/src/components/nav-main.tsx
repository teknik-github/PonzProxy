import { useEffect, useState } from "react"
import { IconChevronRight, IconCirclePlusFilled, type Icon } from "@tabler/icons-react"
import { Collapsible } from "radix-ui"

import {
  SidebarGroup,
  SidebarGroupContent,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
} from "@/components/ui/sidebar"
import type { Section } from "@/sections"

export interface NavItem {
  id: Section
  title: string
  icon: Icon
  /** Shown on the right of the row — a count, or a health summary. */
  badge?: string | undefined
  /** Marks the badge as a problem rather than a plain figure. */
  alarm?: boolean | undefined
}

/** NavGroup is a heading with its own screens under it. The console grew past
 *  the point where one flat list of eleven rows could be scanned: grouping
 *  turns "where was that" into "which of four things am I doing". */
export interface NavGroup {
  id: string
  title: string
  icon: Icon
  items: NavItem[]
}

interface Props {
  groups: NavGroup[]
  active: Section
  onSelect: (section: Section) => void
  onAddHost: () => void
  canEdit: boolean
}

const STORAGE_KEY = "ponzproxy.sidebar.collapsed"

/** loadCollapsed reads which groups the operator folded away.
 *
 *  Wrapped because localStorage throws in a private window and in an iframe
 *  with site data blocked, and a sidebar that cannot remember a preference is
 *  a much smaller problem than a console that will not render. */
function loadCollapsed(): Set<string> {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return new Set()
    const parsed: unknown = JSON.parse(raw)
    return Array.isArray(parsed) ? new Set(parsed.filter((v) => typeof v === "string")) : new Set()
  } catch {
    return new Set()
  }
}

export function NavMain({ groups, active, onSelect, onAddHost, canEdit }: Props) {
  const [collapsed, setCollapsed] = useState<Set<string>>(loadCollapsed)

  useEffect(() => {
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify([...collapsed]))
    } catch {
      // See loadCollapsed: not being able to remember is not an error.
    }
  }, [collapsed])

  const toggle = (id: string, open: boolean) =>
    setCollapsed((prev) => {
      const next = new Set(prev)
      if (open) next.delete(id)
      else next.add(id)
      return next
    })

  return (
    <SidebarGroup>
      <SidebarGroupContent className="flex flex-col gap-2">
        {canEdit && (
          <SidebarMenu>
            <SidebarMenuItem className="flex items-center gap-2">
              <SidebarMenuButton
                tooltip="Add host"
                onClick={onAddHost}
                className="bg-primary text-primary-foreground hover:bg-primary/90 hover:text-primary-foreground active:bg-primary/90 active:text-primary-foreground min-w-8 duration-200 ease-linear"
              >
                <IconCirclePlusFilled />
                <span>Add host</span>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        )}

        <SidebarMenu>
          {groups.map((group) => {
            const holdsActive = group.items.some((i) => i.id === active)
            // A group is never folded while the screen you are on lives
            // inside it, or the sidebar would stop showing where you are.
            const open = holdsActive || !collapsed.has(group.id)
            const worst = group.items.find((i) => i.alarm)

            return (
              <Collapsible.Root
                key={group.id}
                open={open}
                onOpenChange={(next) => toggle(group.id, next)}
                className="group/collapsible"
              >
                <SidebarMenuItem>
                  <Collapsible.Trigger asChild>
                    <SidebarMenuButton
                      tooltip={group.title}
                      // Folding a group is not navigation, so the heading
                      // never takes the active styling its children do.
                      className="cursor-pointer"
                    >
                      <group.icon />
                      <span>{group.title}</span>
                      {/* A problem inside a folded group has to be visible
                          from the outside, or folding hides an incident. */}
                      {worst && !open && (
                        <span className="bg-destructive ml-auto size-1.5 rounded-full" />
                      )}
                      <IconChevronRight
                        className={`ml-auto transition-transform duration-200 ${
                          open ? "rotate-90" : ""
                        } ${worst && !open ? "!ml-1.5" : ""}`}
                      />
                    </SidebarMenuButton>
                  </Collapsible.Trigger>

                  <Collapsible.Content className="overflow-hidden">
                    <SidebarMenuSub>
                      {group.items.map((item) => (
                        <SidebarMenuSubItem key={item.id}>
                          <SidebarMenuSubButton
                            asChild
                            isActive={active === item.id}
                          >
                            <button
                              type="button"
                              onClick={() => onSelect(item.id)}
                              className="w-full cursor-pointer"
                            >
                              <item.icon className="size-4" />
                              <span>{item.title}</span>
                              {item.badge && (
                                <span
                                  className={`ml-auto font-mono text-xs tabular-nums ${
                                    item.alarm
                                      ? "text-destructive"
                                      : "text-muted-foreground"
                                  }`}
                                >
                                  {item.badge}
                                </span>
                              )}
                            </button>
                          </SidebarMenuSubButton>
                        </SidebarMenuSubItem>
                      ))}
                    </SidebarMenuSub>
                  </Collapsible.Content>
                </SidebarMenuItem>
              </Collapsible.Root>
            )
          })}
        </SidebarMenu>
      </SidebarGroupContent>
    </SidebarGroup>
  )
}
