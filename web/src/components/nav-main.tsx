import { IconCirclePlusFilled, type Icon } from "@tabler/icons-react"

import {
  SidebarGroup,
  SidebarGroupContent,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
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

interface Props {
  items: NavItem[]
  active: Section
  onSelect: (section: Section) => void
  onAddHost: () => void
  canEdit: boolean
}

export function NavMain({ items, active, onSelect, onAddHost, canEdit }: Props) {
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
          {items.map((item) => (
            <SidebarMenuItem key={item.id}>
              <SidebarMenuButton
                tooltip={item.title}
                isActive={active === item.id}
                onClick={() => onSelect(item.id)}
              >
                <item.icon />
                <span>{item.title}</span>
                {item.badge && (
                  <span
                    className={`ml-auto font-mono text-xs tabular-nums ${
                      item.alarm ? "text-destructive" : "text-muted-foreground"
                    }`}
                  >
                    {item.badge}
                  </span>
                )}
              </SidebarMenuButton>
            </SidebarMenuItem>
          ))}
        </SidebarMenu>
      </SidebarGroupContent>
    </SidebarGroup>
  )
}
