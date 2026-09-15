/** Formatting shared across the console. Numbers an operator scans in a column
 *  need a consistent width and a predictable number of digits, so these all
 *  return short strings rather than exact ones. */

export function rate(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return '0'
  if (value < 10) return value.toFixed(1)
  return Math.round(value).toLocaleString()
}

export function count(value: number): string {
  return Math.round(value).toLocaleString()
}

export function bytes(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return '0 B'
  const units = ['B', 'kB', 'MB', 'GB', 'TB']
  let n = value
  let i = 0
  while (n >= 1000 && i < units.length - 1) {
    n /= 1000
    i += 1
  }
  return `${n < 10 && i > 0 ? n.toFixed(1) : Math.round(n)} ${units[i]}`
}

export function bytesPerSec(value: number): string {
  return `${bytes(value)}/s`
}

export function millis(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return '0'
  if (value < 10) return value.toFixed(1)
  return String(Math.round(value))
}

export function percent(fraction: number, digits = 1): string {
  if (!Number.isFinite(fraction)) return '0'
  return (fraction * 100).toFixed(digits)
}

export function duration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return '—'
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m`
  return `${Math.floor(seconds)}s`
}

/** nanosToSeconds converts a Go time.Duration, which arrives as a count of
 *  nanoseconds, into the seconds the forms display. */
export function nanosToSeconds(nanos: number): number {
  return Math.round(nanos / 1_000_000_000)
}

export function relativeDays(days: number): string {
  if (days < 0) return `expired ${Math.abs(days)}d ago`
  if (days === 0) return 'expires today'
  if (days === 1) return 'expires tomorrow'
  return `expires in ${days}d`
}

const dateFormat = new Intl.DateTimeFormat(undefined, {
  year: 'numeric',
  month: 'short',
  day: 'numeric',
})

export function date(iso: string | undefined): string {
  if (!iso) return '—'
  const parsed = new Date(iso)
  return Number.isNaN(parsed.getTime()) ? '—' : dateFormat.format(parsed)
}

const timeFormat = new Intl.DateTimeFormat(undefined, {
  hour: '2-digit',
  minute: '2-digit',
})

export function clock(iso: string): string {
  const parsed = new Date(iso)
  return Number.isNaN(parsed.getTime()) ? '' : timeFormat.format(parsed)
}
