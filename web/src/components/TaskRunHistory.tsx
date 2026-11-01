import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Loader2, CheckCircle2, XCircle, ChevronRight } from 'lucide-react'
import { fetchTaskRuns } from '../api/client'
import type { TaskRun } from '../api/types'

const TRIGGER_CLS: Record<TaskRun['trigger'], string> = {
  cron: 'bg-surface-border text-gray-400',
  retry: 'bg-amber-900/30 text-amber-400',
  manual: 'bg-accent/15 text-accent',
}

/** One-line preview: the first line, or the whole result collapsed when it is
 * pretty-printed JSON whose first line is just "{". */
export function resultPreview(result: string, max = 200): string {
  const first = result.split('\n', 1)[0].trim()
  const line = first.length > 2 ? first : result.replace(/\s+/g, ' ').trim()
  return line.length > max ? line.slice(0, max) + '…' : line
}

function RunRow({ run }: { run: TaskRun }) {
  const [open, setOpen] = useState(false)
  const firstLine = resultPreview(run.result)
  const multi = run.result.includes('\n') || run.result.length > 120
  const secs = Math.max(0, run.finished_at - run.started_at)

  return (
    <div className="border-t border-surface-border first:border-t-0">
      <button
        onClick={() => multi && setOpen((o) => !o)}
        className={`flex w-full items-center gap-2 px-3 py-1.5 text-left text-[11px] ${multi ? 'hover:bg-surface-overlay cursor-pointer' : 'cursor-default'}`}
      >
        <ChevronRight size={10} className={`shrink-0 text-gray-600 transition-transform ${open ? 'rotate-90' : ''} ${multi ? '' : 'invisible'}`} />
        {run.ok
          ? <CheckCircle2 size={11} className="shrink-0 text-emerald-400" />
          : <XCircle size={11} className="shrink-0 text-red-400" />}
        <span className="shrink-0 text-gray-400 tabular-nums">{new Date(run.started_at * 1000).toLocaleString()}</span>
        <span className={`shrink-0 rounded px-1.5 py-px text-[10px] ${TRIGGER_CLS[run.trigger] ?? TRIGGER_CLS.cron}`}>{run.trigger}</span>
        {run.attempt > 0 && <span className="shrink-0 text-[10px] text-gray-600">attempt {run.attempt}</span>}
        <span className="shrink-0 text-[10px] text-gray-600 tabular-nums">{secs < 10 ? secs.toFixed(1) : Math.round(secs)}s</span>
        <span className={`min-w-0 flex-1 truncate font-mono text-[10px] ${run.ok ? 'text-gray-500' : 'text-red-400/80'}`}>{firstLine}</span>
      </button>
      {open && (
        <pre className="mx-3 mb-2 max-h-64 overflow-auto whitespace-pre-wrap break-words rounded-lg bg-surface p-2 font-mono text-[10px] text-gray-400">
          {run.result}
        </pre>
      )}
    </div>
  )
}

export default function TaskRunHistory({ taskId }: { taskId: number }) {
  const { data: runs = [], isLoading, error } = useQuery({
    queryKey: ['task-runs', taskId],
    queryFn: () => fetchTaskRuns(taskId),
    refetchInterval: 30_000,
  })

  return (
    <div className="mt-3 rounded-lg border border-surface-border bg-surface/60">
      {isLoading && (
        <div className="flex items-center justify-center py-4 text-gray-600"><Loader2 size={14} className="animate-spin" /></div>
      )}
      {error && <p className="px-3 py-2 text-[11px] text-red-400">Failed to load history: {(error as Error).message}</p>}
      {!isLoading && !error && runs.length === 0 && (
        <p className="px-3 py-3 text-center text-[11px] text-gray-600">No runs recorded yet</p>
      )}
      <div className="max-h-80 overflow-y-auto">
        {runs.map((r) => <RunRow key={r.id} run={r} />)}
      </div>
    </div>
  )
}
