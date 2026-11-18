export interface Session {
  id: number
  created_at: number
  message_count?: number
  title?: string
  preview?: string
  last_at?: number
}

export interface Message {
  id: number
  session_id: number
  ts: number
  role: 'user' | 'assistant' | 'tool' | 'raw_model_json'
  content: string
}

export interface SkillParam {
  type: string
  description: string
  required?: boolean
}

export interface Skill {
  name: string
  description: string
  parameters: Record<string, SkillParam>
  body?: string
  kind?: 'builtin' | 'markdown' | 'remote'
  requires_approval?: boolean
}

export interface SkillValidateResult {
  ok: boolean
  skill?: Skill
  error?: string
}

export interface SkillSaveResult {
  ok: boolean
  skill: Skill
  created: boolean
}

export interface SchedulerTask {
  id: number
  name: string
  cron_expr: string
  skill: string
  args: Record<string, unknown>
  notify_telegram_chat_id: number | null
  one_shot: boolean
  enabled: boolean
  created_at: number
  last_run_at: number | null
  last_result: string | null
  run_count: number
  timezone: string
  prompt?: string
  max_retries: number
  retry_delay_sec: number
  retry_attempt: number
  next_retry_at: number | null
}

export interface CreateTaskRequest {
  name: string
  cron_expr: string
  skill?: string
  prompt?: string
  args?: Record<string, unknown>
  notify_telegram_chat_id?: number | null
  one_shot?: boolean
  timezone?: string
  max_retries?: number
  retry_delay_sec?: number
}

export interface TaskRun {
  id: number
  task_id: number
  trigger: 'cron' | 'retry' | 'manual'
  attempt: number
  started_at: number
  finished_at: number
  ok: boolean
  result: string
}

export interface MemoryHit {
  id: string
  text: string
  metadata: Record<string, unknown>
  distance?: number
}

export interface MemoryAddResult {
  ok: boolean
  memory_id: string
  skipped: boolean
  reason?: string
}

export interface MemoryNamespace {
  namespace: string
  count: number
}

export interface MemoryUpdateRequest {
  text?: string
  meta?: Record<string, string | null>
  ttl_seconds?: number
}

export interface ImportReport {
  imported: number
  duplicates: number
  existing: number
  failed: number
  errors?: string[]
}

export interface UsageBucket {
  start: number
  queries: number
  tool_calls: number
  tool_errors: number
  avg_tool_ms: number
}

export interface SkillUsage {
  skill: string
  calls: number
  errors: number
  avg_ms: number
}

export interface UsageStats {
  since: number
  bucket: 'hour' | 'day'
  buckets: UsageBucket[]
  skills: SkillUsage[]
}

export interface HealthStatus {
  ok: boolean
  version?: string
  provider: string
  chat_model: string
  chat_base: string
  skills_dir: string
  scheduler_db: string
  embed_model: string
  embed_provider?: string
  embed_ok: boolean
  embed_error?: string
}

export interface QueryRequest {
  query: string
  session_id?: number
  remember?: boolean
  max_steps?: number
}

export interface QueryResult {
  session_id: number
  answer: string
  meta: {
    step_count: number
    model: string
    timings: Record<string, number>
  }
  memories?: MemoryHit[]
  debug_log?: string
}

export interface Invocation {
  id: number
  session_id: number
  step: number
  ts: number
  skill: string
  kind: 'skill' | 'tool' | 'unknown' | 'replay'
  args: Record<string, unknown> | null
  result?: unknown
  error?: string
  duration_ms: number
  why?: string
  replayed_from?: number
}

export interface InvocationListResult {
  invocations: Invocation[]
  total: number
  limit: number
  offset: number
}

export interface InvocationFilter {
  skill?: string
  session_id?: number
  only_errors?: boolean
  limit?: number
  offset?: number
}

export interface ReplayResult {
  replayed_from: number
  skill: string
  result: unknown
  duration_ms: number
  error: string
}

export interface SkillAttachment {
  type: string
  filename: string
  mime_type: string
  base64: string
}

export interface SkillInvokeResult {
  name: string
  args: Record<string, unknown>
  result: unknown
  elapsed_s: number
  error: string | null
  attachments: SkillAttachment[] | null
}

// SSE event payloads
export interface ThinkingEvent  { step: number }
export interface ToolCallEvent  { step: number; tool: string; args: Record<string, unknown>; why: string }
export interface ToolResultEvent { step: number; tool: string; result: string; elapsed_s: number }
export interface FinalEvent     { session_id: number; answer: string; meta: QueryResult['meta'] }
// The gateway sends {"error": "..."}; errors raised client-side in stream.ts
// (401, network failure) use {"message": "..."}.
export interface ErrorEvent     { error?: string; message?: string }

export interface ApprovalRequest {
  id: string
  session_id: number
  step: number
  tool: string
  args: Record<string, unknown>
  why?: string
  created_at: string
  expires_at: string
}
export interface ApprovalDecision { approved: boolean; by?: string; reason?: string; scope?: string }

export interface ApprovalRecord {
  id: number
  ts: number
  session_id: number
  step: number
  tool: string
  args: unknown
  why?: string
  approved: boolean
  by: string
  reason?: string
  scope?: string
  wait_ms: number
}

export interface ApprovalGrant { session_id: number; tool: string; expires_at: string }

export interface RunRecord {
  id: number
  session_id: number
  ts: number
  duration_ms: number
  query: string
  namespace?: string
  model: string
  tool_mode: string
  retrieval: string
  offered: string[]
  used: string[]
  steps: number
  llm_calls: number
  llm_ms: number
  tool_ms: number
  prompt_tokens: number
  completion_tokens: number
  approval_wait_ms: number
  unattended?: boolean
  outcome: 'final' | 'fallback' | 'max_steps' | 'error' | 'cancelled'
  error?: string
}

export interface ToolRunStats { tool: string; offered: number; used: number; used_not_offered: number }

export interface RunStats {
  since: number
  runs: number
  outcomes: Record<string, number>
  avg_duration_ms: number
  p95_duration_ms: number
  avg_llm_calls: number
  prompt_tokens: number
  completion_tokens: number
  approval_wait_ms: number
  retrieval_runs: number
  miss_rate: number
  tools: ToolRunStats[]
}
export interface ApprovalEvent {
  step: number
  tool: string
  approval: ApprovalRequest
  decision?: ApprovalDecision
}

export type StreamEvent =
  | { event: 'thinking';    data: ThinkingEvent }
  | { event: 'tool_call';   data: ToolCallEvent }
  | { event: 'tool_result'; data: ToolResultEvent }
  | { event: 'final';       data: FinalEvent }
  | { event: 'error';       data: ErrorEvent }
  | { event: 'approval_required'; data: ApprovalEvent }
  | { event: 'approval_resolved'; data: ApprovalEvent }
