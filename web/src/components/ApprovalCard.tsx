import { useState } from 'react'
import { ShieldAlert, ShieldCheck, ShieldX, Loader2 } from 'lucide-react'
import { toast } from 'sonner'
import { resolveApproval } from '../api/client'
import type { ApprovalItem } from '../store/chat'
import { useChatStore } from '../store/chat'

/** A tool call paused until the user approves or denies it. */
export default function ApprovalCard({ item }: { item: ApprovalItem }) {
  const { approval, tool } = item.event
  const resolveItem = useChatStore((s) => s.resolveApprovalItem)
  const [busy, setBusy] = useState(false)

  const decide = async (approved: boolean, scope: 'once' | 'session' = 'once') => {
    setBusy(true)
    try {
      await resolveApproval(approval.id, approved, '', scope)
      resolveItem(approval.id, approved, scope === 'session' ? 'web, whole session' : 'web')
    } catch (err) {
      toast.error(`Approval failed: ${(err as Error).message}`)
    } finally {
      setBusy(false)
    }
  }

  const pending = item.status === 'pending'
  const border = pending
    ? 'border-amber-700/60 bg-amber-950/20'
    : item.status === 'approved'
      ? 'border-emerald-800/50 bg-emerald-950/10'
      : 'border-red-900/50 bg-red-950/20'

  return (
    <div className={`my-2 rounded-xl border px-3 py-2 animate-fade-in ${border}`}>
      <div className="flex items-center gap-2 text-xs">
        {pending && <ShieldAlert size={14} className="shrink-0 text-amber-400" />}
        {item.status === 'approved' && <ShieldCheck size={14} className="shrink-0 text-emerald-400" />}
        {item.status === 'denied' && <ShieldX size={14} className="shrink-0 text-red-400" />}
        <span className="text-gray-300">
          {pending ? 'Approval needed to run ' : item.status === 'approved' ? 'Approved: ' : 'Not approved: '}
          <span className="font-mono font-semibold text-indigo-300">{tool}</span>
          {!pending && item.by && <span className="ml-1 text-gray-500">({item.by})</span>}
        </span>
      </div>
      {pending && (
        <>
          <pre className="mt-2 max-h-48 overflow-auto rounded-lg bg-black/30 p-2 text-[11px] text-gray-300 whitespace-pre-wrap break-all">
            {JSON.stringify(approval.args, null, 2)}
          </pre>
          {approval.why && <p className="mt-1 text-[11px] italic text-gray-500">why: {approval.why}</p>}
          <div className="mt-2 flex gap-2">
            <button
              disabled={busy}
              onClick={() => decide(true)}
              className="flex items-center gap-1 rounded-lg bg-emerald-600/80 px-3 py-1 text-xs text-white hover:bg-emerald-600 disabled:opacity-50"
            >
              {busy && <Loader2 size={11} className="animate-spin" />} Approve
            </button>
            <button
              disabled={busy}
              onClick={() => decide(true, 'session')}
              title={`Also run ${tool} without asking for the rest of this session`}
              className="rounded-lg border border-emerald-700/60 px-3 py-1 text-xs text-emerald-300 hover:bg-emerald-900/40 disabled:opacity-50"
            >
              Approve for session
            </button>
            <button
              disabled={busy}
              onClick={() => decide(false)}
              className="rounded-lg bg-red-600/30 px-3 py-1 text-xs text-red-300 hover:bg-red-600/50 disabled:opacity-50"
            >
              Deny
            </button>
            <span className="ml-auto self-center text-[10px] text-gray-600">
              expires {new Date(approval.expires_at).toLocaleTimeString()}
            </span>
          </div>
        </>
      )}
    </div>
  )
}
