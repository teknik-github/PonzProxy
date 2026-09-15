import * as React from "react"

const MOBILE_BREAKPOINT = 768

/** useIsMobile reports whether the viewport is narrow enough that the sidebar
 *  should behave as a drawer. shadcn's sidebar expects this hook to exist. */
export function useIsMobile() {
  const [isMobile, setIsMobile] = React.useState<boolean | undefined>(undefined)

  React.useEffect(() => {
    const query = window.matchMedia(`(max-width: ${MOBILE_BREAKPOINT - 1}px)`)
    const update = () => setIsMobile(window.innerWidth < MOBILE_BREAKPOINT)
    query.addEventListener("change", update)
    update()
    return () => query.removeEventListener("change", update)
  }, [])

  return !!isMobile
}
