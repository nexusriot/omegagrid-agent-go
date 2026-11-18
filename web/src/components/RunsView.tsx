import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Loader2, X, ChevronDown } from 'lucide-react'
import { fetchRuns, fetchRunStats } from '../api/client'
import type { RunRecord, RunStats } from '../api/types'

const fmtMs = (ms: number) => (ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}s`)
const fmtNum = (n: number) => (n >= 10_000 ? `${(n / 1000).toFixed(1)}k` : String(n))

const outcomeCls: Record<string, string> = {
  final: 'bg-emerald-950/60 text-emerald-400',
  fallback: 'bg-amber-950/60 text-amber-400',
  max_steps: 'bg-amber-950/60 text-amber-400',
  error: 'bg-red-950/60 text-red-400',
  cancelled: 'bg-gray-800 text-gray-400',
}

function Outcome({ o }: { o: string }) {
  return <span className={`rounded px-1.5 py-0.5 text-[10px] font-mono ${outcomeCls[o] ?? outcomeCls.cancelled}`}>{o}</span>
}

function Tile({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="rounded-xl border border-surface-border bg-surface-overlay px-4 py-3" title={hint}>
      <div className="text-[10px] uppercase tracking-widest text-gray-600">{label}</div>
      <div className="mt-1 text-lg font-semibold tabular-nums text-gray-200">{value}</div>
    </div>
  )
}

function StatsPanel({ st }: { st: RunStats }) {
  const misses = st.tools.filter((t) => t.used_not_offered > 0).slice(0, 8)
  return (
    <div className="space-y-3">
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-6">
        <Tile label="Runs (7d)" value={String(st.runs)} />
        <Tile label="Avg / p95" value={`${fmtMs(Math.round(st.avg_duration_ms))} / ${fmtMs(st.p95_duration_ms)}`} />
        <Tile label="LLM calls / run" value={st.avg_llm_calls.toFixed(1)} />
        <Tile
          label="Tokens in / out"
          value={`${fmtNum(st.prompt_tokens)} / ${fmtNum(st.completion_tokens)}`}
          hint="Only backends that report usage are counted"
        />
        <Tile label="Approval wait" value={fmtMs(st.approval_wait_ms)} />
        <Tile
          label="Retrieval misses"
          value={st.retrieval_runs ? `${Math.round(st.miss_rate * 100)}%` : '—'}
          hint="Share of retrieval runs that used a tool the prompt had not listed (found via find_tools or called by name)"
        />
      </div>
      {misses.length > 0 && (
        <div className="rounded-xl border border-amber-900/40 bg-amber-950/10 px-4 py-2 text-[11px] text-gray-400">
          <span className="text-amber-400">Ranked too low for the queries that needed them: </span>
          {misses.map((t, i) => (
            <span key={t.tool}>
              {i > 0 && ', '}
              <code className="text-indigo-300">{t.tool}</code> ({t.used_not_offered}/{t.used})
            </span>
          ))}
          <span className="text-gray-600"> — check with GET /api/tools/rank?q=… or raise AGENT_TOOL_TOP_K.</span>
        </div>
      )}
    </div>
  )
}

function RunDrawer({ run, onClose }: { run: RunRecord; onClose: () => void }) {
  const used = [...new Set(run.used)]
  const offered = new Set(run.offered)
  return (
    <div className="fixed inset-0 z-50 flex justify-end">
      <div className="absolute inset-0 bg-black/50" onClick={onClose} />
      <div className="relative z-10 flex w-full max-w-lg flex-col overflow-hidden border-l border-surface-border bg-surface-raised shadow-2xl animate-slide-up">
        <div className="flex items-center gap-3 border-b border-surface-border px-5 py-3">
          <Outcome o={run.outcome} />
          <span className="flex-1 truncate text-sm text-gray-300">Run #{run.id} · session #{run.session_id}</span>
          <button onClick={onClose} className="text-gray-600 hover:text-gray-300" aria-label="Close">
            <X size={16} />
          </button>
        </div>
        <div className="flex-1 space-y-4 overflow-y-auto p-5 text-xs text-gray-400">
          <p className="whitespace-pre-wrap text-sm text-gray-200">{run.query}</p>
          {run.error && <p className="rounded-lg bg-red-950/30 p-2 text-red-400">{run.error}</p>}
          <dl className="grid grid-cols-2 gap-x-4 gap-y-1">
            <dt>Model</dt><dd className="font-mono text-gray-300">{run.model}</dd>
            <dt>Tool mode</dt><dd>{run.tool_mode}</dd>
            <dt>Retrieval</dt><dd>{run.retrieval}{run.offered.length > 0 && ` (${run.offered.length} offered)`}</dd>
            <dt>Duration</dt><dd>{fmtMs(run.duration_ms)} (LLM {fmtMs(run.llm_ms)}, tools {fmtMs(run.tool_ms)})</dd>
            <dt>Steps / LLM calls</dt><dd>{run.steps} / {run.llm_calls}</dd>
            <dt>Tokens in / out</dt><dd>{run.prompt_tokens} / {run.completion_tokens}</dd>
            {run.approval_wait_ms > 0 && (<><dt>Approval wait</dt><dd>{fmtMs(run.approval_wait_ms)}</dd></>)}
            {run.unattended && (<><dt>Unattended</dt><dd>scheduled task</dd></>)}
          </dl>
          <div>
            <div className="mb-1 text-[10px] uppercase tracking-widest text-gray-600">Tools used</div>
            {used.length === 0 ? '—' : (
              <div className="flex flex-wrap gap-1">
                {used.map((u) => (
                  <code
                    key={u}
                    title={run.offered.length && !offered.has(u) ? 'not in the prompt: found via find_tools or called by name' : ''}
                    className={`rounded px-1.5 py-0.5 ${run.offered.length && !offered.has(u) ? 'bg-amber-950/60 text-amber-300' : 'bg-indigo-950/60 text-indigo-300'}`}
                  >
                    {u}
                  </code>
                ))}
              </div>
            )}
          </div>
          {run.offered.length > 0 && (
            <div>
              <div className="mb-1 text-[10px] uppercase tracking-widest text-gray-600">Offered by retrieval</div>
              <p className="font-mono text-[11px] leading-relaxed text-gray-500">{run.offered.join(', ')}</p>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

export default function RunsView() {
  const [outcome, setOutcome] = useState('')
  const [selected, setSelected] = useState<RunRecord | null>(null)
  const runs = useQuery({
    queryKey: ['runs', outcome],
    queryFn: () => fetchRuns({ outcome: outcome || undefined, limit: 100 }),
    refetchInterval: 10_000,
  })
  const stats = useQuery({ queryKey: ['run-stats'], queryFn: () => fetchRunStats(7), refetchInterval: 30_000 })
  const list = runs.data?.runs ?? []
  const inputCls = 'rounded-lg border border-surface-border bg-surface-overlay px-3 py-1.5 text-xs text-gray-300 outline-none focus:border-accent/50'

  return (
    <div className="flex h-full flex-col">
      <div className="flex flex-wrap items-center gap-2 border-b border-surface-border px-5 py-2">
        <div className="relative">
          <select value={outcome} onChange={(e) => setOutcome(e.target.value)} className={`${inputCls} cursor-pointer appearance-none pr-6`}>
            <option value="">All outcomes</option>
            {['final', 'fallback', 'max_steps', 'error', 'cancelled'].map((o) => <option key={o} value={o}>{o}</option>)}
          </select>
          <ChevronDown size={11} className="pointer-events-none absolute right-2 top-1/2 -translate-y-1/2 text-gray-600" />
        </div>
        <div className="flex-1" />
        {runs.data && <span className="text-xs text-gray-600">{runs.data.total} run{runs.data.total !== 1 ? 's' : ''}</span>}
      </div>
      <div className="flex-1 space-y-3 overflow-y-auto p-5">
        {stats.data && stats.data.runs > 0 && <StatsPanel st={stats.data} />}
        {runs.isLoading && (
          <div className="flex justify-center py-20 text-gray-600"><Loader2 size={20} className="animate-spin" /></div>
        )}
        {runs.error && (
          <div className="rounded-2xl border border-red-900/50 bg-red-950/20 p-4 text-sm text-red-400">
            Failed to load runs: {(runs.error as Error).message}
          </div>
        )}
        <div className="space-y-2">
          {list.map((r) => (
            <button
              key={r.id}
              onClick={() => setSelected(r)}
              className="flex w-full items-center gap-3 rounded-xl border border-surface-border bg-surface-overlay px-4 py-3 text-left transition-colors hover:bg-white/5"
            >
              <Outcome o={r.outcome} />
              <span className="min-w-0 flex-1 truncate text-[12px] text-gray-300">{r.query}</span>
              <span className="hidden shrink-0 text-[11px] text-gray-600 md:block">
                {[...new Set(r.used)].slice(0, 3).join(', ')}{new Set(r.used).size > 3 ? '…' : ''}
              </span>
              <span className="shrink-0 text-[11px] tabular-nums text-gray-600">{r.llm_calls}×LLM</span>
              {r.prompt_tokens > 0 && (
                <span className="hidden shrink-0 text-[11px] tabular-nums text-gray-600 sm:block">{fmtNum(r.prompt_tokens + r.completion_tokens)} tok</span>
              )}
              <span className="shrink-0 text-[11px] tabular-nums text-gray-600">{fmtMs(r.duration_ms)}</span>
              <span className="hidden shrink-0 text-[11px] text-gray-600 lg:block">{new Date(r.ts * 1000).toLocaleString()}</span>
            </button>
          ))}
        </div>
        {!runs.isLoading && !runs.error && list.length === 0 && (
          <p className="py-20 text-center text-sm text-gray-500">No runs recorded yet.</p>
        )}
      </div>
      {selected && <RunDrawer run={selected} onClose={() => setSelected(null)} />}
    </div>
  )
}
