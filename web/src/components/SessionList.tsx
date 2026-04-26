import { useQuery } from '@tanstack/react-query'
import { MessageSquare, Plus, Loader2 } from 'lucide-react'
import { fetchSessions, createSession } from '../api/client'
import { useChatStore } from '../store/chat'
import type { Session } from '../api/types'

function fmtDate(ts: number): string {
  const d = new Date(ts * 1000)
  const now = new Date()
  if (d.toDateString() === now.toDateString()) {
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  }
  return d.toLocaleDateString([], { month: 'short', day: 'numeric' })
}

export default function SessionList() {
  const { sessionId, setSessionId, sessionListKey } = useChatStore()

  const { data: sessions = [], isLoading, refetch } = useQuery<Session[]>({
    queryKey: ['sessions', sessionListKey],
    queryFn: () => fetchSessions(),
    staleTime: 10_000,
  })

  async function handleNew() {
    const id = await createSession()
    setSessionId(id)
    refetch()
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

      {/* List */}
      <div className="flex-1 overflow-y-auto py-1">
        {isLoading && (
          <div className="flex items-center justify-center py-8 text-gray-600">
            <Loader2 size={16} className="animate-spin" />
          </div>
        )}
        {sessions.map((s) => (
          <button
            key={s.id}
            onClick={() => setSessionId(s.id)}
            className={[
              'flex w-full items-center gap-2 px-3 py-2 text-left transition-colors',
              s.id === sessionId
                ? 'bg-accent/15 text-gray-200'
                : 'text-gray-400 hover:bg-surface-overlay hover:text-gray-300',
            ].join(' ')}
          >
            <MessageSquare size={13} className="shrink-0 opacity-60" />
            <div className="flex-1 min-w-0">
              <div className="truncate text-xs font-medium">#{s.id}</div>
              <div className="text-[10px] text-gray-600">{fmtDate(s.created_at)}</div>
            </div>
            {s.message_count !== undefined && (
              <span className="shrink-0 rounded-full bg-surface-border px-1.5 py-0.5 text-[10px] text-gray-500">
                {s.message_count}
              </span>
            )}
          </button>
        ))}
        {!isLoading && sessions.length === 0 && (
          <p className="py-8 text-center text-xs text-gray-600">No sessions yet</p>
        )}
      </div>
    </div>
  )
}
