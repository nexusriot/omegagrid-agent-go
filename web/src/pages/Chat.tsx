import { useEffect, useRef, useState, useCallback, useMemo } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Send, Square, Loader2, BotMessageSquare, Sparkles } from 'lucide-react'
import { toast } from 'sonner'
import SessionList from '../components/SessionList'
import ChatBubble from '../components/ChatBubble'
import ToolCard from '../components/ToolCard'
import { fetchMessages } from '../api/client'
import { streamQuery } from '../api/stream'
import { useChatStore } from '../store/chat'
import type { StreamEvent } from '../api/types'

export default function Chat() {
  const {
    sessionId,
    stream, startStream, addThinking, addToolCall, addToolResult,
    setFinal, setStreamError, endStream,
  } = useChatStore()

  const qc = useQueryClient()
  const [input, setInput] = useState('')
  const bottomRef = useRef<HTMLDivElement>(null)
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  // Load message history for the active session.
  // refetchOnWindowFocus disabled so a mid-stream tab-switch doesn't reload
  // history and displace the pending user message.
  const { data: messages = [] } = useQuery({
    queryKey: ['messages', sessionId],
    queryFn: () => fetchMessages(sessionId!),
    enabled: sessionId !== null,
    staleTime: 5_000,
    refetchOnWindowFocus: false,
  })

  // Freeze history the moment streaming starts. We do this synchronously in the
  // render body (not useEffect) so the pending user message appears on the very
  // first streaming render — no one-render delay / flicker.
  const frozenSnapshotRef = useRef<typeof messages>([])
  const wasStreamingRef = useRef(false)
  if (stream.isStreaming && !wasStreamingRef.current) {
    frozenSnapshotRef.current = messages
  }
  wasStreamingRef.current = stream.isStreaming

  // During streaming: frozen pre-stream history + pending user message appended.
  // After streaming: fresh server messages (loaded via invalidation in the effect below).
  const displayMessages = useMemo(() => {
    if (!stream.isStreaming) return messages
    if (!stream.pendingMessage) return frozenSnapshotRef.current
    return [
      ...frozenSnapshotRef.current,
      { id: -1, session_id: sessionId ?? 0, ts: Date.now() / 1000, role: 'user' as const, content: stream.pendingMessage },
    ]
  // frozenSnapshotRef is intentionally excluded — it's a ref written before this memo runs
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [stream.isStreaming, stream.pendingMessage, messages, sessionId])

  // Auto-scroll on new content
  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [displayMessages, stream.steps, stream.finalData])

  // Invalidate history once stream finishes.
  useEffect(() => {
    if (!stream.isStreaming && stream.finalData) {
      qc.invalidateQueries({ queryKey: ['messages', sessionId] })
    }
  }, [stream.isStreaming, stream.finalData, sessionId, qc])

  const handleAbort = useCallback(() => {
    stream.abortFn?.()
    endStream()
  }, [stream, endStream])

  const handleSend = useCallback(() => {
    const q = input.trim()
    if (!q || stream.isStreaming) return
    setInput('')

    const ctrl = streamQuery(
      { query: q, session_id: sessionId ?? 0, remember: true },
      (ev: StreamEvent) => {
        switch (ev.event) {
          case 'thinking':   addThinking(ev.data.step);   break
          case 'tool_call':  addToolCall(ev.data);         break
          case 'tool_result':addToolResult(ev.data);       break
          case 'final':      setFinal(ev.data);            break
          case 'error':
            setStreamError(ev.data.message)
            toast.error(ev.data.message)
            break
        }
      },
      () => {
        endStream()
        qc.invalidateQueries({ queryKey: ['sessions'] })
      },
    )

    startStream(() => ctrl.abort(), q)

    // If no session yet, one will be assigned via setFinal → ev.session_id
    if (!sessionId) {
      // session_id 0 triggers new session; update once final arrives
    }
  }, [input, sessionId, stream.isStreaming, addThinking, addToolCall, addToolResult, setFinal, setStreamError, endStream, startStream, qc])

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      handleSend()
    }
  }

  const visibleMessages = displayMessages.filter(
    (m) => m.role !== 'raw_model_json' && m.role !== 'tool',
  )

  const isEmpty = visibleMessages.length === 0 && !stream.isStreaming

  return (
    <div className="flex h-full overflow-hidden">
      {/* Sidebar */}
      <SessionList />

      {/* Main chat area */}
      <div className="flex flex-1 flex-col overflow-hidden">
        {/* Header */}
        <header className="flex h-12 items-center justify-between border-b border-surface-border px-4 shrink-0">
          <div className="flex items-center gap-2">
            <BotMessageSquare size={16} className="text-accent" />
            <span className="text-sm font-medium text-gray-300">
              {sessionId ? `Session #${sessionId}` : 'New session'}
            </span>
          </div>
          {stream.isStreaming && (
            <div className="flex items-center gap-2 text-xs text-gray-500">
              <Loader2 size={12} className="animate-spin text-accent" />
              <span>
                {stream.steps.length > 0 ? `Step ${stream.steps.length}…` : 'Thinking…'}
              </span>
            </div>
          )}
        </header>

        {/* Messages */}
        <div className="flex-1 overflow-y-auto px-6 py-6">
          {isEmpty && (
            <div className="flex h-full flex-col items-center justify-center gap-3 text-center">
              <div className="flex h-14 w-14 items-center justify-center rounded-2xl bg-accent/10 text-accent">
                <Sparkles size={26} />
              </div>
              <p className="text-sm font-medium text-gray-400">Ask anything</p>
              <p className="text-xs text-gray-600 max-w-xs">
                The agent has tools for weather, web search, DNS, scheduling, memory, and more.
              </p>
            </div>
          )}

          {/* History messages */}
          {visibleMessages.map((m) => (
            <ChatBubble key={m.id} message={m} />
          ))}

          {/* Live stream steps — only shown while streaming; cleared on endStream() */}
          {stream.isStreaming && (
            <div className="mb-4">
              {stream.steps.map((s) => (
                <ToolCard key={s.step} step={s} />
              ))}
              {stream.steps.length === 0 && (
                <div className="flex items-center gap-2 text-xs text-gray-600">
                  <Loader2 size={12} className="animate-spin" />
                  Starting…
                </div>
              )}
            </div>
          )}

          {/* Stream error */}
          {stream.errorMsg && !stream.isStreaming && (
            <div className="mb-4 rounded-xl border border-red-900/50 bg-red-950/30 px-4 py-3 text-sm text-red-400">
              {stream.errorMsg}
            </div>
          )}

          <div ref={bottomRef} />
        </div>

        {/* Input */}
        <div className="border-t border-surface-border px-4 py-3 shrink-0">
          <div className="flex items-end gap-2 rounded-2xl border border-surface-border bg-surface-overlay px-4 py-3 focus-within:border-accent/50 transition-colors">
            <textarea
              ref={textareaRef}
              rows={1}
              value={input}
              onChange={(e) => {
                setInput(e.target.value)
                // auto-resize
                e.target.style.height = 'auto'
                e.target.style.height = Math.min(e.target.scrollHeight, 160) + 'px'
              }}
              onKeyDown={handleKeyDown}
              placeholder="Message the agent… (Enter to send, Shift+Enter for newline)"
              disabled={stream.isStreaming}
              className="flex-1 resize-none bg-transparent text-sm text-gray-200 placeholder-gray-600 outline-none disabled:opacity-50 max-h-40"
              style={{ overflowY: 'auto' }}
            />
            {stream.isStreaming ? (
              <button
                onClick={handleAbort}
                className="shrink-0 flex h-8 w-8 items-center justify-center rounded-xl bg-red-500/20 text-red-400 hover:bg-red-500/30 transition-colors"
                title="Stop generation"
              >
                <Square size={14} />
              </button>
            ) : (
              <button
                onClick={handleSend}
                disabled={!input.trim()}
                className="shrink-0 flex h-8 w-8 items-center justify-center rounded-xl bg-accent text-white hover:bg-accent-hover disabled:opacity-30 disabled:cursor-not-allowed transition-colors"
                title="Send"
              >
                <Send size={14} />
              </button>
            )}
          </div>
          <p className="mt-1.5 text-center text-[10px] text-gray-700">
            Powered by OmegaGrid Agent
          </p>
        </div>
      </div>
    </div>
  )
}
