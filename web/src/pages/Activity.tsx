import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  History, X, RotateCcw, Loader2, CheckCircle2, XCircle,
  ChevronDown, AlertCircle,
} from 'lucide-react'
import { toast } from 'sonner'
import { fetchInvocations, fetchSkills, fetchSessions, replayInvocation } from '../api/client'
import type { Invocation, InvocationFilter, Session } from '../api/types'

function fmtTs(ts: number): string {
  return new Date(ts * 1000).toLocaleString()
}

function fmtDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(2)}s`
}

function KindBadge({ kind }: { kind: Invocation['kind'] }) {
  const map: Record<string, string> = {
    skill:   'bg-indigo-950/60 text-indigo-400',
    tool:    'bg-blue-950/60 text-blue-400',
    replay:  'bg-amber-950/60 text-amber-400',
    unknown: 'bg-red-950/60 text-red-400',
  }
  return (
    <span className={`rounded px-1.5 py-0.5 text-[10px] font-mono ${map[kind] ?? map.unknown}`}>
      {kind}
    </span>
  )
}

function StatusIcon({ error }: { error?: string }) {
  return error
    ? <XCircle size={14} className="text-red-400 shrink-0" />
    : <CheckCircle2 size={14} className="text-emerald-400 shrink-0" />
}

function JsonBlock({ value, label }: { value: unknown; label: string }) {
  const text = value == null ? '—' : JSON.stringify(value, null, 2)
  return (
    <div>
      <div className="mb-1 text-[10px] uppercase tracking-widest text-gray-600">{label}</div>
      <pre className="overflow-x-auto rounded-lg bg-surface p-3 font-mono text-[11px] text-gray-400 max-h-64 whitespace-pre-wrap break-all">
        {text}
      </pre>
    </div>
  )
}

function Drawer({ inv, onClose }: { inv: Invocation; onClose: () => void }) {
  const qc = useQueryClient()
  const replayMut = useMutation({
    mutationFn: () => replayInvocation(inv.id),
    onSuccess: (r) => {
      toast.success(`Replayed in ${fmtDuration(r.duration_ms)}${r.error ? ' (with error)' : ''}`)
      qc.invalidateQueries({ queryKey: ['invocations'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const canReplay = inv.kind === 'skill'

  return (
    <div className="fixed inset-0 z-50 flex justify-end">
      <div className="absolute inset-0 bg-black/40" onClick={onClose} />
      <div className="relative z-10 flex w-full max-w-lg flex-col border-l border-surface-border bg-surface-raised shadow-2xl animate-slide-up overflow-hidden">
        {/* Header */}
        <div className="flex items-center gap-3 border-b border-surface-border px-5 py-3 shrink-0">
          <StatusIcon error={inv.error} />
          <code className="flex-1 font-mono text-sm font-semibold text-indigo-300 truncate">{inv.skill}</code>
          <KindBadge kind={inv.kind} />
          <button onClick={onClose} className="text-gray-600 hover:text-gray-300 ml-2">
            <X size={16} />
          </button>
        </div>

        {/* Meta row */}
        <div className="flex gap-6 border-b border-surface-border px-5 py-2 text-[11px] text-gray-500 shrink-0">
          <span>Session <span className="text-gray-400">{inv.session_id}</span></span>
          <span>Step <span className="text-gray-400">{inv.step}</span></span>
          <span>Duration <span className="text-gray-400">{fmtDuration(inv.duration_ms)}</span></span>
          <span className="ml-auto">{fmtTs(inv.ts)}</span>
        </div>

        {/* Body */}
        <div className="flex-1 overflow-y-auto p-5 space-y-4">
          {inv.why && (
            <div>
              <div className="mb-1 text-[10px] uppercase tracking-widest text-gray-600">Why</div>
              <p className="text-xs text-gray-400 italic">{inv.why}</p>
            </div>
          )}

          {inv.error && (
            <div className="flex items-start gap-2 rounded-xl border border-red-900/50 bg-red-950/20 p-3 text-xs text-red-400">
              <AlertCircle size={14} className="shrink-0 mt-0.5" />
              {inv.error}
            </div>
          )}

          <JsonBlock value={inv.args} label="Args" />
          <JsonBlock value={inv.result} label="Result" />

          {inv.replayed_from != null && (
            <p className="text-[11px] text-gray-600">
              Replayed from invocation #{inv.replayed_from}
            </p>
          )}
        </div>

        {/* Footer */}
        <div className="border-t border-surface-border px-5 py-3 shrink-0 flex justify-end">
          {canReplay && (
            <button
              onClick={() => replayMut.mutate()}
              disabled={replayMut.isPending}
              className="flex items-center gap-2 rounded-xl bg-accent/20 px-4 py-2 text-xs font-medium text-accent hover:bg-accent/30 disabled:opacity-40 transition-colors"
            >
              {replayMut.isPending
                ? <Loader2 size={12} className="animate-spin" />
                : <RotateCcw size={12} />}
              Replay
            </button>
          )}
        </div>
      </div>
    </div>
  )
}

function InvocationRow({ inv, onClick }: { inv: Invocation; onClick: () => void }) {
  return (
    <button
      onClick={onClick}
      className="flex w-full items-center gap-3 rounded-xl border border-surface-border bg-surface-overlay px-4 py-3 text-left hover:bg-white/5 transition-colors animate-fade-in"
    >
      <StatusIcon error={inv.error} />
      <code className="w-36 shrink-0 truncate font-mono text-[12px] text-indigo-300">{inv.skill}</code>
      <KindBadge kind={inv.kind} />
      <span className="flex-1 truncate text-[11px] text-gray-500 hidden sm:block">{inv.why || '—'}</span>
      <span className="shrink-0 text-[11px] text-gray-600 tabular-nums">{fmtDuration(inv.duration_ms)}</span>
      <span className="shrink-0 text-[11px] text-gray-600 hidden md:block">{fmtTs(inv.ts)}</span>
      <span className="shrink-0 text-[10px] text-gray-700">#{inv.session_id}</span>
    </button>
  )
}

export default function Activity() {
  const [skill, setSkill]         = useState('')
  const [sessionId, setSessionId] = useState<number | ''>('')
  const [onlyErrors, setOnlyErrors] = useState(false)
  const [selected, setSelected]   = useState<Invocation | null>(null)

  const filter: InvocationFilter = {
    skill:       skill || undefined,
    session_id:  sessionId || undefined,
    only_errors: onlyErrors || undefined,
    limit:       100,
  }

  const { data, isLoading, error } = useQuery({
    queryKey: ['invocations', filter],
    queryFn:  () => fetchInvocations(filter),
    refetchInterval: 10_000,
  })

  const { data: skills = [] } = useQuery({
    queryKey: ['skills'],
    queryFn:  fetchSkills,
    staleTime: 60_000,
  })

  const { data: sessions = [] } = useQuery<Session[]>({
    queryKey: ['sessions'],
    queryFn:  () => fetchSessions(100),
    staleTime: 30_000,
  })

  const invocations = data?.invocations ?? []
  const total       = data?.total ?? 0

  const inputCls = 'rounded-lg border border-surface-border bg-surface-overlay px-3 py-1.5 text-xs text-gray-300 outline-none focus:border-accent/50'

  return (
    <div className="flex h-full flex-col">
      <header className="flex h-12 items-center gap-3 border-b border-surface-border px-5 shrink-0">
        <History size={16} className="text-accent" />
        <span className="text-sm font-semibold text-gray-300">Activity</span>
        <div className="flex-1" />
        {!isLoading && (
          <span className="text-xs text-gray-600">{total} invocation{total !== 1 ? 's' : ''}</span>
        )}
      </header>

      {/* Filter bar */}
      <div className="flex flex-wrap items-center gap-2 border-b border-surface-border px-5 py-2 shrink-0">
        {/* Skill filter */}
        <div className="relative">
          <select
            value={skill}
            onChange={(e) => setSkill(e.target.value)}
            className={`${inputCls} appearance-none pr-6 cursor-pointer`}
          >
            <option value="">All skills</option>
            {skills.map((s) => <option key={s.name} value={s.name}>{s.name}</option>)}
          </select>
          <ChevronDown size={11} className="pointer-events-none absolute right-2 top-1/2 -translate-y-1/2 text-gray-600" />
        </div>

        {/* Session filter */}
        <div className="relative">
          <select
            value={sessionId}
            onChange={(e) => setSessionId(e.target.value ? Number(e.target.value) : '')}
            className={`${inputCls} appearance-none pr-6 cursor-pointer`}
          >
            <option value="">All sessions</option>
            {sessions.map((s) => (
              <option key={s.id} value={s.id}>#{s.id}</option>
            ))}
          </select>
          <ChevronDown size={11} className="pointer-events-none absolute right-2 top-1/2 -translate-y-1/2 text-gray-600" />
        </div>

        {/* Errors only toggle */}
        <button
          onClick={() => setOnlyErrors((v) => !v)}
          className={[
            'flex items-center gap-1.5 rounded-lg border px-3 py-1.5 text-xs transition-colors',
            onlyErrors
              ? 'border-red-800/60 bg-red-950/30 text-red-400'
              : 'border-surface-border bg-surface-overlay text-gray-500 hover:text-gray-300',
          ].join(' ')}
        >
          <XCircle size={11} /> Errors only
        </button>
      </div>

      {/* List */}
      <div className="flex-1 overflow-y-auto p-5 space-y-2">
        {isLoading && (
          <div className="flex items-center justify-center py-20 text-gray-600">
            <Loader2 size={20} className="animate-spin" />
          </div>
        )}

        {error && (
          <div className="rounded-2xl border border-red-900/50 bg-red-950/20 p-4 text-sm text-red-400">
            Failed to load activity: {(error as Error).message}
          </div>
        )}

        {invocations.map((inv) => (
          <InvocationRow key={inv.id} inv={inv} onClick={() => setSelected(inv)} />
        ))}

        {!isLoading && !error && invocations.length === 0 && (
          <div className="flex flex-col items-center justify-center py-20 gap-3 text-center">
            <div className="flex h-12 w-12 items-center justify-center rounded-2xl bg-accent/10 text-accent">
              <History size={22} />
            </div>
            <p className="text-sm text-gray-500">No invocations recorded yet</p>
            <p className="text-xs text-gray-700 max-w-xs">
              Skill and tool calls will appear here as the agent runs.
            </p>
          </div>
        )}
      </div>

      {selected && <Drawer inv={selected} onClose={() => setSelected(null)} />}
    </div>
  )
}
