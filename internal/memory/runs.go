package memory

import (
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// Run traces and the approval log.
//
// skill_invocations records what each tool did; these two tables record the
// run around it — which tools the model was offered and which it used, how
// many LLM calls and tokens it took, how long it waited for a human — and
// every approval decision, including the ones an allow-rule or a session
// grant made without asking.

const traceSchema = `
CREATE TABLE IF NOT EXISTS agent_runs (
	id                INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id        INTEGER NOT NULL,
	ts                REAL    NOT NULL,
	duration_ms       INTEGER NOT NULL,
	query             TEXT    NOT NULL,
	namespace         TEXT    NOT NULL DEFAULT '',
	model             TEXT    NOT NULL DEFAULT '',
	tool_mode         TEXT    NOT NULL DEFAULT '',
	retrieval         TEXT    NOT NULL DEFAULT '',
	offered_json      TEXT    NOT NULL DEFAULT '[]',
	used_json         TEXT    NOT NULL DEFAULT '[]',
	steps             INTEGER NOT NULL DEFAULT 0,
	llm_calls         INTEGER NOT NULL DEFAULT 0,
	llm_ms            INTEGER NOT NULL DEFAULT 0,
	tool_ms           INTEGER NOT NULL DEFAULT 0,
	prompt_tokens     INTEGER NOT NULL DEFAULT 0,
	completion_tokens INTEGER NOT NULL DEFAULT 0,
	approval_wait_ms  INTEGER NOT NULL DEFAULT 0,
	unattended        INTEGER NOT NULL DEFAULT 0,
	outcome           TEXT    NOT NULL,
	error_msg         TEXT
);
CREATE INDEX IF NOT EXISTS idx_runs_ts      ON agent_runs(ts);
CREATE INDEX IF NOT EXISTS idx_runs_session ON agent_runs(session_id, ts);
CREATE TABLE IF NOT EXISTS approval_log (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	ts         REAL    NOT NULL,
	session_id INTEGER NOT NULL,
	step       INTEGER NOT NULL,
	tool       TEXT    NOT NULL,
	args_json  TEXT    NOT NULL,
	why        TEXT,
	approved   INTEGER NOT NULL,
	decided_by TEXT    NOT NULL,
	reason     TEXT,
	scope      TEXT,
	wait_ms    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_approval_ts   ON approval_log(ts);
CREATE INDEX IF NOT EXISTS idx_approval_tool ON approval_log(tool, ts);
`

// runRetention is how long run traces are kept (they are diagnostics; the
// audit log and the approval log are kept for good).
const runRetention = 90 * 24 * time.Hour

func initTraceTables(db *sql.DB) error {
	if _, err := db.Exec(traceSchema); err != nil {
		return err
	}
	_, _ = db.Exec(`DELETE FROM agent_runs WHERE ts < ?`, nowSec()-runRetention.Seconds())
	return nil
}

func nowSec() float64 { return float64(time.Now().UnixMilli()) / 1000.0 }

// RunRecord is one agent run.
type RunRecord struct {
	ID               int64    `json:"id"`
	SessionID        int      `json:"session_id"`
	TS               float64  `json:"ts"` // start, unix seconds
	DurationMS       int64    `json:"duration_ms"`
	Query            string   `json:"query"`
	Namespace        string   `json:"namespace,omitempty"`
	Model            string   `json:"model"`
	ToolMode         string   `json:"tool_mode"`
	Retrieval        string   `json:"retrieval"` // embed+lexical | lexical | off
	Offered          []string `json:"offered"`   // tools listed in the prompt (empty when retrieval is off)
	Used             []string `json:"used"`      // tools called, in order
	Steps            int      `json:"steps"`
	LLMCalls         int      `json:"llm_calls"`
	LLMMS            int64    `json:"llm_ms"`
	ToolMS           int64    `json:"tool_ms"`
	PromptTokens     int64    `json:"prompt_tokens"`
	CompletionTokens int64    `json:"completion_tokens"`
	ApprovalWaitMS   int64    `json:"approval_wait_ms"`
	Unattended       bool     `json:"unattended,omitempty"`
	Outcome          string   `json:"outcome"` // final | fallback | max_steps | error | cancelled
	Error            string   `json:"error,omitempty"`
}

// RunFilter selects runs for ListRuns.
type RunFilter struct {
	SessionID int
	Outcome   string
	Tool      string // runs that used this tool
	Since     float64
	Limit     int
	Offset    int
}

// AddRun stores a run trace (best effort; errors are for tests).
func (c *Client) AddRun(r RunRecord) (int64, error) {
	if r.TS == 0 {
		r.TS = nowSec()
	}
	if len(r.Query) > 2000 {
		r.Query = safeTruncateUTF8(r.Query, 2000) + "…"
	}
	offered, _ := json.Marshal(nonNilStrings(r.Offered))
	used, _ := json.Marshal(nonNilStrings(r.Used))
	var errMsg *string
	if r.Error != "" {
		e := safeTruncateUTF8(r.Error, 1000)
		errMsg = &e
	}
	res, err := c.hist.db.Exec(`INSERT INTO agent_runs
		(session_id,ts,duration_ms,query,namespace,model,tool_mode,retrieval,offered_json,used_json,
		 steps,llm_calls,llm_ms,tool_ms,prompt_tokens,completion_tokens,approval_wait_ms,unattended,outcome,error_msg)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.SessionID, r.TS, r.DurationMS, r.Query, r.Namespace, r.Model, r.ToolMode, r.Retrieval,
		string(offered), string(used), r.Steps, r.LLMCalls, r.LLMMS, r.ToolMS,
		r.PromptTokens, r.CompletionTokens, r.ApprovalWaitMS, r.Unattended, r.Outcome, errMsg)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const runCols = `id,session_id,ts,duration_ms,query,namespace,model,tool_mode,retrieval,offered_json,used_json,
	steps,llm_calls,llm_ms,tool_ms,prompt_tokens,completion_tokens,approval_wait_ms,unattended,outcome,error_msg`

func scanRun(scan func(...any) error) (RunRecord, error) {
	var r RunRecord
	var offered, used string
	var errMsg sql.NullString
	err := scan(&r.ID, &r.SessionID, &r.TS, &r.DurationMS, &r.Query, &r.Namespace, &r.Model, &r.ToolMode,
		&r.Retrieval, &offered, &used, &r.Steps, &r.LLMCalls, &r.LLMMS, &r.ToolMS, &r.PromptTokens,
		&r.CompletionTokens, &r.ApprovalWaitMS, &r.Unattended, &r.Outcome, &errMsg)
	if err != nil {
		return r, err
	}
	_ = json.Unmarshal([]byte(offered), &r.Offered)
	_ = json.Unmarshal([]byte(used), &r.Used)
	r.Offered, r.Used = nonNilStrings(r.Offered), nonNilStrings(r.Used)
	r.Error = errMsg.String
	return r, nil
}

func runWhere(f RunFilter) (string, []any) {
	var cl []string
	var args []any
	if f.SessionID > 0 {
		cl, args = append(cl, "session_id=?"), append(args, f.SessionID)
	}
	if f.Outcome != "" {
		cl, args = append(cl, "outcome=?"), append(args, f.Outcome)
	}
	if f.Tool != "" {
		// used_json is a JSON array of names; match the quoted element.
		b, _ := json.Marshal(f.Tool)
		cl, args = append(cl, "instr(used_json, ?) > 0"), append(args, string(b))
	}
	if f.Since > 0 {
		cl, args = append(cl, "ts>=?"), append(args, f.Since)
	}
	if len(cl) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(cl, " AND "), args
}

// ListRuns returns a page of runs, newest first, and the total count.
func (c *Client) ListRuns(f RunFilter) ([]RunRecord, int, error) {
	where, args := runWhere(f)
	var total int
	if err := c.hist.db.QueryRow(`SELECT COUNT(*) FROM agent_runs`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := c.hist.db.Query(`SELECT `+runCols+` FROM agent_runs`+where+` ORDER BY ts DESC LIMIT ? OFFSET ?`,
		append(args, limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []RunRecord{}
	for rows.Next() {
		r, err := scanRun(rows.Scan)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// GetRun returns one run, or nil.
func (c *Client) GetRun(id int64) (*RunRecord, error) {
	r, err := scanRun(c.hist.db.QueryRow(`SELECT `+runCols+` FROM agent_runs WHERE id=?`, id).Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ToolRunStats is how one tool fared across runs with tool retrieval on.
type ToolRunStats struct {
	Tool    string `json:"tool"`
	Offered int    `json:"offered"` // runs whose prompt listed it
	Used    int    `json:"used"`    // runs that called it
	// UsedNotOffered counts runs that called it although the prompt did not
	// list it (found through find_tools, or called by name): a sign that
	// retrieval ranks it too low for the queries that need it.
	UsedNotOffered int `json:"used_not_offered"`
}

// RunStats summarises the runs since a time.
type RunStats struct {
	Since            float64        `json:"since"`
	Runs             int            `json:"runs"`
	Outcomes         map[string]int `json:"outcomes"`
	AvgDurationMS    float64        `json:"avg_duration_ms"`
	P95DurationMS    int64          `json:"p95_duration_ms"`
	AvgLLMCalls      float64        `json:"avg_llm_calls"`
	PromptTokens     int64          `json:"prompt_tokens"`
	CompletionTokens int64          `json:"completion_tokens"`
	ApprovalWaitMS   int64          `json:"approval_wait_ms"`
	RetrievalRuns    int            `json:"retrieval_runs"`
	// MissRate is the share of retrieval runs that used at least one tool
	// the prompt had not listed.
	MissRate float64        `json:"miss_rate"`
	Tools    []ToolRunStats `json:"tools"`
}

// RunStats aggregates the (at most 20 000 most recent) runs since since.
func (c *Client) RunStats(since float64) (*RunStats, error) {
	rows, err := c.hist.db.Query(`SELECT `+runCols+` FROM agent_runs WHERE ts >= ? ORDER BY ts DESC LIMIT 20000`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	st := &RunStats{Since: since, Outcomes: map[string]int{}, Tools: []ToolRunStats{}}
	per := map[string]*ToolRunStats{}
	tool := func(n string) *ToolRunStats {
		t := per[n]
		if t == nil {
			t = &ToolRunStats{Tool: n}
			per[n] = t
		}
		return t
	}
	var durs []int64
	var llmCalls, missed int
	for rows.Next() {
		r, err := scanRun(rows.Scan)
		if err != nil {
			return nil, err
		}
		st.Runs++
		st.Outcomes[r.Outcome]++
		durs = append(durs, r.DurationMS)
		llmCalls += r.LLMCalls
		st.PromptTokens += r.PromptTokens
		st.CompletionTokens += r.CompletionTokens
		st.ApprovalWaitMS += r.ApprovalWaitMS
		retrieval := r.Retrieval != "" && r.Retrieval != "off" && len(r.Offered) > 0
		offered := map[string]bool{}
		if retrieval {
			st.RetrievalRuns++
			for _, n := range r.Offered {
				offered[n] = true
				tool(n).Offered++
			}
		}
		seen := map[string]bool{}
		miss := false
		for _, n := range r.Used {
			if seen[n] {
				continue
			}
			seen[n] = true
			t := tool(n)
			t.Used++
			if retrieval && !offered[n] {
				t.UsedNotOffered++
				miss = true
			}
		}
		if miss {
			missed++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if st.Runs > 0 {
		var sum int64
		for _, d := range durs {
			sum += d
		}
		st.AvgDurationMS = float64(sum) / float64(st.Runs)
		st.AvgLLMCalls = float64(llmCalls) / float64(st.Runs)
		sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
		st.P95DurationMS = durs[(len(durs)*95+99)/100-1] // nearest-rank
	}
	if st.RetrievalRuns > 0 {
		st.MissRate = float64(missed) / float64(st.RetrievalRuns)
	}
	for _, t := range per {
		st.Tools = append(st.Tools, *t)
	}
	sort.Slice(st.Tools, func(i, j int) bool {
		a, b := st.Tools[i], st.Tools[j]
		if a.UsedNotOffered != b.UsedNotOffered {
			return a.UsedNotOffered > b.UsedNotOffered
		}
		if a.Used != b.Used {
			return a.Used > b.Used
		}
		return a.Tool < b.Tool
	})
	return st, nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ApprovalRecord is one approval decision.
type ApprovalRecord struct {
	ID        int64   `json:"id"`
	TS        float64 `json:"ts"`
	SessionID int     `json:"session_id"`
	Step      int     `json:"step"`
	Tool      string  `json:"tool"`
	Args      any     `json:"args"`
	Why       string  `json:"why,omitempty"`
	Approved  bool    `json:"approved"`
	By        string  `json:"by"` // web, telegram:<id>, cli, api, rule, session, policy, timeout, cancelled
	Reason    string  `json:"reason,omitempty"`
	Scope     string  `json:"scope,omitempty"`
	WaitMS    int64   `json:"wait_ms"`
}

// ApprovalFilter selects rows for ListApprovals.
type ApprovalFilter struct {
	SessionID  int
	Tool       string
	OnlyDenied bool
	By         string
	Limit      int
	Offset     int
}

// AddApproval appends a decision to the approval log.
func (c *Client) AddApproval(r ApprovalRecord) error {
	if r.TS == 0 {
		r.TS = nowSec()
	}
	max := c.auditMaxBlob
	if max <= 0 || max > 4096 {
		max = 4096
	}
	_, err := c.hist.db.Exec(`INSERT INTO approval_log
		(ts,session_id,step,tool,args_json,why,approved,decided_by,reason,scope,wait_ms)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		r.TS, r.SessionID, r.Step, r.Tool, marshalAuditBlob(r.Args, max), nullStr(r.Why),
		r.Approved, r.By, nullStr(r.Reason), nullStr(r.Scope), r.WaitMS)
	return err
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ListApprovals returns a page of decisions, newest first, and the total.
func (c *Client) ListApprovals(f ApprovalFilter) ([]ApprovalRecord, int, error) {
	var cl []string
	var args []any
	if f.SessionID > 0 {
		cl, args = append(cl, "session_id=?"), append(args, f.SessionID)
	}
	if f.Tool != "" {
		cl, args = append(cl, "tool=?"), append(args, f.Tool)
	}
	if f.OnlyDenied {
		cl = append(cl, "approved=0")
	}
	if f.By != "" {
		cl, args = append(cl, "decided_by=?"), append(args, f.By)
	}
	where := ""
	if len(cl) > 0 {
		where = " WHERE " + strings.Join(cl, " AND ")
	}
	var total int
	if err := c.hist.db.QueryRow(`SELECT COUNT(*) FROM approval_log`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := c.hist.db.Query(`SELECT id,ts,session_id,step,tool,args_json,why,approved,decided_by,reason,scope,wait_ms
		FROM approval_log`+where+` ORDER BY ts DESC, id DESC LIMIT ? OFFSET ?`, append(args, limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []ApprovalRecord{}
	for rows.Next() {
		var r ApprovalRecord
		var argsJSON string
		var why, reason, scope sql.NullString
		if err := rows.Scan(&r.ID, &r.TS, &r.SessionID, &r.Step, &r.Tool, &argsJSON, &why,
			&r.Approved, &r.By, &reason, &scope, &r.WaitMS); err != nil {
			return nil, 0, err
		}
		r.Args = unmarshalAuditBlob(argsJSON)
		r.Why, r.Reason, r.Scope = why.String, reason.String, scope.String
		out = append(out, r)
	}
	return out, total, rows.Err()
}
