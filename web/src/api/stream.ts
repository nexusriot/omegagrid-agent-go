import type { StreamEvent, QueryRequest } from './types'
import { authHeaders, promptForToken } from './auth'

const BASE = import.meta.env.VITE_API_BASE ?? ''

/**
 * Open a streaming agent query via fetch + ReadableStream.
 * Calls `onEvent` for each SSE event, calls `onDone` when the stream closes.
 * Returns an AbortController so the caller can cancel early.
 */
export function streamQuery(
  req: QueryRequest,
  onEvent: (ev: StreamEvent) => void,
  onDone: () => void,
): AbortController {
  const ctrl = new AbortController()

  ;(async () => {
    try {
      const res = await fetch(`${BASE}/api/query/stream`, {
        method: 'POST',
        signal: ctrl.signal,
        headers: { 'Content-Type': 'application/json', ...authHeaders() },
        body: JSON.stringify(req),
      })

      if (res.status === 401) {
        const saved = promptForToken()
        onEvent({ event: 'error', data: { message: saved
          ? 'API token saved — please resend.'
          : 'API token required — please enter a token and resend.' } })
        return
      }
      if (!res.ok || !res.body) {
        onEvent({ event: 'error', data: { message: `HTTP ${res.status}` } })
        return
      }

      const reader = res.body.getReader()
      const decoder = new TextDecoder()
      let buf = ''

      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        buf += decoder.decode(value, { stream: true })

        // SSE wire format: "event: <name>\ndata: <json>\n\n"
        const blocks = buf.split('\n\n')
        buf = blocks.pop() ?? ''

        for (const block of blocks) {
          const lines = block.split('\n')
          let eventName = ''
          let dataStr = ''
          for (const line of lines) {
            if (line.startsWith('event: ')) eventName = line.slice(7).trim()
            if (line.startsWith('data: '))  dataStr  = line.slice(6).trim()
          }
          if (!eventName || !dataStr) continue
          try {
            const data = JSON.parse(dataStr)
            onEvent({ event: eventName, data } as StreamEvent)
          } catch {
            // ignore malformed SSE frames
          }
        }
      }
    } catch (err) {
      if ((err as Error).name !== 'AbortError') {
        onEvent({ event: 'error', data: { message: String(err) } })
      }
    } finally {
      onDone()
    }
  })()

  return ctrl
}
