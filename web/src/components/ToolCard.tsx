import { useState } from 'react'
import { ChevronDown, ChevronRight, Wrench, CheckCircle2, Clock } from 'lucide-react'
import type { StreamStep } from '../store/chat'

function prettyJSON(v: unknown): string {
  try { return JSON.stringify(v, null, 2) } catch { return String(v) }
}

export default function ToolCard({ step }: { step: StreamStep }) {
  const [open, setOpen] = useState(false)
  const { toolCall: tc, toolResult: tr } = step

  if (!tc) {
    // Pure "thinking" indicator
    return (
      <div className="flex items-center gap-2 py-1 text-xs text-gray-600 animate-pulse-slow">
        <span className="inline-block h-1.5 w-1.5 rounded-full bg-gray-600" />
        Step {step.step}…
      </div>
    )
  }

  const done = !!tr

  return (
    <div className="my-1 rounded-xl border border-surface-border bg-surface-overlay overflow-hidden animate-fade-in">
      {/* Header row */}
      <button
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-center gap-2 px-3 py-2 text-left hover:bg-white/5 transition-colors"
      >
        {done ? (
          <CheckCircle2 size={13} className="shrink-0 text-emerald-500" />
        ) : (
          <Wrench size={13} className="shrink-0 text-accent animate-pulse-slow" />
        )}

        <span className="flex-1 min-w-0">
          <span className="font-mono text-xs font-semibold text-indigo-300">
            {tc.tool}
          </span>
          {tc.why && (
            <span className="ml-2 text-xs italic text-gray-500 truncate">
              — {tc.why}
            </span>
          )}
        </span>

        {tr && (
          <span className="shrink-0 flex items-center gap-1 text-[10px] text-gray-600">
            <Clock size={10} />
            {tr.elapsed_s.toFixed(2)}s
          </span>
        )}

        {open
          ? <ChevronDown size={12} className="shrink-0 text-gray-600" />
          : <ChevronRight size={12} className="shrink-0 text-gray-600" />
        }
      </button>

      {/* Expandable body */}
      {open && (
        <div className="border-t border-surface-border text-xs">
          {/* Args */}
          {tc.args && Object.keys(tc.args).length > 0 && (
            <div className="px-3 py-2">
              <div className="mb-1 text-[10px] uppercase tracking-widest text-gray-600">args</div>
              <pre className="overflow-x-auto rounded-lg bg-surface p-2 font-mono text-gray-400 text-[11px]">
                {prettyJSON(tc.args)}
              </pre>
            </div>
          )}
          {/* Result */}
          {tr && (
            <div className="border-t border-surface-border px-3 py-2">
              <div className="mb-1 text-[10px] uppercase tracking-widest text-gray-600">result</div>
              <pre className="overflow-x-auto rounded-lg bg-surface p-2 font-mono text-gray-400 text-[11px] max-h-48">
                {(() => {
                  try { return prettyJSON(JSON.parse(tr.result)) }
                  catch { return tr.result }
                })()}
              </pre>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
