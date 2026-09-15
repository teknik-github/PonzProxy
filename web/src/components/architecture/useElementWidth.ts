import { useEffect, useState, type RefObject } from "react"

/** useElementWidth reports the measured width of an element in CSS pixels.
 *
 *  The diagram lays itself out in real pixels rather than scaling a fixed
 *  viewBox, because a viewBox scaled to 400px would take the labels down with
 *  it and an unreadable label is worse than a redrawn one.
 *
 *  The width is quantised to 8px steps: a resize otherwise produces a new path
 *  string on every animation frame, and every one of those restarts the flow
 *  animation on every link. */
export function useElementWidth(ref: RefObject<HTMLElement | null>): number {
  const [width, setWidth] = useState(0)

  useEffect(() => {
    const element = ref.current
    if (!element) return

    const apply = (px: number) => setWidth(Math.max(0, Math.floor(px / 8) * 8))

    apply(element.getBoundingClientRect().width)
    const observer = new ResizeObserver((entries) => {
      const entry = entries[0]
      if (entry) apply(entry.contentRect.width)
    })
    observer.observe(element)
    return () => observer.disconnect()
  }, [ref])

  return width
}
