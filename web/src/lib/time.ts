/** "in 3d", "in 5h", "in 12m", or "expired" for a unix-seconds deadline. */
export function fmtRelativeFuture(unixSec: number, now = Date.now()): string {
  const s = Math.round(unixSec - now / 1000)
  if (s <= 0) return 'expired'
  if (s < 3600) return `in ${Math.max(1, Math.round(s / 60))}m`
  if (s < 86400 * 2) return `in ${Math.round(s / 3600)}h`
  return `in ${Math.round(s / 86400)}d`
}

export function fmtDateTime(unixSec: number): string {
  return new Date(unixSec * 1000).toLocaleString([], {
    month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit',
  })
}
