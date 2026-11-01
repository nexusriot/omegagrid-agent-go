import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { BarChart3, Loader2 } from 'lucide-react'
import { fetchUsage } from '../api/client'
import type { UsageBucket, UsageStats } from '../api/types'

const RANGES = [
  { label: '24h', days: 1 },
  { label: '7d', days: 7 },
  { label: '30d', days: 30 },
] as const

const COLORS = {
  queries: '#6366f1', // accent
  tools: '#38bdf8',
  errors: '#f87171',
  grid: '#2a2d3e', // surface-border
  label: '#6b7280',
}

const HEIGHT = 180
const PAD = { top: 10, right: 8, bottom: 22, left: 30 }

function useWidth<T extends HTMLElement>(): [React.RefObject<T>, number] {
  const ref = useRef<T>(null)
  const [w, setW] = useState(600)
  useEffect(() => {
    const el = ref.current
    if (!el) return
    const ro = new ResizeObserver(([e]) => setW(Math.max(200, Math.floor(e.contentRect.width))))
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  return [ref, w]
}

function bucketLabel(b: UsageBucket, kind: UsageStats['bucket']): string {
  const d = new Date(b.start * 1000)
  return kind === 'hour'
    ? d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
    : d.toLocaleDateString([], { month: 'short', day: 'numeric' })
}

function niceMax(v: number): number {
  if (v <= 4) return 4
  const p = Math.pow(10, Math.floor(Math.log10(v)))
  for (const m of [1, 2, 2.5, 5, 10]) if (m * p >= v) return m * p
  return 10 * p
}

function UsageChart({ usage }: { usage: UsageStats }) {
  const [ref, width] = useWidth<HTMLDivElement>()
  const buckets = usage.buckets ?? []
  const n = Math.max(1, buckets.length)
  const max = niceMax(Math.max(0, ...buckets.map((b) => Math.max(b.queries, b.tool_calls))))
  const plotW = width - PAD.left - PAD.right
  const plotH = HEIGHT - PAD.top - PAD.bottom
  const slot = plotW / n
  const barW = Math.max(1, Math.min(14, (slot - 2) / 2))
  const y = (v: number) => PAD.top + plotH - (v / max) * plotH
  const labelEvery = Math.max(1, Math.ceil(n / Math.max(2, Math.floor(plotW / 70))))
  const ticks = [0, max / 2, max]

  return (
    <div ref={ref} className="w-full">
      <svg width={width} height={HEIGHT} role="img" aria-label="Queries and tool calls per bucket">
        {ticks.map((t) => (
          <g key={t}>
            <line x1={PAD.left} x2={width - PAD.right} y1={y(t)} y2={y(t)} stroke={COLORS.grid} strokeDasharray={t ? '3 3' : undefined} />
            <text x={PAD.left - 5} y={y(t) + 3} textAnchor="end" fontSize={9} fill={COLORS.label}>
              {Number.isInteger(t) ? t : t.toFixed(1)}
            </text>
          </g>
        ))}
        {buckets.map((b, i) => {
          const x0 = PAD.left + i * slot + (slot - 2 * barW) / 2
          const okCalls = b.tool_calls - b.tool_errors
          const tip = [
            bucketLabel(b, usage.bucket) + (usage.bucket === 'day' ? '' : ` ${new Date(b.start * 1000).toLocaleDateString([], { month: 'short', day: 'numeric' })}`),
            `${b.queries} quer${b.queries === 1 ? 'y' : 'ies'}`,
            `${b.tool_calls} tool call${b.tool_calls === 1 ? '' : 's'}${b.tool_errors ? ` (${b.tool_errors} failed)` : ''}`,
            b.tool_calls ? `avg ${Math.round(b.avg_tool_ms)} ms` : '',
          ].filter(Boolean).join('\n')
          return (
            <g key={b.start}>
              <title>{tip}</title>
              <rect x={PAD.left + i * slot} y={PAD.top} width={slot} height={plotH} fill="transparent" className="hover:fill-white/5" />
              {b.queries > 0 && (
                <rect x={x0} y={y(b.queries)} width={barW} height={y(0) - y(b.queries)} rx={1} fill={COLORS.queries} />
              )}
              {okCalls > 0 && (
                <rect x={x0 + barW} y={y(b.tool_calls)} width={barW} height={y(0) - y(okCalls)} rx={1} fill={COLORS.tools} />
              )}
              {b.tool_errors > 0 && (
                <rect x={x0 + barW} y={y(b.tool_errors)} width={barW} height={y(0) - y(b.tool_errors)} rx={1} fill={COLORS.errors} />
              )}
              {i % labelEvery === 0 && (
                <text x={PAD.left + i * slot + slot / 2} y={HEIGHT - 6} textAnchor="middle" fontSize={9} fill={COLORS.label}>
                  {bucketLabel(b, usage.bucket)}
                </text>
              )}
            </g>
          )
        })}
      </svg>
    </div>
  )
}

function Legend({ color, label }: { color: string; label: string }) {
  return (
    <span className="flex items-center gap-1.5">
      <span className="inline-block h-2 w-2 rounded-sm" style={{ background: color }} />
      {label}
    </span>
  )
}

export default function UsagePanel() {
  const [days, setDays] = useState<number>(7)
  const { data, isLoading, error, isFetching } = useQuery({
    queryKey: ['usage', days],
    queryFn: () => fetchUsage(days),
    refetchInterval: 60_000,
    placeholderData: (prev) => prev,
  })

  const buckets = data?.buckets ?? []
  const totals = buckets.reduce(
    (t, b) => ({ q: t.q + b.queries, c: t.c + b.tool_calls, e: t.e + b.tool_errors }),
    { q: 0, c: 0, e: 0 },
  )
  const skills = data?.skills ?? []

  return (
    <div className="rounded-2xl border border-surface-border bg-surface-overlay p-4 space-y-4">
      <div className="flex items-center gap-2">
        <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-accent/15 text-accent">
          <BarChart3 size={15} />
        </div>
        <span className="text-xs text-gray-500 uppercase tracking-widest">Usage</span>
        {isFetching && <Loader2 size={12} className="animate-spin text-gray-600" />}
        <div className="flex-1" />
        <div className="flex gap-1 rounded-lg border border-surface-border bg-surface p-0.5">
          {RANGES.map((r) => (
            <button
              key={r.days}
              onClick={() => setDays(r.days)}
              className={`rounded-md px-2.5 py-1 text-[11px] font-medium transition-colors ${
                days === r.days ? 'bg-accent/20 text-accent' : 'text-gray-500 hover:text-gray-300'
              }`}
            >
              {r.label}
            </button>
          ))}
        </div>
      </div>

      {isLoading && (
        <div className="flex items-center justify-center py-10 text-gray-600"><Loader2 size={16} className="animate-spin" /></div>
      )}
      {error && <p className="text-xs text-red-400">Failed to load usage: {(error as Error).message}</p>}

      {data && (
        <>
          <div className="flex flex-wrap items-center gap-x-5 gap-y-1 text-[11px] text-gray-500">
            <span><span className="font-mono text-sm text-gray-200">{totals.q}</span> queries</span>
            <span><span className="font-mono text-sm text-gray-200">{totals.c}</span> tool calls</span>
            <span>
              <span className={`font-mono text-sm ${totals.e ? 'text-red-400' : 'text-gray-200'}`}>{totals.e}</span> errors
              {totals.c > 0 && ` (${((totals.e / totals.c) * 100).toFixed(1)}%)`}
            </span>
            <div className="flex-1" />
            <Legend color={COLORS.queries} label="queries" />
            <Legend color={COLORS.tools} label="tool calls" />
            <Legend color={COLORS.errors} label="errors" />
          </div>

          <UsageChart usage={data} />

          <div>
            <div className="mb-2 text-[10px] uppercase tracking-widest text-gray-600">Top skills</div>
            {skills.length === 0 ? (
              <p className="py-3 text-center text-xs text-gray-600">No tool calls in this range</p>
            ) : (
              <table className="w-full text-left text-xs">
                <thead>
                  <tr className="text-[10px] uppercase tracking-widest text-gray-600">
                    <th className="pb-1.5 font-normal">Skill</th>
                    <th className="pb-1.5 font-normal text-right">Calls</th>
                    <th className="pb-1.5 font-normal text-right">Errors</th>
                    <th className="pb-1.5 font-normal text-right">Avg</th>
                  </tr>
                </thead>
                <tbody>
                  {skills.slice(0, 15).map((s) => {
                    const pct = s.calls ? (s.errors / s.calls) * 100 : 0
                    return (
                      <tr key={s.skill} className="border-t border-surface-border">
                        <td className="py-1.5 font-mono text-indigo-300">{s.skill}</td>
                        <td className="py-1.5 text-right font-mono text-gray-300">{s.calls}</td>
                        <td className={`py-1.5 text-right font-mono ${pct > 0 ? 'text-red-400' : 'text-gray-600'}`}>
                          {pct.toFixed(pct > 0 && pct < 10 ? 1 : 0)}%
                        </td>
                        <td className="py-1.5 text-right font-mono text-gray-400">{Math.round(s.avg_ms)} ms</td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            )}
          </div>
        </>
      )}
    </div>
  )
}
