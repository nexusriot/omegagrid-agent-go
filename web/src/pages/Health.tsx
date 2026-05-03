import { useQuery } from '@tanstack/react-query'
import { Activity, Server, Bot, FolderOpen, RefreshCw, CheckCircle2, XCircle, Loader2, Cpu, type LucideIcon } from 'lucide-react'
import { fetchHealth } from '../api/client'
import type { HealthStatus } from '../api/types'

function Row({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex items-center justify-between py-2.5 border-b border-surface-border last:border-0">
      <span className="text-xs text-gray-500">{label}</span>
      <span className={`text-xs text-gray-300 ${mono ? 'font-mono' : ''}`}>{value}</span>
    </div>
  )
}

function HealthCard({ status }: { status: HealthStatus }) {
  return (
    <div className="rounded-2xl border border-surface-border bg-surface-overlay overflow-hidden">
      {/* Status banner */}
      <div className={[
        'flex items-center gap-2 px-4 py-3 text-sm font-semibold',
        status.ok
          ? 'bg-emerald-950/40 text-emerald-400 border-b border-emerald-900/30'
          : 'bg-red-950/40 text-red-400 border-b border-red-900/30',
      ].join(' ')}>
        {status.ok
          ? <CheckCircle2 size={16} />
          : <XCircle size={16} />
        }
        {status.ok ? 'System healthy' : 'System degraded'}
      </div>

      <div className="px-4">
        <Row label="Provider"     value={status.provider} />
        <Row label="Chat model"   value={status.chat_model} mono />
        <Row label="LLM base URL" value={status.chat_base} mono />
        <Row label="Embed model"  value={`${status.embed_model} ${status.embed_ok ? '✓' : '✗'}`} mono />
        <Row label="Skills dir"   value={status.skills_dir} mono />
        <Row label="Scheduler DB" value={status.scheduler_db} mono />
      </div>
      {!status.embed_ok && status.embed_error && (
        <div className="mx-4 mb-3 rounded-lg border border-red-900/50 bg-red-950/20 px-3 py-2 text-[11px] text-red-400 font-mono break-all">
          Embed error: {status.embed_error}
        </div>
      )}
    </div>
  )
}

function StatCard({ icon: Icon, label, value, sub }: {
  icon: LucideIcon
  label: string
  value: string
  sub?: string
}) {
  return (
    <div className="rounded-2xl border border-surface-border bg-surface-overlay p-4">
      <div className="flex items-center gap-2 mb-3">
        <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-accent/15 text-accent">
          <Icon size={15} />
        </div>
        <span className="text-xs text-gray-500 uppercase tracking-widest">{label}</span>
      </div>
      <div className="font-mono text-base font-semibold text-gray-200">{value}</div>
      {sub && <div className="mt-0.5 text-[11px] text-gray-600">{sub}</div>}
    </div>
  )
}

export default function Health() {
  const { data, isLoading, error, dataUpdatedAt, refetch, isFetching } = useQuery<HealthStatus>({
    queryKey: ['health'],
    queryFn: fetchHealth,
    refetchInterval: 30_000,
  })

  const lastChecked = dataUpdatedAt
    ? new Date(dataUpdatedAt).toLocaleTimeString()
    : '—'

  return (
    <div className="flex h-full flex-col">
      <header className="flex h-12 items-center gap-3 border-b border-surface-border px-5 shrink-0">
        <Activity size={16} className="text-accent" />
        <span className="text-sm font-semibold text-gray-300">Health</span>
        <div className="flex-1" />
        <span className="text-[11px] text-gray-600">Last checked: {lastChecked}</span>
        <button
          onClick={() => refetch()}
          disabled={isFetching}
          title="Refresh"
          className="flex h-7 w-7 items-center justify-center rounded-lg text-gray-500 hover:bg-surface-overlay hover:text-gray-300 transition-colors disabled:opacity-40"
        >
          <RefreshCw size={13} className={isFetching ? 'animate-spin' : ''} />
        </button>
      </header>

      <div className="flex-1 overflow-y-auto p-5 space-y-5">
        {isLoading && (
          <div className="flex items-center justify-center py-20 text-gray-600">
            <Loader2 size={20} className="animate-spin" />
          </div>
        )}

        {error && (
          <div className="rounded-2xl border border-red-900/50 bg-red-950/20 p-4 text-sm text-red-400">
            <XCircle size={16} className="inline mr-2" />
            Gateway unreachable: {(error as Error).message}
          </div>
        )}

        {data && (
          <>
            <HealthCard status={data} />

            <div className="grid grid-cols-2 gap-3">
              <StatCard
                icon={Bot}
                label="Provider"
                value={data.provider}
                sub="LLM backend"
              />
              <StatCard
                icon={Server}
                label="Chat model"
                value={data.chat_model}
                sub="Active chat model"
              />
              <StatCard
                icon={Cpu}
                label="Embed model"
                value={data.embed_model}
                sub={data.embed_ok ? 'Embeddings OK' : (data.embed_error ?? 'Unavailable')}
              />
              <StatCard
                icon={FolderOpen}
                label="Skills"
                value={data.skills_dir.split('/').pop() ?? data.skills_dir}
                sub={data.skills_dir}
              />
            </div>
          </>
        )}
      </div>
    </div>
  )
}
