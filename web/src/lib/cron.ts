// Client-side preview of when a cron expression fires. It mirrors
// internal/scheduler/cron.go + runner.go, which is what actually runs the
// task: each real minute is converted to wall-clock fields in the task's
// timezone ("" = UTC) and matched there, so a DST fall-back hour can fire
// twice and a spring-forward gap never fires. Month/weekday names and weekday
// 7 (= Sunday) are accepted, "N/S" means N through the field maximum in steps
// of S, and day-of-month / day-of-week follow Vixie semantics (OR when both
// are restricted; a field starting with "*" counts as unrestricted).
const CRON_NAMES: Record<string, number> = {
  jan: 1, feb: 2, mar: 3, apr: 4, may: 5, jun: 6, jul: 7, aug: 8, sep: 9, oct: 10, nov: 11, dec: 12,
  sun: 0, mon: 1, tue: 2, wed: 3, thu: 4, fri: 5, sat: 6,
}

const WEEKDAYS: Record<string, number> = { Sun: 0, Mon: 1, Tue: 2, Wed: 3, Thu: 4, Fri: 5, Sat: 6 }

export const COMMON_TIMEZONES = [
  'UTC', 'Europe/London', 'Europe/Berlin', 'Europe/Paris', 'Europe/Kyiv', 'Europe/Moscow',
  'America/New_York', 'America/Chicago', 'America/Denver', 'America/Los_Angeles', 'America/Sao_Paulo',
  'Asia/Dubai', 'Asia/Kolkata', 'Asia/Singapore', 'Asia/Shanghai', 'Asia/Tokyo',
  'Australia/Sydney', 'Pacific/Auckland',
]

export function browserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || ''
  } catch {
    return ''
  }
}

export interface ZonedParts {
  year: number
  month: number // 1-12
  day: number
  hour: number // 0-23
  minute: number
  weekday: number // 0 = Sunday
}

const formatters = new Map<string, Intl.DateTimeFormat>()

/** Throws RangeError for an unknown zone. "" means UTC. */
function zoneFormatter(tz: string): Intl.DateTimeFormat {
  const key = tz || 'UTC'
  let f = formatters.get(key)
  if (!f) {
    f = new Intl.DateTimeFormat('en-US', {
      timeZone: key,
      hourCycle: 'h23',
      year: 'numeric', month: 'numeric', day: 'numeric',
      hour: 'numeric', minute: 'numeric', weekday: 'short',
    })
    formatters.set(key, f)
  }
  return f
}

export function isValidTimezone(tz: string): boolean {
  try {
    zoneFormatter(tz)
    return true
  } catch {
    return false
  }
}

/** Wall-clock fields of the instant `ms` in zone `tz` ("" = UTC). */
export function zonedParts(ms: number, tz: string): ZonedParts {
  const p: Record<string, string> = {}
  for (const part of zoneFormatter(tz).formatToParts(new Date(ms))) p[part.type] = part.value
  return {
    year: parseInt(p.year, 10),
    month: parseInt(p.month, 10),
    day: parseInt(p.day, 10),
    hour: parseInt(p.hour, 10) % 24,
    minute: parseInt(p.minute, 10),
    weekday: WEEKDAYS[p.weekday] ?? 0,
  }
}

/** "HH:MM" of the instant in zone `tz` ("" = UTC). */
export function fmtZonedHM(d: Date, tz: string): string {
  const p = zonedParts(d.getTime(), tz)
  return `${String(p.hour).padStart(2, '0')}:${String(p.minute).padStart(2, '0')}`
}

function cronAtoi(tok: string): number {
  const t = tok.trim().toLowerCase()
  if (t in CRON_NAMES) return CRON_NAMES[t]
  return /^\d+$/.test(t) ? parseInt(t, 10) : NaN
}

function cronFieldMatches(field: string, value: number, lo: number): boolean {
  for (const part of field.split(',')) {
    const trimmed = part.trim()
    let step = 1
    let base = trimmed
    const slashIdx = trimmed.indexOf('/')
    if (slashIdx >= 0) {
      step = parseInt(trimmed.slice(slashIdx + 1), 10)
      if (isNaN(step) || step <= 0) continue
      base = trimmed.slice(0, slashIdx)
    }
    if (base === '*') {
      if ((value - lo) % step === 0) return true
      continue
    }
    const dashIdx = base.indexOf('-')
    if (dashIdx >= 0) {
      const a = cronAtoi(base.slice(0, dashIdx))
      const b = cronAtoi(base.slice(dashIdx + 1))
      if (!isNaN(a) && !isNaN(b) && value >= a && value <= b && (value - a) % step === 0) return true
      continue
    }
    const n = cronAtoi(base)
    if (isNaN(n)) continue
    if (n === value || (slashIdx >= 0 && value >= n && (value - n) % step === 0)) return true
  }
  return false
}

function cronDayMatches(parts: string[], p: ZonedParts): boolean {
  if (!cronFieldMatches(parts[3], p.month, 1)) return false
  const wd = p.weekday
  const dom = cronFieldMatches(parts[2], p.day, 1)
  const dow = cronFieldMatches(parts[4], wd, 0) || (wd === 0 && cronFieldMatches(parts[4], 7, 0))
  const domRestricted = !parts[2].startsWith('*')
  const dowRestricted = !parts[4].startsWith('*')
  return domRestricted && dowRestricted ? dom || dow : dom && dow
}

const MINUTE = 60_000

/**
 * The next `count` fire times of `expr` evaluated in zone `tz` ("" = UTC), or
 * null when the expression is malformed, the zone unknown, or fewer than
 * `count` fires exist in the next ~5 years.
 */
export function nextCronFires(expr: string, count: number, tz = ''): Date[] | null {
  const parts = expr.trim().split(/\s+/)
  if (parts.length !== 5 || !isValidTimezone(tz)) return null
  const results: Date[] = []
  let t = Math.floor(Date.now() / MINUTE) * MINUTE + MINUTE
  const end = t + 366 * 5 * 24 * 60 * MINUTE
  // Zone offsets only change on 15-minute UTC boundaries, so the wall-clock
  // fields are computed once per 15-minute chunk and whole non-matching
  // days/hours are skipped. A skip stops an hour short of local midnight so
  // a 23-hour DST day cannot be jumped over.
  while (t < end && results.length < count) {
    const p = zonedParts(t, tz)
    const chunk = Math.min(15 - (new Date(t).getUTCMinutes() % 15), 60 - p.minute)
    if (!cronDayMatches(parts, p)) {
      const toMidnight = 24 * 60 - (p.hour * 60 + p.minute)
      t += Math.max(chunk, toMidnight - 60) * MINUTE
      continue
    }
    if (!cronFieldMatches(parts[1], p.hour, 0)) {
      t += (60 - p.minute) * MINUTE
      continue
    }
    for (let k = 0; k < chunk && results.length < count; k++) {
      if (cronFieldMatches(parts[0], p.minute + k, 0)) results.push(new Date(t + k * MINUTE))
    }
    t += chunk * MINUTE
  }
  return results.length === count ? results : null
}
