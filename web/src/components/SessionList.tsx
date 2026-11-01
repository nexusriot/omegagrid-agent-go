import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { MessageSquare, Plus, Loader2, Search, Pencil, Trash2, X } from 'lucide-react'
import { toast } from 'sonner'
import { fetchSessions, createSession, renameSession, deleteSession } from '../api/client'
import { useChatStore } from '../store/chat'
import { useDebounced } from '../lib/useDebounced'
import type { Session } from '../api/types'

function fmtDate(ts: number): string {
  const d = new Date(ts * 1000)
  const now = new Date()
  if (d.toDateString() === now.toDateString()) {
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  }
  return d.toLocaleDateString([], { month: 'short', day: 'numeric' })
}

function sessionLabel(s: Session): string {
  return s.title?.trim() || s.preview?.trim() || `Session #${s.id}`
}

interface RowProps {
  session: Session
  active: boolean
  onSelect: () => void
  onChanged: () => void
}

function SessionRow({ session: s, active, onSelect, onChanged }: RowProps) {
  const { sessionId, setSessionId } = useChatStore()
  const qc = useQueryClient()
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState('')
  const [busy, setBusy] = useState(false)

  function startEdit(e: React.MouseEvent) {
    e.stopPropagation()
    setDraft(s.title ?? '')
    setEditing(true)
  }

  async function saveTitle() {
    if (busy) return
    setBusy(true)
    try {
      await renameSession(s.id, draft.trim())
      setEditing(false)
      onChanged()
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  async function handleDelete(e: React.MouseEvent) {
    e.stopPropagation()
    if (!confirm(`Delete "${sessionLabel(s)}" and all its messages?`)) return
    try {
      await deleteSession(s.id)
      if (sessionId === s.id) setSessionId(null)
      qc.removeQueries({ queryKey: ['messages', s.id] })
      toast.success('Session deleted')
      onChanged()
    } catch (err) {
      toast.error((err as Error).message)
    }
  }

  if (editing) {
    return (
      <div className="flex items-center gap-1 px-2 py-1.5 bg-surface-overlay">
        <input
          autoFocus
          value={draft}
          maxLength={200}
          readOnly={busy}
          placeholder={s.preview || `Session #${s.id}`}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') saveTitle()
            if (e.key === 'Escape') setEditing(false)
          }}
          className="min-w-0 flex-1 rounded-md border border-accent/50 bg-surface px-2 py-1 text-xs text-gray-200 outline-none"
        />
        <button
          onClick={() => setEditing(false)}
          title="Cancel (Esc)"
          className="flex h-5 w-5 shrink-0 items-center justify-center rounded text-gray-500 hover:text-gray-300"
        >
          <X size={12} />
        </button>
      </div>
    )
  }

  return (
    <div
      role="button"
      tabIndex={0}
      onClick={onSelect}
      onKeyDown={(e) => { if (e.key === 'Enter') onSelect() }}
      className={[
        'group flex w-full cursor-pointer items-center gap-2 px-3 py-2 text-left transition-colors',
        active
          ? 'bg-accent/15 text-gray-200'
          : 'text-gray-400 hover:bg-surface-overlay hover:text-gray-300',
      ].join(' ')}
    >
      <MessageSquare size={13} className="shrink-0 opacity-60" />
      <div className="flex-1 min-w-0">
        <div className="truncate text-xs font-medium" title={sessionLabel(s)}>{sessionLabel(s)}</div>
        <div className="text-[10px] text-gray-600">{fmtDate(s.last_at || s.created_at)}</div>
      </div>
      <div className="hidden shrink-0 items-center gap-0.5 group-hover:flex">
        <button
          onClick={startEdit}
          title="Rename"
          className="flex h-5 w-5 items-center justify-center rounded text-gray-500 hover:bg-surface-border hover:text-gray-300"
        >
          <Pencil size={11} />
        </button>
        <button
          onClick={handleDelete}
          title="Delete"
          className="flex h-5 w-5 items-center justify-center rounded text-gray-500 hover:bg-red-900/30 hover:text-red-400"
        >
          <Trash2 size={11} />
        </button>
      </div>
      {s.message_count !== undefined && (
        <span className="shrink-0 rounded-full bg-surface-border px-1.5 py-0.5 text-[10px] text-gray-500 group-hover:hidden">
          {s.message_count}
        </span>
      )}
    </div>
  )
}

export default function SessionList() {
  const { sessionId, setSessionId, sessionListKey } = useChatStore()
  const [search, setSearch] = useState('')
  const q = useDebounced(search.trim(), 250)

  const { data: sessions = [], isLoading, refetch } = useQuery<Session[]>({
    queryKey: ['sessions', sessionListKey, q],
    queryFn: () => fetchSessions(50, q),
    staleTime: 10_000,
    placeholderData: (prev) => prev,
  })

  async function handleNew() {
    try {
      const id = await createSession()
      setSessionId(id)
      refetch()
    } catch (e) {
      toast.error((e as Error).message)
    }
  }

  return (
    <div className="flex h-full w-56 shrink-0 flex-col border-r border-surface-border bg-surface-raised">
      {/* Header */}
      <div className="flex items-center justify-between px-3 py-3 border-b border-surface-border">
        <span className="text-xs font-semibold uppercase tracking-widest text-gray-500">
          Sessions
        </span>
        <button
          onClick={handleNew}
          title="New session"
          className="flex h-6 w-6 items-center justify-center rounded-md text-gray-500 hover:bg-surface-overlay hover:text-gray-300 transition-colors"
        >
          <Plus size={14} />
        </button>
      </div>

      <div className="px-2 py-2 border-b border-surface-border">
        <div className="relative">
          <Search size={12} className="absolute left-2 top-1/2 -translate-y-1/2 text-gray-600" />
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Escape') setSearch('') }}
            placeholder="Search sessions…"
            className="w-full rounded-md border border-surface-border bg-surface pl-6 pr-6 py-1 text-xs text-gray-300 placeholder-gray-600 outline-none focus:border-accent/50"
          />
          {search && (
            <button
              onClick={() => setSearch('')}
              title="Clear"
              className="absolute right-1.5 top-1/2 -translate-y-1/2 text-gray-600 hover:text-gray-400"
            >
              <X size={11} />
            </button>
          )}
        </div>
      </div>

      {/* List */}
      <div className="flex-1 overflow-y-auto py-1">
        {isLoading && (
          <div className="flex items-center justify-center py-8 text-gray-600">
            <Loader2 size={16} className="animate-spin" />
          </div>
        )}
        {sessions.map((s) => (
          <SessionRow
            key={s.id}
            session={s}
            active={s.id === sessionId}
            onSelect={() => setSessionId(s.id)}
            onChanged={() => refetch()}
          />
        ))}
        {!isLoading && sessions.length === 0 && (
          <p className="py-8 text-center text-xs text-gray-600">
            {q ? `No sessions matching "${q}"` : 'No sessions yet'}
          </p>
        )}
      </div>
    </div>
  )
}
