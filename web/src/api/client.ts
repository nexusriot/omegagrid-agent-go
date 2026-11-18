import type {
  Session, Message, Skill, SchedulerTask,
  CreateTaskRequest, MemoryHit, MemoryAddResult,
  HealthStatus, QueryResult, QueryRequest,
  InvocationListResult, InvocationFilter, ReplayResult,
  MemoryNamespace, MemoryUpdateRequest, ImportReport,
  TaskRun, UsageStats, SkillValidateResult, SkillSaveResult,
  ApprovalRequest, ApprovalRecord, ApprovalGrant, RunRecord, RunStats,
} from './types'
import { authHeaders, promptForToken } from './auth'

const BASE = import.meta.env.VITE_API_BASE ?? ''

// Error bodies are {"error": "..."}; surface that text rather than raw JSON.
function errorText(body: string, fallback: string): string {
  try {
    const parsed = JSON.parse(body) as { error?: unknown }
    if (typeof parsed?.error === 'string' && parsed.error) return parsed.error
  } catch { /* not JSON */ }
  return body || fallback
}

/** Authenticated fetch: retries once after a token prompt, throws on non-2xx. */
async function request(path: string, init?: RequestInit, retried = false): Promise<Response> {
  const res = await fetch(`${BASE}${path}`, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...authHeaders(), ...init?.headers },
  })
  if (res.status === 401 && !retried && promptForToken()) {
    return request(path, init, true)
  }
  if (!res.ok) {
    const body = await res.text().catch(() => '')
    throw new Error(`${res.status}: ${errorText(body, res.statusText)}`)
  }
  return res
}

async function json<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await request(path, init)
  return res.json() as Promise<T>
}

export const fetchHealth = () => json<HealthStatus>('/health')

export const fetchSessions = (limit = 50, q = '') => {
  const p = new URLSearchParams({ limit: String(limit) })
  if (q) p.set('q', q)
  return json<{ sessions: Session[] | null }>(`/api/sessions?${p}`).then(r => r.sessions ?? [])
}

export const renameSession = (id: number, title: string) =>
  json<{ ok: boolean; session_id: number; title: string }>(`/api/sessions/${id}`, {
    method: 'PATCH',
    body: JSON.stringify({ title }),
  })

export const deleteSession = (id: number) =>
  json<{ ok: boolean }>(`/api/sessions/${id}`, { method: 'DELETE' })

export const createSession = () =>
  json<{ session_id: number }>('/api/sessions/new', { method: 'POST' }).then(r => r.session_id)

export const fetchMessages = (sessionId: number, limit = 200) =>
  json<{ messages: Message[] }>(`/api/sessions/${sessionId}/messages?limit=${limit}`).then(r => r.messages)

export const query = (req: QueryRequest) =>
  json<QueryResult>('/api/query', { method: 'POST', body: JSON.stringify(req) })

/** namespaces: undefined = search everything; otherwise only those ('' = shared). */
export const searchMemory = (q: string, k = 10, namespaces?: string[]) =>
  json<{ hits: MemoryHit[] | null }>('/api/memory/search', {
    method: 'POST',
    body: JSON.stringify({ query: q, k, ...(namespaces ? { namespaces } : {}) }),
  }).then(r => r.hits ?? [])

export interface AddMemoryOptions {
  namespace?: string
  ttl_seconds?: number
}

export const addMemory = (text: string, meta: Record<string, unknown> = {}, opts: AddMemoryOptions = {}) =>
  json<MemoryAddResult>('/api/memory/add', {
    method: 'POST',
    body: JSON.stringify({ text, meta, ...opts }),
  })

/** namespace: undefined = all namespaces, '' = the shared default namespace. */
export const listMemories = (limit = 100, offset = 0, namespace?: string) => {
  const p = new URLSearchParams({ limit: String(limit), offset: String(offset) })
  if (namespace !== undefined) p.set('namespace', namespace)
  return json<{ hits: MemoryHit[] | null; total: number }>(`/api/memory?${p}`)
    .then(r => ({ hits: r.hits ?? [], total: r.total }))
}

export const fetchMemoryNamespaces = () =>
  json<{ namespaces: MemoryNamespace[] | null }>('/api/memory/namespaces').then(r => r.namespaces ?? [])

export const updateMemory = (id: string, req: MemoryUpdateRequest) =>
  json<{ ok: boolean; hit: MemoryHit }>(`/api/memory/${encodeURIComponent(id)}`, {
    method: 'PUT',
    body: JSON.stringify(req),
  })

export const exportMemories = (withEmbeddings = false) =>
  request(`/api/memory/export${withEmbeddings ? '?embeddings=1' : ''}`).then(r => r.blob())

export const importMemories = (file: Blob) =>
  json<{ ok: boolean; report: ImportReport }>('/api/memory/import', {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-ndjson' },
    body: file,
  }).then(r => r.report)

export const deleteMemory = (id: string) =>
  json<{ ok: boolean; deleted: string }>(`/api/memory/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  })

export const fetchSkills = () =>
  json<{ skills: Skill[] }>('/api/skills').then(r => r.skills)

export const fetchSkillSource = (name: string) =>
  json<{ name: string; content: string }>(`/api/skills/${encodeURIComponent(name)}/source`)
    .then(r => r.content)

export const validateSkillSource = (content: string, signal?: AbortSignal) =>
  json<SkillValidateResult>('/api/skills/validate', {
    method: 'POST',
    body: JSON.stringify({ content }),
    signal,
  })

export const saveSkillSource = (content: string) =>
  json<SkillSaveResult>('/api/skills/source', {
    method: 'POST',
    body: JSON.stringify({ content }),
  })

export const deleteSkill = (name: string) =>
  json<{ ok: boolean }>(`/api/skills/${encodeURIComponent(name)}`, { method: 'DELETE' })

export const fetchTasks = () =>
  json<SchedulerTask[]>('/api/scheduler/tasks').then(r => r ?? [])

export const createTask = (req: CreateTaskRequest) =>
  json<SchedulerTask>('/api/scheduler/tasks', {
    method: 'POST',
    body: JSON.stringify(req),
  })

export const deleteTask = (id: number) =>
  json<{ ok: boolean }>(`/api/scheduler/tasks/${id}`, { method: 'DELETE' })

export const enableTask = (id: number) =>
  json<{ ok: boolean }>(`/api/scheduler/tasks/${id}/enable`, { method: 'POST' })

export const disableTask = (id: number) =>
  json<{ ok: boolean }>(`/api/scheduler/tasks/${id}/disable`, { method: 'POST' })

export const runTask = (id: number) =>
  json<TaskRun>(`/api/scheduler/tasks/${id}/run`, { method: 'POST' })

export const fetchTaskRuns = (id: number, limit = 50) =>
  json<TaskRun[] | null>(`/api/scheduler/tasks/${id}/runs?limit=${limit}`).then(r => r ?? [])

export const fetchUsage = (days: number) =>
  json<UsageStats>(`/api/stats/usage?days=${days}`)

export const fetchInvocations = (f: InvocationFilter = {}) => {
  const p = new URLSearchParams()
  if (f.skill)         p.set('skill', f.skill)
  if (f.session_id)    p.set('session_id', String(f.session_id))
  if (f.only_errors)   p.set('only_errors', 'true')
  if (f.limit)         p.set('limit', String(f.limit))
  if (f.offset)        p.set('offset', String(f.offset))
  const qs = p.toString()
  return json<InvocationListResult>(`/api/invocations${qs ? '?' + qs : ''}`)
}

export const replayInvocation = (id: number) =>
  json<ReplayResult>(`/api/invocations/${id}/replay`, { method: 'POST' })

export const invokeSkill = (name: string, args: Record<string, unknown>) =>
  json<import('./types').SkillInvokeResult>(`/api/skills/${encodeURIComponent(name)}/invoke`, {
    method: 'POST',
    body: JSON.stringify({ args }),
  })

export const fetchApprovals = () =>
  json<{ enabled: boolean; pending: ApprovalRequest[] }>('/api/approvals')

export const resolveApproval = (id: string, approved: boolean, reason = '', scope: 'once' | 'session' = 'once') =>
  json<{ id: string; approved: boolean; scope: string }>(`/api/approvals/${id}`, {
    method: 'POST',
    body: JSON.stringify({ approved, reason, by: 'web', scope }),
  })

export const fetchApprovalHistory = (f: { only_denied?: boolean; tool?: string; limit?: number; offset?: number } = {}) => {
  const p = new URLSearchParams()
  if (f.only_denied) p.set('only_denied', 'true')
  if (f.tool)        p.set('tool', f.tool)
  if (f.limit)       p.set('limit', String(f.limit))
  if (f.offset)      p.set('offset', String(f.offset))
  const qs = p.toString()
  return json<{ approvals: ApprovalRecord[]; total: number }>(`/api/approvals/history${qs ? '?' + qs : ''}`)
}

export const fetchApprovalGrants = () =>
  json<{ enabled: boolean; grants: ApprovalGrant[]; rules: string[] }>('/api/approvals/grants')

export const revokeApprovalGrants = (sessionId?: number) =>
  json<{ revoked: number }>(`/api/approvals/grants${sessionId ? `?session_id=${sessionId}` : ''}`, { method: 'DELETE' })

export const fetchRuns = (f: { outcome?: string; tool?: string; limit?: number; offset?: number } = {}) => {
  const p = new URLSearchParams()
  if (f.outcome) p.set('outcome', f.outcome)
  if (f.tool)    p.set('tool', f.tool)
  if (f.limit)   p.set('limit', String(f.limit))
  if (f.offset)  p.set('offset', String(f.offset))
  const qs = p.toString()
  return json<{ runs: RunRecord[]; total: number }>(`/api/runs${qs ? '?' + qs : ''}`)
}

export const fetchRunStats = (days = 7) => json<RunStats>(`/api/runs/stats?days=${days}`)
