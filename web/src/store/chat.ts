import { create } from 'zustand'
import type { ToolCallEvent, ToolResultEvent, FinalEvent } from '../api/types'

export interface StreamStep {
  step: number
  toolCall?: ToolCallEvent
  toolResult?: ToolResultEvent
}

export interface StreamState {
  isStreaming: boolean
  pendingMessage: string | null
  steps: StreamStep[]
  finalData: FinalEvent | null
  errorMsg: string | null
  abortFn: (() => void) | null
}

interface ChatStore {
  // Active session
  sessionId: number | null
  setSessionId: (id: number | null) => void

  // Streaming
  stream: StreamState
  startStream: (abort: () => void, query: string) => void
  addThinking: (step: number) => void
  addToolCall: (ev: ToolCallEvent) => void
  addToolResult: (ev: ToolResultEvent) => void
  setFinal: (ev: FinalEvent) => void
  setStreamError: (msg: string) => void
  endStream: () => void

  // Sidebar refresh trigger
  sessionListKey: number
  refreshSessionList: () => void
}

export const useChatStore = create<ChatStore>((set, get) => ({
  sessionId: null,
  setSessionId: (id) => set({ sessionId: id }),

  stream: {
    isStreaming: false,
    pendingMessage: null,
    steps: [],
    finalData: null,
    errorMsg: null,
    abortFn: null,
  },

  startStream: (abort, query) =>
    set({
      stream: {
        isStreaming: true,
        pendingMessage: query,
        steps: [],
        finalData: null,
        errorMsg: null,
        abortFn: abort,
      },
    }),

  addThinking: (step) =>
    set((s) => {
      const existing = s.stream.steps.find((x) => x.step === step)
      if (existing) return s
      return {
        stream: {
          ...s.stream,
          steps: [...s.stream.steps, { step }],
        },
      }
    }),

  addToolCall: (ev) =>
    set((s) => {
      const steps = s.stream.steps.map((x) =>
        x.step === ev.step ? { ...x, toolCall: ev } : x,
      )
      // create slot if missing
      if (!steps.find((x) => x.step === ev.step)) {
        steps.push({ step: ev.step, toolCall: ev })
      }
      return { stream: { ...s.stream, steps } }
    }),

  addToolResult: (ev) =>
    set((s) => {
      const steps = s.stream.steps.map((x) =>
        x.step === ev.step ? { ...x, toolResult: ev } : x,
      )
      return { stream: { ...s.stream, steps } }
    }),

  setFinal: (ev) =>
    set((s) => ({
      // sessionId is updated in endStream, not here, so that the messages query
      // key stays stable during streaming and doesn't trigger a premature refetch.
      stream: { ...s.stream, finalData: ev },
    })),

  setStreamError: (msg) =>
    set((s) => ({
      stream: { ...s.stream, errorMsg: msg },
    })),

  endStream: () => {
    const { stream, refreshSessionList } = get()
    refreshSessionList()
    const newSessionId = stream.finalData?.session_id
    set((s) => ({
      ...(newSessionId !== undefined ? { sessionId: newSessionId } : {}),
      stream: { ...s.stream, isStreaming: false, pendingMessage: null, abortFn: null, steps: [] },
    }))
  },

  sessionListKey: 0,
  refreshSessionList: () =>
    set((s) => ({ sessionListKey: s.sessionListKey + 1 })),
}))
