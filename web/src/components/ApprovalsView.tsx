import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Loader2, ShieldCheck, ShieldX, XCircle } from 'lucide-react'
import { toast } from 'sonner'
import { fetchApprovalGrants, fetchApprovalHistory, revokeApprovalGrants } from '../api/client'

const fmtMs = (ms: number) => (ms < 1000 ? `${ms}ms` : ms < 60_000 ? `${(ms / 1000).toFixed(1)}s` : `${Math.round(ms / 60_000)}m`)

/** Approval history, active session grants and the configured allow-rules. */
export default function ApprovalsView() {
  const qc = useQueryClient()
  const [onlyDenied, setOnlyDenied] = useState(false)
  const history = useQuery({
    queryKey: ['approval-history', onlyDenied],
    queryFn: () => fetchApprovalHistory({ only_denied: onlyDenied, limit: 100 }),
    refetchInterval: 10_000,
  })
  const grants = useQuery({ queryKey: ['approval-grants'], queryFn: fetchApprovalGrants, refetchInterval: 10_000 })
  const revoke = useMutation({
    mutationFn: (sid?: number) => revokeApprovalGrants(sid),
    onSuccess: (r) => {
      toast.success(`Revoked ${r.revoked} grant${r.revoked !== 1 ? 's' : ''}`)
      qc.invalidateQueries({ queryKey: ['approval-grants'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const list = history.data?.approvals ?? []
  const g = grants.data

  return (
    <div className="flex h-full flex-col">
      <div className="flex flex-wrap items-center gap-2 border-b border-surface-border px-5 py-2">
        <button
          onClick={() => setOnlyDenied((v) => !v)}
          className={[
            'flex items-center gap-1.5 rounded-lg border px-3 py-1.5 text-xs transition-colors',
            onlyDenied ? 'border-red-800/60 bg-red-950/30 text-red-400' : 'border-surface-border bg-surface-overlay text-gray-500 hover:text-gray-300',
          ].join(' ')}
        >
          <XCircle size={11} /> Denied only
        </button>
        <div className="flex-1" />
        {history.data && <span className="text-xs text-gray-600">{history.data.total} decision{history.data.total !== 1 ? 's' : ''}</span>}
      </div>
      <div className="flex-1 space-y-4 overflow-y-auto p-5">
        {g && !g.enabled && <p className="text-sm text-gray-500">The approval gate is off.</p>}
        {g && g.enabled && (
          <div className="grid gap-3 md:grid-cols-2">
            <section className="rounded-xl border border-surface-border bg-surface-overlay p-4">
              <div className="mb-2 flex items-center">
                <h3 className="flex-1 text-[10px] uppercase tracking-widest text-gray-600">Session grants</h3>
                {g.grants.length > 0 && (
                  <button onClick={() => revoke.mutate(undefined)} className="text-[11px] text-red-400 hover:text-red-300">Revoke all</button>
                )}
              </div>
              {g.grants.length === 0 ? (
                <p className="text-xs text-gray-600">None. “Approve for session” on an approval card creates one.</p>
              ) : (
                <ul className="space-y-1 text-xs">
                  {g.grants.map((gr) => (
                    <li key={`${gr.session_id}:${gr.tool}`} className="flex items-center gap-2">
                      <code className="text-indigo-300">{gr.tool}</code>
                      <span className="text-gray-500">session #{gr.session_id}</span>
                      <span className="flex-1 text-gray-600">until {new Date(gr.expires_at).toLocaleTimeString()}</span>
                      <button onClick={() => revoke.mutate(gr.session_id)} className="text-[11px] text-red-400 hover:text-red-300">Revoke</button>
                    </li>
                  ))}
                </ul>
              )}
            </section>
            <section className="rounded-xl border border-surface-border bg-surface-overlay p-4">
              <h3 className="mb-2 text-[10px] uppercase tracking-widest text-gray-600">Allow-rules (APPROVAL_ALLOW)</h3>
              {g.rules.length === 0 ? (
                <p className="text-xs text-gray-600">None configured.</p>
              ) : (
                <ul className="space-y-1">
                  {g.rules.map((r) => <li key={r}><code className="break-all text-[11px] text-gray-300">{r}</code></li>)}
                </ul>
              )}
            </section>
          </div>
        )}
        {history.isLoading && <div className="flex justify-center py-20 text-gray-600"><Loader2 size={20} className="animate-spin" /></div>}
        {history.error && (
          <div className="rounded-2xl border border-red-900/50 bg-red-950/20 p-4 text-sm text-red-400">
            Failed to load approvals: {(history.error as Error).message}
          </div>
        )}
        <div className="space-y-2">
          {list.map((a) => (
            <details key={a.id} className="rounded-xl border border-surface-border bg-surface-overlay px-4 py-2">
              <summary className="flex cursor-pointer list-none items-center gap-3 text-xs">
                {a.approved ? <ShieldCheck size={14} className="shrink-0 text-emerald-400" /> : <ShieldX size={14} className="shrink-0 text-red-400" />}
                <code className="w-36 shrink-0 truncate text-indigo-300">{a.tool}</code>
                <span className="shrink-0 rounded bg-surface px-1.5 py-0.5 font-mono text-[10px] text-gray-400">
                  {a.by}{a.scope === 'session' ? ' · session' : ''}
                </span>
                <span className="min-w-0 flex-1 truncate text-gray-500">{a.reason || a.why || ''}</span>
                {a.wait_ms > 0 && <span className="shrink-0 tabular-nums text-gray-600">{fmtMs(a.wait_ms)}</span>}
                <span className="hidden shrink-0 text-gray-600 md:block">{new Date(a.ts * 1000).toLocaleString()}</span>
                <span className="shrink-0 text-[10px] text-gray-700">#{a.session_id}</span>
              </summary>
              <pre className="mt-2 max-h-48 overflow-auto whitespace-pre-wrap break-all rounded-lg bg-black/30 p-2 text-[11px] text-gray-400">
                {JSON.stringify(a.args, null, 2)}
              </pre>
            </details>
          ))}
        </div>
        {!history.isLoading && !history.error && list.length === 0 && (
          <p className="py-16 text-center text-sm text-gray-500">No approval decisions yet.</p>
        )}
      </div>
    </div>
  )
}
