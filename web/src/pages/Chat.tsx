import { useEffect, useRef, useState, useCallback } from 'react'
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
  const [justSent, setJustSent] = useState('')
  const bottomRef = useRef<HTMLDivElement>(null)
  const pendingBubbleRef = useRef<HTMLDivElement>(null)

  // Load history for the active session.
  // - New session: sessionId is null → query disabled → messages = [].
  // - Existing session: sessionId is stable throughout streaming (only updated
  //   in endStream, not in setFinal) → query key unchanged → no mid-stream refetch.
  // Both cases mean `messages` is stable while streaming and safe to read directly.
  const { data: messages = [] } = useQuery({
    queryKey: ['messages', sessionId],
    queryFn: () => fetchMessages(sessionId!),
    enabled: sessionId != null,
    staleTime: 0,
    refetchOnWindowFocus: false,
  })

  // Scroll to bottom whenever messages change.
  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages])

  // Scroll to pending bubble when streaming starts, anchoring it at the top of the
  // viewport while ToolCards (which start expanded) expand below it.
  useEffect(() => {
    if (stream.isStreaming && pendingBubbleRef.current) {
      pendingBubbleRef.current.scrollIntoView({ behavior: 'smooth', block: 'start' })
    }
  }, [stream.isStreaming])

  // Clear just-sent message when streaming ends.
  useEffect(() => {
    if (!stream.isStreaming && justSent) {
      setJustSent('')
    }
  }, [stream.isStreaming])

  // Reload history after streaming finishes. Use sessionId from finalData
  // to ensure the query goes to the correct (newly created) session.
  useEffect(() => {
    if (!stream.isStreaming && stream.finalData) {
      const sid = stream.finalData.session_id
      qc.invalidateQueries({ queryKey: ['messages', sid] })
    }
  }, [stream.isStreaming, stream.finalData, qc])

  const handleAbort = useCallback(() => {
    stream.abortFn?.()
    endStream()
  }, [stream, endStream])

  const handleSend = useCallback(() => {
    const q = input.trim()
    if (!q || stream.isStreaming) return
    setInput('')
    setJustSent(q)

    const ctrl = streamQuery(
      { query: q, session_id: sessionId ?? 0, remember: true },
      (ev: StreamEvent) => {
        switch (ev.event) {
          case 'thinking':    addThinking(ev.data.step);  break
          case 'tool_call':   addToolCall(ev.data);        break
          case 'tool_result': addToolResult(ev.data);      break
          case 'final':       setFinal(ev.data);           break
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

    // startStream sets stream.pendingMessage = q and stream.isStreaming = true
    // in a single atomic Zustand update, so they are always consistent.
    startStream(() => ctrl.abort(), q)
  }, [input, sessionId, stream.isStreaming, addThinking, addToolCall, addToolResult,
      setFinal, setStreamError, endStream, startStream, qc])

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      handleSend()
    }
  }

  const visibleMessages = messages.filter(
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
              <span>{stream.steps.length > 0 ? `Step ${stream.steps.length}…` : 'Thinking…'}</span>
            </div>
          )}
        </header>

        {/* Messages */}
        <div className="flex-1 overflow-y-auto px-6 py-6">

          {/* Empty state */}
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

          {/* Conversation history */}
          {visibleMessages.map((m) => (
            <ChatBubble key={m.id} message={m} />
          ))}

          {/* Pending user bubble
              stream.pendingMessage is set atomically with stream.isStreaming=true
              inside startStream(), so it appears in the very same Zustand render
              that starts the stream — before any SSE events arrive.
              It is cleared atomically with isStreaming=false inside endStream().
          */}
          {justSent && (
            <div ref={pendingBubbleRef} className="flex justify-end mb-4">
              <div className="max-w-[75%] rounded-2xl rounded-tr-sm bg-accent px-4 py-3 text-sm text-white shadow-lg shadow-accent/20">
                {justSent}
              </div>
            </div>
          )}

          {/* Live tool-call steps */}
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
              rows={1}
              value={input}
              onChange={(e) => {
                setInput(e.target.value)
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
